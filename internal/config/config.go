package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Provider                    string   `json:"provider"`
	Model                       string   `json:"model"`
	Effort                      string   `json:"effort,omitempty"`
	AutoCompactAt               float64  `json:"auto_compact_at"`
	BashTimeoutSeconds          int      `json:"bash_timeout_seconds"`
	ProjectDocFallbackFilenames []string `json:"project_doc_fallback_filenames,omitempty"`
	ProjectDocMaxBytes          int      `json:"project_doc_max_bytes,omitempty"`
}

func Defaults() Config {
	// A new installation deliberately has no provider or model. Atom must not
	// assume either a credential or a model until the user has signed in.
	return Config{AutoCompactAt: .80, BashTimeoutSeconds: 120, ProjectDocMaxBytes: 32 * 1024}
}

func Load(workdir string) (Config, error) {
	cfg := Defaults()
	if home, err := Home(); err == nil {
		var err error
		cfg, err = loadInto(cfg, filepath.Join(home, "config.json"))
		if err != nil {
			return cfg, err
		}
	}
	return loadInto(cfg, filepath.Join(workdir, ".atom", "config.json"))
}

func loadInto(cfg Config, path string) (Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	var overrides configOverrides
	if err := json.Unmarshal(data, &overrides); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if overrides.Provider != nil {
		cfg.Provider = *overrides.Provider
	}
	if overrides.Model != nil {
		cfg.Model = *overrides.Model
	}
	if overrides.Effort != nil {
		cfg.Effort = *overrides.Effort
	}
	if overrides.AutoCompactAt != nil {
		cfg.AutoCompactAt = *overrides.AutoCompactAt
	}
	if overrides.BashTimeoutSeconds != nil {
		cfg.BashTimeoutSeconds = *overrides.BashTimeoutSeconds
	}
	if overrides.ProjectDocFallbackFilenames != nil {
		cfg.ProjectDocFallbackFilenames = *overrides.ProjectDocFallbackFilenames
	}
	if overrides.ProjectDocMaxBytes != nil {
		cfg.ProjectDocMaxBytes = *overrides.ProjectDocMaxBytes
	}
	cfg = normalize(cfg)
	return cfg, nil
}

type configOverrides struct {
	Provider                    *string   `json:"provider"`
	Model                       *string   `json:"model"`
	Effort                      *string   `json:"effort"`
	AutoCompactAt               *float64  `json:"auto_compact_at"`
	BashTimeoutSeconds          *int      `json:"bash_timeout_seconds"`
	ProjectDocFallbackFilenames *[]string `json:"project_doc_fallback_filenames"`
	ProjectDocMaxBytes          *int      `json:"project_doc_max_bytes"`
}

func normalize(cfg Config) Config {
	defaults := Defaults()
	if cfg.AutoCompactAt <= 0 || cfg.AutoCompactAt >= 1 {
		cfg.AutoCompactAt = defaults.AutoCompactAt
	}
	if cfg.BashTimeoutSeconds <= 0 {
		cfg.BashTimeoutSeconds = defaults.BashTimeoutSeconds
	}
	if cfg.ProjectDocMaxBytes <= 0 {
		cfg.ProjectDocMaxBytes = 32 * 1024
	}
	return cfg
}

func Home() (string, error) {
	if value := os.Getenv("ATOM_HOME"); value != "" {
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".atom"), nil
}

func Save(workdir string, cfg Config) error {
	path := filepath.Join(workdir, ".atom", "config.json")
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
