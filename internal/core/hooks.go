package core

// Hooks: events sent to the bot by other systems (POST /hooks/<name>), checked by the model
// against the hook's task, or passed on as they are.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tfielding/llamero/internal/toolbox"
)

// Hook is one configured hook.
type Hook struct {
	Name     string
	Task     string        // what the model does with the events; empty = send them on unchanged
	OnlyIf   string        // report only when this is true; empty = always report
	To       []string      // people who get the result; empty (and no Groups) = everyone allowed
	Groups   []string      // group chats that also get it; they can't talk back, snooze or follow up
	Wake     bool          // wake the Ollama machine if it's asleep, instead of sending the events unchecked
	Group    time.Duration // events within this long of the first are handled together
	Cooldown time.Duration // after a message, events with the same key are skipped for this long; 0 = off
	Model    string        // the model for its checks; empty = the recipient's chat model
	Think    string        // thinking for its checks; empty = the chat's setting
	Tools    []string      // the only tools its checks get; empty = every read-only tool
	Title    string        // the first line of its messages; empty = "🔔 <name>", "none" = no title line
}

const (
	maxHookEvents     = 10   // events kept per group; more are counted, not listed
	maxHookEventChars = 1000 // longer event texts are cut
)

// hookRun is a group of events waiting for, or being handled in, one run.
type hookRun struct {
	name    string
	events  []string
	dropped int             // events past maxHookEvents
	keys    map[string]bool // the events' keys (e.g. a camera), for the cooldown; "" = no key

	// A recheck (/hooks replay) of events that first went out unchecked: when they did, and who
	// asked. Zero for a run of new events.
	missedAt  time.Time
	requester string

	// A replay (/hooks replay <number>) of an earlier run, whose answer only goes to whoever
	// asked. Nil otherwise.
	replay *hookEntry
}

// missedRun is a run whose events went out unchecked, kept so /hooks replay can check them.
type missedRun struct {
	run *hookRun
	why string
}

const (
	maxMissed = 20 // unchecked runs kept for /hooks replay; older ones are dropped
	maxReplay = 10 // runs one /hooks replay <numbers> checks
)

// Hook takes an event for the hook called name, and reports false if there's no such hook. The
// event is handled after the hook's grouping window, together with any others that arrive in it,
// unless the hook is cooling down for key (e.g. the camera) after a message. ctx is the bot's,
// not the request's: the run outlives the request.
func (b *Bot) Hook(ctx context.Context, name, text, key string) bool {
	h, ok := b.opt.Hooks[name]
	if !ok {
		return false
	}
	key = strings.TrimSpace(key)
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > maxHookEventChars {
		text = string(r[:maxHookEventChars]) + "…"
	}
	skipped := &hookRun{name: name, events: []string{text}, keys: map[string]bool{key: true}}
	b.hooks.mu.Lock()
	until := b.hooks.cooldowns[name+"\x00"+key]
	b.hooks.mu.Unlock()
	if time.Now().Before(until) {
		slog.Info("hook event skipped: cooling down", "hook", name, "key", key, "for", time.Until(until).Round(time.Second))
		b.logHook(skipped, outCooling, fmt.Sprintf("skipped: cooling down for %s", time.Until(until).Round(time.Second)), 0)
		return true
	}
	if to, left := b.awake(h, map[string]bool{key: true}); len(to) == 0 && len(h.Groups) == 0 && left > 0 {
		slog.Info("hook event skipped: snoozed", "hook", name, "key", key, "for", left.Round(time.Second))
		b.logHook(skipped, outSnoozed, "skipped: snoozed for "+humanDuration(left), 0)
		return true
	}
	b.hooks.mu.Lock()
	run, waiting := b.hooks.runs[name]
	if !waiting {
		run = &hookRun{name: name, keys: map[string]bool{}}
		b.hooks.runs[name] = run
	}
	run.keys[key] = true
	if len(run.events) < maxHookEvents {
		run.events = append(run.events, text)
	} else {
		run.dropped++
	}
	b.hooks.mu.Unlock()
	slog.Info("hook event", "hook", name, "chars", len(text), "grouped", waiting)
	if waiting {
		return true
	}
	if h.Task != "" {
		b.workers.Add(1)
		go func() {
			defer b.workers.Done()
			b.warmUp(ctx, h)
		}()
	}
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		if h.Group > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(h.Group):
			}
		}
		b.hooks.mu.Lock()
		run := b.hooks.runs[name]
		delete(b.hooks.runs, name)
		b.hooks.mu.Unlock()
		// The run goes in the first recipient's queue, behind their own messages, and uses
		// their chat's model (a group's, which is the default, if it only goes to groups).
		to := slices.Concat(b.hookRecipients(h), h.Groups)
		if len(to) == 0 || !b.enqueue(ctx, to[0], message{hook: run}) {
			slog.Warn("hook events dropped", "hook", name, "events", len(run.events)+run.dropped)
		}
	}()
	return true
}

