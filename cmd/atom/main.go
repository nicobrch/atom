package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/nicobrch/atom/internal/agent"
	"github.com/nicobrch/atom/internal/config"
	"github.com/nicobrch/atom/internal/instructions"
	"github.com/nicobrch/atom/internal/provider"
	"github.com/nicobrch/atom/internal/session"
	"github.com/nicobrch/atom/internal/tool"
)

const version = "0.1.1"

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
	fmt.Fprintf(t.out, "\033[2m└─\033[0m \033[36m%s\033[0m ", c.Name)
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
	var printMode, showVersion bool
	flag.StringVar(&providerName, "provider", "", "provider: openai or copilot")
	flag.StringVar(&model, "model", "", "model identifier")
	flag.StringVar(&prompt, "p", "", "run one prompt and exit")
	flag.BoolVar(&printMode, "print", false, "plain output in interactive-disabled mode")
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
	if printMode && prompt == "" {
		fatal("--print requires a prompt")
	}
	wd, err := os.Getwd()
	if err != nil {
		fatal(err.Error())
	}
	cfg, err := config.Load(wd)
	if err != nil {
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
		if prompt != "" {
			fatal(err.Error())
		}
		p = unavailableProvider{err}
	}
	agentsText, agentFiles, err := instructions.LoadAgents(wd)
	if err != nil {
		fatal(err.Error())
	}
	skills, err := instructions.DiscoverSkills(wd)
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
	defer store.Close()
	var obs agent.Observer
	if printMode {
		obs = &plain{out: os.Stdout}
	} else {
		obs = &terminal{out: os.Stdout}
	}
	loop := &agent.Loop{Provider: p, Model: cfg.Model, ReasoningEffort: cfg.Effort, Tools: toolsAsInterface(tool.NewRegistry(wd, time.Duration(cfg.BashTimeoutSeconds)*time.Second)), System: system, Sink: store, Observer: obs}
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
			fatal(err.Error())
		}
		return
	}
	runApp(ctx, loop, skills, store.Path(), cfg, wd, len(agentFiles))
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
		key, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && len(key) == 0 {
			fatal(err.Error())
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
	switch name {
	case "openai":
		cmd := exec.Command("codex", "login")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("Codex sign-in failed: %w", err)
		}
		p, err := provider.OpenAIFromEnv()
		if err != nil || !p.Responses {
			return fmt.Errorf("Codex sign-in completed, but Atom cannot use ChatGPT subscription credentials")
		}
		fmt.Println("ChatGPT subscription available to Atom.")
	case "copilot", "github-copilot":
		code, err := provider.StartCopilotLogin()
		if err != nil {
			return err
		}
		fmt.Printf("Open %s and enter code %s. Waiting for authorization…\n", code.VerificationURI, code.UserCode)
		if err := provider.FinishCopilotLogin(code); err != nil {
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

var commands = []string{
	"/clear", "/compact", "/exit", "/help", "/login <openai|copilot> [subscription|api]",
	"/effort", "/model <id>", "/models", "/session", "/skill <name>", "/skills",
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

type promptModel struct {
	value    string
	selected int
	done     bool
}

func (m promptModel) Init() tea.Cmd { return nil }
func (m promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	matches := commandMatches(m.value)
	switch k.String() {
	case "ctrl+c", "esc":
		m.done = true
		m.value = ""
		return m, tea.Quit
	case "enter":
		if len(matches) > 0 {
			m.value = commandName(matches[m.selected])
		}
		m.done = true
		return m, tea.Quit
	case "up":
		if len(matches) > 0 {
			m.selected = (m.selected + len(matches) - 1) % len(matches)
		}
	case "down":
		if len(matches) > 0 {
			m.selected = (m.selected + 1) % len(matches)
		}
	case "backspace":
		runes := []rune(m.value)
		if len(runes) > 0 {
			m.value = string(runes[:len(runes)-1])
		}
	default:
		if len(k.Runes) > 0 {
			m.value += string(k.Runes)
		}
	}
	return m, nil
}
func (m promptModel) View() string {
	if m.done {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\033[32m› \033[0m%s", m.value)
	for i, command := range commandMatches(m.value) {
		if i == m.selected {
			fmt.Fprintf(&b, "\n\033[7m › %s \033[0m", command)
		} else {
			fmt.Fprintf(&b, "\n\033[2m   %s\033[0m", command)
		}
	}
	return b.String()
}

func readPrompt() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Print("\033[32m› \033[0m")
		return bufio.NewReader(os.Stdin).ReadString('\n')
	}
	final, err := tea.NewProgram(promptModel{}, tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout)).Run()
	if err != nil {
		return "", err
	}
	return final.(promptModel).value, nil
}

type choiceModel struct {
	title    string
	options  []string
	selected int
	done     bool
}

func (m choiceModel) Init() tea.Cmd { return nil }
func (m choiceModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "ctrl+c", "esc":
		m.done, m.selected = true, -1
		return m, tea.Quit
	case "enter":
		m.done = true
		return m, tea.Quit
	case "up":
		m.selected = (m.selected + len(m.options) - 1) % len(m.options)
	case "down":
		m.selected = (m.selected + 1) % len(m.options)
	}
	return m, nil
}
func (m choiceModel) View() string {
	if m.done {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\033[1m%s\033[0m  \033[2m(↑/↓, Enter)\033[0m", m.title)
	for i, option := range m.options {
		if i == m.selected {
			fmt.Fprintf(&b, "\n\033[7m › %s \033[0m", option)
		} else {
			fmt.Fprintf(&b, "\n   %s", option)
		}
	}
	return b.String()
}

func choose(label string, options []string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("%s requires an interactive terminal", label)
	}
	final, err := tea.NewProgram(choiceModel{title: label, options: options}, tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout)).Run()
	if err != nil {
		return "", err
	}
	m := final.(choiceModel)
	if m.selected < 0 {
		return "", io.EOF
	}
	return m.options[m.selected], nil
}

func readSecret() (string, error) {
	fmt.Print("API key: ")
	key, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return strings.TrimSpace(string(key)), err
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

func printBanner(p agent.Provider, model, wd string) {
	text := fmt.Sprintf("\033[1mAtom %s\033[0m  %s/%s  %s", version, p.Name(), model, wd)
	fmt.Printf("\033]0;Atom %s/%s\a%s\n", p.Name(), model, text)
}

func refreshBanner(p agent.Provider, model, wd string) {
	// Bubble Tea owns its frame. Clearing after its picker exits avoids stale
	// frames while preserving terminal scrollback.
	fmt.Print("\033[2J\033[H")
	printBanner(p, model, wd)
}

func updateBanner(p agent.Provider, model, wd string, atTop bool) {
	if atTop {
		refreshBanner(p, model, wd)
	} else {
		fmt.Printf("model: %s/%s\n", p.Name(), model)
	}
}

func setModel(ctx context.Context, loop *agent.Loop, id, wd string, bannerAtTop bool) error {
	models, err := availableModels(ctx, loop.Provider)
	if err != nil {
		return err
	}
	for _, model := range models {
		if model.ID == id {
			loop.Model = id
			updateBanner(loop.Provider, loop.Model, wd, bannerAtTop)
			return nil
		}
	}
	return fmt.Errorf("model %q is not available for %s; use /model", id, loop.Provider.Name())
}

func runInteractive(ctx context.Context, loop *agent.Loop, skills []instructions.Skill, sessionPath string, cfg config.Config, wd string) {
	bannerAtTop := true
	for {
		raw, err := readPrompt()
		if err != nil {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case line == "/exit" || line == "/quit":
			return
		case line == "/help":
			fmt.Println(strings.Join(commands, "  "))
			continue
		case line == "/models":
			models, err := availableModels(ctx, loop.Provider)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				continue
			}
			for _, model := range models {
				marker := " "
				if model.ID == loop.Model {
					marker = "*"
				}
				if model.Name != "" && model.Name != model.ID {
					fmt.Printf("%s %s — %s\n", marker, model.ID, model.Name)
				} else {
					fmt.Printf("%s %s\n", marker, model.ID)
				}
			}
			continue
		case strings.HasPrefix(line, "/model "):
			if err := setModel(ctx, loop, strings.TrimSpace(strings.TrimPrefix(line, "/model ")), wd, bannerAtTop); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
			}
			continue
		case line == "/model":
			models, err := availableModels(ctx, loop.Provider)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				continue
			}
			options := make([]string, len(models))
			for i, model := range models {
				options[i] = model.ID
			}
			id, err := choose("Select model", options)
			if err == nil {
				if err := setModel(ctx, loop, id, wd, bannerAtTop); err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
				}
			}
			continue
		case line == "/session":
			fmt.Println(sessionPath)
			continue
		case line == "/skills":
			for _, sk := range skills {
				fmt.Printf("%s — %s\n", sk.Name, sk.Description)
			}
			continue
		case strings.HasPrefix(line, "/skill "):
			name := strings.TrimSpace(strings.TrimPrefix(line, "/skill "))
			text, err := instructions.LoadSkill(skills, name)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				continue
			}
			loop.System += "\n\nSkill " + name + ":\n" + text
			fmt.Println("loaded skill", name)
			continue
		case line == "/compact":
			if err := loop.Compact(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
			}
			continue
		case line == "/clear":
			loop.Messages = nil
			fmt.Println("conversation cleared")
			continue
		case strings.HasPrefix(line, "/login"):
			parts := strings.Fields(line)
			if len(parts) > 3 {
				fmt.Fprintln(os.Stderr, "usage: /login <openai|copilot> [subscription|api]")
				continue
			}
			name := ""
			if len(parts) > 1 {
				name = parts[1]
			} else {
				name, err = choose("Login provider  (↑/↓, Enter)", []string{"openai", "copilot"})
				if err != nil {
					continue
				}
			}
			method := ""
			if len(parts) > 2 {
				method = parts[2]
			} else {
				method, err = choose("Login method  (↑/↓, Enter)", []string{"subscription", "api"})
				if err != nil {
					continue
				}
			}
			if method == "api" {
				key, err := readSecret()
				if err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					continue
				}
				if err := provider.SaveAPIKey(name, key); err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					continue
				}
			} else if err := loginSubscription(name, method); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				continue
			}
			p, err := selectProvider(name)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				continue
			}
			loop.Provider = p
			fmt.Printf("logged in with %s\n", p.Name())
			updateBanner(loop.Provider, loop.Model, wd, bannerAtTop)
			continue
		}
		if loop.ApproxTokens() > int(float64(cfg.ContextTokens)*cfg.AutoCompactAt) {
			fmt.Println("\033[2mCompacting context…\033[0m")
			if err := loop.Compact(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				continue
			}
		}
		fmt.Printf("\n\033[7m › %s \033[0m\n", line)
		bannerAtTop = false
		if err := loop.Prompt(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

type uiText string
type uiStatus string
type uiTool struct{ name, output string }
type turnDone struct{ err error }
type modelsLoaded struct {
	models []provider.Model
	err    error
}
type loginDone struct {
	provider string
	err      error
}
type thinkingTick struct{}
type toastDone struct{}

type mousePoint struct{ x, y int }

type uiObserver struct{ events chan<- tea.Msg }

func (o uiObserver) Text(s string)              { o.events <- uiText(s) }
func (o uiObserver) Status(s string)            { o.events <- uiStatus(s) }
func (o uiObserver) ToolStart(c agent.ToolCall) { o.events <- uiTool{name: c.Name} }
func (o uiObserver) ToolEnd(_ agent.ToolCall, out string, err error) {
	if err != nil {
		out = "error: " + err.Error()
	}
	one := strings.ReplaceAll(strings.TrimSpace(out), "\n", " ")
	if len(one) > 140 {
		one = one[:140] + "…"
	}
	o.events <- uiTool{output: one}
}

type appModel struct {
	ctx           context.Context
	loop          *agent.Loop
	skills        []instructions.Skill
	sessionPath   string
	cfg           config.Config
	wd            string
	events        chan tea.Msg
	input         string
	transcript    []string
	queue         []string
	busy          bool
	width         int
	height        int
	selected      int
	menu          []string
	menuTitle     string
	menuKind      string
	loginProvider string
	models        []provider.Model
	assistantOpen bool
	scroll        int
	thinking      bool
	thinkingFrame int
	selecting     bool
	selectionFrom mousePoint
	selectionTo   mousePoint
	toast         string
}

func runApp(ctx context.Context, loop *agent.Loop, skills []instructions.Skill, sessionPath string, cfg config.Config, wd string, agentFiles int) {
	events := make(chan tea.Msg, 64)
	loop.Observer = uiObserver{events: events}
	m := appModel{ctx: ctx, loop: loop, skills: skills, sessionPath: sessionPath, cfg: cfg, wd: wd, events: events, width: 100, height: 30}
	if agentFiles > 0 {
		m.transcript = append(m.transcript, fmt.Sprintf("Loaded %d AGENTS.md file(s)", agentFiles))
	}
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "atom:", err)
	}
}

