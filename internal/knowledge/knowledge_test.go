package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGeneralAndServerNotes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "home.md", "## Home\n- Our car: blue hatchback\n")
	write(t, dir, "people.MD", "Jordan lives here.")
	write(t, dir, "Frigate.md", "Use the grid tools for pictures.")
	write(t, dir, "notes.txt", "not markdown")
	write(t, dir, ".hidden.md", "editor swap file")
	write(t, dir, "empty.md", "  \n")
	os.Mkdir(filepath.Join(dir, "sub.md"), 0o755)

	s := New(dir, map[string][]string{"frigate.md": {"frigate"}, "cams.md": {"cams"}}, 1000).Get()
	if s.Err != nil {
		t.Fatal(s.Err)
	}
	if s.General != "## Home\n- Our car: blue hatchback\n\nJordan lives here." {
		t.Errorf("general: %q", s.General)
	}
	if len(s.Servers) != 1 || s.Servers["frigate"] != "Use the grid tools for pictures." {
		t.Errorf("servers: %v", s.Servers)
	}
	var names []string
	for _, f := range s.Files {
		names = append(names, f.Name+"/"+strings.Join(f.Servers, "+"))
	}
	if strings.Join(names, ",") != "home.md/,people.MD/,Frigate.md/frigate" {
		t.Errorf("files: %v", names)
	}
	if s.Chars != len(s.General)-2+len(s.Servers["frigate"]) {
		t.Errorf("chars: %d", s.Chars)
	}
}

func TestMissingFolderIsEmpty(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "nope"), nil, 1000).Get()
	if s.Err != nil || s.General != "" || len(s.Files) != 0 {
		t.Fatalf("got %+v", s)
	}
}

func TestEditsAreReadAgain(t *testing.T) {
	dir := t.TempDir()
	k := New(dir, nil, 1000)
	if k.Get().General != "" {
		t.Fatal("not empty")
	}
	write(t, dir, "home.md", "first")
	if got := k.Get().General; got != "first" {
		t.Fatalf("new file: %q", got)
	}
	write(t, dir, "home.md", "second, longer")
	if got := k.Get().General; got != "second, longer" {
		t.Fatalf("edited file: %q", got)
	}
	// Same size, new modification time.
	write(t, dir, "home.md", "third, longer!")
	future := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(dir, "home.md"), future, future)
	if got := k.Get().General; got != "third, longer!" {
		t.Fatalf("same-size edit: %q", got)
	}
	os.Remove(filepath.Join(dir, "home.md"))
	if got := k.Get(); got.General != "" || len(got.Files) != 0 {
		t.Fatalf("removed file: %+v", got)
	}
}

func TestCap(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.md", strings.Repeat("line of notes\n", 30)) // 419 chars after trimming
	write(t, dir, "cams.md", "camera notes")                     // server notes come after general ones
	write(t, dir, "b.md", "é"+strings.Repeat("x", 199))
	s := New(dir, map[string][]string{"cams.md": {"cams"}}, 500).Get()
	if s.Chars > 500 {
		t.Errorf("over the cap: %d", s.Chars)
	}
	files := map[string]File{}
	for _, f := range s.Files {
		files[f.Name] = f
	}
	if files["a.md"].Cut || !files["b.md"].Cut || !files["cams.md"].Cut || files["cams.md"].Chars != 0 {
		t.Errorf("files: %+v", s.Files)
	}
	if _, ok := s.Servers["cams"]; ok {
		t.Errorf("cut server notes still given: %v", s.Servers)
	}
	if !strings.HasPrefix(s.General, "line of notes") || !strings.Contains(s.General, "é") {
		t.Errorf("general: %q", s.General)
	}
}

func TestOneFileForTwoServers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "cameras.md", "driveway faces the street.")
	write(t, dir, "frigate.md", "not referenced: general")
	s := New(dir, map[string][]string{"Cameras.md": {"cams", "frigate"}}, 1000).Get()
	if s.Servers["cams"] != "driveway faces the street." || s.Servers["frigate"] != "driveway faces the street." {
		t.Errorf("servers: %v", s.Servers)
	}
	if s.General != "not referenced: general" {
		t.Errorf("general: %q", s.General)
	}
}
