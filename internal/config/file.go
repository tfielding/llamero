package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/tfielding/llamero/internal/toolbox"
	"go.yaml.in/yaml/v3"
)

// DefaultFile is where the config file is looked for when CONFIG_FILE isn't set.
const DefaultFile = "/config/config.yml"

// defaultFile is DefaultFile; tests point it elsewhere.
var defaultFile = DefaultFile

// fileHook is one hook under webhooks.hooks.
type fileHook struct {
	Task     string     `yaml:"task"`
	OnlyIf   string     `yaml:"only_if"`
	To       stringList `yaml:"to"`
	Wake     string     `yaml:"wake"`
	Group    string     `yaml:"group"`
	Cooldown string     `yaml:"cooldown"`
	Model    string     `yaml:"model"`
	Think    string     `yaml:"think"`
	Tools    stringList `yaml:"tools"`
	Title    string     `yaml:"title"`
}

type fileServer struct {
	URL        string     `yaml:"url"`
	Token      string     `yaml:"token"`
	AllowWrite string     `yaml:"allow_write"`
	Tools      stringList `yaml:"tools"`
	ReadOnly   stringList `yaml:"read_only"`
	Knowledge  string     `yaml:"knowledge"`
}

// file is a loaded config.yml: settings by environment variable name, and the MCP servers.
type file struct {
	path    string // empty = no file
	values  map[string]string
	servers map[string]fileServer
	hooks   map[string]fileHook
}

// stringList accepts a YAML list or a comma-separated string.
type stringList []string

func (l *stringList) UnmarshalYAML(n *yaml.Node) error {
	var items []string
	switch n.Kind {
	case yaml.ScalarNode:
		items = []string{n.Value}
	case yaml.SequenceNode:
		if err := n.Decode(&items); err != nil {
			return err
		}
	default:
		return fmt.Errorf("line %d: want a list or a comma-separated string", n.Line)
	}
	*l = nil
	for _, item := range items {
		for _, s := range strings.Split(item, ",") {
			if s = strings.TrimSpace(s); s != "" {
				*l = append(*l, s)
			}
		}
	}
	return nil
}

