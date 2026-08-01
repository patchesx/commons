package s3storage

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/util"
)

// Backup uploads all files to S3 under a dated key prefix.
// Returns the key prefix used (parentPrefix/YYYY-MM-DD - topic/).
func Backup(
	ctx context.Context,
	pool *pgxpool.Pool,
	encKey []byte,
	parentPrefix string,
	files []platform.RecordingFile,
	downloadToken string,
	meetingTopic string,
	meetingDate time.Time,
	streamer platform.RecordingStreamer,
) (string, error) {
	if parentPrefix == "" {
		return "", fmt.Errorf("s3: key prefix not provided")
	}

	localDate := util.LocalizeMeetingDate(ctx, pool, encKey, meetingDate)
	folderName := fmt.Sprintf("%s - %s", meetingTopic, localDate.Format("2006-01-02"))
	prefix := strings.TrimRight(parentPrefix, "/") + "/" + folderName + "/"

	for _, f := range files {
		name := recordingFileName(f)
		key := prefix + name

		reader, err := streamer.Stream(ctx, f.DownloadURL, downloadToken)
		if err != nil {
			return "", fmt.Errorf("s3: stream %s: %w", name, err)
		}
		if err := UploadObject(ctx, pool, encKey, key, reader, f.FileSize); err != nil {
			reader.Close()
			return "", fmt.Errorf("s3: upload %s: %w", name, err)
		}
		reader.Close()
		log.Printf("s3storage: uploaded %s", key)
	}

	return prefix, nil
}

func recordingFileName(f platform.RecordingFile) string {
	if f.FileName != "" {
		return f.FileName
	}
	ext := strings.ToLower(f.FileType)
	switch ext {
	case "transcript", "timeline":
		ext = "vtt"
	}
	if f.RecordingType != "" {
		return f.RecordingType + "." + ext
	}
	return ext
}