// warmUp has Ollama load the model a hook's check will use while the events are being grouped,
// so the check doesn't wait for it, e.g. after Ollama unloaded it when idle. It's only a head
// start: if it fails, the check finds out for itself.
func (b *Bot) warmUp(ctx context.Context, h Hook) {
	model := h.Model
	if model == "" {
		to := slices.Concat(b.hookRecipients(h), h.Groups)
		if len(to) == 0 {
			return
		}
		chat, err := b.store.Load(to[0]) // the check runs in the first recipient's chat
		if err != nil {
			return
		}
		model = b.modelOf(chat)
	}
	start := time.Now()
	if err := b.llm.Load(ctx, model); err != nil {
		if ctx.Err() == nil {
			slog.Info("hook: couldn't load the model ahead of the check", "hook", h.Name, "model", model, "err", short(err))
		}
		return
	}
	slog.Info("hook: model ready for the check", "hook", h.Name, "model", model, "took", time.Since(start).Round(100*time.Millisecond))
}

// titled puts the hook's title line (and a note, if any, in brackets) above body. With bold set
// (for Markdown messages from the model), the title is bold.
func (h Hook) titled(note, body string, bold bool) string {
	title := h.Title
	if strings.EqualFold(title, "none") {
		if note == "" {
			return body
		}
		return "(" + note + ")\n\n" + body
	}
	switch {
	case title == "" && bold:
		title = "🔔 **" + h.Name + "**"
	case title == "":
		title = "🔔 " + h.Name
	case bold:
		title = "**" + title + "**"
	}
	if note != "" {
		title += " (" + note + ")"
	}
	return title + "\n\n" + body
}

// hookRecipients is which people get a hook's messages: its To, or everyone allowed if it has no
// To and no Groups. Groups get them too, but aren't people: see Hook.Groups.
func (b *Bot) hookRecipients(h Hook) []string {
	if len(h.To) > 0 || len(h.Groups) > 0 {
		return h.To
	}
	var all []string
	for s := range b.opt.Allowed {
		all = append(all, s)
	}
	sort.Strings(all)
	return all
}

