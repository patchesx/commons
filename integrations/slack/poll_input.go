package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	minPollOptions = 2
	maxPollOptions = 6

	maxPollQuestionLen = 250
	maxPollLabelLen    = 80

	minPollWindow = 30 * time.Minute
	maxPollWindow = 30 * 24 * time.Hour
)

var emojiNameRe = regexp.MustCompile(`^[a-z0-9_+\-]+$`)

type standardEmoji struct {
	Char  string   `json:"e"`
	Names []string `json:"n"`
}

var (
	standardEmojiOnce   sync.Once
	standardEmojiByName map[string]string // shortcode -> character
	standardEmojiByChar map[string]string // character (without variation selector) -> first shortcode
)

func loadStandardEmoji() {
	standardEmojiOnce.Do(func() {
		var all []standardEmoji
		standardEmojiByName = map[string]string{}
		standardEmojiByChar = map[string]string{}
		if err := json.Unmarshal(standardEmojiJSON, &all); err != nil {
			log.Printf("slack/poll: parse standard emoji: %v", err)
			return
		}
		for _, e := range all {
			for _, n := range e.Names {
				standardEmojiByName[n] = e.Char
			}
			if len(e.Names) > 0 {
				standardEmojiByChar[stripVariation(e.Char)] = e.Names[0]
			}
		}
	})
}

// stripVariation removes the emoji variation selector, which Slack and the emoji data use inconsistently, and
// skin tone modifiers, so a pasted skin-toned emoji is the same option as the plain one.
func stripVariation(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\uFE0F' || (r >= 0x1F3FB && r <= 0x1F3FF) {
			return -1
		}
		return r
	}, s)
}

// emojiKey identifies an emoji independent of which shortcode names it, so :+1: and :thumbsup: are the same
// option. Custom emoji are identified by name.
func emojiKey(name string) string {
	loadStandardEmoji()
	if c, ok := standardEmojiByName[name]; ok {
		return "u:" + stripVariation(c)
	}
	return "c:" + name
}

const customEmojiTTL = 5 * time.Minute

var (
	customEmojiMu    sync.Mutex
	customEmojiNames map[string]struct{}
	customEmojiAt    time.Time
)

// customEmojiSet returns the workspace's custom emoji names, cached briefly because the poll modal validates
// inside Slack's three-second response window. If Slack can't be asked (for example without emoji:read), it
// returns nil and only standard emoji are accepted.
func customEmojiSet(ctx context.Context) map[string]struct{} {
	customEmojiMu.Lock()
	defer customEmojiMu.Unlock()
	if customEmojiNames != nil && time.Since(customEmojiAt) < customEmojiTTL {
		return customEmojiNames
	}
	list, err := ListEmojis(ctx)
	if err != nil {
		log.Printf("slack/poll: list custom emoji: %v", err)
		return nil
	}
	set := make(map[string]struct{}, len(list))
	for _, e := range list {
		set[e.Name] = struct{}{}
	}
	customEmojiNames, customEmojiAt = set, time.Now()
	return set
}

// pollEmojiKnown reports whether name is a standard emoji or one of the workspace's custom emoji.
func pollEmojiKnown(ctx context.Context) func(string) bool {
	loadStandardEmoji()
	return func(name string) bool {
		if _, ok := standardEmojiByName[name]; ok {
			return true
		}
		_, ok := customEmojiSet(ctx)[name]
		return ok
	}
}

// normalizePollEmoji turns what a creator typed into a shortcode: ":Pizza:" and "pizza" both give "pizza", and
// an emoji character typed or picked in Slack gives its shortcode.
func normalizePollEmoji(raw string) string {
	loadStandardEmoji()
	s := strings.TrimSpace(raw)
	if name, ok := standardEmojiByChar[stripVariation(s)]; ok {
		return name
	}
	return strings.ToLower(strings.Trim(s, ": "))
}

var durationPartRe = regexp.MustCompile(`^\s*(\d+)\s*([a-z]+)`)

var durationUnits = map[string]time.Duration{
	"m": time.Minute, "min": time.Minute, "mins": time.Minute, "minute": time.Minute, "minutes": time.Minute,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
}

