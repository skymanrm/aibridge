package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/skymanrm/aibridge/bridge"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	activityLimit = 100
	detailLimit   = 20 // newest requests that keep their request/response bodies
	maxLimit      = 16 // concurrent requests per provider
	minTimeout    = 10
	maxTimeout    = 3600
	usagePoll     = 5 * time.Minute // while the tray shows usage
	usageSettle   = 3 * time.Second // after a request ends, before re-reading usage
)

// State is everything the window shows about the bridge besides providers and activity.
type State struct {
	Running        bool              `json:"running"`
	Addr           string            `json:"addr"`
	Version        string            `json:"version"`
	Error          string            `json:"error"`
	Active         int               `json:"active"`
	MaxConcurrent  int               `json:"max_concurrent"` // default limit for providers without an override
	ProviderLimits map[string]int    `json:"provider_limits"`
	TimeoutSec     int               `json:"timeout_sec"`
	DefaultModels  map[string]string `json:"default_models"` // provider id -> chosen model; missing means the CLI default
	Origins        []string          `json:"origins"`
	TrayUsage      bridge.TrayUsage  `json:"tray_usage"`
	Token          string            `json:"token"`
	ConfigPath     string            `json:"config_path"`
}

// App owns the bridge server for the lifetime of the window and is bound to the frontend.
type App struct {
	ctx      context.Context
	registry *bridge.Registry
	ready    chan struct{} // closed once startup has set registry; bound calls can arrive before that

	mu       sync.Mutex
	server   *bridge.Server
	running  bool
	lastErr  string
	activity []bridge.Activity

	usageMu    sync.Mutex
	usage      []bridge.UsageLimits // last fetched, drawn in the tray
	usageTimer *time.Timer
}

func NewApp() *App { return &App{ready: make(chan struct{})} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	InstallTray(a)
	cfg, err := bridge.LoadConfig()
	if err != nil {
		a.lastErr = err.Error()
		close(a.ready)
		return
	}
	a.registry = bridge.DefaultRegistry(cfg)
	close(a.ready)
	a.Start()
	go a.pollUsage()
}

// pollUsage keeps the tray meter current even while the window is closed; its labels tick every minute.
func (a *App) pollUsage() {
	a.Usage(false)
	poll, minute := time.NewTicker(usagePoll), time.NewTicker(time.Minute)
	for {
		select {
		case <-poll.C:
			if cfg, err := bridge.LoadConfig(); err == nil && cfg.TrayUsage.Style != "off" {
				a.Usage(false)
			}
		case <-minute.C:
			a.drawTrayUsage() // reset and "updated" labels
		}
	}
}

func (a *App) shutdown(context.Context) {
	a.mu.Lock()
	srv := a.server
	a.mu.Unlock()
	if srv != nil {
		_ = srv.Stop()
	}
}

// Start binds 127.0.0.1:<port>; errors (e.g. port in use) are shown in the window.
func (a *App) Start() State {
	cfg, err := bridge.LoadConfig()
	a.mu.Lock()
	if err != nil {
		a.lastErr = err.Error()
		a.mu.Unlock()
		return a.State()
	}
	if a.running {
		a.mu.Unlock()
		return a.State()
	}
	if err := a.launchLocked(cfg); err != nil {
		a.lastErr = err.Error()
		a.mu.Unlock()
		return a.State()
	}
	a.mu.Unlock()
	a.emitState()
	return a.State()
}

// SetPort saves a new port; a running bridge moves to it and keeps the old port if the new one can't be bound.
func (a *App) SetPort(port int) (State, error) {
	if port < 1 || port > 65535 {
		return a.State(), fmt.Errorf("port must be between 1 and 65535")
	}
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return a.State(), err
	}
	if cfg.Port == port {
		return a.State(), nil
	}
	cfg.Port = port
	a.mu.Lock()
	old, failed := a.server, !a.running && a.lastErr != ""
	if old != nil {
		if err := a.launchLocked(cfg); err != nil {
			a.mu.Unlock()
			return a.State(), err
		}
	}
	a.mu.Unlock()
	if err := cfg.Save(); err != nil {
		return a.State(), err
	}
	if old != nil {
		_ = old.Stop()
		a.emitState()
	} else if failed {
		a.Start()
	} else {
		a.emitState()
	}
	return a.State(), nil
}

// SetLimit saves how many requests one provider may run at once; the running server applies it to new requests.
func (a *App) SetLimit(provider string, n int) (State, error) {
	if n < 1 || n > maxLimit {
		return a.State(), fmt.Errorf("limit must be between 1 and %d", maxLimit)
	}
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return a.State(), err
	}
	if cfg.ProviderLimits == nil {
		cfg.ProviderLimits = map[string]int{}
	}
	cfg.ProviderLimits[provider] = n
	if err := cfg.Save(); err != nil {
		return a.State(), err
	}
	a.emitState()
	return a.State(), nil
}

