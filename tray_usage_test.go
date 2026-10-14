package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"slices"
	"testing"
	"time"

	"github.com/skymanrm/aibridge/bridge"
)

func testLimits(now time.Time) []bridge.UsageLimits {
	resets := now.Add(time.Hour)
	return []bridge.UsageLimits{
		{Provider: "claude", Name: "Claude Code", Windows: []bridge.UsageWindow{
			{ID: "session", UsedPercent: 30, ResetsAt: &resets}, {ID: "week", UsedPercent: 47}}},
		{Provider: "codex", Name: "Codex", Windows: []bridge.UsageWindow{{ID: "session", UsedPercent: 120}}},
		{Provider: "gemini", Name: "Gemini CLI", Windows: []bridge.UsageWindow{}},
	}
}

func TestBuildTrayMeter(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local)
	running := map[string]int{"codex": 2, "gemini": 1}
	m := buildTrayMeter(bridge.TrayUsage{Style: "vertical", Window: "session"}, testLimits(now), running, now)
	if !m.Vertical || !m.Icon || !m.Dots || !slices.Equal(m.Percents, []float64{30, 100}) || len(m.Lines) != 2 {
		t.Fatalf("meter %+v", m)
	}
	if !slices.Equal(m.Active, []int{0, 2}) || m.Other != 1 || !slices.Equal(m.IDs, []string{"claude", "codex"}) || !m.Tools || m.Resets != nil {
		t.Errorf("activity %v, other %d, ids %v, tools %v, resets %v", m.Active, m.Other, m.IDs, m.Tools, m.Resets)
	}
	r := buildTrayMeter(bridge.TrayUsage{Style: "horizontal", Window: "session", ShowReset: true, HideToolIcons: true}, testLimits(now), nil, now)
	if r.Tools || !slices.Equal(r.Resets, []string{"1h", ""}) {
		t.Errorf("resets %q, tools %v", r.Resets, r.Tools)
	}
	if m.Lines[0] != "Claude Code: 30% of 5-hour limit, resets 13:00" || m.Lines[1] != "Codex: 120% of 5-hour limit, 2 running" {
		t.Errorf("lines %q", m.Lines)
	}
	// Without dots, every running request counts towards the number next to the icon.
	m = buildTrayMeter(bridge.TrayUsage{Style: "vertical", Window: "session", HideIcon: true, HideActivity: true}, testLimits(now), running, now)
	if m.Icon || m.Dots || m.Other != 3 {
		t.Errorf("hidden icon and dots %+v", m)
	}

	m = buildTrayMeter(bridge.TrayUsage{Style: "horizontal", Window: "week", Hidden: []string{"codex"}}, testLimits(now), nil, now)
	if m.Vertical || !slices.Equal(m.Percents, []float64{47}) {
		t.Errorf("weekly %+v", m)
	}
	// With every AI tool unticked there are no bars, so the sparkle stays even when hidden.
	m = buildTrayMeter(bridge.TrayUsage{Style: "vertical", Window: "session", HideIcon: true, Hidden: []string{"claude", "codex"}}, testLimits(now), nil, now)
	if len(m.Percents) != 0 || !m.Icon {
		t.Errorf("no bars %+v", m)
	}
	m = buildTrayMeter(bridge.TrayUsage{Style: "horizontal", Window: "session", HideIcon: true}, nil, nil, now)
	if !m.Icon {
		t.Errorf("before usage loads %+v", m)
	}
	// Off keeps the menu lines but draws no bars, and the icon can't be hidden then.
	m = buildTrayMeter(bridge.TrayUsage{Style: "off", Window: "session", HideIcon: true}, testLimits(now), nil, now)
	if len(m.Percents) != 0 || len(m.Lines) != 2 || !m.Icon {
		t.Errorf("off %+v", m)
	}
}

