package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// CodexProvider drives `codex exec` (ChatGPT login) in a read-only sandbox without user config.
type CodexProvider struct {
	Bin    string
	Runner Runner
}

func (p *CodexProvider) ID() string { return "codex" }

// Efforts that spawn sub-agents make no sense for single-shot text tasks.
var codexSkippedEfforts = []string{"ultra"}

func (p *CodexProvider) Detect(ctx context.Context) ProviderInfo {
	info := ProviderInfo{ID: p.ID(), Name: "Codex (ChatGPT)", Models: []Model{}, Efforts: []string{}}
	if p.Bin == "" {
		info.Error = "codex CLI not found"
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
	Type string `json:"type"`
	Item *struct {
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

func (p *CodexProvider) Run(ctx context.Context, req ChatRequest, emit func(string)) (ChatResult, error) {
	res := ChatResult{Model: req.Model}
	var (
		currentID, currentText string
		lastMessage            string
		errMsg                 string
		completed              bool
	)
	onLine := func(line []byte) {
		var l codexLine
		if json.Unmarshal(line, &l) != nil {
			return
		}
		switch l.Type {
		case "item.started", "item.updated", "item.completed":
			if l.Item == nil || l.Item.Type != "agent_message" {
				return
			}
			if l.Item.ID != currentID {
				if currentText != "" {
					emit("\n\n")
				}
				currentID, currentText = l.Item.ID, ""
			}
			if strings.HasPrefix(l.Item.Text, currentText) && len(l.Item.Text) > len(currentText) {
				emit(l.Item.Text[len(currentText):])
			}
			currentText = l.Item.Text
			if l.Type == "item.completed" {
				lastMessage = l.Item.Text
			}
		case "error":
			errMsg = codexErrorMessage(l.Message)
		case "turn.failed":
			if l.Error != nil {
				errMsg = codexErrorMessage(l.Error.Message)
			}
		case "turn.completed":
			completed = true
			if l.Usage != nil {
				res.Usage = Usage{InputTokens: l.Usage.InputTokens, OutputTokens: l.Usage.OutputTokens}
			}
		}
	}
	var stderr string
	err := withTempDir(func(dir string) error {
		var runErr error
		stderr, runErr = p.Runner(ctx, p.Bin, p.Args(req, dir), dir, req.Prompt(), onLine)
		return runErr
	})
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if errMsg != "" {
		return res, &ProviderError{"provider_failed", errMsg}
	}
	if !completed {
		msg := stderr
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "codex exited without completing the turn"
		}
		return res, &ProviderError{"provider_failed", msg}
	}
	res.Text = lastMessage
	return res, nil
}
