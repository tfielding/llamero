// Package store persists per-chat state as JSON files.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/tfielding/llamero/internal/atomicfile"
	"github.com/tfielding/llamero/internal/ollama"
)

type Chat struct {
	Model   string `json:"model,omitempty"`   // empty = default model
	Think   string `json:"think,omitempty"`   // "" (model default), on, off, low, medium, high
	Summary string `json:"summary,omitempty"` // older messages, condensed by the model
	// Unanswered is the last message that got no answer (error, offline, /stop), for /retry.
	Unanswered string           `json:"unanswered,omitempty"`
	History    []ollama.Message `json:"history"`
	Last       *LastReply       `json:"last,omitempty"` // timings of the most recent reply, for /stats
}

type LastReply struct {
	Model string       `json:"model"`
	At    time.Time    `json:"at"`
	Stats ollama.Stats `json:"stats"`
}

type Store struct{ dir string }

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Load returns the chat for id, or an empty chat if none is saved.
func (s *Store) Load(id string) (Chat, error) {
	var c Chat
	b, err := os.ReadFile(atomicfile.HashedPath(s.dir, id))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// Save writes the chat atomically and durably.
func (s *Store) Save(id string, c Chat) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(atomicfile.HashedPath(s.dir, id), b, 0o600)
}
