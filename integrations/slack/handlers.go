package slack

import (
	_ "embed"
	"net/http"

	"commons/internal/httpx"
)

// HandleListSlackChannels handles GET /api/slack/channels.
// Returns all non-archived channels visible to the bot for use in dropdowns.
func HandleListSlackChannels() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channels, err := ListChannels(r.Context())
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, channels)
	}
}

// HandleListSlackEmojis handles GET /api/slack/emojis.
// Returns all custom emojis in the workspace for use in the composer picker.
func HandleListSlackEmojis() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		emojis, err := ListEmojis(r.Context())
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, emojis)
	}
}

// standardEmojiJSON is the standard emoji set the composer's picker and preview use. See emoji/README.md.
//
//go:embed emoji/standard.json
var standardEmojiJSON []byte

// HandleStandardEmojis handles GET /api/slack/emojis/standard. The data only changes with the binary, so
// browsers may keep it for a day.
func HandleStandardEmojis() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Write(standardEmojiJSON)
	}
}