// parsePollDuration reads "90m", "2h", "3 days" or "1d 12h".
func parsePollDuration(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	var total time.Duration
	for rest := s; strings.TrimSpace(rest) != ""; {
		m := durationPartRe.FindStringSubmatchIndex(rest)
		if m == nil {
			return 0, fmt.Errorf("unreadable duration")
		}
		n, err := strconv.Atoi(rest[m[2]:m[3]])
		unit, ok := durationUnits[rest[m[4]:m[5]]]
		if err != nil || !ok {
			return 0, fmt.Errorf("unreadable duration")
		}
		total += time.Duration(n) * unit
		rest = rest[m[1]:]
	}
	if total <= 0 {
		return 0, fmt.Errorf("unreadable duration")
	}
	return total, nil
}

var (
	optionBulletRe    = regexp.MustCompile(`^[\s•*\-]+`)
	optionSeparatorRe = regexp.MustCompile(`^[\s\-–—=:]*`)
)

// splitEmojiToken splits an option line into its emoji and the rest. The emoji is a :shortcode:, or an emoji
// character, which ends at the first space or dash.
func splitEmojiToken(line string) (token, rest string) {
	if strings.HasPrefix(line, ":") {
		if i := strings.Index(line[1:], ":"); i >= 0 {
			return line[:i+2], line[i+2:]
		}
		return line, ""
	}
	if i := strings.IndexAny(line, " \t-–—="); i >= 0 {
		return line[:i], line[i:]
	}
	return line, ""
}

// parsePollOptionLines reads options written one per line as an emoji, a dash and what it means, like
// ":pizza: - Pizza". A leading bullet and any of - – — = : between the emoji and its label are allowed. It
// returns the options it could read and a message for each line it couldn't, so one typo doesn't lose the rest.
func parsePollOptionLines(text string, known func(string) bool) (options []PollOptionInput, problems []string) {
	for _, raw := range strings.Split(text, "\n") {
		line := optionBulletRe.ReplaceAllString(strings.TrimSpace(raw), "")
		if line == "" {
			continue
		}
		token, rest := splitEmojiToken(line)
		loadStandardEmoji()
		if _, isChar := standardEmojiByChar[stripVariation(token)]; !isChar && !strings.HasPrefix(token, ":") {
			// A plain word like "fire" must not quietly become :fire:.
			problems = append(problems, fmt.Sprintf("“%s” — start each option with an emoji. Pick it with Slack's emoji picker.", escapeMrkdwn(raw)))
			continue
		}
		name := normalizePollEmoji(token)
		if !emojiNameRe.MatchString(name) || !known(name) {
			problems = append(problems, fmt.Sprintf("“%s” — I don't know the emoji %s. Pick it with Slack's emoji picker.", escapeMrkdwn(raw), escapeMrkdwn(token)))
			continue
		}
		label := strings.TrimSpace(optionSeparatorRe.ReplaceAllString(rest, ""))
		switch {
		case label == "":
			problems = append(problems, fmt.Sprintf("“%s” — say what :%s: means, like :%s: - Pizza.", escapeMrkdwn(raw), name, name))
			continue
		case len([]rune(label)) > maxPollLabelLen:
			problems = append(problems, fmt.Sprintf("“%s” — keep the label under %d characters.", escapeMrkdwn(raw), maxPollLabelLen))
			continue
		}
		options = append(options, PollOptionInput{Emoji: name, Label: label})
	}
	return options, problems
}

// pollWindowError says why a closing time can't be used, or returns "" if it can.
func pollWindowError(closesAt, now time.Time) string {
	switch {
	case closesAt.Before(now.Add(minPollWindow)):
		return "The poll must close at least 30 minutes from now."
	case closesAt.After(now.Add(maxPollWindow)):
		return "The poll can stay open for 30 days at most."
	}
	return ""
}

// EmojiCharacter is the character for a standard emoji's shortcode, or "" for a custom or unknown name. The
// admin pages use it to show an option's emoji without Slack's image hosting.
func EmojiCharacter(name string) string {
	loadStandardEmoji()
	return standardEmojiByName[name]
}
