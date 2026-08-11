package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type scriptedProvider struct{ calls int }

func (p *scriptedProvider) Name() string { return "scripted" }
func (p *scriptedProvider) Stream(_ context.Context, _ Request) (<-chan StreamEvent, <-chan error) {
	e := make(chan StreamEvent, 4)
	errs := make(chan error, 1)
	p.calls++
	if p.calls == 1 {
		e <- StreamEvent{ToolCallDelta: &ToolCallDelta{Index: 0, ID: "call-1", Name: "read", Arguments: `{"path":"note.txt"`}}
		e <- StreamEvent{ToolCallDelta: &ToolCallDelta{Index: 0, Arguments: `}`}}
		e <- StreamEvent{FinishReason: "tool_calls"}
	} else {
		e <- StreamEvent{TextDelta: "The file says hello."}
		e <- StreamEvent{FinishReason: "stop"}
	}
	close(e)
	close(errs)
	return e, errs
}

type memorySink struct{ kinds []string }

func (s *memorySink) WriteEvent(kind string, _ any) error {
	s.kinds = append(s.kinds, kind)
	return nil
}

type memoryDiagnostics struct{ events []DiagnosticEvent }

func (s *memoryDiagnostics) WriteDiagnostic(event DiagnosticEvent) error {
	s.events = append(s.events, event)
	return nil
}

func TestLoopExecutesToolThenContinues(t *testing.T) {
	tools := []Tool{readFixture{}}
	p := &scriptedProvider{}
	sink := &memorySink{}
	l := &Loop{Provider: p, Model: "test", Tools: tools, Sink: sink}
	if err := l.Prompt(context.Background(), "read note"); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 || len(l.Messages) != 4 || l.Messages[2].Role != "tool" || l.Messages[3].Content != "The file says hello." {
		t.Fatalf("unexpected loop state: %#v", l.Messages)
	}
	if len(sink.kinds) != 4 {
		t.Fatalf("events: %#v", sink.kinds)
	}
}

type failingProvider struct{}

func (failingProvider) Name() string { return "test-provider" }
func (failingProvider) Stream(_ context.Context, _ Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent)
	errs := make(chan error, 1)
	errs <- &ProviderError{Stage: "stream", StatusCode: 400, Code: "invalid_request", Message: "request is invalid"}
	close(events)
	close(errs)
	return events, errs
}

func TestLoopRecordsAndDiagnosesProviderFailure(t *testing.T) {
	sink := &memorySink{}
	diagnostics := &memoryDiagnostics{}
	loop := &Loop{Provider: failingProvider{}, Model: "test", Sink: sink, Diagnostics: diagnostics}
	err := loop.Prompt(context.Background(), "private prompt that must not enter diagnostics")
	if err == nil {
		t.Fatal("expected provider failure")
	}
	var failure *ProviderError
	if !errors.As(err, &failure) || failure.Code != "invalid_request" {
		t.Fatalf("error = %#v, want provider failure with invalid_request", err)
	}
	if len(sink.kinds) != 2 || sink.kinds[0] != "message" || sink.kinds[1] != "error" {
		t.Fatalf("session events = %#v, want message then error", sink.kinds)
	}
	if len(diagnostics.events) != 2 {
		t.Fatalf("diagnostic events = %#v", diagnostics.events)
	}
	started, failed := diagnostics.events[0], diagnostics.events[1]
	if started.Event != "request_started" || failed.Event != "request_failed" || started.RequestID == "" || started.RequestID != failed.RequestID {
		t.Fatalf("correlation events = %#v", diagnostics.events)
	}
	if !strings.Contains(err.Error(), "diagnostic "+started.RequestID) {
		t.Fatalf("error should include the diagnostic correlation ID: %v", err)
	}
	if failed.Failure == nil || failed.Failure.Code != "invalid_request" || failed.MessageCount != 1 {
		t.Fatalf("failure diagnostic = %#v", failed)
	}
}

type overloadThenSuccessProvider struct{ calls int }

func (p *overloadThenSuccessProvider) Name() string { return "test-provider" }
func (p *overloadThenSuccessProvider) Stream(_ context.Context, _ Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 1)
	errs := make(chan error, 1)
	p.calls++
	if p.calls == 1 {
		errs <- &ProviderError{Stage: "stream", StatusCode: 200, Type: "service_unavailable_error", Code: "server_is_overloaded", Message: "try again later"}
	} else {
		events <- StreamEvent{TextDelta: "recovered"}
	}
	close(events)
	close(errs)
	return events, errs
}

