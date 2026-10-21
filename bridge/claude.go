package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	if len(attachments(req.Files)) > 0 {
		args = append(args, "--input-format", "stream-json")
	}
	return args
}

type claudeSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

type claudeBlock struct {
	Type   string        `json:"type"`
	Text   string        `json:"text,omitempty"`
	Source *claudeSource `json:"source,omitempty"`
	Title  string        `json:"title,omitempty"`
}

// Input is the stdin: the plain prompt, or with images/PDFs one stream-json user message of content blocks.
func (p *ClaudeProvider) Input(req ChatRequest) string {
	atts := attachments(req.Files)
	if len(atts) == 0 {
		return req.Prompt()
	}
	blocks := make([]claudeBlock, 0, len(atts)+1)
	for _, a := range atts {
		b := claudeBlock{Type: "image", Source: &claudeSource{"base64", a.mimeType(), a.Data}}
		if a.Kind == filePDF {
			b = claudeBlock{Type: "document", Source: &claudeSource{"base64", "application/pdf", a.Data}, Title: a.safeName()}
		}
		blocks = append(blocks, b)
	}
	blocks = append(blocks, claudeBlock{Type: "text", Text: req.Prompt()})
	msg := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": blocks}}
	line, _ := json.Marshal(msg)
	return string(line) + "\n"
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
		stderr, runErr = p.Runner(ctx, p.Bin, p.Args(req), dir, p.Input(req), nil, onLine)
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

var claudeUsageArgs = []string{"-p", "/usage", "--output-format", "json", "--no-session-persistence",
	"--tools", "", "--setting-sources", "", "--strict-mcp-config"}

// Usage runs Claude Code's local /usage command, which reads the subscription limits without spending tokens.
func (p *ClaudeProvider) Usage(ctx context.Context) (UsageLimits, error) {
	var out UsageLimits
	var final *claudeLine
	var stderr string
	err := withTempDir(func(dir string) error {
		var runErr error
		stderr, runErr = p.Runner(ctx, p.Bin, claudeUsageArgs, dir, "", nil, func(line []byte) {
			var l claudeLine
			if json.Unmarshal(line, &l) == nil && l.Type == "result" {
				final = &l
			}
		})
		return runErr
	})
	if final == nil || final.IsError {
		msg := stderr
		if final != nil {
			msg = final.Result
		}
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "claude /usage returned nothing"
		}
		return out, errors.New(strings.TrimSpace(msg))
	}
	out.Windows = ParseClaudeUsage(final.Result, time.Now())
	if len(out.Windows) == 0 {
		// e.g. API-key logins, which have no subscription limits.
		first, _, _ := strings.Cut(strings.TrimSpace(final.Result), "\n")
		return out, errors.New(first)
	}
	out.Plan = p.plan(ctx)
	return out, nil
}