// loadFile reads the config file at path. With path empty it looks for DefaultFile, and a
// missing file means no file; a file named by CONFIG_FILE must exist.
func loadFile(path string) (file, error) {
	explicit := path != ""
	if !explicit {
		path = defaultFile
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && !explicit {
		return file{}, nil
	}
	if err != nil {
		return file{}, fmt.Errorf("config file: %w", err)
	}
	f, err := parseFile(data)
	if err != nil {
		return file{}, fmt.Errorf("%s: %w", path, err)
	}
	f.path = path
	return f, nil
}

var unknownField = regexp.MustCompile(`^(line \d+): field (\S+) not found in type .*$`)
var wrongType = regexp.MustCompile(`^(line \d+): cannot unmarshal !!(\w+) .*into (.*)$`)

// readable rewrites the decoder's errors without Go type names: one line per problem.
func readable(te *yaml.TypeError) error {
	msgs := make([]string, len(te.Errors))
	for i, e := range te.Errors {
		switch {
		case unknownField.MatchString(e) && unknownField.FindStringSubmatch(e)[2] == "signal":
			msgs[i] = unknownField.ReplaceAllString(e, `$1: unknown setting "$2" (it goes under transports:)`)
		case unknownField.MatchString(e):
			msgs[i] = unknownField.ReplaceAllString(e, `$1: unknown setting "$2" (check the spelling and indentation)`)
		case wrongType.MatchString(e):
			m := wrongType.FindStringSubmatch(e)
			want := "a single value"
			if strings.Contains(m[3], "map[") || strings.Contains(m[3], "struct") {
				want = "a section with settings under it"
			}
			found := map[string]string{"seq": "list", "map": "section"}[m[2]]
			if found == "" {
				found = "value"
			}
			msgs[i] = fmt.Sprintf("%s: found a %s, want %s", m[1], found, want)
		default:
			msgs[i] = e
		}
	}
	return errors.New(strings.Join(msgs, "; "))
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// parseFile reads config.yml: the settings in the table by their place in it, and the MCP servers
// and hooks. Unknown keys and values of the wrong kind are errors with their line, all at once.
// ${VAR} in any value is filled in from the environment, so secrets can stay in .env.
func parseFile(data []byte) (file, error) {
	f := file{values: map[string]string{}}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		if bytes.Contains(data, []byte("${")) && strings.Contains(err.Error(), "did not find expected") {
			err = fmt.Errorf("%w (inside { } or [ ], put ${VAR} in quotes: \"${VAR}\")", err)
		}
		return f, err
	}
	if root.Kind == 0 || len(root.Content) == 0 { // empty, or only comments
		return f, nil
	}
	doc := root.Content[0]
	if doc.Kind == yaml.ScalarNode && doc.Tag == "!!null" {
		return f, nil
	}
	var problems []string
	checkSection(doc, "", &problems)
	if len(problems) > 0 {
		return f, errors.New(strings.Join(problems, "; "))
	}
	if err := expandEnv(doc); err != nil {
		return f, err
	}
	for _, st := range table {
		if st.path == "" {
			continue
		}
		n := at(doc, st.path)
		if n == nil {
			continue
		}
		var v string
		if n.Kind == yaml.SequenceNode {
			var items stringList
			if err := n.Decode(&items); err != nil {
				return f, err
			}
			v = strings.Join(items, ",")
		} else if n.Tag != "!!null" {
			v = n.Value
		}
		if v = strings.TrimSpace(v); v != "" {
			f.values[st.env] = v
		}
	}
	if n := at(doc, "mcp.servers"); n != nil {
		if err := decode(n, &f.servers); err != nil {
			return f, err
		}
	}
	if n := at(doc, "webhooks.hooks"); n != nil {
		if err := decode(n, &f.hooks); err != nil {
			return f, err
		}
	}
	return f, nil
}

// decode decodes n into v, with the decoder's errors made readable.
func decode(n *yaml.Node, v any) error {
	err := n.Decode(v)
	var te *yaml.TypeError
	if errors.As(err, &te) {
		return readable(te)
	}
	return err
}

// expandEnv fills in ${VAR} in every value from the environment.
func expandEnv(doc *yaml.Node) error {
	var missing []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "${") {
			n.Value = envRef.ReplaceAllStringFunc(n.Value, func(ref string) string {
				name := envRef.FindStringSubmatch(ref)[1]
				v, ok := os.LookupEnv(name)
				if !ok {
					missing = append(missing, fmt.Sprintf("${%s} (line %d)", name, n.Line))
				}
				return v
			})
			n.Tag = "!!str"
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(doc)
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("not set in the environment: %s", strings.Join(missing, ", "))
	}
	return nil
}

// at is the node at a dotted path under n, or nil.
func at(n *yaml.Node, path string) *yaml.Node {
	for _, key := range strings.Split(path, ".") {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
			}
		}
		n = next
	}
	return n
}

// The layout of config.yml: the sections and settings in the table, plus the MCP servers and
// hooks, which are named by the user and have their own keys.
var (
	serverKeys = yamlKeys(fileServer{})
	hookKeys   = yamlKeys(fileHook{})
)

// named are the sections whose keys are names the user picks, with the keys each entry takes.
var named = map[string][]string{"mcp.servers": serverKeys, "webhooks.hooks": hookKeys}