// runHook handles a group of events: checked by the model when the hook has a task, otherwise
// sent on as they are.
func (b *Bot) runHook(ctx context.Context, sender string, run *hookRun) {
	h := b.opt.Hooks[run.name]
	if run.replay != nil {
		b.replayRun(ctx, sender, h, run)
		return
	}
	// Someone may have snoozed the hook while the events were being grouped. Groups can't snooze.
	people, left := b.awake(h, run.keys)
	to := slices.Concat(people, h.Groups)
	recheck := !run.missedAt.IsZero()
	prefix := ""
	if recheck {
		prefix = "rechecked: "
	}
	if len(to) == 0 {
		slog.Info("hook events skipped: snoozed", "hook", h.Name, "events", len(run.events)+run.dropped)
		b.logHook(run, outSnoozed, prefix+"skipped: snoozed for "+humanDuration(left), 0)
		b.tellRequester(ctx, run, "skipped, snoozed for "+humanDuration(left))
		return
	}
	start := time.Now()
	events := eventList(run)
	if h.Task == "" {
		b.sendHook(ctx, h, run, to, response{text: h.titled("", events, false)})
		slog.Info("hook events sent on", "hook", h.Name, "events", len(run.events)+run.dropped)
		b.logHook(run, outForwarded, "sent on unchecked (no task)", 0)
		return
	}
	c := b.checkHook(ctx, sender, h, events)
	switch {
	case c.shutdown:
		return
	case c.stopped:
		slog.Info("hook check stopped", "hook", h.Name)
		b.logHook(run, outStopped, prefix+"stopped with /stop", time.Since(start))
		return
	case c.why != "" && recheck: // they were sent unchecked already: keep them for another try instead
		slog.Warn("hook recheck failed", "hook", h.Name, "reason", c.why)
		b.logHook(run, outUnchecked, "recheck failed: "+c.why, time.Since(start))
		b.addMissed(run, c.why)
		b.tellRequester(ctx, run, "still couldn't check: "+c.why+". /hooks replay tries again.")
		return
	case c.why != "":
		slog.Warn("hook events sent unchecked", "hook", h.Name, "reason", c.why)
		b.logHook(run, outUnchecked, "sent unchecked: "+c.why, time.Since(start))
		b.sendHook(ctx, h, run, to, response{text: h.titled("not checked: "+c.why, events, false)})
		run.missedAt = time.Now()
		b.addMissed(run, c.why)
		return
	case c.nothing != "":
		slog.Info("hook check: nothing to report", "hook", h.Name, "took", time.Since(start).Round(100*time.Millisecond))
		b.logHook(run, outNothing, prefix+c.nothing, time.Since(start))
		b.tellRequester(ctx, run, c.nothing)
		return
	}
	note := ""
	if recheck {
		note = "rechecked, events from " + b.clock(run.missedAt)
	}
	b.sendHook(ctx, h, run, to, response{text: h.titled(note, c.answer, true), fromModel: true, images: c.images})
	slog.Info("hook check sent", "hook", h.Name, "chars", len(c.answer), "images", len(c.images), "took", time.Since(start).Round(100*time.Millisecond), "recheck", recheck)
	b.logHook(run, outSent, prefix+"sent", time.Since(start))
	if recheck && !slices.Contains(to, run.requester) {
		b.tellRequester(ctx, run, "sent")
	}
	for _, r := range people { // groups can't follow up, so they have no conversation to add to
		b.remember(r, fmt.Sprintf("(Event from %s: %s)", h.Name, strings.ReplaceAll(events, "\n", " ")), c.answer, len(c.images))
	}
}

// hookCheck is how the model's check of a run's events went. One of these is set.
type hookCheck struct {
	answer   string // what to send
	images   []toolbox.Image
	nothing  string // nothing to report: "nothing to report (the model's reason)"
	why      string // the events couldn't be checked, and why
	stopped  bool   // with /stop
	shutdown bool   // the bot is shutting down
}

// checkHook has the model check a hook's events against its task, waking the Ollama machine
// first if the hook says to.
func (b *Bot) checkHook(ctx context.Context, sender string, h Hook, events string) hookCheck {
	if err := b.llm.Ping(ctx); err != nil {
		switch {
		case h.Wake && b.opt.Wake != nil && b.wakeQuietly(ctx):
		case h.Wake && b.opt.Wake != nil:
			return hookCheck{why: "Ollama is unreachable, and a wake-up didn't bring it online"}
		default:
			return hookCheck{why: "Ollama is unreachable"}
		}
	}
	// Checks don't need the chat: no history, so every model call has less to read.
	answer, images, stopped, err := b.checkRun(ctx, sender, "checking "+h.Name, hookTask(h, events),
		checkOptions{model: h.Model, think: h.Think, tools: h.Tools, noHistory: true})
	answer = stripImageNotes(answer)
	if err == nil && !stopped && answer == "" { // only a note about pictures: that's no answer either
		err = noAnswer{}
	}
	switch {
	case stopped:
		return hookCheck{stopped: true}
	case err != nil && ctx.Err() != nil:
		return hookCheck{shutdown: true}
	case err != nil && unreachable(err):
		return hookCheck{why: "lost the connection to Ollama (" + short(err) + ")"}
	case errors.As(err, new(noAnswer)):
		return hookCheck{why: "the model gave no answer"}
	case err != nil:
		return hookCheck{why: "the model returned an error (" + short(err) + ")"}
	case h.OnlyIf != "" && nothing(answer): // pictures it looked at to decide don't count
		outcome := "nothing to report"
		if why := nothingReason(answer); why != "" {
			outcome += " (" + why + ")"
		}
		return hookCheck{nothing: outcome}
	}
	return hookCheck{answer: answer, images: images}
}

