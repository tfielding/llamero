package core

// /snooze: mute hooks for a while, for the person who asks. Kept in memory only, so a restart
// ends every snooze (messages come back rather than staying muted).

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxSnooze = 7 * 24 * time.Hour

// snoozeKey is what one snooze mutes for one person: hook "" = every hook, key "" = every key.
type snoozeKey struct{ who, hook, key string }

// snoozedFor is how much longer who gets no messages from hook for events with these keys:
// every key has to be muted, by a snooze on the key, the whole hook or every hook. 0 = not
// snoozed.
func (b *Bot) snoozedFor(who, hook string, keys map[string]bool) time.Duration {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	whole := later(b.snoozes[snoozeKey{who, "", ""}], b.snoozes[snoozeKey{who, hook, ""}])
	least := time.Duration(-1)
	for k := range keys {
		until := whole
		if k != "" {
			until = later(until, b.snoozes[snoozeKey{who, hook, strings.ToLower(k)}])
		}
		left := until.Sub(now)
		if left <= 0 {
			return 0
		}
		if least < 0 || left < least {
			least = left
		}
	}
	if least < 0 { // no keys
		return max(0, whole.Sub(now))
	}
	return least
}

// awake is who should get a hook's messages for events with these keys: its recipients that
// haven't snoozed them. When that's no one, left is how long until the first snooze ends.
func (b *Bot) awake(h Hook, keys map[string]bool) (to []string, left time.Duration) {
	for _, r := range b.hookRecipients(h) {
		d := b.snoozedFor(r, h.Name, keys)
		switch {
		case d == 0:
			to = append(to, r)
		case left == 0 || d < left:
			left = d
		}
	}
	return to, left
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// snoozeCommand answers /snooze [all|<hook> [key]] <duration|off>.
func (b *Bot) snoozeCommand(sender string, args []string) string {
	if len(b.opt.Hooks) == 0 {
		return "No hooks are set up (webhooks.hooks in config.yml)."
	}
	if len(args) == 0 {
		return b.snoozeList(sender)
	}
	if len(args) > 3 {
		return snoozeUsage
	}
	last, scope := strings.ToLower(args[len(args)-1]), args[:len(args)-1]
	hook, key := "", ""
	if len(scope) > 0 && !strings.EqualFold(scope[0], "all") {
		h, ok := b.hookNamed(scope[0])
		if !ok {
			return fmt.Sprintf("No hook named %q. Hooks: %s", scope[0], strings.Join(b.hookNames(), ", "))
		}
		if !b.receives(sender, h) {
			return fmt.Sprintf("You don't get %s's messages, so there's nothing to snooze.", h.Name)
		}
		hook = h.Name
	} else if len(scope) > 1 {
		return snoozeUsage
	}
	if len(scope) == 2 {
		key = strings.ToLower(scope[1])
	}
	what := "all hooks"
	if hook != "" {
		what = hook
		if key != "" {
			what += " [" + key + "]"
		}
	}

	if last == "off" {
		n := 0
		b.mu.Lock()
		for k := range b.snoozes {
			if k.who == sender && (hook == "" || k.hook == hook && (key == "" || k.key == key)) {
				delete(b.snoozes, k)
				n++
			}
		}
		b.mu.Unlock()
		if n == 0 {
			return "Nothing to unsnooze for " + what + "."
		}
		return "Unsnoozed " + what + "."
	}

	d, err := parseSnooze(last)
	if err != nil {
		return err.Error() + "\n\n" + snoozeUsage
	}
	until := time.Now().Add(d)
	b.mu.Lock()
	for k, t := range b.snoozes {
		if time.Now().After(t) {
			delete(b.snoozes, k)
		}
	}
	b.snoozes[snoozeKey{sender, hook, key}] = until
	b.mu.Unlock()
	return fmt.Sprintf("Snoozed %s for %s, until %s. /snooze off to undo.", what, humanDuration(d), b.clock(until))
}

// snoozeList answers /snooze on its own: what sender has snoozed.
func (b *Bot) snoozeList(sender string) string {
	type item struct {
		what  string
		until time.Time
	}
	var items []item
	now := time.Now()
	b.mu.Lock()
	for k, t := range b.snoozes {
		if k.who != sender || !t.After(now) {
			continue
		}
		what := "all hooks"
		if k.hook != "" {
			what = k.hook
			if k.key != "" {
				what += " [" + k.key + "]"
			}
		}
		items = append(items, item{what, t})
	}
	b.mu.Unlock()
	if len(items) == 0 {
		return "Nothing is snoozed.\n\n" + snoozeUsage
	}
	sort.Slice(items, func(i, j int) bool { return items[i].what < items[j].what })
	var sb strings.Builder
	sb.WriteString("Snoozed:\n")
	for _, it := range items {
		fmt.Fprintf(&sb, "- %s until %s (%s left)\n", it.what, b.clock(it.until), humanDuration(time.Until(it.until)))
	}
	sb.WriteString("\n/snooze off ends them all; /snooze <hook> off ends one.")
	return sb.String()
}

const snoozeUsage = `/snooze 2h - mute all hooks for 2 hours
/snooze <hook> 30m - mute one hook
/snooze <hook> <key> 1d - mute one hook's events for one key (e.g. a camera)
/snooze off - unmute (or /snooze <hook> off)
Durations: 30m, 2h, 1h30m, 1d, up to 7d.`

// parseSnooze reads a snooze length: a Go duration ("90m", "1h30m") or whole days ("2d").
func parseSnooze(s string) (time.Duration, error) {
	var d time.Duration
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("%q isn't a length of time", s)
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("%q isn't a length of time", s)
		}
	}
	switch {
	case d < time.Minute:
		return 0, fmt.Errorf("%s is too short: at least 1m", s)
	case d > maxSnooze:
		return 0, fmt.Errorf("%s is too long: at most 7d", s)
	}
	return d.Round(time.Minute), nil
}

// humanDuration is d as "2h 30m", "1d 4h" or "45m", rounded to the minute (at least 1m).
func humanDuration(d time.Duration) string {
	m := int((d + 30*time.Second) / time.Minute)
	if m < 1 {
		m = 1
	}
	days, hours, mins := m/(24*60), m/60%24, m%60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 && days == 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	return strings.Join(parts, " ")
}

// clock is t as a time of day, with the day when it isn't today.
func (b *Bot) clock(t time.Time) string {
	t, now := t.In(b.loc()), time.Now().In(b.loc())
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}

// hookNamed finds a hook by name, ignoring case.
func (b *Bot) hookNamed(name string) (Hook, bool) {
	if h, ok := b.opt.Hooks[name]; ok {
		return h, true
	}
	for n, h := range b.opt.Hooks {
		if strings.EqualFold(n, name) {
			return h, true
		}
	}
	return Hook{}, false
}

func (b *Bot) hookNames() []string {
	var names []string
	for n := range b.opt.Hooks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// receives reports whether who gets h's messages.
func (b *Bot) receives(who string, h Hook) bool {
	for _, r := range b.hookRecipients(h) {
		if r == who {
			return true
		}
	}
	return false
}
