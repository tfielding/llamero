// Package schedule holds the user's scheduled checks: when they run (Spec), and where they're
// kept (Store). Running them is up to the bot.
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MinInterval is the shortest repeat allowed: a model and its tools on every run is not cheap,
// and a machine that should sleep never would.
const MinInterval = 15 * time.Minute

// Forms is the list of accepted forms, for error messages and the tool description.
const Forms = `"every 30m", "every 2h between 22:00 and 06:00", "daily 07:30", "weekdays 07:30", ` +
	`"weekends 10:00", "mon,wed,fri 18:00", "at 17:00", "tomorrow 9am", "2026-11-10 08:00"`

// Spec is when a schedule runs.
type Spec struct {
	Every    time.Duration  // repeat interval; 0 = at a time of day, or once
	From, To clock          // with Every: only between these times of day (To may be after midnight)
	Window   bool           // From/To are set
	Days     [7]bool        // at a time of day: on these weekdays (indexed by time.Weekday)
	At       clock          // the time of day
	Once     time.Time      // a single run at this moment
	loc      *time.Location // time zone for times of day
}

type clock struct{ h, m int }

func (c clock) String() string { return fmt.Sprintf("%02d:%02d", c.h, c.m) }

func (c clock) on(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), c.h, c.m, 0, 0, day.Location())
}

// Parse reads a schedule as people say it (see Forms). Times are in now's time zone; a one-off
// time without a date is the next such time.
func Parse(s string, now time.Time) (Spec, error) { return parse(s, now, false) }

// reload reads a schedule saved with Saved. A one-off may be in the past by now (the bot was down).
func reload(s string, now time.Time) (Spec, error) { return parse(s, now, true) }

func parse(s string, now time.Time, allowPast bool) (Spec, error) {
	spec := Spec{loc: now.Location()}
	text := strings.ToLower(strings.Join(strings.Fields(s), " "))
	fail := func() (Spec, error) { return Spec{}, fmt.Errorf("%q not understood; use e.g. %s", s, Forms) }
	if text == "" {
		return fail()
	}

	// every <duration> [between <time> and <time>]
	if rest, ok := strings.CutPrefix(text, "every "); ok && !strings.HasPrefix(rest, "day") {
		dur, window, _ := strings.Cut(rest, " between ")
		d, err := parseEvery(dur)
		if err != nil {
			return fail()
		}
		if d < MinInterval {
			return Spec{}, fmt.Errorf("%q is too often: the shortest is every %s", s, human(MinInterval))
		}
		spec.Every = d
		if window != "" {
			from, to, ok := strings.Cut(window, " and ")
			if !ok {
				return fail()
			}
			if spec.From, err = parseClock(from); err != nil {
				return fail()
			}
			if spec.To, err = parseClock(to); err != nil {
				return fail()
			}
			if spec.From == spec.To {
				return Spec{}, fmt.Errorf("%q: the start and end of the window are the same", s)
			}
			spec.Window = true
		}
		return spec, nil
	}

	// One-off: at <time>, today/tonight/tomorrow [at] <time>, <date> [at] <time>.
	day, rest := now, strings.TrimPrefix(text, "at ")
	explicit := false
	for _, p := range []struct {
		word string
		days int
	}{{"today", 0}, {"tonight", 0}, {"tomorrow", 1}} {
		if r, ok := strings.CutPrefix(rest, p.word); ok {
			day, rest, explicit = now.AddDate(0, 0, p.days), strings.TrimPrefix(strings.TrimSpace(r), "at "), true
			break
		}
	}
	if !explicit {
		if date, r, ok := strings.Cut(rest, " "); ok {
			if d, err := time.ParseInLocation("2006-01-02", date, now.Location()); err == nil {
				day, rest, explicit = d, strings.TrimPrefix(r, "at "), true
			}
		}
	}
	if c, err := parseClock(rest); err == nil && (explicit || strings.HasPrefix(text, "at ") || rest == text) {
		t := c.on(day)
		if !explicit && !t.After(now) {
			t = c.on(now.AddDate(0, 0, 1))
		}
		if !t.After(now) && !allowPast {
			return Spec{}, fmt.Errorf("%q is in the past", s)
		}
		spec.Once = t
		return spec, nil
	}

	// Repeating at a time of day: daily, every day, weekdays, weekends, or day names.
	when, at, ok := strings.Cut(text, " ")
	if !ok {
		return fail()
	}
	if when == "every" { // "every day at 07:30"
		var r string
		if when, r, ok = strings.Cut(at, " "); !ok {
			return fail()
		}
		at = r
	}
	at = strings.TrimPrefix(at, "at ")
	c, err := parseClock(at)
	if err != nil {
		return fail()
	}
	spec.At = c
	switch when {
	case "daily", "day":
		spec.Days = [7]bool{true, true, true, true, true, true, true}
	case "weekdays":
		for d := time.Monday; d <= time.Friday; d++ {
			spec.Days[d] = true
		}
	case "weekends":
		spec.Days[time.Saturday], spec.Days[time.Sunday] = true, true
	default:
		for _, name := range strings.Split(when, ",") {
			d, ok := weekday(name)
			if !ok {
				return fail()
			}
			spec.Days[d] = true
		}
	}
	return spec, nil
}

