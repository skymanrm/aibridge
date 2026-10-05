package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct {
	id     string
	deltas []string
	err    error
	gotReq ChatRequest
}

func (f *fakeProvider) ID() string { return f.id }

func (f *fakeProvider) Detect(context.Context) ProviderInfo {
	return ProviderInfo{ID: f.id, Name: "Fake", Available: true, DefaultModel: "m1",
		Models: []Model{{ID: "m1", Name: "M1"}}, Efforts: []string{"low", "high"}}
}

func (f *fakeProvider) Run(_ context.Context, req ChatRequest, emit func(string)) (ChatResult, error) {
	f.gotReq = req
	if f.err != nil {
		return ChatResult{}, f.err
	}
	for _, d := range f.deltas {
		emit(d)
	}
	return ChatResult{Text: strings.Join(f.deltas, ""), Model: req.Model, Usage: Usage{InputTokens: 3, OutputTokens: 2}}, nil
}

const testOrigin = "https://app.example.com"

func newTestServer(t *testing.T, p Provider) (*Server, *Config) {
	t.Helper()
	t.Setenv("AI_BRIDGE_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.AllowOrigin(testOrigin + "/some/path"); err != nil {
		t.Fatal(err)
	}
	return NewServer(cfg, NewRegistry(time.Minute, p)), cfg
}

func do(s *Server, method, path, origin, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:7777"+path, strings.NewReader(body))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

type sseEvent struct {
	Name string
	Data map[string]any
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	for _, chunk := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var ev sseEvent
		for _, line := range strings.Split(chunk, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				ev.Name = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				if err := json.Unmarshal([]byte(v), &ev.Data); err != nil {
					t.Fatalf("bad data %q: %v", v, err)
				}
			}
		}
		events = append(events, ev)
	}
	return events
}

func TestHealthReportsAuthorization(t *testing.T) {
	s, cfg := newTestServer(t, &fakeProvider{id: "fake"})
	for token, want := range map[string]bool{"": false, "wrong": false, cfg.Token: true} {
		rec := do(s, "GET", "/health", testOrigin, token, "")
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != 200 || body["authorized"] != want {
			t.Errorf("token %q: code %d body %v", token, rec.Code, body)
		}
	}
}

func TestOriginAllowlistAndCORS(t *testing.T) {
	s, _ := newTestServer(t, &fakeProvider{id: "fake"})
	if rec := do(s, "GET", "/health", "https://evil.example", "", ""); rec.Code != 403 {
		t.Errorf("foreign origin: got %d", rec.Code)
	}
	req := httptest.NewRequest("OPTIONS", "http://127.0.0.1:7777/v1/chat", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	h := rec.Header()
	if rec.Code != 204 || h.Get("Access-Control-Allow-Origin") != testOrigin ||
		h.Get("Access-Control-Allow-Private-Network") != "true" ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("preflight: %d %v", rec.Code, h)
	}
}

func TestRejectsNonLoopbackHost(t *testing.T) {
	s, cfg := newTestServer(t, &fakeProvider{id: "fake"})
	req := httptest.NewRequest("GET", "http://attacker.example:7777/v1/providers", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("rebinding host: got %d", rec.Code)
	}
}

func TestProvidersRequiresToken(t *testing.T) {
	s, cfg := newTestServer(t, &fakeProvider{id: "fake"})
	if rec := do(s, "GET", "/v1/providers", testOrigin, "nope", ""); rec.Code != 401 {
		t.Errorf("bad token: got %d", rec.Code)
	}
	rec := do(s, "GET", "/v1/providers", testOrigin, cfg.Token, "")
	var body struct{ Providers []ProviderInfo }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 200 || len(body.Providers) != 1 || body.Providers[0].DefaultModel != "m1" {
		t.Errorf("providers: %d %s", rec.Code, rec.Body)
	}
}

func TestChatStreamsAndDefaultsModel(t *testing.T) {
	fake := &fakeProvider{id: "fake", deltas: []string{"Hel", "lo"}}
	s, cfg := newTestServer(t, fake)
	rec := do(s, "POST", "/v1/chat", testOrigin, cfg.Token,
		`{"provider":"fake","system":"sys","messages":[{"role":"user","content":"hi"}],"effort":"low"}`)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}
	events := parseSSE(t, rec.Body.String())
	names := []string{}
	for _, e := range events {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"start", "delta", "delta", "done"}) {
		t.Fatalf("events: %v", names)
	}
	done := events[3].Data
	if done["text"] != "Hello" || done["model"] != "m1" || fake.gotReq.Model != "m1" || fake.gotReq.System != "sys" {
		t.Errorf("done: %v req: %+v", done, fake.gotReq)
	}
}

