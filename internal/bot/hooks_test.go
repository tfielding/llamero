package bot

import (
	"errors"
	"fmt"
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
	if !b.Hook(b.ctx, "camera_alert", "Alert 123 on front_door: person", "") {
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
	if b.Hook(b.ctx, "nope", "x", "") {
		t.Error("unknown hook accepted")
	}
}

func TestHookGroupsEvents(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "cams", Task: "Summarize.", Group: 100 * time.Millisecond})
	b.Hook(b.ctx, "cams", "person on driveway", "")
	b.Hook(b.ctx, "cams", "person on porch", "")
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
		b.Hook(b.ctx, "cams", "event", "")
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
	b.Hook(b.ctx, "door", "motion on front_door", "")
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
	b.Hook(b.ctx, "door", "motion on front_door", "")
	if got := reply(t, m); !strings.Contains(got, "A person is at the door.") {
		t.Errorf("got %q", got)
	}
}

func TestHookWithoutTask(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "leak", To: []string{me}})
	b.Hook(b.ctx, "leak", "Water under the sink", "")
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
	b.Hook(b.ctx, "door", "person on front_door", "")
	if got := reply(t, m); got != "🔔 door (not checked: Ollama is unreachable)\n\nperson on front_door" {
		t.Errorf("got %q", got)
	}
}

func TestNoAnswerAfterTools(t *testing.T) {
	// Chat: one empty answer after a tool call gets a second ask, which answers.
	b, m, l, ft := setupHooks(t, Hook{Name: "door", Task: "Check the door."})
	l.toolCall, l.blanks = call("get_state", map[string]any{"name": "porch"}), 1
	b.Handle(dm(me, "is anyone at the door?"))
	if got := reply(t, m); !strings.Contains(got, "echo: You used tools but wrote no answer. Answer now, in words") {
		t.Errorf("after one blank: %q", got)
	}
	if ft.called() != 1 {
		t.Errorf("tool called %d times", ft.called())
	}

	// Chat: blank twice, so the user gets the explanation.
	l.mu.Lock()
	l.blanks = 2
	l.mu.Unlock()
	b.Handle(dm(me, "and now?"))
	if got := reply(t, m); got != "Sorry, the model returned no answer after using its tools. Try asking again." {
		t.Errorf("after two blanks: %q", got)
	}

	// A hook doesn't pass that on: the event arrives, marked not checked.
	l.mu.Lock()
	l.blanks = 2
	l.mu.Unlock()
	b.Hook(b.ctx, "door", "person on front_door", "")
	if got := reply(t, m); got != "🔔 door (not checked: the model gave no answer)\n\nperson on front_door" {
		t.Errorf("hook: %q", got)
	}
}

func TestHookOnlyIfWithPictures(t *testing.T) {
	// The model looks at a snapshot to decide, then finds nothing to report: still silent.
	b, m, l, ft := setupHooks(t, Hook{Name: "door", Task: "Check the door.", OnlyIf: "a person is at the door"})
	ft.out = []toolbox.Image{{MIME: "image/jpeg", Data: []byte("jpg")}}
	l.toolCall, l.answer = call("get_state", map[string]any{"name": "front_door"}), "NOTHING_TO_REPORT. Only a car passed on the street."
	b.Hook(b.ctx, "door", "car on front_door", "")
	expectSilence(t, m)
	if ft.called() != 1 {
		t.Fatalf("snapshot tool called %d times", ft.called())
	}

	// When it does report, the picture comes with the message.
	l.mu.Lock()
	l.answer = "A person is at the door."
	l.mu.Unlock()
	b.Hook(b.ctx, "door", "person on front_door", "")
	if got := reply(t, m); !strings.Contains(got, "A person is at the door.") {
		t.Fatalf("got %q", got)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.attached) != 1 || m.attached[0] != "image/jpeg image-1.jpg" {
		t.Errorf("attached: %v", m.attached)
	}
}

