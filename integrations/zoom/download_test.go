package zoom

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func completedMP4(recordingType string) zoomRecordingFile {
	return zoomRecordingFile{
		FileType:      "MP4",
		RecordingType: recordingType,
		DownloadURL:   "https://example.com/" + recordingType,
		Status:        "completed",
	}
}

func TestSelectRecordingFilePrefersSpeakerView(t *testing.T) {
	files := []zoomRecordingFile{
		completedMP4("shared_screen"),
		completedMP4("shared_screen_with_speaker_view"),
		completedMP4("audio_only"),
	}
	got := selectRecordingFile(files)
	assert.Equal(t, "shared_screen_with_speaker_view", got.RecordingType)
}

func TestSelectRecordingFileSpeakerViewWinsEvenIfLast(t *testing.T) {
	// shared_screen appears first; speaker_view should still win.
	files := []zoomRecordingFile{
		completedMP4("shared_screen"),
		completedMP4("shared_screen_with_speaker_view"),
	}
	got := selectRecordingFile(files)
	assert.Equal(t, "shared_screen_with_speaker_view", got.RecordingType)
}

func TestSelectRecordingFileFallsBackToSharedScreen(t *testing.T) {
	files := []zoomRecordingFile{
		completedMP4("audio_only"),
		completedMP4("shared_screen"),
	}
	got := selectRecordingFile(files)
	assert.Equal(t, "shared_screen", got.RecordingType)
}

func TestSelectRecordingFileFallsBackToAnyMP4(t *testing.T) {
	files := []zoomRecordingFile{
		completedMP4("audio_only"),
	}
	got := selectRecordingFile(files)
	assert.Equal(t, "audio_only", got.RecordingType)
}

func TestSelectRecordingFileSkipsProcessing(t *testing.T) {
	files := []zoomRecordingFile{
		{FileType: "MP4", RecordingType: "shared_screen_with_speaker_view", Status: "processing"},
	}
	assert.Nil(t, selectRecordingFile(files))
}

func TestSelectRecordingFileSkipsNonMP4(t *testing.T) {
	files := []zoomRecordingFile{
		{FileType: "M4A", RecordingType: "audio_only", Status: "completed"},
	}
	assert.Nil(t, selectRecordingFile(files))
}

func TestSelectRecordingFileEmptyList(t *testing.T) {
	assert.Nil(t, selectRecordingFile([]zoomRecordingFile{}))
}

func TestSelectTranscriptFileFound(t *testing.T) {
	files := []zoomRecordingFile{
		{FileType: "MP4", Status: "completed"},
		{FileType: "TRANSCRIPT", Status: "completed", DownloadURL: "https://example.com/transcript.vtt"},
	}
	got := selectTranscriptFile(files)
	assert.NotNil(t, got)
	assert.Equal(t, "https://example.com/transcript.vtt", got.DownloadURL)
}

func TestSelectTranscriptFileSkipsProcessing(t *testing.T) {
	files := []zoomRecordingFile{
		{FileType: "TRANSCRIPT", Status: "processing"},
	}
	assert.Nil(t, selectTranscriptFile(files))
}

func TestSelectTranscriptFileNotPresent(t *testing.T) {
	files := []zoomRecordingFile{
		{FileType: "MP4", Status: "completed"},
	}
	assert.Nil(t, selectTranscriptFile(files))
}

func TestSelectTranscriptFileEmptyList(t *testing.T) {
	assert.Nil(t, selectTranscriptFile([]zoomRecordingFile{}))
}
