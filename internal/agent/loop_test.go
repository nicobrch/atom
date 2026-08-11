package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

type errorSink struct{}

func (errorSink) WriteEvent(string, any) error { return errors.New("disk full") }

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

type cancellingProvider struct{}

func (cancellingProvider) Name() string { return "test-provider" }
func (cancellingProvider) Stream(ctx context.Context, _ Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		<-ctx.Done()
		errs <- &ProviderError{Stage: "transport", Message: ctx.Err().Error()}
	}()
	return events, errs
}

func TestLoopReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&Loop{Provider: cancellingProvider{}, Model: "test"}).Prompt(ctx, "hello")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
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

type compactionProvider struct{ systems []string }

func (p *compactionProvider) Name() string { return "test-provider" }
func (p *compactionProvider) Stream(_ context.Context, request Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 1)
	errs := make(chan error)
	p.systems = append(p.systems, request.System)
	if strings.Contains(request.System, "Summarize this coding session") {
		events <- StreamEvent{TextDelta: "old work summary"}
	} else {
		events <- StreamEvent{TextDelta: "new answer"}
	}
	close(events)
	close(errs)
	return events, errs
}

func TestPromptAutoCompactsBeforeAddingNewUserMessage(t *testing.T) {
	provider := &compactionProvider{}
	loop := &Loop{
		Provider: provider, Model: "test", ContextTokens: 20, AutoCompactAt: .5,
		Messages: []Message{{Role: "user", Content: strings.Repeat("x", 40)}, {Role: "assistant", Content: "done"}},
	}
	if err := loop.Prompt(context.Background(), "new request"); err != nil {
		t.Fatal(err)
	}
	if len(provider.systems) != 2 || !strings.Contains(provider.systems[0], "Summarize this coding session") {
		t.Fatalf("provider systems = %q", provider.systems)
	}
	if len(loop.Messages) != 3 || loop.Messages[0].Content != "Session handoff summary:\nold work summary" || loop.Messages[1].Content != "new request" {
		t.Fatalf("messages = %#v", loop.Messages)
	}
}

func TestCompactKeepsMessagesWhenProviderReturnsEmptySummary(t *testing.T) {
	messages := []Message{{Role: "user", Content: "keep this"}, {Role: "assistant", Content: "and this"}}
	loop := &Loop{Provider: &scriptedProvider{}, Model: "test", Messages: append([]Message(nil), messages...)}
	if err := loop.Compact(context.Background()); err == nil || !strings.Contains(err.Error(), "empty compaction summary") {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(loop.Messages, messages) {
		t.Fatalf("messages changed: %#v", loop.Messages)
	}
}

func TestCompactKeepsMessagesWhenSummaryCannotBePersisted(t *testing.T) {
	messages := []Message{{Role: "user", Content: "keep this"}, {Role: "assistant", Content: "and this"}}
	loop := &Loop{Provider: &compactionProvider{}, Model: "test", Sink: errorSink{}, Messages: append([]Message(nil), messages...)}
	if err := loop.Compact(context.Background()); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(loop.Messages, messages) {
		t.Fatalf("messages changed: %#v", loop.Messages)
	}
}

type readFixture struct{}

func (readFixture) Definition() ToolDefinition {
	return ToolDefinition{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}
}

type outputErrorTool struct{}

func (outputErrorTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "fail", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (outputErrorTool) Run(context.Context, json.RawMessage) (string, error) {
	return "useful stderr", errors.New("exit status 1")
}

type errorToolProvider struct{ calls int }

func (p *errorToolProvider) Name() string { return "scripted" }
func (p *errorToolProvider) Stream(context.Context, Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 1)
	errs := make(chan error)
	p.calls++
	if p.calls == 1 {
		events <- StreamEvent{ToolCallDelta: &ToolCallDelta{Index: 0, ID: "call-1", Name: "fail", Arguments: `{}`}}
	}
	close(events)
	close(errs)
	return events, errs
}

func TestLoopPreservesToolOutputOnFailure(t *testing.T) {
	loop := &Loop{Provider: &errorToolProvider{}, Model: "test", Tools: []Tool{outputErrorTool{}}}
	if err := loop.Prompt(context.Background(), "run it"); err != nil {
		t.Fatal(err)
	}
	if got := loop.Messages[2].Content; got != "useful stderr\nError: exit status 1" {
		t.Fatalf("tool result = %q", got)
	}
}

type truncatedToolProvider struct{ calls int }

func (p *truncatedToolProvider) Name() string { return "scripted" }
func (p *truncatedToolProvider) Stream(context.Context, Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 2)
	errs := make(chan error)
	p.calls++
	if p.calls == 1 {
		events <- StreamEvent{ToolCallDelta: &ToolCallDelta{Index: 0, ID: "call-1", Name: "track", Arguments: `{}`}}
		events <- StreamEvent{FinishReason: "length"}
	}
	close(events)
	close(errs)
	return events, errs
}

