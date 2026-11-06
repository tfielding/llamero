package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultFile is where the config file is looked for when CONFIG_FILE isn't set.
const DefaultFile = "/config/config.yml"

// defaultFile is DefaultFile; tests point it elsewhere.
var defaultFile = DefaultFile

// fileConfig is the layout of config.yml. Every setting can also be set by its environment
// variable (see fileKeys), which wins over the file.
type fileConfig struct {
	Signal struct {
		URL            string     `yaml:"url"`
		Number         string     `yaml:"number"`
		AllowedSenders stringList `yaml:"allowed_senders"`
	} `yaml:"signal"`
	Ollama struct {
		URL         string `yaml:"url"`
		Model       string `yaml:"model"`
		VisionModel string `yaml:"vision_model"`
		Timeout     string `yaml:"timeout"`
		WakeOnLAN   struct {
			MAC  string `yaml:"mac"`
			Addr string `yaml:"addr"`
			Wait string `yaml:"wait"`
		} `yaml:"wake_on_lan"`
	} `yaml:"ollama"`
	History struct {
		Limit        string `yaml:"limit"`
		Summary      string `yaml:"summary"`
		SummaryModel string `yaml:"summary_model"`
	} `yaml:"history"`
	SystemPrompt string `yaml:"system_prompt"`
	Reaction     string `yaml:"reaction"`
	DataDir      string `yaml:"data_dir"`
	Schedules    struct {
		Enabled string `yaml:"enabled"`
	} `yaml:"schedules"`
	Health struct {
		Addr string `yaml:"addr"`
	} `yaml:"health"`
	Knowledge struct {
		Dir      string `yaml:"dir"`
		MaxChars string `yaml:"max_chars"`
	} `yaml:"knowledge"`
	Webhooks struct {
		Token string              `yaml:"token"`
		Hooks map[string]fileHook `yaml:"hooks"`
	} `yaml:"webhooks"`
	MCP struct {
		AllowWrite     string                `yaml:"allow_write"`
		Tools          stringList            `yaml:"tools"`
		MaxResultChars string                `yaml:"max_result_chars"`
		Servers        map[string]fileServer `yaml:"servers"`
	} `yaml:"mcp"`
}

// fileHook is one hook under webhooks.hooks.
type fileHook struct {
	Task   string     `yaml:"task"`
	OnlyIf string     `yaml:"only_if"`
	To     stringList `yaml:"to"`
	Wake   string     `yaml:"wake"`
	Group  string     `yaml:"group"`
}

type fileServer struct {
	URL        string     `yaml:"url"`
	Token      string     `yaml:"token"`
	AllowWrite string     `yaml:"allow_write"`
	Tools      stringList `yaml:"tools"`
	Knowledge  string     `yaml:"knowledge"`
}

