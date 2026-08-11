package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nicobrch/atom/internal/agent"
	"github.com/nicobrch/atom/internal/config"
	"github.com/nicobrch/atom/internal/instructions"
	"github.com/nicobrch/atom/internal/provider"
	"github.com/nicobrch/atom/internal/session"
)

func TestClipboardCommandUsesLinuxClipboardUtilities(t *testing.T) {
	tests := []struct {
		name      string
		available map[string]bool
		want      []string
	}{
		{name: "wayland", available: map[string]bool{"wl-copy": true}, want: []string{"wl-copy"}},
		{name: "x11 xclip", available: map[string]bool{"xclip": true}, want: []string{"xclip", "-selection", "clipboard"}},
		{name: "x11 xsel", available: map[string]bool{"xsel": true}, want: []string{"xsel", "--clipboard", "--input"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := clipboardCommand("linux", func(command string) (string, error) {
				if tt.available[command] {
					return "/usr/bin/" + command, nil
				}
				return "", errors.New("not found")
			})
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("clipboardCommand() = %q, %v; want %q, nil", got, err, tt.want)
			}
		})
	}
}

func TestClipboardCommandExplainsMissingLinuxUtility(t *testing.T) {
	_, err := clipboardCommand("linux", func(string) (string, error) { return "", errors.New("not found") })
	if err == nil || !strings.Contains(err.Error(), "install wl-clipboard") {
		t.Fatalf("clipboardCommand() error = %v", err)
	}
}

func TestCommandMatchesFiltersByTypedPrefix(t *testing.T) {
	got := commandMatches("/m")
	if len(got) != 1 || got[0] != "/model" {
		t.Fatalf("/m matches %q", got)
	}
}

func TestUpdateAtomPullsAndRebuildsGlobalInstallation(t *testing.T) {
	var calls [][]string
	run := func(dir, name string, args ...string) error {
		calls = append(calls, append([]string{dir, name}, args...))
		return nil
	}
	if err := updateAtom("/home/user/.atom", run); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"/home/user/.atom", "git", "pull", "--ff-only"},
		{"/home/user/.atom", "go", "build", "-o", "/home/user/.atom/atom", "./cmd/atom"},
	}
	if !slices.EqualFunc(calls, want, func(a, b []string) bool { return slices.Equal(a, b) }) {
		t.Fatalf("update commands = %q, want %q", calls, want)
	}
}

func TestUpdateAtomUsesSourceSubdirectory(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "source")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	run := func(dir, name string, args ...string) error {
		calls = append(calls, append([]string{dir, name}, args...))
		return nil
	}
	if err := updateAtom(home, run); err != nil {
		t.Fatal(err)
	}
	if calls[0][0] != source || calls[1][0] != source || calls[1][4] != filepath.Join(home, "atom") {
		t.Fatalf("update commands = %q", calls)
	}
}

func TestUpdateAtomStopsWhenPullFails(t *testing.T) {
	calls := 0
	err := updateAtom("/home/user/.atom", func(string, string, ...string) error {
		calls++
		return errors.New("not a git repository")
	})
	if err == nil || !strings.Contains(err.Error(), "pull update") || calls != 1 {
		t.Fatalf("updateAtom() = %v after %d calls", err, calls)
	}
}

func TestRemoveLegacyCopilotBundle(t *testing.T) {
	source := t.TempDir()
	dir := filepath.Join(source, "cmd", "atom")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "zcopilot_darwin_arm64.go")
	keep := filepath.Join(dir, "main.go")
	for _, path := range []string{legacy, keep} {
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeLegacyCopilotBundle(source); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy bundle remains: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated source removed: %v", err)
	}
}

func TestCommandsDoNotAdvertiseArguments(t *testing.T) {
	for _, command := range commands {
		if strings.ContainsAny(command, " <>[]") {
			t.Fatalf("command %q advertises arguments", command)
		}
	}
}

