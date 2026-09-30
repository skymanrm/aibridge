package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// scriptedProvider answers each Run with the next reply.
type scriptedProvider struct {
	fakeProvider
	replies []string
	calls   []ChatRequest
}

func (p *scriptedProvider) Run(_ context.Context, req ChatRequest, _ func(string)) (ChatResult, error) {
	p.calls = append(p.calls, req)
	reply := p.replies[min(len(p.calls), len(p.replies))-1]
	return ChatResult{Text: reply, Model: req.Model, Usage: Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

type fakeCapture struct {
	mu       sync.Mutex
	requests []captureRequest
	reject   int // number of first captures rejected with an issue
}

func (f *fakeCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/health" {
		_, _ = w.Write([]byte(`{"ok":true,"n8n_version":"2.41.4"}`))
		return
	}
	var req captureRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	n := len(f.requests)
	f.mu.Unlock()
	if n <= f.reject {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_workflow","message":"Some nodes show warnings in n8n",
			"issues":[{"node":"Fetch","message":"Parameter \"URL\" is required."}]}}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"image": map[string]any{"mime": "image/png", "data": []byte("PNG")},
		"workflow": json.RawMessage(req.Workflow), "issues": []any{}, "notes": []string{}, "n8n_version": "2.41.4"})
}

const testWorkflow = `{"name":"Digest","nodes":[{"name":"Fetch","type":"n8n-nodes-base.httpRequest"}],"connections":{}}`

func newStudioServer(t *testing.T, p Provider, capture *fakeCapture) *Server {
	t.Helper()
	s, _ := newTestServer(t, p)
	ts := httptest.NewServer(capture)
	t.Cleanup(ts.Close)
	s.studio.Docker, s.studio.CaptureURL = "docker", ts.URL
	if _, err := s.studio.syncFiles(); err != nil { // files in place + healthy service: no `docker compose up`
		t.Fatal(err)
	}
	return s
}

func TestN8nScreenshotRetriesWithIssues(t *testing.T) {
	p := &scriptedProvider{fakeProvider: fakeProvider{id: "fake"},
		replies: []string{"Here you go:\n```json\n" + testWorkflow + "\n```", testWorkflow}}
	capture := &fakeCapture{reject: 1}
	s := newStudioServer(t, p, capture)
	rec := do(s, "POST", "/v1/n8n/screenshot", testOrigin, s.config().Token,
		`{"provider":"fake","prompt":"RSS to Telegram","size":"1000x1000","theme":"dark"}`)
	events := parseSSE(t, rec.Body.String())
	last := events[len(events)-1]
	if last.Name != "done" {
		t.Fatalf("last event %q: %s", last.Name, last.Data)
	}
	var done struct {
		Images   []Image         `json:"images"`
		Workflow json.RawMessage `json:"workflow"`
		Attempts int             `json:"attempts"`
		Usage    Usage           `json:"usage"`
		Text     string          `json:"text"`
	}
	if err := json.Unmarshal(mustJSON(t, last.Data), &done); err != nil {
		t.Fatal(err)
	}
	if done.Attempts != 2 || len(done.Images) != 1 || string(done.Images[0].Data) != "PNG" {
		t.Fatalf("unexpected done: %+v", done)
	}
	if done.Usage.InputTokens != 20 || !strings.Contains(done.Text, "Digest — 1 nodes") {
		t.Fatalf("usage/text: %+v %q", done.Usage, done.Text)
	}
	if len(capture.requests) != 2 || !capture.requests[0].Strict || !capture.requests[1].Strict {
		t.Fatalf("captures: %+v", capture.requests)
	}
	first := capture.requests[0]
	if first.Width != 1000 || first.Height != 1000 || first.Theme != "dark" || first.Layout != "auto" || first.Scale != 2 {
		t.Fatalf("capture options: %+v", first)
	}
	fix := p.calls[1].Messages
	if len(fix) != 3 || fix[1].Role != "assistant" || !strings.Contains(fix[2].Content, `Node "Fetch": Parameter "URL" is required.`) {
		t.Fatalf("fix conversation: %+v", fix)
	}
	if !strings.Contains(p.calls[0].System, "n8n expert") {
		t.Fatal("system prompt not sent")
	}
}

func TestN8nScreenshotLastAttemptIsLenient(t *testing.T) {
	p := &scriptedProvider{fakeProvider: fakeProvider{id: "fake"}, replies: []string{testWorkflow}}
	capture := &fakeCapture{reject: 2}
	s := newStudioServer(t, p, capture)
	rec := do(s, "POST", "/v1/n8n/screenshot", testOrigin, s.config().Token, `{"provider":"fake","prompt":"x"}`)
	events := parseSSE(t, rec.Body.String())
	if last := events[len(events)-1]; last.Name != "done" {
		t.Fatalf("last event %q: %s", last.Name, last.Data)
	}
	if len(capture.requests) != studioAttempts || capture.requests[studioAttempts-1].Strict {
		t.Fatalf("captures: %+v", capture.requests)
	}
}

func TestN8nScreenshotRendersGivenWorkflowWithoutAI(t *testing.T) {
	p := &scriptedProvider{fakeProvider: fakeProvider{id: "fake"}, replies: []string{"unused"}}
	capture := &fakeCapture{}
	s := newStudioServer(t, p, capture)
	rec := do(s, "POST", "/v1/n8n/screenshot", testOrigin, s.config().Token,
		`{"workflow":`+testWorkflow+`,"frame":"canvas","layout":"keep"}`)
	events := parseSSE(t, rec.Body.String())
	if last := events[len(events)-1]; last.Name != "done" {
		t.Fatalf("last event %q: %s", last.Name, last.Data)
	}
	if len(p.calls) != 0 || len(capture.requests) != 1 || capture.requests[0].Frame != "canvas" || capture.requests[0].Layout != "keep" {
		t.Fatalf("calls %d, captures %+v", len(p.calls), capture.requests)
	}
}

func TestN8nValidation(t *testing.T) {
	s := newStudioServer(t, &fakeProvider{id: "fake"}, &fakeCapture{})
	for _, body := range []string{
		`{"provider":"fake"}`,
		`{"prompt":"x"}`,
		`{"provider":"fake","prompt":"x","size":"big"}`,
		`{"provider":"fake","prompt":"x","theme":"neon"}`,
		`{"provider":"fake","prompt":"x","frame":"window"}`,
	} {
		if rec := do(s, "POST", "/v1/n8n/screenshot", testOrigin, s.config().Token, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, rec.Code)
		}
	}
}

func TestN8nStatus(t *testing.T) {
	s := newStudioServer(t, &fakeProvider{id: "fake"}, &fakeCapture{})
	rec := do(s, "GET", "/v1/n8n", testOrigin, s.config().Token, "")
	var st StudioStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Available || !st.Running || st.N8nVersion != "2.41.4" || st.EditorURL != studioEditorURL {
		t.Fatalf("status: %+v", st)
	}
}

func TestExtractJSON(t *testing.T) {
	if raw, err := extractJSON("Sure!\n```json\n" + testWorkflow + "\n```\nEnjoy."); err != nil || !json.Valid(raw) {
		t.Fatalf("fenced: %v", err)
	}
	for _, bad := range []string{"no json", `{"name":"x"}`, `{"nodes":[]}`, `{"nodes":[{]}`} {
		if _, err := extractJSON(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
