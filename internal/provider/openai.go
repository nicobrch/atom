package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nicobrch/atom/internal/agent"
)

// OpenAICompatible implements the streaming Chat Completions protocol. Keeping
// this adapter wire-compatible makes the provider seam useful for OpenAI,
// Copilot, and private gateways without pulling an SDK into Atom.
type OpenAICompatible struct {
	ProviderName string
	BaseURL      string
	Token        string
	Headers      map[string]string
	Client       *http.Client
	Responses    bool
	AccountID    string
}

type Model struct {
	ID            string
	Name          string
	Efforts       []string
	DefaultEffort string
}

// Models returns models granted to this exact credential. ChatGPT accounts use
// Codex's own account-aware catalog; API and Copilot credentials ask their API.
func (p *OpenAICompatible) Models(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if p.Responses {
		return codexModels(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("list models: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var data struct {
		Data []struct {
			ID                 string   `json:"id"`
			Name               string   `json:"name"`
			ModelPickerEnabled bool     `json:"model_picker_enabled"`
			SupportedEndpoints []string `json:"supported_endpoints"`
			Policy             struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities struct {
				Supports struct {
					ReasoningEffort []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	models := make([]Model, 0, len(data.Data))
	for _, item := range data.Data {
		if item.ID == "" || item.Policy.State == "disabled" {
			continue
		}
		if p.ProviderName == "copilot" && (!item.ModelPickerEnabled || (len(item.SupportedEndpoints) > 0 && !contains(item.SupportedEndpoints, "/chat/completions"))) {
			continue
		}
		models = append(models, Model{ID: item.ID, Name: item.Name, Efforts: item.Capabilities.Supports.ReasoningEffort})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	if len(models) == 0 {
		return nil, fmt.Errorf("no selectable models available for %s", p.ProviderName)
	}
	return models, nil
}

func codexModels(ctx context.Context) ([]Model, error) {
	output, err := exec.CommandContext(ctx, "codex", "debug", "models").Output()
	if err != nil {
		return nil, fmt.Errorf("read ChatGPT models with Codex: %w", err)
	}
	var catalog struct {
		Models []struct {
			Slug           string `json:"slug"`
			DisplayName    string `json:"display_name"`
			Visibility     string `json:"visibility"`
			SupportedInAPI bool   `json:"supported_in_api"`
			DefaultEffort  string `json:"default_reasoning_level"`
			Efforts        []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(output, &catalog); err != nil {
		return nil, fmt.Errorf("decode ChatGPT models: %w", err)
	}
	models := make([]Model, 0, len(catalog.Models))
	for _, item := range catalog.Models {
		if item.Slug != "" && item.Visibility == "list" && item.SupportedInAPI {
			efforts := make([]string, 0, len(item.Efforts))
			for _, effort := range item.Efforts {
				if effort.Effort != "" {
					efforts = append(efforts, effort.Effort)
				}
			}
			models = append(models, Model{ID: item.Slug, Name: item.DisplayName, Efforts: efforts, DefaultEffort: item.DefaultEffort})
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no models available for this ChatGPT account")
	}
	return models, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func OpenAIFromEnv() (*OpenAICompatible, error) {
	saved, err := loadAuth()
	if err != nil {
		return nil, err
	}
	if saved.OpenAIAPIKey != "" {
		return &OpenAICompatible{ProviderName: "openai", BaseURL: "https://api.openai.com/v1", Token: saved.OpenAIAPIKey}, nil
	}
	if subscription, err := codexSubscription(); err == nil {
		return subscription, nil
	}
	token, err := OpenAIKey()
	if err != nil {
		return nil, err
	}
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &OpenAICompatible{ProviderName: "openai", BaseURL: base, Token: token}, nil
}

func codexSubscription() (*OpenAICompatible, error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".codex")
	}
	b, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return nil, err
	}
	var auth struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(b, &auth); err != nil {
		return nil, err
	}
	if auth.AuthMode != "chatgpt" || strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return nil, fmt.Errorf("Codex ChatGPT subscription not found")
	}
	return &OpenAICompatible{ProviderName: "openai", BaseURL: codexResponsesURL, Token: auth.Tokens.AccessToken, AccountID: auth.Tokens.AccountID, Responses: true}, nil
}

// OpenAIKey first honors the explicit API-key environment variable. When it is
// absent, it reuses the API-style credential created by Codex's supported
// "Sign in with ChatGPT" flow. Atom never copies that credential or writes it
// to a project file; it is read only for the request being made.
func OpenAIKey() (string, error) {
	if token := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); token != "" {
		return token, nil
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(userHome, ".codex")
	}
	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("OpenAI credentials not found: set OPENAI_API_KEY or run `atom auth openai`")
		}
		return "", fmt.Errorf("read Codex credentials: %w", err)
	}
	var auth struct {
		APIKey string `json:"OPENAI_API_KEY"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", fmt.Errorf("parse Codex credentials: %w", err)
	}
	if token := strings.TrimSpace(auth.APIKey); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("Codex is signed in but no reusable API credential is available; run `codex --login` again or set OPENAI_API_KEY")
}

func CopilotFromEnv() (*OpenAICompatible, error) {
	saved, err := loadAuth()
	if err != nil {
		return nil, err
	}
	token := saved.CopilotToken
	if token == "" {
		token = os.Getenv("COPILOT_TOKEN")
	}
	if token == "" {
		token = os.Getenv("COPILOT_GITHUB_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("Copilot credentials not found: run `/login copilot` or `atom login copilot subscription`")
	}
	base := os.Getenv("COPILOT_BASE_URL")
	if base == "" {
		base = "https://api.githubcopilot.com"
	}
	return &OpenAICompatible{
		ProviderName: "copilot", BaseURL: base, Token: token,
		Headers: map[string]string{"Editor-Plugin-Version": "atom/0.1.3", "Openai-Intent": "conversation-edits"},
	}, nil
}

func (p *OpenAICompatible) Name() string { return p.ProviderName }

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Tools           []chatTool    `json:"tools,omitempty"`
	Stream          bool          `json:"stream"`
	StreamOpts      streamOptions `json:"stream_options"`
	Temperature     *float64      `json:"temperature,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}
type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}
type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}
type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (p *OpenAICompatible) Stream(ctx context.Context, req agent.Request) (<-chan agent.StreamEvent, <-chan error) {
	if p.Responses {
		return p.streamResponses(ctx, req)
	}
	events := make(chan agent.StreamEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		payload := chatRequest{Model: req.Model, Stream: true, StreamOpts: streamOptions{IncludeUsage: true}, Temperature: req.Temperature, ReasoningEffort: req.ReasoningEffort}
		if req.System != "" {
			payload.Messages = append(payload.Messages, chatMessage{Role: "system", Content: req.System})
		}
		for _, m := range req.Messages {
			cm := chatMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
			for _, call := range m.ToolCalls {
				cm.ToolCalls = append(cm.ToolCalls, toolCall{ID: call.ID, Type: "function", Function: toolFunction{Name: call.Name, Arguments: string(call.Arguments)}})
			}
			payload.Messages = append(payload.Messages, cm)
		}
		for _, t := range req.Tools {
			payload.Tools = append(payload.Tools, chatTool{Type: "function", Function: chatFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters}})
		}
		body, err := json.Marshal(payload)
		if err != nil {
			errs <- providerFailure("serialize request", err)
			return
		}
		url := strings.TrimRight(p.BaseURL, "/") + "/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			errs <- providerFailure("create request", err)
			return
		}
		httpReq.Header.Set("Authorization", "Bearer "+p.Token)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if p.ProviderName == "copilot" {
			initiator := "agent"
			if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "user" {
				initiator = "user"
			}
			httpReq.Header.Set("X-Initiator", initiator)
		}
		for k, v := range p.Headers {
			httpReq.Header.Set(k, v)
		}
		client := p.Client
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			errs <- providerFailure("transport", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			errs <- httpFailure("HTTP response", resp.StatusCode, responseRequestID(resp.Header), b)
			return
		}
		if err := parseSSE(resp.Body, events); err != nil {
			errs <- enrichFailure(err, resp.StatusCode, responseRequestID(resp.Header))
		}
	}()
	return events, errs
}

type chatChunk struct {
	ID      string    `json:"id"`
	Error   *apiError `json:"error"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func parseSSE(r io.Reader, out chan<- agent.StreamEvent) error {
	return parseSSELines(r, func(data []byte) error {
		var chunk chatChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return providerFailure("decode stream event", err)
		}
		if chunk.Error != nil {
			return responseFailure("stream", chunk.ID, *chunk.Error)
		}
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			out <- agent.StreamEvent{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				out <- agent.StreamEvent{TextDelta: c.Delta.Content}
			}
			for _, tc := range c.Delta.ToolCalls {
				out <- agent.StreamEvent{ToolCallDelta: &agent.ToolCallDelta{Index: tc.Index, ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}}
			}
			if c.FinishReason != "" {
				out <- agent.StreamEvent{FinishReason: c.FinishReason}
			}
		}
		return nil
	})
}

func parseSSELines(r io.Reader, handle func([]byte) error) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}
		if err := handle([]byte(data)); err != nil {
			return err
		}
	}
	return s.Err()
}
