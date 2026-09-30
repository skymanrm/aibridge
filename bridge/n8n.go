package bridge

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed all:studio
var studioFiles embed.FS

//go:embed n8n_prompt.md
var n8nSystemPrompt string

const (
	studioCaptureURL = "http://127.0.0.1:7778"
	studioEditorURL  = "http://127.0.0.1:7779"
	studioAttempts   = 3
	// First start builds the capture image and pulls n8n; later starts take seconds.
	studioStartTimeout = 10 * time.Minute
	// n8n runs plus AI retries take longer than a plain chat.
	studioMinTimeout = 600
)

// N8nRequest asks for an n8n workflow screenshot: designed by the AI from Prompt, or Workflow rendered as is.
type N8nRequest struct {
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Effort   string          `json:"effort"`
	Prompt   string          `json:"prompt"`
	Workflow json.RawMessage `json:"workflow"`
	Size     string          `json:"size"`   // WIDTHxHEIGHT of the viewport in CSS px (rendered at 2x)
	Theme    string          `json:"theme"`  // light | dark
	Frame    string          `json:"frame"`  // editor (n8n UI around the canvas) | canvas
	Layout   string          `json:"layout"` // auto (n8n "Tidy up") | keep
}

var viewportSize = regexp.MustCompile(`^([1-9][0-9]{2,3})x([1-9][0-9]{2,3})$`)

func (r N8nRequest) Validate() error {
	hasWorkflow := len(bytes.TrimSpace(r.Workflow)) > 0 && string(bytes.TrimSpace(r.Workflow)) != "null"
	if !hasWorkflow && strings.TrimSpace(r.Prompt) == "" {
		return &ProviderError{"validation_error", "prompt or workflow is required"}
	}
	if !hasWorkflow && r.Provider == "" {
		return &ProviderError{"validation_error", "provider is required to design a workflow"}
	}
	if len(r.Prompt) > maxImagePrompt {
		return &ProviderError{"validation_error", fmt.Sprintf("prompt is longer than %d bytes", maxImagePrompt)}
	}
	if r.Size != "" && !viewportSize.MatchString(r.Size) {
		return &ProviderError{"validation_error", "size must be WIDTHxHEIGHT"}
	}
	for field, v := range map[string][2]string{"theme": {r.Theme, "light dark"}, "frame": {r.Frame, "editor canvas"},
		"layout": {r.Layout, "auto keep"}} {
		if v[0] != "" && !strings.Contains(" "+v[1]+" ", " "+v[0]+" ") {
			return &ProviderError{"validation_error", fmt.Sprintf("%s must be one of: %s", field, v[1])}
		}
	}
	if r.Model != "" && !safeArg.MatchString(r.Model) {
		return &ProviderError{"validation_error", "invalid model"}
	}
	if r.Effort != "" && !safeArg.MatchString(r.Effort) {
		return &ProviderError{"validation_error", "invalid effort"}
	}
	return nil
}

func (r N8nRequest) viewport() (int, int) {
	m := viewportSize.FindStringSubmatch(r.Size)
	if m == nil {
		return 1280, 720
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	return w, h
}

type CaptureIssue struct {
	Node    string `json:"node,omitempty"`
	Message string `json:"message"`
}

type captureRequest struct {
	Workflow json.RawMessage `json:"workflow"`
	Width    int             `json:"width"`
	Height   int             `json:"height"`
	Scale    float64         `json:"scale"`
	Theme    string          `json:"theme,omitempty"`
	Frame    string          `json:"frame,omitempty"`
	Layout   string          `json:"layout,omitempty"`
	Strict   bool            `json:"strict"`
}

type captureResponse struct {
	Image      Image           `json:"image"`
	Workflow   json.RawMessage `json:"workflow"`
	Issues     []CaptureIssue  `json:"issues"`
	Notes      []string        `json:"notes"`
	N8nVersion string          `json:"n8n_version"`
	Error      *struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Issues  []CaptureIssue `json:"issues"`
	} `json:"error"`
}

// N8nResult is the `done` payload of /v1/n8n/screenshot.
type N8nResult struct {
	Images     []Image         `json:"images"`
	Workflow   json.RawMessage `json:"workflow"`
	Issues     []CaptureIssue  `json:"issues"`
	Notes      []string        `json:"notes"`
	Attempts   int             `json:"attempts"`
	N8nVersion string          `json:"n8n_version"`
	Text       string          `json:"text"`
	Model      string          `json:"-"`
	Usage      Usage           `json:"-"`
}