func TestArgumentBearingCommandsAreRejected(t *testing.T) {
	m := appModel{loop: &agent.Loop{}}
	for _, line := range []string{"/login openai", "/model gpt-5.4", "/skill review"} {
		got, _ := m.submit(line)
		m = got.(appModel)
		if m.transcript[len(m.transcript)-1] != "unsupported here: "+line {
			t.Fatalf("%q result = %q", line, m.transcript)
		}
	}
}

func TestWrappedTranscriptKeepsLongResponsesWithinViewport(t *testing.T) {
	lines := wrappedTranscript([]string{"one two three four five"}, 8)
	if len(lines) < 3 {
		t.Fatalf("lines=%q", lines)
	}
	for _, line := range lines {
		if len([]rune(line)) > 8 {
			t.Fatalf("wide line %q", line)
		}
	}
}

func TestRenderedTranscriptFormatsAssistantMarkdown(t *testing.T) {
	lines := renderedTranscript([]string{"## Recommended next steps\n\n1. **Add secret scanning** before every push.\n- [ ] Keep `.atom/logs/` ignored.\n> This is a note.\n\n```go\nfmt.Println(\"hello\")\n```"}, 80)
	got := make([]string, len(lines))
	for i, line := range lines {
		got[i] = line.text
	}
	want := []string{
		"Recommended next steps",
		"",
		"1. Add secret scanning before every push.",
		"☐ Keep .atom/logs/ ignored.",
		"│ This is a note.",
		"",
		`fmt.Println("hello")`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rendered lines = %q, want %q", got, want)
	}
	if lines[0].kind != markdownHeading || lines[4].kind != markdownQuote || lines[6].kind != markdownCode {

		t.Fatalf("Markdown styles = %#v", lines)
	}
}

func TestRenderedTranscriptLeavesUserMarkdownUntouched(t *testing.T) {
	lines := renderedTranscript([]string{"› explain **this**"}, 80)
	if len(lines) != 1 || lines[0].text != "› explain **this**" {
		t.Fatalf("user prompt was reformatted: %#v", lines)
	}
}

func TestClampScrollKeepsHistoryPosition(t *testing.T) {
	m := appModel{width: 20, height: 8, transcript: []string{"one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen"}, scroll: 1 << 30}
	m.clampScroll()
	if m.scroll == 0 || m.scroll >= 1<<30 {
		t.Fatalf("scroll=%d", m.scroll)
	}
}

func TestSelectionTextCopiesTranscriptAndInput(t *testing.T) {
	m := appModel{
		width:      40,
		height:     20,
		transcript: []string{"hello world"},
		input:      "prompt",
		selecting:  true,
	}
	m.selectionFrom, m.selectionTo = mousePoint{0, 6}, mousePoint{5, 6}
	if got := m.selectionText(); got != "hello" {
		t.Fatalf("transcript selection = %q", got)
	}
	m.selectionFrom, m.selectionTo = mousePoint{4, 16}, mousePoint{10, 16}
	if got := m.selectionText(); got != "prompt" {
		t.Fatalf("input selection = %q", got)
	}
}

func TestSkillsAndCompactCommandsAreHandled(t *testing.T) {
	m := appModel{skills: []instructions.Skill{{Name: "review", Description: "Review code", Scope: "repository", Path: "/repo/.agents/skills/review/SKILL.md"}}}
	got, _ := m.submit("/skills")
	m = got.(appModel)
	if len(m.transcript) != 1 || m.transcript[0] != "review — Review code (repository, /repo/.agents/skills/review/SKILL.md)" {
		t.Fatalf("skills transcript = %q", m.transcript)
	}
	got, cmd := m.submit("/compact")
	m = got.(appModel)
	if !m.busy || cmd == nil || m.turnCancel == nil || m.transcript[len(m.transcript)-1] != "· compacting" {
		t.Fatalf("compact state: busy=%v transcript=%q", m.busy, m.transcript)
	}
	m.turnCancel()
}

func TestClearWaitsForActiveTurn(t *testing.T) {
	m := appModel{busy: true, loop: &agent.Loop{Messages: []agent.Message{{Role: "user", Content: "keep"}}}}
	got, _ := m.submit("/clear")
	m = got.(appModel)
	if len(m.loop.Messages) != 1 || !strings.Contains(m.transcript[0], "wait for current turn") {
		t.Fatalf("messages=%#v transcript=%q", m.loop.Messages, m.transcript)
	}
}

