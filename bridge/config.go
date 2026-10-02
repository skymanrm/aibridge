package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const defaultPort = 7777

// Version is overridden at build time via -ldflags "-X .../bridge.Version=…".
var Version = "0.3.0"

// Config lives in ~/.config/ai-bridge/config.json (0600).
type Config struct {
	Port          int               `json:"port"`
	Token         string            `json:"token"`
	Origins       []string          `json:"origins"`
	MaxConcurrent int               `json:"max_concurrent"`
	TimeoutSec    int               `json:"timeout_sec"`
	Binaries      map[string]string `json:"binaries,omitempty"`

	path string
}

// ConfigPath honours AI_BRIDGE_CONFIG, else ~/.config/ai-bridge/config.json.
func ConfigPath() (string, error) {
	if p := os.Getenv("AI_BRIDGE_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ai-bridge", "config.json"), nil
}

// LoadConfig reads the config, creating it with a fresh token on first use.
func LoadConfig() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	cfg := &Config{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	dirty := cfg.applyDefaults()
	if dirty {
		if err := cfg.Save(); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func (c *Config) applyDefaults() (dirty bool) {
	if c.Token == "" {
		c.Token = NewToken()
		dirty = true
	}
	if c.Port == 0 {
		c.Port = defaultPort
		dirty = true
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 2
		dirty = true
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 300
		dirty = true
	}
	if c.Origins == nil {
		c.Origins = []string{}
		dirty = true
	}
	return dirty
}

func (c *Config) Path() string { return c.path }

func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, append(data, '\n'), 0o600)
}

func NewToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "aib_" + hex.EncodeToString(b)
}

// NormalizeOrigin turns "https://Site.example/path" into "https://site.example".
func NormalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid origin %q: expected scheme://host[:port]", raw)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func (c *Config) AllowOrigin(raw string) (string, error) {
	origin, err := NormalizeOrigin(raw)
	if err != nil {
		return "", err
	}
	if !slices.Contains(c.Origins, origin) {
		c.Origins = append(c.Origins, origin)
	}
	return origin, c.Save()
}

func (c *Config) DenyOrigin(raw string) (string, error) {
	origin, err := NormalizeOrigin(raw)
	if err != nil {
		return "", err
	}
	c.Origins = slices.DeleteFunc(c.Origins, func(o string) bool { return o == origin })
	return origin, c.Save()
}

func (c *Config) OriginAllowed(origin string) bool {
	return slices.Contains(c.Origins, strings.ToLower(origin))
}
