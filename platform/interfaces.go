package platform

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Message is a platform-agnostic notification payload.
// Integrations map this to their native format (Slack blocks, Discord embeds, etc.)
type Message struct {
	Text string
}

// Notifier sends messages to users by their internal user ID.
// The implementation is responsible for resolving the platform-specific recipient.
type Notifier interface {
	NotifyUser(ctx context.Context, userID string, msg Message) error
}

// MemberInterface is the rich interactive layer — Slack App Home, a Discord server, etc.
// Extends Notifier. Optional: the app functions with just a Notifier.
type MemberInterface interface {
	Notifier
	PublishHome(ctx context.Context, userID string) error
}

// MeetingPlatform is the recording source (Zoom today; Google Meet, Teams, etc. in future).
type MeetingPlatform interface {
	WebhookHandler() http.Handler
	DownloadRecording(ctx context.Context, url, token string) (io.ReadCloser, error)
}

// VideoMeta carries platform-agnostic metadata for a video upload.
type VideoMeta struct {
	Title       string
	Description string
	MeetingDate string
}

// VideoHost is where recordings are published (YouTube today).
type VideoHost interface {
	Upload(ctx context.Context, r io.Reader, meta VideoMeta) (string, error)
}

// RecordingFile is a single file from a recording source (e.g. Zoom),
// used when streaming files to backup storage (e.g. Google Drive).
type RecordingFile struct {
	RecordingType string // e.g. "shared_screen_with_speaker_view", "audio_only"
	FileType      string // e.g. "MP4", "M4A", "TRANSCRIPT"
	FileName      string // provider-supplied filename, e.g. "Audio Transcript.vtt"
	DownloadURL   string
	FileSize      int64
}

// BackupFile represents a single file to be backed up.
type BackupFile struct {
	Name     string
	MIMEType string
	Reader   io.ReadCloser
}

// StorageBackend is for archival/backup of recording files (Google Drive today).
// Returns a folder or resource URL on success.
type StorageBackend interface {
	Backup(ctx context.Context, jobID string, files []BackupFile) (string, error)
}

// RecordingStreamer fetches a recording file from a remote URL.
// The implementation authenticates against the recording source's CDN.
// Callers provide the URL and token extracted from the recording source's webhook data.
type RecordingStreamer interface {
	Stream(ctx context.Context, downloadURL, downloadToken string) (io.ReadCloser, error)
}

// StreamerFunc is a function adapter for RecordingStreamer. Useful in tests.
type StreamerFunc func(ctx context.Context, downloadURL, downloadToken string) (io.ReadCloser, error)

func (f StreamerFunc) Stream(ctx context.Context, downloadURL, downloadToken string) (io.ReadCloser, error) {
	return f(ctx, downloadURL, downloadToken)
}

// RecordingMeta carries platform-agnostic meeting metadata for video upload helpers
// (title building, description, resource creation). Contains only the fields that
// upload logic needs — not source-specific identifiers.
type RecordingMeta struct {
	MeetingTopic string
	MeetingDate  time.Time
	DurationSecs *int
	HostEmail    *string
}

// RecordingJobResult is a unified view of a recording pipeline job.
// Combines generic job lifecycle state with integration-specific recording and
// upload data. Used by notifications, resource creation, and the Slack App Home.
type RecordingJobResult struct {
	// Generic job fields.
	JobID        string
	Status       string
	ErrorMessage *string
	StartedAt    time.Time
	CompletedAt  *time.Time
	// Zoom recording metadata.
	MeetingTopic string
	MeetingDate  time.Time
	DurationSecs *int
	// Outputs — nil if the respective integration was disabled or failed.
	VideoID    *string
	VideoTitle *string
	FolderID   *string
}
