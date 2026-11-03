package bot

// Scheduled checks: the built-in tools the model uses to create them, the loop that runs them,
// and /schedules.

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/tfielding/signal-ollama/internal/ollama"
	"github.com/tfielding/signal-ollama/internal/schedule"
	"github.com/tfielding/signal-ollama/internal/toolbox"
)

const (
	toolCreateSchedule = "create_schedule"
	toolListSchedules  = "list_schedules"
	toolDeleteSchedule = "delete_schedule"

	// nothingToReport is how the model says an "only if" check found nothing.
	nothingToReport = "NOTHING_TO_REPORT"

	// missedAfter: a run this late (the bot was down) is skipped rather than run late.
	missedAfter = 10 * time.Minute
)

// scheduleTools are the bot's own tools for managing schedules. Creating and deleting always
// need the user's approval.
func scheduleTools() []toolbox.Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return []toolbox.Tool{
		{
			Name: toolCreateSchedule,
			Description: "Set up a check that runs later or repeatedly, with its result sent to the user: e.g. \"every weekday " +
				"at 7:30 tell me what happened overnight on the cameras\", \"remind me at 5pm to check the porch\", \"every " +
				"30 minutes after 22:00 tell me if the garage door is open\". Only for things the user asked to be done " +
				"later or regularly. The user is asked to approve it.",
			Schema: map[string]any{"type": "object", "required": []string{"when", "task"}, "properties": map[string]any{
				"when": str("when to run, one of these forms: " + schedule.Forms),
				"task": str("what to check or say, in the user's words, e.g. \"summarize camera alerts since 22:00\""),
				"only_if": str("optional: report only when this is true, e.g. \"the garage door is open\"; " +
					"leave out to always report"),
				"wake": map[string]any{"type": "boolean", "description": "optional: wake the Ollama machine if it's asleep " +
					"instead of skipping the run"},
			}},
			Confirm: true,
		},
		{
			Name:        toolListSchedules,
			Description: "List the user's schedules with their numbers.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
			ReadOnly:    true,
		},
		{
			Name:        toolDeleteSchedule,
			Description: "Delete one of the user's schedules by its number (from list_schedules). The user is asked to approve it.",
			Schema: map[string]any{"type": "object", "required": []string{"id"}, "properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "the schedule's number"},
			}},
			Confirm: true,
		},
	}
}

func isScheduleTool(name string) bool {
	return name == toolCreateSchedule || name == toolListSchedules || name == toolDeleteSchedule
}

func (b *Bot) loc() *time.Location {
	if b.opt.Location != nil {
		return b.opt.Location
	}
	return time.Local
}

// checkScheduleCall validates a schedule tool call before the user is asked to approve it, so
// they're never asked about something that can't be saved. It returns what to tell the model
// if the call is invalid.
func (b *Bot) checkScheduleCall(sender, name string, args map[string]any) string {
	switch name {
	case toolCreateSchedule:
		if strings.TrimSpace(argString(args, "task")) == "" {
			return "Tool failed: task is required: what to check or say."
		}
		if _, err := schedule.Parse(argString(args, "when"), time.Now().In(b.loc())); err != nil {
			return "Tool failed: when " + err.Error()
		}
		if f, err := b.opt.Schedules.Get(sender); err == nil && len(f.Items) >= schedule.MaxPerChat {
			return fmt.Sprintf("Tool failed: the user already has %d schedules, the most allowed. One must be deleted first.", schedule.MaxPerChat)
		}
	case toolDeleteSchedule:
		f, err := b.opt.Schedules.Get(sender)
		if err != nil {
			return "Tool failed: " + err.Error()
		}
		if _, ok := f.Find(argInt(args, "id")); !ok {
			return fmt.Sprintf("Tool failed: there is no schedule number %v; use list_schedules to see them.", args["id"])
		}
	}
	return ""
}

// schedulePrompt is the approval request for a schedule tool call, in plain words.
func (b *Bot) schedulePrompt(sender, name string, args map[string]any, timeout time.Duration) string {
	var sb strings.Builder
	switch name {
	case toolCreateSchedule:
		now := time.Now().In(b.loc())
		spec, _ := schedule.Parse(argString(args, "when"), now)
		sb.WriteString("Create this schedule?\n")
		fmt.Fprintf(&sb, "• when: %s (first: %s)\n", spec, spec.Next(now, time.Time{}).Format("Mon 2 Jan 15:04"))
		fmt.Fprintf(&sb, "• task: %s\n", strings.TrimSpace(argString(args, "task")))
		if c := strings.TrimSpace(argString(args, "only_if")); c != "" {
			fmt.Fprintf(&sb, "• only tell me if: %s\n", c)
		}
		if argBool(args, "wake") {
			sb.WriteString("• wakes the Ollama machine if it's asleep\n")
		}
		fmt.Fprintf(&sb, "Reply yes to save it, anything else cancels (%s).", shortDuration(timeout))
	case toolDeleteSchedule:
		f, _ := b.opt.Schedules.Get(sender)
		it, _ := f.Find(argInt(args, "id"))
		fmt.Fprintf(&sb, "Delete schedule %d?\n• %s: %s\n", it.ID, b.specText(it.When), it.Task)
		fmt.Fprintf(&sb, "Reply yes to delete it, anything else cancels (%s).", shortDuration(timeout))
	}
	return sb.String()
}

