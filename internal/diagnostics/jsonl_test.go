package diagnostics

import (
	"os"
	"strings"
	"testing"

	"github.com/nicobrch/atom/internal/agent"
)

func TestJSONLWritesMetadataOnlyOwnerOnlyLog(t *testing.T) {
	log, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if err := log.WriteDiagnostic(agent.DiagnosticEvent{
		Event: "request_failed", RequestID: "atom_test", Provider: "openai", Model: "test",
		MessageCount: 3, ToolCount: 5, Failure: &agent.ProviderError{Stage: "stream", Code: "rate_limit", Message: "try again"},
	}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, `"request_id":"atom_test"`) || !strings.Contains(text, `"code":"rate_limit"`) {
		t.Fatalf("missing diagnostic fields: %s", text)
	}
	if strings.Contains(text, "content") || strings.Contains(text, "prompt") || strings.Contains(text, "authorization") {
		t.Fatalf("diagnostic log should not contain transcript or credentials: %s", text)
	}
	info, err := os.Stat(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("permissions = %o, want 0600", got)
	}
}
