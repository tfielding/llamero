package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tfielding/llamero/internal/atomicfile"
	"github.com/tfielding/llamero/internal/ollama"
)

func TestStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	const me, other = "+15550000001", "+15550000002"

	// Nothing saved yet: empty chat, no error.
	c, err := s.Load(me)
	if err != nil || c.Model != "" || len(c.History) != 0 || c.Last != nil {
		t.Fatalf("missing chat: %+v %v", c, err)
	}

	// Save and read back.
	now := time.Date(2026, 11, 25, 9, 30, 15, 123456789, time.UTC)
	want := Chat{
		Model:      "gemma4:latest",
		Think:      "high",
		Summary:    "the user asked about alerts",
		Unanswered: "what time does the alarm go off?",
		History: []ollama.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello", Thinking: "let me think"},
			{Role: "tool", Content: `{"ok":true}`, ToolName: "alarm"},
			{Role: "assistant", Content: "9:00", Images: [][]byte{{1, 2, 3}}},
		},
		Last: &LastReply{
			Model: "gemma4:latest",
			At:    now,
			Stats: ollama.Stats{TotalDuration: time.Second, PromptEvalCount: 12, EvalCount: 34, Calls: 2},
		},
	}
	if err := s.Save(me, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(me)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != want.Model || got.Think != want.Think || got.Summary != want.Summary || got.Unanswered != want.Unanswered {
		t.Errorf("chat: %+v", got)
	}
	if len(got.History) != 4 {
		t.Fatalf("history: %+v", got.History)
	}
	if got.History[1].Thinking != "let me think" || got.History[2].ToolName != "alarm" || got.History[3].Images == nil || len(got.History[3].Images[0]) != 3 {
		t.Errorf("history: %+v", got.History)
	}
	if got.Last == nil || got.Last.Model != want.Last.Model || !got.Last.At.Equal(want.Last.At) {
		t.Errorf("last: %+v", got.Last)
	}
	if got.Last == nil || got.Last.Stats.Calls != 2 || got.Last.Stats.PromptEvalCount != 12 || got.Last.Stats.EvalCount != 34 {
		t.Errorf("stats: %+v", got.Last)
	}

	// Phone numbers don't appear as file names, and the temp file is gone.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || strings.Contains(entries[0].Name(), "555") || strings.HasSuffix(entries[0].Name(), ".tmp") {
		t.Errorf("files: %v", entries)
	}
	fi, err := os.Stat(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perms: %o", fi.Mode().Perm())
	}

	// Saving overwrites: a second save leaves one file, the last chat.
	want2 := Chat{Model: "other:model", History: []ollama.Message{{Role: "user", Content: "again"}}}
	if err := s.Save(me, want2); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Load(me)
	if got.Model != "other:model" || len(got.History) != 1 || got.History[0].Content != "again" {
		t.Errorf("after overwrite: %+v", got)
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("files: %v", entries)
	}

	// Chats are separate.
	if err := s.Save(other, Chat{Model: "other:model"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(me); got.Model != "other:model" {
		t.Errorf("me: %+v", got)
	}
	if got, _ := s.Load(other); got.Model != "other:model" || len(got.History) != 0 {
		t.Errorf("other: %+v", got)
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("files: %v", entries)
	}
}

func TestLoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	const me = "+15550000001"
	// Write garbage where the hashed path will be.
	if err := os.WriteFile(atomicfile.HashedPath(dir, me), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(me); err == nil {
		t.Fatal("corrupt file was not reported")
	}
}
