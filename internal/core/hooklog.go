package core

// /hooks: the latest hook checks and what each decided, for tuning a hook's rules. Saved with
// the hook state, events and all, so /hooks replay can check them again after a restart.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	hookLogSize   = 20  // checks remembered
	hookLogEvent  = 80  // characters of the event shown
	hookLogReason = 120 // characters of the model's reason shown
)

type hookEntry struct {
	id      int // its number in /hooks, for /hooks replay
	at      time.Time
	hook    string
	keys    []string // the events' keys, e.g. cameras
	events  []string // the events themselves, for /hooks replay
	dropped int
	outcome string // "sent", "nothing to report (…)", "skipped: cooling down", …
	took    time.Duration
}

// logHook remembers how a run's check (or a skipped event) went, for /hooks, /hooks stats and
// /hooks replay.
func (b *Bot) logHook(run *hookRun, kind hookOutcome, outcome string, took time.Duration) {
	keys := runKeys(run)
	b.hooks.mu.Lock()
	b.hooks.lastID++
	e := hookEntry{id: b.hooks.lastID, at: time.Now(), hook: run.name, keys: keys, events: slices.Clone(run.events),
		dropped: run.dropped, outcome: outcome, took: took}
	b.countHook(run.name, kind, keys, strings.TrimPrefix(outcome, "sent unchecked: "), took)
	b.hooks.log = append(b.hooks.log, e)
	if len(b.hooks.log) > hookLogSize {
		b.hooks.log = b.hooks.log[len(b.hooks.log)-hookLogSize:]
	}
	b.hooks.mu.Unlock()
	b.saveHookState()
}

// firstEvent is the entry's first event, saying how many more there were.
func (e hookEntry) firstEvent() string {
	return firstEvent(&hookRun{events: e.events, dropped: e.dropped})
}

// entryLabel is how replies name the entry: "door #14 (10:42)".
func (b *Bot) entryLabel(e hookEntry) string {
	return fmt.Sprintf("%s #%d (%s)", e.hook, e.id, b.clock(e.at))
}

// runKeys are a run's event keys, sorted, without the empty one.
func runKeys(run *hookRun) []string {
	var out []string
	for k := range run.keys {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// firstEvent is a run's first event, saying how many more there were.
func firstEvent(run *hookRun) string {
	if len(run.events) == 0 {
		return ""
	}
	s := run.events[0]
	if more := len(run.events) - 1 + run.dropped; more > 0 {
		s = fmt.Sprintf("%s (+%d more)", s, more)
	}
	return s
}

// nothingReason is the reason the model gave after NOTHING_TO_REPORT, if any.
func nothingReason(answer string) string {
	a := strings.TrimSpace(strings.Trim(strings.TrimSpace(answer), "`*_\"'"))
	if i := strings.Index(strings.ToUpper(a), nothingToReport); i >= 0 {
		a = a[i+len(nothingToReport):]
	}
	return shorten(oneLine(strings.Trim(a, "`*_.:;,-–— \"'")), hookLogReason)
}

// hooksCommand answers /hooks.
func (b *Bot) hooksCommand(ctx context.Context, sender string, args []string) string {
	if len(b.opt.Hooks) == 0 {
		return "No hooks are set up (webhooks.hooks in config.yml)."
	}
	switch {
	case len(args) == 1 && strings.EqualFold(args[0], "stats"):
		return b.hookStatsText()
	case len(args) > 0 && strings.EqualFold(args[0], "replay"):
		return b.replayCommand(ctx, sender, args[1:])
	case len(args) > 0:
		return "/hooks shows the latest checks; /hooks stats the totals since the bot started; /hooks replay checks the events that went out unchecked, and /hooks replay <number> checks one from /hooks again, for you only."
	}
	b.hooks.mu.Lock()
	log := append([]hookEntry(nil), b.hooks.log...)
	b.hooks.mu.Unlock()
	if len(log) == 0 {
		msg := "No hook events yet."
		if n := b.missedCount(); n > 0 {
			msg += fmt.Sprintf("\n%s went out unchecked: /hooks replay checks them now.", plural(n, "run"))
		}
		return msg
	}
	var sb strings.Builder
	sb.WriteString("Latest hook checks, newest first:\n")
	for i := len(log) - 1; i >= 0; i-- {
		e := log[i]
		fmt.Fprintf(&sb, "\n#%d %s %s", e.id, b.clock(e.at), e.hook)
		if len(e.keys) > 0 {
			fmt.Fprintf(&sb, " [%s]", strings.Join(e.keys, ", "))
		}
		fmt.Fprintf(&sb, ": %s", e.outcome)
		if e.took >= time.Second {
			fmt.Fprintf(&sb, ", %s", e.took.Round(time.Second))
		}
		if ev := e.firstEvent(); ev != "" {
			fmt.Fprintf(&sb, "\n  %s", shorten(oneLine(ev), hookLogEvent))
		}
	}
	sb.WriteString("\n\nThe last 20. /hooks replay <number> checks one again with the hook's current settings, for you only. /hooks stats for the totals.")
	if n := b.missedCount(); n > 0 {
		them := "them"
		if n == 1 {
			them = "it"
		}
		fmt.Fprintf(&sb, "\n%s went out unchecked: /hooks replay checks %s now and sends what it finds.", plural(n, "run"), them)
	}
	return sb.String()
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
