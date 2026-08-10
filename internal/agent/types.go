package agent

import (
	"context"
	"encoding/json"
)

// Message is the provider-neutral conversation record. Tool calls use the
// OpenAI-compatible shape, which both bundled providers understand.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Request struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolDefinition
	Temperature *float64
}

// StreamEvent is intentionally small. Providers can report text and partial
// tool arguments as they arrive without teaching the loop their wire format.
type StreamEvent struct {
	TextDelta     string
	ToolCallDelta *ToolCallDelta
	FinishReason  string
	InputTokens   int
	OutputTokens  int
}

type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

type Provider interface {
	Name() string
	Stream(context.Context, Request) (<-chan StreamEvent, <-chan error)
}

type Tool interface {
	Definition() ToolDefinition
	Run(context.Context, json.RawMessage) (string, error)
}

type EventSink interface {
	WriteEvent(kind string, value any) error
}
