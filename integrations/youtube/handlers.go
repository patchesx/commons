package youtube

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
)

// HandleUploadToYouTube handles POST /api/youtube/upload.
// Accepts a multipart form with: file (video), title, category, description (optional).
// Uploads to YouTube as unlisted, then creates a resource library entry.
func HandleUploadToYouTube(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if err := r.ParseMultipartForm(32 << 20); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid multipart form")
			return
		}
		defer r.MultipartForm.RemoveAll()

		title := strings.TrimSpace(r.FormValue("title"))
		category := strings.TrimSpace(r.FormValue("category"))
		description := strings.TrimSpace(r.FormValue("description"))

		if title == "" || category == "" {
			httpx.WriteError(w, http.StatusBadRequest, "title and category are required")
			return
		}

		file, _, err := r.FormFile("file")
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "file is required")
			return
		}
		defer file.Close()

		videoID, err := UploadVideo(ctx, pool, encKey, file, title, description)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("upload failed: %v", err))
			return
		}

		if transcript, _, err := r.FormFile("transcript"); err == nil {
			defer transcript.Close()
			if err := InsertCaptions(ctx, pool, encKey, videoID, transcript); err != nil {
				log.Printf("youtube/upload: video %s uploaded but caption insert failed: %v", videoID, err)
			}
		}

		var desc *string
		if description != "" {
			desc = &description
		}

		resource := &store.Resource{
			Title:       title,
			URL:         fmt.Sprintf("https://youtu.be/%s", videoID),
			Category:    category,
			Description: desc,
		}
		if err := store.CreateResource(ctx, pool, resource); err != nil {
			log.Printf("youtube/upload: created video %s but failed to create resource entry: %v", videoID, err)
		}

		store.LogAction(ctx, pool, nil, "youtube.uploaded", resource.ID, map[string]string{
			"title":    title,
			"video_id": videoID,
		})

		httpx.WriteJSON(w, http.StatusCreated, resource)
	}
}