// StudioStatus describes the Docker n8n studio for GET /v1/n8n.
type StudioStatus struct {
	Available  bool   `json:"available"`
	Running    bool   `json:"running"`
	N8nVersion string `json:"n8n_version"`
	EditorURL  string `json:"editor_url"`
	Error      string `json:"error"`
}

// Studio manages the Docker Compose stack (n8n + Playwright capture service); nothing runs on the host but docker.
type Studio struct {
	Docker     string
	Dir        string
	CaptureURL string
	HTTP       *http.Client

	mu sync.Mutex
}

func NewStudio(cfg *Config) *Studio {
	docker, _ := FindBinary("docker", cfg.Binaries["docker"])
	if docker == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if p := filepath.Join(home, ".orbstack", "bin", "docker"); fileExists(p) {
				docker = p
			}
		}
	}
	return &Studio{Docker: docker, Dir: filepath.Join(filepath.Dir(cfg.path), "studio"), CaptureURL: studioCaptureURL,
		HTTP: &http.Client{}}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func (s *Studio) Status(ctx context.Context) StudioStatus {
	st := StudioStatus{Available: s.Docker != "", EditorURL: studioEditorURL}
	if !st.Available {
		st.Error = "docker not found"
		return st
	}
	version, err := s.health(ctx)
	st.Running, st.N8nVersion = err == nil, version
	return st
}

