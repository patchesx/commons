package adminui

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"commons/store"
)

const (
	importTokenTTL  = 24 * time.Hour
	importResultTTL = time.Hour
)

func newImportToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type importResultEntry struct {
	result  store.InventoryImportResult
	expires time.Time
}

// ImportState keeps, in memory, which import previews have already been confirmed and the result of each
// confirmed import. It exists so one preview can be confirmed only once (a reload or the back button must not
// import a file's blank-code rows a second time) and so the result page can be reloaded safely.
//
// It is not persisted: after a restart a preview that was already confirmed could be confirmed again, and old
// result pages are gone.
type ImportState struct {
	mu      sync.Mutex
	used    map[string]time.Time
	results map[string]importResultEntry
	now     func() time.Time
}

func NewImportState() *ImportState {
	return &ImportState{used: map[string]time.Time{}, results: map[string]importResultEntry{}, now: time.Now}
}

func (s *ImportState) pruneLocked(now time.Time) {
	for k, expires := range s.used {
		if now.After(expires) {
			delete(s.used, k)
		}
	}
	for k, e := range s.results {
		if now.After(e.expires) {
			delete(s.results, k)
		}
	}
}

// Use spends a preview's token. It returns true the first time and false every time after.
func (s *ImportState) Use(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	if _, done := s.used[token]; done {
		return false
	}
	s.used[token] = now.Add(importTokenTTL)
	return true
}

// StoreResult keeps a confirmed import's result so its page can be shown, and reloaded, for a while.
func (s *ImportState) StoreResult(token string, r store.InventoryImportResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	s.results[token] = importResultEntry{result: r, expires: now.Add(importResultTTL)}
}

func (s *ImportState) Result(token string) (store.InventoryImportResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	e, ok := s.results[token]
	return e.result, ok
}
