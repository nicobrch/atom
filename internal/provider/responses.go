package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/nicobrch/atom/internal/agent"
)

const codexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"

func (p *OpenAICompatible) streamResponses(ctx context.Context, req agent.Request) (<-chan agent.StreamEvent, <-chan error) {
	events := make(chan agent.StreamEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		input := make([]any, 0, len(req.Messages))
		for _, m := range req.Messages {
			switch m.Role {
			case "tool":
				input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
			default:
				for _, item := range m.ProviderItems {
					input = append(input, item)
				}
				if m.Content != "" {
					input = append(input, map[string]any{"role": m.Role, "content": m.Content})
				}
				for _, call := range m.ToolCalls {
					input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": string(call.Arguments)})
				}
			}
		}
		tools := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters})
		}
		payload := map[string]any{
			"model": req.Model, "instructions": req.System, "input": input, "tools": tools,
			"stream": true, "store": false, "tool_choice": "auto", "parallel_tool_calls": true,
			"include": []string{"reasoning.encrypted_content"}, "text": map[string]string{"verbosity": "low"},
		}
		if req.ReasoningEffort != "" {
			payload["reasoning"] = map[string]string{"effort": req.ReasoningEffort}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			errs <- providerFailure("serialize request", err)
			return
		}
		endpoint := p.BaseURL
		if !p.Responses {
			endpoint = strings.TrimRight(endpoint, "/") + "/responses"
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			errs <- providerFailure("create request", err)
			return
		}
		httpReq.Header.Set("Authorization", "Bearer "+p.Token)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if p.Responses {
			httpReq.Header.Set("OpenAI-Beta", "responses=experimental")
			httpReq.Header.Set("Originator", "atom")
			httpReq.Header.Set("User-Agent", "atom")
			if p.AccountID != "" {
				httpReq.Header.Set("ChatGPT-Account-Id", p.AccountID)
			}
		}
		if p.ProviderName == "copilot" {
			initiator := "agent"
			if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "user" {
				initiator = "user"
			}
			httpReq.Header.Set("X-Initiator", initiator)
		}
		for key, value := range p.Headers {
			httpReq.Header.Set(key, value)
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
		if err := parseResponsesSSE(resp.Body, events); err != nil {
			errs <- enrichFailure(err, resp.StatusCode, responseRequestID(resp.Header))
		}
	}()
	return events, errs
}

func parseResponsesSSE(r io.Reader, out chan<- agent.StreamEvent) error {
	arguments := map[int]string{}
	return parseSSELines(r, func(data []byte) error {
		var event struct {
			Type        string          `json:"type"`
			Delta       string          `json:"delta"`
			Arguments   string          `json:"arguments"`
			OutputIndex int             `json:"output_index"`
			Item        json.RawMessage `json:"item"`
			Error       apiError        `json:"error"`
			Response    struct {
				ID    string   `json:"id"`
				Error apiError `json:"error"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return providerFailure("decode stream event", err)
		}
		switch event.Type {
		case "response.output_text.delta":
			out <- agent.StreamEvent{TextDelta: event.Delta}
		case "response.function_call_arguments.delta":
			arguments[event.OutputIndex] += event.Delta
			out <- agent.StreamEvent{ToolCallDelta: &agent.ToolCallDelta{Index: event.OutputIndex, Arguments: event.Delta}}
		case "response.function_call_arguments.done":
			if event.Arguments != arguments[event.OutputIndex] {
				out <- agent.StreamEvent{ToolCallDelta: finalArgumentsDelta(event.OutputIndex, arguments[event.OutputIndex], event.Arguments)}
				arguments[event.OutputIndex] = event.Arguments
			}
		case "response.output_item.added":
			var item struct {
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			}
			if json.Unmarshal(event.Item, &item) == nil && item.Name != "" {
				out <- agent.StreamEvent{ToolCallDelta: &agent.ToolCallDelta{Index: event.OutputIndex, ID: item.CallID, Name: item.Name}}
			}
		case "response.output_item.done":
			var item struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal(event.Item, &item) == nil {
				if item.Type == "reasoning" {
					out <- agent.StreamEvent{ProviderItem: append(json.RawMessage(nil), event.Item...)}
				} else if item.Type == "function_call" {
					delta := finalArgumentsDelta(event.OutputIndex, arguments[event.OutputIndex], item.Arguments)
					delta.ID, delta.Name = item.CallID, item.Name
					if delta.ID != "" || delta.Name != "" || delta.Arguments != "" {
						out <- agent.StreamEvent{ToolCallDelta: delta}
					}
					arguments[event.OutputIndex] = item.Arguments
				}
			}
		case "response.completed":
			out <- agent.StreamEvent{FinishReason: "stop", InputTokens: event.Response.Usage.InputTokens, OutputTokens: event.Response.Usage.OutputTokens}
		case "response.failed":
			detail := event.Response.Error
			if detail.Message == "" {
				detail = event.Error
			}
			return responseFailure("stream", event.Response.ID, detail)
		case "error":
			return responseFailure("stream", event.Response.ID, event.Error)
		}
		return nil
	})
}

func finalArgumentsDelta(index int, streamed, final string) *agent.ToolCallDelta {
	delta := &agent.ToolCallDelta{Index: index}
	if strings.HasPrefix(final, streamed) {
		delta.Arguments = final[len(streamed):]
	} else {
		delta.Arguments, delta.ResetArguments = final, true
	}
	return delta
}
