package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// browseMinTimeout covers opening the page, the site permission prompt in Chrome and reading it.
const browseMinTimeout = 300

const maxBrowsePrompt = 32000

type BrowseRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	// URL is opened in a new tab of the user's Chrome (their logins apply).
	URL string `json:"url"`
	// Prompt says what to read from the page.
	Prompt string `json:"prompt"`
	// Schema is a JSON Schema object for `done.data`; without it the answer is free text.
	Schema json.RawMessage `json:"schema"`
}

type BrowseResult struct {
	Text  string          `json:"text"`
	Data  json.RawMessage `json:"data"`
	Model string          `json:"model"`
	Usage Usage           `json:"usage"`
}

// Browser is implemented by providers that can read pages in the user's browser.
type Browser interface {
	Browse(ctx context.Context, req BrowseRequest, emit func(delta string)) (BrowseResult, error)
}

func (r BrowseRequest) Validate() error {
	u, err := url.Parse(strings.TrimSpace(r.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return &ProviderError{"validation_error", "url must be an absolute http(s) URL"}
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return &ProviderError{"validation_error", "prompt must not be empty"}
	}
	if len(r.Prompt) > maxBrowsePrompt {
		return &ProviderError{"validation_error", fmt.Sprintf("prompt is longer than %d bytes", maxBrowsePrompt)}
	}
	if len(r.Schema) > 0 && string(r.Schema) != "null" {
		var probe map[string]any
		if json.Unmarshal(r.Schema, &probe) != nil {
			return &ProviderError{"validation_error", "schema must be a JSON Schema object"}
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

func (r BrowseRequest) hasSchema() bool { return len(r.Schema) > 0 && string(r.Schema) != "null" }

const browseSystemPrompt = `You read one web page in the user's own Chrome through the Claude in Chrome tools and report what it shows.

Rules:
- Call tabs_context_mcp with createIfEmpty true, then tabs_create_mcp for a fresh tab, then navigate it to the given URL.
- Read the page with get_page_text, and with read_page (filter "all") when you need the values of form fields, selects, checkboxes or toggles. Use find to locate a specific element.
- Only read. Never click, type, submit, change settings or open other sites; the user is logged in and actions there are real.
- If a tool says a Chrome permission prompt is waiting, retry the same call once: the user is asked to click Allow.
- When you are done, close the tab you created with tabs_close_mcp.
- Report only what the page actually shows. Leave out values that are not on the page instead of guessing.`

const browseFailurePrompt = `If the page cannot be read (permission not granted, login wall, error page, wrong page), set "ok" to false and explain why in "error".`

// browseTools are the read-only Claude in Chrome tools a browse run may use.
var browseTools = []string{"tabs_context_mcp", "tabs_create_mcp", "tabs_close_mcp", "navigate", "get_page_text", "read_page", "find"}

// browseDeniedTools act on the page or the machine; they are removed from the model's tool list.
var browseDeniedTools = []string{"computer", "browser_batch", "form_input", "javascript_tool", "file_upload", "upload_image",
	"gif_creator", "resize_window", "shortcuts_execute", "shortcuts_list", "switch_browser", "select_browser",
	"list_connected_browsers", "read_console_messages", "read_network_requests"}

func chromeTools(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "mcp__claude-in-chrome__" + n
	}
	return strings.Join(out, ",")
}

// browseSchema wraps the caller's schema so the model can report an unreadable page instead of inventing data.
func browseSchema(data json.RawMessage) string {
	if len(data) == 0 || string(data) == "null" {
		data = json.RawMessage(`{"type":"string","description":"The answer"}`)
	}
	wrapped := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok":    map[string]any{"type": "boolean", "description": "true when the page was read"},
			"error": map[string]any{"type": "string", "description": "Why the page could not be read"},
			"data":  data,
		},
		"required": []string{"ok"},
	}
	b, _ := json.Marshal(wrapped)
	return string(b)
}

// browseProgress describes a tool call for the progress stream.
func browseProgress(tool string, input map[string]any) string {
	name := strings.TrimPrefix(tool, "mcp__claude-in-chrome__")
	switch name {
	case "tabs_context_mcp", "tabs_create_mcp":
		return "Opening a Chrome tab"
	case "navigate":
		if u, ok := input["url"].(string); ok {
			return "Opening " + u
		}
		return "Opening the page"
	case "get_page_text", "read_page", "find":
		return "Reading the page"
	case "tabs_close_mcp":
		return "Closing the tab"
	}
	return ""
}
