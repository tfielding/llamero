package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tfielding/llamero/internal/toolbox"
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

	// Only a note about pictures is no answer either: no title over bare pictures.
	l.mu.Lock()
	l.answer = "[2 images were sent to the user with this reply.]"
	l.mu.Unlock()
	b.Hook(b.ctx, "door", "door left open", "")
	if got := reply(t, m); got != "🔔 door (not checked: the model gave no answer)\n\ndoor left open" {
		t.Errorf("image note only: %q", got)
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
	if got := reply(t, m); got != "No hook events yet." {
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

// /hooks replay <number> checks an earlier run again and tells only whoever asked, next to what
// it decided before. Nothing else changes: no cooldown, no new entry in /hooks.
func TestHooksReplay(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "cams", Task: "Check.", OnlyIf: "a person", Cooldown: time.Minute})
	b.opt.Location = time.UTC
	l.answer = nothingToReport + " Only a car."
	b.Hook(b.ctx, "cams", "car on laneway_south", "laneway_south")
	expectSilence(t, m)
	b.hooks.mu.Lock()
	at := b.clock(b.hooks.log[0].at)
	b.hooks.mu.Unlock()

	l.mu.Lock()
	l.answer = "A person by the car."
	l.mu.Unlock()
	b.Handle(dm(me, "/hooks replay #1 7"))
	got := replies(t, m, 2)
	if !strings.HasPrefix(got[0], "Checking again with the hooks' current settings. The answers come only to you:\n") ||
		!strings.Contains(got[0], "\n#1 "+at+" cams [laneway_south], before: nothing to report (Only a car)") ||
		!strings.Contains(got[0], "\n#7: not kept any more") {
		t.Errorf("reply:\n%s", got[0])
	}
	if want := "🔔 **cams** (replay of #1 from " + at + ", only to you)\n\nA person by the car.\n\nBefore: nothing to report (Only a car)."; got[1] != want {
		t.Errorf("replay:\n%q\nwant\n%q", got[1], want)
	}
	b.hooks.mu.Lock()
	entries := len(b.hooks.log)
	_, cooling := b.hooks.cooldowns["cams\x00laneway_south"]
	b.hooks.mu.Unlock()
	if entries != 1 || cooling {
		t.Errorf("the replay changed things: %d entries, cooling %v", entries, cooling)
	}

	l.mu.Lock()
	l.answer = nothingToReport + " Still a car."
	l.mu.Unlock()
	b.Handle(dm(me, "/hooks replay 1"))
	if got := replies(t, m, 2); got[1] != "cams #1 ("+at+"): nothing to report (Still a car). Before: nothing to report (Only a car)." {
		t.Errorf("nothing to report: %q", got[1])
	}

	for in, want := range map[string]string{
		"/hooks replay x":    "/hooks replay takes numbers from /hooks",
		"/hooks replay 3-1":  "/hooks replay takes numbers from /hooks",
		"/hooks replay 1-11": "At most 10 runs at a time",
	} {
		b.Handle(dm(me, in))
		if got := reply(t, m); !strings.HasPrefix(got, want) {
			t.Errorf("%s: %q", in, got)
		}
	}
}

