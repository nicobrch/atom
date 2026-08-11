package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicobrch/atom/internal/agent"
)

type integrationTool struct{ calls int }

func (t *integrationTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (t *integrationTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	t.calls++
	if string(args) != `{"path":"note.txt"}` {
		return "", fmt.Errorf("unexpected arguments %s", args)
	}
	return "hello from disk", nil
}

type integrationSink struct{ kinds []string }

func (s *integrationSink) WriteEvent(kind string, _ any) error {
	s.kinds = append(s.kinds, kind)
	return nil
}

func TestOpenAICompatibleRunsToolLoopEndToEnd(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			if len(body.Messages) != 2 || body.Messages[1].Content != "read note.txt" || len(body.Tools) != 1 {
				t.Errorf("first request = %#v", body)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"note.txt\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			if len(body.Messages) != 4 || body.Messages[2].ToolCalls[0].Function.Name != "read" || body.Messages[3].Content != "hello from disk" {
				t.Errorf("second request = %#v", body)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"verified\"},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()

	tool := &integrationTool{}
	sink := &integrationSink{}
	loop := &agent.Loop{
		Provider: &OpenAICompatible{ProviderName: "openai", BaseURL: s.URL, Token: "test"},
		Model:    "test", System: "system", Tools: []agent.Tool{tool}, Sink: sink,
	}
	if err := loop.Prompt(context.Background(), "read note.txt"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || tool.calls != 1 || len(loop.Messages) != 4 || loop.Messages[3].Content != "verified" {
		t.Fatalf("requests=%d tool calls=%d messages=%#v", requests, tool.calls, loop.Messages)
	}
	if len(sink.kinds) != 4 {
		t.Fatalf("durable events = %q, want four messages", sink.kinds)
	}
}

func TestResponsesRunsToolLoopAndReplaysReasoningEndToEnd(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"read\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"{\\\"path\\\":\\\"wrong\\\"}\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":1,\"arguments\":\"{\\\"path\\\":\\\"note.txt\\\"}\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"opaque\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
		} else {
			var joined strings.Builder
			for _, item := range body.Input {
				joined.Write(item)
				joined.WriteByte('\n')
			}
			for _, want := range []string{`"encrypted_content":"opaque"`, `"type":"function_call"`, `"type":"function_call_output"`, `"output":"hello from disk"`} {
				if !strings.Contains(joined.String(), want) {
					t.Errorf("second request missing %s: %s", want, joined.String())
				}
			}
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"verified\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
		}
	}))
	defer s.Close()

	tool := &integrationTool{}
	loop := &agent.Loop{Provider: &OpenAICompatible{ProviderName: "openai", BaseURL: s.URL, Token: "test", Responses: true}, Model: "test", Tools: []agent.Tool{tool}}
	if err := loop.Prompt(context.Background(), "read note.txt"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || tool.calls != 1 || len(loop.Messages[1].ProviderItems) != 1 || loop.Messages[3].Content != "verified" {
		t.Fatalf("requests=%d tool calls=%d messages=%#v", requests, tool.calls, loop.Messages)
	}
}

func TestResponsesSSEStreamsTextAndTools(t *testing.T) {
	input := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"call_id\":\"call-1\",\"name\":\"read\"}}\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{}\"}\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"opaque\"}}\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n"
	out := make(chan agent.StreamEvent, 5)
	if err := parseResponsesSSE(bytes.NewBufferString(input), out); err != nil {
		t.Fatal(err)
	}
	close(out)
	var events []agent.StreamEvent
	for e := range out {
		events = append(events, e)
	}
	if len(events) != 5 || events[0].ToolCallDelta.Name != "read" || events[1].ToolCallDelta.Arguments != "{}" || events[2].TextDelta != "done" || !bytes.Contains(events[3].ProviderItem, []byte(`"encrypted_content":"opaque"`)) || events[4].InputTokens != 3 {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestResponsesSSEPreservesFailureDetails(t *testing.T) {
	input := `data: {"type":"response.failed","response":{"id":"resp_123","error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"Try again later"}}}` + "\n"
	err := parseResponsesSSE(bytes.NewBufferString(input), make(chan agent.StreamEvent))
	var failure *agent.ProviderError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want ProviderError", err)
	}
	if failure.Code != "rate_limit_exceeded" || failure.Type != "rate_limit_error" || failure.ResponseID != "resp_123" || failure.Message != "Try again later" {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestResponsesSSEFinalArgumentsReplaceDivergentStream(t *testing.T) {
	input := "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"path\\\":\\\"wrong\"}\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"arguments\":\"{\\\"path\\\":\\\"note.txt\\\"}\"}\n"
	out := make(chan agent.StreamEvent, 2)
	if err := parseResponsesSSE(bytes.NewBufferString(input), out); err != nil {
		t.Fatal(err)
	}
	close(out)
	first, second := <-out, <-out
	if first.ToolCallDelta.Arguments == "" || !second.ToolCallDelta.ResetArguments || second.ToolCallDelta.Arguments != `{"path":"note.txt"}` {
		t.Fatalf("events = %#v, %#v", first, second)
	}
}

func TestResponsesHTTPFailurePreservesRequestIDAndDetails(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "req_123")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"Try again later"}}`)
	}))
	defer s.Close()
	p := &OpenAICompatible{BaseURL: s.URL, Token: "test", Responses: true}
	events, errs := p.Stream(context.Background(), agent.Request{Model: "test"})
	for range events {
	}
	err := <-errs
	var failure *agent.ProviderError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want ProviderError", err)
	}
	if failure.StatusCode != http.StatusTooManyRequests || failure.RequestID != "req_123" || failure.Code != "rate_limit_exceeded" {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestOpenAICompatibleStreamsTextAndTools(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\",\"tool_calls\":[{\"index\":0,\"id\":\"x\",\"function\":{\"name\":\"read\",\"arguments\":\"{\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"}\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	p := &OpenAICompatible{BaseURL: s.URL, Token: "test"}
	events, errs := p.Stream(context.Background(), agent.Request{Model: "test"})
	var got []agent.StreamEvent
	for e := range events {
		got = append(got, e)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].TextDelta != "hi" || got[1].ToolCallDelta.Name != "read" || got[3].FinishReason != "tool_calls" {
		t.Fatalf("unexpected events: %#v", got)
	}
}

func TestOpenAIKeyReadsEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "from-environment")
	key, err := OpenAIKey()
	if err != nil || key != "from-environment" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestCopilotModelsUsesPickerAndChatCapabilities(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer test" {
			t.Fatalf("unexpected request: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"data":[
			{"id":"enabled","name":"Enabled","model_picker_enabled":true,"supported_endpoints":["/responses","/chat/completions"],"capabilities":{"limits":{"context_window":1000000}}},
			{"id":"responses-only","model_picker_enabled":true,"supported_endpoints":["/responses"]},
			{"id":"messages-only","model_picker_enabled":true,"supported_endpoints":["/v1/messages"]},
			{"id":"no-tools","model_picker_enabled":true,"supported_endpoints":["/chat/completions"],"capabilities":{"supports":{"tool_calls":false}}},
			{"id":"hidden","model_picker_enabled":false,"supported_endpoints":["/chat/completions"]}
		]}`)
	}))
	defer s.Close()
	p := &OpenAICompatible{ProviderName: "copilot", BaseURL: s.URL, Token: "test"}
	models, err := p.Models(context.Background())
	if err != nil || len(models) != 2 || models[0].ID != "enabled" || models[0].ContextTokens != 1000000 || !p.usesResponses("enabled") || !p.usesResponses("responses-only") {
		t.Fatalf("models=%#v err=%v", models, err)
	}
}

