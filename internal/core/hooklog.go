package core

// /hooks: the latest hook checks and what each decided, for tuning a hook's rules. Kept in
// memory only.

import (
	"fmt"
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
	at      time.Time
	hook    string
	keys    string // the events' keys, e.g. cameras
	event   string // the first event, cut
	outcome string // "sent", "nothing to report (…)", "skipped: cooling down", …
	took    time.Duration
}

// logHook remembers how a check (or a skipped event) went, for /hooks and /hooks stats.
func (b *Bot) logHook(hook string, kind hookOutcome, keys []string, event, outcome string, took time.Duration) {
	e := hookEntry{at: time.Now(), hook: hook, keys: strings.Join(keys, ", "), event: shorten(oneLine(event), hookLogEvent),
		outcome: outcome, took: took}
	b.mu.Lock()
	b.countHook(hook, kind, keys, strings.TrimPrefix(outcome, "sent unchecked: "), took)
	b.hookLog = append(b.hookLog, e)
	if len(b.hookLog) > hookLogSize {
		b.hookLog = b.hookLog[len(b.hookLog)-hookLogSize:]
	}
	b.mu.Unlock()
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
func (b *Bot) hooksCommand(args []string) string {
	if len(b.opt.Hooks) == 0 {
		return "No hooks are set up (webhooks.hooks in config.yml)."
	}
	switch {
	case len(args) == 1 && strings.EqualFold(args[0], "stats"):
		return b.hookStatsText()
	case len(args) > 0:
		return "/hooks shows the latest checks; /hooks stats the totals since the bot started."
	}
	b.mu.Lock()
	log := append([]hookEntry(nil), b.hookLog...)
	b.mu.Unlock()
	if len(log) == 0 {
		return "No hook events since the bot started."
	}
	var sb strings.Builder
	sb.WriteString("Latest hook checks, newest first:\n")
	for i := len(log) - 1; i >= 0; i-- {
		e := log[i]
		fmt.Fprintf(&sb, "\n%s %s", e.at.In(b.loc()).Format("15:04"), e.hook)
		if e.keys != "" {
			fmt.Fprintf(&sb, " [%s]", e.keys)
		}
		fmt.Fprintf(&sb, ": %s", e.outcome)
		if e.took >= time.Second {
			fmt.Fprintf(&sb, ", %s", e.took.Round(time.Second))
		}
		if e.event != "" {
			fmt.Fprintf(&sb, "\n  %s", e.event)
		}
	}
	sb.WriteString("\n\nKept since the bot started, up to 20. /hooks stats for the totals.")
	return sb.String()
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
