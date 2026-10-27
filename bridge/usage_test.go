package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const claudeUsageText = `You are currently using your subscription to power your Claude Code usage

Current session: 29% used · resets Oct 13 at 3:50pm (Europe/Moscow)
Current week (all models): 47% used · resets Oct 14 at 6am (Europe/Moscow)
Current week (Fable): 0% used

What's contributing to your limits usage?
Last 24h · 2470 requests · 38 sessions
  66% of your usage was at >150k context
  Top skills: /claude-in-chrome 4%, /ru-text 2%`

func TestParseClaudeUsage(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	now := time.Date(2026, 10, 13, 14, 0, 0, 0, msk)
	got := ParseClaudeUsage(claudeUsageText, now)
	if len(got) != 3 {
		t.Fatalf("windows %+v", got)
	}
	want := []struct {
		id     string
		pct    float64
		mins   int
		resets time.Time
	}{
		{"session", 29, 300, time.Date(2026, 10, 13, 15, 50, 0, 0, msk)},
		{"week", 47, 10080, time.Date(2026, 10, 14, 6, 0, 0, 0, msk)},
		{"week_fable", 0, 10080, time.Time{}},
	}
	for i, w := range want {
		g := got[i]
		if g.ID != w.id || g.UsedPercent != w.pct || g.WindowMinutes != w.mins {
			t.Errorf("window %d: %+v", i, g)
		}
		if w.resets.IsZero() != (g.ResetsAt == nil) || (g.ResetsAt != nil && !g.ResetsAt.Equal(w.resets)) {
			t.Errorf("window %d resets %v, want %v", i, g.ResetsAt, w.resets)
		}
	}
}

