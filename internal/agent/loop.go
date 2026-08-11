package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Observer interface {
	Text(string)
	ToolStart(ToolCall)
	ToolEnd(ToolCall, string, error)
	Status(string)
}
type Loop struct {
	Provider                  Provider
	Model                     string
	Tools                     []Tool
	System                    string
	Messages                  []Message
	Sink                      EventSink
	Diagnostics               DiagnosticSink
	Observer                  Observer
	InputTokens, OutputTokens int
	ReasoningEffort           string
	ContextTokens             int
	AutoCompactAt             float64
	Steering                  func() []string
	// RetryWait is injectable for tests. Production uses a cancellable timer.
	RetryWait func(context.Context, time.Duration) error
}

const (
	maxStreamAttempts = 3
	initialRetryDelay = 2 * time.Second
)

func (l *Loop) Prompt(ctx context.Context, text string) error {
	if l.shouldAutoCompact() {
		if err := l.Compact(ctx); err != nil {
			return err
		}
	}
	l.Messages = append(l.Messages, Message{Role: "user", Content: text})
	if err := l.record("message", l.Messages[len(l.Messages)-1]); err != nil {
		return err
	}
	return l.run(ctx)
}

func (l *Loop) Clear() error {
	l.Messages = nil
	return l.record("clear", struct{}{})
}

