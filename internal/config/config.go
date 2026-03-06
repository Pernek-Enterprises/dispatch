package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

var Root string

func init() {
	Root = os.Getenv("DISPATCH_ROOT")
	if Root == "" {
		home, _ := os.UserHomeDir()
		Root = filepath.Join(home, ".dispatch")
	}
}

type Config struct {
	PollIntervalMs    int                `json:"pollIntervalMs"`
	PipePath          string             `json:"pipePath"`
	MaxLoopIterations int                `json:"maxLoopIterations"`
	DefaultTimeouts   map[string]int     `json:"defaultTimeouts"`
	Notifications     NotificationConfig `json:"notifications"`
	Pi                PiConfig           `json:"pi"`
	OpenClaw          PiConfig           `json:"openclaw"` // deprecated, use pi
}

type NotificationConfig struct {
	Escalation string `json:"escalation"` // channel: discord, telegram, etc.
	Target     string `json:"target"`     // reply-to target: #channel, +phone, etc.
	Channel    string `json:"channel"`    // deprecated alias for target
}

type PiConfig struct {
	// Path to the Pi binary (default: auto-detect)
	Binary       string   `json:"binary"`
	// Default tools for Pi (default: read,bash,edit,write)
	DefaultTools []string `json:"defaultTools"`
}

// Kept for backward compat — will be removed
type OpenClawConfig = PiConfig

type Model struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
}

type Agent struct {
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	Notify       []string `json:"notify,omitempty"`
}

func Load() (*Config, error) {
	cfg := &Config{
		PollIntervalMs:    30000,
		PipePath:          "/tmp/dispatch.pipe",
		MaxLoopIterations: 3,
	}
	if err := loadJSON("config.json", cfg); err != nil {
		return nil, err
	}
	if cfg.PipePath == "" {
		cfg.PipePath = "/tmp/dispatch.pipe"
	}
	if cfg.PollIntervalMs == 0 {
		cfg.PollIntervalMs = 30000
	}
	if cfg.MaxLoopIterations == 0 {
		cfg.MaxLoopIterations = 3
	}
	// Merge deprecated openclaw config into pi
	if cfg.Pi.Binary == "" && cfg.OpenClaw.Binary != "" {
		cfg.Pi.Binary = cfg.OpenClaw.Binary
	}
	return cfg, nil
}

func LoadModels() (map[string]Model, error) {
	models := make(map[string]Model)
	if err := loadJSON("models.json", &models); err != nil {
		return nil, err
	}
	return models, nil
}

func LoadAgents() (map[string]Agent, error) {
	agents := make(map[string]Agent)
	if err := loadJSON("agents.json", &agents); err != nil {
		return nil, err
	}
	return agents, nil
}

func loadJSON(name string, v interface{}) error {
	p := filepath.Join(Root, name)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			example := filepath.Join(Root, name+".example")
			if _, e2 := os.Stat(example); e2 == nil {
				return fmt.Errorf("%s not found. Copy %s.example to %s and configure it", name, name, name)
			}
			return fmt.Errorf("%s not found", name)
		}
		return err
	}
	return json.Unmarshal(data, v)
}

// EnsureDirs creates the required directory structure.
func EnsureDirs() {
	dirs := []string{
		"jobs/pending", "jobs/active", "jobs/done", "jobs/failed",
		"artifacts", "logs", "workflows", "sessions", "prompts",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(Root, d), 0755)
	}
}
