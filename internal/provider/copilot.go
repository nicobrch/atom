package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/nicobrch/atom/internal/agent"
)

const copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

var copilotProxyEndpoint = regexp.MustCompile(`(?:^|;)proxy-ep=([^;]+)`)

type copilotAccess struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// CopilotFromEnv uses Atom's stored GitHub device-flow token directly. Keeping
// tool execution in Atom's provider-neutral loop makes every call observable,
// cancellable, and resumable instead of hiding it inside Copilot CLI sessions.
func CopilotFromEnv() (agent.Provider, error) {
	auth, err := loadAuth()
	if err != nil {
		return nil, err
	}
	githubToken := strings.TrimSpace(auth.CopilotToken)
	if githubToken == "" {
		githubToken = strings.TrimSpace(os.Getenv("COPILOT_GITHUB_TOKEN"))
	}
	if githubToken == "" {
		return nil, fmt.Errorf("GitHub Copilot credentials not found: run `atom login copilot`")
	}

	p := &OpenAICompatible{ProviderName: "copilot", Headers: map[string]string{
		"User-Agent":             "GitHubCopilotChat/0.35.0",
		"Editor-Version":         "vscode/1.107.0",
		"Editor-Plugin-Version":  "copilot-chat/0.35.0",
		"Copilot-Integration-Id": "vscode-chat",
		"X-GitHub-Api-Version":   "2026-06-01",
		"Openai-Intent":          "conversation-edits",
	}}
	var expires time.Time
	p.Refresh = func(ctx context.Context) error {
		if p.Token != "" && time.Until(expires) > 5*time.Minute {
			return nil
		}
		access, err := exchangeCopilotToken(ctx, http.DefaultClient, copilotTokenURL, githubToken)
		if err != nil {
			return err
		}
		p.Token = access.Token
		p.BaseURL = copilotBaseURL(access.Token)
		expires = time.Unix(access.ExpiresAt, 0)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p.refreshAuth(ctx); err != nil {
		return nil, err
	}
	if _, err := p.Models(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func exchangeCopilotToken(ctx context.Context, client *http.Client, endpoint, githubToken string) (copilotAccess, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return copilotAccess{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
	req.Header.Set("Editor-Version", "vscode/1.107.0")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.35.0")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	resp, err := client.Do(req)
	if err != nil {
		return copilotAccess{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		detail := strings.TrimSpace(string(body))
		if resp.StatusCode == http.StatusForbidden {
			detail += "; run `atom login copilot` to replace credentials created by older Atom versions"
		}
		return copilotAccess{}, fmt.Errorf("get Copilot token: %s: %s", resp.Status, detail)
	}
	var access copilotAccess
	if err := json.NewDecoder(resp.Body).Decode(&access); err != nil {
		return copilotAccess{}, err
	}
	if access.Token == "" || access.ExpiresAt == 0 {
		return copilotAccess{}, fmt.Errorf("invalid Copilot token response")
	}
	return access, nil
}

func copilotBaseURL(token string) string {
	match := copilotProxyEndpoint.FindStringSubmatch(token)
	if len(match) != 2 {
		return "https://api.individual.githubcopilot.com"
	}
	return "https://" + strings.Replace(match[1], "proxy.", "api.", 1)
}
