package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	return append(args, "-")
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
		var runErr error
		stderr, runErr = p.Runner(ctx, p.Bin, p.Args(req, dir), dir, req.Prompt(), nil, turn.onLine)
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
	if req.Size != "" && req.Size != "auto" {
		brief += "\n\nImage size: " + req.Size + " pixels."
	}
	chat := ChatRequest{Model: req.Model, Effort: req.Effort, System: codexImageInstructions,
		Messages: []Message{{Role: "user", Content: brief}}}
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