func TestNewAndClonePreserveSessionHistory(t *testing.T) {
	wd := t.TempDir()
	store, err := session.New(wd)
	if err != nil {
		t.Fatal(err)
	}
	active := &resumableSession{store: store}
	messages := []agent.Message{{Role: "user", Content: "keep this"}, {Role: "assistant", Content: "kept"}}
	for _, message := range messages {
		if err := active.WriteEvent("message", message); err != nil {
			t.Fatal(err)
		}
	}
	original := active.Path()
	m := appModel{loop: &agent.Loop{Messages: messages}, session: active, wd: wd}
	got, _ := m.submit("/clone")
	m = got.(appModel)
	clone := active.Path()
	if clone == original {
		t.Fatal("clone did not switch session")
	}
	loaded, err := session.LoadMessages(clone)
	if err != nil || !reflect.DeepEqual(loaded, messages) {
		t.Fatalf("cloned messages=%#v error=%v", loaded, err)
	}
	got, _ = m.submit("/new")
	m = got.(appModel)
	if active.Path() == clone || len(m.loop.Messages) != 0 || len(m.history) != 0 {
		t.Fatalf("new session path=%q messages=%#v history=%q", active.Path(), m.loop.Messages, m.history)
	}
	loaded, err = session.LoadMessages(original)
	if err != nil || !reflect.DeepEqual(loaded, messages) {
		t.Fatalf("original messages=%#v error=%v", loaded, err)
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResumeLoadsSelectedSessionAndMakesItActive(t *testing.T) {
	wd := t.TempDir()
	current, err := session.New(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	previousPath := filepath.Join(wd, ".atom", "sessions", "previous.jsonl")
	previous, err := session.Open(previousPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.WriteEvent("message", agent.Message{Role: "user", Content: "resume this"}); err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}

	m := appModel{loop: &agent.Loop{}, session: &resumableSession{store: current}, wd: wd}
	got, _ := m.submit("/resume")
	m = got.(appModel)
	if m.menuKind != "resume" || len(m.menu) != 1 {
		t.Fatalf("resume menu = %q, %q", m.menuKind, m.menu)
	}
	for i, entry := range m.sessionHistory {
		if entry.Path == previousPath {
			m.selected = i
			break
		}
	}
	got, _ = m.menuKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(appModel)
	if m.session.Path() != previousPath || len(m.loop.Messages) != 1 || m.loop.Messages[0].Content != "resume this" {
		t.Fatalf("resume state = path %q messages %#v", m.session.Path(), m.loop.Messages)
	}
	if len(m.transcript) < 2 || m.transcript[0] != "› resume this" {
		t.Fatalf("resumed transcript = %q", m.transcript)
	}
	if _, err := os.Stat(current.Path()); !os.IsNotExist(err) {
		t.Fatalf("empty initial session should be removed, stat error = %v", err)
	}
}

func TestTranscriptForMessagesRestoresConversationAndToolActivity(t *testing.T) {
	lines := transcriptForMessages([]agent.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "Hi", ToolCalls: []agent.ToolCall{{Name: "bash"}}},
		{Role: "tool", Content: "done"},
	})
	want := []string{"› hello", "", "Hi", "└─ bash  done"}
	if !slices.Equal(lines, want) {
		t.Fatalf("transcript = %q, want %q", lines, want)
	}
}

func TestLiveAssistantResponseStartsOnASeparateLine(t *testing.T) {
	m := appModel{transcript: []string{"› hello"}}
	got, _ := m.Update(uiText("Hi"))
	m = got.(appModel)
	if !slices.Equal(m.transcript, []string{"› hello", "", "Hi"}) {
		t.Fatalf("live transcript = %q", m.transcript)
	}
}

func TestResumePickerUsesVerticalBoundedNavigation(t *testing.T) {
	m := appModel{menuKind: "resume", menu: []string{"first", "second", "third"}, width: 80, height: 9}
	got, _ := m.menuKey(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(appModel)
	if m.selected != 1 {
		t.Fatalf("down selected %d, want 1", m.selected)
	}
	got, _ = m.menuKey(tea.KeyMsg{Type: tea.KeyEnd})
	m = got.(appModel)
	got, _ = m.menuKey(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(appModel)
	if m.selected != 2 {
		t.Fatalf("navigation should stop at last item, selected %d", m.selected)
	}
	view := m.View()
	if !strings.Contains(view, "Resume session") || !strings.Contains(view, "\n   second\n") || !strings.Contains(view, "\n\033[7m › third") || !strings.Contains(view, "↑ more sessions") {
		t.Fatalf("resume picker should render rows vertically: %q", view)
	}
}

func TestModelPickerUsesVerticalBoundedNavigation(t *testing.T) {
	m := appModel{menuKind: "model", menu: []string{"first", "second", "third"}, width: 80, height: 9}
	got, _ := m.menuKey(tea.KeyMsg{Type: tea.KeyEnd})
	m = got.(appModel)
	got, _ = m.menuKey(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(appModel)
	if m.selected != 2 {
		t.Fatalf("navigation should stop at last item, selected %d", m.selected)
	}
	view := m.View()
	if !strings.Contains(view, "Select model") || !strings.Contains(view, "\n   second\n") || !strings.Contains(view, "\n\033[7m › third") || !strings.Contains(view, "↑ more models") {
		t.Fatalf("model picker should render rows vertically: %q", view)
	}
}

func TestSkillToolUsesProgressiveDisclosure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: review\ndescription: Review code\n---\nFull instructions"), 0644); err != nil {
		t.Fatal(err)
	}
	output, err := (skillTool{skills: []instructions.Skill{{Name: "review", Path: path}}}).Run(context.Background(), json.RawMessage(`{"name":"review"}`))
	if err != nil || !strings.Contains(output, "Full instructions") {
		t.Fatalf("load_skill output = %q, err = %v", output, err)
	}
}

func TestBasePromptOnlySuppliesOperationalContext(t *testing.T) {
	prompt := basePrompt("/workspace")
	if !strings.Contains(prompt, "working in /workspace") || !strings.Contains(prompt, "never claim a tool ran") || !strings.Contains(prompt, "load_skill") {
		t.Fatalf("base prompt is missing required operational context: %q", prompt)
	}
	if strings.Contains(prompt, "careful terminal coding agent") {
		t.Fatalf("base prompt retains the removed persona: %q", prompt)
	}
}

func TestJSONObserverIncludesToolResultAndError(t *testing.T) {
	var output bytes.Buffer
	observer := &jsonObserver{encoder: json.NewEncoder(&output)}
	call := agent.ToolCall{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"false"}`)}
	observer.ToolStart(call)
	observer.ToolEnd(call, "stderr", errors.New("exit status 1"))
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"type":"tool_start"`) || !strings.Contains(lines[1], `"output":"stderr"`) || !strings.Contains(lines[1], `"error":"exit status 1"`) {
		t.Fatalf("events = %q", lines)
	}
}

func TestToolCallLabelShowsTargetWithoutWriteContent(t *testing.T) {
	label := toolCallLabel(agent.ToolCall{Name: "write", Arguments: json.RawMessage(`{"path":"note.txt","content":"private contents"}`)})
	if label != "write  note.txt" || strings.Contains(label, "private") {
		t.Fatalf("label = %q", label)
	}
}

func TestUIObserverKeepsFailedToolOutput(t *testing.T) {
	events := make(chan tea.Msg, 1)
	uiObserver{events: events}.ToolEnd(agent.ToolCall{}, "useful stderr\nError: exit status 1", errors.New("exit status 1"))
	if event := (<-events).(uiTool); !strings.Contains(event.output, "useful stderr") {
		t.Fatalf("output = %q", event.output)
	}
}

func TestContextStatusShowsCurrentUsageAndProcessedTokens(t *testing.T) {
	loop := &agent.Loop{
		Messages:     []agent.Message{{Content: "1234567890123"}},
		InputTokens:  1200,
		OutputTokens: 300,
	}
	if got := contextStatus(loop, 10); got != "ctx ~4/10 (40%) · processed 1.5k" {
		t.Fatalf("context status = %q", got)
	}
}

func TestFooterPartsPinsContextToRightEdge(t *testing.T) {
	left, gap, right := footerParts("openai  ·  gpt-5.6  ·  xhigh", "ctx ~1.5k/128.0k (1%) · processed 2.0k", 80)
	if left != "openai  ·  gpt-5.6  ·  xhigh" || right != "ctx ~1.5k/128.0k (1%) · processed 2.0k" {
		t.Fatalf("footer labels = %q, %q", left, right)
	}
	if len([]rune(left+gap+right)) != 80 {
		t.Fatalf("footer width = %d, want 80", len([]rune(left+gap+right)))
	}
}

func TestFooterPartsKeepsContextInNarrowTerminal(t *testing.T) {
	left, gap, right := footerParts("openai  ·  gpt-5.6  ·  xhigh", "ctx ~1.5k/128.0k", 20)
	if len([]rune(left+gap+right)) != 20 {
		t.Fatalf("footer width = %d, want 20", len([]rune(left+gap+right)))
	}
	if right != "ctx ~1.5k/128.0k" {
		t.Fatalf("context label = %q", right)
	}
}

func TestViewStacksLowercaseVersionAndSubscriptionBesideLogo(t *testing.T) {
	m := appModel{
		width:  100,
		height: 30,
		loop:   &agent.Loop{Provider: unavailableProvider{}},
		cfg:    config.Defaults(),
	}
	view := m.View()
	versionIndex := strings.Index(view, "atom 0.3.1")
	subscriptionIndex := strings.Index(view, "Sign in required")
	if versionIndex < 0 || subscriptionIndex < 0 {
		t.Fatalf("header metadata missing from view: %q", view[:100])
	}
	if strings.Count(view[:subscriptionIndex], "\n") <= strings.Count(view[:versionIndex], "\n") {
		t.Fatalf("subscription should be below version")
	}
	if strings.Contains(view, "Atom 0.3.0") {
		t.Fatalf("version label should use lowercase atom")
	}
}

func TestViewUsesSelectedModelsContextLimit(t *testing.T) {
	m := appModel{
		width: 100, height: 30,
		loop:   &agent.Loop{Provider: unavailableProvider{}, Model: "large-context", Messages: []agent.Message{{Content: "hello"}}},
		models: []provider.Model{{ID: "large-context", ContextTokens: 1000000}},
	}
	if !strings.Contains(m.View(), "ctx ~2/1.0m") {
		t.Fatalf("view did not show model context window: %q", m.View())
	}
}

func TestConversationRequiresLogin(t *testing.T) {
	m := appModel{loop: &agent.Loop{Provider: unavailableProvider{}}}
	got, cmd := m.submit("hello")
	m = got.(appModel)
	if cmd != nil || m.busy || len(m.transcript) != 1 || !strings.Contains(m.transcript[0], "sign in required") {
		t.Fatalf("unavailable provider started a conversation: %#v", m)
	}
}

func TestEscapeCancelsActiveTurn(t *testing.T) {
	cancelled := false
	m := appModel{busy: true, turnCancel: func() { cancelled = true }}
	got, _ := m.inputKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = got.(appModel)
	if !cancelled || m.transcript[len(m.transcript)-1] != "· cancelling turn" {
		t.Fatalf("cancelled=%v transcript=%q", cancelled, m.transcript)
	}
}

func TestSubmittingTurnStoresCancellationBeforeReturningModel(t *testing.T) {
	m := appModel{ctx: context.Background(), loop: &agent.Loop{Provider: &provider.OpenAICompatible{}, Model: "test"}}
	got, cmd := m.submit("hello")
	m = got.(appModel)
	if cmd == nil || !m.busy || m.turnCancel == nil {
		t.Fatalf("command=%v busy=%v cancel=%v", cmd, m.busy, m.turnCancel)
	}
	m.turnCancel()
}

func TestBusySubmissionDoesNotBlockWhenSteeringBufferIsFull(t *testing.T) {
	steering := make(chan string, 1)
	steering <- "already queued"
	m := appModel{busy: true, steering: steering, loop: &agent.Loop{Provider: &provider.OpenAICompatible{}, Model: "test"}}
	got, _ := m.submit("follow up")
	m = got.(appModel)
	if !slices.Equal(m.queue, []string{"follow up"}) {
		t.Fatalf("queue = %q", m.queue)
	}
}

func TestQueuedFallbackDoesNotDuplicateVisiblePrompt(t *testing.T) {
	m := appModel{
		ctx: context.Background(), busy: true, queue: []string{"follow up"}, transcript: []string{"› follow up"},
		loop: &agent.Loop{Provider: &provider.OpenAICompatible{}, Model: "test"},
	}
	got, cmd := m.Update(turnDone{err: errors.New("failed")})
	m = got.(appModel)
	if cmd == nil || !m.busy || strings.Count(strings.Join(m.transcript, "\n"), "› follow up") != 1 {
		t.Fatalf("busy=%v transcript=%q", m.busy, m.transcript)
	}
	m.turnCancel()
}

func TestHorizontalMenuUsesLeftAndRightArrows(t *testing.T) {
	m := appModel{menuKind: "login-provider", menu: []string{"openai", "copilot"}}

	got, _ := m.menuKey(tea.KeyMsg{Type: tea.KeyRight})
	m = got.(appModel)
	if m.selected != 1 {
		t.Fatalf("right selected %d, want 1", m.selected)
	}

	got, _ = m.menuKey(tea.KeyMsg{Type: tea.KeyLeft})
	m = got.(appModel)
	if m.selected != 0 {
		t.Fatalf("left selected %d, want 0", m.selected)
	}
}

func TestEmptyLoadingMenuIgnoresNavigation(t *testing.T) {
	m := appModel{menuKind: "effort-loading"}

	got, _ := m.menuKey(tea.KeyMsg{Type: tea.KeyRight})
	m = got.(appModel)
	if m.menuKind != "effort-loading" {
		t.Fatalf("menu kind = %q, want effort-loading", m.menuKind)
	}
}

func TestSelectingModelOpensItsEffortPicker(t *testing.T) {
	m := appModel{
		loop:     &agent.Loop{},
		wd:       t.TempDir(),
		menuKind: "model",
		menu:     []string{"gpt-5.4"},
		models: []provider.Model{{
			ID:      "gpt-5.4",
			Efforts: []string{"low", "high"},
		}},
	}

	got, _ := m.menuKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(appModel)
	if m.loop.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4", m.loop.Model)
	}
	if m.menuKind != "effort" || m.menuTitle != "Select effort" {
		t.Fatalf("menu = %q/%q, want Select effort/effort", m.menuTitle, m.menuKind)
	}
	if len(m.menu) != 2 || m.menu[0] != "low" || m.menu[1] != "high" {
		t.Fatalf("effort options = %q", m.menu)
	}
}