// plan reads the subscription type (e.g. "max") from `claude auth status`.
func (p *ClaudeProvider) plan(ctx context.Context) string {
	var buf strings.Builder
	_, _ = p.Runner(ctx, p.Bin, []string{"auth", "status", "--json"}, "", "", nil, func(line []byte) {
		buf.Write(line)
		buf.WriteByte('\n')
	})
	var status struct {
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal([]byte(buf.String()), &status) != nil {
		return ""
	}
	return capitalize(status.SubscriptionType)
}

// claudeUsageLine matches "Current week (all models): 47% used · resets Oct 14 at 6am (Europe/Moscow)".
var claudeUsageLine = regexp.MustCompile(`(?m)^[ \t]*([^:\n]+?):[ \t]*(\d+(?:\.\d+)?)%[ \t]+used(?:[ \t]*·[ \t]*resets[ \t]+(.+?))?[ \t]*$`)

// ParseClaudeUsage reads the limit lines of the /usage report.
func ParseClaudeUsage(text string, now time.Time) []UsageWindow {
	windows := []UsageWindow{}
	for _, m := range claudeUsageLine.FindAllStringSubmatch(text, -1) {
		pct, _ := strconv.ParseFloat(m[2], 64)
		w := UsageWindow{ID: windowID(m[1]), Label: m[1], UsedPercent: pct, ResetsAt: parseClaudeReset(m[3], now)}
		switch {
		case strings.HasPrefix(w.ID, "session"):
			w.WindowMinutes = 5 * 60
		case strings.HasPrefix(w.ID, "week"):
			w.WindowMinutes = 7 * 24 * 60
		}
		windows = append(windows, w)
	}
	return windows
}

// parseClaudeReset reads "Oct 14 at 6am (Europe/Moscow)" or "3:50pm (Europe/Moscow)" as the next such moment.
func parseClaudeReset(s string, now time.Time) *time.Time {
	s = strings.TrimSpace(s)
	loc := now.Location()
	if i := strings.LastIndex(s, " ("); i >= 0 && strings.HasSuffix(s, ")") {
		if l, err := time.LoadLocation(s[i+2 : len(s)-1]); err == nil {
			loc = l
		}
		s = s[:i]
	}
	now = now.In(loc)
	for _, layout := range []string{"Jan 2 at 3:04pm", "Jan 2 at 3pm", "Jan 2, 2006 at 3:04pm", "Jan 2, 2006 at 3pm", "3:04pm", "3pm"} {
		t, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		switch {
		case !strings.Contains(layout, "Jan"):
			t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if t.Before(now) {
				t = t.AddDate(0, 0, 1)
			}
		case !strings.Contains(layout, "2006"):
			t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if t.Before(now.AddDate(0, 0, -1)) {
				t = t.AddDate(1, 0, 0)
			}
		}
		return &t
	}
	return nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (p *ClaudeProvider) browseArgs(req BrowseRequest) []string {
	args := []string{"-p", "--chrome", "--output-format", "stream-json", "--verbose",
		"--tools", "", "--allowedTools", chromeTools(browseTools), "--disallowedTools", chromeTools(browseDeniedTools),
		"--system-prompt", browseSystemPrompt, "--json-schema", browseSchema(req.Schema),
		"--no-session-persistence", "--setting-sources", "", "--strict-mcp-config"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	return args
}

type claudeBrowseLine struct {
	claudeLine
	Message *struct {
		Content []struct {
			Type    string          `json:"type"`
			Name    string          `json:"name"`
			Input   map[string]any  `json:"input"`
			Content json.RawMessage `json:"content"`
		} `json:"content"`
	} `json:"message"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// Browse opens req.URL in the user's Chrome via Claude in Chrome (read-only tools) and answers req.Prompt.
func (p *ClaudeProvider) Browse(ctx context.Context, req BrowseRequest, emit func(string)) (BrowseResult, error) {
	res := BrowseResult{Model: req.Model}
	var final *claudeBrowseLine
	last := ""
	progress := func(msg string) {
		if msg != "" && msg != last {
			last = msg
			emit(msg + "\n")
		}
	}
	onLine := func(line []byte) {
		var l claudeBrowseLine
		if json.Unmarshal(line, &l) != nil {
			return
		}
		switch {
		case l.Type == "system" && l.Subtype == "init" && l.Model != "":
			res.Model = l.Model
		case (l.Type == "assistant" || l.Type == "user") && l.Message != nil:
			for _, b := range l.Message.Content {
				switch {
				case b.Type == "tool_use":
					progress(browseProgress(b.Name, b.Input))
				case b.Type == "tool_result" && strings.Contains(string(b.Content), "permission prompt"):
					progress("Waiting for Allow in the Claude extension in Chrome")
				}
			}
		case l.Type == "result":
			final = &l
		}
	}
	prompt := fmt.Sprintf("URL: %s\n\n%s\n\n%s", strings.TrimSpace(req.URL), strings.TrimSpace(req.Prompt), browseFailurePrompt)
	var stderr string
	err := withTempDir(func(dir string) error {
		var runErr error
		stderr, runErr = p.Runner(ctx, p.Bin, p.browseArgs(req), dir, prompt, nil, onLine)
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
	if u := final.Usage; u != nil {
		res.Usage = Usage{InputTokens: u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens, OutputTokens: u.OutputTokens}
	}
	raw := final.StructuredOutput
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(final.Result)
	}
	var out struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return res, &ProviderError{"browse_failed", "The answer is not valid JSON: " + truncate(final.Result, 300)}
	}
	if !out.OK {
		msg := strings.TrimSpace(out.Error)
		if msg == "" {
			msg = "The page could not be read"
		}
		return res, &ProviderError{"browse_failed", msg}
	}
	if req.hasSchema() {
		res.Data = out.Data
	} else {
		var text string
		_ = json.Unmarshal(out.Data, &text)
		res.Text = text
	}
	return res, nil
}
