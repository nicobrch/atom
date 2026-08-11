package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEditRequiresOneMatchAndStaysInWorkspace(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "x.txt"), []byte("one one"), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(d, time.Second)
	if _, err := r.Run(context.Background(), "edit", []byte(`{"path":"x.txt","old_text":"one","new_text":"two"}`)); err == nil {
		t.Fatal("expected ambiguous edit error")
	}
	if _, err := r.Run(context.Background(), "read", []byte(`{"path":"../secret"}`)); err == nil {
		t.Fatal("expected escape error")
	}
}

func TestToolsRejectSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	workspace, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(workspace, time.Second)
	if _, err := r.Run(context.Background(), "read", []byte(`{"path":"escape/secret"}`)); err == nil {
		t.Fatal("expected symlink escape error")
	}
	if _, err := r.Run(context.Background(), "write", []byte(`{"path":"escape/new","content":"nope"}`)); err == nil {
		t.Fatal("expected symlink write escape error")
	}
}

func TestReadSupportsLineRanges(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "large.txt"), []byte("one\ntwo\nthree\nfour\n"), 0644); err != nil {
		t.Fatal(err)
	}
	output, err := NewRegistry(workspace, time.Second).Run(context.Background(), "read", []byte(`{"path":"large.txt","offset":2,"limit":2}`))
	if err != nil || !strings.HasPrefix(output, "two\nthree\n") || !strings.Contains(output, "lines 2-3") {
		t.Fatalf("output = %q, error = %v", output, err)
	}
}

func TestBoundedOutputKeepsHeadAndTail(t *testing.T) {
	var output boundedOutput
	input := strings.Repeat("a", maxOutputBytes/2) + strings.Repeat("x", 100) + strings.Repeat("z", maxOutputBytes/2)
	for start := 0; start < len(input); start += 997 {
		end := min(start+997, len(input))
		if _, err := output.Write([]byte(input[start:end])); err != nil {
			t.Fatal(err)
		}
	}
	got := output.String()
	if !strings.HasPrefix(got, strings.Repeat("a", maxOutputBytes/2)) || !strings.HasSuffix(got, strings.Repeat("z", maxOutputBytes/2)) || !strings.Contains(got, fmt.Sprintf("truncated (%d bytes total)", len(input))) || strings.Contains(got, strings.Repeat("x", 100)) {
		t.Fatalf("bounded output did not retain expected head/tail: %d bytes", len(got))
	}
}

func TestBashTimeoutKillsBackgroundChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process groups")
	}
	started := time.Now()
	_, err := NewRegistry(t.TempDir(), 100*time.Millisecond).Run(context.Background(), "bash", []byte(`{"command":"sleep 10 & wait"}`))
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("error=%v duration=%s", err, time.Since(started))
	}
}
