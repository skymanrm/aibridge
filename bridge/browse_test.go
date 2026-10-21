package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

type fakeBrowser struct {
	fakeProvider
	gotBrowse BrowseRequest
}

func (f *fakeBrowser) Browse(_ context.Context, req BrowseRequest, emit func(string)) (BrowseResult, error) {
	f.gotBrowse = req
	emit("Reading the page\n")
	return BrowseResult{Data: json.RawMessage(`{"title":"Ad"}`), Model: req.Model}, nil
}

func TestBrowseStreamsData(t *testing.T) {
	fake := &fakeBrowser{fakeProvider: fakeProvider{id: "fake"}}
	s, cfg := newTestServer(t, fake)
	rec := do(s, "POST", "/v1/browse", testOrigin, cfg.Token,
		`{"provider":"fake","url":"https://ads.telegram.org/account/ad/4","prompt":"Read the ad","schema":{"type":"object"}}`)
	events := parseSSE(t, rec.Body.String())
	last := events[len(events)-1]
	data, _ := last.Data["data"].(map[string]any)
	if rec.Code != 200 || last.Name != "done" || data["title"] != "Ad" || last.Data["model"] != "m1" {
		t.Fatalf("browse: %d %+v", rec.Code, events)
	}
	if fake.gotBrowse.URL != "https://ads.telegram.org/account/ad/4" {
		t.Errorf("request: %+v", fake.gotBrowse)
	}
	var body struct{ Providers []ProviderInfo }
	_ = json.Unmarshal(do(s, "GET", "/v1/providers", testOrigin, cfg.Token, "").Body.Bytes(), &body)
	if !slices.Contains(body.Providers[0].Capabilities, "browse") {
		t.Errorf("capabilities: %v", body.Providers[0].Capabilities)
	}
}

func TestBrowseValidation(t *testing.T) {
	s, cfg := newTestServer(t, &fakeBrowser{fakeProvider: fakeProvider{id: "fake"}})
	for _, body := range []string{
		`{"provider":"fake","url":"file:///etc/passwd","prompt":"x"}`,
		`{"provider":"fake","url":"https://example.com","prompt":" "}`,
		`{"provider":"fake","url":"https://example.com","prompt":"x","schema":[1]}`,
	} {
		if rec := do(s, "POST", "/v1/browse", testOrigin, cfg.Token, body); rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	plain, cfg2 := newTestServer(t, &fakeProvider{id: "fake"})
	rec := do(plain, "POST", "/v1/browse", testOrigin, cfg2.Token, `{"provider":"fake","url":"https://example.com","prompt":"x"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "unsupported") {
		t.Errorf("non-browser provider: %d %s", rec.Code, rec.Body.String())
	}
}

func TestClaudeBrowseParsesStructuredOutput(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-sonnet-5-5"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__claude-in-chrome__navigate","input":{"url":"https://example.com/ad"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"No response to the Claude in Chrome permission prompt"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__claude-in-chrome__get_page_text","input":{}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"{}","structured_output":{"ok":true,"data":{"title":"Ad"}},"usage":{"input_tokens":10,"output_tokens":4}}`,
	}
	var args []string
	var stdin string
	p := &ClaudeProvider{Bin: "claude", Runner: fixtureRunner(lines, "", nil, &args, &stdin)}
	var deltas []string
	res, err := p.Browse(context.Background(), BrowseRequest{URL: "https://example.com/ad", Prompt: "Read it", Model: "sonnet",
		Schema: json.RawMessage(`{"type":"object"}`)}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != `{"title":"Ad"}` || res.Model != "claude-sonnet-5-5" || res.Usage.OutputTokens != 4 {
		t.Errorf("result %+v", res)
	}
	want := []string{"Opening https://example.com/ad\n", "Waiting for Allow in the Claude extension in Chrome\n", "Reading the page\n"}
	if !slices.Equal(deltas, want) {
		t.Errorf("deltas %q", deltas)
	}
	joined := strings.Join(args, " ")
	for _, w := range []string{"--chrome", "--tools  ", "mcp__claude-in-chrome__get_page_text", "--disallowedTools", "--json-schema", "--model sonnet"} {
		if !strings.Contains(joined, w) {
			t.Errorf("args missing %q: %v", w, args)
		}
	}
	allowed := args[slices.Index(args, "--allowedTools")+1]
	if strings.Contains(allowed, "computer") || strings.Contains(allowed, "javascript_tool") || strings.Contains(allowed, "form_input") {
		t.Errorf("unsafe tool allowed: %s", allowed)
	}
	if !strings.Contains(stdin, "URL: https://example.com/ad") || !strings.Contains(stdin, "Read it") {
		t.Errorf("stdin %q", stdin)
	}
}

func TestClaudeBrowseReportsUnreadablePage(t *testing.T) {
	var args []string
	var stdin string
	p := &ClaudeProvider{Bin: "claude", Runner: fixtureRunner([]string{
		`{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"ok":false,"error":"Login required"}}`,
	}, "", nil, &args, &stdin)}
	_, err := p.Browse(context.Background(), BrowseRequest{URL: "https://example.com", Prompt: "x"}, func(string) {})
	var perr *ProviderError
	if !errors.As(err, &perr) || perr.Code != "browse_failed" || perr.Message != "Login required" {
		t.Errorf("err %v", err)
	}
	p.Runner = fixtureRunner([]string{
		`{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"ok":true,"data":"Example Domain"}}`,
	}, "", nil, &args, &stdin)
	res, err := p.Browse(context.Background(), BrowseRequest{URL: "https://example.com", Prompt: "Heading?"}, func(string) {})
	if err != nil || res.Text != "Example Domain" || res.Data != nil {
		t.Errorf("text answer %+v %v", res, err)
	}
}
