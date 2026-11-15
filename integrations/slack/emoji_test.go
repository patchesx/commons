package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildEmojiListResolvesAliases(t *testing.T) {
	got := buildEmojiList(map[string]string{
		"party":      "https://emoji.example/party.png",
		"party-alt":  "alias:party",
		"party-alt2": "alias:party-alt",
		"thumbs":     "alias:+1",
		"loop-a":     "alias:loop-b",
		"loop-b":     "alias:loop-a",
	})
	byName := map[string]SlackEmoji{}
	for _, e := range got {
		byName[e.Name] = e
	}
	assert.Equal(t, "https://emoji.example/party.png", byName["party-alt"].URL, "alias of custom takes its image")
	assert.Equal(t, "https://emoji.example/party.png", byName["party-alt2"].URL, "alias chains are followed")
	assert.Equal(t, SlackEmoji{Name: "thumbs", Alias: "+1"}, byName["thumbs"], "alias of a standard emoji names it")
	assert.Empty(t, byName["loop-a"].URL, "a loop gives up instead of hanging")
	assert.Equal(t, "loop-a", got[0].Name, "sorted by name")
}

func TestStandardEmojiDataIsServed(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleStandardEmojis().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/slack/emojis/standard", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var emoji []struct {
		E string   `json:"e"`
		N []string `json:"n"`
		T []string `json:"t"`
		C string   `json:"c"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &emoji))
	assert.Greater(t, len(emoji), 1500)
	var thumbs bool
	for _, e := range emoji {
		require.NotEmpty(t, e.E)
		require.NotEmpty(t, e.N)
		require.NotEmpty(t, e.C)
		for _, n := range e.N {
			thumbs = thumbs || (n == "+1" && e.E == "👍")
		}
	}
	assert.True(t, thumbs, ":+1: is 👍")
}
