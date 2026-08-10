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
	if len(os.Args) >= 2 && os.Args[1] == "auth" {
		runAuth(os.Args[2:])
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
		fatal(err.Error())
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

func runAuth(args []string) {
	if len(args) != 1 || args[0] != "openai" {
		fatal("usage: atom auth openai")
	}
	cmd := exec.Command("codex", "--login")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fatal("Codex sign-in failed: " + err.Error())
	}
	if _, err := provider.OpenAIKey(); err != nil {
		fatal("Codex sign-in completed, but Atom cannot use its credential: " + err.Error())
	}
	fmt.Println("OpenAI credential available to Atom.")
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

func (t registryTool) Definition() agent.ToolDefinition { return t.d }
func (t registryTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	return t.r.Run(ctx, t.d.Name, args)
}

type plain struct{ out io.Writer }

func (p *plain) Text(s string)                               { fmt.Fprint(p.out, s) }
func (p *plain) ToolStart(agent.ToolCall)                    {}
func (p *plain) ToolEnd(_ agent.ToolCall, _ string, _ error) {}
func (p *plain) Status(string)                               {}

func runInteractive(ctx context.Context, loop *agent.Loop, skills []instructions.Skill, sessionPath string, cfg config.Config) {
	s := bufio.NewScanner(os.Stdin)
	s.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for {
		fmt.Print("\033[32m› \033[0m")
		if !s.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		switch {
		case line == "/exit" || line == "/quit":
			return
		case line == "/help":
			fmt.Println("/compact  /clear  /session  /skills  /skill <name>  /exit")
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
