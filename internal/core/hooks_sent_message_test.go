package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tfielding/llamero/internal/toolbox"
)

// waitHookLog waits until the hook log has n entries: a run's entry is written just after its
// message is sent, so reading the log straight after reply() can come too early.
func waitHookLog(t *testing.T, b testBot, n int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		b.hooks.mu.Lock()
		got := len(b.hooks.log)
		b.hooks.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the hook log never reached %d entries", n)
}

// A sent run shows what the model sent, and how many pictures came with it, in /hooks. Runs that
// found nothing or were skipped show no such line.
func TestHooksSentMessageShown(t *testing.T) {
	b, m, l, ft := setupHooks(t, Hook{Name: "door", Task: "Check the door.", OnlyIf: "a person", Cooldown: time.Minute})
	ft.out = []toolbox.Image{{MIME: "image/jpeg", Data: []byte("jpg")}}
	l.caps = []string{"tools", "vision"}
	l.toolCall = call("get_state", map[string]any{"name": "front_door"})

	l.answer = "A person left a parcel."
	b.Hook(b.ctx, "door", "person on front_door", "front_door")
	reply(t, m)
	waitHookLog(t, b, 1)

	l.mu.Lock()
	l.answer = nothingToReport + " Only a car."
	l.mu.Unlock()
	b.Hook(b.ctx, "door", "car on laneway", "laneway")
	expectSilence(t, m)
	waitHookLog(t, b, 2)

	b.Hook(b.ctx, "door", "person on front_door again", "front_door") // cooling down
	expectSilence(t, m)
	waitHookLog(t, b, 3)

	b.Handle(dm(me, "/hooks"))
	got := reply(t, m)
	if want := "door [front_door]: sent\n  person on front_door\n  → A person left a parcel.  📷 1"; !strings.Contains(got, want) {
		t.Errorf("lacks %q:\n%s", want, got)
	}
	if n := strings.Count(got, "→"); n != 1 {
		t.Errorf("%d → lines, want 1 (only the sent run):\n%s", n, got)
	}
	for _, want := range []string{"nothing to report (Only a car)", "skipped: cooling down"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
}

// The message and picture count are saved with the hook state and shown after a restart.
func TestHooksSentMessageSurvivesRestart(t *testing.T) {
	b, m, l, ft := setupHooks(t, Hook{Name: "door", Task: "Check the door."})
	path := filepath.Join(t.TempDir(), "hooks.json")
	b.opt.HookStateFile = path
	ft.out = []toolbox.Image{{MIME: "image/jpeg", Data: []byte("jpg")}}
	l.caps = []string{"tools", "vision"}
	l.toolCall = call("get_state", map[string]any{"name": "front_door"})
	l.answer = "Parcel at the door."
	b.Hook(b.ctx, "door", "person on front_door", "front_door")
	reply(t, m)
	waitHookLog(t, b, 1)
	// The file is written just after the entry is added.
	for i := 0; i < 100; i++ {
		if data, _ := os.ReadFile(path); strings.Contains(string(data), `"answer"`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"answer": "Parcel at the door."`) ||
		!strings.Contains(string(data), `"images": 1`) {
		t.Fatalf("saved state (err %v):\n%s", err, data)
	}

	// A new bot, as after a restart, with the same file.
	again := New(m, l, b.store, Options{Allowed: b.opt.Allowed, DefaultModel: "llama3.2:latest", Hooks: b.opt.Hooks,
		Tools: b.opt.Tools, HookStateFile: path, TypingRefresh: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); again.Wait() })
	b2 := testBot{again, ctx}
	b2.Handle(dm(me, "/hooks"))
	if got := reply(t, m); !strings.Contains(got, "  → Parcel at the door.  📷 1") {
		t.Errorf("message lost after a restart:\n%s", got)
	}
}

// /hooks replay shows what the run sent before, next to the new decision.
func TestHooksReplayShowsSentMessage(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check the door."})
	l.answer = "Someone left a parcel."
	b.Hook(b.ctx, "door", "person on front_door", "")
	reply(t, m)
	waitHookLog(t, b, 1)

	l.mu.Lock()
	l.answer = "The parcel is still there."
	l.mu.Unlock()
	b.Handle(dm(me, "/hooks replay 1"))
	all := strings.Join(replies(t, m, 2), "\n")
	for _, want := range []string{
		`before: sent: "Someone left a parcel."`,                                  // the list of runs being checked
		"The parcel is still there.\n\nBefore: sent: \"Someone left a parcel.\".", // the new answer
	} {
		if !strings.Contains(all, want) {
			t.Errorf("lacks %q:\n%s", want, all)
		}
	}
}

// A long message is cut when it's kept, and its line breaks become spaces.
func TestHooksSentMessageCut(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check."})
	l.answer = strings.Repeat("word ", 150) + "\n\nsecond paragraph"
	b.Hook(b.ctx, "door", "event", "")
	reply(t, m)
	waitHookLog(t, b, 1)
	b.hooks.mu.Lock()
	got := b.hooks.log[len(b.hooks.log)-1].answer
	b.hooks.mu.Unlock()
	if strings.Contains(got, "\n") {
		t.Errorf("line breaks kept: %q", got)
	}
	// shorten cuts to hookKeepAnswer characters and adds "…".
	if n := utf8.RuneCountInString(got); n != hookKeepAnswer+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("kept %d characters, want %d plus the ellipsis", n, hookKeepAnswer)
	}
}
