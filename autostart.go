package main

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/skymanrm/aibridge/bridge"
)

const (
	autoStartOrigin   = "auto-start"     // Activity origin of automatic session starts
	autoStartCooldown = 10 * time.Minute // between attempts for one AI tool, so a failing start doesn't loop
)

// SetAutoStart turns on or off starting a new 5-hour session for an AI tool as soon as its last one resets.
func (a *App) SetAutoStart(provider string, on bool) (State, error) {
	cfg, err := bridge.LoadConfig()
	if err != nil {
		return a.State(), err
	}
	cfg.AutoStart = slices.DeleteFunc(cfg.AutoStart, func(id string) bool { return id == provider })
	if on {
		cfg.AutoStart = append(cfg.AutoStart, provider)
	}
	if err := cfg.Save(); err != nil {
		return a.State(), err
	}
	a.emitState()
	a.usageMu.Lock()
	limits := a.usage
	a.usageMu.Unlock()
	a.autoStart(cfg, limits)
	return a.State(), nil
}

// autoStart sends a tiny prompt to each opted-in AI tool whose session window is idle, starting the next window.
func (a *App) autoStart(cfg *bridge.Config, limits []bridge.UsageLimits) {
	if len(cfg.AutoStart) == 0 || a.registry == nil {
		return
	}
	now := time.Now()
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	if a.sessionStarts == nil {
		a.sessionStarts = map[string]time.Time{}
	}
	for _, u := range limits {
		if !slices.Contains(cfg.AutoStart, u.Provider) || !u.SessionIdle(now) || u.Exhausted() ||
			now.Sub(a.sessionStarts[u.Provider]) < autoStartCooldown {
			continue
		}
		a.sessionStarts[u.Provider] = now
		go a.startSession(cfg, u.Provider)
	}
}

// startSession runs the connection-test prompt through an AI tool and logs it like a request.
func (a *App) startSession(cfg *bridge.Config, provider string) {
	req := bridge.TestRequest(cfg.DefaultModels[provider])
	body, _ := json.Marshal(req)
	act := bridge.Activity{ID: bridge.NextActivityID(), Origin: autoStartOrigin, Provider: provider, Model: req.Model,
		Status: "running", StartedAt: time.Now(), Request: body}
	a.record(act)
	res := a.registry.Test(a.ctx, provider, cfg)
	act.DurationMs, act.Model = res.DurationMs, res.Model
	if res.OK {
		act.Status = "ok"
		act.Response, _ = json.Marshal(map[string]string{"text": res.Text})
	} else {
		act.Status, act.Error = "error", res.Error
		act.Response, _ = json.Marshal(map[string]any{"error": map[string]string{"code": "provider_failed", "message": res.Error}})
	}
	a.record(act)
	a.Usage(true)
}
