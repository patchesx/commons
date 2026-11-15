package adminui

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	slackpkg "commons/integrations/slack"
	"commons/permissions"
	"commons/plugin"
	"commons/store"
	"commons/util"
	"commons/web"
	admintempl "commons/web/templ"
)

const (
	broadcastHistoryLimit = 100
	// broadcastLocalLayout is the value format of <input type="datetime-local">.
	broadcastLocalLayout = "2006-01-02T15:04"
)

// holdsPermission reports whether the signed-in user holds a permission. /admin/* only requires a session, so
// handlers check their own.
func (d Deps) holdsPermission(r *http.Request, key string) bool {
	ctx := r.Context()
	perms, err := store.GetUserPermissions(ctx, d.Pool, web.UserIDFromContext(ctx))
	if err != nil {
		return false
	}
	for _, p := range perms {
		if p == key {
			return true
		}
	}
	return false
}

// canBroadcast reports whether the signed-in user holds broadcast.send.
func (d Deps) canBroadcast(r *http.Request) bool {
	return d.holdsPermission(r, permissions.BroadcastSend)
}

// orgLocation is the organization's default timezone, which broadcast times are entered and shown in.
func (d Deps) orgLocation(r *http.Request) *time.Location {
	name := util.DefaultTimezone(r.Context(), d.Pool, d.EncKey)
	loc, err := time.LoadLocation(name)
	if err != nil {
		return util.FallbackLocation()
	}
	return loc
}

func inLocation(t *time.Time, loc *time.Location) *time.Time {
	if t == nil {
		return nil
	}
	l := t.In(loc)
	return &l
}

func toBroadcastView(b *slackpkg.Broadcast, loc *time.Location) admintempl.BroadcastView {
	return admintempl.BroadcastView{
		ID:           b.ID,
		AuthorName:   b.AuthorName,
		Body:         b.Body,
		Sending:      b.Status == slackpkg.BroadcastSending,
		Scheduled:    b.Status == slackpkg.BroadcastScheduled,
		Cancelled:    b.Status == slackpkg.BroadcastCancelled,
		CreatedAt:    b.CreatedAt.In(loc),
		CompletedAt:  inLocation(b.CompletedAt, loc),
		ScheduledFor: inLocation(b.ScheduledFor, loc),
		Total:        b.Total,
		Sent:         b.Sent,
		Failed:       b.Failed,
		Pending:      b.Pending,
	}
}

// BroadcastsPage lists past broadcasts. broadcast.send.
func (d Deps) BroadcastsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.canBroadcast(r) {
			forbidden(w)
			return
		}
		list, err := slackpkg.ListBroadcasts(r.Context(), d.Pool, broadcastHistoryLimit)
		if err != nil {
			http.Error(w, "failed to load broadcasts", http.StatusInternalServerError)
			return
		}
		loc := d.orgLocation(r)
		views := make([]admintempl.BroadcastView, len(list))
		for i := range list {
			views[i] = toBroadcastView(&list[i], loc)
		}
		admintempl.BroadcastsPage(views).Render(r.Context(), w)
	}
}

func (d Deps) renderBroadcastNew(w http.ResponseWriter, r *http.Request, body, errMsg string) {
	ctx := r.Context()
	channels, users, _, _ := loadActionFormDeps(ctx, d.Pool)
	userMap := make(map[string]string, len(users))
	for _, u := range users {
		if u.SlackID != "" {
			userMap[u.SlackID] = u.DisplayName
		}
	}
	recipients, err := slackpkg.CountBroadcastRecipients(ctx, d.Pool)
	if err != nil {
		http.Error(w, "failed to count recipients", http.StatusInternalServerError)
		return
	}
	opts := admintempl.SlackComposerOpts{
		FieldName:  "body",
		InitValue:  body,
		UserMap:    userMap,
		Channels:   channels,
		Standalone: true,
		PipelineVars: []plugin.DataFieldDef{
			{Key: "member_name", Label: "Member name", Description: "The recipient's display name"},
		},
	}
	loc := d.orgLocation(r)
	schedule := admintempl.BroadcastScheduleOpts{
		Timezone: loc.String(),
		Min:      time.Now().In(loc).Format(broadcastLocalLayout),
	}
	admintempl.BroadcastNewPage(opts, recipients, slackpkg.BroadcastConfigured(ctx), errMsg, schedule).Render(ctx, w)
}

// BroadcastNewPage shows the composer. broadcast.send.
func (d Deps) BroadcastNewPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.canBroadcast(r) {
			forbidden(w)
			return
		}
		d.renderBroadcastNew(w, r, "", "")
	}
}

