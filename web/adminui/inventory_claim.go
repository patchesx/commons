package adminui

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"commons/store"
	admintempl "commons/web/templ"
)

const (
	defaultClaimsPerIPPerHour = 10 // overridden by inventory.claim_rate_limit_per_hour
	claimsPerCodePerHour      = 20
	claimWindow               = time.Hour
	maxClaimNameLen           = 200
	maxClaimBodyBytes         = 16 << 10
)

// The inventory claim page is public: the QR code on an item's label opens it, so there is no session.
// It never shows member data, and a visitor sees the same confirmation whether or not their email matches
// a member, so the page can't be used to find out who is a member.

func claimClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// claimsPerIP is the per-IP submission limit per hour from config, or the default.
func (d Deps) claimsPerIP(ctx context.Context) int {
	if v, err := store.GetServiceConfig(ctx, d.Pool, "inventory", "claim_rate_limit_per_hour", d.EncKey); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return defaultClaimsPerIPPerHour
}

func writeClaimPage(w http.ResponseWriter, r *http.Request, status int, data admintempl.InventoryClaimPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	admintempl.InventoryClaimPage(data).Render(r.Context(), w)
}

func writeClaimNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(http.StatusNotFound)
	admintempl.InventoryClaimNotFoundPage().Render(r.Context(), w)
}

func newClaimPageData(item *store.InventoryItem) admintempl.InventoryClaimPageData {
	return admintempl.InventoryClaimPageData{
		Action:   "/inventory/" + item.Code,
		ItemName: item.Name,
		Code:     store.FormatInventoryCode(item.Code),
	}
}

// InventoryClaimPage shows the check-in form for the item with the code in the path.
func (d Deps) InventoryClaimPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		item, err := store.GetInventoryItemByCode(r.Context(), d.Pool, r.PathValue("code"))
		if errors.Is(err, store.ErrNotFound) {
			writeClaimNotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
			return
		}
		writeClaimPage(w, r, http.StatusOK, newClaimPageData(item))
	}
}

func validClaimEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s
}

// InventoryClaimSubmit records a check-in. A matching member takes possession right away; otherwise the claim
// is held as pending, the holder is unchanged, and inventory admins are told so they can link it.
func (d Deps) InventoryClaimSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		r.Body = http.MaxBytesReader(w, r.Body, maxClaimBodyBytes)

		ip := claimClientIP(r)
		tooMany := func() {
			http.Error(w, "Too many submissions. Please try again later.", http.StatusTooManyRequests)
		}
		if d.ClaimLimiter != nil && !d.ClaimLimiter.Allow("claim-ip:"+ip, d.claimsPerIP(ctx), claimWindow) {
			tooMany()
			return
		}
		item, err := store.GetInventoryItemByCode(ctx, d.Pool, r.PathValue("code"))
		if errors.Is(err, store.ErrNotFound) {
			writeClaimNotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
			return
		}
		if d.ClaimLimiter != nil && !d.ClaimLimiter.Allow("claim-code:"+item.Code, claimsPerCodePerHour, claimWindow) {
			tooMany()
			return
		}

		data := newClaimPageData(item)
		if r.FormValue("website") != "" {
			// The hidden field is for bots. Look like a success and record nothing.
			data.Done = true
			writeClaimPage(w, r, http.StatusOK, data)
			return
		}
		data.Name = strings.TrimSpace(r.FormValue("name"))
		data.Email = strings.TrimSpace(r.FormValue("email"))
		switch {
		case data.Name == "" || utf8.RuneCountInString(data.Name) > maxClaimNameLen:
			data.Error = "Please enter your name."
		case !validClaimEmail(data.Email):
			data.Error = "Please enter a valid email address."
		case r.FormValue("confirm") == "":
			data.Error = "Please tick the box to confirm you are taking this item."
		}
		if data.Error != "" {
			writeClaimPage(w, r, http.StatusBadRequest, data)
			return
		}

		ua := r.UserAgent()
		if len(ua) > 500 {
			ua = ua[:500]
		}
		fail := func(err error) {
			if errors.Is(err, store.ErrInventoryItemArchived) || errors.Is(err, store.ErrNotFound) {
				writeClaimNotFound(w, r)
				return
			}
			log.Printf("inventory: claim of item %s: %v", item.ID, err)
			http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
		}

		user, err := store.FindUserForClaimEmail(ctx, d.Pool, data.Email)
		switch {
		case err == nil:
			_, err := store.ApplyCustodyChange(ctx, d.Pool, store.CustodyChange{
				ItemID: item.ID, UserID: user.ID, Source: "web_form",
				RawName: &data.Name, RawEmail: &data.Email, IPAddress: &ip, UserAgent: &ua,
			})
			// Already holding it is a no-op, and looks the same as success.
			if err != nil && !errors.Is(err, store.ErrInventoryAlreadyHolder) {
				fail(err)
				return
			}
			// The member hears about it; if it wasn't them, that is how they find out.
			if err == nil && d.NotifyClaimApplied != nil {
				go d.NotifyClaimApplied(context.Background(), item.ID, user.ID)
			}
		case errors.Is(err, store.ErrNotFound):
			ev, err := store.RecordPendingClaim(ctx, d.Pool, item.ID, data.Name, data.Email, &ip, &ua)
			if err != nil {
				fail(err)
				return
			}
			if d.NotifyUnmatchedClaim != nil {
				go d.NotifyUnmatchedClaim(context.Background(), ev.ID)
			}
		default:
			fail(err)
			return
		}

		data.Done = true
		writeClaimPage(w, r, http.StatusOK, data)
	}
}
