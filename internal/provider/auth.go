package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type savedAuth struct {
	OpenAIAPIKey string           `json:"openai_api_key,omitempty"`
	CopilotToken string           `json:"copilot_token,omitempty"`
	OpenAIOAuth  *OAuthCredential `json:"openai_oauth,omitempty"`
}

type OAuthCredential struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"account_id,omitempty"`
}

func authPath() (string, error) {
	if home := os.Getenv("ATOM_HOME"); home != "" {
		return filepath.Join(home, "auth.json"), nil
	}
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func SaveAPIKey(name, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("API key is required")
	}
	switch name {
	case "openai":
		return saveAuth(func(auth *savedAuth) {
			auth.OpenAIAPIKey = key
			auth.OpenAIOAuth = nil
		})
	case "copilot", "github-copilot":
		return saveAuth(func(auth *savedAuth) { auth.CopilotToken = key })
	default:
		return fmt.Errorf("unknown provider %q", name)
	}
}

func SaveCopilotToken(token string) error {
	return SaveAPIKey("copilot", token)
}

func saveOpenAIOAuth(credential OAuthCredential) error {
	return saveAuth(func(auth *savedAuth) {
		auth.OpenAIAPIKey = ""
		auth.OpenAIOAuth = &credential
	})
}
