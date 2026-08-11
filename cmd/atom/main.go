package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/nicobrch/atom/internal/agent"
	"github.com/nicobrch/atom/internal/config"
	"github.com/nicobrch/atom/internal/diagnostics"
	"github.com/nicobrch/atom/internal/instructions"
	"github.com/nicobrch/atom/internal/provider"
	"github.com/nicobrch/atom/internal/session"
	"github.com/nicobrch/atom/internal/tool"
)

const version = "0.3.1"
const headerPadding = 1
const composerPadding = 1

var atomLogo = []string{
	"       _                   ",
	"  __ _| |_ ___  _ __ ___  ",
	" / _` | __/ _ \\| '_ ` _ \\",
	"| (_| | || (_) | | | | | |",
	" \\__,_|\\__\\___/|_| |_| |_|",
}

var workingFrames = []string{
	"", ".", "..", "...",
}

type terminal struct {
	out           io.Writer
	assistantOpen bool
	toolLineOpen  bool
}

func (t *terminal) Text(s string) {
	if !t.assistantOpen {
		fmt.Fprint(t.out, "\033[1mAtom\033[0m  ")
		t.assistantOpen = true
	}
	fmt.Fprint(t.out, s)
}
func (t *terminal) ToolStart(c agent.ToolCall) {
	if t.assistantOpen || t.toolLineOpen {
		fmt.Fprintln(t.out)
	}
	fmt.Fprintf(t.out, "\033[2m└─\033[0m \033[36m%s\033[0m ", toolCallLabel(c))
	t.assistantOpen, t.toolLineOpen = false, true
}
func (t *terminal) ToolEnd(_ agent.ToolCall, output string, err error) {
	label := "done"
	if err != nil {
		label = "error"
	}
	one := strings.ReplaceAll(strings.TrimSpace(output), "\n", " ")
	if len(one) > 140 {
		one = one[:140] + "…"
	}
	fmt.Fprintf(t.out, "\033[2m%s: %s\033[0m\n", label, one)
	t.toolLineOpen = false
}
func (t *terminal) Status(s string) {
	if s == "thinking" {
		fmt.Fprint(t.out, "\n\033[2m• thinking\033[0m\n")
		t.assistantOpen, t.toolLineOpen = false, false
	} else if strings.HasPrefix(s, "retrying ") {
		fmt.Fprintf(t.out, "\033[2m• %s\033[0m\n", s)
	} else if s == "done" {
		if t.assistantOpen || t.toolLineOpen {
			fmt.Fprintln(t.out)
		}
		fmt.Fprintln(t.out, "\033[2m────────────────────────────────────────────────────────\033[0m")
	}
}

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "auth" || os.Args[1] == "login") {
		runLogin(os.Args[2:])
		return
	}
	var providerName, model, prompt, sessionPath string
	var printMode, jsonMode, listModels, showVersion bool
	flag.StringVar(&providerName, "provider", "", "provider: openai or copilot")
	flag.StringVar(&model, "model", "", "model identifier")
	flag.StringVar(&prompt, "p", "", "run one prompt and exit")
	flag.BoolVar(&printMode, "print", false, "plain output in interactive-disabled mode")
	flag.BoolVar(&jsonMode, "json", false, "JSONL events in interactive-disabled mode")
	flag.BoolVar(&listModels, "list-models", false, "list authenticated provider models and exit")
	flag.StringVar(&sessionPath, "session", "", "resume or append to a JSONL session")
	flag.BoolVar(&showVersion, "version", false, "print version")
	flag.Parse()
	if showVersion {
		fmt.Println("atom", version)
		return
	}
	if prompt == "" && flag.NArg() > 0 {
		prompt = strings.Join(flag.Args(), " ")
	}
	if printMode && jsonMode {
		fatal("--print and --json are mutually exclusive")
	}
	if (printMode || jsonMode) && prompt == "" {
		fatal("--print and --json require a prompt")
	}
	wd, err := os.Getwd()
	if err != nil {
		fatal(err.Error())
	}
	cfg, err := config.Load(wd)
	if err != nil {
		fatal(err.Error())
	}
	if err := config.MigrateLocalConfig(wd, cfg); err != nil {
		fatal(err.Error())
	}
	if providerName != "" {
		cfg.Provider = providerName
	}
	if model != "" {
		cfg.Model = model
	}
	p, err := selectProvider(cfg.Provider)
	if err != nil {
		if prompt != "" || listModels {
			fatal(err.Error())
		}
		p = unavailableProvider{err}
	}
	if listModels {
		models, modelErr := availableModels(context.Background(), p)
		if modelErr != nil {
			fatal(modelErr.Error())
		}
		for _, available := range models {
			fmt.Printf("%s\t%s\n", available.ID, available.Name)
		}
		return
	}
	if prompt != "" && cfg.Model != "" {
		if models, modelErr := availableModels(context.Background(), p); modelErr == nil {
			cfg.Effort = normalizedEffort(cfg.Model, cfg.Effort, models)
		}
	}
	atomHome, err := config.Home()
	if err != nil {
		fatal(err.Error())
	}
	instructionOptions := instructions.DefaultOptions()
	instructionOptions.Home = atomHome
	instructionOptions.ProjectDocFallbackNames = cfg.ProjectDocFallbackFilenames
	instructionOptions.ProjectDocMaxBytes = cfg.ProjectDocMaxBytes
	agentsText, agentFiles, err := instructions.LoadAgents(wd, instructionOptions)
	if err != nil {
		fatal(err.Error())
	}
	skills, err := instructions.DiscoverSkills(wd, instructionOptions)
	if err != nil {
		fatal(err.Error())
	}
	system := basePrompt(wd) + "\n\n" + agentsText + "\n\n" + instructions.SkillCatalog(skills)
	var store *session.JSONL
	if sessionPath != "" {
		if !filepath.IsAbs(sessionPath) {
			sessionPath = filepath.Join(wd, sessionPath)
		}
		store, err = session.Open(sessionPath)
	} else {
		store, err = session.New(wd)
	}
	if err != nil {
		fatal(err.Error())
	}
	logStore, err := diagnostics.New(wd)
	if err != nil {
		fatal(err.Error())
	}
	defer logStore.Close()
	var obs agent.Observer
	if jsonMode {
		obs = &jsonObserver{encoder: json.NewEncoder(os.Stdout)}
	} else if printMode {
		obs = &plain{out: os.Stdout}
	} else {
		obs = &terminal{out: os.Stdout}
	}
	agentTools := toolsAsInterface(tool.NewRegistry(wd, time.Duration(cfg.BashTimeoutSeconds)*time.Second))
	agentTools = append(agentTools, skillTool{skills: skills})
	sessionSink := &resumableSession{store: store}
	defer sessionSink.Close()
	loop := &agent.Loop{Provider: p, Model: cfg.Model, ReasoningEffort: cfg.Effort, AutoCompactAt: cfg.AutoCompactAt, Tools: agentTools, System: system, Sink: sessionSink, Diagnostics: logStore, Observer: obs}
	if sessionPath != "" {
		messages, e := session.LoadMessages(sessionPath)
		if e != nil {
			fatal(e.Error())
		}
		loop.Messages = messages
	}
	ctx := context.Background()
	if prompt != "" {
		if err := loop.Prompt(ctx, prompt); err != nil {
			if jsonMode {
				obs.(*jsonObserver).emit(map[string]any{"type": "error", "error": err.Error()})
			}
			fatal(err.Error())
		}
		if jsonMode {
			obs.(*jsonObserver).emit(map[string]any{"type": "result", "input_tokens": loop.InputTokens, "output_tokens": loop.OutputTokens, "session": store.Path()})
		}
		return
	}
	runApp(ctx, loop, skills, sessionSink, logStore.Path(), atomHome, cfg, wd, len(agentFiles))
}

func runLogin(args []string) {
	if len(args) < 1 || len(args) > 2 {
		fatal("usage: atom login <openai|copilot> [subscription|api]")
	}
	name := args[0]
	method := "subscription"
	if len(args) == 2 {
		method = args[1]
	}
	if method == "api" {
		fmt.Fprint(os.Stderr, "API key: ")
		var key string
		if term.IsTerminal(int(os.Stdin.Fd())) {
			value, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				fatal(err.Error())
			}
			key = string(value)
		} else {
			value, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && len(value) == 0 {
				fatal(err.Error())
			}
			key = value
		}
		if err := provider.SaveAPIKey(name, key); err != nil {
			fatal(err.Error())
		}
		fmt.Println("Credential saved for Atom.")
		return
	}
	if err := loginSubscription(name, method); err != nil {
		fatal(err.Error())
	}
}

