package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	Observer                  Observer
	InputTokens, OutputTokens int
}

func (l *Loop) Prompt(ctx context.Context, text string) error {
	l.Messages = append(l.Messages, Message{Role: "user", Content: text})
	if err := l.record("message", l.Messages[len(l.Messages)-1]); err != nil {
		return err
	}
	return l.run(ctx)
}

func (l *Loop) run(ctx context.Context) error {
	lookup := map[string]Tool{}
	defs := make([]ToolDefinition, 0, len(l.Tools))
	for _, t := range l.Tools {
		lookup[t.Definition().Name] = t
		defs = append(defs, t.Definition())
	}
	for turn := 0; turn < 32; turn++ {
		if l.Observer != nil {
			l.Observer.Status("thinking")
		}
		events, errs := l.Provider.Stream(ctx, Request{Model: l.Model, System: l.System, Messages: l.Messages, Tools: defs})
		var text strings.Builder
		calls := map[int]*ToolCall{}
		var order []int
		for e := range events {
			l.InputTokens += e.InputTokens
			l.OutputTokens += e.OutputTokens
			if e.TextDelta != "" {
				text.WriteString(e.TextDelta)
				if l.Observer != nil {
					l.Observer.Text(e.TextDelta)
				}
			}
			if d := e.ToolCallDelta; d != nil {
				c, ok := calls[d.Index]
				if !ok {
					c = &ToolCall{}
					calls[d.Index] = c
					order = append(order, d.Index)
				}
				if d.ID != "" {
					c.ID = d.ID
				}
				if d.Name != "" {
					c.Name = d.Name
				}
				if d.Arguments != "" {
					c.Arguments = append(c.Arguments, d.Arguments...)
				}
			}
		}
		if err := <-errs; err != nil {
			return err
		}
		assistant := Message{Role: "assistant", Content: text.String()}
		for _, i := range order {
			assistant.ToolCalls = append(assistant.ToolCalls, *calls[i])
		}
		l.Messages = append(l.Messages, assistant)
		if err := l.record("message", assistant); err != nil {
			return err
		}
		if len(assistant.ToolCalls) == 0 {
			if l.Observer != nil {
				l.Observer.Status("done")
			}
			return nil
		}
		for _, call := range assistant.ToolCalls {
			if l.Observer != nil {
				l.Observer.ToolStart(call)
			}
			tool, ok := lookup[call.Name]
			var output string
			var err error
			if !ok {
				err = fmt.Errorf("unknown tool %q", call.Name)
			} else {
				output, err = tool.Run(ctx, call.Arguments)
			}
			if err != nil {
				output = "Error: " + err.Error()
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
	}
	return fmt.Errorf("stopped after 32 tool turns")
}

func (l *Loop) Compact(ctx context.Context) error {
	if len(l.Messages) < 2 {
		return nil
	}
	if l.Observer != nil {
		l.Observer.Status("compacting")
	}
	prompt := "Summarize this coding session for the next agent turn. Preserve the user's goal, decisions, changed files, tests run, errors, and remaining work. Be concise."
	events, errs := l.Provider.Stream(ctx, Request{Model: l.Model, System: prompt, Messages: l.Messages})
	var b strings.Builder
	for e := range events {
		b.WriteString(e.TextDelta)
		l.InputTokens += e.InputTokens
		l.OutputTokens += e.OutputTokens
	}
	if err := <-errs; err != nil {
		return err
	}
	summary := Message{Role: "user", Content: "Session handoff summary:\n" + b.String()}
	l.Messages = []Message{summary}
	if err := l.record("compaction", summary); err != nil {
		return err
	}
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
	}
	return n
}
func (l *Loop) MarshalMessages() ([]byte, error) { return json.Marshal(l.Messages) }
