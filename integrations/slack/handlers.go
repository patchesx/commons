package slack

import (
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
