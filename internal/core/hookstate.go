package core

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"
)

// hookState is everything the bot keeps about hooks while it runs: events waiting to be
// handled, cooldowns, snoozes, and what happened to past runs (/hooks, /hooks stats,
// /hooks replay). It has its own lock, apart from the chats'.
//
// Snoozes, the latest checks and the runs waiting for /hooks replay are saved
// (Options.HookStateFile), so a restart doesn't unmute everything or lose what can be checked
// again. The rest starts over.
type hookState struct {
	mu        sync.Mutex
	runs      map[string]*hookRun     // events waiting for their run, by hook
	cooldowns map[string]time.Time    // until when each hook (and key) skips events, after a message
	snoozes   map[snoozeKey]time.Time // until when someone gets no messages from a hook (/snooze)
	log       []hookEntry             // the latest checks, for /hooks and /hooks replay
	lastID    int                     // the latest check's number
	missed    []missedRun             // runs whose events went out unchecked, for /hooks replay
	stats     map[string]*hookStats   // how each hook's events were handled, for /hooks stats

	saveMu sync.Mutex // one save at a time, so an older snapshot can't overwrite a newer one
}

func newHookState() hookState {
	return hookState{runs: map[string]*hookRun{}, cooldowns: map[string]time.Time{}, snoozes: map[snoozeKey]time.Time{},
		stats: map[string]*hookStats{}}
}

// savedHooks is the hook state file's layout.
type savedHooks struct {
	Snoozes []savedSnooze `json:"snoozes,omitempty"`
	Missed  []savedMissed `json:"missed,omitempty"`
	Log     []savedEntry  `json:"log,omitempty"`
}

type savedEntry struct {
	ID      int           `json:"id"`
	At      time.Time     `json:"at"`
	Hook    string        `json:"hook"`
	Keys    []string      `json:"keys,omitempty"`
	Events  []string      `json:"events"`
	Dropped int           `json:"dropped,omitempty"`
	Outcome string        `json:"outcome"`
	Took    time.Duration `json:"took,omitempty"`
}

type savedSnooze struct {
	Who   string    `json:"who"`
	Hook  string    `json:"hook,omitempty"`
	Key   string    `json:"key,omitempty"`
	Until time.Time `json:"until"`
}

type savedMissed struct {
	Hook    string    `json:"hook"`
	Events  []string  `json:"events"`
	Dropped int       `json:"dropped,omitempty"`
	Keys    []string  `json:"keys,omitempty"`
	At      time.Time `json:"at"`
	Why     string    `json:"why"`
}

// loadHookState reads the saved snoozes, checks and missed runs. A file that can't be read is logged
// and left alone: the bot starts without them rather than not at all.
func (b *Bot) loadHookState() {
	path := b.opt.HookStateFile
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var saved savedHooks
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil {
		slog.Warn("hook state unreadable, starting without saved snoozes and checks", "file", path, "err", err)
		return
	}
	now := time.Now()
	b.hooks.mu.Lock()
	defer b.hooks.mu.Unlock()
	for _, s := range saved.Snoozes {
		if s.Until.After(now) {
			b.hooks.snoozes[snoozeKey{s.Who, s.Hook, s.Key}] = s.Until
		}
	}
	for _, m := range saved.Missed {
		run := &hookRun{name: m.Hook, events: m.Events, dropped: m.Dropped, keys: map[string]bool{}, missedAt: m.At}
		for _, k := range m.Keys {
			run.keys[k] = true
		}
		if len(m.Keys) == 0 {
			run.keys[""] = true
		}
		b.hooks.missed = append(b.hooks.missed, missedRun{run: run, why: m.Why})
	}
	if len(b.hooks.missed) > maxMissed {
		b.hooks.missed = b.hooks.missed[len(b.hooks.missed)-maxMissed:]
	}
	for _, e := range saved.Log {
		b.hooks.log = append(b.hooks.log, hookEntry{id: e.ID, at: e.At, hook: e.Hook, keys: e.Keys, events: e.Events,
			dropped: e.Dropped, outcome: e.Outcome, took: e.Took})
		b.hooks.lastID = max(b.hooks.lastID, e.ID)
	}
	if len(b.hooks.log) > hookLogSize {
		b.hooks.log = b.hooks.log[len(b.hooks.log)-hookLogSize:]
	}
	slog.Info("hook state loaded", "snoozes", len(b.hooks.snoozes), "checks", len(b.hooks.log), "missed", len(b.hooks.missed))
}

// saveHookState writes the snoozes, checks and missed runs, replacing the file in one step.
func (b *Bot) saveHookState() {
	path := b.opt.HookStateFile
	if path == "" {
		return
	}
	b.hooks.saveMu.Lock()
	defer b.hooks.saveMu.Unlock()
	var saved savedHooks
	now := time.Now()
	b.hooks.mu.Lock()
	for k, until := range b.hooks.snoozes {
		if until.After(now) {
			saved.Snoozes = append(saved.Snoozes, savedSnooze{Who: k.who, Hook: k.hook, Key: k.key, Until: until})
		}
	}
	for _, m := range b.hooks.missed {
		saved.Missed = append(saved.Missed, savedMissed{Hook: m.run.name, Events: m.run.events, Dropped: m.run.dropped,
			Keys: runKeys(m.run), At: m.run.missedAt, Why: m.why})
	}
	for _, e := range b.hooks.log {
		saved.Log = append(saved.Log, savedEntry{ID: e.id, At: e.at, Hook: e.hook, Keys: e.keys, Events: e.events,
			Dropped: e.dropped, Outcome: e.outcome, Took: e.took})
	}
	b.hooks.mu.Unlock()
	slices.SortFunc(saved.Snoozes, func(a, b savedSnooze) int { return a.Until.Compare(b.Until) })
	data, err := json.MarshalIndent(saved, "", "  ")
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		slog.Error("saving hook state", "file", path, "err", err)
	}
}