func TestNormalizedEffortClearsUnsupportedSavedValue(t *testing.T) {
	models := []provider.Model{{ID: "gpt-4.1"}, {ID: "reasoning", DefaultEffort: "medium", Efforts: []string{"low", "medium"}}}
	if got := normalizedEffort("gpt-4.1", "xhigh", models); got != "" {
		t.Fatalf("non-reasoning effort = %q", got)
	}
	if got := normalizedEffort("reasoning", "xhigh", models); got != "medium" {
		t.Fatalf("reasoning fallback = %q", got)
	}
	if got := normalizedEffort("catalog-alias", "xhigh", models); got != "" {
		t.Fatalf("unknown-model effort = %q", got)
	}
}

func TestInputEditorMovesCaretAndReplacesSelection(t *testing.T) {
	m := appModel{width: 80, input: "hello world", inputCursor: 11, inputAnchor: 11}

	got, _ := m.inputKey(tea.KeyMsg{Type: tea.KeyLeft})
	m = got.(appModel)
	if m.inputCursor != 10 || m.inputAnchor != 10 {
		t.Fatalf("left cursor = %d/%d, want 10/10", m.inputCursor, m.inputAnchor)
	}

	got, _ = m.inputKey(tea.KeyMsg{Type: tea.KeyCtrlShiftLeft})
	m = got.(appModel)
	if m.inputCursor != 6 || m.inputAnchor != 10 {
		t.Fatalf("word selection = %d/%d, want 6/10", m.inputCursor, m.inputAnchor)
	}
	got, _ = m.inputKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Atom")})
	m = got.(appModel)
	if m.input != "hello Atomd" || m.inputCursor != 10 || m.inputAnchor != 10 {
		t.Fatalf("selection replacement = %q at %d/%d", m.input, m.inputCursor, m.inputAnchor)
	}
}