func TestCopilotResponsesUsesResponsesEndpointAndHeaders(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("X-Initiator") != "user" || r.Header.Get("Openai-Intent") != "conversation-edits" {
			t.Fatalf("request = %s headers=%#v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer s.Close()
	p := &OpenAICompatible{ProviderName: "copilot", BaseURL: s.URL, Token: "test", Headers: map[string]string{"Openai-Intent": "conversation-edits"}, responseModels: map[string]bool{"gpt-5.4": true}}
	events, errs := p.Stream(context.Background(), agent.Request{Model: "gpt-5.4", Messages: []agent.Message{{Role: "user", Content: "hi"}}})
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestCopilotSendsRequiredConversationHeaders(t *testing.T) {
	request := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantInitiator := []string{"user", "agent"}[request]
		request++
		if r.Header.Get("Openai-Intent") != "conversation-edits" || r.Header.Get("X-Initiator") != wantInitiator {
			t.Fatalf("headers = %#v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	p := &OpenAICompatible{ProviderName: "copilot", BaseURL: s.URL, Token: "test", Headers: map[string]string{"Openai-Intent": "conversation-edits"}}
	for _, messages := range [][]agent.Message{
		{{Role: "user", Content: "hi"}},
		{{Role: "user", Content: "hi"}, {Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)}}}, {Role: "tool", ToolCallID: "call-1", Content: "done"}},
	} {
		events, errs := p.Stream(context.Background(), agent.Request{Messages: messages})
		for range events {
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestIndividualCopilotModelsFallBackToEnabledPolicy(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"enabled","model_picker_enabled":false,"supported_endpoints":["/chat/completions"],"policy":{"state":"enabled"}}]}`)
	}))
	defer s.Close()
	p := &OpenAICompatible{ProviderName: "copilot", BaseURL: "https://api.individual.githubcopilot.com", Token: "test", Client: &http.Client{Transport: rewriteTransport{target: s.URL}}}
	models, err := p.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "enabled" {
		t.Fatalf("models=%#v error=%v", models, err)
	}
}

type rewriteTransport struct{ target string }

func (t rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target, _ := http.NewRequest(request.Method, t.target+request.URL.Path, nil)
	target.Header = request.Header
	return http.DefaultTransport.RoundTrip(target)
}

func TestResponsesSendsSelectedEffort(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ChatGPT-Account-Id") != "account-1" || r.Header.Get("Originator") != "atom" || r.Header.Get("OpenAI-Beta") != "responses=experimental" {
			t.Fatalf("headers = %#v", r.Header)
		}
		var body struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
			Include []string `json:"include"`
			Input   []struct {
				Type             string `json:"type"`
				EncryptedContent string `json:"encrypted_content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Reasoning.Effort != "high" || len(body.Include) != 1 || len(body.Input) != 1 || body.Input[0].EncryptedContent != "opaque" {
			t.Fatalf("body=%#v err=%v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer s.Close()
	p := &OpenAICompatible{BaseURL: s.URL, Token: "test", AccountID: "account-1", Responses: true}
	events, errs := p.Stream(context.Background(), agent.Request{Model: "test", ReasoningEffort: "high", Messages: []agent.Message{{Role: "assistant", ProviderItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}}})
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}
