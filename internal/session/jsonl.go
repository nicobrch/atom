package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func New(workdir string) (*JSONL, error) {
	dir := filepath.Join(workdir, ".atom", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return Open(filepath.Join(dir, time.Now().Format("20060102-150405")+".jsonl"))
}

func Open(path string) (*JSONL, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
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
	var messages []agent.Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("invalid session record: %w", err)
		}
		if r.Type != "message" {
			continue
		}
		var m agent.Message
		if err := json.Unmarshal(r.Data, &m); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, sc.Err()
}
