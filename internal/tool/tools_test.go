package tool

import (
	"context"
	"os"
	"path/filepath"
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