func (s *Studio) health(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.CaptureURL+"/health", nil)
	res, err := s.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var body struct {
		N8nVersion string `json:"n8n_version"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&body) != nil {
		return "", fmt.Errorf("capture service answered %d", res.StatusCode)
	}
	return body.N8nVersion, nil
}

// syncFiles writes the embedded compose project to Dir; reports whether anything changed.
func (s *Studio) syncFiles() (bool, error) {
	changed := false
	err := fs.WalkDir(studioFiles, "studio", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := studioFiles.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(s.Dir, strings.TrimPrefix(path, "studio/"))
		if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, data) {
			return nil
		}
		changed = true
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	return changed, err
}

// Ensure starts (or rebuilds after a bridge update) the stack and waits until the capture service is healthy.
func (s *Studio) Ensure(ctx context.Context, progress func(string)) error {
	if s.Docker == "" {
		return &ProviderError{"studio_unavailable", "docker not found; install OrbStack or Docker Desktop"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed, err := s.syncFiles()
	if err != nil {
		return err
	}
	if !changed {
		if _, err := s.health(ctx); err == nil {
			return nil
		}
	}
	progress("Starting n8n in Docker (the first start builds the images and takes a few minutes)…\n")
	ctx, cancel := context.WithTimeout(ctx, studioStartTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Docker, "compose", "-f", filepath.Join(s.Dir, "compose.yaml"),
		"up", "-d", "--build", "--wait", "--remove-orphans")
	cmd.Dir = s.Dir
	// Apps started from Finder get a minimal PATH; compose needs docker-credential-* helpers next to docker.
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(s.Docker)+":/usr/local/bin:/opt/homebrew/bin:"+os.Getenv("PATH"))
	var out tailBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ProviderError{"studio_failed", "docker compose up failed: " + out.String()}
	}
	_, err = s.health(ctx)
	return err
}

// capture posts a workflow to the capture service; a rejected workflow returns its issues and no error.
func (s *Studio) capture(ctx context.Context, req captureRequest) (*captureResponse, error) {
	payload, _ := json.Marshal(req)
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.CaptureURL+"/capture", bytes.NewReader(payload))
	hreq.Header.Set("Content-Type", "application/json")
	res, err := s.HTTP.Do(hreq)
	if err != nil {
		return nil, &ProviderError{"studio_failed", "capture service: " + err.Error()}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	var out captureResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, &ProviderError{"studio_failed", fmt.Sprintf("capture service answered %d: %s", res.StatusCode, truncate(string(body), 300))}
	}
	if out.Error != nil && out.Error.Code != "invalid_workflow" {
		return nil, &ProviderError{"studio_failed", out.Error.Message}
	}
	return &out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// extractJSON pulls the first JSON object out of a model reply (tolerates fences and stray prose).
func extractJSON(text string) (json.RawMessage, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, errors.New("the reply contains no JSON object")
	}
	raw := json.RawMessage(text[start : end+1])
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("the reply is not valid JSON: %v", err)
	}
	if nodes, ok := probe["nodes"].([]any); !ok || len(nodes) == 0 {
		return nil, errors.New(`the JSON has no "nodes" array`)
	}
	return raw, nil
}

func issueList(issues []CaptureIssue) string {
	var b strings.Builder
	for _, i := range issues {
		if i.Node != "" {
			fmt.Fprintf(&b, "- Node %q: %s\n", i.Node, i.Message)
		} else {
			fmt.Fprintf(&b, "- %s\n", i.Message)
		}
	}
	return b.String()
}

// Screenshot designs a workflow with the provider (unless req.Workflow is given), fixes it from n8n's feedback
// and returns the rendered PNG.
func (s *Studio) Screenshot(ctx context.Context, p Provider, req N8nRequest, progress func(string)) (N8nResult, error) {
	var res N8nResult
	if err := s.Ensure(ctx, progress); err != nil {
		return res, err
	}
	width, height := req.viewport()
	capReq := captureRequest{Width: width, Height: height, Scale: 2, Theme: req.Theme, Frame: req.Frame, Layout: req.Layout}
	if capReq.Layout == "" {
		capReq.Layout = "auto"
	}
	if p == nil {
		progress("Rendering in n8n…\n")
		capReq.Workflow = req.Workflow
		out, err := s.capture(ctx, capReq)
		if err != nil {
			return res, err
		}
		if out.Error != nil {
			return res, &ProviderError{"invalid_workflow", out.Error.Message + ":\n" + issueList(out.Error.Issues)}
		}
		return s.result(res, out, 0), nil
	}

	messages := []Message{{Role: "user", Content: req.Prompt}}
	for attempt := 1; attempt <= studioAttempts; attempt++ {
		if attempt == 1 {
			progress("Designing the workflow…\n")
		}
		chat, err := p.Run(ctx, ChatRequest{Provider: req.Provider, Model: req.Model, Effort: req.Effort,
			System: n8nSystemPrompt, Messages: messages}, func(string) {})
		res.Model = chat.Model
		res.Usage.InputTokens += chat.Usage.InputTokens
		res.Usage.OutputTokens += chat.Usage.OutputTokens
		if err != nil {
			return res, err
		}
		messages = append(messages, Message{Role: "assistant", Content: chat.Text})
		workflow, err := extractJSON(chat.Text)
		if err != nil {
			progress(fmt.Sprintf("The answer was not a workflow (%v), retrying…\n", err))
			messages = append(messages, Message{Role: "user",
				Content: fmt.Sprintf("That was not usable: %v. Reply with the n8n workflow JSON object only.", err)})
			continue
		}
		progress("Rendering in n8n…\n")
		capReq.Workflow, capReq.Strict = workflow, attempt < studioAttempts
		out, err := s.capture(ctx, capReq)
		if err != nil {
			return res, err
		}
		if out.Error == nil {
			return s.result(res, out, attempt), nil
		}
		progress(fmt.Sprintf("n8n reported %d problem(s), fixing…\n%s", len(out.Error.Issues), issueList(out.Error.Issues)))
		messages = append(messages, Message{Role: "user", Content: "n8n reported these problems:\n" +
			issueList(out.Error.Issues) + "\nReturn the whole corrected workflow JSON."})
	}
	return res, &ProviderError{"invalid_workflow", "The AI could not produce a valid n8n workflow; try again or rephrase the brief"}
}

func (s *Studio) result(res N8nResult, out *captureResponse, attempts int) N8nResult {
	res.Images = []Image{out.Image}
	res.Workflow, res.Issues, res.Notes = out.Workflow, out.Issues, out.Notes
	res.Attempts, res.N8nVersion = attempts, out.N8nVersion
	if res.Issues == nil {
		res.Issues = []CaptureIssue{}
	}
	if res.Notes == nil {
		res.Notes = []string{}
	}
	var wf struct {
		Name  string `json:"name"`
		Nodes []struct {
			Type string `json:"type"`
		} `json:"nodes"`
	}
	_ = json.Unmarshal(out.Workflow, &wf)
	res.Text = fmt.Sprintf("%s — %d nodes, n8n %s", wf.Name, len(wf.Nodes), out.N8nVersion)
	return res
}