// SetTimeout saves the per-request timeout in seconds.
func (a *App) SetTimeout(sec int) (State, error) {
	if sec < minTimeout || sec > maxTimeout {
		return a.State(), fmt.Errorf("timeout must be between %d and %d seconds", minTimeout, maxTimeout)
	}
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return a.State(), err
	}
	cfg.TimeoutSec = sec
	if err := cfg.Save(); err != nil {
		return a.State(), err
	}
	a.emitState()
	return a.State(), nil
}

// SetDefaultModel saves the model a provider uses when a request names none; empty restores the CLI default.
func (a *App) SetDefaultModel(provider, model string) (State, error) {
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return a.State(), err
	}
	if model == "" {
		delete(cfg.DefaultModels, provider)
	} else {
		if cfg.DefaultModels == nil {
			cfg.DefaultModels = map[string]string{}
		}
		cfg.DefaultModels[provider] = model
	}
	if err := cfg.Save(); err != nil {
		return a.State(), err
	}
	a.emitState()
	return a.State(), nil
}

// launchLocked starts a server on cfg.Port and makes it the current one; a.mu must be held.
func (a *App) launchLocked(cfg *bridge.Config) error {
	srv := bridge.NewServer(cfg, a.registry)
	srv.OnActivity = a.record
	done, err := srv.Start()
	if err != nil {
		return err
	}
	a.server, a.running, a.lastErr = srv, true, ""
	go func() {
		if err := <-done; err != nil {
			log.Printf("server stopped: %v", err)
			a.mu.Lock()
			a.lastErr = err.Error()
			a.mu.Unlock()
		}
		a.mu.Lock()
		if a.server == srv {
			a.running, a.server = false, nil
		}
		a.mu.Unlock()
		a.emitState()
	}()
	return nil
}

// Stop closes the port and cancels running requests.
func (a *App) Stop() State {
	a.mu.Lock()
	srv := a.server
	a.server, a.running = nil, false
	a.mu.Unlock()
	if srv != nil {
		_ = srv.Stop()
	}
	a.emitState()
	return a.State()
}

func (a *App) State() State {
	cfg, err := bridge.LoadConfig()
	a.mu.Lock()
	defer a.mu.Unlock()
	st := State{Running: a.running, Version: bridge.Version, Error: a.lastErr, Active: a.activeLocked(),
		Origins: []string{}, ProviderLimits: map[string]int{}, DefaultModels: map[string]string{}}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Addr = "127.0.0.1:" + strconv.Itoa(cfg.Port)
	st.MaxConcurrent, st.TimeoutSec, st.Origins, st.Token, st.ConfigPath = cfg.MaxConcurrent, cfg.TimeoutSec, cfg.Origins, cfg.Token, cfg.Path()
	st.TrayUsage = cfg.TrayUsage
	st.ProviderLimits = cfg.ProviderLimits
	if st.ProviderLimits == nil {
		st.ProviderLimits = map[string]int{}
	}
	st.DefaultModels = cfg.DefaultModels
	if st.DefaultModels == nil {
		st.DefaultModels = map[string]string{}
	}
	return st
}

func (a *App) Providers(refresh bool) []bridge.ProviderInfo {
	<-a.ready
	if a.registry == nil {
		return []bridge.ProviderInfo{}
	}
	return a.registry.Infos(a.ctx, refresh)
}

// Usage returns each AI CLI's subscription usage limits, cached for a minute unless refresh is set.
func (a *App) Usage(refresh bool) []bridge.UsageLimits {
	<-a.ready
	if a.registry == nil {
		return []bridge.UsageLimits{}
	}
	limits := a.registry.Usage(a.ctx, refresh)
	a.usageMu.Lock()
	a.usage = limits
	a.usageMu.Unlock()
	runtime.EventsEmit(a.ctx, "usage", limits)
	a.drawTrayUsage()
	return limits
}

func (a *App) drawTrayUsage() {
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return
	}
	a.usageMu.Lock()
	limits := a.usage
	a.usageMu.Unlock()
	running := map[string]int{}
	a.mu.Lock()
	for _, x := range a.activity {
		if x.Status == "running" {
			running[x.Provider]++
		}
	}
	a.mu.Unlock()
	UpdateTrayUsage(buildTrayMeter(cfg.TrayUsage, limits, running, time.Now()))
}

