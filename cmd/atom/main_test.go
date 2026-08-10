package main

import "testing"

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
