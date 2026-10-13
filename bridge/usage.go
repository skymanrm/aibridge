package bridge

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// UsageWindow is one rolling subscription limit, e.g. the 5-hour session or the weekly cap.
type UsageWindow struct {
	ID            string     `json:"id"`
	Label         string     `json:"label"`
	UsedPercent   float64    `json:"used_percent"`
	WindowMinutes int        `json:"window_minutes,omitempty"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
}

// UsageLimits is a provider's subscription usage as its CLI reports it.
type UsageLimits struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	// Supported is false for CLIs that don't report usage limits.
	Supported bool          `json:"supported"`
	Plan      string        `json:"plan,omitempty"`
	Windows   []UsageWindow `json:"windows"`
	FetchedAt *time.Time    `json:"fetched_at,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// UsageReporter is implemented by providers whose CLI reports subscription usage limits.
type UsageReporter interface {
	Usage(ctx context.Context) (UsageLimits, error)
}

const (
	usageTTL     = time.Minute
	usageTimeout = 30 * time.Second
)

var usageSlug = regexp.MustCompile(`[^a-z0-9]+`)

// windowID turns a label like "Current week (Fable)" into "week_fable".
func windowID(label string) string {
	s := strings.ToLower(label)
	s = strings.TrimPrefix(s, "current ")
	s = strings.ReplaceAll(s, "(all models)", "")
	return strings.Trim(usageSlug.ReplaceAllString(s, "_"), "_")
}

type usageCache struct {
	mu      sync.Mutex
	limits  []UsageLimits
	fetched time.Time
}

// Usage asks each provider's CLI for its subscription limits; results are cached for a minute unless refresh is set.
func (r *Registry) Usage(ctx context.Context, refresh bool) []UsageLimits {
	r.usage.mu.Lock()
	defer r.usage.mu.Unlock()
	if !refresh && r.usage.limits != nil && time.Since(r.usage.fetched) < usageTTL {
		return r.usage.limits
	}
	// A client hanging up shouldn't cache errors for everyone else.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageTimeout)
	defer cancel()
	infos := r.Infos(ctx, false)
	out := make([]UsageLimits, len(r.providers))
	var wg sync.WaitGroup
	for i, p := range r.providers {
		info := infos[i]
		u := UsageLimits{Provider: p.ID(), Name: info.Name, Windows: []UsageWindow{}}
		rep, ok := p.(UsageReporter)
		switch {
		case !ok:
			u.Error = info.Name + " does not report usage limits"
		case !info.Available:
			u.Supported, u.Error = true, info.Name+" is not available: "+info.Error
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := rep.Usage(ctx)
				now := time.Now()
				got.Provider, got.Name, got.Supported, got.FetchedAt = u.Provider, u.Name, true, &now
				if got.Windows == nil {
					got.Windows = []UsageWindow{}
				}
				if err != nil {
					got.Error = err.Error()
				}
				out[i] = got
			}()
			continue
		}
		out[i] = u
	}
	wg.Wait()
	r.usage.limits, r.usage.fetched = out, time.Now()
	return out
}