func TestParseRunNumbers(t *testing.T) {
	got, err := parseRunNumbers([]string{"#3", "5-7", "6", "#9-#10"})
	if err != nil || !slices.Equal(got, []int{3, 5, 6, 7, 9, 10}) {
		t.Errorf("got %v, %v", got, err)
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

func TestHookTitle(t *testing.T) {
	for _, tc := range []struct {
		title, note string
		bold        bool
		want        string
	}{
		{"", "", true, "🔔 **door**\n\nbody"},
		{"", "not checked: x", false, "🔔 door (not checked: x)\n\nbody"},
		{"📷 Camera", "", true, "**📷 Camera**\n\nbody"},
		{"📷 Camera", "not checked: x", false, "📷 Camera (not checked: x)\n\nbody"},
		{"none", "", true, "body"},
		{"None", "not checked: x", false, "(not checked: x)\n\nbody"},
	} {
		h := Hook{Name: "door", Title: tc.title}
		if got := h.titled(tc.note, "body", tc.bold); got != tc.want {
			t.Errorf("title %q note %q: got %q, want %q", tc.title, tc.note, got, tc.want)
		}
	}

	// A checked message uses it.
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check the door.", Title: "📷 Camera"})
	l.answer = "Someone is there."
	b.Hook(b.ctx, "door", "person on front_door", "")
	if got := reply(t, m); got != "**📷 Camera**\n\nSomeone is there." {
		t.Errorf("got %q", got)
	}
}

func TestHookToGroups(t *testing.T) {
	const group = "group.ZmVkY2JhOTg3NjU0MzIxMA=="
	b, m, l, _ := setupHooks(t, Hook{Name: "cams", Task: "Check it.", Groups: []string{group}})
	l.answer = "Someone is at the door."
	b.Hook(b.ctx, "cams", "person on front_door", "front_door")
	reply(t, m)
	expectSilence(t, m) // only the group: To is empty, but that's not "everyone allowed" with a group
	m.mu.Lock()
	sent := m.sent[len(m.sent)-1]
	m.mu.Unlock()
	if sent != group+": 🔔 **cams**\n\nSomeone is at the door." {
		t.Errorf("sent %q", sent)
	}
	time.Sleep(50 * time.Millisecond)
	if chat, _ := b.store.Load(me); len(chat.History) != 0 {
		t.Errorf("my history: %+v", chat.History)
	}
	if chat, _ := b.store.Load(group); len(chat.History) != 0 {
		t.Errorf("a group got a conversation: %+v", chat.History)
	}

	// A group and a person: the person's snooze doesn't mute the group, and only the person
	// gets the alert in their conversation.
	b2, m2, l2, _ := setupHooks(t, Hook{Name: "cams", Task: "Check it.", To: []string{me}, Groups: []string{group}})
	l2.answer = "Someone is at the door."
	b2.Hook(b2.ctx, "cams", "person on front_door", "front_door")
	reply(t, m2)
	reply(t, m2)
	chat, _ := b2.store.Load(me)
	for i := 0; i < 50 && len(chat.History) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		chat, _ = b2.store.Load(me)
	}
	if len(chat.History) != 2 {
		t.Errorf("my history: %+v", chat.History)
	}
	b2.Handle(dm(me, "/snooze 1h"))
	reply(t, m2)
	b2.Hook(b2.ctx, "cams", "person on garden", "garden")
	reply(t, m2)
	expectSilence(t, m2)
	m2.mu.Lock()
	sent = m2.sent[len(m2.sent)-1]
	m2.mu.Unlock()
	if !strings.HasPrefix(sent, group+": ") {
		t.Errorf("sent %q, want it only to the group", sent)
	}
}

func TestHookMissingTools(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check.", Tools: []string{"get_state", "turn_off", "getstate"}})
	l.answer = "All quiet."
	b.CheckHookTools(b.ctx) // only logs
	b.Hook(b.ctx, "door", "door left open", "")
	reply(t, m)
	l.mu.Lock()
	offered, last := l.offered, l.last[len(l.last)-1].Content
	l.mu.Unlock()
	if strings.Join(offered, ",") != "get_state" {
		t.Errorf("offered %v", offered)
	}
	if !strings.Contains(last, "(Not available right now: getstate, turn_off. Don't try to call them") {
		t.Errorf("the check isn't told what's missing:\n%s", last)
	}
}

func TestToolProblems(t *testing.T) {
	all := []toolbox.Tool{{Name: "get_state", ReadOnly: true}, {Name: "turn_off"}, {Name: "home__GetLiveContext", ReadOnly: true}}
	got := toolProblems(all, []string{"turn_off", "getstate", "GetLiveContext", "nope"})
	want := []string{
		"turn_off (not read-only, so checks can't use it; if it only looks things up, add it to its server's read_only)",
		"getstate (no such tool; did you mean get_state?)",
		"GetLiveContext (no such tool; did you mean home__GetLiveContext?)",
		"nope (no such tool, its server is down, or allow_write: false hides it)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s", strings.Join(got, "\n"))
	}
}

// replies collects n messages, in whatever order they arrive: a command's reply and the run it
// queued can race.
func replies(t *testing.T, m *fakeMsg, n int) []string {
	t.Helper()
	out := make([]string, n)
	for i := range out {
		out[i] = reply(t, m)
	}
	sort.Strings(out)
	return out
}