// BroadcastSend records the broadcast under the signed-in user. With mode "schedule" it waits for send_at,
// read in the organization's timezone; otherwise it starts sending in the background. broadcast.send.
func (d Deps) BroadcastSend() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.canBroadcast(r) {
			forbidden(w)
			return
		}
		ctx := r.Context()
		body := strings.TrimSpace(r.FormValue("body"))
		if body == "" {
			d.renderBroadcastNew(w, r, "", "Write a message first.")
			return
		}
		var scheduledFor *time.Time
		if r.FormValue("mode") == "schedule" {
			at, msg := parseBroadcastTime(r.FormValue("send_at"), d.orgLocation(r), time.Now())
			if msg != "" {
				d.renderBroadcastNew(w, r, body, msg)
				return
			}
			scheduledFor = &at
		}
		if !slackpkg.BroadcastConfigured(ctx) {
			d.renderBroadcastNew(w, r, body, "Slack is not connected.")
			return
		}
		userID := web.UserIDFromContext(ctx)
		author, err := store.GetUserByID(ctx, d.Pool, userID)
		if err != nil {
			http.Error(w, "failed to load your account", http.StatusInternalServerError)
			return
		}
		id, err := slackpkg.CreateBroadcast(ctx, d.Pool, userID, author.DisplayName, body, scheduledFor)
		if err != nil {
			http.Error(w, "failed to create broadcast", http.StatusInternalServerError)
			return
		}
		if scheduledFor == nil {
			slackpkg.StartBroadcast(d.Pool, id)
		}
		http.Redirect(w, r, "/admin/broadcasts/"+id, http.StatusSeeOther)
	}
}

func (d Deps) loadBroadcast(w http.ResponseWriter, r *http.Request) (admintempl.BroadcastView, []admintempl.BroadcastRecipientView, bool) {
	ctx := r.Context()
	id := r.PathValue("id")
	b, err := slackpkg.GetBroadcast(ctx, d.Pool, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "failed to load broadcast", http.StatusInternalServerError)
		}
		return admintempl.BroadcastView{}, nil, false
	}
	recs, err := slackpkg.ListBroadcastRecipients(ctx, d.Pool, id)
	if err != nil {
		http.Error(w, "failed to load recipients", http.StatusInternalServerError)
		return admintempl.BroadcastView{}, nil, false
	}
	views := make([]admintempl.BroadcastRecipientView, len(recs))
	for i, rec := range recs {
		views[i] = admintempl.BroadcastRecipientView{
			DisplayName: rec.DisplayName, Status: rec.Status, Error: rec.Error, SentAt: rec.SentAt,
		}
	}
	return toBroadcastView(b, d.orgLocation(r)), views, true
}

// BroadcastDetailPage shows one broadcast and each member's result. broadcast.send.
func (d Deps) BroadcastDetailPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.canBroadcast(r) {
			forbidden(w)
			return
		}
		if b, recs, ok := d.loadBroadcast(w, r); ok {
			admintempl.BroadcastDetailPage(b, recs).Render(r.Context(), w)
		}
	}
}

// BroadcastDetailFragment is the polled body of the detail page. broadcast.send.
func (d Deps) BroadcastDetailFragment() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.canBroadcast(r) {
			forbidden(w)
			return
		}
		if b, recs, ok := d.loadBroadcast(w, r); ok {
			admintempl.BroadcastDetail(b, recs).Render(r.Context(), w)
		}
	}
}

// BroadcastRetry sends again to the members whose DM failed. broadcast.send.
func (d Deps) BroadcastRetry() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.canBroadcast(r) {
			forbidden(w)
			return
		}
		id := r.PathValue("id")
		n, err := slackpkg.ResetFailedBroadcastRecipients(r.Context(), d.Pool, id)
		if err != nil {
			http.Error(w, "failed to retry broadcast", http.StatusInternalServerError)
			return
		}
		if n > 0 {
			slackpkg.StartBroadcast(d.Pool, id)
		}
		http.Redirect(w, r, "/admin/broadcasts/"+id, http.StatusSeeOther)
	}
}

// BroadcastCancel cancels a scheduled broadcast that has not started. broadcast.send.
func (d Deps) BroadcastCancel() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.canBroadcast(r) {
			forbidden(w)
			return
		}
		id := r.PathValue("id")
		if _, err := slackpkg.CancelScheduledBroadcast(r.Context(), d.Pool, id); err != nil {
			http.Error(w, "failed to cancel broadcast", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/broadcasts/"+id, http.StatusSeeOther)
	}
}

// parseBroadcastTime reads a datetime-local value as a time in the organization's timezone. A non-empty
// message says why it can't be used.
func parseBroadcastTime(value string, loc *time.Location, now time.Time) (time.Time, string) {
	at, err := time.ParseInLocation(broadcastLocalLayout, value, loc)
	if err != nil {
		return time.Time{}, "Pick a date and time to schedule the message."
	}
	if !at.After(now) {
		return time.Time{}, "The scheduled time must be in the future."
	}
	return at, ""
}
