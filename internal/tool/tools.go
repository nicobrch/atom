package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nicobrch/atom/internal/agent"
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
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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
	root, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	original := path
	for {
		resolved, evalErr := filepath.EvalSymlinks(path)
		if evalErr == nil {
			rest, relErr := filepath.Rel(path, original)
			if relErr != nil {
				return "", relErr
			}
			path = filepath.Join(resolved, rest)
			break
		}
		if !os.IsNotExist(evalErr) {
			return "", evalErr
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", evalErr
		}
		path = parent
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %q", path)
	}
	return path, nil
}

const maxOutputBytes = 30_000

type boundedOutput struct {
	mu         sync.Mutex
	head, tail []byte
	total      int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += n
	half := maxOutputBytes / 2
	if len(b.head) < half {
		keep := min(half-len(b.head), len(p))
		b.head = append(b.head, p[:keep]...)
		p = p[keep:]
	}
	if len(p) >= half {
		b.tail = append(b.tail[:0], p[len(p)-half:]...)
		return n, nil
	}
	if overflow := len(b.tail) + len(p) - half; overflow > 0 {
		if overflow >= len(b.tail) {
			b.tail = b.tail[:0]
		} else {
			copy(b.tail, b.tail[overflow:])
			b.tail = b.tail[:len(b.tail)-overflow]
		}
	}
	b.tail = append(b.tail, p...)
	return n, nil
}

func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.total <= maxOutputBytes {
		return string(append(append([]byte(nil), b.head...), b.tail...))
	}
	return string(b.head) + fmt.Sprintf("\n... truncated (%d bytes total) ...\n", b.total) + string(b.tail)
}

type readTool struct{ root string }

func (t readTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "read", Description: "Read a UTF-8 text file from the workspace. Use offset and limit for large files.", Parameters: schema(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":2000}},"required":["path"],"additionalProperties":false}`)}
}
func (t readTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	p, err := resolve(t.root, a.Path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if a.Offset < 1 {
		a.Offset = 1
	}
	if a.Limit < 1 || a.Limit > 2000 {
		a.Limit = 2000
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	current, total := 1, 0
	var output boundedOutput
scan:
	for {
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if current >= a.Offset && current-a.Offset < a.Limit {
				_, _ = output.Write(fragment)
			}
			if fragment[len(fragment)-1] == '\n' {
				total = current
				current++
			}
		}
		switch readErr {
		case nil, bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(fragment) > 0 && fragment[len(fragment)-1] != '\n' {
				total = current
			}
		default:
			return "", readErr
		}
		break scan
	}
	if a.Offset > total {
		return fmt.Sprintf("offset %d is past end of file (%d lines)", a.Offset, total), nil
	}
	end := total
	if total-a.Offset+1 > a.Limit {
		end = a.Offset + a.Limit - 1
	}
	result := output.String()
	if a.Offset > 1 || end < total {
		result += fmt.Sprintf("\n... lines %d-%d of %d", a.Offset, end, total)
	}
	return result, nil
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
	if err := atomicWrite(p, []byte(a.Content)); err != nil {
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
	if err := atomicWrite(p, []byte(strings.Replace(string(b), a.Old, a.New, 1))); err != nil {
		return "", err
	}
	return "edited " + a.Path, nil
}

func atomicWrite(path string, contents []byte) error {
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".atom-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(contents); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
	configureProcessGroup(cmd)
	cmd.WaitDelay = time.Second
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	result := output.String()
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
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return "no matches", nil
		}
		return output.String(), fmt.Errorf("grep failed: %w", err)
	}
	return output.String(), nil
}