// callScheduleTool runs an (approved) schedule tool call.
func (b *Bot) callScheduleTool(sender, name string, args map[string]any) toolbox.Output {
	now := time.Now().In(b.loc())
	switch name {
	case toolCreateSchedule:
		spec, err := schedule.Parse(argString(args, "when"), now)
		if err != nil {
			return toolbox.Output{Text: "when " + err.Error(), IsError: true}
		}
		var it schedule.Item
		err = b.opt.Schedules.Update(sender, func(f *schedule.File) error {
			var err error
			it, err = f.Add(spec, argString(args, "task"), argString(args, "only_if"), argBool(args, "wake"), now)
			return err
		})
		if err != nil {
			return toolbox.Output{Text: err.Error(), IsError: true}
		}
		slog.Info("schedule created", "id", it.ID, "when", it.When)
		return toolbox.Output{Text: fmt.Sprintf("Saved as schedule %d: %s, first on %s. The user can see them with /schedules.",
			it.ID, spec, it.Next.In(b.loc()).Format("Mon 2 Jan 15:04"))}
	case toolDeleteSchedule:
		id := argInt(args, "id")
		removed := false
		err := b.opt.Schedules.Update(sender, func(f *schedule.File) error { removed = f.Remove(id); return nil })
		if err != nil || !removed {
			return toolbox.Output{Text: fmt.Sprintf("Couldn't delete schedule %d: %v", id, err), IsError: true}
		}
		slog.Info("schedule deleted", "id", id)
		return toolbox.Output{Text: fmt.Sprintf("Deleted schedule %d.", id)}
	case toolListSchedules:
		return toolbox.Output{Text: b.schedulesText(sender, false)}
	}
	return toolbox.Output{Text: "unknown schedule tool " + name, IsError: true}
}