// replayRun checks an earlier run's events again with the hook's current settings, and tells
// only sender what it decides, next to what it decided before. Nothing else changes: no
// cooldown, no log entry, nothing in anyone's conversation.
func (b *Bot) replayRun(ctx context.Context, sender string, h Hook, run *hookRun) {
	e := run.replay
	start := time.Now()
	c := b.checkHook(ctx, sender, h, eventList(run))
	slog.Info("hook replay", "hook", h.Name, "run", e.id, "took", time.Since(start).Round(100*time.Millisecond))
	var r response
	switch {
	case c.shutdown, c.stopped:
		return
	case c.why != "":
		r.text = fmt.Sprintf("%s: couldn't check: %s.", b.entryLabel(*e), c.why)
	case c.nothing != "":
		r.text = fmt.Sprintf("%s: %s. Before: %s.", b.entryLabel(*e), c.nothing, e.outcome)
	default:
		note := fmt.Sprintf("replay of #%d from %s, only to you", e.id, b.clock(e.at))
		r = response{text: h.titled(note, c.answer+"\n\nBefore: "+e.outcome+".", true), fromModel: true, images: c.images}
	}
	if err := b.send(ctx, sender, r); err != nil {
		slog.Error("send failed", "to", mask(sender), "err", err)
	}
}

// sendHook sends a hook's message, and starts its cooldown for the keys of the events in it.
// A recheck doesn't: its events are old, and new ones for the same keys shouldn't be held back.
func (b *Bot) sendHook(ctx context.Context, h Hook, run *hookRun, to []string, r response) {
	if h.Cooldown > 0 && run.missedAt.IsZero() {
		until := time.Now().Add(h.Cooldown)
		b.hooks.mu.Lock()
		for k, t := range b.hooks.cooldowns {
			if time.Now().After(t) {
				delete(b.hooks.cooldowns, k)
			}
		}
		for key := range run.keys {
			b.hooks.cooldowns[h.Name+"\x00"+key] = until
		}
		b.hooks.mu.Unlock()
	}
	for _, recipient := range to {
		if err := b.send(ctx, recipient, r); err != nil {
			slog.Error("send failed", "to", mask(recipient), "err", err)
		}
	}
}

// eventList lists a run's events, one per line.
func eventList(run *hookRun) string {
	var sb strings.Builder
	for _, e := range run.events {
		if len(run.events) > 1 {
			sb.WriteString("- ")
		}
		sb.WriteString(e)
		sb.WriteString("\n")
	}
	if run.dropped > 0 {
		fmt.Fprintf(&sb, "(and %d more)\n", run.dropped)
	}
	return strings.TrimSpace(sb.String())
}

// hookTask is the message the model gets for a hook's events.
func hookTask(h Hook, events string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "(Sent by another system through the hook %q, set up by the user. This is not a new message from them.)\n", h.Name)
	fmt.Fprintf(&sb, "What happened:\n%s\n\n", events)
	fmt.Fprintf(&sb, "Do this now, using your tools to look things up: %s\n", h.Task)
	if h.OnlyIf != "" {
		fmt.Fprintf(&sb, "Only report if this is true: %s. If it isn't, reply with exactly %s and nothing else.\n", h.OnlyIf, nothingToReport)
	}
	sb.WriteString("You can only look things up here, not take actions. Keep the answer short: it's sent to the user's phone.")
	return sb.String()
}

