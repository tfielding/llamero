package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tfielding/llamero/internal/knowledge"
	"github.com/tfielding/llamero/internal/toolbox"
)

func TestKnowledgeReachesTheModel(t *testing.T) {
	b, m, l, _ := setup(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "home.md"), []byte("Our car is a blue hatchback."), 0o644)
	os.WriteFile(filepath.Join(dir, "frigate.md"), []byte("Use get_events_grid for pictures."), 0o644)
	b.opt.Knowledge = knowledge.New(dir, map[string][]string{"frigate.md": {"frigate"}}, 1000)
	ft := &fakeTools{tools: []toolbox.Tool{{Name: "get_events", Schema: lightSchema, ReadOnly: true}}}
	b.opt.Tools = ft
	l.caps = []string{"tools"}

	b.Handle(dm(me, "any cars today?"))
	reply(t, m)
	l.mu.Lock()
	msgs := l.last
	l.mu.Unlock()
	// System prompt, then the notes, then tool guidance, then the conversation.
	if len(msgs) < 4 || msgs[0].Content != "sys" || msgs[1].Role != "system" ||
		!strings.HasSuffix(msgs[1].Content, "Our car is a blue hatchback.") || !strings.Contains(msgs[2].Content, "You have tools") ||
		msgs[3].Role != "user" {
		t.Fatalf("messages: %+v", msgs)
	}
	ft.mu.Lock()
	notes := ft.notes
	ft.mu.Unlock()
	if notes["frigate"] != "Use get_events_grid for pictures." {
		t.Fatalf("server notes passed to guidance: %v", notes)
	}

	b.Handle(dm(me, "/knowledge"))
	got := reply(t, m)
	for _, want := range []string{"Notes in " + dir, "- home.md: 28 characters, always", "- frigate.md: 33 characters, with the frigate tools",
		"Total 61 of 1000 characters"} {
		if !strings.Contains(got, want) {
			t.Errorf("/knowledge lacks %q:\n%s", want, got)
		}
	}
	b.Handle(dm(me, "/status"))
	if got := reply(t, m); !strings.Contains(got, "Knowledge: 2 files, 61 characters") {
		t.Errorf("/status:\n%s", got)
	}
}

func TestNoKnowledge(t *testing.T) {
	b, m, l, _ := setup(t)
	b.opt.Knowledge = knowledge.New(filepath.Join(t.TempDir(), "missing"), nil, 1000)
	b.Handle(dm(me, "hi"))
	reply(t, m)
	l.mu.Lock()
	n := len(l.last)
	l.mu.Unlock()
	if n != 2 { // system prompt + message
		t.Fatalf("%d messages", n)
	}
	b.Handle(dm(me, "/knowledge"))
	if got := reply(t, m); !strings.Contains(got, "No notes yet. Put Markdown files in") {
		t.Errorf("/knowledge: %s", got)
	}
	b.Handle(dm(me, "/status"))
	if got := reply(t, m); !strings.Contains(got, "Knowledge: none") {
		t.Errorf("/status: %s", got)
	}
}
