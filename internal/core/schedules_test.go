package core

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tfielding/llamero/internal/ollama"
	"github.com/tfielding/llamero/internal/schedule"
	"github.com/tfielding/llamero/internal/store"
	"github.com/tfielding/llamero/internal/toolbox"
)

func setupSchedules(t *testing.T) (testBot, *fakeMsg, *fakeLLM, *store.Store, *schedule.Store) {
	t.Helper()
	b, m, l, st := setup(t)
	ss, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.opt.Schedules, b.opt.Location = ss, time.UTC
	l.caps = []string{"tools"}
	return b, m, l, st, ss
}

func call(name string, args map[string]any) *ollama.ToolCall {
	tc := &ollama.ToolCall{}
	tc.Function.Name, tc.Function.Arguments = name, args
	return tc
}

// addSchedule saves a schedule that's due now.
func addSchedule(t *testing.T, ss *schedule.Store, when, task, onlyIf string) schedule.Item {
	t.Helper()
	now := time.Now().UTC()
	spec, err := schedule.Parse(when, now)
	if err != nil {
		t.Fatal(err)
	}
	var it schedule.Item
	err = ss.Update(me, func(f *schedule.File) error {
		var err error
		if it, err = f.Add(spec, task, onlyIf, false, now); err != nil {
			return err
		}
		f.Items[len(f.Items)-1].Next = now.Add(-time.Minute)
		it = f.Items[len(f.Items)-1]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestCreateScheduleNeedsApproval(t *testing.T) {
	b, m, l, _, ss := setupSchedules(t)
	l.toolCall = call(toolCreateSchedule, map[string]any{"when": "weekdays 07:30", "task": "Summarize overnight camera alerts"})
	b.Handle(dm(me, "every weekday at 7:30 tell me what happened overnight"))
	prompt := reply(t, m)
	for _, want := range []string{"Create this schedule?", "• when: weekdays 07:30 (first: ", "• task: Summarize overnight camera alerts", "Reply yes to save it"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if f, _ := ss.Get(me); len(f.Items) != 0 {
		t.Fatal("saved before approval")
	}
	b.Handle(dm(me, "yes"))
	got := reply(t, m)
	f, _ := ss.Get(me)
	if len(f.Items) != 1 || f.Items[0].When != "weekdays 07:30" || f.Items[0].Next.IsZero() {
		t.Fatalf("schedules: %+v", f.Items)
	}
	if !strings.Contains(got, "Saved as schedule 1: weekdays 07:30") {
		t.Errorf("model wasn't told it was saved: %s", got)
	}
}

func TestCreateScheduleDeclined(t *testing.T) {
	b, m, l, _, ss := setupSchedules(t)
	l.toolCall = call(toolCreateSchedule, map[string]any{"when": "daily 08:00", "task": "Check the porch"})
	b.Handle(dm(me, "check the porch every morning"))
	reply(t, m) // the prompt
	b.Handle(dm(me, "no"))
	reply(t, m)
	if f, _ := ss.Get(me); len(f.Items) != 0 {
		t.Fatalf("saved although declined: %+v", f.Items)
	}
}

func TestInvalidScheduleIsNotAsked(t *testing.T) {
	b, m, l, _, ss := setupSchedules(t)
	l.toolCall = call(toolCreateSchedule, map[string]any{"when": "every so often", "task": "Check the porch"})
	b.Handle(dm(me, "check the porch sometimes"))
	got := reply(t, m)
	if strings.Contains(got, "Create this schedule?") || !strings.Contains(got, `when "every so often" not understood`) ||
		!strings.Contains(got, "weekdays 07:30") {
		t.Fatalf("got: %s", got)
	}
	if f, _ := ss.Get(me); len(f.Items) != 0 {
		t.Fatal("invalid schedule saved")
	}
}

func TestScheduledRun(t *testing.T) {
	b, m, l, st, ss := setupSchedules(t)
	b.opt.Tools = &fakeTools{tools: []toolbox.Tool{
		{Name: "turn_off", Schema: lightSchema, Confirm: true},
		{Name: "get_state", Schema: lightSchema, ReadOnly: true},
	}}
	it := addSchedule(t, ss, "daily 07:30", "Check the driveway", "")
	b.dueSchedules(b.ctx, time.Now().UTC())
	got := reply(t, m)
	if !strings.Contains(got, "Check the driveway") || !strings.Contains(got, "Scheduled check 1") {
		t.Fatalf("got: %s", got)
	}
	l.mu.Lock()
	offered, last := l.offered, l.last[len(l.last)-1].Content
	l.mu.Unlock()
	// Only read-only tools: no one is there to approve an action, and no schedule tools.
	if strings.Join(offered, ",") != "get_state" {
		t.Errorf("tools offered: %v", offered)
	}
	if !strings.Contains(last, "not a new message from them") || !strings.Contains(last, "Do this now, using your tools to look things up: Check the driveway") {
		t.Errorf("task message: %s", last)
	}
	waitIdle(t, b)
	f, _ := ss.Get(me)
	if f.Items[0].LastRun.IsZero() || !f.Items[0].Next.After(time.Now()) {
		t.Errorf("not moved on: %+v", f.Items[0])
	}
	chat, _ := st.Load(me)
	if n := len(chat.History); n != 2 || chat.History[0].Content != "(Scheduled check 1: Check the driveway)" {
		t.Errorf("history: %+v", chat.History)
	}
	_ = it
}

func TestScheduledOnlyIfStaysQuiet(t *testing.T) {
	b, m, l, st, ss := setupSchedules(t)
	l.answer = "NOTHING_TO_REPORT"
	addSchedule(t, ss, "every 30m", "Is the garage door open?", "the garage door is open")
	b.dueSchedules(b.ctx, time.Now().UTC())
	expectSilence(t, m)
	waitIdle(t, b)
	l.mu.Lock()
	task := l.last[len(l.last)-1].Content
	l.mu.Unlock()
	if !strings.Contains(task, "Only report if this is true: the garage door is open. If it isn't, reply with exactly NOTHING_TO_REPORT") {
		t.Errorf("task message: %s", task)
	}
	if chat, _ := st.Load(me); len(chat.History) != 0 {
		t.Errorf("quiet run added to history: %+v", chat.History)
	}

	// Something to report: it's sent.
	l.mu.Lock()
	l.answer = "Yes, the garage door has been open since 22:10."
	l.mu.Unlock()
	b.dueSchedules(b.ctx, time.Now().UTC().Add(31*time.Minute))
	if got := reply(t, m); !strings.Contains(got, "open since 22:10") {
		t.Fatalf("got: %s", got)
	}
}

func TestOneOffIsRemovedAfterRunning(t *testing.T) {
	b, m, _, _, ss := setupSchedules(t)
	addSchedule(t, ss, "tomorrow 9am", "Remind me to call the plumber", "")
	b.dueSchedules(b.ctx, time.Now().UTC())
	reply(t, m)
	waitIdle(t, b)
	if f, _ := ss.Get(me); len(f.Items) != 0 {
		t.Fatalf("one-off kept: %+v", f.Items)
	}
}

func TestMissedRunsAreSkipped(t *testing.T) {
	b, m, _, _, ss := setupSchedules(t)
	addSchedule(t, ss, "every 1h", "Check the cameras", "")
	// Long overdue (the bot was down): skipped, next run moved on.
	b.dueSchedules(b.ctx, time.Now().UTC().Add(2*time.Hour))
	expectSilence(t, m)
	f, _ := ss.Get(me)
	if !f.Items[0].Next.After(time.Now().Add(2*time.Hour)) || !f.Items[0].LastRun.IsZero() {
		t.Errorf("missed run: %+v", f.Items[0])
	}
}

func TestFailedRunsAreReportedOnce(t *testing.T) {
	b, m, l, _, ss := setupSchedules(t)
	addSchedule(t, ss, "every 30m", "Is the garage door open?", "")
	due := func(after time.Duration) {
		ss.Update(me, func(f *schedule.File) error { f.Items[0].Next = time.Now().Add(-time.Minute); return nil })
		b.dueSchedules(b.ctx, time.Now().UTC().Add(after))
	}
	l.mu.Lock()
	l.pingErr = refused
	l.mu.Unlock()

	due(0)
	got := reply(t, m)
	for _, want := range []string{"Schedule 1 didn't run: Ollama is unreachable (connect: connection refused).",
		"Task: Is the garage door open?", "I won't repeat this until it has worked again."} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
	waitIdle(t, b)
	if f, _ := ss.Get(me); !f.Items[0].Failing {
		t.Fatal("not marked failing")
	}
	b.Handle(dm(me, "/schedules"))
	if got := reply(t, m); !strings.Contains(got, "last run failed") {
		t.Errorf("list: %s", got)
	}

	// Still failing: not reported again.
	due(0)
	expectSilence(t, m)
	waitIdle(t, b)

	// Works again: the result is sent and the mark cleared.
	l.mu.Lock()
	l.pingErr = nil
	l.mu.Unlock()
	due(0)
	if got := reply(t, m); !strings.Contains(got, "Is the garage door open?") {
		t.Fatalf("got: %s", got)
	}
	waitIdle(t, b)
	if f, _ := ss.Get(me); f.Items[0].Failing {
		t.Fatal("still marked failing")
	}

	// Failing again later is reported again.
	l.mu.Lock()
	l.chatErr = errors.New("model not found")
	l.mu.Unlock()
	due(0)
	if got := reply(t, m); !strings.Contains(got, "didn't run: the model returned an error (model not found)") {
		t.Fatalf("got: %s", got)
	}
}

func TestFailedOneOffIsReported(t *testing.T) {
	b, m, l, _, ss := setupSchedules(t)
	addSchedule(t, ss, "tomorrow 9am", "Remind me to call the plumber", "")
	l.mu.Lock()
	l.pingErr = refused
	l.mu.Unlock()
	b.dueSchedules(b.ctx, time.Now().UTC())
	got := reply(t, m)
	if !strings.Contains(got, "Schedule 1 didn't run") || strings.Contains(got, "won't repeat") {
		t.Fatalf("got: %s", got)
	}
	waitIdle(t, b)
	if f, _ := ss.Get(me); len(f.Items) != 0 {
		t.Fatalf("one-off kept: %+v", f.Items)
	}
}

func TestSchedulesCommand(t *testing.T) {
	b, m, _, _, ss := setupSchedules(t)
	b.Handle(dm(me, "/schedules"))
	if got := reply(t, m); !strings.Contains(got, "No schedules yet") {
		t.Fatalf("empty: %s", got)
	}
	addSchedule(t, ss, "weekdays 07:30", "Summarize overnight alerts", "")
	addSchedule(t, ss, "every 30m between 22:00 and 06:00", "Is the garage door open?", "it's open")
	b.Handle(dm(me, "/schedules"))
	got := reply(t, m)
	for _, want := range []string{"Schedules (times are UTC):", "1. weekdays 07:30: Summarize overnight alerts",
		"2. every 30m between 22:00 and 06:00: Is the garage door open?", "   only if: it's open", "/schedules delete, pause, resume or run"} {
		if !strings.Contains(got, want) {
			t.Errorf("list lacks %q:\n%s", want, got)
		}
	}
	for cmd, want := range map[string]string{
		"/schedules pause 2":      "Paused schedule 2.",
		"/schedules delete 9":     "There's no schedule 9.",
		"/schedules frobnicate 1": "Use /schedules",
	} {
		b.Handle(dm(me, cmd))
		if got := reply(t, m); !strings.Contains(got, want) {
			t.Errorf("%s: %s", cmd, got)
		}
	}
	if f, _ := ss.Get(me); !f.Items[1].Paused {
		t.Error("not paused")
	}
	b.Handle(dm(me, "/schedules"))
	if got := reply(t, m); !strings.Contains(got, "   paused") {
		t.Errorf("list doesn't show paused:\n%s", got)
	}
	b.Handle(dm(me, "/status"))
	if got := reply(t, m); !strings.Contains(got, "Schedules: 2, next ") {
		t.Errorf("/status: %s", got)
	}
	b.Handle(dm(me, "/schedules delete 1"))
	reply(t, m)
	if f, _ := ss.Get(me); len(f.Items) != 1 || f.Items[0].ID != 2 {
		t.Errorf("after delete: %+v", f.Items)
	}
	b.Handle(dm(me, "/schedules run 2"))
	if got := reply(t, m); got != "Running schedule 2 now." {
		t.Errorf("run: %s", got)
	}
	if got := reply(t, m); !strings.Contains(got, "Is the garage door open?") {
		t.Errorf("run result: %s", got)
	}
}

func TestDeleteScheduleTool(t *testing.T) {
	b, m, l, _, ss := setupSchedules(t)
	addSchedule(t, ss, "daily 08:00", "Check the porch", "")
	l.toolCall = call(toolDeleteSchedule, map[string]any{"id": float64(1)})
	b.Handle(dm(me, "stop the porch check"))
	if got := reply(t, m); !strings.Contains(got, "Delete schedule 1?") || !strings.Contains(got, "daily 08:00: Check the porch") {
		t.Fatalf("prompt: %s", got)
	}
	b.Handle(dm(me, "yes"))
	reply(t, m)
	if f, _ := ss.Get(me); len(f.Items) != 0 {
		t.Fatalf("not deleted: %+v", f.Items)
	}
	// A number that doesn't exist: the model is told, the user isn't asked.
	l.toolCall = call(toolDeleteSchedule, map[string]any{"id": "7"})
	b.Handle(dm(me, "delete schedule 7"))
	if got := reply(t, m); strings.Contains(got, "Delete schedule") || !strings.Contains(got, "no schedule number 7") {
		t.Fatalf("got: %s", got)
	}
}

func TestNothingToReport(t *testing.T) {
	for in, want := range map[string]bool{
		"NOTHING_TO_REPORT": true, "`NOTHING_TO_REPORT`": true, "nothing_to_report.": true,
		"NOTHING_TO_REPORT - the door is closed": true,
		"NOTHING_TO_REPORT. A car drove along the street and parked across the road for a few minutes before leaving.": true,
		"**NOTHING_TO_REPORT**\n\nOnly a delivery van passed by on the street, nobody came to the door.":               true,
		"The garage door is open. (not NOTHING_TO_REPORT, it's been open for an hour and you should check it)":         false,
		"All quiet": false,
	} {
		if got := nothing(in); got != want {
			t.Errorf("nothing(%q) = %v", in, got)
		}
	}
}

func TestScheduleToolsWithoutMCP(t *testing.T) {
	b, m, l, _, _ := setupSchedules(t) // no MCP servers configured
	b.Handle(dm(me, "hi"))
	reply(t, m)
	l.mu.Lock()
	offered := l.offered
	l.mu.Unlock()
	if strings.Join(offered, ",") != "create_schedule,list_schedules,delete_schedule" {
		t.Errorf("tools offered: %v", offered)
	}
}

// waitIdle waits until nothing is running for me.
func waitIdle(t *testing.T, b testBot) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		_, busy := b.active[me]
		n := len(b.scheduled)
		b.mu.Unlock()
		if !busy && n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("still busy")
}

func TestScheduledRunNoAnswer(t *testing.T) {
	b, m, l, _, ss := setupSchedules(t)
	b.opt.Tools = &fakeTools{tools: []toolbox.Tool{{Name: "get_state", Schema: lightSchema, ReadOnly: true}}}
	l.toolCall, l.blanks = call("get_state", map[string]any{"name": "driveway"}), 2
	addSchedule(t, ss, "daily 07:30", "Check the driveway", "")
	b.dueSchedules(b.ctx, time.Now().UTC())
	if got := reply(t, m); !strings.Contains(got, "Schedule 1 didn't run: the model gave no answer.") {
		t.Errorf("got %q", got)
	}
}
