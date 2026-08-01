package plugin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
)

// IntegrationCardSpec lets a plugin declare its Integrations page card,
// replacing the default credential-table cards for the listed integration types.
type IntegrationCardSpec struct {
	ReplacesTypes   []string // integration.type values this card absorbs; first match wins
	Name            string   // card header display name
	ConfigService   string   // config service to load credential entries from
	OAuthConnectURL string   // if non-empty: also render OAuth status section
	// GetOAuthStatus is called at render time; captures pool+encKey via closure in Init.
	GetOAuthStatus func(ctx context.Context) (connected bool, email string)
}

type pluginContext struct {
	pool    *pgxpool.Pool
	encKey  []byte
	config  store.ConfigGetter
	mux     *http.ServeMux
	apiAuth func(http.Handler) http.Handler

	notifier          *platform.CompositeNotifier
	meetingScheduler  platform.MeetingScheduler
	recordingStreamer platform.RecordingStreamer

	jobMu sync.Mutex
	jobs  map[string]context.CancelFunc

	navItems         []NavItemReg
	settingsCards    []SettingsCardReg
	integrationCards []IntegrationCardSpec
}

// NewContext returns a PluginContext backed by pool and encKey.
// mux and apiAuth are used by plugins to register HTTP routes during Init.
func NewContext(pool *pgxpool.Pool, encKey []byte, mux *http.ServeMux, apiAuth func(http.Handler) http.Handler) PluginContext {
	return &pluginContext{
		pool:     pool,
		encKey:   encKey,
		config:   store.NewConfigGetter(pool, encKey),
		mux:      mux,
		apiAuth:  apiAuth,
		jobs:     make(map[string]context.CancelFunc),
		notifier: &platform.CompositeNotifier{},
	}
}

func (c *pluginContext) DB() *pgxpool.Pool          { return c.pool }
func (c *pluginContext) EncKey() []byte             { return c.encKey }
func (c *pluginContext) Config() store.ConfigGetter { return c.config }

func (c *pluginContext) Notifier() platform.Notifier { return c.notifier }

func (c *pluginContext) RecordingStreamer() platform.RecordingStreamer { return c.recordingStreamer }
func (c *pluginContext) SetRecordingStreamer(s platform.RecordingStreamer) {
	c.recordingStreamer = s
}

func (c *pluginContext) MeetingScheduler() platform.MeetingScheduler { return c.meetingScheduler }
func (c *pluginContext) SetMeetingScheduler(s platform.MeetingScheduler) {
	c.meetingScheduler = s
}

// AddNotifier appends a notifier to the composite. Both plugins and code that
// previously called SetNotifier should switch to AddNotifier.
func (c *pluginContext) AddNotifier(n platform.Notifier) { c.notifier.Add(n) }

// SetNotifier is kept for backwards compatibility; it now appends to the composite.
func (c *pluginContext) SetNotifier(n platform.Notifier) { c.notifier.Add(n) }

// MemberInterface returns nil; wired in a future phase.
func (c *pluginContext) MemberInterface() platform.MemberInterface { return nil }

func (c *pluginContext) RegisteredPlugins() []Plugin { return All() }

func (c *pluginContext) HasCapability(cap string) bool {
	for _, p := range All() {
		for _, provided := range p.Provides() {
			if provided == cap {
				return true
			}
		}
	}
	return false
}

func (c *pluginContext) RegisterRoute(method, pattern string, handler http.HandlerFunc) {
	c.mux.HandleFunc(method+" "+pattern, handler)
}

func (c *pluginContext) RegisterAuthRoute(method, pattern string, handler http.HandlerFunc) {
	c.mux.Handle(method+" "+pattern, c.apiAuth(handler))
}

func (c *pluginContext) RegisterJob(id string, cancel context.CancelFunc) {
	c.jobMu.Lock()
	c.jobs[id] = cancel
	c.jobMu.Unlock()
}

func (c *pluginContext) UnregisterJob(id string) {
	c.jobMu.Lock()
	delete(c.jobs, id)
	c.jobMu.Unlock()
}

func (c *pluginContext) CancelJob(id string) bool {
	c.jobMu.Lock()
	cancel, ok := c.jobs[id]
	c.jobMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (c *pluginContext) RegisterNavItem(label, path string) {
	c.navItems = append(c.navItems, NavItemReg{Label: label, Path: path})
}

func (c *pluginContext) RegisterSettingsCard(group, path string, handler http.HandlerFunc) {
	c.mux.Handle("GET "+path, c.apiAuth(handler))
	c.settingsCards = append(c.settingsCards, SettingsCardReg{Group: group, Path: path})
}

func (c *pluginContext) ExtraNavItems() []NavItemReg           { return c.navItems }
func (c *pluginContext) ExtraSettingsCards() []SettingsCardReg { return c.settingsCards }

func (c *pluginContext) RegisterIntegrationCard(spec IntegrationCardSpec) {
	c.integrationCards = append(c.integrationCards, spec)
}

func (c *pluginContext) ExtraIntegrationCards() []IntegrationCardSpec { return c.integrationCards }

// RegisterScheduledJob launches a goroutine that polls config_store every minute.
// It runs fn() when the job is enabled and enough time has elapsed since the last run.
// enabledKey and intervalKey are looked up under service "jobs".
func (c *pluginContext) RegisterScheduledJob(enabledKey, intervalKey string, startupDelay time.Duration, fn func()) {
	pool := c.pool
	encKey := c.encKey
	go func() {
		time.Sleep(startupDelay)
		var lastRun time.Time
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			enabledStr, err := store.GetServiceConfig(context.Background(), pool, "jobs", enabledKey, encKey)
			if err != nil {
				continue
			}
			intervalStr, err := store.GetServiceConfig(context.Background(), pool, "jobs", intervalKey, encKey)
			if err != nil {
				continue
			}
			run, _, err := ShouldRun(enabledStr, intervalStr, lastRun)
			if err != nil || !run {
				continue
			}
			lastRun = time.Now()
			fn()
		}
	}()
}

// ShouldRun returns true if the job is enabled and the interval has elapsed since lastRun.
func ShouldRun(enabledStr, intervalStr string, lastRun time.Time) (bool, time.Duration, error) {
	if enabledStr != "true" {
		return false, 0, nil
	}
	mins, err := strconv.Atoi(intervalStr)
	if err != nil {
		return false, 0, err
	}
	if mins <= 0 {
		return false, 0, fmt.Errorf("interval must be positive, got %d", mins)
	}
	interval := time.Duration(mins) * time.Minute
	return time.Since(lastRun) >= interval, interval, nil
}
