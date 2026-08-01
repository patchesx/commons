package blocks

import (
	"fmt"
	"time"

	slacklib "github.com/slack-go/slack"

	"commons/platform"
	"commons/store"
)

// JobsBlocks returns Block Kit blocks for a list of recording pipeline jobs.
func JobsBlocks(jobs []platform.RecordingJobResult) []slacklib.Block {
	if len(jobs) == 0 {
		return []slacklib.Block{
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No uploads yet._", false, false),
				nil, nil,
			),
		}
	}

	var blocks []slacklib.Block
	for i := range jobs {
		blocks = append(blocks, jobBlock(&jobs[i]))
	}
	return blocks
}

func jobBlock(j *platform.RecordingJobResult) slacklib.Block {
	emoji := statusEmoji(j.Status)
	title := j.MeetingTopic
	if j.VideoTitle != nil && *j.VideoTitle != "" {
		title = *j.VideoTitle
	}

	dateStr := j.MeetingDate.Format("Jan 2, 2006")
	elapsed := elapsedLabel(j.StartedAt, j.CompletedAt)

	var text string
	switch j.Status {
	case store.JobStatusComplete:
		videoURL := ""
		if j.VideoID != nil {
			videoURL = fmt.Sprintf("https://youtu.be/%s", *j.VideoID)
		}
		text = fmt.Sprintf("%s *%s* — %s\n<%s|Watch on YouTube>  •  %s", emoji, title, dateStr, videoURL, elapsed)
	case store.JobStatusFailed:
		errSnippet := "unknown error"
		if j.ErrorMessage != nil {
			errSnippet = *j.ErrorMessage
			if len(errSnippet) > 80 {
				errSnippet = errSnippet[:80] + "…"
			}
		}
		text = fmt.Sprintf("%s *%s* — %s\n_%s_  •  %s", emoji, title, dateStr, errSnippet, elapsed)
	default:
		text = fmt.Sprintf("%s *%s* — %s\n%s  •  %s", emoji, title, dateStr, j.Status, elapsed)
	}

	return slacklib.NewSectionBlock(
		slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
		nil, nil,
	)
}

func statusEmoji(status string) string {
	switch status {
	case store.JobStatusComplete:
		return ":white_check_mark:"
	case store.JobStatusFailed:
		return ":x:"
	default:
		return ":hourglass_flowing_sand:"
	}
}

func elapsedLabel(start time.Time, completedAt *time.Time) string {
	end := time.Now()
	if completedAt != nil {
		end = *completedAt
	}
	d := end.Sub(start).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}