// StartScheduler runs scheduled checks until ctx ends. Due checks join the chat's queue, so
// they wait behind the user's own messages.
func (b *Bot) StartScheduler(ctx context.Context) {
	if b.opt.Schedules == nil {
		return
	}
	every := b.opt.ScheduleTick
	if every == 0 {
		every = 15 * time.Second
	}
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			b.dueSchedules(ctx, time.Now().In(b.loc()))
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// dueSchedules queues every schedule that's due, and moves each on to its next run.
func (b *Bot) dueSchedules(ctx context.Context, now time.Time) {
	all, err := b.opt.Schedules.All()
	if err != nil {
		slog.Error("reading schedules", "err", err)
		return
	}
	for chat, f := range all {
		if !b.opt.Allowed[chat] {
			continue // no longer allowed to use the bot
		}
		for _, it := range f.Items {
			if it.Paused || it.Next.IsZero() || it.Next.After(now) || b.pending(chat, it.ID) {
				continue
			}
			late := now.Sub(it.Next) > missedAfter
			err := b.opt.Schedules.Update(chat, func(f *schedule.File) error {
				cur, ok := f.Find(it.ID)
				if !ok {
					return nil
				}
				spec, err := cur.Spec(b.loc())
				if err != nil {
					slog.Warn("schedule unreadable, removed", "id", cur.ID, "when", cur.When, "err", err)
					f.Remove(cur.ID)
					return nil
				}
				if late {
					cur.Next = spec.Next(now, time.Time{})
					return nil
				}
				cur.LastRun = now
				cur.Next = spec.Next(now, now)
				if !spec.Once.IsZero() {
					cur.Next = time.Time{} // a one-off is done once it has run
				}
				return nil
			})
			if err != nil {
				slog.Error("updating schedule", "err", err)
				continue
			}
			if late {
				slog.Warn("scheduled check missed (the bot was down or busy), skipped", "id", it.ID, "was_due", it.Next)
				continue
			}
			b.queueSchedule(ctx, chat, it.ID)
		}
	}
}

func (b *Bot) pending(chat string, id int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.scheduled[fmt.Sprintf("%s#%d", chat, id)]
}

// queueSchedule puts a scheduled run in the chat's queue, unless one is already waiting.
func (b *Bot) queueSchedule(ctx context.Context, chat string, id int) {
	key := fmt.Sprintf("%s#%d", chat, id)
	b.mu.Lock()
	if b.scheduled[key] {
		b.mu.Unlock()
		return
	}
	b.scheduled[key] = true
	b.mu.Unlock()
	if !b.enqueue(ctx, chat, message{schedule: id}) {
		b.mu.Lock()
		delete(b.scheduled, key)
		b.mu.Unlock()
	}
}

// runSchedule does one scheduled check: asks the model with read-only tools and sends the
// result, unless it's an "only if" check that found nothing.
func (b *Bot) runSchedule(ctx context.Context, sender string, id int) {
	defer func() {
		b.mu.Lock()
		delete(b.scheduled, fmt.Sprintf("%s#%d", sender, id))
		b.mu.Unlock()
	}()
	f, err := b.opt.Schedules.Get(sender)
	if err != nil {
		slog.Error("reading schedules", "err", err)
		return
	}
	it, ok := f.Find(id) // paused ones only get here from /schedules run
	if !ok {
		return
	}
	done := func() { // a one-off is removed once it has run
		if it.Next.IsZero() {
			_ = b.opt.Schedules.Update(sender, func(f *schedule.File) error { f.Remove(id); return nil })
		}
	}
	start := time.Now()
	chat, err := b.store.Load(sender)
	if err != nil {
		slog.Error("load history", "err", err)
		return
	}
	model := chat.Model
	if model == "" {
		model = b.opt.DefaultModel
	}
	if err := b.llm.Ping(ctx); err != nil {
		if !it.Wake || b.opt.Wake == nil || !b.wakeQuietly(ctx) {
			slog.Warn("scheduled check skipped: ollama unreachable", "id", id, "err", err)
			done()
			return
		}
	}
	info, _ := b.llm.Info(ctx, model)
	var tools []toolbox.Tool
	if b.opt.Tools != nil && info.Has("tools") {
		for _, t := range b.opt.Tools.Tools(ctx) {
			if t.ReadOnly { // no one is there to approve an action
				tools = append(tools, t)
			}
		}
	}
	msgs := b.prompt(chat, tools, ollama.Message{Role: "user", Content: scheduledTask(*it, b.specText(it.When))})

	b.mu.Lock()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.active[sender] = activity{model: model, started: start, cancel: cancel, task: fmt.Sprintf("running schedule %d", id)}
	b.mu.Unlock()
	slog.Info("scheduled check", "id", id, "model", model, "tools", len(tools))
	answer, images, _, err := b.run(callCtx, sender, model, msgs, b.thinkValue(ctx, model, chat.Think), tools, info.Has("vision"))
	b.mu.Lock()
	delete(b.active, sender)
	b.mu.Unlock()
	done()
	if err != nil {
		slog.Error("scheduled check failed", "id", id, "err", err)
		return
	}
	answer = stripImageNotes(answer)
	if it.OnlyIf != "" && len(images) == 0 && nothing(answer) {
		slog.Info("scheduled check: nothing to report", "id", id, "took", time.Since(start).Round(100*time.Millisecond))
		return
	}
	text := fmt.Sprintf("⏰ **%s**\n\n%s", it.Task, answer)
	if err := b.send(ctx, sender, response{text: text, fromModel: true, images: images}); err != nil {
		slog.Error("send failed", "to", mask(sender), "err", err)
		return
	}
	slog.Info("scheduled check sent", "id", id, "chars", len(answer), "images", len(images), "took", time.Since(start).Round(100*time.Millisecond))

	// Keep it in the conversation, so "show me that one" works afterwards.
	if chat, err = b.store.Load(sender); err == nil {
		saved := answer
		if len(images) > 0 {
			saved += fmt.Sprintf("\n\n[%d image(s) were sent to the user with this reply.]", len(images))
		}
		chat.History = trim(append(chat.History,
			ollama.Message{Role: "user", Content: fmt.Sprintf("(Scheduled check %d: %s)", id, it.Task)},
			ollama.Message{Role: "assistant", Content: saved}), b.historyCap())
		if err := b.store.Save(sender, chat); err != nil {
			slog.Error("save history", "err", err)
		}
	}
}

// scheduledTask is the message the model gets for a scheduled run.
func scheduledTask(it schedule.Item, when string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "(Scheduled check %d, set up by the user earlier to run %s. This is not a new message from them.)\n", it.ID, when)
	fmt.Fprintf(&sb, "Do this now, using your tools to look things up: %s\n", it.Task)
	if it.OnlyIf != "" {
		fmt.Fprintf(&sb, "Only report if this is true: %s. If it isn't, reply with exactly %s and nothing else.\n", it.OnlyIf, nothingToReport)
	}
	sb.WriteString("You can only look things up here, not take actions. Keep the answer short: it's sent to the user's phone.")
	return sb.String()
}