// children is what may be under the section at path ("" = the top), and whether each is a
// setting (true) or a section (false).
func children(path string) map[string]bool {
	out := map[string]bool{}
	prefix := ""
	if path != "" {
		prefix = path + "."
	}
	for _, st := range table {
		if st.path == "" || !strings.HasPrefix(st.path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(st.path, prefix)
		first, _, deeper := strings.Cut(rest, ".")
		out[first] = out[first] || !deeper
	}
	for p := range named {
		if strings.HasPrefix(p, prefix) {
			first, _, _ := strings.Cut(strings.TrimPrefix(p, prefix), ".")
			if _, ok := out[first]; !ok {
				out[first] = false
			}
		}
	}
	return out
}

// checkSection checks the keys under a section, and what's under them, adding a line for each
// problem.
func checkSection(n *yaml.Node, path string, problems *[]string) {
	if keys, ok := named[path]; ok {
		checkNamed(n, path, keys, problems)
		return
	}
	if n.Kind != yaml.MappingNode {
		*problems = append(*problems, fmt.Sprintf("line %d: found a %s, want a section with settings under it", n.Line, kind(n)))
		return
	}
	known := children(path)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		setting, ok := known[key.Value]
		full := key.Value
		if path != "" {
			full = path + "." + key.Value
		}
		switch {
		case !ok:
			*problems = append(*problems, unknown(key, val, path, slices.Collect(maps.Keys(known))))
		case !setting:
			if val.Kind == yaml.ScalarNode && val.Tag == "!!null" { // an empty section
				continue
			}
			checkSection(val, full, problems)
		case val.Kind == yaml.MappingNode, val.Kind == yaml.SequenceNode && !isList(full):
			*problems = append(*problems, fmt.Sprintf("line %d: found a %s, want a single value", val.Line, kind(val)))
		}
	}
}

// checkNamed checks a section of named entries (MCP servers, hooks): each a section of keys.
func checkNamed(n *yaml.Node, path string, keys []string, problems *[]string) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return
	}
	if n.Kind != yaml.MappingNode {
		*problems = append(*problems, fmt.Sprintf("line %d: found a %s, want a section with settings under it", n.Line, kind(n)))
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name, entry := n.Content[i], n.Content[i+1]
		if entry.Kind != yaml.MappingNode {
			*problems = append(*problems, fmt.Sprintf("line %d: %s.%s: found a %s, want a section with settings under it", entry.Line, path, name.Value, kind(entry)))
			continue
		}
		for j := 0; j+1 < len(entry.Content); j += 2 {
			key, val := entry.Content[j], entry.Content[j+1]
			if !slices.Contains(keys, key.Value) {
				*problems = append(*problems, unknown(key, val, path+"."+name.Value, keys))
			}
		}
	}
}

// unknown describes a key that doesn't belong where it is, with a hint: where it probably goes
// (judging by what's under it), or the setting it's probably a misspelling of.
func unknown(key, val *yaml.Node, path string, known []string) string {
	msg := fmt.Sprintf("line %d: unknown setting %q", key.Line, key.Value)
	has := func(k string) bool { return val.Kind == yaml.MappingNode && at(val, k) != nil }
	switch {
	case path == "" && (key.Value == "signal" || key.Value == "telegram"):
		return msg + " (it goes under transports:)"
	case has("url") && path != "mcp.servers" && !strings.HasPrefix(path, "transports"):
		return msg + " (it looks like an MCP server: it goes under mcp.servers:, lined up with the other servers)"
	case (has("task") || has("only_if")) && path != "webhooks.hooks":
		return msg + " (it looks like a hook: it goes under webhooks.hooks:, lined up with the other hooks)"
	}
	if alt := toolbox.Suggest(key.Value, known); alt != "" && alt != key.Value {
		return msg + fmt.Sprintf(" (did you mean %q?)", alt)
	}
	return msg + " (check the spelling and indentation)"
}

func isList(path string) bool {
	for _, st := range table {
		if st.path == path {
			return st.list
		}
	}
	return false
}

func kind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.SequenceNode:
		return "list"
	case yaml.MappingNode:
		return "section"
	}
	return "value"
}

// yamlKeys is the yaml names of v's fields.
func yamlKeys(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		out = append(out, name)
	}
	return out
}
