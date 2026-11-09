package core

// /hooks stats: per hook, how events were handled since the bot started: how many were checked
// and sent, how many the model filtered out, how often a check couldn't run, and how long checks
// take. Kept in memory only.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// hookOutcome is how a hook's event, or group of events, was handled.
type hookOutcome int

const (
	outSent      hookOutcome = iota // checked, and a message sent
	outNothing                      // checked: nothing to report
	outUnchecked                    // sent without a check: Ollama down, or the model failed
	outForwarded                    // sent on as it is: the hook has no task
	outStopped                      // stopped with /stop
	outCooling                      // skipped: cooling down
	outSnoozed                      // skipped: snoozed
	numOutcomes
)

const (
	hookStatsTimes = 500 // check times kept per hook, for the typical and slowest
	hookStatsKeys  = 8   // keys listed per hook
)

type hookStats struct {
	counts    [numOutcomes]int
	unchecked map[string]int       // why checks couldn't run, without the error details
	times     []time.Duration      // how long checks took (sent or nothing to report), newest last
	keys      map[string]*keyStats // by event key, e.g. camera
}

type keyStats struct{ events, messages int }

// countHook adds a handled run (or skipped event) to the hook's stats. why is the reason for an
// unchecked one. The caller holds b.mu.
func (b *Bot) countHook(hook string, kind hookOutcome, keys []string, why string, took time.Duration) {
	s := b.hookStats[hook]
	if s == nil {
		s = &hookStats{unchecked: map[string]int{}, keys: map[string]*keyStats{}}
		b.hookStats[hook] = s
	}
	s.counts[kind]++
	switch kind {
	case outUnchecked:
		why, _, _ = strings.Cut(why, " (") // "the model returned an error (…)": the error varies
		s.unchecked[why]++
	case outSent, outNothing:
		s.times = append(s.times, took)
		if len(s.times) > hookStatsTimes {
			s.times = s.times[len(s.times)-hookStatsTimes:]
		}
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		ks := s.keys[k]
		if ks == nil {
			ks = &keyStats{}
			s.keys[k] = ks
		}
		ks.events++
		if kind == outSent || kind == outUnchecked || kind == outForwarded {
			ks.messages++
		}
	}
}

// hookStatsText answers /hooks stats.
func (b *Bot) hookStatsText() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Hook stats since the bot started (%s, %s ago):\n",
		b.boot.In(b.loc()).Format("Mon 2 Jan 15:04"), humanDuration(time.Since(b.boot)))
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, name := range b.hookNames() {
		fmt.Fprintf(&sb, "\n%s\n", name)
		s := b.hookStats[name]
		if s == nil {
			sb.WriteString("- no events yet\n")
			continue
		}
		c := s.counts
		if checked := c[outSent] + c[outNothing]; checked > 0 {
			fmt.Fprintf(&sb, "- %d checked: %d sent (%d%%), %d nothing to report", checked, c[outSent], percent(c[outSent], checked), c[outNothing])
			if c[outStopped] > 0 {
				fmt.Fprintf(&sb, "; %d stopped", c[outStopped])
			}
			sb.WriteString("\n")
		} else if c[outStopped] > 0 {
			fmt.Fprintf(&sb, "- %d stopped\n", c[outStopped])
		}
		if c[outUnchecked] > 0 {
			fmt.Fprintf(&sb, "- %d sent unchecked: %s\n", c[outUnchecked], tally(s.unchecked))
		}
		if c[outForwarded] > 0 {
			fmt.Fprintf(&sb, "- %d sent on (no task)\n", c[outForwarded])
		}
		if skipped := c[outCooling] + c[outSnoozed]; skipped > 0 {
			fmt.Fprintf(&sb, "- %d skipped: %s\n", skipped, tally(map[string]int{"cooling down": c[outCooling], "snoozed": c[outSnoozed]}))
		}
		if len(s.times) > 0 {
			t := append([]time.Duration(nil), s.times...)
			sort.Slice(t, func(i, j int) bool { return t[i] < t[j] })
			fmt.Fprintf(&sb, "- check time: typical %s, 9 in 10 under %s, slowest %s\n",
				checkTime(t[len(t)/2]), checkTime(t[min(len(t)-1, len(t)*9/10)]), checkTime(t[len(t)-1]))
		}
		if len(s.keys) > 0 {
			sb.WriteString("- by key: " + keyTally(s.keys) + "\n")
		}
	}
	sb.WriteString("\nChecked = the model looked; sent unchecked = it couldn't, so the events were sent as they are.")
	return sb.String()
}

func percent(n, of int) int { return (n*100 + of/2) / of }

// tally is counts as "Ollama is unreachable 2, the model gave no answer 1", biggest first,
// leaving out zeros.
func tally(counts map[string]int) string {
	var names []string
	for n, c := range counts {
		if c > 0 {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s %d", n, counts[n])
	}
	return strings.Join(parts, ", ")
}

// keyTally is the busiest keys as "front_door 20 (3 sent), garden 4 (0 sent)".
func keyTally(keys map[string]*keyStats) string {
	var names []string
	for n := range keys {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if keys[names[i]].events != keys[names[j]].events {
			return keys[names[i]].events > keys[names[j]].events
		}
		return names[i] < names[j]
	})
	var parts []string
	for i, n := range names {
		if i == hookStatsKeys {
			parts = append(parts, fmt.Sprintf("%d more", len(names)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d (%d sent)", n, keys[n].events, keys[n].messages))
	}
	return strings.Join(parts, ", ")
}

// checkTime is a check time: tenths of a second under 10s, whole seconds above.
func checkTime(d time.Duration) string {
	if d < 10*time.Second {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}