func TestChatValidation(t *testing.T) {
	s, cfg := newTestServer(t, &fakeProvider{id: "fake"})
	cases := map[string]int{
		`{"provider":"nope","messages":[{"role":"user","content":"x"}]}`:                       404,
		`{"provider":"fake","messages":[]}`:                                                    400,
		`{"provider":"fake","model":"--dangerous","messages":[{"role":"user","content":"x"}]}`: 400,
		`{"provider":"fake","effort":"ultra","messages":[{"role":"user","content":"x"}]}`:      400,
		`{"provider":"fake","messages":[{"role":"system","content":"x"}]}`:                     400,
		`not json`: 400,
	}
	for body, want := range cases {
		if rec := do(s, "POST", "/v1/chat", testOrigin, cfg.Token, body); rec.Code != want {
			t.Errorf("%s: got %d want %d (%s)", body, rec.Code, want, rec.Body)
		}
	}
}

func TestChatProviderErrorIsStreamed(t *testing.T) {
	s, cfg := newTestServer(t, &fakeProvider{id: "fake", err: &ProviderError{"provider_failed", "boom"}})
	rec := do(s, "POST", "/v1/chat", testOrigin, cfg.Token, `{"provider":"fake","messages":[{"role":"user","content":"x"}]}`)
	events := parseSSE(t, rec.Body.String())
	last := events[len(events)-1]
	if last.Name != "error" || last.Data["message"] != "boom" {
		t.Errorf("events: %+v", events)
	}
}

func TestActivityRecordsRequestAndResponse(t *testing.T) {
	s, cfg := newTestServer(t, &fakeProvider{id: "fake", deltas: []string{"Hi"}})
	var last Activity
	s.OnActivity = func(a Activity) { last = a }
	do(s, "POST", "/v1/chat", testOrigin, cfg.Token, `{"provider":"fake","messages":[{"role":"user","content":"hello"}]}`)
	var req ChatRequest
	var res map[string]any
	if err := json.Unmarshal(last.Request, &req); err != nil || req.Messages[0].Content != "hello" {
		t.Fatalf("request: %s (%v)", last.Request, err)
	}
	if err := json.Unmarshal(last.Response, &res); err != nil || res["text"] != "Hi" {
		t.Fatalf("response: %s (%v)", last.Response, err)
	}

	s, cfg = newTestServer(t, &fakeProvider{id: "fake", err: &ProviderError{"provider_failed", "boom"}})
	s.OnActivity = func(a Activity) { last = a }
	do(s, "POST", "/v1/chat", testOrigin, cfg.Token, `{"provider":"fake","messages":[{"role":"user","content":"x"}]}`)
	if !strings.Contains(string(last.Response), `"message":"boom"`) {
		t.Errorf("error response: %s", last.Response)
	}
}

func TestConfigReloadsOnChange(t *testing.T) {
	s, cfg := newTestServer(t, &fakeProvider{id: "fake"})
	other := "https://other.example"
	if rec := do(s, "GET", "/health", other, "", ""); rec.Code != 403 {
		t.Fatalf("before allow: %d", rec.Code)
	}
	time.Sleep(10 * time.Millisecond)
	fresh, _ := LoadConfig()
	if _, err := fresh.AllowOrigin(other); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	_ = os.Chtimes(cfg.path, future, future)
	if rec := do(s, "GET", "/health", other, "", ""); rec.Code != 200 {
		t.Errorf("after allow: %d", rec.Code)
	}
}

func fixtureRunner(lines []string, stderr string, err error, gotArgs *[]string, gotStdin *string) Runner {
	return func(_ context.Context, _ string, args []string, _ string, stdin string, _ []string, onLine func([]byte)) (string, error) {
		*gotArgs, *gotStdin = args, stdin
		for _, l := range lines {
			onLine([]byte(l))
		}
		return stderr, err
	}
}

func TestClaudeRunParsesStream(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-haiku-4-5-20251001"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hey, "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"there"}}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Hey, there","usage":{"input_tokens":10,"cache_read_input_tokens":5,"output_tokens":7}}`,
	}
	var args []string
	var stdin string
	p := &ClaudeProvider{Bin: "claude", Runner: fixtureRunner(lines, "", nil, &args, &stdin)}
	var deltas []string
	res, err := p.Run(context.Background(), ChatRequest{Model: "haiku", System: "Be brief", Effort: "low",
		Messages: []Message{{Role: "user", Content: "hi"}}}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hey, there" || res.Model != "claude-haiku-4-5-20251001" || res.Usage.InputTokens != 15 ||
		res.Usage.OutputTokens != 7 || !slices.Equal(deltas, []string{"Hey, ", "there"}) {
		t.Errorf("result %+v deltas %v", res, deltas)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--tools  ", "--system-prompt Be brief", "--model haiku", "--effort low", "--setting-sources  "} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if stdin != "hi" {
		t.Errorf("stdin %q", stdin)
	}
}