func TestMeterIconICO(t *testing.T) {
	for _, vertical := range []bool{false, true} {
		ico := meterIconICO(trayMeter{Vertical: vertical, Dots: true, Tools: true, Percents: []float64{30, 80, 95},
			IDs: []string{"claude", "codex", "gemini"}, Active: []int{1, 0, 0}}, 32)
		var head struct {
			Reserved, Type, Count uint16
			W, H, Colors, _       uint8
			Planes, Bits          uint16
			Size, Offset          uint32
		}
		if err := binary.Read(bytes.NewReader(ico), binary.LittleEndian, &head); err != nil {
			t.Fatal(err)
		}
		if head.Type != 1 || head.Count != 1 || head.W != 32 || head.Offset != 22 || int(head.Size) != len(ico)-22 {
			t.Fatalf("ico header %+v", head)
		}
		img, err := png.Decode(bytes.NewReader(ico[22:]))
		if err != nil || img.Bounds().Dx() != 32 {
			t.Fatalf("png: %v", err)
		}
		if _, _, _, a := img.At(16, 16).RGBA(); a == 0 {
			t.Error("tile center should be opaque")
		}
	}
	// Monochrome uses no level colors: every opaque bar pixel is gray.
	ico := meterIconICO(trayMeter{Mono: true, Dots: true, Tools: true, Percents: []float64{95}, IDs: []string{"claude"}, Active: []int{1}}, 32)
	img, _ := png.Decode(bytes.NewReader(ico[22:]))
	for y := range 32 {
		for x := range 32 {
			r, g, b, _ := img.At(x, y).RGBA()
			if r>>8 > b>>8+40 || g>>8 > b>>8+40 {
				t.Fatalf("colored pixel at %d,%d", x, y)
			}
		}
	}
}

func TestResetIn(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second: "0m", 44*time.Minute + time.Second: "45m", 80 * time.Minute: "1h", 100 * time.Minute: "2h",
		23*time.Hour + 50*time.Minute: "24h", 60 * time.Hour: "3d",
	} {
		if got := resetIn(d); got != want {
			t.Errorf("resetIn(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestBuildTrayPanel(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local)
	if p := buildTrayPanel(nil, nil, now); p.Updated != "Checking…" || len(p.Providers) != 0 || p.Empty != "" {
		t.Errorf("before usage loads %+v", p)
	}
	fetched := now.Add(-5 * time.Minute)
	limits := testLimits(now)
	limits[0].FetchedAt, limits[0].Supported, limits[0].Plan = &fetched, true, "Max"
	limits[0].Windows[0].Label, limits[0].Windows[1].Label = "Current session", "Current week (all models)"
	limits[1].Supported = true
	limits[2].Supported, limits[2].Error = true, "Gemini CLI is not available: not found"
	p := buildTrayPanel(limits, map[string]int{"codex": 2}, now)
	if p.Updated != "Updated 5m ago" || len(p.Providers) != 2 {
		t.Fatalf("panel %+v", p)
	}
	c := p.Providers[0]
	if c.Name != "Claude Code" || c.Plan != "Max" || c.Status != "Idle" || c.Busy || len(c.Limits) != 2 {
		t.Errorf("claude %+v", c)
	}
	if l := c.Limits[0]; l.Label != "Session · 5h" || l.Reset != "resets 1h 0m" || l.Percent != 30 {
		t.Errorf("session %+v", l)
	}
	if l := c.Limits[1]; l.Label != "Weekly · all models" || l.Reset != "" {
		t.Errorf("week %+v", l)
	}
	if x := p.Providers[1]; !x.Busy || x.Status != "2 running" || x.Limits[0].Percent != 100 {
		t.Errorf("codex %+v", x)
	}
	if p := buildTrayPanel([]bridge.UsageLimits{limits[2]}, nil, now); p.Empty == "" {
		t.Errorf("nothing reports %+v", p)
	}
}

func TestPanelLabel(t *testing.T) {
	for in, want := range map[string]string{
		"Current session": "Session · 5h", "Current week (all models)": "Weekly · all models", "Current week (Fable)": "Weekly · Fable",
		"5h limit": "Session · 5h", "Weekly limit": "Weekly", "Spark · 5h limit": "Spark · 5h", "3-day limit": "3-day",
	} {
		if got := panelLabel(in); got != want {
			t.Errorf("panelLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResetSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second: "now", 44*time.Minute + time.Second: "45m", 134 * time.Minute: "2h 14m", 77 * time.Hour: "3d 5h",
	} {
		if got := resetSpan(d); got != want {
			t.Errorf("resetSpan(%v) = %q, want %q", d, got, want)
		}
	}
}
