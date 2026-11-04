package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxPerChat caps how many schedules one chat can have.
const MaxPerChat = 20

// Item is one schedule.
type Item struct {
	ID      int       `json:"id"`
	When    string    `json:"when"`              // in Spec.Saved form
	Task    string    `json:"task"`              // what to check, in the user's words
	OnlyIf  string    `json:"only_if,omitempty"` // report only if this is true; empty = always report
	Wake    bool      `json:"wake,omitempty"`    // wake the Ollama machine if it's asleep, instead of skipping
	Paused  bool      `json:"paused,omitempty"`
	Failing bool      `json:"failing,omitempty"` // the last run failed and the user was told; cleared when one works
	Created time.Time `json:"created"`
	LastRun time.Time `json:"last_run,omitzero"`
	Next    time.Time `json:"next,omitzero"` // zero = no more runs (a one-off that's done)
}

// Spec reads the item's schedule back.
func (it Item) Spec(loc *time.Location) (Spec, error) {
	return reload(it.When, time.Now().In(loc))
}

// File is one chat's schedules.
type File struct {
	Chat   string `json:"chat"` // who the results go to (the chat's Signal number)
	NextID int    `json:"next_id"`
	Items  []Item `json:"items"`
}

// Find returns the item with id.
func (f *File) Find(id int) (*Item, bool) {
	for i := range f.Items {
		if f.Items[i].ID == id {
			return &f.Items[i], true
		}
	}
	return nil, false
}

// Remove deletes the item with id, reporting whether it was there.
func (f *File) Remove(id int) bool {
	for i := range f.Items {
		if f.Items[i].ID == id {
			f.Items = append(f.Items[:i], f.Items[i+1:]...)
			return true
		}
	}
	return false
}

// Add adds a schedule, numbering it and working out its first run.
func (f *File) Add(spec Spec, task, onlyIf string, wake bool, now time.Time) (Item, error) {
	if len(f.Items) >= MaxPerChat {
		return Item{}, fmt.Errorf("there are already %d schedules; delete one first", MaxPerChat)
	}
	if f.NextID == 0 {
		f.NextID = 1
	}
	it := Item{ID: f.NextID, When: spec.Saved(), Task: strings.TrimSpace(task), OnlyIf: strings.TrimSpace(onlyIf),
		Wake: wake, Created: now, Next: spec.Next(now, time.Time{})}
	f.NextID++
	f.Items = append(f.Items, it)
	return it, nil
}

// Store keeps each chat's schedules in its own file in dir. File names are hashed, like chat
// history, so phone numbers aren't file names. All changes go through Update, under one lock.
type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Get returns chat's schedules (none if it has no file).
func (s *Store) Get(chat string) (File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(chat)
}

// Update loads chat's schedules, applies fn and saves them if fn succeeds.
func (s *Store) Update(chat string, fn func(*File) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load(chat)
	if err != nil {
		return err
	}
	if err := fn(&f); err != nil {
		return err
	}
	return s.save(chat, f)
}

// All returns every chat's schedules, by chat.
func (s *Store) All() (map[string]File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := map[string]File{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var f File
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err == nil {
			err = json.Unmarshal(b, &f)
		}
		if err != nil || f.Chat == "" {
			slog.Warn("schedule file unreadable, skipped", "file", e.Name(), "err", err)
			continue
		}
		out[f.Chat] = f
	}
	return out, nil
}

func (s *Store) load(chat string) (File, error) {
	f := File{Chat: chat}
	b, err := os.ReadFile(s.path(chat))
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	f.Chat = chat
	return f, nil
}

func (s *Store) save(chat string, f File) error {
	if len(f.Items) == 0 {
		err := os.Remove(s.path(chat))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	sort.Slice(f.Items, func(i, j int) bool { return f.Items[i].ID < f.Items[j].ID })
	f.Chat = chat
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(chat) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(chat))
}

func (s *Store) path(chat string) string {
	h := sha256.Sum256([]byte(chat))
	return filepath.Join(s.dir, hex.EncodeToString(h[:8])+".json")
}