// Next is the first run strictly after t, or zero if there is none (a one-off that's done).
// last is the previous run (zero if none), which repeats are counted from.
func (s Spec) Next(t, last time.Time) time.Time {
	t = t.In(s.loc)
	switch {
	case !s.Once.IsZero():
		if s.Once.After(t) {
			return s.Once
		}
		return time.Time{}
	case s.Every > 0:
		next := t.Add(s.Every).Truncate(time.Minute)
		if !last.IsZero() && last.Add(s.Every).After(t) {
			next = last.Add(s.Every)
		}
		if !s.Window || s.inWindow(next) {
			return next
		}
		// Outside the window: the next time it opens.
		start := s.From.on(next)
		if !start.After(next) {
			start = s.From.on(next.AddDate(0, 0, 1))
		}
		return start
	default:
		for i := 0; i <= 7; i++ {
			day := t.AddDate(0, 0, i)
			if at := s.At.on(day); s.Days[at.Weekday()] && at.After(t) {
				return at
			}
		}
		return time.Time{}
	}
}

func (s Spec) inWindow(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	from, to := s.From.h*60+s.From.m, s.To.h*60+s.To.m
	if from < to {
		return m >= from && m < to
	}
	return m >= from || m < to // overnight, e.g. 22:00 to 06:00
}

// Saved is the schedule in a form that reads back to the same schedule later: a one-off as its
// date and time, a repeat in minutes.
func (s Spec) Saved() string {
	switch {
	case !s.Once.IsZero():
		return s.Once.In(s.loc).Format("2006-01-02 15:04")
	case s.Every > 0:
		out := fmt.Sprintf("every %dm", int(s.Every/time.Minute))
		if s.Window {
			out += " between " + s.From.String() + " and " + s.To.String()
		}
		return out
	}
	return strings.ToLower(s.String())
}

// String is the schedule in its usual form, e.g. "weekdays 07:30" or "once Tue 4 Nov 17:00".
func (s Spec) String() string {
	switch {
	case !s.Once.IsZero():
		return "once " + s.Once.In(s.loc).Format("Mon 2 Jan 15:04")
	case s.Every > 0:
		out := "every " + human(s.Every)
		if s.Window {
			out += " between " + s.From.String() + " and " + s.To.String()
		}
		return out
	}
	var days []string
	n := 0
	for d := time.Sunday; d <= time.Saturday; d++ {
		if s.Days[d] {
			n++
		}
	}
	weekdays := n == 5 && !s.Days[time.Saturday] && !s.Days[time.Sunday]
	weekends := n == 2 && s.Days[time.Saturday] && s.Days[time.Sunday]
	switch {
	case n == 7:
		return "daily " + s.At.String()
	case weekdays:
		return "weekdays " + s.At.String()
	case weekends:
		return "weekends " + s.At.String()
	}
	for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday} {
		if s.Days[d] {
			days = append(days, d.String()[:3])
		}
	}
	return strings.Join(days, ",") + " " + s.At.String()
}

func parseEvery(s string) (time.Duration, error) {
	s = strings.ReplaceAll(s, " ", "")
	for _, u := range []struct {
		suffixes []string
		unit     time.Duration
	}{
		{[]string{"hours", "hour", "hrs", "hr", "h"}, time.Hour},
		{[]string{"minutes", "minute", "mins", "min", "m"}, time.Minute},
	} {
		for _, suf := range u.suffixes {
			if n, ok := strings.CutSuffix(s, suf); ok {
				f, err := strconv.ParseFloat(n, 64)
				if err != nil || f <= 0 {
					return 0, errors.New("bad duration")
				}
				return time.Duration(f * float64(u.unit)).Round(time.Minute), nil
			}
		}
	}
	return 0, errors.New("bad duration")
}

// parseClock reads a time of day: 07:30, 7:30, 7am, 7:30 pm, 19:30, noon, midnight.
func parseClock(s string) (clock, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "noon", "midday":
		return clock{12, 0}, nil
	case "midnight":
		return clock{0, 0}, nil
	}
	ampm := ""
	for _, suf := range []string{"a.m.", "p.m.", "am", "pm"} {
		if r, ok := strings.CutSuffix(s, suf); ok {
			s, ampm = strings.TrimSpace(r), suf[:1]
			break
		}
	}
	hs, ms, hasMin := strings.Cut(s, ":")
	if !hasMin && ampm == "" {
		return clock{}, errors.New("bad time")
	}
	h, err := strconv.Atoi(hs)
	if err != nil {
		return clock{}, errors.New("bad time")
	}
	m := 0
	if hasMin {
		if len(ms) != 2 {
			return clock{}, errors.New("bad time")
		}
		if m, err = strconv.Atoi(ms); err != nil || m > 59 {
			return clock{}, errors.New("bad time")
		}
	}
	switch {
	case ampm != "" && (h < 1 || h > 12):
		return clock{}, errors.New("bad hour")
	case ampm == "p" && h < 12:
		h += 12
	case ampm == "a" && h == 12:
		h = 0
	case h < 0 || h > 23:
		return clock{}, errors.New("bad hour")
	}
	return clock{h, m}, nil
}

func weekday(s string) (time.Weekday, bool) {
	s = strings.TrimSpace(s)
	for d := time.Sunday; d <= time.Saturday; d++ {
		name := strings.ToLower(d.String())
		if s == name || s == name[:3] || s == name+"s" {
			return d, true
		}
	}
	return 0, false
}

func human(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d > time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d/time.Minute)%60)
	}
	return fmt.Sprintf("%dm", int(d/time.Minute))
}