type trackingTool struct{ ran *bool }

func (t trackingTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "track", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (t trackingTool) Run(context.Context, json.RawMessage) (string, error) {
	*t.ran = true
	return "ran", nil
}

func TestLoopDoesNotExecuteTruncatedToolCall(t *testing.T) {
	ran := false
	loop := &Loop{Provider: &truncatedToolProvider{}, Model: "test", Tools: []Tool{trackingTool{ran: &ran}}}
	if err := loop.Prompt(context.Background(), "run it"); err != nil {
		t.Fatal(err)
	}
	if ran || !strings.Contains(loop.Messages[2].Content, "was not executed") {
		t.Fatalf("ran=%v result=%q", ran, loop.Messages[2].Content)
	}
}

type steeringProvider struct{ requests []Request }

func (p *steeringProvider) Name() string { return "scripted" }
func (p *steeringProvider) Stream(_ context.Context, request Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 1)
	errs := make(chan error)
	p.requests = append(p.requests, request)
	if len(p.requests) == 1 {
		events <- StreamEvent{TextDelta: "initial answer"}
	} else {
		events <- StreamEvent{TextDelta: "corrected answer"}
	}
	close(events)
	close(errs)
	return events, errs
}

type reasoningReplayProvider struct{ requests []Request }

func (p *reasoningReplayProvider) Name() string { return "scripted" }
func (p *reasoningReplayProvider) Stream(_ context.Context, request Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 2)
	errs := make(chan error)
	p.requests = append(p.requests, request)
	if len(p.requests) == 1 {
		events <- StreamEvent{ProviderItem: json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}
		events <- StreamEvent{ToolCallDelta: &ToolCallDelta{Index: 0, ID: "call-1", Name: "read", Arguments: `{"path":"note.txt"}`}}
	} else {
		events <- StreamEvent{TextDelta: "done"}
	}
	close(events)
	close(errs)
	return events, errs
}

func TestLoopPersistsProviderItemsAcrossToolTurn(t *testing.T) {
	provider := &reasoningReplayProvider{}
	loop := &Loop{Provider: provider, Model: "test", Tools: []Tool{readFixture{}}}
	if err := loop.Prompt(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 || len(provider.requests[1].Messages[1].ProviderItems) != 1 || len(loop.Messages[1].ProviderItems) != 1 {
		t.Fatalf("requests=%#v messages=%#v", provider.requests, loop.Messages)
	}
}

func TestLoopAdmitsSteeringBeforeStopping(t *testing.T) {
	provider := &steeringProvider{}
	steering := []string{"change direction"}
	loop := &Loop{Provider: provider, Model: "test", Steering: func() []string {
		messages := steering
		steering = nil
		return messages
	}}
	if err := loop.Prompt(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 || len(provider.requests[1].Messages) != 3 || provider.requests[1].Messages[2].Content != "change direction" {
		t.Fatalf("requests = %#v", provider.requests)
	}
	if len(loop.Messages) != 4 || loop.Messages[3].Content != "corrected answer" {
		t.Fatalf("messages = %#v", loop.Messages)
	}
}
func (readFixture) Run(_ context.Context, args json.RawMessage) (string, error) {
	if string(args) != `{"path":"note.txt"}` {
		return "", nil
	}
	return "hello", nil
}