func (l *Loop) run(ctx context.Context) error {
	lookup := map[string]Tool{}
	defs := make([]ToolDefinition, 0, len(l.Tools))
	for _, t := range l.Tools {
		lookup[t.Definition().Name] = t
		defs = append(defs, t.Definition())
	}
	for turn := 0; turn < 32; turn++ {
		if turn > 0 && l.shouldAutoCompact() {
			if err := l.Compact(ctx); err != nil {
				return err
			}
		}
		if l.Observer != nil {
			l.Observer.Status("thinking")
		}
		req := Request{ID: newRequestID(), Model: l.Model, System: l.System, Messages: l.Messages, Tools: defs, ReasoningEffort: l.ReasoningEffort}
		var text strings.Builder
		calls := map[int]*ToolCall{}
		var order []int
		var providerItems []json.RawMessage
		var finishReason string
		requestStarted := time.Now()
		totalInputTokens, totalOutputTokens := 0, 0
		for attempt := 1; attempt <= maxStreamAttempts; attempt++ {
			l.writeDiagnostic(DiagnosticEvent{
				Event: "request_started", RequestID: req.ID, Provider: l.Provider.Name(), Model: req.Model,
				ReasoningEffort: req.ReasoningEffort, Turn: turn + 1, Attempt: attempt, MaxAttempts: maxStreamAttempts, MessageCount: len(req.Messages), ToolCount: len(req.Tools),
			})
			attemptStarted := time.Now()
			events, errs := l.Provider.Stream(ctx, req)
			var attemptText strings.Builder
			attemptCalls := map[int]*ToolCall{}
			var attemptOrder []int
			var attemptProviderItems []json.RawMessage
			attemptInputTokens, attemptOutputTokens := 0, 0
			attemptFinishReason := ""
			for e := range events {
				l.InputTokens += e.InputTokens
				l.OutputTokens += e.OutputTokens
				attemptInputTokens += e.InputTokens
				attemptOutputTokens += e.OutputTokens
				if e.TextDelta != "" {
					attemptText.WriteString(e.TextDelta)
					if l.Observer != nil {
						l.Observer.Text(e.TextDelta)
					}
				}
				if d := e.ToolCallDelta; d != nil {
					c, ok := attemptCalls[d.Index]
					if !ok {
						c = &ToolCall{}
						attemptCalls[d.Index] = c
						attemptOrder = append(attemptOrder, d.Index)
					}
					if d.ID != "" {
						c.ID = d.ID
					}
					if d.Name != "" {
						c.Name = d.Name
					}
					if d.ResetArguments {
						c.Arguments = append(c.Arguments[:0], d.Arguments...)
					} else if d.Arguments != "" {
						c.Arguments = append(c.Arguments, d.Arguments...)
					}
				}
				if len(e.ProviderItem) > 0 {
					attemptProviderItems = append(attemptProviderItems, append(json.RawMessage(nil), e.ProviderItem...))
				}
				if e.FinishReason != "" {
					attemptFinishReason = e.FinishReason
				}
			}
			totalInputTokens += attemptInputTokens
			totalOutputTokens += attemptOutputTokens
			if err := <-errs; err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				failure := failureForDiagnostic(err)
				if retryable(ctx, failure) && attemptText.Len() == 0 && len(attemptCalls) == 0 && attempt < maxStreamAttempts {
					delay := retryDelay(attempt)
					l.writeDiagnostic(DiagnosticEvent{
						Event: "request_retrying", RequestID: req.ID, Provider: l.Provider.Name(), Model: req.Model,
						ReasoningEffort: req.ReasoningEffort, Turn: turn + 1, Attempt: attempt, MaxAttempts: maxStreamAttempts, MessageCount: len(req.Messages), ToolCount: len(req.Tools),
						Duration: time.Since(attemptStarted), DurationMS: time.Since(attemptStarted).Milliseconds(), InputTokens: attemptInputTokens, OutputTokens: attemptOutputTokens,
						RetryDelayMS: delay.Milliseconds(), Failure: failure,
					})
					if l.Observer != nil {
						l.Observer.Status(retryStatus(failure, attempt+1, maxStreamAttempts, delay))
					}
					if waitErr := l.waitForRetry(ctx, delay); waitErr == nil {
						continue
					} else {
						err = waitErr
						failure = failureForDiagnostic(err)
					}
				}
				diagnostic := DiagnosticEvent{
					Event: "request_failed", RequestID: req.ID, Provider: l.Provider.Name(), Model: req.Model,
					ReasoningEffort: req.ReasoningEffort, Turn: turn + 1, Attempt: attempt, MaxAttempts: maxStreamAttempts, MessageCount: len(req.Messages), ToolCount: len(req.Tools),
					Duration: time.Since(requestStarted), DurationMS: time.Since(requestStarted).Milliseconds(), InputTokens: totalInputTokens, OutputTokens: totalOutputTokens,
					PartialTextChars: utf8.RuneCountInString(attemptText.String()), Failure: failure,
				}
				l.writeDiagnostic(diagnostic)
				if recordErr := l.record("error", diagnostic); recordErr != nil {
					return fmt.Errorf("%w (also failed to record diagnostic %s: %v)", err, req.ID, recordErr)
				}
				return withDiagnosticID(err, req.ID)
			}
			text, calls, order, providerItems, finishReason = attemptText, attemptCalls, attemptOrder, attemptProviderItems, attemptFinishReason
			l.writeDiagnostic(DiagnosticEvent{
				Event: "request_succeeded", RequestID: req.ID, Provider: l.Provider.Name(), Model: req.Model,
				ReasoningEffort: req.ReasoningEffort, Turn: turn + 1, Attempt: attempt, MaxAttempts: maxStreamAttempts, MessageCount: len(req.Messages), ToolCount: len(req.Tools),
				Duration: time.Since(requestStarted), DurationMS: time.Since(requestStarted).Milliseconds(), InputTokens: totalInputTokens, OutputTokens: totalOutputTokens,
			})
			break
		}
		assistant := Message{Role: "assistant", Content: text.String(), ProviderItems: providerItems}
		for _, i := range order {
			assistant.ToolCalls = append(assistant.ToolCalls, *calls[i])
		}
		l.Messages = append(l.Messages, assistant)
		if err := l.record("message", assistant); err != nil {
			return err
		}
		for _, call := range assistant.ToolCalls {
			if l.Observer != nil {
				l.Observer.ToolStart(call)
			}
			tool, ok := lookup[call.Name]
			var output string
			var err error
			if finishReason == "length" || finishReason == "max_tokens" {
				err = fmt.Errorf("tool call was truncated by model output limit and was not executed")
			} else if !ok {
				err = fmt.Errorf("unknown tool %q", call.Name)
			} else {
				output, err = tool.Run(ctx, call.Arguments)
			}
			if err != nil {
				if output != "" {
					output += "\n"
				}
				output += "Error: " + err.Error()
			}
			if l.Observer != nil {
				l.Observer.ToolEnd(call, output, err)
			}
			result := Message{Role: "tool", ToolCallID: call.ID, Content: output}
			l.Messages = append(l.Messages, result)
			if e := l.record("message", result); e != nil {
				return e
			}
		}
		steering := l.takeSteering()
		for _, text := range steering {
			message := Message{Role: "user", Content: text}
			l.Messages = append(l.Messages, message)
			if err := l.record("message", message); err != nil {
				return err
			}
		}
		if len(assistant.ToolCalls) == 0 && len(steering) == 0 {
			if l.Observer != nil {
				l.Observer.Status("done")
			}
			return nil
		}
	}
	return fmt.Errorf("stopped after 32 tool turns")
}

