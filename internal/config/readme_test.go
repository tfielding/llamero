package config

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The README's settings table matches the code: every setting with a variable has a row, each
// row's config.yml place and default (when it's a value in backticks) are the code's, server and
// hook keys are the ones the file accepts, and no row names a setting that doesn't exist.
func TestREADMESettingsTable(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ env, path, def, about string }
	var rows []row
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "| Variable | config.yml | Default |"):
			in = true
			continue
		case !in:
			continue
		case !strings.HasPrefix(line, "|"):
			in = false
			continue
		case strings.HasPrefix(line, "|---"):
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|") // no cell has a | in it
		if len(cells) != 4 {
			t.Errorf("row doesn't have 4 cells: %s", line)
			continue
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, row{unquote(cells[0]), unquote(cells[1]), cells[2], cells[3]})
	}
	if len(rows) == 0 {
		t.Fatal("no settings table in the README")
	}

	// Settings the table in the code doesn't have: read elsewhere, or only shown by /settings.
	elsewhere := map[string]string{
		"CONFIG_FILE":      "",
		"MCP_TOKEN_<NAME>": "mcp.servers.<name>.token",
	}
	documented := map[string]bool{}
	for _, r := range rows {
		if r.env == "" {
			checkFileOnly(t, r.path, r.about)
			continue
		}
		documented[r.env] = true
		if path, ok := elsewhere[r.env]; ok {
			if r.path != path {
				t.Errorf("%s: README says %q, want %q", r.env, r.path, path)
			}
			continue
		}
		s, ok := lookup(r.env)
		if !ok {
			t.Errorf("README lists %s, which isn't a setting", r.env)
			continue
		}
		if r.path != s.path && !(r.env == "MCP_SERVERS" && r.path == "mcp.servers.<name>.url") {
			t.Errorf("%s: README says config.yml %q, the code %q", r.env, r.path, s.path)
		}
		if v, ok := strings.CutPrefix(r.def, "`"); ok && strings.HasSuffix(v, "`") && strings.Count(r.def, "`") == 2 {
			if v = strings.TrimSuffix(v, "`"); v != s.def {
				t.Errorf("%s: README says default %q, the code %q", r.env, v, s.def)
			}
		}
	}
	for _, s := range table {
		if s.env != "HOOK" && !documented[s.env] { // HOOK only shows hooks in /settings
			t.Errorf("%s isn't in the README's settings table", s.env)
		}
	}
}

// checkFileOnly checks a row with no variable: a key of every MCP server or every hook.
func checkFileOnly(t *testing.T, path, about string) {
	t.Helper()
	if key, ok := strings.CutPrefix(path, "mcp.servers.<name>."); ok {
		if !slices.Contains(yamlKeys(fileServer{}), key) {
			t.Errorf("README lists %s, which servers don't have", path)
		}
		return
	}
	if path == "webhooks.hooks.<name>" {
		var listed []string
		for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(about, -1) {
			listed = append(listed, m[1])
		}
		if want := yamlKeys(fileHook{}); !slices.Equal(listed, want) {
			t.Errorf("README lists hook keys %v, the code has %v", listed, want)
		}
		return
	}
	t.Errorf("README lists %s, a file-only setting the test doesn't know", path)
}

func unquote(s string) string { return strings.Trim(s, "`") }