// SetTrayUsage saves how the menu bar / tray icon shows usage: style, which limit, and which AI tools to leave out.
func (a *App) SetTrayUsage(t bridge.TrayUsage) (State, error) {
	if !slices.Contains(bridge.TrayStyles, t.Style) || !slices.Contains(bridge.TrayWindows, t.Window) {
		return a.State(), fmt.Errorf("unknown tray style %q or limit %q", t.Style, t.Window)
	}
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return a.State(), err
	}
	cfg.TrayUsage = t
	if err := cfg.Save(); err != nil {
		return a.State(), err
	}
	a.drawTrayUsage()
	a.emitState()
	return a.State(), nil
}

// TestProvider sends a tiny prompt through one AI CLI to check it answers.
func (a *App) TestProvider(id string) bridge.TestResult {
	<-a.ready
	if a.registry == nil {
		return bridge.TestResult{Error: "bridge is not configured"}
	}
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return bridge.TestResult{Error: err.Error()}
	}
	return a.registry.Test(a.ctx, id, cfg)
}

// IntegrationPrompt builds the prompt users paste into their AI coding assistant; task is optional.
func (a *App) IntegrationPrompt(task string) (string, error) {
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return "", err
	}
	return bridge.IntegrationPrompt(cfg, a.Providers(false), task)
}

func (a *App) Activity() []bridge.Activity {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]bridge.Activity{}, a.activity...)
}

// ActivityDetail is the request and response JSON of one activity entry.
type ActivityDetail struct {
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}

func (a *App) ActivityDetail(id int64) *ActivityDetail {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.activity, func(x bridge.Activity) bool { return x.ID == id })
	if i < 0 || a.activity[i].Request == nil {
		return nil
	}
	return &ActivityDetail{Request: a.activity[i].Request, Response: a.activity[i].Response}
}

func (a *App) ClearActivity() {
	a.mu.Lock()
	a.activity = slices.DeleteFunc(a.activity, func(x bridge.Activity) bool { return x.Status != "running" })
	a.mu.Unlock()
	runtime.EventsEmit(a.ctx, "activity:reset")
}

// AllowOrigin saves the site; the running server picks the config change up on its next request.
func (a *App) AllowOrigin(origin string) (State, error) {
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return a.State(), err
	}
	if _, err := cfg.AllowOrigin(origin); err != nil {
		return a.State(), err
	}
	return a.State(), nil
}

func (a *App) DenyOrigin(origin string) (State, error) {
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return a.State(), err
	}
	if _, err := cfg.DenyOrigin(origin); err != nil {
		return a.State(), err
	}
	return a.State(), nil
}

func (a *App) RotateToken() (State, error) {
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return a.State(), err
	}
	cfg.Token = bridge.NewToken()
	if err := cfg.Save(); err != nil {
		return a.State(), err
	}
	return a.State(), nil
}

func (a *App) CopyToken() error {
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return err
	}
	return runtime.ClipboardSetText(a.ctx, cfg.Token)
}

func (a *App) record(act bridge.Activity) {
	a.mu.Lock()
	if i := slices.IndexFunc(a.activity, func(x bridge.Activity) bool { return x.ID == act.ID }); i >= 0 {
		a.activity[i] = act
	} else {
		a.activity = append([]bridge.Activity{act}, a.activity...)
		// Newest first; concurrent requests may be recorded out of start order.
		slices.SortStableFunc(a.activity, func(x, y bridge.Activity) int { return y.StartedAt.Compare(x.StartedAt) })
		if len(a.activity) > activityLimit {
			a.activity = a.activity[:activityLimit]
		}
		for i := detailLimit; i < len(a.activity); i++ {
			a.activity[i].Request, a.activity[i].Response = nil, nil
		}
	}
	a.mu.Unlock()
	runtime.EventsEmit(a.ctx, "activity", act)
	a.emitState()
	a.drawTrayUsage() // activity dots
	if act.Status != "running" {
		// A finished run moves the limits; the registry caches them for a minute, so bursts stay cheap.
		a.usageMu.Lock()
		if a.usageTimer != nil {
			a.usageTimer.Stop()
		}
		a.usageTimer = time.AfterFunc(usageSettle, func() { a.Usage(false) })
		a.usageMu.Unlock()
	}
}

func (a *App) activeLocked() int {
	n := 0
	for _, x := range a.activity {
		if x.Status == "running" {
			n++
		}
	}
	return n
}

func (a *App) emitState() {
	if a.ctx == nil {
		return
	}
	st := a.State()
	runtime.EventsEmit(a.ctx, "state", st)
	status := "Bridge stopped"
	if st.Running {
		status = "Bridge open on " + st.Addr
	}
	UpdateTray(st.Running, st.Active, status)
}
