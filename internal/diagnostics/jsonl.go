// Package diagnostics writes metadata-only operational logs for Atom.
package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nicobrch/atom/internal/agent"
)

// JSONL appends one structured event per line. Files are rotated daily and are
// owner-readable only because provider diagnostics can still be sensitive.
type JSONL struct {
	dir  string
	day  string
	path string
	file *os.File
	mu   sync.Mutex
}

type record struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Component string    `json:"component"`
	agent.DiagnosticEvent
}

func New(workdir string) (*JSONL, error) {
	dir := filepath.Join(workdir, ".atom", "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	log := &JSONL{dir: dir}
	if err := log.rotate(); err != nil {
		return nil, err
	}
	return log, nil
}

func (l *JSONL) Path() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

func (l *JSONL) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

func (l *JSONL) WriteDiagnostic(event agent.DiagnosticEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.rotate(); err != nil {
		return err
	}
	event.DurationMS = event.Duration.Milliseconds()
	b, err := json.Marshal(record{
		Timestamp:       time.Now().UTC(),
		Level:           diagnosticLevel(event),
		Component:       "agent",
		DiagnosticEvent: event,
	})
	if err != nil {
		return err
	}
	_, err = l.file.Write(append(b, '\n'))
	return err
}

func (l *JSONL) rotate() error {
	day := time.Now().Format("2006-01-02")
	if l.file != nil && day == l.day {
		return nil
	}
	path := filepath.Join(l.dir, day+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	old := l.file
	l.path, l.day, l.file = path, day, f
	if old != nil {
		return old.Close()
	}
	return nil
}

func diagnosticLevel(event agent.DiagnosticEvent) string {
	if event.Failure != nil {
		return "error"
	}
	return "info"
}
