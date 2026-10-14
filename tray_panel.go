package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/skymanrm/aibridge/bridge"
)

// trayPanel is the usage overview at the top of the menu bar menu: every AI tool with its limits.
type trayPanel struct {
	Updated   string          `json:"updated"` // "Updated just now", "Updated 5m ago" or "Checking…"
	Providers []panelProvider `json:"providers"`
	Empty     string          `json:"empty,omitempty"` // shown instead of providers
}

type panelProvider struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Plan   string       `json:"plan,omitempty"`
	Status string       `json:"status"` // "Idle", "Running" or "2 running"
	Busy   bool         `json:"busy"`
	Note   string       `json:"note,omitempty"` // why there are no limits
	Limits []panelLimit `json:"limits"`
}

type panelLimit struct {
	Label   string  `json:"label"`
	Reset   string  `json:"reset,omitempty"`
	Percent float64 `json:"percent"`
}

// buildTrayPanel lists the AI tools that report usage, like the window does; nil limits means not fetched yet.
func buildTrayPanel(limits []bridge.UsageLimits, running map[string]int, now time.Time) trayPanel {
	p := trayPanel{Updated: "Checking…", Providers: []panelProvider{}}
	if limits == nil {
		return p
	}
	var fetched time.Time
	for _, u := range limits {
		if !u.Supported || (len(u.Windows) == 0 && strings.Contains(u.Error, "is not available")) {
			continue
		}
		if u.FetchedAt != nil && u.FetchedAt.After(fetched) {
			fetched = *u.FetchedAt
		}
		pp := panelProvider{ID: u.Provider, Name: u.Name, Plan: u.Plan, Status: "Idle", Limits: []panelLimit{}}
		if n := running[u.Provider]; n > 0 {
			pp.Busy, pp.Status = true, "Running"
			if n > 1 {
				pp.Status = fmt.Sprintf("%d running", n)
			}
		}
		for _, w := range u.Windows {
			label := w.Label
			if label == "" {
				label = w.ID
			}
			l := panelLimit{Label: panelLabel(label), Percent: math.Max(0, math.Min(100, w.UsedPercent))}
			if w.ResetsAt != nil {
				l.Reset = "resets " + resetSpan(w.ResetsAt.Sub(now))
			}
			pp.Limits = append(pp.Limits, l)
		}
		if len(pp.Limits) == 0 {
			pp.Note = u.Error
			if pp.Note == "" {
				pp.Note = "No usage limits reported"
			}
		}
		p.Providers = append(p.Providers, pp)
	}
	if len(p.Providers) == 0 {
		p.Empty = "No AI tool reports usage limits."
	}
	if !fetched.IsZero() {
		p.Updated = "Updated " + updatedAgo(now.Sub(fetched))
	}
	return p
}

// panelLabel shortens CLI labels: "Current week (Fable)" → "Weekly · Fable", "5h limit" → "Session · 5h".
func panelLabel(label string) string {
	l := strings.TrimSuffix(strings.TrimPrefix(label, "Current "), " limit")
	if i := strings.Index(l, " ("); i >= 0 && strings.HasSuffix(l, ")") {
		l = l[:i] + " · " + l[i+2:len(l)-1]
	}
	head, rest, _ := strings.Cut(l, " · ")
	switch strings.ToLower(head) {
	case "session", "5h":
		head = "Session · 5h"
	case "week", "weekly":
		head = "Weekly"
	}
	if rest != "" {
		head += " · " + rest
	}
	if head == "" {
		return ""
	}
	return strings.ToUpper(head[:1]) + head[1:]
}

// resetSpan is a two-unit time to reset: "45m", "2h 14m" or "3d 5h".
func resetSpan(d time.Duration) string {
	mins := int(math.Ceil(d.Minutes()))
	switch {
	case mins <= 0:
		return "now"
	case mins < 60:
		return fmt.Sprintf("%dm", mins)
	case mins < 24*60:
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dd %dh", mins/(24*60), mins%(24*60)/60)
}

// updatedAgo is "just now", "5m ago" or "2h ago".
func updatedAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}
