package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// CodexProvider drives `codex exec` (ChatGPT login) in a read-only sandbox without user config.
type CodexProvider struct {
	Bin    string
	Runner Runner
	// Home overrides $CODEX_HOME (tests).
	Home string
}

func (p *CodexProvider) ID() string { return "codex" }

// Efforts that spawn sub-agents make no sense for single-shot text tasks.
var codexSkippedEfforts = []string{"ultra"}

func (p *CodexProvider) Detect(ctx context.Context) ProviderInfo {
	info := ProviderInfo{ID: p.ID(), Path: p.Bin, Name: "Codex (ChatGPT)", Models: []Model{}, Efforts: []string{}}
	if p.Bin == "" {
		info.Error = "codex CLI not found; install it or set its path under \"binaries\" in the config"
		return info
	}
	out, err := commandOutput(ctx, p.Bin, "--version")
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Version = strings.TrimSpace(strings.TrimPrefix(out, "codex-cli"))
	catalog, err := commandOutput(ctx, p.Bin, "debug", "models")
	if err != nil {
		info.Error = "model catalog: " + err.Error()
		return info
	}
	info.Models, err = ParseCodexModels([]byte(catalog))
	if err != nil {
		info.Error = "model catalog: " + err.Error()
		return info
	}
	if len(info.Models) > 0 {
		info.DefaultModel = info.Models[0].ID
		info.Efforts = info.Models[0].Efforts
	}
	info.Available = true
	return info
}

type codexCatalogModel struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility"`
	Priority    int    `json:"priority"`
	Levels      []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

// ParseCodexModels returns listed models ordered by catalog priority.
func ParseCodexModels(data []byte) ([]Model, error) {
	var catalog struct {
		Models []codexCatalogModel `json:"models"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, err
	}
	entries := catalog.Models
	slices.SortStableFunc(entries, func(a, b codexCatalogModel) int { return a.Priority - b.Priority })
	models := []Model{}
	for _, m := range entries {
		if m.Visibility != "list" || m.Slug == "" {
			continue
		}
		model := Model{ID: m.Slug, Name: m.DisplayName}
		if model.Name == "" {
			model.Name = m.Slug
		}
		for _, l := range m.Levels {
			if !slices.Contains(codexSkippedEfforts, l.Effort) {
				model.Efforts = append(model.Efforts, l.Effort)
			}
		}
		models = append(models, model)
	}
	return models, nil
}

// tomlString encodes s as a TOML basic string for `-c key=value` overrides.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f || r == utf8.RuneError {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func (p *CodexProvider) Args(req ChatRequest, dir string) []string {
	system := req.System
	if strings.TrimSpace(system) == "" {
		system = defaultSystemPrompt
	}
	args := []string{"exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral",
		"--ignore-user-config", "--ignore-rules", "--color", "never", "-C", dir,
		"-c", "developer_instructions=" + tomlString(system)}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+tomlString(req.Effort))
	}
	for _, a := range attachments(req.Files) {
		if a.Kind == fileImage {
			args = append(args, "--image="+filepath.Join(dir, filepath.FromSlash(a.Path)))
		}
	}
	return append(args, "-")
}

// prompt points Codex at attached PDFs, which it reads from the working directory in its read-only sandbox.
func (p *CodexProvider) prompt(req ChatRequest) string {
	var pdfs []string
	for _, a := range attachments(req.Files) {
		if a.Kind == filePDF {
			pdfs = append(pdfs, a.Path)
		}
	}
	if len(pdfs) == 0 {
		return req.Prompt()
	}
	return req.Prompt() + "\n\nAttached files (in the working directory): " + strings.Join(pdfs, ", ")
}

type codexLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Message string `json:"message"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// codexErrorMessage unwraps `{"error":{"message":...}}` payloads embedded as strings.
func codexErrorMessage(raw string) string {
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &nested) == nil && nested.Error.Message != "" {
		return nested.Error.Message
	}
	return raw
}

// codexTurn collects the JSONL events of one `codex exec --json` run.
type codexTurn struct {
	emit                   func(string)
	threadID               string
	currentID, currentText string
	lastMessage            string
	errMsg                 string
	completed              bool
	usage                  Usage
}

func (t *codexTurn) onLine(line []byte) {
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	switch l.Type {
	case "thread.started":
		t.threadID = l.ThreadID
	case "item.started", "item.updated", "item.completed":
		if l.Item == nil || l.Item.Type != "agent_message" {
			return
		}
		if l.Item.ID != t.currentID {
			if t.currentText != "" {
				t.emit("\n\n")
			}
			t.currentID, t.currentText = l.Item.ID, ""
		}
		if strings.HasPrefix(l.Item.Text, t.currentText) && len(l.Item.Text) > len(t.currentText) {
			t.emit(l.Item.Text[len(t.currentText):])
		}
		t.currentText = l.Item.Text
		if l.Type == "item.completed" {
			t.lastMessage = l.Item.Text
		}
	case "error":
		t.errMsg = codexErrorMessage(l.Message)
	case "turn.failed":
		if l.Error != nil {
			t.errMsg = codexErrorMessage(l.Error.Message)
		}
	case "turn.completed":
		t.completed = true
		if l.Usage != nil {
			t.usage = Usage{InputTokens: l.Usage.InputTokens, OutputTokens: l.Usage.OutputTokens}
		}
	}
}

// exec runs one turn in an empty temp dir and returns it once completed.
func (p *CodexProvider) exec(ctx context.Context, req ChatRequest, emit func(string)) (*codexTurn, error) {
	turn := &codexTurn{emit: emit}
	var stderr string
	err := withTempDir(func(dir string) error {
		if err := writeAttachments(dir, attachments(req.Files)); err != nil {
			return err
		}
		var runErr error
		stderr, runErr = p.Runner(ctx, p.Bin, p.Args(req, dir), dir, p.prompt(req), nil, turn.onLine)
		return runErr
	})
	if ctx.Err() != nil {
		return turn, ctx.Err()
	}
	if turn.errMsg != "" {
		return turn, &ProviderError{"provider_failed", turn.errMsg}
	}
	if !turn.completed {
		msg := stderr
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "codex exited without completing the turn"
		}
		return turn, &ProviderError{"provider_failed", msg}
	}
	return turn, nil
}

