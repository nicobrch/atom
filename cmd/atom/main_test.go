package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nicobrch/atom/internal/agent"
	"github.com/nicobrch/atom/internal/config"
	"github.com/nicobrch/atom/internal/instructions"
	"github.com/nicobrch/atom/internal/provider"
)

func TestCommandMatchesFiltersByTypedPrefix(t *testing.T) {
	got := commandMatches("/m")
	if len(got) != 2 || got[0] != "/model <id>" || got[1] != "/models" {
		t.Fatalf("/m matches %q", got)
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
	m := appModel{skills: []instructions.Skill{{Name: "review", Description: "Review code"}}}
	got, _ := m.submit("/skills")
	m = got.(appModel)
	if len(m.transcript) != 1 || m.transcript[0] != "review — Review code" {
		t.Fatalf("skills transcript = %q", m.transcript)
	}
	got, cmd := m.submit("/compact")
	m = got.(appModel)
	if !m.busy || cmd == nil || m.transcript[len(m.transcript)-1] != "· compacting" {
		t.Fatalf("compact state: busy=%v transcript=%q", m.busy, m.transcript)
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
	versionIndex := strings.Index(view, "atom 0.1.2")
	subscriptionIndex := strings.Index(view, "OpenAI API key")
	if versionIndex < 0 || subscriptionIndex < 0 {
		t.Fatalf("header metadata missing from view: %q", view[:100])
	}
	if strings.Count(view[:subscriptionIndex], "\n") <= strings.Count(view[:versionIndex], "\n") {
		t.Fatalf("subscription should be below version")
	}
	if strings.Contains(view, "Atom 0.1.2") {
		t.Fatalf("version label should use lowercase atom")
	}
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
