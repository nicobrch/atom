package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type savedAuth struct {
	OpenAIAPIKey      string `json:"openai_api_key,omitempty"`
	CopilotToken      string `json:"copilot_token,omitempty"`
	CopilotOAuthToken string `json:"copilot_oauth_token,omitempty"`
}

func authPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".atom", "auth.json"), nil
}

func loadAuth() (savedAuth, error) {
	path, err := authPath()
	if err != nil {
		return savedAuth{}, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return savedAuth{}, nil
	}
	if err != nil {
		return savedAuth{}, fmt.Errorf("read %s: %w", path, err)
	}
	var auth savedAuth
	if err := json.Unmarshal(b, &auth); err != nil {
		return savedAuth{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return auth, nil
}

func saveAuth(update func(*savedAuth)) error {
	path, err := authPath()
	if err != nil {
		return err
	}
	auth, err := loadAuth()
	if err != nil {
		return err
	}
	update(&auth)
	b, err := json.Marshal(auth)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

func SaveAPIKey(name, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("API key is required")
	}
	switch name {
	case "openai":
		return saveAuth(func(auth *savedAuth) { auth.OpenAIAPIKey = key })
	case "copilot", "github-copilot":
		return saveAuth(func(auth *savedAuth) { auth.CopilotToken = key })
	default:
		return fmt.Errorf("unknown provider %q", name)
	}
}

func SaveCopilotToken(token string) error {
	return SaveAPIKey("copilot", token)
}

func SaveCopilotOAuthToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("Copilot OAuth token is required")
	}
	return saveAuth(func(auth *savedAuth) { auth.CopilotOAuthToken = token })
}
