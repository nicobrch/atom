package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		body, err := json.Marshal(map[string]any{"model": req.Model, "instructions": req.System, "input": input, "tools": tools, "stream": true, "store": false})
		if err != nil {
			errs <- err
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		httpReq.Header.Set("Authorization", "Bearer "+p.Token)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if p.AccountID != "" {
			httpReq.Header.Set("ChatGPT-Account-Id", p.AccountID)
		}
		client := p.Client
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			errs <- fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
			return
		}
		if err := parseResponsesSSE(resp.Body, events); err != nil {
			errs <- err
		}
	}()
	return events, errs
}

func parseResponsesSSE(r io.Reader, out chan<- agent.StreamEvent) error {
	return parseSSELines(r, func(data []byte) error {
		var event struct {
			Type        string `json:"type"`
			Delta       string `json:"delta"`
			OutputIndex int    `json:"output_index"`
			Item        struct {
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			} `json:"item"`
			Response struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		switch event.Type {
		case "response.output_text.delta":
			out <- agent.StreamEvent{TextDelta: event.Delta}
		case "response.function_call_arguments.delta":
			out <- agent.StreamEvent{ToolCallDelta: &agent.ToolCallDelta{Index: event.OutputIndex, Arguments: event.Delta}}
		case "response.output_item.added":
			if event.Item.Name != "" {
				out <- agent.StreamEvent{ToolCallDelta: &agent.ToolCallDelta{Index: event.OutputIndex, ID: event.Item.CallID, Name: event.Item.Name}}
			}
		case "response.completed":
			out <- agent.StreamEvent{FinishReason: "stop", InputTokens: event.Response.Usage.InputTokens, OutputTokens: event.Response.Usage.OutputTokens}
		case "response.failed", "error":
			return fmt.Errorf("responses request failed")
		}
		return nil
	})
}
