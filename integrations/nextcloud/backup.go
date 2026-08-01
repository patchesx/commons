package nextcloud

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/util"
)

// Backup creates a dated subfolder under parentPath and uploads all files into it.
// Returns the folder path created on Nextcloud.
func Backup(
	ctx context.Context,
	pool *pgxpool.Pool,
	encKey []byte,
	parentPath string,
	files []platform.RecordingFile,
	downloadToken string,
	meetingTopic string,
	meetingDate time.Time,
	streamer platform.RecordingStreamer,
) (string, error) {
	if parentPath == "" {
		return "", fmt.Errorf("nextcloud: parent path not provided")
	}

	localDate := util.LocalizeMeetingDate(ctx, pool, encKey, meetingDate)
	folderName := fmt.Sprintf("%s - %s", meetingTopic, localDate.Format("2006-01-02"))
	folderPath := parentPath + "/" + folderName

	if err := EnsureFolder(ctx, pool, encKey, folderPath); err != nil {
		return "", fmt.Errorf("nextcloud: create folder %q: %w", folderPath, err)
	}
	log.Printf("nextcloud: ensured folder %q", folderPath)

	for _, f := range files {
		name := recordingFileName(f)
		reader, err := streamer.Stream(ctx, f.DownloadURL, downloadToken)
		if err != nil {
			return "", fmt.Errorf("nextcloud: stream %s: %w", name, err)
		}
		if err := UploadFile(ctx, pool, encKey, folderPath, name, reader, f.FileSize); err != nil {
			reader.Close()
			return "", fmt.Errorf("nextcloud: upload %s: %w", name, err)
		}
		reader.Close()
		log.Printf("nextcloud: uploaded %s", name)
	}

	return folderPath, nil
}

func recordingFileName(f platform.RecordingFile) string {
	if f.FileName != "" {
		return f.FileName
	}
	ext := f.FileType
	switch ext {
	case "transcript", "timeline":
		ext = "vtt"
	}
	if f.RecordingType != "" {
		return f.RecordingType + "." + ext
	}
	return ext
}
