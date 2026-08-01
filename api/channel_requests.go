package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
	"commons/web"
)

type channelRequestResponse struct {
	ID               string  `json:"id"`
	SlackChannelID   string  `json:"slackChannelId"`
	SlackChannelName string  `json:"slackChannelName"`
	Status           string  `json:"status"`
	RequestedAt      string  `json:"requestedAt"`
	ReviewedAt       *string `json:"reviewedAt,omitempty"`
}

func toChannelRequestResponse(r *store.ChannelRequest) channelRequestResponse {
	resp := channelRequestResponse{
		ID:               r.ID,
		SlackChannelID:   r.SlackChannelID,
		SlackChannelName: r.SlackChannelName,
		Status:           r.Status,
		RequestedAt:      r.RequestedAt.Format(time.RFC3339),
	}
	if r.ReviewedAt != nil {
		s := r.ReviewedAt.Format(time.RFC3339)
		resp.ReviewedAt = &s
	}
	return resp
}

// ListMyChannelRequests handles GET /api/channel-requests.
// Returns the current user's channel access requests, ordered newest first.
func ListMyChannelRequests(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := web.UserIDFromContext(r.Context())
		requests, err := store.ListRequestsByRequester(r.Context(), pool, userID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list requests")
			return
		}
		out := make([]channelRequestResponse, len(requests))
		for i := range requests {
			out[i] = toChannelRequestResponse(&requests[i])
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// CreateChannelRequest handles POST /api/channel-requests.
// Body: {"slackChannelId": "...", "slackChannelName": "..."}
func CreateChannelRequest(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := web.UserIDFromContext(r.Context())

		var body struct {
			SlackChannelID   string `json:"slackChannelId"`
			SlackChannelName string `json:"slackChannelName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.SlackChannelID == "" || body.SlackChannelName == "" {
			httpx.WriteError(w, http.StatusBadRequest, "slackChannelId and slackChannelName are required")
			return
		}

		req := &store.ChannelRequest{
			RequesterID:      &userID,
			SlackChannelID:   body.SlackChannelID,
			SlackChannelName: body.SlackChannelName,
		}
		if err := store.CreateChannelRequest(r.Context(), pool, req); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create request")
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, toChannelRequestResponse(req))
	}
}
