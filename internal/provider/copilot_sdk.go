package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/embeddedcli"
	"github.com/nicobrch/atom/internal/agent"
)

// CopilotSDK delegates authentication and inference to GitHub's supported CLI.
type CopilotSDK struct {
	path  string
	tools []agent.Tool
}

func NewCopilotSDK(path string) *CopilotSDK       { return &CopilotSDK{path: path} }
func (p *CopilotSDK) Name() string                { return "copilot" }
func (p *CopilotSDK) SetTools(tools []agent.Tool) { p.tools = tools }

func CopilotCLIPath() (string, error) {
	if path := embeddedcli.Path(); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("Copilot CLI is not bundled: rebuild Atom with `make build`")
}

func (p *CopilotSDK) client() *copilot.Client {
	return copilot.NewClient(&copilot.ClientOptions{Connection: copilot.StdioConnection{Path: p.path}, LogLevel: "error"})
}

func (p *CopilotSDK) Models(ctx context.Context) ([]Model, error) {
	c := p.client()
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("start Copilot CLI: %w", err)
	}
	defer c.Stop()
	items, err := c.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Copilot models: %w", err)
	}
	models := make([]Model, 0, len(items))
	for _, item := range items {
		models = append(models, Model{ID: item.ID, Name: item.Name, Efforts: item.SupportedReasoningEfforts, DefaultEffort: item.DefaultReasoningEffort})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no selectable models available for copilot")
	}
	return models, nil
}

func (p *CopilotSDK) Stream(ctx context.Context, req agent.Request) (<-chan agent.StreamEvent, <-chan error) {
	events := make(chan agent.StreamEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		c := p.client()
		if err := c.Start(ctx); err != nil {
			errs <- providerFailure("start Copilot CLI", err)
			return
		}
		defer c.Stop()
		config := &copilot.SessionConfig{Model: req.Model, ReasoningEffort: req.ReasoningEffort, WorkingDirectory: "", Streaming: copilot.Bool(true), EnableConfigDiscovery: copilot.Bool(false), EnableSkills: copilot.Bool(false), SystemMessage: &copilot.SystemMessageConfig{Content: req.System}}
		config.Tools, config.AvailableTools = p.sdkTools(ctx, req.Tools)
		s, err := c.CreateSession(ctx, config)
		if err != nil {
			errs <- providerFailure("create Copilot session", err)
			return
		}
		defer s.Disconnect()
		done := make(chan error, 1)
		unsub := s.On(func(event copilot.SessionEvent) {
			switch d := event.Data.(type) {
			case *copilot.AssistantMessageDeltaData:
				events <- agent.StreamEvent{TextDelta: d.DeltaContent}
			case *copilot.SessionIdleData:
				select {
				case done <- nil:
				default:
				}
			case *copilot.SessionErrorData:
				select {
				case done <- fmt.Errorf("%s", d.Message):
				default:
				}
			}
		})
		defer unsub()
		if _, err := s.Send(ctx, copilot.MessageOptions{Prompt: transcript(req.Messages)}); err != nil {
			errs <- providerFailure("send Copilot prompt", err)
			return
		}
		select {
		case err := <-done:
			if err != nil {
				errs <- providerFailure("Copilot session", err)
			}
		case <-ctx.Done():
			errs <- providerFailure("Copilot session", ctx.Err())
		}
	}()
	return events, errs
}

func (p *CopilotSDK) sdkTools(ctx context.Context, defs []agent.ToolDefinition) ([]copilot.Tool, []string) {
	byName := map[string]agent.Tool{}
	for _, tool := range p.tools {
		byName[tool.Definition().Name] = tool
	}
	tools := make([]copilot.Tool, 0, len(defs))
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		params := map[string]any{}
		_ = json.Unmarshal(def.Parameters, &params)
		tool := byName[def.Name]
		tools = append(tools, copilot.Tool{Name: def.Name, Description: def.Description, Parameters: params, OverridesBuiltInTool: true, Handler: func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
			if tool == nil {
				return copilot.ToolResult{}, fmt.Errorf("unknown tool %q", inv.ToolName)
			}
			args, err := json.Marshal(inv.Arguments)
			if err != nil {
				return copilot.ToolResult{}, err
			}
			out, err := tool.Run(ctx, args)
			if err != nil {
				return copilot.ToolResult{ResultType: "failure", Error: err.Error()}, nil
			}
			return copilot.ToolResult{ResultType: "success", TextResultForLLM: out}, nil
		}})
		names = append(names, def.Name)
	}
	return tools, names
}

func transcript(messages []agent.Message) string {
	var b strings.Builder
	for _, m := range messages {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	return b.String()
}