func TestParseClaudeResetRollsForward(t *testing.T) {
	now := time.Date(2026, 12, 31, 22, 0, 0, 0, time.UTC)
	if got := parseClaudeReset("Jan 2 at 6am (UTC)", now); got == nil || !got.Equal(time.Date(2027, 1, 2, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("next year: %v", got)
	}
	if got := parseClaudeReset("9:30pm (UTC)", now); got == nil || !got.Equal(time.Date(2027, 1, 1, 21, 30, 0, 0, time.UTC)) {
		t.Errorf("time only: %v", got)
	}
	if got := parseClaudeReset("soon", now); got != nil {
		t.Errorf("unparseable: %v", got)
	}
}

func TestClaudeUsageRunsSlashCommand(t *testing.T) {
	result, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": claudeUsageText})
	var calls [][]string
	p := &ClaudeProvider{Bin: "claude", Runner: func(_ context.Context, _ string, args []string, _, _ string, _ []string, onLine func([]byte)) (string, error) {
		calls = append(calls, args)
		if args[0] == "auth" {
			onLine([]byte(`{`))
			onLine([]byte(`  "loggedIn": true, "subscriptionType": "max"`))
			onLine([]byte(`}`))
			return "", nil
		}
		onLine(result)
		return "", nil
	}}
	u, err := p.Usage(context.Background())
	if err != nil || len(u.Windows) != 3 || u.Plan != "Max" {
		t.Fatalf("usage %+v (%v)", u, err)
	}
	if len(calls) != 2 || calls[0][0] != "-p" || calls[0][1] != "/usage" {
		t.Errorf("calls %q", calls)
	}

	apiKey, _ := json.Marshal(map[string]any{"type": "result", "result": "Usage limits are only shown for subscription plans.\n"})
	p.Runner = fixtureRunner([]string{string(apiKey)}, "", nil, new([]string), new(string))
	if _, err := p.Usage(context.Background()); err == nil || !strings.Contains(err.Error(), "subscription plans") {
		t.Errorf("no windows: %v", err)
	}
	p.Runner = fixtureRunner(nil, "not logged in", errors.New("exit status 1"), new([]string), new(string))
	if _, err := p.Usage(context.Background()); err == nil || err.Error() != "not logged in" {
		t.Errorf("failure: %v", err)
	}
}

const codexRateLimits = `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":84,"windowDurationMins":300,"resetsAt":1791908271},
"secondary":{"usedPercent":27,"windowDurationMins":10080,"resetsAt":1792472869},"planType":"plus"},
"rateLimitsByLimitId":{
 "spark":{"limitId":"spark","limitName":"GPT Spark","primary":{"usedPercent":5,"windowDurationMins":1440,"resetsAt":0},"planType":"plus"},
 "codex":{"limitId":"codex","limitName":null,"primary":{"usedPercent":84,"windowDurationMins":300,"resetsAt":1791908271},
  "secondary":{"usedPercent":27,"windowDurationMins":10080,"resetsAt":1792472869},"planType":"plus"}}}`

func TestParseCodexRateLimits(t *testing.T) {
	u, err := ParseCodexRateLimits([]byte(codexRateLimits))
	if err != nil || u.Plan != "Plus" || len(u.Windows) != 3 {
		t.Fatalf("limits %+v (%v)", u, err)
	}
	s, w, x := u.Windows[0], u.Windows[1], u.Windows[2]
	if s.ID != "session" || s.UsedPercent != 84 || s.WindowMinutes != 300 || s.ResetsAt == nil || s.ResetsAt.Unix() != 1791908271 {
		t.Errorf("session %+v", s)
	}
	if w.ID != "week" || w.Label != "Weekly limit" || w.UsedPercent != 27 {
		t.Errorf("week %+v", w)
	}
	if x.ID != "spark_1d" || x.Label != "GPT Spark · 1-day limit" || x.ResetsAt != nil {
		t.Errorf("extra limit %+v", x)
	}
}

func TestAppServerExchange(t *testing.T) {
	var sent bytes.Buffer
	replies := `{"id":1,"result":{"userAgent":"x"}}
{"method":"remoteControl/status/changed","params":{}}
{"id":2,"result":{"rateLimits":null}}
`
	got, err := appServerExchange(&sent, strings.NewReader(replies), "account/rateLimits/read")
	if err != nil || string(got) != `{"rateLimits":null}` {
		t.Fatalf("result %s (%v)", got, err)
	}
	lines := strings.Split(strings.TrimSpace(sent.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"initialize"`) || !strings.Contains(lines[2], `"account/rateLimits/read"`) {
		t.Errorf("sent %q", lines)
	}

	_, err = appServerExchange(&bytes.Buffer{}, strings.NewReader(`{"id":1,"result":{}}`+"\n"+`{"id":2,"error":{"message":"not logged in"}}`), "m")
	if err == nil || err.Error() != "not logged in" {
		t.Errorf("rpc error: %v", err)
	}
	if _, err = appServerExchange(&bytes.Buffer{}, strings.NewReader(""), "m"); err == nil {
		t.Error("EOF without answer should fail")
	}
}

type usageProvider struct {
	fakeProvider
	calls int
}

func (u *usageProvider) Usage(context.Context) (UsageLimits, error) {
	u.calls++
	return UsageLimits{Plan: "Pro", Windows: []UsageWindow{{ID: "session", UsedPercent: 12}}}, nil
}

func TestUsageEndpoint(t *testing.T) {
	up := &usageProvider{fakeProvider: fakeProvider{id: "fake"}}
	s, cfg := newTestServer(t, up)
	s.registry.providers = append(s.registry.providers, &fakeProvider{id: "plain"})
	if rec := do(s, "GET", "/v1/usage", testOrigin, "nope", ""); rec.Code != 401 {
		t.Errorf("bad token: got %d", rec.Code)
	}
	var body struct{ Providers []UsageLimits }
	for range 2 {
		rec := do(s, "GET", "/v1/usage", testOrigin, cfg.Token, "")
		if err := json.Unmarshal(rec.Body.Bytes(), &body); rec.Code != 200 || err != nil || len(body.Providers) != 2 {
			t.Fatalf("usage: %d %s", rec.Code, rec.Body)
		}
	}
	got, plain := body.Providers[0], body.Providers[1]
	if got.Provider != "fake" || !got.Supported || got.Plan != "Pro" || len(got.Windows) != 1 || got.FetchedAt == nil {
		t.Errorf("fake %+v", got)
	}
	if plain.Supported || plain.Error == "" || plain.Windows == nil {
		t.Errorf("plain %+v", plain)
	}
	if up.calls != 1 {
		t.Errorf("cached result should be reused, got %d calls", up.calls)
	}
	do(s, "GET", "/v1/usage?refresh=1", testOrigin, cfg.Token, "")
	if up.calls != 2 {
		t.Errorf("refresh=1 should refetch, got %d calls", up.calls)
	}
}

func TestSessionIdle(t *testing.T) {
	now := time.Date(2026, 10, 27, 17, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	session := func(pct float64, resets *time.Time) UsageLimits {
		return UsageLimits{FetchedAt: at(-3 * time.Minute), Windows: []UsageWindow{
			{ID: "session", UsedPercent: pct, WindowMinutes: 300, ResetsAt: resets},
			{ID: "week", UsedPercent: 40, WindowMinutes: 7 * 24 * 60, ResetsAt: at(48 * time.Hour)},
		}}
	}
	for _, tc := range []struct {
		name string
		u    UsageLimits
		want bool
	}{
		{"running", session(12, at(2*time.Hour)), false},
		{"no reset time", session(0, nil), true},
		{"reset passed", session(30, at(-2*time.Minute)), true},
		{"reset just passed", session(30, at(-30*time.Second)), false},
		{"unused, reset a full window after fetch", session(0, at(5*time.Hour-3*time.Minute)), true},
		{"just started", session(0, at(4*time.Hour)), false},
		{"no session window", UsageLimits{Windows: []UsageWindow{{ID: "week"}}}, false},
		{"error", UsageLimits{Error: "boom", Windows: session(0, nil).Windows}, false},
	} {
		if got := tc.u.SessionIdle(now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if !(UsageLimits{Windows: []UsageWindow{{ID: "week", UsedPercent: 100}}}).Exhausted() || session(99, nil).Exhausted() {
		t.Error("Exhausted")
	}
}
