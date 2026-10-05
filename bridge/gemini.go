package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// GeminiProvider drives `gemini -p` (Google login) with every tool denied by policy and no extensions or MCP servers.
type GeminiProvider struct {
	Bin    string
	Runner Runner
}

func (p *GeminiProvider) ID() string { return "gemini" }

var geminiModels = []Model{
	{ID: "auto", Name: "Auto"},
	{ID: "pro", Name: "Pro"},
	{ID: "flash", Name: "Flash"},
	{ID: "flash-lite", Name: "Flash Lite"},
}

func (p *GeminiProvider) Detect(ctx context.Context) ProviderInfo {
	info := ProviderInfo{ID: p.ID(), Path: p.Bin, Name: "Gemini CLI", DefaultModel: "auto", Models: geminiModels, Efforts: []string{}}
	if p.Bin == "" {
		info.Error = "gemini CLI not found; install it or set its path under \"binaries\" in the config"
		return info
	}
	out, err := commandOutput(ctx, p.Bin, "--version")
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Version = strings.TrimSpace(out)
	info.Available = true
	return info
}

// geminiPolicy denies every tool (built-in, MCP and skills) so a prompt cannot read files or run commands.
const geminiPolicy = `[[rule]]
toolName = "*"
decision = "deny"
priority = 999
`

const (
	geminiPolicyFile = "ai-bridge-policy.toml"
	geminiSystemFile = "ai-bridge-system.md"
)

func (p *GeminiProvider) Args(req ChatRequest, dir string) []string {
	args := []string{"--output-format", "stream-json", "--extensions", "none",
		"--allowed-mcp-server-names", "ai-bridge-none", "--policy", filepath.Join(dir, geminiPolicyFile)}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	return args
}

// Env replaces Gemini's agent system prompt and trusts the empty temp workspace.
func (p *GeminiProvider) Env(dir string) []string {
	return []string{"GEMINI_SYSTEM_MD=" + filepath.Join(dir, geminiSystemFile), "GEMINI_CLI_TRUST_WORKSPACE=true"}
}

// prompt keeps user text from being run as a Gemini slash command (custom commands can run shell).
func (p *GeminiProvider) prompt(req ChatRequest) string {
	prompt := req.Prompt()
	if strings.HasPrefix(strings.TrimSpace(prompt), "/") {
		prompt = "<user>\n" + prompt + "\n</user>"
	}
	return prompt
}

type geminiLine struct {
	Type     string `json:"type"`
	Model    string `json:"model"`
	Role     string `json:"role"`
	Content  string `json:"content"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Status   string `json:"status"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Stats *struct {
		InputTokens  int                        `json:"input_tokens"`
		OutputTokens int                        `json:"output_tokens"`
		Models       map[string]json.RawMessage `json:"models"`
	} `json:"stats"`
}

// geminiErrorMessage unwraps "[API Error: {"error":{"message":"{\"error\":…}"}}]" down to the innermost message.
func geminiErrorMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if inner, ok := strings.CutPrefix(msg, "[API Error: "); ok {
		msg = strings.TrimSuffix(inner, "]")
	}
	for range 4 {
		var nested struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(msg), &nested) != nil || nested.Error.Message == "" {
			break
		}
		msg = strings.TrimSpace(nested.Error.Message)
	}
	return msg
}

func (p *GeminiProvider) Run(ctx context.Context, req ChatRequest, emit func(string)) (ChatResult, error) {
	res := ChatResult{Model: req.Model}
	var text strings.Builder
	var final *geminiLine
	var errMsg string
	onLine := func(line []byte) {
		var l geminiLine
		if json.Unmarshal(line, &l) != nil {
			return
		}
		switch l.Type {
		case "init":
			if l.Model != "" {
				res.Model = l.Model
			}
		case "message":
			if l.Role == "assistant" && l.Content != "" {
				text.WriteString(l.Content)
				emit(l.Content)
			}
		case "error":
			if l.Severity == "error" {
				errMsg = geminiErrorMessage(l.Message)
			}
		case "result":
			final = &l
		}
	}
	system := req.System
	if strings.TrimSpace(system) == "" {
		system = defaultSystemPrompt
	}
	var stderr string
	err := withTempDir(func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, geminiPolicyFile), []byte(geminiPolicy), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, geminiSystemFile), []byte(system), 0o600); err != nil {
			return err
		}
		var runErr error
		stderr, runErr = p.Runner(ctx, p.Bin, p.Args(req, dir), dir, p.prompt(req), p.Env(dir), onLine)
		return runErr
	})
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if final == nil {
		msg := errMsg
		if msg == "" {
			msg = stderr
		}
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "gemini exited without a result"
		}
		return res, &ProviderError{"provider_failed", msg}
	}
	if final.Status != "success" {
		msg := errMsg
		if final.Error != nil && final.Error.Message != "" {
			msg = geminiErrorMessage(final.Error.Message)
		}
		if msg == "" {
			msg = "gemini reported an error"
		}
		return res, &ProviderError{"provider_failed", msg}
	}
	res.Text = text.String()
	if errMsg != "" && res.Text == "" {
		return res, &ProviderError{"provider_failed", errMsg}
	}
	if s := final.Stats; s != nil {
		res.Usage = Usage{InputTokens: s.InputTokens, OutputTokens: s.OutputTokens}
		if len(s.Models) == 1 {
			for name := range s.Models {
				res.Model = name
			}
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) && res.Text == "" {
		return res, &ProviderError{"provider_failed", err.Error()}
	}
	return res, nil
}