func TestInputEditorDeletesWordsAndForwardCharacters(t *testing.T) {
	m := appModel{width: 80, input: "hello world", inputCursor: 11, inputAnchor: 11}

	got, _ := m.inputKey(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
	m = got.(appModel)
	if m.input != "hello " || m.inputCursor != 6 {
		t.Fatalf("word delete = %q at %d", m.input, m.inputCursor)
	}
	got, _ = m.inputKey(tea.KeyMsg{Type: tea.KeyLeft})
	m = got.(appModel)
	got, _ = m.inputKey(tea.KeyMsg{Type: tea.KeyDelete})
	m = got.(appModel)
	if m.input != "hello" || m.inputCursor != 5 {
		t.Fatalf("forward delete = %q at %d", m.input, m.inputCursor)
	}
}

func TestComposerWrapsLongInputIntoVerticalRows(t *testing.T) {
	m := appModel{
		width:       20,
		height:      30,
		input:       "abcdefghijklmnopqrstuv",
		inputCursor: len([]rune("abcdefghijklmnopqrstuv")),
		inputAnchor: len([]rune("abcdefghijklmnopqrstuv")),
		loop:        &agent.Loop{Provider: unavailableProvider{}},
		cfg:         config.Defaults(),
	}
	lines := m.inputLines()
	if len(lines) != 2 || lines[0].text != "abcdefghijkl" || lines[1].text != "mnopqrstuv" {
		t.Fatalf("input lines = %#v", lines)
	}
	if m.composerRows() != 2 {
		t.Fatalf("composer rows = %d, want 2", m.composerRows())
	}
	view := m.View()
	if !strings.Contains(view, "│\033[0m › abcdefghijkl") || !strings.Contains(view, "│\033[0m   mnopqrstuv") {
		t.Fatalf("composer did not render vertical rows: %q", view)
	}
}

func TestAltEnterInsertsNewlineWithoutSubmitting(t *testing.T) {
	m := appModel{width: 80, input: "one", inputCursor: 3, inputAnchor: 3}
	got, cmd := m.inputKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = got.(appModel)
	if cmd != nil || m.input != "one\n" || m.inputCursor != 4 {
		t.Fatalf("input=%q cursor=%d command=%v", m.input, m.inputCursor, cmd)
	}
}

func TestAltEnterQueuesFollowUpWhileBusy(t *testing.T) {
	m := appModel{busy: true, width: 80, input: "after everything", inputCursor: 16, inputAnchor: 16}
	got, cmd := m.inputKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = got.(appModel)
	if cmd != nil || m.input != "" || !slices.Equal(m.followUps, []string{"after everything"}) || m.transcript[len(m.transcript)-1] != "queued follow-up (1)" {
		t.Fatalf("input=%q follow-ups=%q transcript=%q", m.input, m.followUps, m.transcript)
	}
}

func TestFollowUpStartsOnlyAfterTurnCompletes(t *testing.T) {
	m := appModel{
		ctx: context.Background(), busy: true, followUps: []string{"after everything"},
		loop: &agent.Loop{Provider: &provider.OpenAICompatible{}, Model: "test"},
	}
	got, cmd := m.Update(turnDone{})
	m = got.(appModel)
	if cmd == nil || !m.busy || len(m.followUps) != 0 || m.turnCancel == nil {
		t.Fatalf("busy=%v follow-ups=%q cancel=%v", m.busy, m.followUps, m.turnCancel)
	}
	m.turnCancel()
}

func TestPromptHistoryNavigatesAndRestoresDraft(t *testing.T) {
	m := appModel{width: 80, input: "draft", inputCursor: 5, inputAnchor: 5, history: []string{"first", "second"}, historyPos: 2}
	got, _ := m.inputKey(tea.KeyMsg{Type: tea.KeyUp})
	m = got.(appModel)
	if m.input != "second" {
		t.Fatalf("first history input = %q", m.input)
	}
	got, _ = m.inputKey(tea.KeyMsg{Type: tea.KeyUp})
	m = got.(appModel)
	if m.input != "first" {
		t.Fatalf("second history input = %q", m.input)
	}
	got, _ = m.inputKey(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(appModel)
	got, _ = m.inputKey(tea.KeyMsg{Type: tea.KeyDown})
	m = got.(appModel)
	if m.input != "draft" || m.inputCursor != 5 {
		t.Fatalf("restored draft = %q at %d", m.input, m.inputCursor)
	}
}

func TestPromptHistoryExcludesCompactionSummary(t *testing.T) {
	history := promptHistory([]agent.Message{{Role: "user", Content: "one"}, {Role: "user", Content: "Session handoff summary:\nsummary"}, {Role: "assistant", Content: "reply"}})
	if !slices.Equal(history, []string{"one"}) {
		t.Fatalf("history = %q", history)
	}
}

func TestWrappedUserPromptKeepsUserStyleOnContinuationLines(t *testing.T) {
	lines := wrappedTranscript([]string{"› one two three four"}, 8)
	if len(lines) < 2 {
		t.Fatalf("wrapped user prompt = %q", lines)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "› ") {
			t.Fatalf("user continuation lost its prefix: %q", lines)
		}
	}
}

func TestComposerAndTranscriptStayWithinTerminalHeight(t *testing.T) {
	m := appModel{
		width:       80,
		height:      30,
		input:       strings.Repeat("x", 320),
		inputCursor: 320,
		inputAnchor: 320,
		transcript:  []string{strings.Repeat("response ", 100)},
		thinking:    true,
		loop:        &agent.Loop{Provider: unavailableProvider{}},
		cfg:         config.Defaults(),
	}
	if rows := strings.Count(m.View(), "\n") + 1; rows > m.height {
		t.Fatalf("view has %d rows in a %d-row terminal", rows, m.height)
	}
}

func TestInputEditorMouseDragSelectsAndRendersCaret(t *testing.T) {
	m := appModel{
		width:  80,
		height: 20,
		input:  "hello world",
		loop:   &agent.Loop{Provider: unavailableProvider{}},
		cfg:    config.Defaults(),
	}
	inputY := m.height - 4
	got, _ := m.Update(tea.MouseMsg{X: 4, Y: inputY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = got.(appModel)
	got, _ = m.Update(tea.MouseMsg{X: 9, Y: inputY, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	m = got.(appModel)
	got, _ = m.Update(tea.MouseMsg{X: 9, Y: inputY, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease})
	m = got.(appModel)
	if from, until := m.inputSelection(); from != 0 || until != 5 {
		t.Fatalf("mouse selection = %d:%d, want 0:5", from, until)
	}
	if !strings.Contains(m.View(), "\033[7mh\033[0m") {
		t.Fatal("input selection should be visibly highlighted")
	}
}

func TestViewUsesWorkingStarburstIndicator(t *testing.T) {
	m := appModel{
		width:         80,
		height:        20,
		thinking:      true,
		thinkingFrame: 3,
		loop:          &agent.Loop{Provider: unavailableProvider{}},
		cfg:           config.Defaults(),
	}
	view := m.View()
	if !strings.Contains(view, "working...") {
		t.Fatalf("working indicator missing from view: %q", view)
	}
	if strings.Contains(view, "thinking") || strings.Contains(view, "✦") || strings.Contains(view, "⠁") || strings.Contains(view, "•") {
		t.Fatalf("working indicator should use a simple dot animation: %q", view)
	}
}
