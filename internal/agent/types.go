package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
	// ID links provider activity, the diagnostic log, and a session error
	// record. It is generated locally and never sent as prompt content.
	ID              string
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolDefinition
	Temperature     *float64
	ReasoningEffort string
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

// DiagnosticEvent is intentionally metadata-only. In particular, it does not
// contain prompts, tool arguments/results, credentials, or raw HTTP bodies.
type DiagnosticEvent struct {
	Event            string         `json:"event"`
	RequestID        string         `json:"request_id"`
	Provider         string         `json:"provider"`
	Model            string         `json:"model"`
	ReasoningEffort  string         `json:"reasoning_effort,omitempty"`
	Turn             int            `json:"turn"`
	Attempt          int            `json:"attempt,omitempty"`
	MaxAttempts      int            `json:"max_attempts,omitempty"`
	MessageCount     int            `json:"message_count"`
	ToolCount        int            `json:"tool_count"`
	Duration         time.Duration  `json:"-"`
	DurationMS       int64          `json:"duration_ms,omitempty"`
	InputTokens      int            `json:"input_tokens,omitempty"`
	OutputTokens     int            `json:"output_tokens,omitempty"`
	PartialTextChars int            `json:"partial_text_chars,omitempty"`
	RetryDelayMS     int64          `json:"retry_delay_ms,omitempty"`
	Failure          *ProviderError `json:"failure,omitempty"`
}

// DiagnosticSink receives lifecycle metadata separately from the conversation
// transcript. A failure to write diagnostics must not alter an agent turn.
type DiagnosticSink interface {
	WriteDiagnostic(DiagnosticEvent) error
}

// ProviderError preserves a provider's actionable failure information without
// retaining raw requests or responses. It is used for both UI errors and
// structured diagnostics.
type ProviderError struct {
	Stage      string `json:"stage"`
	StatusCode int    `json:"status_code,omitempty"`
	RequestID  string `json:"provider_request_id,omitempty"`
	ResponseID string `json:"response_id,omitempty"`
	Code       string `json:"code,omitempty"`
	Type       string `json:"type,omitempty"`
	Param      string `json:"param,omitempty"`
	Message    string `json:"message"`
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	var details []string
	if e.StatusCode != 0 {
		details = append(details, fmt.Sprintf("HTTP %d", e.StatusCode))
	}
	if e.Type != "" {
		details = append(details, e.Type)
	}
	if e.Code != "" {
		details = append(details, e.Code)
	}
	if e.ResponseID != "" {
		details = append(details, "response "+e.ResponseID)
	}
	if e.RequestID != "" {
		details = append(details, "request "+e.RequestID)
	}
	prefix := "provider request failed"
	if e.Stage != "" {
		prefix = "provider " + e.Stage + " failed"
	}
	if len(details) > 0 {
		prefix += " (" + strings.Join(details, ", ") + ")"
	}
	if e.Message != "" {
		prefix += ": " + e.Message
	}
	return prefix
}
