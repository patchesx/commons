package slack

import (
	"fmt"
	"strings"
	"time"

	slacklib "github.com/slack-go/slack"
)

// pollDateToken renders a time in each viewer's own timezone, falling back to the organization's timezone
// where Slack can't (notifications, old clients).
func pollDateToken(t time.Time, loc *time.Location) string {
	fallback := t.In(loc).Format("Jan 2, 2006 3:04 PM MST")
	return fmt.Sprintf("<!date^%d^{date_short_pretty} at {time}|%s>", t.Unix(), fallback)
}

func pollMD(s string) *slacklib.TextBlockObject {
	return slacklib.NewTextBlockObject(slacklib.MarkdownType, s, false, false)
}

// pollMessage builds the fallback text and blocks for a poll in its current state. A closed poll shows its
// totals and says it closed at its deadline, and a cancelled one says so.
func pollMessage(p *Poll, loc *time.Location) (string, []slacklib.Block) {
	switch p.Status {
	case PollClosed:
		return pollClosedMessage(p, loc, p.ClosesAt, true)
	case PollCancelled:
		return pollCancelledMessage(p)
	}
	return pollOpenMessage(p, loc)
}

// pollOpenMessage opens with when the poll closes, then the question and the emoji to react with.
func pollOpenMessage(p *Poll, loc *time.Location) (string, []slacklib.Block) {
	md := pollMD
	options := ""
	for i, o := range p.Options {
		if i > 0 {
			options += "\n"
		}
		options += fmt.Sprintf(":%s:  %s", o.Emoji, escapeMrkdwn(o.Label))
	}
	blocks := []slacklib.Block{
		slacklib.NewSectionBlock(md(":bar_chart: *Poll closes "+pollDateToken(p.ClosesAt, loc)+"*"), nil, nil),
		slacklib.NewSectionBlock(md("*"+escapeMrkdwn(p.Question)+"*"), nil, nil),
		slacklib.NewSectionBlock(md("React with the emoji for your answer:\n"+options), nil, nil),
		slacklib.NewContextBlock("", md("Poll by <@"+p.CreatorSlackID+">")),
	}
	return "Poll: " + p.Question, blocks
}

// pollOptionLines lists the options, with their vote totals once the poll is tallied.
func pollOptionLines(p *Poll, withVotes bool) string {
	var b strings.Builder
	for i, o := range p.Options {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, ":%s:  %s", o.Emoji, escapeMrkdwn(o.Label))
		if withVotes {
			fmt.Fprintf(&b, "  \u2014  *%d*", o.Votes)
		}
	}
	return b.String()
}

// pollVotersLine says how many members voted: out of everyone who received the poll, or just a count for a
// channel.
func pollVotersLine(p *Poll) string {
	switch {
	case p.Audience == PollAudienceEveryone:
		return fmt.Sprintf("%d of %d members voted", p.Voters, p.Posted)
	case p.Voters == 1:
		return "1 person voted"
	default:
		return fmt.Sprintf("%d people voted", p.Voters)
	}
}

// pollClosedMessage replaces the opening line with when the poll closed and shows the final totals. closedAt is
// the deadline for a poll that ran its course, or the moment it was closed early. If the reactions couldn't be
// read there are no totals to show.
func pollClosedMessage(p *Poll, loc *time.Location, closedAt time.Time, resultsAvailable bool) (string, []slacklib.Block) {
	body := pollOptionLines(p, resultsAvailable)
	footer := pollVotersLine(p) + " \u00b7 Poll by <@" + p.CreatorSlackID + ">"
	if !resultsAvailable {
		body += "\n_Results are unavailable._"
		footer = "Poll by <@" + p.CreatorSlackID + ">"
	}
	blocks := []slacklib.Block{
		slacklib.NewSectionBlock(pollMD(":bar_chart: *Poll closed "+pollDateToken(closedAt, loc)+"*"), nil, nil),
		slacklib.NewSectionBlock(pollMD("*"+escapeMrkdwn(p.Question)+"*"), nil, nil),
		slacklib.NewSectionBlock(pollMD(body), nil, nil),
		slacklib.NewContextBlock("", pollMD(footer)),
	}
	return "Poll closed: " + p.Question, blocks
}

// pollCancelledMessage replaces the poll with a notice that the creator cancelled it.
func pollCancelledMessage(p *Poll) (string, []slacklib.Block) {
	blocks := []slacklib.Block{
		slacklib.NewSectionBlock(pollMD(":no_entry_sign: *Poll cancelled*"), nil, nil),
		slacklib.NewSectionBlock(pollMD("~"+escapeMrkdwn(p.Question)+"~"), nil, nil),
		slacklib.NewContextBlock("", pollMD("Poll by <@"+p.CreatorSlackID+">")),
	}
	return "Poll cancelled: " + p.Question, blocks
}

// pollControlsBlocks is the creator's "your poll is live" message with its Close now and Cancel poll buttons.
func pollControlsBlocks(text, pollID string) []slacklib.Block {
	plain := func(s string) *slacklib.TextBlockObject {
		return slacklib.NewTextBlockObject(slacklib.PlainTextType, s, false, false)
	}
	closeBtn := slacklib.NewButtonBlockElement(ActionPollClose, pollID, plain("Close now"))
	closeBtn.Confirm = slacklib.NewConfirmationBlockObject(plain("Close this poll now?"),
		plain("The reactions are tallied now and the results are posted on the poll."), plain("Close now"), plain("Keep it open"))
	cancelBtn := slacklib.NewButtonBlockElement(ActionPollCancel, pollID, plain("Cancel poll"))
	cancelBtn.Style = slacklib.StyleDanger
	cancelBtn.Confirm = slacklib.NewConfirmationBlockObject(plain("Cancel this poll?"),
		plain("The poll is replaced with a cancelled notice and no results are tallied."), plain("Cancel poll"), plain("Keep it"))
	return []slacklib.Block{
		slacklib.NewSectionBlock(pollMD(text), nil, nil),
		slacklib.NewActionBlock("poll_controls", closeBtn, cancelBtn),
	}
}
