package core

// Hooks: events sent to the bot by other systems (POST /hooks/<name>), checked by the model
// against the hook's task, or passed on as they are.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// Hook is one configured hook.
type Hook struct {
	Name     string
	Task     string        // what the model does with the events; empty = send them on unchanged
	OnlyIf   string        // report only when this is true; empty = always report
	To       []string      // who gets the result; empty = everyone allowed
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
}

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
	b.mu.Lock()
	until := b.cooldowns[name+"\x00"+key]
	b.mu.Unlock()
	if time.Now().Before(until) {
		slog.Info("hook event skipped: cooling down", "hook", name, "key", key, "for", time.Until(until).Round(time.Second))
		b.logHook(name, []string{key}, text, fmt.Sprintf("skipped: cooling down for %s", time.Until(until).Round(time.Second)), 0)
		return true
	}
	if r := []rune(strings.TrimSpace(text)); len(r) > maxHookEventChars {
		text = string(r[:maxHookEventChars]) + "…"
	}
	b.mu.Lock()
	run, waiting := b.hookRuns[name]
	if !waiting {
		run = &hookRun{name: name, keys: map[string]bool{}}
		b.hookRuns[name] = run
	}
	run.keys[key] = true
	if len(run.events) < maxHookEvents {
		run.events = append(run.events, strings.TrimSpace(text))
	} else {
		run.dropped++
	}
	b.mu.Unlock()
	slog.Info("hook event", "hook", name, "chars", len(text), "grouped", waiting)
	if waiting {
		return true
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
		b.mu.Lock()
		run := b.hookRuns[name]
		delete(b.hookRuns, name)
		b.mu.Unlock()
		// The run goes in the first recipient's queue, behind their own messages, and uses
		// their chat's model.
		to := b.hookRecipients(h)
		if len(to) == 0 || !b.enqueue(ctx, to[0], message{hook: run}) {
			slog.Warn("hook events dropped", "hook", name, "events", len(run.events)+run.dropped)
		}
	}()
	return true
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

// hookRecipients is who gets a hook's messages: its To, or everyone allowed.
func (b *Bot) hookRecipients(h Hook) []string {
	if len(h.To) > 0 {
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
	to := b.hookRecipients(h)
	start := time.Now()
	events := eventList(run)
	if h.Task == "" {
		b.sendHook(ctx, h, run, to, response{text: h.titled("", events, false)})
		slog.Info("hook events sent on", "hook", h.Name, "events", len(run.events)+run.dropped)
		b.logHook(h.Name, runKeys(run), firstEvent(run), "sent on unchecked (no task)", 0)
		return
	}
	unchecked := func(why string) {
		slog.Warn("hook events sent unchecked", "hook", h.Name, "reason", why)
		b.logHook(h.Name, runKeys(run), firstEvent(run), "sent unchecked: "+why, time.Since(start))
		b.sendHook(ctx, h, run, to, response{text: h.titled("not checked: "+why, events, false)})
	}
	if err := b.llm.Ping(ctx); err != nil {
		switch {
		case h.Wake && b.opt.Wake != nil && b.wakeQuietly(ctx):
		case h.Wake && b.opt.Wake != nil:
			unchecked("Ollama is unreachable, and a wake-up didn't bring it online")
			return
		default:
			unchecked("Ollama is unreachable")
			return
		}
	}
	// Checks don't need the chat: no history, so every model call has less to read.
	answer, images, stopped, err := b.checkRun(ctx, sender, "checking "+h.Name, hookTask(h, events),
		checkOptions{model: h.Model, think: h.Think, tools: h.Tools, noHistory: true})
	switch {
	case stopped:
		slog.Info("hook check stopped", "hook", h.Name)
		b.logHook(h.Name, runKeys(run), firstEvent(run), "stopped with /stop", time.Since(start))
		return
	case err != nil && ctx.Err() != nil:
		return // shutting down
	case err != nil && unreachable(err):
		unchecked("lost the connection to Ollama (" + short(err) + ")")
		return
	case errors.As(err, new(noAnswer)):
		unchecked("the model gave no answer")
		return
	case err != nil:
		unchecked("the model returned an error (" + short(err) + ")")
		return
	}
	answer = stripImageNotes(answer)
	if h.OnlyIf != "" && nothing(answer) { // pictures it looked at to decide don't count
		slog.Info("hook check: nothing to report", "hook", h.Name, "took", time.Since(start).Round(100*time.Millisecond))
		outcome := "nothing to report"
		if why := nothingReason(answer); why != "" {
			outcome += " (" + why + ")"
		}
		b.logHook(h.Name, runKeys(run), firstEvent(run), outcome, time.Since(start))
		return
	}
	b.sendHook(ctx, h, run, to, response{text: h.titled("", answer, true), fromModel: true, images: images})
	slog.Info("hook check sent", "hook", h.Name, "chars", len(answer), "images", len(images), "took", time.Since(start).Round(100*time.Millisecond))
	b.logHook(h.Name, runKeys(run), firstEvent(run), "sent", time.Since(start))
	for _, r := range to {
		b.remember(r, fmt.Sprintf("(Event from %s: %s)", h.Name, strings.ReplaceAll(events, "\n", " ")), answer, len(images))
	}
}

// sendHook sends a hook's message, and starts its cooldown for the keys of the events in it.
func (b *Bot) sendHook(ctx context.Context, h Hook, run *hookRun, to []string, r response) {
	if h.Cooldown > 0 {
		until := time.Now().Add(h.Cooldown)
		b.mu.Lock()
		for k, t := range b.cooldowns {
			if time.Now().After(t) {
				delete(b.cooldowns, k)
			}
		}
		for key := range run.keys {
			b.cooldowns[h.Name+"\x00"+key] = until
		}
		b.mu.Unlock()
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
