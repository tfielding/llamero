package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "schedules")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const me, other = "+15550000001", "+15550000002"
	daily, _ := Parse("daily 07:30", now)
	once, _ := Parse("at 17:00", now)

	err = s.Update(me, func(f *File) error {
		if _, err := f.Add(daily, " Summarize overnight alerts ", "", false, now); err != nil {
			return err
		}
		_, err := f.Add(once, "Remind me to check the porch", "", true, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Get(me)
	if err != nil || len(f.Items) != 2 || f.Items[0].ID != 1 || f.Items[1].ID != 2 || f.Items[0].Task != "Summarize overnight alerts" ||
		!f.Items[0].Next.Equal(at(4, 7, 30)) || f.Items[1].When != "2026-11-03 17:00" || !f.Items[1].Wake {
		t.Fatalf("%+v %v", f, err)
	}
	// Phone numbers aren't file names.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || strings.Contains(entries[0].Name(), "555") {
		t.Errorf("files: %v", entries)
	}

	// A failed update changes nothing.
	if err := s.Update(me, func(f *File) error { f.Items = nil; return errors.New("no") }); err == nil {
		t.Fatal("error lost")
	}
	if f, _ := s.Get(me); len(f.Items) != 2 {
		t.Fatal("failed update was saved")
	}

	// Ids keep counting after a delete.
	s.Update(me, func(f *File) error { f.Remove(1); _, err := f.Add(daily, "x", "", false, now); return err })
	if f, _ := s.Get(me); len(f.Items) != 2 || f.Items[1].ID != 3 {
		t.Fatalf("after delete: %+v", f.Items)
	}

	s.Update(other, func(f *File) error { _, err := f.Add(daily, "y", "", false, now); return err })
	all, err := s.All()
	if err != nil || len(all) != 2 || all[me].Chat != me || len(all[other].Items) != 1 {
		t.Fatalf("all: %+v %v", all, err)
	}

	// A chat with no schedules left has no file.
	s.Update(other, func(f *File) error { f.Items = nil; return nil })
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("empty file kept: %v", entries)
	}
}

func TestStoreLimitAndConcurrency(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	every, _ := Parse("every 1h", now)
	var wg sync.WaitGroup
	for i := 0; i < MaxPerChat+5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Update("+15550000001", func(f *File) error { _, err := f.Add(every, "x", "", false, now); return err })
		}()
	}
	wg.Wait()
	f, _ := s.Get("+15550000001")
	if len(f.Items) != MaxPerChat {
		t.Fatalf("%d schedules, want %d (no lost or extra updates)", len(f.Items), MaxPerChat)
	}
	err := s.Update("+15550000001", func(f *File) error { _, err := f.Add(every, "x", "", false, now); return err })
	if err == nil || !strings.Contains(err.Error(), "already 20 schedules") {
		t.Errorf("over the limit: %v", err)
	}
}
