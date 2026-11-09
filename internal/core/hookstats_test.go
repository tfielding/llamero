package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHookStats(t *testing.T) {
	b, m, l, _ := setupHooks(t,
		Hook{Name: "cams", Task: "Check.", OnlyIf: "a person", Cooldown: time.Minute},
		Hook{Name: "door"}, Hook{Name: "quiet", Task: "Check."})
	set := func(f func()) { l.mu.Lock(); f(); l.mu.Unlock() }

	set(func() { l.answer = "NOTHING_TO_REPORT. A car." })
	b.Hook(b.ctx, "cams", "car on laneway", "laneway")
	expectSilence(t, m)
	b.Hook(b.ctx, "cams", "car on laneway", "laneway")
	expectSilence(t, m)
	set(func() { l.answer = "Someone is at the door." })
	b.Hook(b.ctx, "cams", "person on front_door", "front_door")
	reply(t, m)
	b.Hook(b.ctx, "cams", "person on front_door again", "front_door") // cooling down
	b.Handle(dm(me, "/snooze cams garden 1h"))
	reply(t, m)
	b.Hook(b.ctx, "cams", "person on garden", "garden") // snoozed
	set(func() { l.pingErr = errors.New("connection refused") })
	b.Hook(b.ctx, "cams", "person on porch", "porch")
	reply(t, m)
	set(func() { l.pingErr = nil; l.chatErr = errors.New("boom") })
	b.Hook(b.ctx, "cams", "person on side", "side")
	reply(t, m)
	b.Hook(b.ctx, "door", "door opened", "")
	reply(t, m)

	b.Handle(dm(me, "/hooks stats"))
	got := reply(t, m)
	for _, want := range []string{
		"Hook stats since the bot started (",
		"\ncams\n- 3 checked: 1 sent (33%), 2 nothing to report\n",
		"- 2 sent unchecked: Ollama is unreachable 1, the model returned an error 1\n",
		"- 2 skipped: cooling down 1, snoozed 1\n",
		"- check time: typical ",
		"- by key: front_door 2 (1 sent), laneway 2 (0 sent), garden 1 (0 sent), porch 1 (1 sent), side 1 (1 sent)\n",
		"\ndoor\n- 1 sent on (no task)\n",
		"\nquiet\n- no events yet\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "door\n- by key") {
		t.Errorf("keyless events listed by key:\n%s", got)
	}

	b.Handle(dm(me, "/hooks nope"))
	if got := reply(t, m); !strings.Contains(got, "/hooks stats") {
		t.Errorf("got %q", got)
	}
}

func TestHookStatsHelpers(t *testing.T) {
	if got := tally(map[string]int{"b": 1, "a": 1, "c": 3, "zero": 0}); got != "c 3, a 1, b 1" {
		t.Errorf("tally: %q", got)
	}
	keys := map[string]*keyStats{}
	for i, k := range strings.Fields("a b c d e f g h i j") {
		keys[k] = &keyStats{events: 20 - i}
	}
	if got := keyTally(keys); !strings.HasPrefix(got, "a 20 (0 sent), b 19") || !strings.HasSuffix(got, "h 13 (0 sent), 2 more") {
		t.Errorf("keyTally: %q", got)
	}
	for d, want := range map[time.Duration]string{8440 * time.Millisecond: "8.4s", 41400 * time.Millisecond: "41s", 75 * time.Second: "1m15s"} {
		if got := checkTime(d); got != want {
			t.Errorf("checkTime(%s) = %q, want %q", d, got, want)
		}
	}
	if percent(1, 3) != 33 || percent(2, 3) != 67 {
		t.Error("percent")
	}
}