func TestHooksReplayMissed(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check the door.", Cooldown: time.Hour})
	b.opt.Location = time.UTC
	l.pingErr = errors.New("connection refused")
	b.Hook(b.ctx, "door", "person on front_door", "front_door")
	if got := reply(t, m); got != "🔔 door (not checked: Ollama is unreachable)\n\nperson on front_door" {
		t.Fatalf("got %q", got)
	}
	b.Handle(dm(me, "/hooks"))
	if got := reply(t, m); !strings.Contains(got, "\n1 run went out unchecked: /hooks replay checks it now and sends what it finds.") {
		t.Fatalf("/hooks:\n%s", got)
	}
	b.hooks.mu.Lock()
	at := b.clock(b.hooks.missed[0].run.missedAt)
	cooling := b.hooks.cooldowns["door\x00front_door"] // from the unchecked message
	b.hooks.mu.Unlock()

	// Still down: it stays on the list, and the one who asked hears so.
	b.Handle(dm(me, "/hooks replay"))
	got := replies(t, m, 2)
	if !strings.HasPrefix(got[0], "Checking 1 missed hook run again, oldest first:\n\n"+at+" door [front_door]: Ollama is unreachable") ||
		got[1] != "door (events from "+at+"): still couldn't check: Ollama is unreachable. /hooks replay tries again." {
		t.Fatalf("retry while down: %q", got)
	}

	// Back up: checked and sent like any check, marked as late. The old events don't start a
	// new cooldown, which would hold back new ones.
	l.mu.Lock()
	l.pingErr, l.answer = nil, "Someone left a parcel."
	l.mu.Unlock()
	b.Handle(dm(me, "/hooks replay"))
	got = replies(t, m, 2)
	if !strings.HasPrefix(got[0], "Checking 1 missed hook run again") ||
		got[1] != "🔔 **door** (rechecked, events from "+at+")\n\nSomeone left a parcel." {
		t.Fatalf("retry when up: %q", got)
	}
	b.hooks.mu.Lock()
	after := b.hooks.cooldowns["door\x00front_door"]
	b.hooks.mu.Unlock()
	if !after.Equal(cooling) {
		t.Errorf("the recheck moved the cooldown from %v to %v", cooling, after)
	}
	b.Handle(dm(me, "/hooks replay"))
	if got := reply(t, m); !strings.HasPrefix(got, "Nothing went out unchecked.") {
		t.Fatalf("nothing left: %q", got)
	}
	b.Handle(dm(me, "/hooks"))
	if got := reply(t, m); !strings.Contains(got, "door [front_door]: rechecked: sent") || strings.Contains(got, "went out unchecked:") {
		t.Fatalf("/hooks after the retry:\n%s", got)
	}
}

func TestHooksReplayMissedNothingToReport(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check the door.", OnlyIf: "someone is at the door"})
	l.pingErr = errors.New("connection refused")
	b.Hook(b.ctx, "door", "motion on front_door", "")
	reply(t, m)
	l.mu.Lock()
	l.pingErr, l.answer = nil, nothingToReport+" Just the wind."
	l.mu.Unlock()
	b.Handle(dm(me, "/hooks replay"))
	got := replies(t, m, 2)
	if !strings.HasPrefix(got[1], "door (events from ") || !strings.HasSuffix(got[1], "): nothing to report (Just the wind)") {
		t.Fatalf("got %q", got)
	}
}

// Snoozes, the latest checks and missed runs outlast a restart.
func TestHookStateSurvivesRestart(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "door", Task: "Check the door."})
	path := filepath.Join(t.TempDir(), "hooks.json")
	b.opt.HookStateFile = path
	b.Handle(dm(me, "/snooze door front_door 2h"))
	reply(t, m)
	l.pingErr = errors.New("connection refused")
	b.Hook(b.ctx, "door", "person on back_door", "back_door")
	reply(t, m)
	// The run is saved just after its unchecked message is sent.
	for i := 0; i < 100; i++ {
		if data, _ := os.ReadFile(path); strings.Contains(string(data), `"missed"`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A new bot, as after a restart, with the same file.
	again := New(m, l, b.store, Options{Allowed: b.opt.Allowed, DefaultModel: "llama3.2:latest", Hooks: b.opt.Hooks,
		Tools: b.opt.Tools, HookStateFile: path, TypingRefresh: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); again.Wait() })
	b2 := testBot{again, ctx}
	b2.Handle(dm(me, "/snooze"))
	if got := reply(t, m); !strings.Contains(got, "door [front_door]") {
		t.Errorf("snooze lost:\n%s", got)
	}
	b2.Handle(dm(me, "/hooks"))
	if got := reply(t, m); !strings.Contains(got, "#1 ") || !strings.Contains(got, "door [back_door]: sent unchecked: Ollama is unreachable\n  person on back_door") ||
		!strings.Contains(got, "1 run went out unchecked: /hooks replay checks it now") {
		t.Errorf("checks or missed run lost:\n%s", got)
	}
	l.mu.Lock()
	l.pingErr, l.answer = nil, "Someone at the back door."
	l.mu.Unlock()
	b2.Handle(dm(me, "/hooks replay"))
	got := replies(t, m, 2)
	if !strings.Contains(got[0], "door [back_door]: Ollama is unreachable") || !strings.HasPrefix(got[1], "🔔 **door** (rechecked, events from ") {
		t.Fatalf("retry after restart: %q", got)
	}

	// An unreadable file doesn't stop the bot.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := New(m, l, b.store, Options{Allowed: b.opt.Allowed, HookStateFile: path})
	if broken.missedCount() != 0 {
		t.Error("loaded something from a broken file")
	}
}