// fileKeys maps each environment variable to its place in config.yml, for error messages.
var fileKeys = map[string]string{
	"SIGNAL_URL":           "signal.url",
	"SIGNAL_NUMBER":        "signal.number",
	"ALLOWED_SENDERS":      "signal.allowed_senders",
	"OLLAMA_URL":           "ollama.url",
	"OLLAMA_MODEL":         "ollama.model",
	"OLLAMA_VISION_MODEL":  "ollama.vision_model",
	"OLLAMA_TIMEOUT":       "ollama.timeout",
	"OLLAMA_WOL_MAC":       "ollama.wake_on_lan.mac",
	"OLLAMA_WOL_ADDR":      "ollama.wake_on_lan.addr",
	"OLLAMA_WOL_WAIT":      "ollama.wake_on_lan.wait",
	"HISTORY_LIMIT":        "history.limit",
	"HISTORY_SUMMARY":      "history.summary",
	"SUMMARY_MODEL":        "history.summary_model",
	"SYSTEM_PROMPT":        "system_prompt",
	"REACTION":             "reaction",
	"DATA_DIR":             "data_dir",
	"SCHEDULES":            "schedules.enabled",
	"HEALTH_ADDR":          "health.addr",
	"KNOWLEDGE_DIR":        "knowledge.dir",
	"KNOWLEDGE_MAX_CHARS":  "knowledge.max_chars",
	"MCP_ALLOW_WRITE":      "mcp.allow_write",
	"MCP_TOOLS":            "mcp.tools",
	"MCP_MAX_RESULT_CHARS": "mcp.max_result_chars",
	"WEBHOOK_TOKEN":        "webhooks.token",
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
	fc, err := parseFile(data)
	if err != nil {
		return file{}, fmt.Errorf("%s: %w", path, err)
	}
	f := file{path: path, servers: fc.MCP.Servers, hooks: fc.Webhooks.Hooks, values: map[string]string{
		"SIGNAL_URL":           fc.Signal.URL,
		"SIGNAL_NUMBER":        fc.Signal.Number,
		"ALLOWED_SENDERS":      strings.Join(fc.Signal.AllowedSenders, ","),
		"OLLAMA_URL":           fc.Ollama.URL,
		"OLLAMA_MODEL":         fc.Ollama.Model,
		"OLLAMA_VISION_MODEL":  fc.Ollama.VisionModel,
		"OLLAMA_TIMEOUT":       fc.Ollama.Timeout,
		"OLLAMA_WOL_MAC":       fc.Ollama.WakeOnLAN.MAC,
		"OLLAMA_WOL_ADDR":      fc.Ollama.WakeOnLAN.Addr,
		"OLLAMA_WOL_WAIT":      fc.Ollama.WakeOnLAN.Wait,
		"HISTORY_LIMIT":        fc.History.Limit,
		"HISTORY_SUMMARY":      fc.History.Summary,
		"SUMMARY_MODEL":        fc.History.SummaryModel,
		"SYSTEM_PROMPT":        fc.SystemPrompt,
		"REACTION":             fc.Reaction,
		"DATA_DIR":             fc.DataDir,
		"SCHEDULES":            fc.Schedules.Enabled,
		"HEALTH_ADDR":          fc.Health.Addr,
		"KNOWLEDGE_DIR":        fc.Knowledge.Dir,
		"KNOWLEDGE_MAX_CHARS":  fc.Knowledge.MaxChars,
		"MCP_ALLOW_WRITE":      fc.MCP.AllowWrite,
		"MCP_TOOLS":            strings.Join(fc.MCP.Tools, ","),
		"MCP_MAX_RESULT_CHARS": fc.MCP.MaxResultChars,
		"WEBHOOK_TOKEN":        fc.Webhooks.Token,
	}}
	for k, v := range f.values {
		if v = strings.TrimSpace(v); v == "" {
			delete(f.values, k)
		} else {
			f.values[k] = v
		}
	}
	return f, nil
}

var unknownField = regexp.MustCompile(`^(line \d+): field (\S+) not found in type .*$`)
var wrongType = regexp.MustCompile(`^(line \d+): cannot unmarshal !!(\w+) .*into (.*)$`)

// readable rewrites the decoder's errors without Go type names: one line per problem.
func readable(te *yaml.TypeError) error {
	msgs := make([]string, len(te.Errors))
	for i, e := range te.Errors {
		switch {
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

// parseFile decodes strictly (unknown keys are errors, with the line in the file), then fills
// in ${VAR} from the environment in every value, so secrets can stay in .env.
func parseFile(data []byte) (fileConfig, error) {
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) { // io.EOF: empty file
		var te *yaml.TypeError
		if errors.As(err, &te) {
			return fc, readable(te)
		}
		if bytes.Contains(data, []byte("${")) && strings.Contains(err.Error(), "did not find expected") {
			err = fmt.Errorf("%w (inside { } or [ ], put ${VAR} in quotes: \"${VAR}\")", err)
		}
		return fc, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || root.Kind == 0 {
		return fc, err
	}
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
	walk(&root)
	if len(missing) > 0 {
		sort.Strings(missing)
		return fc, fmt.Errorf("not set in the environment: %s", strings.Join(missing, ", "))
	}
	fc = fileConfig{}
	return fc, root.Decode(&fc)
}
