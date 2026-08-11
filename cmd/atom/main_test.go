package main

import (
	"testing"

	"github.com/nicobrch/atom/internal/agent"
	"github.com/nicobrch/atom/internal/instructions"
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
		height:     10,
		transcript: []string{"hello world"},
		input:      "prompt",
		selecting:  true,
	}
	m.selectionFrom, m.selectionTo = mousePoint{0, 2}, mousePoint{5, 2}
	if got := m.selectionText(); got != "hello" {
		t.Fatalf("transcript selection = %q", got)
	}
	m.selectionFrom, m.selectionTo = mousePoint{4, 7}, mousePoint{10, 7}
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
