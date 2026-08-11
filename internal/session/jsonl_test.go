package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicobrch/atom/internal/agent"
)

func TestJSONLPersistsErrorsWithoutChangingConversationReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteEvent("message", agent.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteEvent("error", agent.DiagnosticEvent{Event: "request_failed", RequestID: "atom_test", Failure: &agent.ProviderError{Stage: "stream", Code: "rate_limit", Message: "try again"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	messages, err := LoadMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Content != "hello" {
		t.Fatalf("replayed messages = %#v", messages)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("permissions = %o, want 0600", got)
	}
}

func TestHistorySortsSessionsAndShowsLatestUserPrompt(t *testing.T) {
	wd := t.TempDir()
	olderPath := filepath.Join(wd, ".atom", "sessions", "older.jsonl")
	older, err := Open(olderPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := older.WriteEvent("message", agent.Message{Role: "user", Content: "older prompt"}); err != nil {
		t.Fatal(err)
	}
	if err := older.Close(); err != nil {
		t.Fatal(err)
	}
	olderTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(olderPath, olderTime, olderTime); err != nil {
		t.Fatal(err)
	}

	newerPath := filepath.Join(wd, ".atom", "sessions", "newer.jsonl")
	newer, err := Open(newerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := newer.WriteEvent("message", agent.Message{Role: "assistant", Content: "reply"}); err != nil {
		t.Fatal(err)
	}
	if err := newer.WriteEvent("message", agent.Message{Role: "user", Content: "latest\n prompt"}); err != nil {
		t.Fatal(err)
	}
	if err := newer.Close(); err != nil {
		t.Fatal(err)
	}

	history, err := History(wd)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Path != newerPath || history[0].Preview != "latest prompt" {
		t.Fatalf("history = %#v", history)
	}
}

func TestLoadMessagesUsesCompactionAsTheNewConversationBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind string
		msg  agent.Message
	}{
		{"message", agent.Message{Role: "user", Content: "before"}},
		{"message", agent.Message{Role: "assistant", Content: "before reply"}},
		{"compaction", agent.Message{Role: "user", Content: "Session handoff summary: summary"}},
		{"message", agent.Message{Role: "assistant", Content: "after"}},
	} {
		if err := store.WriteEvent(event.kind, event.msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	messages, err := LoadMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content != "Session handoff summary: summary" || messages[1].Content != "after" {
		t.Fatalf("messages after compaction = %#v", messages)
	}
}