func TestLoopRetriesPreOutputOverload(t *testing.T) {
	p := &overloadThenSuccessProvider{}
	diagnostics := &memoryDiagnostics{}
	var waits []time.Duration
	loop := &Loop{
		Provider: p, Model: "test", Diagnostics: diagnostics,
		RetryWait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
	}
	if err := loop.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 || len(waits) != 1 || waits[0] != 2*time.Second {
		t.Fatalf("calls=%d waits=%v, want two calls and one 2s wait", p.calls, waits)
	}
	if got := loop.Messages[len(loop.Messages)-1].Content; got != "recovered" {
		t.Fatalf("assistant content = %q", got)
	}
	if len(diagnostics.events) != 4 {
		t.Fatalf("diagnostic events = %#v", diagnostics.events)
	}
	for i, want := range []string{"request_started", "request_retrying", "request_started", "request_succeeded"} {
		if got := diagnostics.events[i].Event; got != want {
			t.Fatalf("event %d = %q, want %q", i, got, want)
		}
	}
	first := diagnostics.events[0]
	if diagnostics.events[1].RetryDelayMS != 2000 || diagnostics.events[3].Attempt != 2 || first.RequestID == "" || diagnostics.events[3].RequestID != first.RequestID {
		t.Fatalf("retry diagnostics = %#v", diagnostics.events)
	}
}

type partialOverloadProvider struct{ calls int }

func (p *partialOverloadProvider) Name() string { return "test-provider" }
func (p *partialOverloadProvider) Stream(_ context.Context, _ Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 1)
	errs := make(chan error, 1)
	p.calls++
	events <- StreamEvent{TextDelta: "partial response"}
	errs <- &ProviderError{Stage: "stream", StatusCode: 200, Type: "service_unavailable_error", Code: "server_is_overloaded", Message: "try again later"}
	close(events)
	close(errs)
	return events, errs
}

func TestLoopDoesNotRetryAfterOutput(t *testing.T) {
	p := &partialOverloadProvider{}
	diagnostics := &memoryDiagnostics{}
	loop := &Loop{
		Provider: p, Model: "test", Diagnostics: diagnostics,
		RetryWait: func(_ context.Context, _ time.Duration) error {
			t.Fatal("retry wait called after output")
			return nil
		},
	}
	if err := loop.Prompt(context.Background(), "hello"); err == nil {
		t.Fatal("expected provider failure")
	}
	if p.calls != 1 || len(diagnostics.events) != 2 || diagnostics.events[1].Event != "request_failed" || diagnostics.events[1].PartialTextChars != len("partial response") {
		t.Fatalf("calls=%d diagnostics=%#v", p.calls, diagnostics.events)
	}
}

func TestCompactRetriesPreOutputOverload(t *testing.T) {
	p := &overloadThenSuccessProvider{}
	diagnostics := &memoryDiagnostics{}
	var waits []time.Duration
	loop := &Loop{
		Provider: p, Model: "test", Diagnostics: diagnostics,
		Messages: []Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}},
		RetryWait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
	}
	if err := loop.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 || len(waits) != 1 || waits[0] != 2*time.Second {
		t.Fatalf("calls=%d waits=%v, want two calls and one 2s wait", p.calls, waits)
	}
	if len(loop.Messages) != 1 || loop.Messages[0].Content != "Session handoff summary:\nrecovered" {
		t.Fatalf("compacted messages = %#v", loop.Messages)
	}
	if len(diagnostics.events) != 4 {
		t.Fatalf("diagnostic events = %#v", diagnostics.events)
	}
	for i, want := range []string{"compaction_started", "compaction_retrying", "compaction_started", "compaction_succeeded"} {
		if got := diagnostics.events[i].Event; got != want {
			t.Fatalf("event %d = %q, want %q", i, got, want)
		}
	}
	if diagnostics.events[3].Attempt != 2 {
		t.Fatalf("successful attempt = %d, want 2", diagnostics.events[3].Attempt)
	}
}

type readFixture struct{}

func (readFixture) Definition() ToolDefinition {
	return ToolDefinition{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (readFixture) Run(_ context.Context, args json.RawMessage) (string, error) {
	if string(args) != `{"path":"note.txt"}` {
		return "", nil
	}
	return "hello", nil
}
