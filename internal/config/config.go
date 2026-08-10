package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Provider           string  `json:"provider"`
	Model              string  `json:"model"`
	ContextTokens      int     `json:"context_tokens"`
	AutoCompactAt      float64 `json:"auto_compact_at"`
	BashTimeoutSeconds int     `json:"bash_timeout_seconds"`
}

func Defaults() Config {
	return Config{Provider: "openai", Model: "gpt-5.4", ContextTokens: 128000, AutoCompactAt: .80, BashTimeoutSeconds: 120}
}

func Load(workdir string) (Config, error) {
	cfg := Defaults()
	path := filepath.Join(workdir, ".atom", "config.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Provider == "" {
		cfg.Provider = "openai"
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-5.4"
	}
	if cfg.ContextTokens <= 0 {
		cfg.ContextTokens = 128000
	}
	if cfg.AutoCompactAt <= 0 || cfg.AutoCompactAt >= 1 {
		cfg.AutoCompactAt = .80
	}
	if cfg.BashTimeoutSeconds <= 0 {
		cfg.BashTimeoutSeconds = 120
	}
	return cfg, nil
}