func TestClaudeRunReportsErrors(t *testing.T) {
	var args []string
	var stdin string
	p := &ClaudeProvider{Bin: "claude", Runner: fixtureRunner(
		[]string{`{"type":"result","subtype":"success","is_error":true,"result":"model not found"}`}, "", nil, &args, &stdin)}
	if _, err := p.Run(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}, func(string) {}); err == nil || err.Error() != "model not found" {
		t.Errorf("is_error: %v", err)
	}
	p.Runner = fixtureRunner(nil, "not logged in", context.DeadlineExceeded, &args, &stdin)
	if _, err := p.Run(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}, func(string) {}); err == nil || err.Error() != "not logged in" {
		t.Errorf("no result: %v", err)
	}
}

func TestCodexRunParsesStream(t *testing.T) {
	lines := []string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"metadata warning"}}`,
		`{"type":"turn.started"}`,
		`{"type":"item.updated","item":{"id":"item_1","type":"agent_message","text":"Sal"}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Salut !"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":50,"output_tokens":6}}`,
	}
	var args []string
	var stdin string
	p := &CodexProvider{Bin: "codex", Runner: fixtureRunner(lines, "", nil, &args, &stdin)}
	var deltas []string
	res, err := p.Run(context.Background(), ChatRequest{Model: "gpt-5.5", System: "Say \"hi\"\nin French", Effort: "low",
		Messages: []Message{{Role: "user", Content: "hi"}}}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Salut !" || res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 6 ||
		!slices.Equal(deltas, []string{"Sal", "ut !"}) {
		t.Errorf("result %+v deltas %v", res, deltas)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{`developer_instructions="Say \"hi\"\nin French"`, "-m gpt-5.5",
		`model_reasoning_effort="low"`, "--sandbox read-only", "--ignore-user-config"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if args[len(args)-1] != "-" {
		t.Errorf("prompt must come from stdin: %v", args)
	}
}

func TestCodexRunReportsTurnFailure(t *testing.T) {
	lines := []string{
		`{"type":"error","message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"model not supported\"}}"}`,
		`{"type":"turn.failed","error":{"message":"{\"type\":\"error\",\"status\":400,\"error\":{\"message\":\"model not supported\"}}"}}`,
	}
	var args []string
	var stdin string
	p := &CodexProvider{Bin: "codex", Runner: fixtureRunner(lines, "", nil, &args, &stdin)}
	_, err := p.Run(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}, func(string) {})
	if err == nil || err.Error() != "model not supported" {
		t.Errorf("err: %v", err)
	}
}

func TestParseCodexModels(t *testing.T) {
	catalog := `{"models":[
		{"slug":"b","display_name":"B","visibility":"list","priority":5,"supported_reasoning_levels":[{"effort":"low"},{"effort":"ultra"}]},
		{"slug":"hidden","visibility":"hide","priority":1},
		{"slug":"a","display_name":"A","visibility":"list","priority":2,"supported_reasoning_levels":[{"effort":"medium"}]}]}`
	models, err := ParseCodexModels([]byte(catalog))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "a" || models[1].ID != "b" || !slices.Equal(models[1].Efforts, []string{"low"}) {
		t.Errorf("models: %+v", models)
	}
}

func TestPromptFlattensConversation(t *testing.T) {
	req := ChatRequest{Messages: []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}}
	p := req.Prompt()
	if !strings.Contains(p, "<user>\na\n</user>") || !strings.Contains(p, "<assistant>\nb\n</assistant>") {
		t.Errorf("prompt: %s", p)
	}
}

func TestNormalizeOrigin(t *testing.T) {
	if o, err := NormalizeOrigin("HTTPS://Site.Example:8443/x?y"); err != nil || o != "https://site.example:8443" {
		t.Errorf("got %q %v", o, err)
	}
	for _, bad := range []string{"site.example", "ftp://x", ""} {
		if _, err := NormalizeOrigin(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

type fakeImageProvider struct {
	fakeProvider
	gotImage ImageRequest
}

func (f *fakeImageProvider) Image(_ context.Context, req ImageRequest, emit func(string)) (ImageResult, error) {
	f.gotImage = req
	emit("Drawing")
	return ImageResult{Images: []Image{{Mime: "image/png", Data: []byte("png")}}, Text: "A leaf", Model: req.Model}, nil
}

func TestImageStreamsBase64Images(t *testing.T) {
	fake := &fakeImageProvider{fakeProvider: fakeProvider{id: "fake"}}
	s, cfg := newTestServer(t, fake)
	rec := do(s, "POST", "/v1/image", testOrigin, cfg.Token, `{"provider":"fake","prompt":"a leaf","size":"1536x1024"}`)
	events := parseSSE(t, rec.Body.String())
	last := events[len(events)-1]
	images, _ := last.Data["images"].([]any)
	if rec.Code != 200 || last.Name != "done" || len(images) != 1 || last.Data["model"] != "m1" {
		t.Fatalf("image: %d %+v", rec.Code, events)
	}
	if img := images[0].(map[string]any); img["mime"] != "image/png" || img["data"] != "cG5n" {
		t.Errorf("image payload: %v", img)
	}
	if fake.gotImage.Size != "1536x1024" || fake.gotImage.Model != "m1" {
		t.Errorf("request: %+v", fake.gotImage)
	}
	var body struct{ Providers []ProviderInfo }
	_ = json.Unmarshal(do(s, "GET", "/v1/providers", testOrigin, cfg.Token, "").Body.Bytes(), &body)
	if !slices.Equal(body.Providers[0].Capabilities, []string{"chat", "image"}) {
		t.Errorf("capabilities: %v", body.Providers[0].Capabilities)
	}
}

func TestImageValidation(t *testing.T) {
	s, cfg := newTestServer(t, &fakeProvider{id: "fake"})
	cases := map[string]int{
		`{"provider":"fake","prompt":"x"}`:                           400, // chat-only provider
		`{"provider":"fake","prompt":" "}`:                           400,
		`{"provider":"fake","prompt":"x","size":"huge"}`:             400,
		`{"provider":"nope","prompt":"x"}`:                           404,
		`{"provider":"fake","prompt":"x","model":"--bad","size":""}`: 400,
	}
	for body, want := range cases {
		if rec := do(s, "POST", "/v1/image", testOrigin, cfg.Token, body); rec.Code != want {
			t.Errorf("%s: got %d want %d (%s)", body, rec.Code, want, rec.Body)
		}
	}
}

func TestCodexImageReadsGeneratedFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "generated_images", "thr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "exec-1.png"), []byte("png"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	lines := []string{
		`{"type":"thread.started","thread_id":"thr"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"A leaf."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":3}}`,
	}
	var args []string
	var stdin string
	p := &CodexProvider{Bin: "codex", Home: home, Runner: fixtureRunner(lines, "", nil, &args, &stdin)}
	res, err := p.Image(context.Background(), ImageRequest{Prompt: "a leaf", Size: "1024x1024"}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || string(res.Images[0].Data) != "png" || res.Images[0].Mime != "image/png" || res.Text != "A leaf." {
		t.Errorf("result: %+v", res)
	}
	if !strings.Contains(stdin, "a leaf") || !strings.Contains(stdin, "1024x1024") ||
		!strings.Contains(strings.Join(args, " "), "image_gen") {
		t.Errorf("stdin %q args %v", stdin, args)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("generated dir should be removed: %v", err)
	}
}

