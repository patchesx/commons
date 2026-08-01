package gdrive

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	driveapi "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/jackc/pgx/v5/pgxpool"

	gauth "commons/integrations/google"
	"commons/platform"
	"commons/store"
	"commons/util"
)

func newDriveService(ctx context.Context, pool *pgxpool.Pool, encKey []byte) (*driveapi.Service, error) {
	clientID, err := store.GetServiceConfig(ctx, pool, "google", "client_id", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("google/client_id not configured in config_store")
	}
	if err != nil {
		return nil, fmt.Errorf("read client_id: %w", err)
	}

	clientSecret, err := store.GetServiceConfig(ctx, pool, "google", "client_secret", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("google/client_secret not configured in config_store")
	}
	if err != nil {
		return nil, fmt.Errorf("read client_secret: %w", err)
	}

	refreshToken, err := store.GetServiceConfig(ctx, pool, "google", "refresh_token", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("google/refresh_token not configured — authorize via the web UI at /auth/google/connect")
	}
	if err != nil {
		return nil, fmt.Errorf("read refresh_token: %w", err)
	}

	oauthCfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{gauth.ScopeDrive},
	}

	tokenSource := oauthCfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	httpClient := oauth2.NewClient(ctx, tokenSource)

	svc, err := driveapi.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create Drive service: %w", err)
	}
	return svc, nil
}

// Backup creates a subfolder in the configured parent Drive folder and uploads all
// files into it. Each file is downloaded from its DownloadURL using downloadToken
// as a Bearer credential (pass an empty string if the URL needs no auth).
func Backup(
	ctx context.Context,
	pool *pgxpool.Pool,
	encKey []byte,
	parentFolderID string,
	files []platform.RecordingFile,
	downloadToken string,
	meetingTopic string,
	meetingDate time.Time,
	streamer platform.RecordingStreamer,
) (folderID string, err error) {
	if parentFolderID == "" {
		return "", fmt.Errorf("parent folder ID not provided")
	}

	svc, err := newDriveService(ctx, pool, encKey)
	if err != nil {
		return "", fmt.Errorf("build Drive client: %w", err)
	}

	localDate := util.LocalizeMeetingDate(ctx, pool, encKey, meetingDate)
	folderName := fmt.Sprintf("%s - %s", meetingTopic, localDate.Format("2006-01-02"))
	folder, err := svc.Files.Create(&driveapi.File{
		Name:     folderName,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentFolderID},
	}).SupportsAllDrives(true).Do()
	if err != nil {
		return "", fmt.Errorf("create Drive folder %q: %w", folderName, err)
	}

	log.Printf("gdrive: created folder %q (id=%s) in parent %s", folderName, folder.Id, parentFolderID)

	for _, f := range files {
		if err := uploadFile(ctx, svc, f, folder.Id, downloadToken, streamer); err != nil {
			return "", fmt.Errorf("upload %s: %w", recordingFileName(f), err)
		}
	}

	return folder.Id, nil
}

func uploadFile(ctx context.Context, svc *driveapi.Service, f platform.RecordingFile, folderID, downloadToken string, streamer platform.RecordingStreamer) error {
	name := recordingFileName(f)
	mimeType := mimeTypeFor(f.FileType)

	reader, err := streamer.Stream(ctx, f.DownloadURL, downloadToken)
	if err != nil {
		return fmt.Errorf("stream recording file: %w", err)
	}
	defer reader.Close()

	_, err = svc.Files.Create(&driveapi.File{
		Name:    name,
		Parents: []string{folderID},
	}).
		Media(reader, googleapi.ContentType(mimeType)).
		SupportsAllDrives(true).
		Do()
	if err != nil {
		return fmt.Errorf("Drive upload: %w", err)
	}

	log.Printf("gdrive: uploaded %s", name)
	return nil
}

func recordingFileName(f platform.RecordingFile) string {
	if f.FileName != "" {
		return f.FileName
	}
	ext := strings.ToLower(f.FileType)
	switch ext {
	case "transcript":
		ext = "vtt"
	case "timeline":
		ext = "vtt"
	}
	if f.RecordingType != "" {
		return f.RecordingType + "." + ext
	}
	return ext
}

func mimeTypeFor(fileType string) string {
	switch strings.ToUpper(fileType) {
	case "MP4":
		return "video/mp4"
	case "M4A":
		return "audio/mp4"
	case "TRANSCRIPT":
		return "text/vtt"
	case "CHAT":
		return "text/plain"
	case "TIMELINE":
		return "text/vtt"
	default:
		return "application/octet-stream"
	}
}