// nothing reports whether the model answered that there's nothing to report.
func nothing(answer string) bool {
	a := strings.Trim(strings.TrimSpace(answer), "`*_. \"'")
	return strings.EqualFold(a, nothingToReport) || (len(a) < 60 && strings.Contains(strings.ToUpper(a), nothingToReport))
}

// wakeQuietly wakes the Ollama machine without messaging the user, and waits for it.
func (b *Bot) wakeQuietly(ctx context.Context) bool {
	if err := b.opt.Wake(ctx); err != nil {
		slog.Warn("wake-on-lan failed", "err", err)
		return false
	}
	deadline := time.Now().Add(b.opt.WakeWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(b.opt.WakePoll):
		}
		if b.llm.Ping(ctx) == nil {
			return true
		}
	}
	return false
}

func (b *Bot) specText(saved string) string {
	if spec, err := (schedule.Item{When: saved}).Spec(b.loc()); err == nil {
		return spec.String()
	}
	return saved
}

// schedulesText lists sender's schedules, for /schedules and list_schedules.
func (b *Bot) schedulesText(sender string, withHelp bool) string {
	f, err := b.opt.Schedules.Get(sender)
	if err != nil {
		return "Couldn't read schedules: " + err.Error()
	}
	if len(f.Items) == 0 {
		return "No schedules yet. Ask for one in plain words, e.g. \"every weekday at 7:30 tell me what happened overnight on the cameras\"."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Schedules (times are %s):\n", time.Now().In(b.loc()).Format("MST"))
	for _, it := range f.Items {
		fmt.Fprintf(&sb, "%d. %s: %s\n", it.ID, b.specText(it.When), it.Task)
		if it.OnlyIf != "" {
			fmt.Fprintf(&sb, "   only if: %s\n", it.OnlyIf)
		}
		switch {
		case it.Paused:
			sb.WriteString("   paused\n")
		case !it.Next.IsZero():
			fmt.Fprintf(&sb, "   next: %s\n", it.Next.In(b.loc()).Format("Mon 2 Jan 15:04"))
		}
	}
	if withHelp {
		sb.WriteString("\nChange one with /schedules delete, pause, resume or run, and its number.")
	}
	return strings.TrimSpace(sb.String())
}

// schedulesCommand answers /schedules [delete|pause|resume|run <n>].
func (b *Bot) schedulesCommand(ctx context.Context, sender string, args []string) string {
	if b.opt.Schedules == nil {
		return "Schedules are turned off (SCHEDULES=false)."
	}
	if len(args) == 0 {
		return b.schedulesText(sender, true)
	}
	usage := "Use /schedules, or /schedules delete, pause, resume or run with a number."
	if len(args) != 2 {
		return usage
	}
	id, err := strconv.Atoi(args[1])
	if err != nil {
		return usage
	}
	action := strings.ToLower(args[0])
	var reply string
	err = b.opt.Schedules.Update(sender, func(f *schedule.File) error {
		it, ok := f.Find(id)
		if !ok {
			reply = fmt.Sprintf("There's no schedule %d. Send /schedules to see them.", id)
			return nil
		}
		switch action {
		case "delete", "remove":
			f.Remove(id)
			reply = fmt.Sprintf("Deleted schedule %d.", id)
		case "pause":
			it.Paused = true
			reply = fmt.Sprintf("Paused schedule %d.", id)
		case "resume":
			it.Paused = false
			if spec, err := it.Spec(b.loc()); err == nil {
				it.Next = spec.Next(time.Now().In(b.loc()), time.Time{})
			}
			reply = fmt.Sprintf("Resumed schedule %d.", id)
		case "run":
			reply = fmt.Sprintf("Running schedule %d now.", id)
		default:
			reply = usage
		}
		return nil
	})
	if err != nil {
		return "Couldn't change schedules: " + err.Error()
	}
	if action == "run" && strings.HasPrefix(reply, "Running") {
		b.queueSchedule(ctx, sender, id)
	}
	return reply
}

// schedulesSummary is the "Schedules:" value for /status.
func (b *Bot) schedulesSummary(sender string) string {
	f, err := b.opt.Schedules.Get(sender)
	if err != nil || len(f.Items) == 0 {
		return "none"
	}
	var next time.Time
	for _, it := range f.Items {
		if !it.Paused && !it.Next.IsZero() && (next.IsZero() || it.Next.Before(next)) {
			next = it.Next
		}
	}
	s := fmt.Sprint(len(f.Items))
	if !next.IsZero() {
		s += ", next " + next.In(b.loc()).Format("Mon 15:04")
	}
	return s
}

func argString(args map[string]any, k string) string {
	if s, ok := args[k].(string); ok {
		return s
	}
	return ""
}

func argBool(args map[string]any, k string) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		ok, _ := strconv.ParseBool(v)
		return ok
	}
	return false
}

func argInt(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(v, "#")))
		return n
	}
	return 0
}
