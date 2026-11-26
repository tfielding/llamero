package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashedPath(t *testing.T) {
	dir := t.TempDir()
	p1 := HashedPath(dir, "+15550000001")
	p2 := HashedPath(dir, "+15550000002")
	if p1 == p2 {
		t.Error("different ids got the same path")
	}
	if !strings.HasSuffix(p1, ".json") {
		t.Errorf("path: %s", p1)
	}
	if strings.Contains(filepath.Base(p1), "555") {
		t.Errorf("path leaks the id: %s", p1)
	}
	if HashedPath(dir, "+15550000001") != p1 {
		t.Error("same id got different paths")
	}
	if filepath.Dir(p1) != dir {
		t.Errorf("path: %s", p1)
	}
}

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.json")
	if err := Write(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perms: %o", fi.Mode().Perm())
	}
	// The temp file is gone.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left: %v", err)
	}
	// Overwriting replaces the file, leaving one file.
	if err := Write(path, []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "world" {
		t.Errorf("got %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("files: %v", entries)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.json")
	if err := Write(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file left: %v", err)
	}
	// A missing file is not an error.
	if err := Remove(path); err != nil {
		t.Errorf("missing file: %v", err)
	}
}