func (p *CodexProvider) Run(ctx context.Context, req ChatRequest, emit func(string)) (ChatResult, error) {
	turn, err := p.exec(ctx, req, emit)
	if err != nil {
		return ChatResult{Model: req.Model}, err
	}
	return ChatResult{Text: turn.lastMessage, Model: req.Model, Usage: turn.usage}, nil
}

const codexImageInstructions = "You generate images with the built-in image_gen tool from the brief the user gives. " +
	"Call image_gen right away, exactly once unless the brief asks for several images: do not run shell commands, " +
	"read files or skills, or ask questions. Honour the requested size and aspect ratio. " +
	"After the image is generated, reply with one short sentence describing it."

// home is the Codex state dir; the built-in image_gen tool saves to <home>/generated_images/<thread id>/.
func (p *CodexProvider) home() string {
	if p.Home != "" {
		return p.Home
	}
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func (p *CodexProvider) Image(ctx context.Context, req ImageRequest, emit func(string)) (ImageResult, error) {
	res := ImageResult{Model: req.Model}
	brief := req.Prompt
	if len(req.Files) > 0 {
		brief += "\n\nUse the attached image(s) as reference."
	}
	if req.Size != "" && req.Size != "auto" {
		brief += "\n\nImage size: " + req.Size + " pixels."
	}
	chat := ChatRequest{Model: req.Model, Effort: req.Effort, System: codexImageInstructions,
		Messages: []Message{{Role: "user", Content: brief}}, Files: req.Files}
	turn, err := p.exec(ctx, chat, emit)
	if turn.threadID != "" {
		dir := filepath.Join(p.home(), "generated_images", turn.threadID)
		defer os.RemoveAll(dir)
		if err == nil {
			res.Images, err = readImages(dir)
		}
	}
	if err != nil {
		return res, err
	}
	if len(res.Images) == 0 {
		msg := "Codex did not generate an image"
		if turn.lastMessage != "" {
			msg += ": " + turn.lastMessage
		}
		return res, &ProviderError{"no_image", msg}
	}
	res.Text, res.Usage = turn.lastMessage, turn.usage
	return res, nil
}

var imageMimes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp"}

// readImages loads the images of a run in creation order.
func readImages(dir string) ([]Image, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type file struct {
		name string
		mod  time.Time
	}
	var files []file
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && imageMimes[strings.ToLower(filepath.Ext(e.Name()))] != "" {
			files = append(files, file{e.Name(), info.ModTime()})
		}
	}
	slices.SortFunc(files, func(a, b file) int { return a.mod.Compare(b.mod) })
	images := make([]Image, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			return nil, err
		}
		images = append(images, Image{Mime: imageMimes[strings.ToLower(filepath.Ext(f.name))], Data: data})
	}
	return images, nil
}

// Usage reads the ChatGPT plan's Codex rate limits from `codex app-server` without starting a turn.
func (p *CodexProvider) Usage(ctx context.Context) (UsageLimits, error) {
	raw, err := p.appServerCall(ctx, "account/rateLimits/read")
	if err != nil {
		return UsageLimits{}, err
	}
	return ParseCodexRateLimits(raw)
}

