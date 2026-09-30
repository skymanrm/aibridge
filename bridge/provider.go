package bridge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	System   string    `json:"system"`
	Messages []Message `json:"messages"`
	Effort   string    `json:"effort"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type ChatResult struct {
	Text  string `json:"text"`
	Model string `json:"model"`
	Usage Usage  `json:"usage"`
}

type Model struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Efforts []string `json:"efforts,omitempty"`
}

type ProviderInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Available    bool     `json:"available"`
	Version      string   `json:"version"`
	DefaultModel string   `json:"default_model"`
	Models       []Model  `json:"models"`
	Efforts      []string `json:"efforts"`
	Error        string   `json:"error"`
}

// Provider is a local AI CLI the bridge can drive.
type Provider interface {
	ID() string
	Detect(ctx context.Context) ProviderInfo
	Run(ctx context.Context, req ChatRequest, emit func(delta string)) (ChatResult, error)
}

// ProviderError carries an HTTP-friendly code.
type ProviderError struct {
	Code    string
	Message string
}

func (e *ProviderError) Error() string { return e.Message }

var safeArg = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`)

// Validate rejects values that could be read as CLI flags.
func (r ChatRequest) Validate() error {
	if len(r.Messages) == 0 {
		return &ProviderError{"validation_error", "messages must not be empty"}
	}
	for _, m := range r.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return &ProviderError{"validation_error", fmt.Sprintf("unsupported role %q", m.Role)}
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

// Prompt flattens the conversation into one prompt for single-shot CLIs.
func (r ChatRequest) Prompt() string {
	if len(r.Messages) == 1 {
		return r.Messages[0].Content
	}
	var b strings.Builder
	b.WriteString("<conversation>\n")
	for _, m := range r.Messages {
		fmt.Fprintf(&b, "<%s>\n%s\n</%s>\n", m.Role, m.Content, m.Role)
	}
	b.WriteString("</conversation>\n\nReply as the assistant to the last user message.")
	return b.String()
}

// Runner starts a command and feeds stdout lines to onLine; returns stderr tail.
type Runner func(ctx context.Context, bin string, args []string, dir, stdin string, onLine func([]byte)) (string, error)

func ExecRunner(ctx context.Context, bin string, args []string, dir, stdin string, onLine func([]byte)) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.WaitDelay = 3 * time.Second
	var stderr tailBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		onLine(sc.Bytes())
	}
	_, _ = io.Copy(io.Discard, stdout)
	err = cmd.Wait()
	if ctx.Err() != nil {
		return stderr.String(), ctx.Err()
	}
	return stderr.String(), err
}

// tailBuffer keeps the last 4KB written to it.
type tailBuffer struct{ buf []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.buf)) }

// FindBinary resolves a CLI even under the minimal PATH of apps started from Finder/Dock.
func FindBinary(name, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{filepath.Join(home, ".local/bin"), "/opt/homebrew/bin", "/usr/local/bin",
		filepath.Join(home, ".npm-global/bin"), filepath.Join(home, ".bun/bin")} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}

// commandOutput runs a short command and returns trimmed stdout.
func commandOutput(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// withTempDir runs fn in an empty working dir so no project files leak into the prompt.
func withTempDir(fn func(dir string) error) error {
	dir, err := os.MkdirTemp("", "ai-bridge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	return fn(dir)
}

// Registry caches provider detection (spawning CLIs is slow).
type Registry struct {
	providers []Provider
	ttl       time.Duration

	mu      sync.Mutex
	cached  []ProviderInfo
	fetched time.Time
}

func NewRegistry(ttl time.Duration, providers ...Provider) *Registry {
	return &Registry{providers: providers, ttl: ttl}
}

func (r *Registry) Get(id string) Provider {
	for _, p := range r.providers {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

func (r *Registry) Infos(ctx context.Context, refresh bool) []ProviderInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !refresh && r.cached != nil && time.Since(r.fetched) < r.ttl {
		return r.cached
	}
	infos := make([]ProviderInfo, len(r.providers))
	var wg sync.WaitGroup
	for i, p := range r.providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			infos[i] = p.Detect(ctx)
		}()
	}
	wg.Wait()
	r.cached, r.fetched = infos, time.Now()
	return infos
}

func (r *Registry) Info(ctx context.Context, id string) (ProviderInfo, bool) {
	for _, info := range r.Infos(ctx, false) {
		if info.ID == id {
			return info, true
		}
	}
	return ProviderInfo{}, false
}

// DefaultRegistry detects the Claude Code and Codex CLIs.
func DefaultRegistry(cfg *Config) *Registry {
	claudeBin, _ := FindBinary("claude", cfg.Binaries["claude"])
	codexBin, _ := FindBinary("codex", cfg.Binaries["codex"])
	return NewRegistry(time.Minute,
		&ClaudeProvider{Bin: claudeBin, Runner: ExecRunner},
		&CodexProvider{Bin: codexBin, Runner: ExecRunner},
	)
}
