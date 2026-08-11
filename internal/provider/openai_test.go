package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicobrch/atom/internal/agent"
)

func TestResponsesSSEStreamsTextAndTools(t *testing.T) {
	input := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"call_id\":\"call-1\",\"name\":\"read\"}}\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{}\"}\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n"
	out := make(chan agent.StreamEvent, 4)
	if err := parseResponsesSSE(bytes.NewBufferString(input), out); err != nil {
		t.Fatal(err)
	}
	close(out)
	var events []agent.StreamEvent
	for e := range out {
		events = append(events, e)
	}
	if len(events) != 4 || events[0].ToolCallDelta.Name != "read" || events[1].ToolCallDelta.Arguments != "{}" || events[2].TextDelta != "done" || events[3].InputTokens != 3 {
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

func TestOpenAIKeyReadsCodexCredentialWhenEnvIsUnset(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "auth.json"), []byte(`{"OPENAI_API_KEY":"from-codex"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_HOME", d)
	key, err := OpenAIKey()
	if err != nil || key != "from-codex" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestCopilotModelsUsesPickerAndChatCapabilities(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer test" {
			t.Fatalf("unexpected request: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"data":[
			{"id":"enabled","name":"Enabled","model_picker_enabled":true,"supported_endpoints":["/chat/completions"]},
			{"id":"messages-only","model_picker_enabled":true,"supported_endpoints":["/v1/messages"]},
			{"id":"hidden","model_picker_enabled":false,"supported_endpoints":["/chat/completions"]}
		]}`)
	}))
	defer s.Close()
	models, err := (&OpenAICompatible{ProviderName: "copilot", BaseURL: s.URL, Token: "test"}).Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "enabled" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
}

func TestResponsesSendsSelectedEffort(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Reasoning.Effort != "high" {
			t.Fatalf("effort=%q err=%v", body.Reasoning.Effort, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer s.Close()
	p := &OpenAICompatible{BaseURL: s.URL, Token: "test", Responses: true}
	events, errs := p.Stream(context.Background(), agent.Request{Model: "test", ReasoningEffort: "high"})
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}
