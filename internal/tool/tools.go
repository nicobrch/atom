package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicobrch/rivet/internal/agent"
)

type Registry struct {
	root    string
	timeout time.Duration
	tools   map[string]agent.Tool
}

func NewRegistry(root string, timeout time.Duration) *Registry {
	r := &Registry{root: root, timeout: timeout, tools: map[string]agent.Tool{}}
	for _, t := range []agent.Tool{readTool{root}, writeTool{root}, editTool{root}, bashTool{root, timeout}, grepTool{root}} {
		r.tools[t.Definition().Name] = t
	}
	return r
}
func (r *Registry) Definitions() []agent.ToolDefinition {
	out := make([]agent.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Definition())
	}
	return out
}
func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return t.Run(ctx, args)
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }
func resolve(root, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %q", path)
	}
	return path, nil
}
func clipped(b []byte) string {
	const max = 30_000
	if len(b) > max {
		return string(b[:max]) + fmt.Sprintf("\n... truncated (%d bytes total)", len(b))
	}
	return string(b)
}

type readTool struct{ root string }

func (t readTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "read", Description: "Read a UTF-8 text file from the workspace.", Parameters: schema(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}
}
func (t readTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	p, err := resolve(t.root, a.Path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return clipped(b), nil
}

type writeTool struct{ root string }

func (t writeTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "write", Description: "Create or replace a UTF-8 text file in the workspace.", Parameters: schema(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`)}
}
func (t writeTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	p, err := resolve(t.root, a.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(a.Content), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)", a.Path, len(a.Content)), nil
}

type editTool struct{ root string }

func (t editTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "edit", Description: "Replace exactly one occurrence of old_text with new_text in a workspace file. Read the file first.", Parameters: schema(`{"type":"object","properties":{"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"}},"required":["path","old_text","new_text"],"additionalProperties":false}`)}
}
func (t editTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Path string `json:"path"`
		Old  string `json:"old_text"`
		New  string `json:"new_text"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if a.Old == "" {
		return "", fmt.Errorf("old_text must not be empty")
	}
	p, err := resolve(t.root, a.Path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	n := strings.Count(string(b), a.Old)
	if n != 1 {
		return "", fmt.Errorf("old_text must match exactly once; found %d matches", n)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), a.Old, a.New, 1)), 0644); err != nil {
		return "", err
	}
	return "edited " + a.Path, nil
}

type bashTool struct {
	root    string
	timeout time.Duration
}

func (t bashTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "bash", Description: "Run a shell command in the workspace. Output is truncated; commands time out.", Parameters: schema(`{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600}},"required":["command"],"additionalProperties":false}`)}
}
func (t bashTool) Run(parent context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if a.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	d := t.timeout
	if a.Timeout > 0 {
		d = time.Duration(a.Timeout) * time.Second
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, d)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-lc", a.Command)
	cmd.Dir = t.root
	b, err := cmd.CombinedOutput()
	result := clipped(b)
	if ctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("command timed out after %s", d)
	}
	if err != nil {
		return result, fmt.Errorf("command failed: %w", err)
	}
	return result, nil
}

type grepTool struct{ root string }

func (t grepTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "grep", Description: "Search workspace text using ripgrep. Use a regular expression and optional glob.", Parameters: schema(`{"type":"object","properties":{"pattern":{"type":"string"},"glob":{"type":"string"}},"required":["pattern"],"additionalProperties":false}`)}
}
func (t grepTool) Run(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Pattern string `json:"pattern"`
		Glob    string `json:"glob"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	args := []string{"--line-number", "--no-heading", "--color", "never", a.Pattern}
	if a.Glob != "" {
		args = append(args, "--glob", a.Glob)
	}
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = t.root
	b, err := cmd.CombinedOutput()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return "no matches", nil
		}
		return clipped(b), fmt.Errorf("grep failed: %w", err)
	}
	return clipped(b), nil
}