func TestHookFeedback(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check the door.", OnlyIf: "a real person is there"})
	b.opt.LearnedDir = t.TempDir()
	l.answer = "A person is at the door."
	b.Hook(b.ctx, "door", "person on front_door", "")
	reply(t, m)

	// The user says it was wrong; the model saves it without asking for approval.
	l.mu.Lock()
	l.toolCall = call(toolHookFeedback, map[string]any{"hook": "door", "verdict": "false_alarm", "note": "it's the plant"})
	l.answer = "Noted."
	l.mu.Unlock()
	b.Handle(dm(me, "that was a false alarm, it's the plant"))
	if got := reply(t, m); got != "Noted." {
		t.Fatalf("got %q", got)
	}
	l.mu.Lock()
	offered := strings.Join(l.offered, ",")
	l.toolCall = nil
	l.mu.Unlock()
	if !strings.Contains(offered, toolHookFeedback) {
		t.Errorf("offered %s", offered)
	}
	lines := b.learned("door")
	if len(lines) != 1 || !strings.Contains(lines[0], `false alarm: reported, but it shouldn't have been. Event: person on front_door. `+
		`The check said: "A person is at the door.". The user: "it's the plant"`) {
		t.Fatalf("learned: %q", lines)
	}

	// The next check of that hook gets it.
	b.Hook(b.ctx, "door", "person on front_door", "")
	reply(t, m)
	l.mu.Lock()
	last := l.last[len(l.last)-1].Content
	l.mu.Unlock()
	if !strings.Contains(last, "The user's verdicts on earlier events from this hook") || !strings.Contains(last, "it's the plant") {
		t.Errorf("check prompt:\n%s", last)
	}

	// /learned lists and clears.
	b.Handle(dm(me, "/learned"))
	if got := reply(t, m); !strings.Contains(got, "door (1):\n- ") || !strings.Contains(got, "/learned clear <hook>") {
		t.Errorf("/learned: %q", got)
	}
	b.Handle(dm(me, "/learned clear door"))
	if got := reply(t, m); got != "Cleared what door had learned." || len(b.learned("door")) != 0 {
		t.Errorf("clear: %q", got)
	}

	// Mistakes the model can correct.
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"hook": "nope", "verdict": "correct"}, `there is no hook named "nope"`},
		{map[string]any{"hook": "door", "verdict": "meh"}, "verdict must be"},
	} {
		if out := b.callFeedbackTool(tc.args); !out.IsError || !strings.Contains(out.Text, tc.want) {
			t.Errorf("%v: %+v", tc.args, out)
		}
	}
	b.mu.Lock()
	delete(b.lastHook, "door")
	b.mu.Unlock()
	if out := b.callFeedbackTool(map[string]any{"hook": "door", "verdict": "missed"}); !out.IsError || !strings.Contains(out.Text, "say which event") {
		t.Errorf("no event: %+v", out)
	}
	if out := b.callFeedbackTool(map[string]any{"hook": "door", "verdict": "missed", "event": "the courier at 14:10"}); out.IsError {
		t.Errorf("missed with event: %+v", out)
	}
}

