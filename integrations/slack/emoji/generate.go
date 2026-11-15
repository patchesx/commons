//go:build ignore

// generate builds standard.json from iamcal/emoji-data, whose short names are the ones Slack uses, and adds
// search terms from GitHub's gemoji where the same emoji appears in both. Run from this directory:
//
//	go run generate.go -iamcal emoji-data/emoji.json -gemoji gemoji/db/emoji.json -out standard.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type iamcalEmoji struct {
	Name       string   `json:"name"`
	Unified    string   `json:"unified"`
	ShortNames []string `json:"short_names"`
	Category   string   `json:"category"`
	SortOrder  int      `json:"sort_order"`
}

type gemojiEmoji struct {
	Emoji       string   `json:"emoji"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

type outEmoji struct {
	E string   `json:"e"` // the character
	N []string `json:"n"` // Slack shortcodes, the first being the main one
	T []string `json:"t"` // search terms
	C string   `json:"c"` // category
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func readJSON(path string, v any) {
	b, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		fail(fmt.Errorf("%s: %w", path, err))
	}
}

func char(unified string) string {
	var b strings.Builder
	for _, hex := range strings.Split(unified, "-") {
		n, err := strconv.ParseInt(hex, 16, 32)
		if err != nil {
			fail(err)
		}
		b.WriteRune(rune(n))
	}
	return b.String()
}

func main() {
	iamcalPath := flag.String("iamcal", "", "iamcal/emoji-data emoji.json")
	gemojiPath := flag.String("gemoji", "", "gemoji db/emoji.json (optional, for search terms)")
	outPath := flag.String("out", "standard.json", "output file")
	flag.Parse()
	if *iamcalPath == "" {
		fail(fmt.Errorf("-iamcal is required"))
	}

	var all []iamcalEmoji
	readJSON(*iamcalPath, &all)
	sort.SliceStable(all, func(i, j int) bool { return all[i].SortOrder < all[j].SortOrder })

	extra := map[string][]string{}
	if *gemojiPath != "" {
		var gem []gemojiEmoji
		readJSON(*gemojiPath, &gem)
		for _, g := range gem {
			extra[strings.ReplaceAll(g.Emoji, "️", "")] = append(g.Tags, g.Description)
		}
	}

	var out []outEmoji
	for _, e := range all {
		if e.Category == "Component" { // skin tone swatches and hair styles are not emoji to pick on their own
			continue
		}
		c := char(e.Unified)
		terms := []string{strings.ToLower(e.Name)}
		terms = append(terms, extra[strings.ReplaceAll(c, "️", "")]...)
		out = append(out, outEmoji{E: c, N: e.ShortNames, T: dedupe(terms), C: e.Category})
	}
	b, err := json.Marshal(out)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(*outPath, append(b, '\n'), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %d emoji to %s\n", len(out), *outPath)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