func (m appModel) Init() tea.Cmd { return waitEvent(m.events) }
func waitEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}
func (m appModel) startTurn(text string) tea.Cmd {
	return func() tea.Msg {
		err := m.loop.Prompt(m.ctx, text)
		return turnDone{err: err}
	}
}
func (m appModel) compact() tea.Cmd {
	return func() tea.Msg { return turnDone{err: m.loop.Compact(m.ctx)} }
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
	limit := len(wrappedTranscript(m.transcript, m.width-2)) - max
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
	max := m.height - 6
	if m.thinking {
		max--
	}
	if m.toast != "" {
		max--
	}
	if (m.menuKind != "" && m.menuKind != "loading" && m.menuKind != "models") || len(commandMatches(m.input)) > 0 {
		max--
	}
	if max < 3 {
		return 3
	}
	return max
}

func (m appModel) visibleTranscript() ([]string, int) {
	lines := wrappedTranscript(m.transcript, m.width-2)
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

func copyToClipboard(text string) error {
	cmd := exec.Command("pbcopy")
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
	inputRow := m.height - 3
	var out []string
	for y := a.y; y <= z.y; y++ {
		line, offset, ok := "", 0, false
		if y >= 2 && y < 2+len(lines) {
			line, ok = lines[y-2], true
		} else if y == inputRow {
			line, offset, ok = m.input, 4, true
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
func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case uiText:
		m.thinking = false
		if !m.assistantOpen {
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
		return m, waitEvent(m.events)
	case uiTool:
		m.thinking = false
		if msg.name != "" {
			m.add("└─ " + msg.name)
		} else if msg.output != "" && len(m.transcript) > 0 {
			m.transcript[len(m.transcript)-1] += "  " + msg.output
		}
		return m, waitEvent(m.events)
	case thinkingTick:
		if m.thinking {
			m.thinkingFrame = (m.thinkingFrame + 1) % 4
			return m, nextThinkingTick()
		}
		return m, nil
	case toastDone:
		m.toast = ""
		return m, nil
	case turnDone:
		m.busy = false
		m.thinking = false
		if msg.err != nil {
			m.add("error: " + msg.err.Error())
		}
		if len(m.queue) > 0 {
			next := m.queue[0]
			m.queue = m.queue[1:]
			m.busy = true
			m.add("› " + next)
			return m, m.startTurn(next)
		}
		return m, nil
	case modelsLoaded:
		if msg.err != nil {
			m.add("error: " + msg.err.Error())
			return m, nil
		}
		m.models = msg.models
		if m.menuKind == "models" {
			for _, model := range m.models {
				mark := " "
				if model.ID == m.loop.Model {
					mark = "*"
				}
				m.add(fmt.Sprintf("%s %s", mark, model.ID))
			}
			m.menuKind = ""
			return m, nil
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
		m.menuTitle, m.menuKind, m.selected = "Select model", "model", 0
		m.menu = make([]string, len(m.models))
		for i, model := range m.models {
			m.menu[i] = model.ID
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
		if err := config.Save(m.wd, m.cfg); err != nil {
			m.add("error saving settings: " + err.Error())
		} else {
			m.add("logged in: " + authSource(p))
		}
		return m, nil
	case tea.KeyMsg:
		if m.menuKind != "" && m.menuKind != "models" {
			return m.menuKey(msg)
		}
		return m.inputKey(msg)
	case tea.MouseMsg:
		switch {
		case msg.Button == tea.MouseButtonWheelUp:
			m.scroll += 3
		case msg.Button == tea.MouseButtonWheelDown:
			m.scroll -= 3
			if m.scroll < 0 {
				m.scroll = 0
			}
		case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
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
	switch k.String() {
	case "esc":
		m.menuKind, m.menu = "", nil
	case "up":
		m.selected = (m.selected + len(m.menu) - 1) % len(m.menu)
	case "down":
		m.selected = (m.selected + 1) % len(m.menu)
	case "enter":
		id := m.menu[m.selected]
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
			if err := config.Save(m.wd, m.cfg); err != nil {
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
				if err := config.Save(m.wd, m.cfg); err != nil {
					m.add("error saving settings: " + err.Error())
				} else {
					m.add("model set: " + id)
				}
				break
			}
		}
	}
	return m, nil
}

func (m appModel) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "up", "down", "ctrl+p", "ctrl+n":
		matches := commandMatches(m.input)
		if len(matches) > 0 {
			if k.String() == "up" || k.String() == "ctrl+p" {
				m.selected = (m.selected + len(matches) - 1) % len(matches)
			} else {
				m.selected = (m.selected + 1) % len(matches)
			}
		} else if m.input == "" {
			if k.String() == "up" || k.String() == "ctrl+p" {
				m.scroll++
			} else if m.scroll > 0 {
				m.scroll--
			}
			m.clampScroll()
		}
	case "pgup", "ctrl+u":
		m.scroll += m.height / 2
		m.clampScroll()
	case "pgdown", "ctrl+d":
		m.scroll -= m.height / 2
		if m.scroll < 0 {
			m.scroll = 0
		}
	case "home":
		m.scroll = 1 << 30
		m.clampScroll()
	case "end":
		m.scroll = 0
	case "backspace":
		r := []rune(m.input)
		if len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	case "enter":
		line := strings.TrimSpace(m.input)
		if matches := commandMatches(line); len(matches) > 0 {
			line = commandName(matches[m.selected%len(matches)])
		}
		m.input, m.selected = "", 0
		return m.submit(line)
	default:
		if len(k.Runes) > 0 {
			m.input += string(k.Runes)
		}
	}
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
		m.loop.Messages, m.transcript = nil, nil
		m.add("conversation cleared")
	case "/help":
		m.add(strings.Join(commands, "  "))
	case "/session":
		m.add(m.sessionPath)
	case "/skills":
		if len(m.skills) == 0 {
			m.add("no skills found")
		}
		for _, skill := range m.skills {
			m.add(fmt.Sprintf("%s — %s", skill.Name, skill.Description))
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
			m.menu[i] = skill.Name
		}
	case "/compact":
		if m.busy {
			m.add("wait for current turn before compacting")
			return m, nil
		}
		m.busy = true
		m.add("· compacting")
		return m, m.compact()
	case "/models":
		m.menuKind = "models"
		return m, m.loadModels()
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
		if strings.HasPrefix(line, "/skill ") {
			if m.busy {
				m.add("wait for current turn before loading a skill")
				return m, nil
			}
			name := strings.TrimSpace(strings.TrimPrefix(line, "/skill "))
			text, err := instructions.LoadSkill(m.skills, name)
			if err != nil {
				m.add("error loading skill: " + err.Error())
			} else {
				m.loop.System += "\n\nSkill " + name + ":\n" + text
				m.add("loaded skill: " + name)
			}
			return m, nil
		}
		if strings.HasPrefix(line, "/") {
			m.add("unsupported here: " + line)
			return m, nil
		}
		m.add("› " + line)
		m.scroll = 0
		if m.busy {
			m.queue = append(m.queue, line)
			m.add(fmt.Sprintf("queued (%d)", len(m.queue)))
			return m, nil
		}
		m.busy = true
		return m, m.startTurn(line)
	}
	return m, nil
}

func (m appModel) View() string {
	width := m.width - 2
	if width < 20 {
		width = 20
	}
	var b strings.Builder
	header := fmt.Sprintf("\033[1mAtom %s\033[0m  \033[36m%s/%s\033[0m", version, m.loop.Provider.Name(), m.loop.Model)
	if m.loop.ReasoningEffort != "" {
		header += "  \033[36m· " + m.loop.ReasoningEffort + "\033[0m"
	}
	fmt.Fprintln(&b, header)
	fmt.Fprintf(&b, "\033[2m%s  ·  %s\033[0m  \033[36m· %s\033[0m\n", m.wd, authSource(m.loop.Provider), contextStatus(m.loop, m.cfg.ContextTokens))
	lines, _ := m.visibleTranscript()
	max := m.transcriptHeight()
	if m.scroll > 0 && len(lines) > 0 {
		lines[0] = "↑ " + lines[0]
	}
	for i, line := range lines {
		m.writeLine(&b, line, i+2)
		b.WriteByte('\n')
	}
	for i := len(lines); i < max; i++ {
		b.WriteByte('\n')
	}
	if m.thinking {
		fmt.Fprintf(&b, "\033[2m· thinking%s\033[0m\n", strings.Repeat(".", m.thinkingFrame+1))
	}
	if m.toast != "" {
		fmt.Fprintf(&b, "\033[7m %s \033[0m\n", m.toast)
	}
	if m.menuKind != "" && m.menuKind != "loading" && m.menuKind != "models" {
		fmt.Fprintf(&b, "\033[1m%s\033[0m\n", m.menuTitle)
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
	fmt.Fprint(&b, "\033[36m│\033[0m › ")
	m.writeSelected(&b, m.input, m.height-3, 4, "")
	b.WriteByte('\n')
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
	fmt.Fprintf(&b, "\033[36m└%s┘\033[0m  \033[2m%s  ↑↓/PgUp/PgDn/scroll  / commands\033[0m", strings.Repeat("─", width-2), state)
	return b.String()
}

func contextStatus(loop *agent.Loop, limit int) string {
	if limit < 1 {
		limit = config.Defaults().ContextTokens
	}
	used := loop.ApproxTokens()
	percent := used * 100 / limit
	return fmt.Sprintf("ctx ~%s/%s (%d%%) · processed %s", formatTokens(used), formatTokens(limit), percent, formatTokens(loop.InputTokens+loop.OutputTokens))
}

func formatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func (m appModel) writeLine(b *strings.Builder, line string, y int) {
	style := "\033[97m"
	if strings.HasPrefix(line, "·") || strings.HasPrefix(line, "└") || strings.HasPrefix(line, "queued") || strings.HasPrefix(line, "model set:") || strings.HasPrefix(line, "Loaded ") {
		style = "\033[2m"
	} else if strings.HasPrefix(line, "› ") {
		style = "\033[36m"
	}
	m.writeSelected(b, line, y, 0, style)
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

func wrappedTranscript(entries []string, width int) []string {
	var out []string
	for _, entry := range entries {
		for _, line := range strings.Split(entry, "\n") {
			runes := []rune(line)
			if len(runes) == 0 {
				out = append(out, "")
				continue
			}
			for len(runes) > width {
				cut := width
				for cut > width/2 && runes[cut] != ' ' {
					cut--
				}
				if cut == width/2 {
					cut = width
				}
				out = append(out, string(runes[:cut]))
				runes = []rune(strings.TrimSpace(string(runes[cut:])))
			}
			out = append(out, string(runes))
		}
	}
	return out
}

func authSource(p agent.Provider) string {
	if openai, ok := p.(*provider.OpenAICompatible); ok && openai.Responses {
		return "ChatGPT subscription"
	}
	if p.Name() == "copilot" {
		return "GitHub Copilot"
	}
	return "OpenAI API key"
}

func basePrompt(wd string) string {
	return fmt.Sprintf(`You are Atom, a careful terminal coding agent. Work in %s. Use tools to inspect before changing files. Keep changes scoped, run relevant tests, and report what changed. Never claim a command succeeded unless its output confirms it. Respect all AGENTS.md instructions.`, wd)
}
func fatal(s string) { fmt.Fprintln(os.Stderr, "atom:", s); os.Exit(1) }