// CheckHookTools logs, at startup, each hook's tools its checks can't use, with the reason, so a
// misspelled name or a tool that needs approval doesn't only show up when an event arrives.
func (b *Bot) CheckHookTools(ctx context.Context) {
	if b.opt.Tools == nil {
		return
	}
	problems := HookToolProblems(b.opt.Hooks, b.opt.Tools.Tools(ctx))
	for _, name := range slices.Sorted(maps.Keys(problems)) {
		slog.Warn("hook lists tools its checks can't use", "hook", name, "tools", strings.Join(problems[name], "; "))
	}
}

// HookToolProblems is, for each hook whose tools list has tools its checks can't use, those
// tools and why, given all the tools there are.
func HookToolProblems(hooks map[string]Hook, all []toolbox.Tool) map[string][]string {
	usable := map[string]bool{}
	for _, t := range all {
		usable[t.Name] = t.ReadOnly
	}
	out := map[string][]string{}
	for name, h := range hooks {
		var missing []string
		for _, t := range h.Tools {
			if !usable[t] {
				missing = append(missing, t)
			}
		}
		if len(missing) > 0 {
			out[name] = toolProblems(all, missing)
		}
	}
	return out
}

// toolProblems says why each of names isn't among the tools a check can use: it needs approval,
// it's probably a different name, or it's unknown (or its server is down).
func toolProblems(all []toolbox.Tool, names []string) []string {
	byName := map[string]toolbox.Tool{}
	known := make([]string, 0, len(all))
	for _, t := range all {
		byName[t.Name] = t
		known = append(known, t.Name)
	}
	out := make([]string, len(names))
	for i, n := range names {
		switch t, ok := byName[n]; {
		case ok && !t.ReadOnly:
			out[i] = n + " (not read-only, so checks can't use it; if it only looks things up, add it to its server's read_only)"
		case ok:
			out[i] = n
		default:
			if alt := toolbox.Suggest(n, known); alt != "" {
				out[i] = n + " (no such tool; did you mean " + alt + "?)"
			} else {
				out[i] = n + " (no such tool, its server is down, or allow_write: false hides it)"
			}
		}
	}
	return out
}

// addMissed keeps a run whose events went out unchecked, for /hooks replay.
func (b *Bot) addMissed(run *hookRun, why string) {
	b.hooks.mu.Lock()
	b.hooks.missed = append(b.hooks.missed, missedRun{run: run, why: why})
	if len(b.hooks.missed) > maxMissed {
		b.hooks.missed = b.hooks.missed[len(b.hooks.missed)-maxMissed:]
	}
	b.hooks.mu.Unlock()
	b.saveHookState()
}

// missedCount is how many unchecked runs /hooks replay would check.
func (b *Bot) missedCount() int {
	b.hooks.mu.Lock()
	defer b.hooks.mu.Unlock()
	return len(b.hooks.missed)
}

// replayCommand answers /hooks replay. Without numbers, the runs that went out unchecked are
// checked again and what's found goes to the hook's people as usual. With numbers from /hooks,
// those runs are checked again with the hooks' current settings, for sender only. Either way
// the checks go in sender's queue and use their chat model, unless the hook sets one.
func (b *Bot) replayCommand(ctx context.Context, sender string, args []string) string {
	if len(args) > 0 {
		ids, err := parseRunNumbers(args)
		if err != nil {
			return err.Error()
		}
		return b.replayRuns(ctx, sender, ids)
	}
	b.hooks.mu.Lock()
	runs := b.hooks.missed
	b.hooks.missed = nil
	b.hooks.mu.Unlock()
	if len(runs) == 0 {
		return "Nothing went out unchecked. Events that do (Ollama down, or the model failed) are checked again with /hooks replay. To check earlier runs again with a hook's current settings, for you only: /hooks replay <number from /hooks>."
	}
	b.saveHookState()
	var sb strings.Builder
	fmt.Fprintf(&sb, "Checking %s again, oldest first:\n", plural(len(runs), "missed hook run"))
	for _, m := range runs {
		_, known := b.opt.Hooks[m.run.name]
		if !known { // no longer in the config
			continue
		}
		m.run.requester = sender
		if !b.enqueue(ctx, sender, message{hook: m.run}) {
			b.addMissed(m.run, m.why)
			continue
		}
		fmt.Fprintf(&sb, "\n%s %s", b.clock(m.run.missedAt), m.run.name)
		if keys := runKeys(m.run); len(keys) > 0 {
			fmt.Fprintf(&sb, " [%s]", strings.Join(keys, ", "))
		}
		fmt.Fprintf(&sb, ": %s", m.why)
	}
	return sb.String()
}

