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

const version = "0.1.0"

type terminal struct {
	out      io.Writer
	lineOpen bool
}

func (t *terminal) Text(s string) { fmt.Fprint(t.out, s); t.lineOpen = true }
func (t *terminal) ToolStart(c agent.ToolCall) {
	if t.lineOpen {
		fmt.Fprintln(t.out)
		t.lineOpen = false
	}
	fmt.Fprintf(t.out, "\033[36m• %s\033[0m ", c.Name)
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
}
func (t *terminal) Status(s string) {
	if s == "thinking" {
		fmt.Fprint(t.out, "\033[2m… \033[0m")
	} else if s == "done" {
		fmt.Fprintln(t.out)
	}
	t.lineOpen = false
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
	loop := &agent.Loop{Provider: p, Model: cfg.Model, Tools: toolsAsInterface(tool.NewRegistry(wd, time.Duration(cfg.BashTimeoutSeconds)*time.Second)), System: system, Sink: store, Observer: obs}
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
	fmt.Printf("\033[1mAtom %s\033[0m  %s/%s  %s\n", version, p.Name(), cfg.Model, wd)
	if len(agentFiles) > 0 {
		fmt.Printf("\033[2mLoaded %d AGENTS.md file(s); session %s\033[0m\n", len(agentFiles), store.Path())
	}
	runInteractive(ctx, loop, skills, store.Path(), cfg)
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
	"/model <id>", "/models", "/session", "/skill <name>", "/skills",
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

func runInteractive(ctx context.Context, loop *agent.Loop, skills []instructions.Skill, sessionPath string, cfg config.Config) {
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
			fmt.Printf("current: %s\nuse: /model <id>\n", loop.Model)
			continue
		case strings.HasPrefix(line, "/model "):
			loop.Model = strings.TrimSpace(strings.TrimPrefix(line, "/model "))
			if loop.Model == "" {
				fmt.Fprintln(os.Stderr, "usage: /model <id>")
			} else {
				fmt.Println("model", loop.Model)
			}
			continue
		case line == "/model":
			fmt.Printf("current: %s\nuse: /model <id>\n", loop.Model)
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
			continue
		}
		if loop.ApproxTokens() > int(float64(cfg.ContextTokens)*cfg.AutoCompactAt) {
			fmt.Println("\033[2mCompacting context…\033[0m")
			if err := loop.Compact(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				continue
			}
		}
		if err := loop.Prompt(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

func basePrompt(wd string) string {
	return fmt.Sprintf(`You are Atom, a careful terminal coding agent. Work in %s. Use tools to inspect before changing files. Keep changes scoped, run relevant tests, and report what changed. Never claim a command succeeded unless its output confirms it. Respect all AGENTS.md instructions.`, wd)
}
func fatal(s string) { fmt.Fprintln(os.Stderr, "atom:", s); os.Exit(1) }
