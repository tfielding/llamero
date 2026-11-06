package bot

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tfielding/signal-ollama/internal/toolbox"
)

func setupHooks(t *testing.T, hooks ...Hook) (testBot, *fakeMsg, *fakeLLM, *fakeTools) {
	t.Helper()
	b, m, l, _ := setup(t)
	b.opt.Hooks = map[string]Hook{}
	for _, h := range hooks {
		b.opt.Hooks[h.Name] = h
	}
	ft := &fakeTools{tools: []toolbox.Tool{
		{Name: "turn_off", Schema: lightSchema, Confirm: true},
		{Name: "get_state", Schema: lightSchema, ReadOnly: true},
	}}
	b.opt.Tools = ft
	l.caps = []string{"tools"}
	return b, m, l, ft
}

func TestHookChecked(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "camera_alert", Task: "Say what happened at the door."})
	l.answer = "Someone left a parcel at the door."
	if !b.Hook(b.ctx, "camera_alert", "Alert 123 on front_door: person") {
		t.Fatal("hook not found")
	}
	if got := reply(t, m); got != "🔔 **camera_alert**\n\nSomeone left a parcel at the door." {
		t.Fatalf("got %q", got)
	}
	l.mu.Lock()
	offered, last := l.offered, l.last[len(l.last)-1].Content
	l.mu.Unlock()
	if strings.Join(offered, ",") != "get_state" {
		t.Errorf("offered %v: only read-only tools", offered)
	}
	for _, want := range []string{`through the hook "camera_alert"`, "not a new message from them", "What happened:\nAlert 123 on front_door: person\n",
		"Do this now, using your tools to look things up: Say what happened at the door.", "not take actions"} {
		if !strings.Contains(last, want) {
			t.Errorf("prompt lacks %q:\n%s", want, last)
		}
	}
	if strings.Contains(last, nothingToReport) {
		t.Error("NOTHING_TO_REPORT offered without only_if")
	}
	// It's in the chat, so "show me that" works next.
	chat, _ := b.store.Load(me)
	for i := 0; i < 50 && len(chat.History) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		chat, _ = b.store.Load(me)
	}
	if n := len(chat.History); n != 2 || chat.History[0].Content != "(Event from camera_alert: Alert 123 on front_door: person)" ||
		chat.History[1].Content != "Someone left a parcel at the door." {
		t.Errorf("history: %+v", chat.History)
	}
	if b.Hook(b.ctx, "nope", "x") {
		t.Error("unknown hook accepted")
	}
}

func TestHookGroupsEvents(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "cams", Task: "Summarize.", Group: 100 * time.Millisecond})
	b.Hook(b.ctx, "cams", "person on driveway")
	b.Hook(b.ctx, "cams", "person on porch")
	reply(t, m)
	expectSilence(t, m)
	l.mu.Lock()
	calls, last := l.calls, l.last[len(l.last)-1].Content
	l.mu.Unlock()
	if calls != 1 || !strings.Contains(last, "What happened:\n- person on driveway\n- person on porch\n") {
		t.Errorf("%d calls, prompt:\n%s", calls, last)
	}

	// Past the cap, events are counted, not listed.
	for i := 0; i < maxHookEvents+3; i++ {
		b.Hook(b.ctx, "cams", "event")
	}
	reply(t, m)
	l.mu.Lock()
	last = l.last[len(l.last)-1].Content
	l.mu.Unlock()
	if strings.Count(last, "- event") != maxHookEvents || !strings.Contains(last, "(and 3 more)") {
		t.Errorf("prompt:\n%s", last)
	}
}

func TestHookOnlyIf(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check the door.", OnlyIf: "a person is at the door"})
	l.answer = "NOTHING_TO_REPORT"
	b.Hook(b.ctx, "door", "motion on front_door")
	expectSilence(t, m)
	l.mu.Lock()
	calls, last := l.calls, l.last[len(l.last)-1].Content
	l.answer = "A person is at the door."
	l.mu.Unlock()
	if calls != 1 || !strings.Contains(last, "Only report if this is true: a person is at the door. If it isn't, reply with exactly NOTHING_TO_REPORT") {
		t.Fatalf("%d calls, prompt:\n%s", calls, last)
	}
	if chat, _ := b.store.Load(me); len(chat.History) != 0 {
		t.Errorf("nothing to report was saved: %+v", chat.History)
	}
	b.Hook(b.ctx, "door", "motion on front_door")
	if got := reply(t, m); !strings.Contains(got, "A person is at the door.") {
		t.Errorf("got %q", got)
	}
}

func TestHookWithoutTask(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "leak", To: []string{me}})
	b.Hook(b.ctx, "leak", "Water under the sink")
	if got := reply(t, m); got != "🔔 leak\n\nWater under the sink" {
		t.Errorf("got %q", got)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls != 0 {
		t.Error("model asked for a hook without a task")
	}
}

func TestHookOllamaDown(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check the door."})
	l.pingErr = errors.New("connection refused")
	b.Hook(b.ctx, "door", "person on front_door")
	if got := reply(t, m); got != "🔔 door (not checked: Ollama is unreachable)\n\nperson on front_door" {
		t.Errorf("got %q", got)
	}
}
