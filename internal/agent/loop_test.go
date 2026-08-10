package agent

import (
	"context"
	"encoding/json"
	"testing"
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