func TestLearnedKeepsNewest(t *testing.T) {
	b, _, _, _ := setupHooks(t, Hook{Name: "door", Task: "x"})
	b.opt.LearnedDir = t.TempDir()
	for i := 0; i < maxLearned+5; i++ {
		if err := b.addLearned("door", fmt.Sprintf("- entry %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	lines := b.learned("door")
	if len(lines) != maxLearned || lines[0] != "- entry 5" || lines[len(lines)-1] != fmt.Sprintf("- entry %d", maxLearned+4) {
		t.Errorf("%d lines, first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
	if got := b.learnedForCheck("door"); !strings.HasSuffix(got, fmt.Sprintf("- entry %d\n\n", maxLearned+4)) {
		t.Errorf("for check: %q", got)
	}
}

func TestHookCooldown(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "cams", Task: "Check it.", Cooldown: 300 * time.Millisecond})
	l.answer = "Someone is there."
	calls := func() int { l.mu.Lock(); defer l.mu.Unlock(); return l.calls }

	b.Hook(b.ctx, "cams", "person on front_door", "front_door")
	reply(t, m)
	// Same camera, during the cooldown: skipped without asking the model.
	b.Hook(b.ctx, "cams", "person on front_door again", "front_door")
	expectSilence(t, m)
	if n := calls(); n != 1 {
		t.Fatalf("model asked %d times during the cooldown", n)
	}
	// Another camera isn't cooling down.
	b.Hook(b.ctx, "cams", "person on garden", "garden")
	reply(t, m)
	// After it, the first camera is checked again.
	time.Sleep(350 * time.Millisecond)
	b.Hook(b.ctx, "cams", "person on front_door later", "front_door")
	reply(t, m)
	if n := calls(); n != 3 {
		t.Errorf("model asked %d times", n)
	}

	// A check that finds nothing doesn't start a cooldown.
	b2, m2, l2, _ := setupHooks(t, Hook{Name: "door", Task: "Check.", OnlyIf: "a person", Cooldown: time.Minute})
	l2.answer = "NOTHING_TO_REPORT"
	b2.Hook(b2.ctx, "door", "shadow", "front_door")
	expectSilence(t, m2)
	l2.mu.Lock()
	l2.answer = "A person."
	l2.mu.Unlock()
	b2.Hook(b2.ctx, "door", "person", "front_door")
	if got := reply(t, m2); !strings.Contains(got, "A person.") {
		t.Errorf("got %q", got)
	}
}

func TestHooksCommand(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "cams", Task: "Check.", OnlyIf: "a person", Cooldown: time.Minute})
	b.Handle(dm(me, "/hooks"))
	if got := reply(t, m); got != "No hook events since the bot started." {
		t.Errorf("empty: %q", got)
	}
	l.answer = "NOTHING_TO_REPORT. Only a car passing on the street."
	b.Hook(b.ctx, "cams", "car on laneway_south", "laneway_south")
	expectSilence(t, m)
	l.mu.Lock()
	l.answer = "Someone is at the door."
	l.mu.Unlock()
	b.Hook(b.ctx, "cams", "person on front_door", "front_door")
	reply(t, m)
	b.Hook(b.ctx, "cams", "person on front_door again", "front_door")

	b.Handle(dm(me, "/hooks"))
	got := reply(t, m)
	for _, want := range []string{
		"Latest hook checks, newest first:\n",
		"cams [front_door]: skipped: cooling down for 1m0s\n  person on front_door again\n",
		"cams [front_door]: sent\n  person on front_door\n",
		"cams [laneway_south]: nothing to report (Only a car passing on the street)\n  car on laneway_south\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "skipped") > strings.Index(got, "nothing to report") {
		t.Errorf("not newest first:\n%s", got)
	}
}

func TestHookLeanRun(t *testing.T) {
	b, m, l, ft := setupHooks(t,
		Hook{Name: "door", Task: "Check the door.", Model: "small-vision", Think: "off", Tools: []string{"get_state", "turn_off", "nope"}},
		Hook{Name: "plain", Task: "Check the door."})
	ft.tools = append(ft.tools, toolbox.Tool{Name: "get_weather", Schema: lightSchema, ReadOnly: true})
	l.answer = "Someone is there."
	b.Handle(dm(me, "remember the secret word is pineapple"))
	reply(t, m)

	// The hook's model, thinking and tools; no chat history.
	b.Hook(b.ctx, "door", "person on front_door", "")
	reply(t, m)
	l.mu.Lock()
	model, think, offered := l.model, l.think, strings.Join(l.offered, ",")
	var prompt strings.Builder
	for _, msg := range l.last {
		prompt.WriteString(msg.Content + "\n")
	}
	l.mu.Unlock()
	if model != "small-vision" || think != false || offered != "get_state" { // turn_off isn't read-only; nope doesn't exist
		t.Errorf("model %q, think %v, tools %q", model, think, offered)
	}
	if strings.Contains(prompt.String(), "pineapple") || !strings.Contains(prompt.String(), "person on front_door") {
		t.Errorf("prompt:\n%s", prompt.String())
	}

	// Without the settings: the chat's model and every read-only tool, still without history.
	b.Hook(b.ctx, "plain", "person on front_door", "")
	reply(t, m)
	l.mu.Lock()
	model, offered = l.model, strings.Join(l.offered, ",")
	prompt.Reset()
	for _, msg := range l.last {
		prompt.WriteString(msg.Content + "\n")
	}
	l.mu.Unlock()
	if model == "small-vision" || offered != "get_state,get_weather" || strings.Contains(prompt.String(), "pineapple") {
		t.Errorf("plain: model %q, tools %q, prompt:\n%s", model, offered, prompt.String())
	}

	// The results still go into the chat, for follow-ups.
	saved := func() bool {
		chat, _ := b.store.Load(me)
		for _, msg := range chat.History {
			if strings.Contains(msg.Content, "Event from plain") {
				return true
			}
		}
		return false
	}
	for i := 0; i < 50 && !saved(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !saved() {
		t.Error("hook result not in the chat's history")
	}
}
