package schedule

import (
	"strings"
	"testing"
	"time"
)

// now is Tuesday 3 Nov 2026, 15:00 UTC.
var now = time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)

func at(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, time.UTC) }

func TestParseAndNext(t *testing.T) {
	for _, tc := range []struct {
		in, str string
		next    time.Time // first run after now
	}{
		{"every 30m", "every 30m", now.Add(30 * time.Minute)},
		{"Every 2 hours", "every 2h", now.Add(2 * time.Hour)},
		{"every 90 minutes", "every 1h30m", now.Add(90 * time.Minute)},
		{"every 30m between 22:00 and 06:00", "every 30m between 22:00 and 06:00", at(3, 22, 0)},
		{"every 1h between 9am and 5pm", "every 1h between 09:00 and 17:00", now.Add(time.Hour)},
		{"daily 07:30", "daily 07:30", at(4, 7, 30)},
		{"every day at 7:30am", "daily 07:30", at(4, 7, 30)},
		{"daily at 16:00", "daily 16:00", at(3, 16, 0)},
		{"weekdays 07:30", "weekdays 07:30", at(4, 7, 30)},
		{"weekends 10am", "weekends 10:00", at(7, 10, 0)},
		{"mon,wed,fri 18:00", "Mon,Wed,Fri 18:00", at(4, 18, 0)},
		{"Sunday at noon", "Sun 12:00", at(8, 12, 0)},
		{"tuesdays 14:00", "Tue 14:00", at(10, 14, 0)}, // today's has passed: next week
		{"at 17:00", "once Tue 3 Nov 17:00", at(3, 17, 0)},
		{"9am", "once Wed 4 Nov 09:00", at(4, 9, 0)}, // passed today: tomorrow
		{"tomorrow 9am", "once Wed 4 Nov 09:00", at(4, 9, 0)},
		{"tonight at 21:30", "once Tue 3 Nov 21:30", at(3, 21, 30)},
		{"2026-11-10 08:00", "once Tue 10 Nov 08:00", at(10, 8, 0)},
		{"at 2026-11-10 at 8am", "once Tue 10 Nov 08:00", at(10, 8, 0)},
	} {
		s, err := Parse(tc.in, now)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		if s.String() != tc.str {
			t.Errorf("Parse(%q).String() = %q, want %q", tc.in, s.String(), tc.str)
		}
		if got := s.Next(now, time.Time{}); !got.Equal(tc.next) {
			t.Errorf("Parse(%q).Next = %v, want %v", tc.in, got, tc.next)
		}
		// Saved and read back a day later, it's the same schedule.
		back, err := reload(s.Saved(), now.Add(24*time.Hour))
		if err != nil || back.String() != s.String() {
			t.Errorf("Parse(%q) saved as %q reads back as %q, %v", tc.in, s.Saved(), back.String(), err)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "not understood",
		"sometimes":                     "not understood",
		"every 5m":                      "too often: the shortest is every 15m",
		"every banana":                  "not understood",
		"every 30m between 22:00":       "not understood",
		"every 30m between 9am and 9am": "the same",
		"daily":                         "not understood",
		"daily 25:00":                   "not understood",
		"funday 10:00":                  "not understood",
		"today 9am":                     "in the past",
		"2026-01-01 10:00":              "in the past",
	} {
		_, err := Parse(in, now)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want an error containing %q", in, err, want)
		}
	}
	if _, err := Parse("sometimes", now); err == nil || !strings.Contains(err.Error(), "weekdays 07:30") {
		t.Errorf("error doesn't list the forms: %v", err)
	}
}

func TestNextRepeats(t *testing.T) {
	// A repeat counts from the last run, not from whenever the check happens.
	every, _ := Parse("every 1h", now)
	last := at(3, 14, 20)
	if got := every.Next(now, last); !got.Equal(at(3, 15, 20)) {
		t.Errorf("from last run: %v", got)
	}
	// A run missed long ago (e.g. the bot was down) isn't caught up: next is from now.
	if got := every.Next(now, at(2, 9, 0)); !got.Equal(at(3, 16, 0)) {
		t.Errorf("after a gap: %v", got)
	}

	// An overnight window: inside it repeats, outside it waits for it to open.
	night, _ := Parse("every 30m between 22:00 and 06:00", now)
	if got := night.Next(at(3, 23, 0), at(3, 23, 0)); !got.Equal(at(3, 23, 30)) {
		t.Errorf("inside window: %v", got)
	}
	if got := night.Next(at(4, 5, 45), at(4, 5, 45)); !got.Equal(at(4, 22, 0)) {
		t.Errorf("past the end of the window: %v", got)
	}

	// A one-off has no run after it's done.
	once, _ := Parse("at 17:00", now)
	if got := once.Next(at(3, 17, 0), at(3, 17, 0)); !got.IsZero() {
		t.Errorf("one-off after its run: %v", got)
	}

	// Times of day follow the time zone, including a DST change (US: 1 Nov 2026).
	ny, _ := time.LoadLocation("America/New_York")
	sat := time.Date(2026, 10, 31, 12, 0, 0, 0, ny)
	daily, _ := Parse("daily 07:30", sat)
	if got := daily.Next(sat, time.Time{}).AddDate(0, 0, 0); got.Hour() != 7 || got.Minute() != 30 {
		t.Errorf("daily in New York: %v", got)
	}
	sun := daily.Next(daily.Next(sat, time.Time{}), time.Time{})
	if sun.In(ny).Hour() != 7 || sun.In(ny).Day() != 2 {
		t.Errorf("after the DST change: %v", sun.In(ny))
	}
}