func (l *Loop) writeDiagnostic(event DiagnosticEvent) {
	if l.Diagnostics != nil {
		// Diagnostics must never make an otherwise valid agent turn fail.
		_ = l.Diagnostics.WriteDiagnostic(event)
	}
}

func failureForDiagnostic(err error) *ProviderError {
	var failure *ProviderError
	if errors.As(err, &failure) {
		return failure
	}
	return &ProviderError{Stage: "stream", Message: "provider returned an unclassified error"}
}

func newRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err == nil {
		return "atom_" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("atom_%x", time.Now().UnixNano())
}

func withDiagnosticID(err error, requestID string) error {
	return fmt.Errorf("%w (diagnostic %s)", err, requestID)
}

func retryable(ctx context.Context, failure *ProviderError) bool {
	if ctx.Err() != nil || failure == nil {
		return false
	}
	if failure.StatusCode == 429 || failure.StatusCode >= 500 {
		return true
	}
	switch failure.Stage {
	case "transport":
		return true
	}
	switch failure.Type {
	case "service_unavailable_error", "server_error", "rate_limit_error":
		return true
	}
	switch failure.Code {
	case "server_is_overloaded", "rate_limit_exceeded", "internal_server_error", "timeout":
		return true
	}
	return false
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return initialRetryDelay * time.Duration(1<<(attempt-1))
}

