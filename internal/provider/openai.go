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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nicobrch/atom/internal/agent"
)

// OpenAICompatible implements the streaming Chat Completions protocol. Keeping
// this adapter wire-compatible makes the provider seam useful for OpenAI,
// Copilot, and private gateways without pulling an SDK into Atom.
type OpenAICompatible struct {
	ProviderName   string
	BaseURL        string
	Token          string
	Headers        map[string]string
	Client         *http.Client
	Responses      bool
	AccountID      string
	Refresh        func(context.Context) error
	authMu         sync.Mutex
	modelMu        sync.RWMutex
	models         []Model
	responseModels map[string]bool
}

type Model struct {
	ID            string
	Name          string
	Efforts       []string
	DefaultEffort string
	ContextTokens int
}

// Models returns provider choices. ChatGPT uses Atom's release-bundled Codex
// catalog; API and Copilot credentials ask their API.
func (p *OpenAICompatible) Models(ctx context.Context) ([]Model, error) {
	if !p.Responses {
		p.modelMu.RLock()
		cached := append([]Model(nil), p.models...)
		p.modelMu.RUnlock()
		if len(cached) > 0 {
			return cached, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.refreshAuth(ctx); err != nil {
		return nil, err
	}
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
					ToolCalls       *bool    `json:"tool_calls"`
				} `json:"supports"`
				Limits struct {
					ContextWindow          int `json:"context_window"`
					ContextLength          int `json:"context_length"`
					MaxContextTokens       int `json:"max_context_tokens"`
					MaxContextLength       int `json:"max_context_length"`
					ContextWindowTokens    int `json:"context_window_tokens"`
					MaxContextWindowTokens int `json:"max_context_window_tokens"`
				} `json:"limits"`
			} `json:"capabilities"`
			ContextWindow          int `json:"context_window"`
			ContextLength          int `json:"context_length"`
			MaxContextTokens       int `json:"max_context_tokens"`
			MaxContextLength       int `json:"max_context_length"`
			ContextWindowTokens    int `json:"context_window_tokens"`
			MaxContextWindowTokens int `json:"max_context_window_tokens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	models := make([]Model, 0, len(data.Data))
	policyModels := make([]Model, 0, len(data.Data))
	responseModels := map[string]bool{}
	for _, item := range data.Data {
		if item.ID == "" || item.Policy.State == "disabled" {
			continue
		}
		model := Model{ID: item.ID, Name: item.Name, Efforts: item.Capabilities.Supports.ReasoningEffort, ContextTokens: firstPositive(
			item.ContextWindow, item.ContextLength, item.MaxContextTokens, item.MaxContextLength, item.ContextWindowTokens, item.MaxContextWindowTokens,
			item.Capabilities.Limits.ContextWindow, item.Capabilities.Limits.ContextLength, item.Capabilities.Limits.MaxContextTokens, item.Capabilities.Limits.MaxContextLength, item.Capabilities.Limits.ContextWindowTokens, item.Capabilities.Limits.MaxContextWindowTokens,
		)}
		if p.ProviderName != "copilot" {
			models = append(models, model)
			continue
		}
		if len(item.SupportedEndpoints) > 0 && !contains(item.SupportedEndpoints, "/chat/completions") && !contains(item.SupportedEndpoints, "/responses") {
			continue
		}
		if item.Capabilities.Supports.ToolCalls != nil && !*item.Capabilities.Supports.ToolCalls {
			continue
		}
		if item.ModelPickerEnabled {
			models = append(models, model)
			responseModels[item.ID] = contains(item.SupportedEndpoints, "/responses")
		}
		if item.Policy.State == "enabled" {
			policyModels = append(policyModels, model)
			if _, exists := responseModels[item.ID]; !exists {
				responseModels[item.ID] = contains(item.SupportedEndpoints, "/responses")
			}
		}
	}
	if p.ProviderName == "copilot" && len(models) == 0 && p.BaseURL == "https://api.individual.githubcopilot.com" {
		models = policyModels
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	if len(models) == 0 {
		return nil, fmt.Errorf("no selectable models available for %s", p.ProviderName)
	}
	p.modelMu.Lock()
	p.models = append([]Model(nil), models...)
	p.responseModels = responseModels
	p.modelMu.Unlock()
	return models, nil
}

func codexModels(context.Context) ([]Model, error) {
	allEfforts := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	return []Model{
		{ID: "gpt-5.6-sol", Name: "GPT-5.6-Sol", DefaultEffort: "low", Efforts: allEfforts, ContextTokens: 272000},
		{ID: "gpt-5.6-terra", Name: "GPT-5.6-Terra", DefaultEffort: "medium", Efforts: allEfforts, ContextTokens: 272000},
		{ID: "gpt-5.6-luna", Name: "GPT-5.6-Luna", DefaultEffort: "medium", Efforts: allEfforts[:5], ContextTokens: 272000},
		{ID: "gpt-5.5", Name: "GPT-5.5", DefaultEffort: "medium", Efforts: allEfforts[:4], ContextTokens: 272000},
		{ID: "gpt-5.4", Name: "GPT-5.4", DefaultEffort: "medium", Efforts: allEfforts[:4], ContextTokens: 272000},
		{ID: "gpt-5.4-mini", Name: "GPT-5.4-Mini", DefaultEffort: "medium", Efforts: allEfforts[:4], ContextTokens: 272000},
	}, nil
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
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
	if saved.OpenAIOAuth != nil {
		credential := *saved.OpenAIOAuth
		p := &OpenAICompatible{ProviderName: "openai", BaseURL: codexResponsesURL, Responses: true}
		p.Refresh = func(ctx context.Context) error {
			if credential.Access != "" && time.Until(time.UnixMilli(credential.Expires)) > 5*time.Minute {
				p.Token, p.AccountID = credential.Access, credential.AccountID
				return nil
			}
			refreshed, err := refreshOpenAIToken(ctx, http.DefaultClient, openAITokenURL, credential.Refresh)
			if err != nil {
				return err
			}
			credential = refreshed
			p.Token, p.AccountID = credential.Access, credential.AccountID
			return saveOpenAIOAuth(credential)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := p.refreshAuth(ctx); err != nil {
			return nil, err
		}
		return p, nil
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

func OpenAIKey() (string, error) {
	if token := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("OpenAI credentials not found: set OPENAI_API_KEY or run `atom login openai`")
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
	if err := p.refreshAuth(ctx); err != nil {
		events := make(chan agent.StreamEvent)
		errs := make(chan error, 1)
		close(events)
		errs <- providerFailure("authenticate", err)
		close(errs)
		return events, errs
	}
	if p.Responses || p.usesResponses(req.Model) {
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

func (p *OpenAICompatible) usesResponses(model string) bool {
	p.modelMu.RLock()
	defer p.modelMu.RUnlock()
	return p.responseModels[model]
}

func (p *OpenAICompatible) refreshAuth(ctx context.Context) error {
	if p.Refresh == nil {
		return nil
	}
	p.authMu.Lock()
	defer p.authMu.Unlock()
	return p.Refresh(ctx)
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
