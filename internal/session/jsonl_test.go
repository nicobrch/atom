package session

import (
	"os"
	"path/filepath"
	"testing"

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