func (l *Loop) waitForRetry(ctx context.Context, delay time.Duration) error {
	if l.RetryWait != nil {
		return l.RetryWait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryStatus(failure *ProviderError, nextAttempt, maxAttempts int, delay time.Duration) string {
	reason := failure.Code
	if reason == "" {
		reason = failure.Type
	}
	if reason == "" {
		reason = "transient provider error"
	}
	return fmt.Sprintf("retrying in %s after %s (attempt %d/%d)", delay, reason, nextAttempt, maxAttempts)
}

func (l *Loop) Compact(ctx context.Context) error {
	if len(l.Messages) < 2 {
		return nil
	}
	if l.Observer != nil {
		l.Observer.Status("compacting")
	}
	prompt := "Summarize this coding session for the next agent turn. Preserve the user's goal, decisions, changed files, tests run, errors, and remaining work. Be concise."
	req := Request{ID: newRequestID(), Model: l.Model, System: prompt, Messages: l.Messages}
	started := time.Now()
	var b strings.Builder
	inputTokens, outputTokens := 0, 0
	successfulAttempt := 0
	for attempt := 1; attempt <= maxStreamAttempts; attempt++ {
		l.writeDiagnostic(DiagnosticEvent{
			Event: "compaction_started", RequestID: req.ID, Provider: l.Provider.Name(), Model: req.Model,
			Attempt: attempt, MaxAttempts: maxStreamAttempts, MessageCount: len(req.Messages),
		})
		attemptStarted := time.Now()
		events, errs := l.Provider.Stream(ctx, req)
		var attemptText strings.Builder
		attemptInputTokens, attemptOutputTokens := 0, 0
		for e := range events {
			attemptText.WriteString(e.TextDelta)
			l.InputTokens += e.InputTokens
			l.OutputTokens += e.OutputTokens
			attemptInputTokens += e.InputTokens
			attemptOutputTokens += e.OutputTokens
		}
		inputTokens += attemptInputTokens
		outputTokens += attemptOutputTokens
		if err := <-errs; err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			failure := failureForDiagnostic(err)
			if retryable(ctx, failure) && attemptText.Len() == 0 && attempt < maxStreamAttempts {
				delay := retryDelay(attempt)
				l.writeDiagnostic(DiagnosticEvent{
					Event: "compaction_retrying", RequestID: req.ID, Provider: l.Provider.Name(), Model: req.Model,
					Attempt: attempt, MaxAttempts: maxStreamAttempts, MessageCount: len(req.Messages),
					Duration: time.Since(attemptStarted), DurationMS: time.Since(attemptStarted).Milliseconds(), InputTokens: attemptInputTokens, OutputTokens: attemptOutputTokens,
					RetryDelayMS: delay.Milliseconds(), Failure: failure,
				})
				if l.Observer != nil {
					l.Observer.Status(retryStatus(failure, attempt+1, maxStreamAttempts, delay))
				}
				if waitErr := l.waitForRetry(ctx, delay); waitErr == nil {
					continue
				} else {
					err = waitErr
					failure = failureForDiagnostic(err)
				}
			}
			diagnostic := DiagnosticEvent{
				Event: "compaction_failed", RequestID: req.ID, Provider: l.Provider.Name(), Model: req.Model,
				Attempt: attempt, MaxAttempts: maxStreamAttempts, MessageCount: len(req.Messages),
				Duration: time.Since(started), DurationMS: time.Since(started).Milliseconds(), InputTokens: inputTokens, OutputTokens: outputTokens,
				PartialTextChars: utf8.RuneCountInString(attemptText.String()), Failure: failure,
			}
			l.writeDiagnostic(diagnostic)
			if recordErr := l.record("error", diagnostic); recordErr != nil {
				return fmt.Errorf("%w (also failed to record diagnostic %s: %v)", err, req.ID, recordErr)
			}
			return withDiagnosticID(err, req.ID)
		}
		b = attemptText
		successfulAttempt = attempt
		break
	}
	if strings.TrimSpace(b.String()) == "" {
		err := fmt.Errorf("provider returned an empty compaction summary")
		diagnostic := DiagnosticEvent{
			Event: "compaction_failed", RequestID: req.ID, Provider: l.Provider.Name(), Model: req.Model,
			Attempt: successfulAttempt, MaxAttempts: maxStreamAttempts, MessageCount: len(req.Messages),
			Duration: time.Since(started), DurationMS: time.Since(started).Milliseconds(), InputTokens: inputTokens, OutputTokens: outputTokens,
			Failure: failureForDiagnostic(err),
		}
		l.writeDiagnostic(diagnostic)
		if recordErr := l.record("error", diagnostic); recordErr != nil {
			return fmt.Errorf("%w (also failed to record diagnostic %s: %v)", err, req.ID, recordErr)
		}
		return withDiagnosticID(err, req.ID)
	}
	l.writeDiagnostic(DiagnosticEvent{
		Event: "compaction_succeeded", RequestID: req.ID, Provider: l.Provider.Name(), Model: req.Model,
		Attempt: successfulAttempt, MaxAttempts: maxStreamAttempts, MessageCount: len(req.Messages),
		Duration: time.Since(started), DurationMS: time.Since(started).Milliseconds(), InputTokens: inputTokens, OutputTokens: outputTokens,
	})
	summary := Message{Role: "user", Content: "Session handoff summary:\n" + b.String()}
	if err := l.record("compaction", summary); err != nil {
		return err
	}
	l.Messages = []Message{summary}
	if l.Observer != nil {
		l.Observer.Status("compacted")
	}
	return nil
}

func (l *Loop) record(kind string, v any) error {
	if l.Sink == nil {
		return nil
	}
	return l.Sink.WriteEvent(kind, v)
}
func (l *Loop) ApproxTokens() int {
	n := 0
	for _, m := range l.Messages {
		n += (len(m.Content) + 3) / 4
		for _, c := range m.ToolCalls {
			n += (len(c.Name) + len(c.Arguments) + 3) / 4
		}
		for _, item := range m.ProviderItems {
			n += (len(item) + 3) / 4
		}
	}
	return n
}
func (l *Loop) shouldAutoCompact() bool {
	return len(l.Messages) > 1 && l.ContextTokens > 0 && l.AutoCompactAt > 0 && float64(l.ApproxTokens()) >= float64(l.ContextTokens)*l.AutoCompactAt
}
func (l *Loop) takeSteering() []string {
	if l.Steering == nil {
		return nil
	}
	return l.Steering()
}
func (l *Loop) MarshalMessages() ([]byte, error) { return json.Marshal(l.Messages) }