// appServerCall runs `codex app-server` for a single JSON-RPC request after the initialize handshake.
func (p *CodexProvider) appServerCall(ctx context.Context, method string) (json.RawMessage, error) {
	procCtx, kill := context.WithCancel(ctx)
	defer kill()
	cmd, err := command(procCtx, p.Bin, "app-server")
	if err != nil {
		return nil, err
	}
	cmd.WaitDelay = 3 * time.Second
	var stderr tailBuffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// The server quits on stdin EOF, so stdin stays open until the answer arrives.
	res, err := appServerExchange(stdin, stdout, method)
	_ = stdin.Close()
	stop := time.AfterFunc(2*time.Second, kill)
	_ = cmd.Wait()
	stop.Stop()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && stderr.String() != "" {
		err = fmt.Errorf("%w: %s", err, stderr.String())
	}
	return res, err
}

// appServerExchange writes the handshake and request, then reads lines until the request's response.
func appServerExchange(w io.Writer, r io.Reader, method string) (json.RawMessage, error) {
	msgs := []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
			"clientInfo": map[string]string{"name": "ai-bridge", "version": Version}}},
		{"jsonrpc": "2.0", "method": "initialized"},
		{"jsonrpc": "2.0", "id": 2, "method": method},
	}
	for _, m := range msgs {
		line, _ := json.Marshal(m)
		if _, err := w.Write(append(line, '\n')); err != nil {
			return nil, err
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil || (*msg.ID != 1 && *msg.ID != 2) {
			continue
		}
		if msg.Error != nil {
			return nil, errors.New(codexErrorMessage(msg.Error.Message))
		}
		if *msg.ID == 2 {
			return msg.Result, nil
		}
	}
	return nil, errors.New("codex app-server exited without answering " + method)
}

type codexLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type codexLimit struct {
	LimitID   string            `json:"limitId"`
	LimitName string            `json:"limitName"`
	Primary   *codexLimitWindow `json:"primary"`
	Secondary *codexLimitWindow `json:"secondary"`
	PlanType  string            `json:"planType"`
}

// ParseCodexRateLimits maps the `account/rateLimits/read` result to usage windows, the main "codex" limit first.
func ParseCodexRateLimits(data []byte) (UsageLimits, error) {
	var res struct {
		RateLimits          *codexLimit           `json:"rateLimits"`
		RateLimitsByLimitID map[string]codexLimit `json:"rateLimitsByLimitId"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return UsageLimits{}, err
	}
	limits := slices.Collect(maps.Values(res.RateLimitsByLimitID))
	if len(limits) == 0 && res.RateLimits != nil {
		limits = []codexLimit{*res.RateLimits}
	}
	slices.SortFunc(limits, func(a, b codexLimit) int {
		if (a.LimitID == "codex") != (b.LimitID == "codex") {
			if a.LimitID == "codex" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.LimitID, b.LimitID)
	})
	out := UsageLimits{Windows: []UsageWindow{}}
	for _, l := range limits {
		if out.Plan == "" {
			out.Plan = capitalize(l.PlanType)
		}
		for _, w := range []*codexLimitWindow{l.Primary, l.Secondary} {
			if w == nil {
				continue
			}
			id, label := codexWindowName(w.WindowDurationMins)
			if l.LimitID != "" && l.LimitID != "codex" {
				name := l.LimitName
				if name == "" {
					name = l.LimitID
				}
				id, label = windowID(l.LimitID)+"_"+id, name+" · "+label
			}
			uw := UsageWindow{ID: id, Label: label, UsedPercent: w.UsedPercent, WindowMinutes: w.WindowDurationMins}
			if w.ResetsAt > 0 {
				t := time.Unix(w.ResetsAt, 0)
				uw.ResetsAt = &t
			}
			out.Windows = append(out.Windows, uw)
		}
	}
	return out, nil
}

// codexWindowName names a limit window by its length, as Codex's /status does.
func codexWindowName(mins int) (id, label string) {
	switch {
	case mins == 5*60:
		return "session", "5h limit"
	case mins == 7*24*60:
		return "week", "Weekly limit"
	case mins > 0 && mins%(24*60) == 0:
		return fmt.Sprintf("%dd", mins/(24*60)), fmt.Sprintf("%d-day limit", mins/(24*60))
	case mins > 0 && mins%60 == 0:
		return fmt.Sprintf("%dh", mins/60), fmt.Sprintf("%dh limit", mins/60)
	default:
		return fmt.Sprintf("%dm", mins), fmt.Sprintf("%d-minute limit", mins)
	}
}