func loginSubscription(name, method string) error {
	if method != "subscription" {
		return fmt.Errorf("login method must be subscription or api")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	defer cancel()
	switch name {
	case "openai":
		code, err := provider.StartOpenAILogin(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Open %s and enter code %s\n", "https://auth.openai.com/codex/device", code.UserCode)
		if err := provider.FinishOpenAILogin(ctx, code); err != nil {
			return err
		}
		fmt.Println("ChatGPT subscription available to Atom.")
	case "copilot", "github-copilot":
		code, err := provider.StartCopilotLogin(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Open %s and enter code %s\n", code.VerificationURI, code.UserCode)
		if err := provider.FinishCopilotLogin(ctx, code); err != nil {
			return err
		}
		fmt.Println("GitHub Copilot subscription available to Atom.")
	default:
		return fmt.Errorf("usage: atom login <openai|copilot> [subscription|api]")
	}
	return nil
}

func selectProvider(name string) (agent.Provider, error) {
	switch name {
	case "openai":
		return provider.OpenAIFromEnv()
	case "copilot", "github-copilot":
		return provider.CopilotFromEnv()
	default:
		return nil, fmt.Errorf("unknown provider %q (use openai or copilot)", name)
	}
}
func toolsAsInterface(r *tool.Registry) []agent.Tool { // registry deliberately owns implementations; expose through one thin adapter.
	defs := r.Definitions()
	out := make([]agent.Tool, 0, len(defs))
	for _, d := range defs {
		out = append(out, registryTool{r, d})
	}
	return out
}

type registryTool struct {
	r *tool.Registry
	d agent.ToolDefinition
}

// skillTool supports the Agent Skills progressive-disclosure pattern: the
// system prompt contains only metadata, and a model reads a matching SKILL.md
// only when it needs that workflow.
type skillTool struct{ skills []instructions.Skill }

func (t skillTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "load_skill",
		Description: "Load the full SKILL.md instructions for a listed skill before using that skill's workflow.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Exact skill name, or its catalog path when the name is duplicated."}},"required":["name"],"additionalProperties":false}`),
	}
}
func (t skillTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("parse load_skill arguments: %w", err)
	}
	if strings.TrimSpace(args.Name) == "" {
		return "", fmt.Errorf("name is required")
	}
	return instructions.LoadSkill(t.skills, args.Name)
}

type unavailableProvider struct{ err error }

func (p unavailableProvider) Name() string { return "not logged in" }
func (p unavailableProvider) Stream(context.Context, agent.Request) (<-chan agent.StreamEvent, <-chan error) {
	events, errs := make(chan agent.StreamEvent), make(chan error, 1)
	close(events)
	errs <- p.err
	close(errs)
	return events, errs
}

func (t registryTool) Definition() agent.ToolDefinition { return t.d }
func (t registryTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	return t.r.Run(ctx, t.d.Name, args)
}

type plain struct{ out io.Writer }

func (p *plain) Text(s string)                               { fmt.Fprint(p.out, s) }
func (p *plain) ToolStart(agent.ToolCall)                    {}
func (p *plain) ToolEnd(_ agent.ToolCall, _ string, _ error) {}
func (p *plain) Status(string)                               {}

type jsonObserver struct{ encoder *json.Encoder }

func (o *jsonObserver) emit(event map[string]any) { _ = o.encoder.Encode(event) }
func (o *jsonObserver) Text(delta string) {
	o.emit(map[string]any{"type": "text_delta", "delta": delta})
}
func (o *jsonObserver) ToolStart(call agent.ToolCall) {
	o.emit(map[string]any{"type": "tool_start", "call": call})
}
func (o *jsonObserver) ToolEnd(call agent.ToolCall, output string, err error) {
	event := map[string]any{"type": "tool_end", "call": call, "output": output}
	if err != nil {
		event["error"] = err.Error()
	}
	o.emit(event)
}
func (o *jsonObserver) Status(status string) {
	o.emit(map[string]any{"type": "status", "status": status})
}

var commands = []string{
	"/clear", "/clone", "/compact", "/exit", "/help", "/login", "/effort", "/logs", "/new",
	"/model", "/resume", "/session", "/skill", "/skills", "/update",
}

// resumableSession lets the UI atomically move the active conversation to a
// selected history file while keeping agent event persistence transparent.
type resumableSession struct{ store *session.JSONL }

func (s *resumableSession) WriteEvent(kind string, value any) error {
	return s.store.WriteEvent(kind, value)
}

func (s *resumableSession) Path() string { return s.store.Path() }

func (s *resumableSession) Close() error { return s.store.Close() }

func (s *resumableSession) Resume(path string) ([]agent.Message, error) {
	messages, err := session.LoadMessages(path)
	if err != nil {
		return nil, err
	}
	next, err := session.Open(path)
	if err != nil {
		return nil, err
	}
	if err := s.replace(next); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *resumableSession) New(workdir string) error {
	next, err := session.New(workdir)
	if err != nil {
		return err
	}
	return s.replace(next)
}

func (s *resumableSession) Clone(workdir string, messages []agent.Message) error {
	next, err := session.New(workdir)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if err := next.WriteEvent("message", message); err != nil {
			next.Close()
			_ = os.Remove(next.Path())
			return err
		}
	}
	return s.replace(next)
}

func (s *resumableSession) replace(next *session.JSONL) error {
	previous := s.store
	if err := previous.Close(); err != nil {
		next.Close()
		return err
	}
	if info, err := os.Stat(previous.Path()); err == nil && info.Size() == 0 {
		_ = os.Remove(previous.Path())
	}
	s.store = next
	return nil
}

func commandName(command string) string { return strings.Fields(command)[0] }

func commandMatches(line string) []string {
	if !strings.HasPrefix(line, "/") || strings.ContainsAny(line, " \t") {
		return nil
	}
	var out []string
	for _, command := range commands {
		if strings.HasPrefix(command, line) {
			out = append(out, command)
		}
	}
	return out
}

type modelLister interface {
	Models(context.Context) ([]provider.Model, error)
}

func availableModels(ctx context.Context, p agent.Provider) ([]provider.Model, error) {
	lister, ok := p.(modelLister)
	if !ok {
		return nil, fmt.Errorf("%s does not provide a model catalog", p.Name())
	}
	return lister.Models(ctx)
}

func normalizedEffort(modelID, effort string, models []provider.Model) string {
	for _, model := range models {
		if model.ID != modelID {
			continue
		}
		for _, supported := range model.Efforts {
			if supported == effort {
				return effort
			}
		}
		if effort != "" {
			return model.DefaultEffort
		}
		return ""
	}
	return ""
}

type uiText string
type uiStatus string
type uiTool struct{ name, output string }
type uiSteered int
type turnDone struct{ err error }
type modelsLoaded struct {
	models []provider.Model
	err    error
}
type loginDone struct {
	provider string
	err      error
}
type updateDone struct{ err error }
type thinkingTick struct{}
type toastDone struct{}

type mousePoint struct{ x, y int }

type uiObserver struct{ events chan<- tea.Msg }

func (o uiObserver) Text(s string)              { o.events <- uiText(s) }
func (o uiObserver) Status(s string)            { o.events <- uiStatus(s) }
func (o uiObserver) ToolStart(c agent.ToolCall) { o.events <- uiTool{name: toolCallLabel(c)} }
func (o uiObserver) ToolEnd(_ agent.ToolCall, out string, err error) {
	if err != nil && strings.TrimSpace(out) == "" {
		out = "error: " + err.Error()
	}
	one := strings.ReplaceAll(strings.TrimSpace(out), "\n", " ")
	if len(one) > 140 {
		one = one[:140] + "…"
	}
	o.events <- uiTool{output: one}
}

func toolCallLabel(call agent.ToolCall) string {
	var args struct {
		Path    string `json:"path"`
		Command string `json:"command"`
		Pattern string `json:"pattern"`
		Name    string `json:"name"`
	}
	_ = json.Unmarshal(call.Arguments, &args)
	detail := map[string]string{
		"bash": args.Command, "read": args.Path, "write": args.Path,
		"edit": args.Path, "grep": args.Pattern, "load_skill": args.Name,
	}[call.Name]
	detail = strings.Join(strings.Fields(detail), " ")
	if runes := []rune(detail); len(runes) > 140 {
		detail = string(runes[:140]) + "…"
	}
	if detail == "" {
		return call.Name
	}
	return call.Name + "  " + detail
}

type appModel struct {
	ctx            context.Context
	loop           *agent.Loop
	skills         []instructions.Skill
	session        *resumableSession
	sessionHistory []session.HistoryEntry
	logPath        string
	atomHome       string
	cfg            config.Config
	wd             string
	events         chan tea.Msg
	input          string
	inputCursor    int
	inputAnchor    int
	draggingInput  bool
	transcript     []string
	queue          []string
	followUps      []string
	busy           bool
	width          int
	height         int
	selected       int
	menu           []string
	menuTitle      string
	menuKind       string
	loginProvider  string
	models         []provider.Model
	assistantOpen  bool
	scroll         int
	thinking       bool
	thinkingFrame  int
	selecting      bool
	selectionFrom  mousePoint
	selectionTo    mousePoint
	toast          string
	turnCancel     context.CancelFunc
	steering       chan string
	history        []string
	historyPos     int
	historyDraft   string
}

func runApp(ctx context.Context, loop *agent.Loop, skills []instructions.Skill, sessionSink *resumableSession, logPath, atomHome string, cfg config.Config, wd string, agentFiles int) {
	events := make(chan tea.Msg, 64)
	steering := make(chan string, 64)
	loop.Steering = func() []string {
		var messages []string
		for {
			select {
			case message := <-steering:
				messages = append(messages, message)
			default:
				if len(messages) > 0 {
					events <- uiSteered(len(messages))
				}
				return messages
			}
		}
	}
	loop.Observer = uiObserver{events: events}
	history := promptHistory(loop.Messages)
	m := appModel{ctx: ctx, loop: loop, skills: skills, session: sessionSink, logPath: logPath, atomHome: atomHome, cfg: cfg, wd: wd, events: events, steering: steering, history: history, historyPos: len(history), width: 100, height: 30}
	if agentFiles > 0 {
		m.transcript = append(m.transcript, fmt.Sprintf("Loaded %d AGENTS.md file(s)", agentFiles))
	}
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "atom:", err)
	}
}

func (m appModel) Init() tea.Cmd {
	// Fetch the authenticated provider's catalog up front so the footer uses the
	// selected model's actual context window, without opening the model picker.
	if _, unavailable := m.loop.Provider.(unavailableProvider); !unavailable {
		return tea.Batch(waitEvent(m.events), m.loadModels())
	}
	return waitEvent(m.events)
}
func waitEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}
func (m *appModel) startTurn(text string) tea.Cmd {
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.turnCancel = cancel
	return func() tea.Msg {
		err := m.loop.Prompt(ctx, text)
		return turnDone{err: err}
	}
}
func (m *appModel) compact() tea.Cmd {
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.turnCancel = cancel
	return func() tea.Msg { return turnDone{err: m.loop.Compact(ctx)} }
}
func (m appModel) update() tea.Cmd {
	return func() tea.Msg { return updateDone{err: updateAtom(m.atomHome, runUpdateCommand)} }
}

type updateCommand func(dir, name string, args ...string) error

func updateAtom(dir string, run updateCommand) error {
	if dir == "" {
		return fmt.Errorf("Atom home is not configured")
	}
	source := filepath.Join(dir, "source")
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		source = dir // Support installations created before the source subdirectory layout.
	}
	if err := run(source, "git", "pull", "--ff-only"); err != nil {
		return fmt.Errorf("pull update: %w", err)
	}
	if err := removeLegacyCopilotBundle(source); err != nil {
		return fmt.Errorf("clean legacy Copilot bundle: %w", err)
	}
	target := filepath.Join(dir, "atom")
	if runtime.GOOS == "windows" {
		target += ".exe"
	}
	if err := run(source, "go", "build", "-o", target, "./cmd/atom"); err != nil {
		return fmt.Errorf("build update: %w", err)
	}
	return nil
}

func removeLegacyCopilotBundle(source string) error {
	matches, err := filepath.Glob(filepath.Join(source, "cmd", "atom", "zcopilot*"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func runUpdateCommand(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if detail := strings.TrimSpace(string(output)); detail != "" {
		return fmt.Errorf("%s", detail)
	}
	return err
}
func (m appModel) loadModels() tea.Cmd {
	return func() tea.Msg {
		models, err := availableModels(m.ctx, m.loop.Provider)
		return modelsLoaded{models: models, err: err}
	}
}
func (m *appModel) add(line string) {
	m.transcript = append(m.transcript, line)
}
func (m *appModel) clampScroll() {
	max := m.transcriptHeight()
	limit := len(renderedTranscript(m.transcript, m.width-2)) - max
	if limit < 0 {
		limit = 0
	}
	if m.scroll > limit {
		m.scroll = limit
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m appModel) transcriptHeight() int {
	max := m.height - len(atomLogo) - headerPadding - composerPadding - 5 - (m.composerRows() - 1)
	if m.thinking {
		max--
	}
	if m.toast != "" {
		max--
	}
	max -= m.menuRows()
	if max < 3 {
		return 3
	}
	return max
}

func (m appModel) menuRows() int {
	if m.menuKind != "" && m.menuKind != "loading" {
		return 2
	}
	if len(commandMatches(m.input)) > 0 {
		return 1
	}
	return 0
}

func (m appModel) visibleTranscript() ([]transcriptLine, int) {
	lines := renderedTranscript(m.transcript, m.width-2)
	max := m.transcriptHeight()
	scroll := m.scroll
	if limit := len(lines) - max; scroll > limit {
		scroll = limit
	}
	if scroll < 0 {
		scroll = 0
	}
	start := len(lines) - max - scroll
	if start < 0 {
		start = 0
	}
	end := start + max
	if end > len(lines) {
		end = len(lines)
	}
	return lines[start:end], start
}

func clipboardCommand(goos string, lookPath func(string) (string, error)) ([]string, error) {
	if goos == "darwin" {
		return []string{"pbcopy"}, nil
	}
	if goos == "windows" {
		return []string{"clip.exe"}, nil
	}
	if goos == "linux" {
		for _, command := range [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		} {
			if _, err := lookPath(command[0]); err == nil {
				return command, nil
			}
		}
		return nil, fmt.Errorf("no clipboard utility found; install wl-clipboard (Wayland) or xclip/xsel (X11)")
	}
	return nil, fmt.Errorf("clipboard is unsupported on %s", goos)
}

func copyToClipboard(text string) error {
	command, err := clipboardCommand(runtime.GOOS, exec.LookPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func (m appModel) selectionText() string {
	if !m.selecting {
		return ""
	}
	a, z := m.selectionFrom, m.selectionTo
	if z.y < a.y || (z.y == a.y && z.x < a.x) {
		a, z = z, a
	}
	lines, _ := m.visibleTranscript()
	inputTop := m.inputScreenTop()
	inputLines, _ := m.inputViewport()
	var out []string
	for y := a.y; y <= z.y; y++ {
		line, offset, ok := "", 0, false
		headerRows := len(atomLogo) + headerPadding
		if y >= headerRows && y < headerRows+len(lines) {
			line, ok = lines[y-headerRows].text, true
		} else if y >= inputTop && y < inputTop+len(inputLines) {
			inputLine := inputLines[y-inputTop]
			line, offset, ok = inputLine.text, 4, true
		}
		if !ok {
			continue
		}
		from, to := 0, len([]rune(line))
		if y == a.y {
			from = a.x - offset
		}
		if y == z.y {
			to = z.x - offset
		}
		if from < 0 {
			from = 0
		}
		if to < 0 {
			to = 0
		}
		r := []rune(line)
		if from > len(r) {
			from = len(r)
		}
		if to > len(r) {
			to = len(r)
		}
		if to > from {
			out = append(out, string(r[from:to]))
		}
	}
	return strings.Join(out, "\n")
}

// inputWidth is deliberately one cell smaller than the available space. That
// leaves room for the caret even when the input fills the prompt.
func (m appModel) inputWidth() int {
	width := m.width - 8
	if width < 1 {
		return 1
	}
	return width
}

const maxComposerRows = 6

type inputLine struct {
	text       string
	start, end int
}

func (m appModel) inputLines() []inputLine {
	runes := []rune(m.input)
	width := m.inputWidth()
	if len(runes) == 0 {
		return []inputLine{{}}
	}
	lines := make([]inputLine, 0, len(runes)/width+1)
	for start := 0; start < len(runes); {
		end := start + width
		if end > len(runes) {
			end = len(runes)
		}
		for i := start; i < end; i++ {
			if runes[i] == '\n' {
				end = i
				break
			}
		}
		lines = append(lines, inputLine{text: string(runes[start:end]), start: start, end: end})
		if end < len(runes) && runes[end] == '\n' {
			start = end + 1
			if start == len(runes) {
				lines = append(lines, inputLine{start: start, end: start})
			}
		} else {
			start = end
		}
	}
	return lines
}

func (m appModel) inputCursorLine(lines []inputLine) int {
	for i, line := range lines {
		if m.inputCursor >= line.start && m.inputCursor <= line.end {
			return i
		}
	}
	return len(lines) - 1
}

func (m appModel) inputViewport() ([]inputLine, int) {
	lines := m.inputLines()
	cursorLine := m.inputCursorLine(lines)
	start := cursorLine - maxComposerRows + 1
	if start < 0 {
		start = 0
	}
	end := start + maxComposerRows
	if end > len(lines) {
		end = len(lines)
		start = end - maxComposerRows
		if start < 0 {
			start = 0
		}
	}
	return lines[start:end], start
}

func (m appModel) composerRows() int {
	lines, _ := m.inputViewport()
	return len(lines)
}

func (m appModel) inputScreenTop() int {
	top := len(atomLogo) + headerPadding + m.transcriptHeight() + composerPadding
	if m.thinking {
		top++
	}
	if m.toast != "" {
		top++
	}
	top += m.menuRows()
	return top + 1 // top border
}

func (m appModel) inputSelection() (int, int) {
	a, z := m.inputAnchor, m.inputCursor
	if a > z {
		a, z = z, a
	}
	return a, z
}

func (m appModel) hasInputSelection() bool {
	return m.inputAnchor != m.inputCursor
}

func (m *appModel) clampInputCursor() {
	length := len([]rune(m.input))
	if m.inputCursor < 0 {
		m.inputCursor = 0
	}
	if m.inputCursor > length {
		m.inputCursor = length
	}
	if m.inputAnchor < 0 {
		m.inputAnchor = 0
	}
	if m.inputAnchor > length {
		m.inputAnchor = length
	}
}

func (m *appModel) moveInputCursor(to int, extend bool) {
	m.clampInputCursor()
	if !extend && m.hasInputSelection() {
		from, until := m.inputSelection()
		if to < m.inputCursor {
			to = from
		} else {
			to = until
		}
	}
	m.inputCursor = to
	if !extend {
		m.inputAnchor = to
	}
	m.clampInputCursor()
}

func previousWordBoundary(runes []rune, cursor int) int {
	for cursor > 0 && unicode.IsSpace(runes[cursor-1]) {
		cursor--
	}
	for cursor > 0 && !unicode.IsSpace(runes[cursor-1]) {
		cursor--
	}
	return cursor
}

func nextWordBoundary(runes []rune, cursor int) int {
	for cursor < len(runes) && unicode.IsSpace(runes[cursor]) {
		cursor++
	}
	for cursor < len(runes) && !unicode.IsSpace(runes[cursor]) {
		cursor++
	}
	return cursor
}

func (m *appModel) deleteInputRange(from, until int) {
	runes := []rune(m.input)
	if from < 0 {
		from = 0
	}
	if until > len(runes) {
		until = len(runes)
	}
	if until < from {
		from, until = until, from
	}
	m.input = string(append(runes[:from], runes[until:]...))
	m.inputCursor, m.inputAnchor = from, from
	m.clampInputCursor()
}

func (m *appModel) deleteInputBackward(word bool) {
	if m.hasInputSelection() {
		from, until := m.inputSelection()
		m.deleteInputRange(from, until)
		return
	}
	from := m.inputCursor - 1
	if word {
		from = previousWordBoundary([]rune(m.input), m.inputCursor)
	}
	if from >= 0 {
		m.deleteInputRange(from, m.inputCursor)
	}
}

func (m *appModel) deleteInputForward() {
	if m.hasInputSelection() {
		from, until := m.inputSelection()
		m.deleteInputRange(from, until)
		return
	}
	runes := []rune(m.input)
	if m.inputCursor < len(runes) {
		m.deleteInputRange(m.inputCursor, m.inputCursor+1)
	}
}

func (m *appModel) insertInput(text []rune) {
	if m.hasInputSelection() {
		from, until := m.inputSelection()
		m.deleteInputRange(from, until)
	}
	runes := []rune(m.input)
	at := m.inputCursor
	runes = append(runes[:at], append(text, runes[at:]...)...)
	m.input = string(runes)
	m.inputCursor += len(text)
	m.inputAnchor = m.inputCursor
	m.clampInputCursor()
}

func (m appModel) inputIndexAt(x, y int) int {
	lines, _ := m.inputViewport()
	row := y - m.inputScreenTop()
	if row < 0 {
		row = 0
	}
	if row >= len(lines) {
		row = len(lines) - 1
	}
	line := lines[row]
	index := line.start + x - 4
	if index < line.start {
		return line.start
	}
	if index > line.end {
		return line.end
	}
	return index
}
func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampInputCursor()
	case uiText:
		m.thinking = false
		if !m.assistantOpen {
			if len(m.transcript) > 0 && m.transcript[len(m.transcript)-1] != "" {
				m.add("")
			}
			m.add(string(msg))
			m.assistantOpen = true
		} else {
			m.transcript[len(m.transcript)-1] += string(msg)
		}
		return m, waitEvent(m.events)
	case uiStatus:
		if msg == "thinking" {
			m.thinking = true
			m.thinkingFrame = 0
			m.assistantOpen = false
			return m, tea.Batch(waitEvent(m.events), nextThinkingTick())
		}
		if strings.HasPrefix(string(msg), "retrying ") {
			m.add("· " + string(msg))
			return m, waitEvent(m.events)
		}
		return m, waitEvent(m.events)
	case uiTool:
		m.thinking = false
		if msg.name != "" {
			m.add("└─ " + msg.name)
		} else if msg.output != "" && len(m.transcript) > 0 {
			m.transcript[len(m.transcript)-1] += "  " + msg.output
		}
		return m, waitEvent(m.events)
	case uiSteered:
		count := int(msg)
		if count > len(m.queue) {
			count = len(m.queue)
		}
		m.queue = m.queue[count:]
		return m, waitEvent(m.events)
	case thinkingTick:
		if m.thinking {
			m.thinkingFrame = (m.thinkingFrame + 1) % len(workingFrames)
			return m, nextThinkingTick()
		}
		return m, nil
	case toastDone:
		m.toast = ""
		return m, nil
	case turnDone:
		if m.turnCancel != nil {
			m.turnCancel()
			m.turnCancel = nil
		}
		m.busy = false
		m.thinking = false
		if errors.Is(msg.err, context.Canceled) {
			m.add("turn cancelled")
		} else if msg.err != nil {
			m.add("error: " + msg.err.Error())
		}
		if len(m.queue) > 0 {
			if m.steering != nil {
				for {
					select {
					case <-m.steering:
					default:
						goto steeringDrained
					}
				}
			}
		steeringDrained:
			next := m.queue[0]
			m.queue = m.queue[1:]
			m.busy = true
			cmd := (&m).startTurn(next)
			return m, cmd
		}
		if len(m.followUps) > 0 {
			next := m.followUps[0]
			m.followUps = m.followUps[1:]
			m.busy = true
			cmd := (&m).startTurn(next)
			return m, cmd
		}
		return m, nil
	case updateDone:
		m.busy = false
		if msg.err != nil {
			m.add("update failed: " + msg.err.Error())
		} else {
			m.add("Atom updated. Restart Atom to use the new version.")
		}
		return m, nil
	case modelsLoaded:
		if msg.err != nil {
			if m.menuKind != "" {
				m.add("error: " + msg.err.Error())
			}
			return m, nil
		}
		m.models = msg.models
		m.loop.ReasoningEffort = normalizedEffort(m.loop.Model, m.loop.ReasoningEffort, m.models)
		m.cfg.Effort = m.loop.ReasoningEffort
		for _, model := range m.models {
			if model.ID == m.loop.Model {
				m.loop.ContextTokens = model.ContextTokens
				break
			}
		}
		if m.menuKind == "effort-loading" {
			for _, model := range m.models {
				if model.ID == m.loop.Model {
					m.menu = model.Efforts
					break
				}
			}
			if len(m.menu) == 0 {
				m.add("selected model does not expose effort controls")
				m.menuKind = ""
				return m, nil
			}
			m.menuTitle, m.menuKind, m.selected = "Select effort", "effort", 0
			return m, nil
		}
		if m.menuKind == "loading" {
			m.menuTitle, m.menuKind, m.selected = "Select model", "model", 0
			m.menu = make([]string, len(m.models))
			for i, model := range m.models {
				m.menu[i] = model.ID
			}
		}
		return m, nil
	case loginDone:
		if msg.err != nil {
			m.add("login failed: " + msg.err.Error())
			return m, nil
		}
		p, err := selectProvider(msg.provider)
		if err != nil {
			m.add("login failed: " + err.Error())
			return m, nil
		}
		m.loop.Provider, m.cfg.Provider = p, msg.provider
		if err := config.SaveGlobal(m.cfg); err != nil {
			m.add("error saving provider: " + err.Error())
		}
		// A provider's model set is credential-specific. Always make the user
		// choose one after login instead of inheriting a guessed default model.
		m.loop.Model, m.cfg.Model = "", ""
		m.add("logged in: " + authSource(p))
		m.menuKind = "loading"
		return m, m.loadModels()
	case tea.KeyMsg:
		if m.menuKind != "" {
			return m.menuKey(msg)
		}
		return m.inputKey(msg)
	case tea.MouseMsg:
		if m.menuKind == "resume" || m.menuKind == "model" {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.moveMenuSelection(-1)
			case tea.MouseButtonWheelDown:
				m.moveMenuSelection(1)
			}
			return m, nil
		}
		switch {
		case msg.Button == tea.MouseButtonWheelUp:
			m.scroll += 3
		case msg.Button == tea.MouseButtonWheelDown:
			m.scroll -= 3
			if m.scroll < 0 {
				m.scroll = 0
			}
		case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && msg.Y >= m.inputScreenTop() && msg.Y < m.inputScreenTop()+m.composerRows() && msg.X >= 4:
			m.selecting = false
			m.draggingInput = true
			m.inputCursor = m.inputIndexAt(msg.X, msg.Y)
			if !msg.Shift {
				m.inputAnchor = m.inputCursor
			}
			m.clampInputCursor()
			return m, nil
		case m.draggingInput && (msg.Action == tea.MouseActionMotion || msg.Action == tea.MouseActionRelease):
			m.inputCursor = m.inputIndexAt(msg.X, msg.Y)
			m.clampInputCursor()
			if msg.Action == tea.MouseActionRelease {
				m.draggingInput = false
			}
			return m, nil
		case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
			m.draggingInput = false
			m.inputAnchor = m.inputCursor
			m.selecting = true
			m.selectionFrom = mousePoint{msg.X, msg.Y}
			m.selectionTo = m.selectionFrom
		case m.selecting && msg.Action == tea.MouseActionMotion:
			m.selectionTo = mousePoint{msg.X, msg.Y}
		case m.selecting && msg.Action == tea.MouseActionRelease:
			m.selectionTo = mousePoint{msg.X, msg.Y}
			text := m.selectionText()
			m.selecting = false
			if text != "" {
				if err := copyToClipboard(text); err != nil {
					m.toast = "copy failed: " + err.Error()
				} else {
					m.toast = "Copied to clipboard"
				}
				return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return toastDone{} })
			}
		}
		m.clampScroll()
	}
	return m, nil
}

func (m appModel) menuKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.menu) == 0 {
		if k.String() == "esc" {
			m.menuKind = ""
		}
		return m, nil
	}
	if m.menuKind == "resume" || m.menuKind == "model" {
		switch k.String() {
		case "esc":
			m.menuKind, m.menu, m.sessionHistory = "", nil, nil
			return m, nil
		case "up", "k":
			m.moveMenuSelection(-1)
			return m, nil
		case "down", "j":
			m.moveMenuSelection(1)
			return m, nil
		case "pgup":
			m.moveMenuSelection(-m.resumePageSize())
			return m, nil
		case "pgdown":
			m.moveMenuSelection(m.resumePageSize())
			return m, nil
		case "home":
			m.selected = 0
			return m, nil
		case "end":
			m.selected = len(m.menu) - 1
			return m, nil
		}
	}
	switch k.String() {
	case "esc":
		m.menuKind, m.menu = "", nil
	case "left":
		m.selected = (m.selected + len(m.menu) - 1) % len(m.menu)
	case "right":
		m.selected = (m.selected + 1) % len(m.menu)
	case "enter":
		id := m.menu[m.selected]
		if m.menuKind == "resume" {
			entry := m.sessionHistory[m.selected]
			messages, err := m.session.Resume(entry.Path)
			m.menuKind, m.menu, m.sessionHistory = "", nil, nil
			if err != nil {
				m.add("error resuming session: " + err.Error())
				return m, nil
			}
			m.loop.Messages = messages
			m.history = promptHistory(messages)
			m.historyPos = len(m.history)
			m.transcript = transcriptForMessages(messages)
			m.add("· resumed session: " + entry.Path)
			m.scroll = 0
			return m, nil
		}
		if m.menuKind == "login-provider" {
			m.loginProvider = id
			m.menuTitle, m.menuKind, m.menu, m.selected = "Login method", "login-method", []string{"subscription", "api"}, 0
			return m, nil
		}
		if m.menuKind == "login-method" {
			m.menuKind, m.menu = "", nil
			cmd := exec.Command(os.Args[0], "login", m.loginProvider, id)
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return loginDone{provider: m.loginProvider, err: err} })
		}
		if m.menuKind == "effort" {
			m.loop.ReasoningEffort, m.cfg.Effort = id, id
			m.menuKind, m.menu = "", nil
			if err := config.SaveGlobal(m.cfg); err != nil {
				m.add("error saving settings: " + err.Error())
			} else {
				m.add("effort set: " + id)
			}
			return m, nil
		}
		if m.menuKind == "skill" {
			m.menuKind, m.menu = "", nil
			text, err := instructions.LoadSkill(m.skills, id)
			if err != nil {
				m.add("error loading skill: " + err.Error())
			} else {
				m.loop.System += "\n\nSkill " + id + ":\n" + text
				m.add("loaded skill: " + id)
			}
			return m, nil
		}
		m.menuKind, m.menu = "", nil
		for _, model := range m.models {
			if model.ID == id {
				m.loop.Model, m.cfg.Model = id, id
				m.loop.ContextTokens = model.ContextTokens
				m.loop.ReasoningEffort = normalizedEffort(id, m.loop.ReasoningEffort, m.models)
				m.cfg.Effort = m.loop.ReasoningEffort
				if err := config.SaveGlobal(m.cfg); err != nil {
					m.add("error saving settings: " + err.Error())
				} else {
					m.add("model set: " + id)
				}
				if len(model.Efforts) > 0 {
					m.menuTitle, m.menuKind, m.menu, m.selected = "Select effort", "effort", model.Efforts, 0
				}
				break
			}
		}
	}
	return m, nil
}

func (m *appModel) moveMenuSelection(delta int) {
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(m.menu) {
		m.selected = len(m.menu) - 1
	}
}

func (m appModel) resumePageSize() int {
	page := m.height - 7
	if page < 1 {
		return 1
	}
	return page
}

func sessionLabel(entry session.HistoryEntry) string {
	preview := entry.Preview
	if len([]rune(preview)) > 72 {
		preview = string([]rune(preview)[:72]) + "…"
	}
	return fmt.Sprintf("%s  %s", entry.Modified.Local().Format("2006-01-02 15:04"), preview)
}

func transcriptForMessages(messages []agent.Message) []string {
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "user":
			if message.Content != "" {
				lines = append(lines, "› "+message.Content)
			}
		case "assistant":
			if message.Content != "" {
				if len(lines) > 0 && lines[len(lines)-1] != "" {
					lines = append(lines, "")
				}
				lines = append(lines, message.Content)
			}
			for _, call := range message.ToolCalls {
				lines = append(lines, "└─ "+toolCallLabel(call))
			}
		case "tool":
			if message.Content != "" {
				if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], "└─ ") {
					lines[len(lines)-1] += "  " + message.Content
				} else {
					lines = append(lines, "└─ tool  "+message.Content)
				}
			}
		}
	}
	return lines
}

func (m appModel) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.clampInputCursor()
	switch k.String() {
	case "ctrl+c":
		if m.busy && m.turnCancel != nil {
			m.turnCancel()
			m.add("· cancelling turn")
			return m, nil
		}
		return m, tea.Quit
	case "esc":
		if m.busy && m.turnCancel != nil {
			m.turnCancel()
			m.add("· cancelling turn")
		}
	case "left":
		m.moveInputCursor(m.inputCursor-1, false)
	case "right":
		m.moveInputCursor(m.inputCursor+1, false)
	case "shift+left":
		m.moveInputCursor(m.inputCursor-1, true)
	case "shift+right":
		m.moveInputCursor(m.inputCursor+1, true)
	case "ctrl+left":
		m.moveInputCursor(previousWordBoundary([]rune(m.input), m.inputCursor), false)
	case "ctrl+right":
		m.moveInputCursor(nextWordBoundary([]rune(m.input), m.inputCursor), false)
	case "ctrl+shift+left":
		m.moveInputCursor(previousWordBoundary([]rune(m.input), m.inputCursor), true)
	case "ctrl+shift+right":
		m.moveInputCursor(nextWordBoundary([]rune(m.input), m.inputCursor), true)
	case "home":
		m.moveInputCursor(0, false)
	case "end":
		m.moveInputCursor(len([]rune(m.input)), false)
	case "shift+home":
		m.moveInputCursor(0, true)
	case "shift+end":
		m.moveInputCursor(len([]rune(m.input)), true)
	case "ctrl+a":
		m.inputAnchor = 0
		m.inputCursor = len([]rune(m.input))
		m.clampInputCursor()
	case "up", "down", "ctrl+p", "ctrl+n":
		matches := commandMatches(m.input)
		if len(matches) > 0 {
			if k.String() == "up" || k.String() == "ctrl+p" {
				m.selected = (m.selected + len(matches) - 1) % len(matches)
			} else {
				m.selected = (m.selected + 1) % len(matches)
			}
		} else if k.String() == "up" || k.String() == "ctrl+p" {
			m.moveHistory(-1)
		} else {
			m.moveHistory(1)
		}
	case "pgup", "ctrl+u":
		m.scroll += m.height / 2
		m.clampScroll()
	case "pgdown", "ctrl+d":
		m.scroll -= m.height / 2
		if m.scroll < 0 {
			m.scroll = 0
		}
	case "ctrl+home":
		m.scroll = 1 << 30
		m.clampScroll()
	case "ctrl+end":
		m.scroll = 0
	case "backspace", "alt+backspace", "ctrl+h":
		m.deleteInputBackward(k.Alt)
	case "ctrl+w":
		m.deleteInputBackward(true)
	case "delete":
		m.deleteInputForward()
	case "alt+enter":
		if m.busy {
			line := strings.TrimSpace(m.input)
			m.input, m.selected = "", 0
			m.inputCursor, m.inputAnchor = 0, 0
			return m.submitFollowUp(line)
		}
		m.insertInput([]rune{'\n'})
	case "shift+enter":
		m.insertInput([]rune{'\n'})
	case "enter":
		line := strings.TrimSpace(m.input)
		if matches := commandMatches(line); len(matches) > 0 {
			line = commandName(matches[m.selected%len(matches)])
		}
		m.input, m.selected = "", 0
		m.inputCursor, m.inputAnchor = 0, 0
		return m.submit(line)
	default:
		if len(k.Runes) > 0 {
			m.insertInput(k.Runes)
		}
	}
	return m, nil
}

func (m appModel) submitFollowUp(line string) (tea.Model, tea.Cmd) {
	if line == "" {
		return m, nil
	}
	m.add("› " + line)
	if len(m.history) == 0 || m.history[len(m.history)-1] != line {
		m.history = append(m.history, line)
	}
	m.historyPos, m.historyDraft = len(m.history), ""
	m.followUps = append(m.followUps, line)
	m.add(fmt.Sprintf("queued follow-up (%d)", len(m.followUps)))
	return m, nil
}

func (m appModel) submit(line string) (tea.Model, tea.Cmd) {
	if line == "" {
		return m, nil
	}
	switch line {
	case "/exit", "/quit":
		return m, tea.Quit
	case "/clear":
		if m.busy {
			m.add("wait for current turn before clearing conversation")
			return m, nil
		}
		if err := m.loop.Clear(); err != nil {
			m.add("error clearing conversation: " + err.Error())
			return m, nil
		}
		m.transcript = nil
		m.history, m.historyPos, m.historyDraft = nil, 0, ""
		m.add("conversation cleared")
	case "/new":
		if m.busy {
			m.add("wait for current turn before starting a new session")
			return m, nil
		}
		if err := m.session.New(m.wd); err != nil {
			m.add("error starting session: " + err.Error())
			return m, nil
		}
		m.loop.Messages = nil
		m.transcript = nil
		m.history, m.historyPos, m.historyDraft = nil, 0, ""
		m.add("· new session: " + m.session.Path())
	case "/clone":
		if m.busy {
			m.add("wait for current turn before cloning session")
			return m, nil
		}
		if err := m.session.Clone(m.wd, m.loop.Messages); err != nil {
			m.add("error cloning session: " + err.Error())
			return m, nil
		}
		m.add("· cloned session: " + m.session.Path())
	case "/help":
		m.add(strings.Join(commands, "  "))
	case "/session":
		m.add(m.session.Path())
	case "/resume":
		if m.busy {
			m.add("wait for current turn before resuming a session")
			return m, nil
		}
		history, err := session.History(m.wd)
		if err != nil {
			m.add("error reading session history: " + err.Error())
			return m, nil
		}
		m.sessionHistory = m.sessionHistory[:0]
		for _, entry := range history {
			if entry.Path != m.session.Path() {
				m.sessionHistory = append(m.sessionHistory, entry)
			}
		}
		if len(m.sessionHistory) == 0 {
			m.add("no previous sessions found")
			return m, nil
		}
		m.menuTitle, m.menuKind, m.selected = "Resume session", "resume", 0
		m.menu = make([]string, len(m.sessionHistory))
		for i, entry := range m.sessionHistory {
			m.menu[i] = sessionLabel(entry)
		}
	case "/logs":
		m.add(m.logPath)
	case "/update":
		if m.busy {
			m.add("wait for the current turn before updating")
			return m, nil
		}
		m.busy = true
		m.add("· updating Atom")
		return m, m.update()
	case "/skills":
		if len(m.skills) == 0 {
			m.add("no skills found")
		}
		for _, skill := range m.skills {
			m.add(fmt.Sprintf("%s — %s (%s, %s)", skill.Name, skill.Description, skill.Scope, skill.Path))
		}
	case "/skill":
		if m.busy {
			m.add("wait for current turn before loading a skill")
			return m, nil
		}
		if len(m.skills) == 0 {
			m.add("no skills found")
			return m, nil
		}
		m.menuTitle, m.menuKind, m.selected = "Load skill", "skill", 0
		m.menu = make([]string, len(m.skills))
		for i, skill := range m.skills {
			m.menu[i] = skill.Path
		}
	case "/compact":
		if m.busy {
			m.add("wait for current turn before compacting")
			return m, nil
		}
		m.busy = true
		m.add("· compacting")
		cmd := (&m).compact()
		return m, cmd
	case "/model":
		if m.busy {
			m.add("wait for current turn before changing model")
			return m, nil
		}
		m.menuKind = "loading"
		return m, m.loadModels()
	case "/effort":
		if m.busy {
			m.add("wait for current turn before changing effort")
			return m, nil
		}
		m.menuKind, m.menu = "effort-loading", nil
		return m, m.loadModels()
	case "/login":
		if m.busy {
			m.add("wait for current turn before changing login")
			return m, nil
		}
		m.menuTitle, m.menuKind, m.menu, m.selected = "Login provider", "login-provider", []string{"openai", "copilot"}, 0
	default:
		if strings.HasPrefix(line, "/") {
			m.add("unsupported here: " + line)
			return m, nil
		}
		if _, unavailable := m.loop.Provider.(unavailableProvider); unavailable {
			m.add("sign in required — use /login before starting a conversation")
			return m, nil
		}
		if m.loop.Model == "" {
			m.add("select a model first with /model")
			return m, nil
		}
		m.add("› " + line)
		if len(m.history) == 0 || m.history[len(m.history)-1] != line {
			m.history = append(m.history, line)
		}
		m.historyPos, m.historyDraft = len(m.history), ""
		m.scroll = 0
		if m.busy {
			m.queue = append(m.queue, line)
			if m.steering != nil {
				select {
				case m.steering <- line:
				default:
				}
			}
			m.add(fmt.Sprintf("queued for next turn (%d)", len(m.queue)))
			return m, nil
		}
		m.busy = true
		cmd := (&m).startTurn(line)
		return m, cmd
	}
	return m, nil
}

func promptHistory(messages []agent.Message) []string {
	var history []string
	for _, message := range messages {
		if message.Role == "user" && message.Content != "" && !strings.HasPrefix(message.Content, "Session handoff summary:") {
			history = append(history, message.Content)
		}
	}
	return history
}

func (m *appModel) moveHistory(delta int) {
	if len(m.history) == 0 {
		return
	}
	if m.historyPos < 0 || m.historyPos > len(m.history) {
		m.historyPos = len(m.history)
	}
	if delta < 0 {
		if m.historyPos == len(m.history) {
			m.historyDraft = m.input
		}
		if m.historyPos > 0 {
			m.historyPos--
		}
	} else if m.historyPos < len(m.history) {
		m.historyPos++
	}
	if m.historyPos == len(m.history) {
		m.input = m.historyDraft
	} else {
		m.input = m.history[m.historyPos]
	}
	m.inputCursor = len([]rune(m.input))
	m.inputAnchor = m.inputCursor
}

func (m appModel) View() string {
	if m.menuKind == "resume" {
		return m.pickerView("Resume session", "resume", "sessions", "session(s)")
	}
	if m.menuKind == "model" {
		return m.pickerView("Select model", "select", "models", "model(s)")
	}
	width := m.width - 2
	if width < 20 {
		width = 20
	}
	var b strings.Builder
	logoWidth := 0
	for _, line := range atomLogo {
		if lineWidth := len([]rune(strings.TrimRight(line, " "))); lineWidth > logoWidth {
			logoWidth = lineWidth
		}
	}
	for i, line := range atomLogo {
		logo := strings.TrimRight(line, " ")
		fmt.Fprintf(&b, "\033[36m%s\033[0m", logo)
		if i == 1 || i == 2 {
			gap := strings.Repeat(" ", logoWidth-len([]rune(logo))+3)
			if i == 1 {
				fmt.Fprintf(&b, "%s\033[1matom %s\033[0m", gap, version)
			} else {
				fmt.Fprintf(&b, "%s\033[2m%s\033[0m", gap, authSource(m.loop.Provider))
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("\n", headerPadding))
	lines, _ := m.visibleTranscript()
	max := m.transcriptHeight()
	if m.scroll > 0 && len(lines) > 0 {
		lines[0].text = "↑ " + lines[0].text
	}
	for i, line := range lines {
		m.writeLine(&b, line, len(atomLogo)+headerPadding+i)
		b.WriteByte('\n')
	}
	for i := len(lines); i < max; i++ {
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("\n", composerPadding))
	if m.thinking {
		fmt.Fprintf(&b, "\033[2mworking%s\033[0m\n", workingFrames[m.thinkingFrame%len(workingFrames)])
	}
	if m.toast != "" {
		fmt.Fprintf(&b, "\033[7m %s \033[0m\n", m.toast)
	}
	if m.menuKind != "" && m.menuKind != "loading" {
		fmt.Fprintf(&b, "\033[1m%s\033[0m  \033[2m(←/→, Enter)\033[0m\n", m.menuTitle)
		for i, item := range m.menu {
			if i == m.selected {
				fmt.Fprintf(&b, "\033[7m › %s \033[0m ", item)
			} else {
				fmt.Fprintf(&b, "   %s ", item)
			}
		}
		b.WriteByte('\n')
	} else if matches := commandMatches(m.input); len(matches) > 0 {
		for i, item := range matches {
			if i == m.selected%len(matches) {
				fmt.Fprintf(&b, "\033[7m › %s \033[0m ", item)
			} else {
				fmt.Fprintf(&b, "\033[2m   %s\033[0m ", item)
			}
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\033[36m┌%s┐\033[0m\n", strings.Repeat("─", width-2))
	inputLines, _ := m.inputViewport()
	for i, line := range inputLines {
		fmt.Fprint(&b, "\033[36m│\033[0m")
		if i == 0 {
			fmt.Fprint(&b, " › ")
		} else {
			fmt.Fprint(&b, "   ")
		}
		m.writeInputLine(&b, line)
		b.WriteString("\033[K\n")
	}
	state := fmt.Sprintf("%s  ·  %s", m.loop.Provider.Name(), m.loop.Model)
	if m.loop.ReasoningEffort != "" {
		state += "  ·  " + m.loop.ReasoningEffort
	}
	if m.busy {
		state += "  ·  working"
	}
	if len(m.queue) > 0 {
		state += fmt.Sprintf("  ·  queued %d", len(m.queue))
	}
	fmt.Fprintf(&b, "\033[36m└%s┘\033[0m\n", strings.Repeat("─", width-2))
	left, gap, right := footerParts(state, contextStatus(m.loop, m.contextTokenLimit()), width)
	fmt.Fprintf(&b, "\033[36m%s\033[0m%s\033[2m%s\033[0m", left, gap, right)
	return b.String()
}

func (m appModel) pickerView(title, action, moreLabel, countLabel string) string {
	width := m.width
	if width < 20 {
		width = 20
	}
	page := m.resumePageSize()
	start := m.selected - page/2
	if start < 0 {
		start = 0
	}
	if start+page > len(m.menu) {
		start = len(m.menu) - page
		if start < 0 {
			start = 0
		}
	}
	end := start + page
	if end > len(m.menu) {
		end = len(m.menu)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\033[1m%s\033[0m  \033[2m(↑/↓ move, Enter %s, Esc cancel)\033[0m\n\n", title, action)
	if start > 0 {
		fmt.Fprintf(&b, "\033[2m↑ more %s\033[0m\n", moreLabel)
	}
	for i := start; i < end; i++ {
		label := truncateRunes(m.menu[i], width-4)
		if i == m.selected {
			fmt.Fprintf(&b, "\033[7m › %-*s \033[0m\n", width-4, label)
		} else {
			fmt.Fprintf(&b, "   %s\n", label)
		}
	}
	if end < len(m.menu) {
		fmt.Fprintf(&b, "\033[2m↓ more %s\033[0m\n", moreLabel)
	}
	fmt.Fprintf(&b, "\n\033[2m%d %s\033[0m", len(m.menu), countLabel)
	return b.String()
}

func truncateRunes(text string, limit int) string {
	if limit < 1 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

// footerParts keeps the model state at the left edge and the context state at
// the right edge, while giving the context state priority in narrow terminals.
func footerParts(left, right string, width int) (string, string, string) {
	if width < 1 {
		return "", "", ""
	}
	right = shortenLabel(right, width)
	space := width - len([]rune(left)) - len([]rune(right))
	if space < 1 {
		left = shortenLabel(left, width-len([]rune(right))-1)
		space = width - len([]rune(left)) - len([]rune(right))
	}
	return left, strings.Repeat(" ", space), right
}

func shortenLabel(label string, width int) string {
	runes := []rune(label)
	if width <= 0 {
		return ""
	}
	if len(runes) <= width {
		return label
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func contextStatus(loop *agent.Loop, limit int) string {
	if limit < 1 {
		return fmt.Sprintf("ctx ~%s/unknown · processed %s", formatTokens(loop.ApproxTokens()), formatTokens(loop.InputTokens+loop.OutputTokens))
	}
	used := loop.ApproxTokens()
	percent := used * 100 / limit
	return fmt.Sprintf("ctx ~%s/%s (%d%%) · processed %s", formatTokens(used), formatTokens(limit), percent, formatTokens(loop.InputTokens+loop.OutputTokens))
}

func (m appModel) contextTokenLimit() int {
	for _, model := range m.models {
		if model.ID == m.loop.Model {
			return model.ContextTokens
		}
	}
	return 0
}

func formatTokens(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fm", float64(n)/1000000)
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func (m appModel) writeLine(b *strings.Builder, line transcriptLine, y int) {
	style := "\033[97m"
	if line.kind == markdownHeading {
		style = "\033[1;97m"
	} else if line.kind == markdownCode {
		style = "\033[2;36m"
	} else if line.kind == markdownQuote {
		style = "\033[2;97m"
	} else if strings.HasPrefix(line.text, "·") || strings.HasPrefix(line.text, "└") || strings.HasPrefix(line.text, "queued") || strings.HasPrefix(line.text, "model set:") || strings.HasPrefix(line.text, "Loaded ") {
		style = "\033[2m"
	} else if strings.HasPrefix(line.text, "› ") {
		style = "\033[36m"
	}
	m.writeSelected(b, line.text, y, 0, style)
}

func (m appModel) writeInputLine(b *strings.Builder, line inputLine) {
	m.clampInputCursor()
	from, until := m.inputSelection()
	runes := []rune(line.text)
	for offset, char := range runes {
		i := line.start + offset
		if i == m.inputCursor {
			b.WriteString("\033[7m")
			b.WriteRune(char)
			b.WriteString("\033[0m")
			continue
		}
		if i >= from && i < until {
			b.WriteString("\033[7m")
			b.WriteRune(runes[i])
			b.WriteString("\033[0m")
			continue
		}
		b.WriteRune(char)
	}
	if m.inputCursor == line.end {
		b.WriteString("\033[7m \033[0m")
	}
}

func (m appModel) writeSelected(b *strings.Builder, line string, y, offset int, style string) {
	if style != "" {
		b.WriteString(style)
	}
	a, z := m.selectionFrom, m.selectionTo
	if !m.selecting || z.y < a.y || (z.y == a.y && z.x < a.x) {
		if !m.selecting {
			b.WriteString(line)
			if style != "" {
				b.WriteString("\033[0m")
			}
			return
		}
		a, z = z, a
	}
	if y < a.y || y > z.y {
		b.WriteString(line)
		if style != "" {
			b.WriteString("\033[0m")
		}
		return
	}
	r := []rune(line)
	from, to := 0, len(r)
	if y == a.y {
		from = a.x - offset
	}
	if y == z.y {
		to = z.x - offset
	}
	if from < 0 {
		from = 0
	}
	if to < 0 {
		to = 0
	}
	if from > len(r) {
		from = len(r)
	}
	if to > len(r) {
		to = len(r)
	}
	if to <= from {
		b.WriteString(line)
	} else {
		b.WriteString(string(r[:from]))
		b.WriteString("\033[7m")
		b.WriteString(string(r[from:to]))
		b.WriteString("\033[27m")
		b.WriteString(string(r[to:]))
	}
	if style != "" {
		b.WriteString("\033[0m")
	}
}

func nextThinkingTick() tea.Cmd {
	return tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return thinkingTick{} })
}

type markdownKind uint8

const (
	markdownText markdownKind = iota
	markdownHeading
	markdownCode
	markdownQuote
)

type transcriptLine struct {
	text string
	kind markdownKind
}

// renderedTranscript turns the small, useful subset of Markdown commonly
// emitted by coding agents into terminal-friendly text. The transcript itself
// remains unmodified, so session files and clipboard copies retain the model's
// original Markdown.
func renderedTranscript(entries []string, width int) []transcriptLine {
	var out []transcriptLine
	for _, entry := range entries {
		for _, line := range markdownLines(entry) {
			out = append(out, wrapTranscriptLine(line, width)...)
		}
	}
	return out
}

func markdownLines(entry string) []transcriptLine {
	// Prompts, tool activity, and Atom's own status lines are already
	// terminal UI text, rather than model Markdown.
	if strings.HasPrefix(entry, "› ") || strings.HasPrefix(entry, "└") || strings.HasPrefix(entry, "·") ||
		strings.HasPrefix(entry, "queued") || strings.HasPrefix(entry, "model set:") || strings.HasPrefix(entry, "Loaded ") {
		return plainTranscriptLines(entry)
	}

	var out []transcriptLine
	inCode := false
	for _, line := range strings.Split(entry, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inCode = !inCode
			continue // Fence markers add noise in a terminal.
		}
		if inCode {
			out = append(out, transcriptLine{text: line, kind: markdownCode})
			continue
		}
		if level, text := markdownHeadingText(trimmed); level > 0 {
			out = append(out, transcriptLine{text: markdownInlineText(text), kind: markdownHeading})
			continue
		}
		if isMarkdownRule(trimmed) {
			out = append(out, transcriptLine{text: "────────────────────────────────"})
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			out = append(out, transcriptLine{text: "│ " + markdownInlineText(strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))), kind: markdownQuote})
			continue
		}
		out = append(out, transcriptLine{text: markdownInlineText(markdownListText(line))})
	}
	return out
}

func plainTranscriptLines(entry string) []transcriptLine {
	lines := strings.Split(entry, "\n")
	out := make([]transcriptLine, len(lines))
	for i, line := range lines {
		if i > 0 && strings.HasPrefix(entry, "› ") && !strings.HasPrefix(line, "› ") {
			line = "› " + line
		}
		out[i] = transcriptLine{text: line}
	}
	return out
}

func markdownHeadingText(line string) (int, string) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || len(line) == level || line[level] != ' ' {
		return 0, ""
	}
	return level, strings.TrimSpace(strings.TrimRight(line[level:], "# "))
}

func isMarkdownRule(line string) bool {
	if len(line) < 3 {
		return false
	}
	for _, r := range line {
		if r != '-' && r != '*' && r != '_' && r != ' ' {
			return false
		}
	}
	return true
}

func markdownListText(line string) string {
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	text := strings.TrimLeft(line, " \t")
	if len(text) >= 6 && (strings.HasPrefix(text, "- [ ] ") || strings.HasPrefix(text, "* [ ] ")) {
		return indent + "☐ " + text[6:]
	}
	if len(text) >= 6 && (strings.HasPrefix(text, "- [x] ") || strings.HasPrefix(text, "* [x] ") || strings.HasPrefix(text, "- [X] ") || strings.HasPrefix(text, "* [X] ")) {
		return indent + "☑ " + text[6:]
	}
	if len(text) >= 2 && (strings.HasPrefix(text, "- ") || strings.HasPrefix(text, "* ") || strings.HasPrefix(text, "+ ")) {
		return indent + "• " + text[2:]
	}
	return line
}

// markdownInlineText deliberately favors readable terminal text over a full
// Markdown AST. It removes visual delimiters while preserving link URLs.
func markdownInlineText(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '\\' && i+1 < len(text) && strings.ContainsRune("\\`*_[]()", rune(text[i+1])) {
			b.WriteByte(text[i+1])
			i += 2
			continue
		}
		if strings.HasPrefix(text[i:], "**") || strings.HasPrefix(text[i:], "__") {
			i += 2
			continue
		}
		if text[i] == '`' || text[i] == '*' || text[i] == '_' {
			i++
			continue
		}
		if text[i] == '[' {
			if close := strings.Index(text[i:], "]("); close >= 0 {
				close += i
				if end := strings.IndexByte(text[close+2:], ')'); end >= 0 {
					end += close + 2
					b.WriteString(text[i+1 : close])
					b.WriteString(" (")
					b.WriteString(text[close+2 : end])
					b.WriteByte(')')
					i = end + 1
					continue
				}
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

func wrapTranscriptLine(line transcriptLine, width int) []transcriptLine {
	if width < 1 {
		width = 1
	}
	runes := []rune(line.text)
	if len(runes) == 0 {
		return []transcriptLine{line}
	}
	var out []transcriptLine
	isUser := strings.HasPrefix(line.text, "› ")
	for len(runes) > width {
		cut := width
		for cut > width/2 && runes[cut] != ' ' {
			cut--
		}
		if cut == width/2 {
			cut = width
		}
		out = append(out, transcriptLine{text: string(runes[:cut]), kind: line.kind})
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
		if isUser && !strings.HasPrefix(string(runes), "› ") {
			runes = append([]rune("› "), runes...)
		}
	}
	out = append(out, transcriptLine{text: string(runes), kind: line.kind})
	return out
}

func wrappedTranscript(entries []string, width int) []string {
	rendered := renderedTranscript(entries, width)
	out := make([]string, len(rendered))
	for i, line := range rendered {
		out[i] = line.text
	}
	return out
}

func authSource(p agent.Provider) string {
	if _, unavailable := p.(unavailableProvider); unavailable {
		return "Sign in required"
	}
	if openai, ok := p.(*provider.OpenAICompatible); ok && openai.Responses {
		return "ChatGPT subscription"
	}
	if p.Name() == "copilot" {
		return "GitHub Copilot"
	}
	return "OpenAI API key"
}

func basePrompt(wd string) string {
	return fmt.Sprintf(`You are Atom, a coding agent working in %s. Use available tools to inspect real state before drawing conclusions. Continue through tool results until the user's request is handled; never claim a tool ran when it did not. Read relevant code and trace callers before editing, make the smallest complete change, and run focused verification. Follow provided AGENTS.md instructions. Before using a relevant or requested skill, call load_skill and follow its SKILL.md.`, wd)
}
func fatal(s string) { fmt.Fprintln(os.Stderr, "atom:", s); os.Exit(1) }
