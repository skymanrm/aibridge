package main

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"strconv"
	"sync"

	"git.home.fanyagin.ru/personal/ai-bridge/bridge"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	activityLimit = 100
	detailLimit   = 20 // newest requests that keep their request/response bodies
)

// State is everything the window shows about the bridge besides providers and activity.
type State struct {
	Running       bool     `json:"running"`
	Addr          string   `json:"addr"`
	Version       string   `json:"version"`
	Error         string   `json:"error"`
	Active        int      `json:"active"`
	MaxConcurrent int      `json:"max_concurrent"`
	Origins       []string `json:"origins"`
	Token         string   `json:"token"`
	ConfigPath    string   `json:"config_path"`
}

// App owns the bridge server for the lifetime of the window and is bound to the frontend.
type App struct {
	ctx      context.Context
	registry *bridge.Registry

	mu       sync.Mutex
	server   *bridge.Server
	running  bool
	lastErr  string
	activity []bridge.Activity
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	InstallTray(a)
	cfg, err := bridge.LoadConfig()
	if err != nil {
		a.lastErr = err.Error()
		return
	}
	a.registry = bridge.DefaultRegistry(cfg)
	a.Start()
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
	srv := bridge.NewServer(cfg, a.registry)
	srv.OnActivity = a.record
	done, err := srv.Start()
	if err != nil {
		a.lastErr = err.Error()
		a.mu.Unlock()
		return a.State()
	}
	a.server, a.running, a.lastErr = srv, true, ""
	a.mu.Unlock()
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
	a.emitState()
	return a.State()
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
		Origins: []string{}}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Addr = "127.0.0.1:" + strconv.Itoa(cfg.Port)
	st.MaxConcurrent, st.Origins, st.Token, st.ConfigPath = cfg.MaxConcurrent, cfg.Origins, cfg.Token, cfg.Path()
	return st
}

func (a *App) Providers(refresh bool) []bridge.ProviderInfo {
	if a.registry == nil {
		return []bridge.ProviderInfo{}
	}
	return a.registry.Infos(a.ctx, refresh)
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
		if len(a.activity) > activityLimit {
			a.activity = a.activity[:activityLimit]
		}
		if len(a.activity) > detailLimit {
			old := &a.activity[detailLimit]
			old.Request, old.Response = nil, nil
		}
	}
	a.mu.Unlock()
	runtime.EventsEmit(a.ctx, "activity", act)
	a.emitState()
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
