package bot

// Hooks: events sent to the bot by other systems (POST /hooks/<name>), checked by the model
// against the hook's task, or passed on as they are.

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// Hook is one configured hook.
type Hook struct {
	Name   string
	Task   string        // what the model does with the events; empty = send them on unchanged
	OnlyIf string        // report only when this is true; empty = always report
	To     []string      // who gets the result; empty = everyone allowed
	Wake   bool          // wake the Ollama machine if it's asleep, instead of sending the events unchecked
	Group  time.Duration // events within this long of the first are handled together
}

const (
	maxHookEvents     = 10   // events kept per group; more are counted, not listed
	maxHookEventChars = 1000 // longer event texts are cut
)

// hookRun is a group of events waiting for, or being handled in, one run.
type hookRun struct {
	name    string
	events  []string
	dropped int // events past maxHookEvents
}

// Hook takes an event for the hook called name, and reports false if there's no such hook. The
// event is handled after the hook's grouping window, together with any others that arrive in it.
// ctx is the bot's, not the request's: the run outlives the request.
func (b *Bot) Hook(ctx context.Context, name, text string) bool {
	h, ok := b.opt.Hooks[name]
	if !ok {
		return false
	}
	if r := []rune(strings.TrimSpace(text)); len(r) > maxHookEventChars {
		text = string(r[:maxHookEventChars]) + "…"
	}
	b.mu.Lock()
	run, waiting := b.hookRuns[name]
	if !waiting {
		run = &hookRun{name: name}
		b.hookRuns[name] = run
	}
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
		b.sendHook(ctx, to, response{text: fmt.Sprintf("🔔 %s\n\n%s", h.Name, events)})
		slog.Info("hook events sent on", "hook", h.Name, "events", len(run.events)+run.dropped)
		return
	}
	unchecked := func(why string) {
		slog.Warn("hook events sent unchecked", "hook", h.Name, "reason", why)
		b.sendHook(ctx, to, response{text: fmt.Sprintf("🔔 %s (not checked: %s)\n\n%s", h.Name, why, events)})
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
	answer, images, stopped, err := b.checkRun(ctx, sender, "checking "+h.Name, hookTask(h, events))
	switch {
	case stopped:
		slog.Info("hook check stopped", "hook", h.Name)
		return
	case err != nil && ctx.Err() != nil:
		return // shutting down
	case err != nil && unreachable(err):
		unchecked("lost the connection to Ollama (" + short(err) + ")")
		return
	case err != nil:
		unchecked("the model returned an error (" + short(err) + ")")
		return
	}
	answer = stripImageNotes(answer)
	if h.OnlyIf != "" && len(images) == 0 && nothing(answer) {
		slog.Info("hook check: nothing to report", "hook", h.Name, "took", time.Since(start).Round(100*time.Millisecond))
		return
	}
	b.sendHook(ctx, to, response{text: fmt.Sprintf("🔔 **%s**\n\n%s", h.Name, answer), fromModel: true, images: images})
	slog.Info("hook check sent", "hook", h.Name, "chars", len(answer), "images", len(images), "took", time.Since(start).Round(100*time.Millisecond))
	for _, r := range to {
		b.remember(r, fmt.Sprintf("(Event from %s: %s)", h.Name, strings.ReplaceAll(events, "\n", " ")), answer, len(images))
	}
}

func (b *Bot) sendHook(ctx context.Context, to []string, r response) {
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
