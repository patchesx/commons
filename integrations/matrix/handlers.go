package matrix

import (
	"encoding/json"
	"net/http"
)

// HandleListMatrixRooms returns a JSON array of rooms the bot has joined.
func HandleListMatrixRooms() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rooms, err := ListRooms(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rooms)
	}
}
