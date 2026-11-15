package adminui

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	slackpkg "commons/integrations/slack"
	"commons/permissions"
	admintempl "commons/web/templ"
)

const pollHistoryLimit = 100

// canPoll reports whether the signed-in user holds poll.create.
func (d Deps) canPoll(r *http.Request) bool {
	return d.holdsPermission(r, permissions.PollCreate)
}

func toPollView(p *slackpkg.Poll, loc *time.Location) admintempl.PollView {
	v := admintempl.PollView{
		ID:          p.ID,
		Question:    p.Question,
		CreatorName: p.CreatorName,
		Audience:    p.Audience,
		ChannelID:   p.ChannelID,
		Status:      p.Status,
		CreatedAt:   p.CreatedAt.In(loc),
		ClosesAt:    p.ClosesAt.In(loc),
		ClosedAt:    inLocation(p.ClosedAt, loc),
		Voters:      p.Voters,
		Messages:    p.Messages,
		Posted:      p.Posted,
		Failed:      p.Failed,
	}
	total := 0
	for _, o := range p.Options {
		total += o.Votes
	}
	for _, o := range p.Options {
		emoji := slackpkg.EmojiCharacter(o.Emoji)
		if emoji == "" {
			emoji = ":" + o.Emoji + ":"
		}
		percent := 0
		if total > 0 {
			percent = (o.Votes*100 + total/2) / total
		}
		v.Options = append(v.Options, admintempl.PollOptionView{Emoji: emoji, Label: o.Label, Votes: o.Votes, Percent: percent})
	}
	return v
}

// PollsPage lists past and current polls. poll.create.
func (d Deps) PollsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.canPoll(r) {
			forbidden(w)
			return
		}
		list, err := slackpkg.ListPolls(r.Context(), d.Pool, pollHistoryLimit)
		if err != nil {
			http.Error(w, "failed to load polls", http.StatusInternalServerError)
			return
		}
		loc := d.orgLocation(r)
		views := make([]admintempl.PollView, len(list))
		for i := range list {
			views[i] = toPollView(&list[i], loc)
		}
		admintempl.PollsPage(views).Render(r.Context(), w)
	}
}

// PollDetailPage shows one poll and, once it has closed, its results. It shows how many members picked more
// than one option but never who voted for what. poll.create.
func (d Deps) PollDetailPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.canPoll(r) {
			forbidden(w)
			return
		}
		ctx := r.Context()
		id := r.PathValue("id")
		p, err := slackpkg.GetPoll(ctx, d.Pool, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.NotFound(w, r)
			} else {
				http.Error(w, "failed to load poll", http.StatusInternalServerError)
			}
			return
		}
		view := toPollView(p, d.orgLocation(r))
		if p.Status == slackpkg.PollClosed {
			votes, err := slackpkg.ListPollVotes(ctx, d.Pool, id)
			if err != nil {
				http.Error(w, "failed to load results", http.StatusInternalServerError)
				return
			}
			view.MultiVoters = slackpkg.MultiVoterCount(votes)
		}
		admintempl.PollDetailPage(view).Render(ctx, w)
	}
}