func TestCodexImageWithoutFilesFails(t *testing.T) {
	lines := []string{
		`{"type":"thread.started","thread_id":"none"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I can't draw that."}}`,
		`{"type":"turn.completed"}`,
	}
	var args []string
	var stdin string
	p := &CodexProvider{Bin: "codex", Home: t.TempDir(), Runner: fixtureRunner(lines, "", nil, &args, &stdin)}
	_, err := p.Image(context.Background(), ImageRequest{Prompt: "x"}, func(string) {})
	var perr *ProviderError
	if !errors.As(err, &perr) || perr.Code != "no_image" || !strings.Contains(perr.Message, "can't draw") {
		t.Errorf("err: %v", err)
	}
}

func TestGeminiRunParsesStream(t *testing.T) {
	lines := []string{
		`{"type":"init","session_id":"s","model":"flash"}`,
		`{"type":"message","role":"user","content":"hi"}`,
		`{"type":"message","role":"assistant","content":"Bon","delta":true}`,
		`{"type":"message","role":"assistant","content":"jour","delta":true}`,
		`{"type":"result","status":"success","stats":{"input_tokens":12,"output_tokens":3,"models":{"gemini-3.8-flash":{}}}}`,
	}
	var gotArgs, gotEnv []string
	var gotStdin string
	var policy, system []byte
	p := &GeminiProvider{Bin: "gemini", Runner: func(_ context.Context, _ string, args []string, dir, stdin string, env []string, onLine func([]byte)) (string, error) {
		gotArgs, gotStdin, gotEnv = args, stdin, env
		policy, _ = os.ReadFile(filepath.Join(dir, geminiPolicyFile))
		system, _ = os.ReadFile(filepath.Join(dir, geminiSystemFile))
		for _, l := range lines {
			onLine([]byte(l))
		}
		return "", nil
	}}
	var deltas []string
	res, err := p.Run(context.Background(), ChatRequest{Model: "flash", System: "Answer in French",
		Messages: []Message{{Role: "user", Content: "/hi"}}}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Bonjour" || res.Model != "gemini-3.8-flash" || res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 3 ||
		!slices.Equal(deltas, []string{"Bon", "jour"}) {
		t.Errorf("result %+v deltas %v", res, deltas)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"--output-format stream-json", "--extensions none", "--model flash", "--policy "} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, gotArgs)
		}
	}
	if !strings.Contains(string(policy), `decision = "deny"`) || string(system) != "Answer in French" {
		t.Errorf("policy %q system %q", policy, system)
	}
	if !strings.Contains(strings.Join(gotEnv, " "), "GEMINI_SYSTEM_MD=") {
		t.Errorf("env: %v", gotEnv)
	}
	if strings.HasPrefix(gotStdin, "/") {
		t.Errorf("slash prompt must be wrapped: %q", gotStdin)
	}
}

