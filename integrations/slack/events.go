package slack

import "encoding/json"

// slackEventEnvelope is the outer wrapper for all Slack Events API payloads.
type slackEventEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	Event     slackEventInner `json:"event"`
}

type slackEventInner struct {
	Type        string          `json:"type"`
	User        json.RawMessage `json:"user"` // string for most events; full object for team_join
	Tab         string          `json:"tab"`
	Channel     string          `json:"channel"`
	ChannelType string          `json:"channel_type"`
	BotID       string          `json:"bot_id"`
	Text        string          `json:"text"`
}

// UserID extracts the Slack user ID string from the User field.
// For events like app_home_opened and message, User is a plain string ID.
// For team_join, User is a full object — this returns "" (use teamJoinEnvelope instead).
func (e slackEventInner) UserID() string {
	var s string
	json.Unmarshal(e.User, &s) //nolint:errcheck
	return s
}

// teamJoinEnvelope is used to re-unmarshal team_join events, whose inner user
// field is a full user object rather than a plain ID string.
type teamJoinEnvelope struct {
	Event struct {
		User struct {
			ID      string `json:"id"`
			Profile struct {
				RealName    string `json:"real_name"`
				DisplayName string `json:"display_name"`
				Email       string `json:"email"`
			} `json:"profile"`
		} `json:"user"`
	} `json:"event"`
}
