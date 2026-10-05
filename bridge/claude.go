package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

const defaultSystemPrompt = "You are a helpful assistant."

// ClaudeProvider drives `claude -p` (Claude Code, subscription login) with all tools disabled.
type ClaudeProvider struct {
	Bin    string
	Runner Runner
}

func (p *ClaudeProvider) ID() string { return "claude" }

var claudeModels = []Model{
	{ID: "sonnet", Name: "Sonnet"},
	{ID: "opus", Name: "Opus"},
	{ID: "fable", Name: "Fable"},
	{ID: "haiku", Name: "Haiku"},
}

func (p *ClaudeProvider) Detect(ctx context.Context) ProviderInfo {
	info := ProviderInfo{ID: p.ID(), Path: p.Bin, Name: "Claude Code", DefaultModel: "sonnet", Models: claudeModels,
		Efforts: []string{"low", "medium", "high", "xhigh", "max"}}
	if p.Bin == "" {
		info.Error = "claude CLI not found; install it or set its path under \"binaries\" in the config"
		return info
	}
	out, err := commandOutput(ctx, p.Bin, "--version")
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Version = strings.TrimSpace(strings.TrimSuffix(out, "(Claude Code)"))
	info.Available = true
	return info
}

func (p *ClaudeProvider) Args(req ChatRequest) []string {
	system := req.System
	if strings.TrimSpace(system) == "" {
		system = defaultSystemPrompt
	}
	args := []string{"-p", "--output-format", "stream-json", "--include-partial-messages", "--verbose",
		"--tools", "", "--system-prompt", system, "--no-session-persistence",
		"--setting-sources", "", "--strict-mcp-config"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	return args
}

type claudeLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Event   *struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Usage   *struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (p *ClaudeProvider) Run(ctx context.Context, req ChatRequest, emit func(string)) (ChatResult, error) {
	res := ChatResult{Model: req.Model}
	var streamed strings.Builder
	var final *claudeLine
	onLine := func(line []byte) {
		var l claudeLine
		if json.Unmarshal(line, &l) != nil {
			return
		}
		switch {
		case l.Type == "system" && l.Subtype == "init" && l.Model != "":
			res.Model = l.Model
		case l.Type == "stream_event" && l.Event != nil && l.Event.Type == "content_block_delta" &&
			l.Event.Delta.Type == "text_delta":
			streamed.WriteString(l.Event.Delta.Text)
			emit(l.Event.Delta.Text)
		case l.Type == "result":
			final = &l
		}
	}
	var stderr string
	err := withTempDir(func(dir string) error {
		var runErr error
		stderr, runErr = p.Runner(ctx, p.Bin, p.Args(req), dir, req.Prompt(), nil, onLine)
		return runErr
	})
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if final == nil {
		msg := stderr
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "claude exited without a result"
		}
		return res, &ProviderError{"provider_failed", msg}
	}
	if final.IsError {
		msg := strings.TrimSpace(final.Result)
		if msg == "" {
			msg = "claude reported an error (" + final.Subtype + ")"
		}
		return res, &ProviderError{"provider_failed", msg}
	}
	res.Text = final.Result
	if res.Text == "" {
		res.Text = streamed.String()
	}
	if u := final.Usage; u != nil {
		res.Usage = Usage{InputTokens: u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens,
			OutputTokens: u.OutputTokens}
	}
	if err != nil && !errors.Is(err, context.Canceled) && res.Text == "" {
		return res, &ProviderError{"provider_failed", err.Error()}
	}
	return res, nil
}
