package bridge

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"text/template"
)

//go:embed integration_prompt.md
var integrationPromptText string

var integrationPrompt = template.Must(template.New("integration").Parse(integrationPromptText))

type promptProvider struct {
	ProviderInfo
	Image bool
}

// IntegrationPrompt renders instructions an AI coding assistant can follow to wire a web app to this bridge.
func IntegrationPrompt(cfg *Config, providers []ProviderInfo, task string) (string, error) {
	data := struct {
		BaseURL                   string
		Origins                   []string
		MaxConcurrent, TimeoutSec int
		Providers                 []promptProvider
		Task                      string
	}{
		BaseURL:       fmt.Sprintf("http://127.0.0.1:%d", cfg.Port),
		Origins:       cfg.Origins,
		MaxConcurrent: cfg.MaxConcurrent,
		TimeoutSec:    cfg.TimeoutSec,
		Task:          strings.TrimSpace(task),
	}
	for _, p := range providers {
		if p.Available {
			data.Providers = append(data.Providers, promptProvider{p, slices.Contains(p.Capabilities, "image")})
		}
	}
	var b strings.Builder
	if err := integrationPrompt.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}