func TestGeminiRunReportsErrors(t *testing.T) {
	apiErr := `[API Error: {"error":{"message":"{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"API key not valid.\"\n  }\n}\n","code":400}}]`
	line, _ := json.Marshal(map[string]any{"type": "result", "status": "error", "error": map[string]string{"message": apiErr}})
	var args []string
	var stdin string
	p := &GeminiProvider{Bin: "gemini", Runner: fixtureRunner([]string{string(line)}, "", nil, &args, &stdin)}
	if _, err := p.Run(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}, func(string) {}); err == nil || err.Error() != "API key not valid." {
		t.Errorf("result error: %v", err)
	}
	p.Runner = fixtureRunner(nil, "Please set an Auth method", errors.New("exit status 41"), &args, &stdin)
	if _, err := p.Run(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}, func(string) {}); err == nil || err.Error() != "Please set an Auth method" {
		t.Errorf("no result: %v", err)
	}
}

func TestRegistryTest(t *testing.T) {
	reg := NewRegistry(time.Minute, &fakeProvider{id: "fake", deltas: []string{"OK"}})
	if r := reg.Test(context.Background(), "fake"); !r.OK || r.Text != "OK" || r.Model != "m1" {
		t.Errorf("ok: %+v", r)
	}
	reg = NewRegistry(time.Minute, &fakeProvider{id: "fake", err: &ProviderError{"provider_failed", "not logged in"}})
	if r := reg.Test(context.Background(), "fake"); r.OK || r.Error != "not logged in" {
		t.Errorf("fail: %+v", r)
	}
	if r := reg.Test(context.Background(), "nope"); r.OK || r.Error == "" {
		t.Errorf("unknown: %+v", r)
	}
}

func TestIntegrationPrompt(t *testing.T) {
	cfg := &Config{Port: 7777, Origins: []string{testOrigin}, MaxConcurrent: 2, TimeoutSec: 300}
	providers := []ProviderInfo{
		{ID: "codex", Name: "Codex", Available: true, DefaultModel: "gpt-5.5", Models: []Model{{ID: "gpt-5.5"}},
			Efforts: []string{"low", "high"}, Capabilities: []string{"chat", "image"}},
		{ID: "gemini", Name: "Gemini CLI", Available: false},
	}
	out, err := IntegrationPrompt(cfg, providers, "Add a summarize button to notes")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"http://127.0.0.1:7777", "`" + testOrigin + "`", "Add a summarize button to notes",
		"`codex` (Codex): models `gpt-5.5`; default `gpt-5.5`; efforts `low`, `high`; can generate images"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(out, "`gemini`") {
		t.Error("unavailable providers must be left out")
	}
	out, _ = IntegrationPrompt(&Config{Port: 9000}, nil, " ")
	if !strings.Contains(out, "propose 2-3 places") || !strings.Contains(out, "none yet") {
		t.Errorf("defaults: %s", out[:400])
	}
}
