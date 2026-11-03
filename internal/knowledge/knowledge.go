// Package knowledge reads a folder of Markdown notes that are given to the model: things it
// should know (people, cars, cameras, routines) and how to use each MCP server's tools.
//
// A file assigned to MCP servers (by the server's knowledge setting, or by default the file
// named after it: frigate.md for "frigate") is given only along with those servers' tools.
// Every other .md file is always given. Files are read again when
// they change, so edits apply from the next message without a restart.
package knowledge

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// DefaultMaxChars caps everything the folder adds to the prompt (about 2k tokens).
const DefaultMaxChars = 8000

// maxFileBytes bounds how much of one file is read, whatever the cap.
const maxFileBytes = 1 << 20

// File describes one note file.
type File struct {
	Name    string   // file name, e.g. home.md
	Servers []string // MCP servers it's for; empty = always given
	Chars   int      // characters given to the model (after the cap)
	Cut     bool     // shortened or left out because of the cap
}

// Snapshot is the folder's current contents.
type Snapshot struct {
	General string            // always given to the model
	Servers map[string]string // by MCP server name, given with that server's tools
	Files   []File
	Chars   int   // total characters given
	Err     error // the folder couldn't be read (a missing folder is not an error)
}

// Base reads the folder, caching it until a file changes.
type Base struct {
	dir      string
	refs     map[string][]string // lowercased file name -> MCP servers it's for
	maxChars int

	mu  sync.Mutex
	sig string // file names, sizes and times when last read
	cur Snapshot
}

// New returns a Base for dir. refs assigns files (by name, e.g. "ha.md") to the MCP servers
// they're for.
func New(dir string, refs map[string][]string, maxChars int) *Base {
	k := &Base{dir: dir, refs: map[string][]string{}, maxChars: maxChars, sig: "unread"}
	for file, servers := range refs {
		k.refs[strings.ToLower(file)] = append(k.refs[strings.ToLower(file)], servers...)
	}
	return k
}

// Dir is the folder being read.
func (k *Base) Dir() string { return k.dir }

// MaxChars is the cap on what the folder adds to the prompt.
func (k *Base) MaxChars() int { return k.maxChars }

// Get returns the current notes, reading the folder again if anything in it changed.
func (k *Base) Get() Snapshot {
	k.mu.Lock()
	defer k.mu.Unlock()
	files, sig, err := k.list()
	if sig == k.sig && err == nil {
		return k.cur
	}
	k.sig = sig
	if err != nil {
		slog.Warn("knowledge folder unreadable", "dir", k.dir, "err", err)
		k.cur = Snapshot{Err: err}
		return k.cur
	}
	k.cur = k.read(files)
	if len(k.cur.Files) > 0 {
		names := make([]string, len(k.cur.Files))
		cut := false
		for i, f := range k.cur.Files {
			names[i] = f.Name
			cut = cut || f.Cut
		}
		slog.Info("knowledge loaded", "dir", k.dir, "files", names, "chars", k.cur.Chars)
		if cut {
			slog.Warn("knowledge over the limit: some of it is cut off", "max_chars", k.maxChars)
		}
	}
	return k.cur
}

// list returns the .md files in the folder, sorted, and a signature that changes when any of
// them is added, removed or edited.
func (k *Base) list() ([]string, string, error) {
	entries, err := os.ReadDir(k.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "error", err
	}
	var files []string
	var sig strings.Builder
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, name)
		fmt.Fprintf(&sig, "%s|%d|%d\n", name, info.Size(), info.ModTime().UnixNano())
	}
	sort.Strings(files)
	return files, sig.String(), nil
}

// read loads the files in name order, general notes first, until the cap is reached.
func (k *Base) read(names []string) Snapshot {
	snap := Snapshot{Servers: map[string]string{}}
	var general, perServer []File
	for _, name := range names {
		f := File{Name: name, Servers: k.refs[strings.ToLower(name)]}
		if len(f.Servers) == 0 {
			general = append(general, f)
		} else {
			perServer = append(perServer, f)
		}
	}
	left := k.maxChars
	var parts []string
	for _, f := range append(general, perServer...) {
		text, err := readFile(filepath.Join(k.dir, f.Name))
		if err != nil {
			slog.Warn("knowledge file unreadable", "file", f.Name, "err", err)
			continue
		}
		if text == "" {
			continue
		}
		if n := utf8.RuneCountInString(text); n > left {
			f.Cut = true
			text = cut(text, left)
		}
		f.Chars = utf8.RuneCountInString(text)
		left -= f.Chars
		snap.Chars += f.Chars
		snap.Files = append(snap.Files, f)
		switch {
		case text == "":
		case len(f.Servers) == 0:
			parts = append(parts, text)
		default:
			for _, srv := range f.Servers {
				snap.Servers[srv] = strings.TrimSpace(snap.Servers[srv] + "\n\n" + text)
			}
		}
	}
	snap.General = strings.Join(parts, "\n\n")
	return snap
}

func readFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b), "")), nil
}

// cut shortens s to at most n characters, ending at a line break when one is near.
func cut(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	out := string(r[:n])
	if i := strings.LastIndex(out, "\n"); i > len(out)/2 {
		out = out[:i]
	}
	return strings.TrimSpace(out)
}