// replayRuns queues replays of the runs numbered ids in /hooks.
func (b *Bot) replayRuns(ctx context.Context, sender string, ids []int) string {
	b.hooks.mu.Lock()
	byID := map[int]hookEntry{}
	for _, e := range b.hooks.log {
		byID[e.id] = e
	}
	b.hooks.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("Checking again with the hooks' current settings. The answers come only to you:\n")
	for _, id := range ids {
		e, kept := byID[id]
		h, known := b.opt.Hooks[e.hook]
		switch {
		case !kept:
			fmt.Fprintf(&sb, "\n#%d: not kept any more (/hooks lists the ones that are)", id)
			continue
		case !known:
			fmt.Fprintf(&sb, "\n#%d %s: that hook isn't set up any more", id, e.hook)
			continue
		case h.Task == "":
			fmt.Fprintf(&sb, "\n#%d %s: that hook has no task, so there's nothing to check", id, e.hook)
			continue
		}
		run := &hookRun{name: e.hook, events: e.events, dropped: e.dropped, keys: map[string]bool{}, requester: sender, replay: &e}
		for _, k := range e.keys {
			run.keys[k] = true
		}
		if !b.enqueue(ctx, sender, message{hook: run}) {
			fmt.Fprintf(&sb, "\n#%d %s: couldn't queue it, try again in a moment", id, e.hook)
			continue
		}
		fmt.Fprintf(&sb, "\n#%d %s %s", id, b.clock(e.at), e.hook)
		if len(e.keys) > 0 {
			fmt.Fprintf(&sb, " [%s]", strings.Join(e.keys, ", "))
		}
		fmt.Fprintf(&sb, ", before: %s", e.outcome)
	}
	return sb.String()
}

// parseRunNumbers reads the run numbers after /hooks replay: "14", "#14", "12-15", several.
func parseRunNumbers(args []string) ([]int, error) {
	usage := errors.New("/hooks replay takes numbers from /hooks: /hooks replay 14, /hooks replay 12-15, or /hooks replay alone for the runs that went out unchecked.")
	var ids []int
	for _, a := range args {
		lo, hi, isRange := strings.Cut(strings.TrimPrefix(a, "#"), "-")
		from, err := strconv.Atoi(lo)
		to := from
		if err == nil && isRange {
			to, err = strconv.Atoi(strings.TrimPrefix(hi, "#"))
		}
		if err != nil || from < 1 || to < from {
			return nil, usage
		}
		for id := from; id <= to; id++ {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
			if len(ids) > maxReplay {
				return nil, fmt.Errorf("At most %d runs at a time: each one is a full check.", maxReplay)
			}
		}
	}
	return ids, nil
}

// tellRequester tells whoever asked for a recheck how it went, when the hook's own message
// won't reach them.
func (b *Bot) tellRequester(ctx context.Context, run *hookRun, outcome string) {
	if run.requester == "" {
		return
	}
	text := fmt.Sprintf("%s (events from %s): %s", run.name, b.clock(run.missedAt), outcome)
	if err := b.send(ctx, run.requester, response{text: text}); err != nil {
		slog.Error("send failed", "to", mask(run.requester), "err", err)
	}
}
