package core

import (
	"strings"
	"testing"
	"time"
)

func TestSnooze(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "cams", Task: "Check it."}, Hook{Name: "door"})
	l.answer = "Someone is there."
	calls := func() int { l.mu.Lock(); defer l.mu.Unlock(); return l.calls }
	say := func(text string) string { t.Helper(); b.Handle(dm(me, text)); return reply(t, m) }

	if got := say("/snooze"); !strings.HasPrefix(got, "Nothing is snoozed.") {
		t.Errorf("empty list: %q", got)
	}
	for in, want := range map[string]string{
		"/snooze nope 1h":       `No hook named "nope". Hooks: cams, door`,
		"/snooze cams 10s":      "10s is too short",
		"/snooze cams 8d":       "8d is too long",
		"/snooze cams soon":     `"soon" isn't a length of time`,
		"/snooze all garden 1h": "/snooze 2h - mute all hooks",
	} {
		if got := say(in); !strings.HasPrefix(got, want) {
			t.Errorf("%s: got %q, want %q…", in, got, want)
		}
	}

	// One key of one hook: that camera is skipped without asking the model, others aren't.
	if got := say("/snooze cams Front_Door 2h"); !strings.HasPrefix(got, "Snoozed cams [front_door] for 2h, until ") {
		t.Errorf("got %q", got)
	}
	b.Hook(b.ctx, "cams", "person on front_door", "front_door")
	expectSilence(t, m)
	if n := calls(); n != 0 {
		t.Fatalf("model asked %d times for a snoozed key", n)
	}
	b.Hook(b.ctx, "cams", "person on garden", "garden")
	reply(t, m)

	// The whole hook, and the list.
	say("/snooze cams 30m")
	b.Hook(b.ctx, "cams", "person on garden", "garden")
	b.Hook(b.ctx, "door", "door opened", "")
	if got := reply(t, m); !strings.Contains(got, "door opened") {
		t.Errorf("another hook muted: %q", got)
	}
	expectSilence(t, m)
	got := say("/snooze")
	for _, want := range []string{"- cams until ", "(30m left)", "- cams [front_door] until ", "(2h left)"} {
		if !strings.Contains(got, want) {
			t.Errorf("list lacks %q:\n%s", want, got)
		}
	}
	b.Handle(dm(me, "/hooks"))
	if got := reply(t, m); !strings.Contains(got, "cams [garden]: skipped: snoozed for 30m") {
		t.Errorf("/hooks:\n%s", got)
	}

	// Off for one hook ends its key snoozes too.
	if got := say("/snooze cams off"); got != "Unsnoozed cams." {
		t.Errorf("got %q", got)
	}
	b.Hook(b.ctx, "cams", "person on front_door", "front_door")
	reply(t, m)

	// Every hook, then off.
	say("/snooze 1d")
	b.Hook(b.ctx, "door", "door opened", "")
	expectSilence(t, m)
	if got := say("/snooze off"); got != "Unsnoozed all hooks." {
		t.Errorf("got %q", got)
	}
	if got := say("/snooze off"); got != "Nothing to unsnooze for all hooks." {
		t.Errorf("got %q", got)
	}
	b.Hook(b.ctx, "door", "door opened", "")
	reply(t, m)
}

func TestSnoozeIsPerPerson(t *testing.T) {
	b, m, l, _ := setupHooks(t, Hook{Name: "cams", Task: "Check it.", Group: 100 * time.Millisecond})
	b.opt.Allowed[other] = true
	l.answer = "Someone is there."
	b.Handle(dm(me, "/snooze cams 1h"))
	reply(t, m)
	b.Hook(b.ctx, "cams", "person", "front_door")
	reply(t, m)
	expectSilence(t, m)
	m.mu.Lock()
	sent := m.sent[len(m.sent)-1]
	m.mu.Unlock()
	if !strings.HasPrefix(sent, other+": ") {
		t.Errorf("sent %q, want it only to the one who didn't snooze", sent)
	}

	// Snoozed while the events were being grouped: no check at all.
	b3, m3, l3, _ := setupHooks(t, Hook{Name: "cams", Task: "Check it.", Group: 50 * time.Millisecond})
	l3.answer = "Someone is there."
	b3.Hook(b3.ctx, "cams", "person", "front_door")
	b3.Handle(dm(me, "/snooze cams 1h"))
	reply(t, m3)
	expectSilence(t, m3)
	l3.mu.Lock()
	calls := l3.calls
	l3.mu.Unlock()
	if calls != 0 {
		t.Errorf("model asked %d times", calls)
	}
	b3.Handle(dm(me, "/hooks"))
	if got := reply(t, m3); !strings.Contains(got, "cams [front_door]: skipped: snoozed for 1h") {
		t.Errorf("/hooks:\n%s", got)
	}

	// Hooks someone doesn't get can't be snoozed.
	b2, m2, _, _ := setupHooks(t, Hook{Name: "cams", To: []string{other}})
	b2.Handle(dm(me, "/snooze cams 1h"))
	if got := reply(t, m2); got != "You don't get cams's messages, so there's nothing to snooze." {
		t.Errorf("got %q", got)
	}
}

func TestParseSnooze(t *testing.T) {
	for in, want := range map[string]time.Duration{"30m": 30 * time.Minute, "1h30m": 90 * time.Minute, "2d": 48 * time.Hour, "7d": maxSnooze} {
		if got, err := parseSnooze(in); err != nil || got != want {
			t.Errorf("%s: %v, %v", in, got, err)
		}
	}
	for d, want := range map[time.Duration]string{time.Minute: "1m", 90 * time.Minute: "1h 30m", 26*time.Hour + 5*time.Minute: "1d 2h", 20 * time.Second: "1m"} {
		if got := humanDuration(d); got != want {
			t.Errorf("%s: got %q, want %q", d, got, want)
		}
	}
}
