package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicobrch/rivet/internal/agent"
)

func TestOpenAICompatibleStreamsTextAndTools(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\",\"tool_calls\":[{\"index\":0,\"id\":\"x\",\"function\":{\"name\":\"read\",\"arguments\":\"{\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"}\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	p := &OpenAICompatible{BaseURL: s.URL, Token: "test"}
	events, errs := p.Stream(context.Background(), agent.Request{Model: "test"})
	var got []agent.StreamEvent
	for e := range events {
		got = append(got, e)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].TextDelta != "hi" || got[1].ToolCallDelta.Name != "read" || got[3].FinishReason != "tool_calls" {
		t.Fatalf("unexpected events: %#v", got)
	}
}

func TestOpenAIKeyReadsCodexCredentialWhenEnvIsUnset(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "auth.json"), []byte(`{"OPENAI_API_KEY":"from-codex"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_HOME", d)
	key, err := OpenAIKey()
	if err != nil || key != "from-codex" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}
