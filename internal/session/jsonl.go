package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nicobrch/atom/internal/agent"
)

type Record struct {
	Type string          `json:"type"`
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"`
}

type JSONL struct {
	path string
	file *os.File
}

// HistoryEntry is a resumable session in a workspace's local history.
type HistoryEntry struct {
	Path     string
	Modified time.Time
	Preview  string
}

func New(workdir string) (*JSONL, error) {
	dir := filepath.Join(workdir, ".atom", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	return Open(filepath.Join(dir, time.Now().Format("20060102-150405.000000000")+".jsonl"))
}

func Open(path string) (*JSONL, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	return &JSONL{path: path, file: f}, nil
}

func (s *JSONL) Path() string { return s.path }
func (s *JSONL) Close() error { return s.file.Close() }

func (s *JSONL) WriteEvent(kind string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	r := Record{Type: kind, At: time.Now().UTC(), Data: b}
	b, err = json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.file.Write(append(b, '\n'))
	return err
}

func LoadMessages(path string) ([]agent.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	allowPartialLast := false
	if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
		last := []byte{0}
		if _, readErr := f.ReadAt(last, info.Size()-1); readErr == nil {
			allowPartialLast = last[0] != '\n'
		}
	}
	var messages []agent.Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var pending []byte
	load := func(line []byte) error {
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("invalid session record: %w", err)
		}
		switch r.Type {
		case "clear":
			messages = nil
		case "message":
			var m agent.Message
			if err := json.Unmarshal(r.Data, &m); err != nil {
				return err
			}
			messages = append(messages, m)
		case "compaction":
			var summary agent.Message
			if err := json.Unmarshal(r.Data, &summary); err != nil {
				return err
			}
			messages = []agent.Message{summary}
		}
		return nil
	}
	for sc.Scan() {
		if pending != nil {
			if err := load(pending); err != nil {
				return nil, err
			}
		}
		pending = append(pending[:0], sc.Bytes()...)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		if err := load(pending); err != nil {
			// Crash during append can leave one partial final JSON object. Earlier
			// records remain durable and sufficient to resume.
			if allowPartialLast {
				return messages, nil
			}
			return nil, err
		}
	}
	return messages, nil
}

// History returns workspace sessions from newest to oldest. A short preview of
// the most recent user message makes the picker useful without reading session
// contents into the UI.
func History(workdir string) ([]HistoryEntry, error) {
	dir := filepath.Join(workdir, ".atom", "sessions")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	history := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		messages, err := LoadMessages(path)
		if err != nil {
			return nil, fmt.Errorf("read session %s: %w", path, err)
		}
		preview := "(empty session)"
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" && strings.TrimSpace(messages[i].Content) != "" {
				preview = strings.Join(strings.Fields(messages[i].Content), " ")
				break
			}
		}
		history = append(history, HistoryEntry{Path: path, Modified: info.ModTime(), Preview: preview})
	}
	slices.SortFunc(history, func(a, b HistoryEntry) int { return b.Modified.Compare(a.Modified) })
	return history, nil
}
