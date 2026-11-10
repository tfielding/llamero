package config

// The settings table: every setting that has an environment variable, in the order /settings
// lists them. Each one says where it lives in config.yml, its default, how it's loaded into
// Config and how /settings shows it. Adding a setting means a Config field and an entry here.

import (
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tfielding/llamero/internal/core"
	"github.com/tfielding/llamero/internal/knowledge"
	"github.com/tfielding/llamero/internal/toolbox"
	"github.com/tfielding/llamero/internal/transport/telegram"
	"github.com/tfielding/llamero/internal/wol"
)

type setting struct {
	env  string // environment variable; also the name /settings shows
	path string // place in config.yml; empty = environment only, or only shown (see load)
	def  string // default when it's set nowhere
	list bool   // config.yml may give a list (joined with commas)
	// load checks the value (the default if it's set nowhere) and puts it in c. name is how
	// errors refer to the setting: its variable, or its place in the file. nil = only shown.
	load func(c *Config, v, name string) error
	// show is what /settings shows for it, one line per value; none = left out.
	show func(c Config) []string
}

var table = []setting{
	{env: "SIGNAL_URL", path: "transports.signal.url", def: "http://signal-cli-rest-api:8080",
		load: urlTo(func(c *Config) *string { return &c.Signal.URL }),
		show: when(func(c Config) bool { return c.Signal.On() }, func(c Config) string { return safeURL(c.Signal.URL) })},
	{env: "SIGNAL_NUMBER", path: "transports.signal.number",
		load: str(func(c *Config) *string { return &c.Signal.Number }),
		show: when(func(c Config) bool { return c.Signal.On() }, func(c Config) string { return mask(c.Signal.Number) })},
	{env: "SIGNAL_ALLOWED_SENDERS", path: "transports.signal.allowed_senders", list: true,
		load: func(c *Config, v, _ string) error {
			c.Signal.Allowed = map[string]bool{}
			for _, id := range strings.Split(v, ",") {
				if id = strings.TrimPrefix(strings.TrimSpace(id), signalPrefix); id != "" {
					c.Signal.Allowed[id] = true
				}
			}
			return nil
		},
		show: when(func(c Config) bool { return c.Signal.On() }, func(c Config) string { return masked(c.Signal.Allowed) })},
	{env: "TELEGRAM_URL", path: "transports.telegram.url", def: telegram.DefaultURL,
		load: urlTo(func(c *Config) *string { return &c.Telegram.URL }),
		show: when(func(c Config) bool { return c.Telegram.On() }, func(c Config) string { return safeURL(c.Telegram.URL) })},
	{env: "TELEGRAM_TOKEN", path: "transports.telegram.token",
		load: str(func(c *Config) *string { return &c.Telegram.Token }),
		show: when(func(c Config) bool { return c.Telegram.On() }, func(Config) string { return "set" })},
	{env: "TELEGRAM_ALLOWED_SENDERS", path: "transports.telegram.allowed_senders", list: true,
		load: func(c *Config, v, name string) error {
			c.Telegram.Allowed = map[string]bool{}
			for _, id := range strings.Split(v, ",") {
				if id = strings.TrimPrefix(strings.TrimSpace(id), telegram.Prefix+":"); id == "" {
					continue
				}
				if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
					return fmt.Errorf("%s: %q is not a Telegram user id (a number; the bot logs it when someone not allowed messages it)", name, id)
				}
				c.Telegram.Allowed[id] = true
			}
			return nil
		},
		show: when(func(c Config) bool { return c.Telegram.On() }, func(c Config) string { return masked(c.Telegram.Allowed) })},

	{env: "OLLAMA_URL", path: "ollama.url", def: "http://localhost:11434",
		load: urlTo(func(c *Config) *string { return &c.OllamaURL }),
		show: always(func(c Config) string { return safeURL(c.OllamaURL) })},
	{env: "OLLAMA_BACKUP_URL", path: "ollama.backup_url",
		load: urlTo(func(c *Config) *string { return &c.OllamaBackupURL }),
		show: when(func(c Config) bool { return c.OllamaBackupURL != "" }, func(c Config) string { return safeURL(c.OllamaBackupURL) })},
	{env: "OLLAMA_MODEL", path: "ollama.model",
		load: func(c *Config, v, _ string) error {
			c.DefaultModel, c.DefaultModelAuto = v, v == ""
			return nil
		},
		show: always(func(c Config) string {
			if c.DefaultModelAuto {
				return c.DefaultModel + " (first installed model)"
			}
			return c.DefaultModel
		})},
	{env: "OLLAMA_VISION_MODEL", path: "ollama.vision_model",
		load: str(func(c *Config) *string { return &c.VisionModel }),
		show: always(func(c Config) string { return or(c.VisionModel, "not set (photos need a chat model that can see)") })},
	{env: "OLLAMA_TIMEOUT", path: "ollama.timeout", def: "5m",
		load: duration(func(c *Config) *time.Duration { return &c.OllamaTimeout }, "5m"),
		show: always(func(c Config) string { return c.OllamaTimeout.String() })},
	{env: "OLLAMA_WOL_MAC", path: "ollama.wake_on_lan.mac",
		load: func(c *Config, v, name string) error {
			if v == "" {
				return nil
			}
			mac, err := wol.ParseMAC(v)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			c.WakeMAC = mac
			return nil
		},
		show: always(func(c Config) string {
			if c.WakeMAC == nil {
				return "not set (Wake-on-LAN off)"
			}
			return c.WakeMAC.String()
		})},
	{env: "OLLAMA_WOL_ADDR", path: "ollama.wake_on_lan.addr", def: wol.DefaultAddr,
		load: func(c *Config, v, name string) error {
			if _, _, err := net.SplitHostPort(v); err != nil {
				return fmt.Errorf("%s must be host:port, e.g. 192.168.1.255:9", name)
			}
			c.WakeAddr = v
			return nil
		},
		show: when(func(c Config) bool { return c.WakeMAC != nil }, func(c Config) string { return c.WakeAddr })},
	{env: "OLLAMA_WOL_WAIT", path: "ollama.wake_on_lan.wait", def: "60s",
		load: duration(func(c *Config) *time.Duration { return &c.WakeWait }, "60s"),
		show: when(func(c Config) bool { return c.WakeMAC != nil }, func(c Config) string { return c.WakeWait.String() })},

	{env: "HISTORY_LIMIT", path: "history.limit", def: "40",
		load: intRange(func(c *Config) *int { return &c.HistoryLimit }, 2, 0),
		show: always(func(c Config) string { return strconv.Itoa(c.HistoryLimit) })},
	{env: "HISTORY_SUMMARY", path: "history.summary", def: "true",
		load: boolean(func(c *Config) *bool { return &c.Summarize }),
		show: always(func(c Config) string { return strconv.FormatBool(c.Summarize) })},
	{env: "SUMMARY_MODEL", path: "history.summary_model",
		load: str(func(c *Config) *string { return &c.SummaryModel }),
		show: always(func(c Config) string { return or(c.SummaryModel, "the chat's model") })},
	{env: "SYSTEM_PROMPT", path: "system_prompt", def: defaultSystemPrompt,
		load: str(func(c *Config) *string { return &c.SystemPrompt }),
		show: always(func(c Config) string {
			if c.SystemPrompt == defaultSystemPrompt {
				return "default"
			}
			return fmt.Sprintf("custom (%d characters)", len(c.SystemPrompt))
		})},
	{env: "REACTION", path: "reaction", def: "👀",
		load: func(c *Config, v, _ string) error {
			if !off(v) {
				c.Reaction = v
			}
			return nil
		},
		show: always(func(c Config) string { return or(c.Reaction, "off") })},
	{env: "DATA_DIR", path: "data_dir", def: "/data",
		load: str(func(c *Config) *string { return &c.DataDir }),
		show: always(func(c Config) string { return c.DataDir })},
	{env: "SCHEDULES", path: "schedules.enabled", def: "true",
		load: boolean(func(c *Config) *bool { return &c.Schedules }),
		show: always(func(c Config) string { return strconv.FormatBool(c.Schedules) })},
	{env: "HEALTH_ADDR", path: "health.addr", def: ":8092",
		load: func(c *Config, v, name string) error {
			if off(v) {
				return nil
			}
			host, port, err := net.SplitHostPort(v)
			if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 1 || n > 65535 {
				return fmt.Errorf("%s must be [host]:port like :8092, or off", name)
			}
			c.HealthAddr = net.JoinHostPort(host, port)
			return nil
		},
		show: always(func(c Config) string { return or(c.HealthAddr, "off") })},
	{env: "TZ", // set for the process, not by llamero: only shown
		show: always(func(Config) string { return time.Local.String() })},
	{env: "KNOWLEDGE_DIR", path: "knowledge.dir", def: "/config/knowledge",
		load: str(func(c *Config) *string { return &c.KnowledgeDir }),
		show: always(func(c Config) string { return c.KnowledgeDir })},
	{env: "KNOWLEDGE_MAX_CHARS", path: "knowledge.max_chars", def: strconv.Itoa(knowledge.DefaultMaxChars),
		load: intRange(func(c *Config) *int { return &c.KnowledgeMaxChars }, 500, 0),
		show: always(func(c Config) string { return strconv.Itoa(c.KnowledgeMaxChars) })},

	{env: "MCP_SERVERS", // mcp.servers in the file, or name=url,... in the environment: see servers
		show: func(c Config) []string {
			if len(c.MCPServers) == 0 {
				return []string{"none"}
			}
			out := make([]string, len(c.MCPServers))
			for i, s := range c.MCPServers {
				out[i] = describeServer(s, c.MCPWrite)
			}
			return out
		}},
	{env: "MCP_TOOLS", path: "mcp.tools", list: true,
		load: func(c *Config, v, _ string) error {
			c.MCPTools = map[string]bool{}
			for _, t := range strings.Split(v, ",") {
				if t = strings.TrimSpace(t); t != "" {
					c.MCPTools[t] = true
				}
			}
			return nil
		},
		show: withServers(func(c Config) string {
			if len(c.MCPTools) == 0 {
				return "all"
			}
			return strings.Join(slices.Sorted(maps.Keys(c.MCPTools)), ", ")
		})},
	{env: "MCP_ALLOW_WRITE", path: "mcp.allow_write", def: "false",
		load: func(c *Config, v, name string) error {
			w, err := toolbox.ParseWrite(v)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			c.MCPWrite = w
			return nil
		},
		show: withServers(func(c Config) string { return c.MCPWrite.String() })},
	{env: "MCP_MAX_RESULT_CHARS", path: "mcp.max_result_chars", def: strconv.Itoa(toolbox.DefaultMaxResultChars),
		load: intRange(func(c *Config) *int { return &c.MCPMaxChars }, 1000, 0),
		show: withServers(func(c Config) string { return strconv.Itoa(c.MCPMaxChars) })},
	{env: "MCP_MAX_TOOL_ROUNDS", path: "mcp.max_tool_rounds", def: strconv.Itoa(core.DefaultMaxToolRounds),
		load: intRange(func(c *Config) *int { return &c.MCPMaxRounds }, 1, 50),
		show: withServers(func(c Config) string { return strconv.Itoa(c.MCPMaxRounds) })},

	{env: "WEBHOOK_TOKEN", path: "webhooks.token",
		load: str(func(c *Config) *string { return &c.WebhookToken }),
		show: when(func(c Config) bool { return len(c.Hooks) > 0 }, func(Config) string { return "set" })},
	{env: "HOOK", // webhooks.hooks in the file: see hooks
		show: func(c Config) []string {
			out := make([]string, len(c.Hooks))
			for i, h := range c.Hooks {
				out[i] = describeHook(h)
			}
			return out
		}},
}

// lookup is the table's entry for env.
func lookup(env string) (setting, bool) {
	for _, s := range table {
		if s.env == env {
			return s, true
		}
	}
	return setting{}, false
}

// Loaders.

func str(field func(*Config) *string) func(*Config, string, string) error {
	return func(c *Config, v, _ string) error {
		*field(c) = v
		return nil
	}
}

func urlTo(field func(*Config) *string) func(*Config, string, string) error {
	return func(c *Config, v, name string) error {
		if v == "" { // only optional ones have no default
			return nil
		}
		if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s must be a URL like http://host:port", name)
		}
		*field(c) = strings.TrimRight(v, "/")
		return nil
	}
}

func boolean(field func(*Config) *bool) func(*Config, string, string) error {
	return func(c *Config, v, name string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s must be true or false", name)
		}
		*field(c) = b
		return nil
	}
}

// intRange loads an integer from min up to max (0 = no maximum).
func intRange(field func(*Config) *int, min, max int) func(*Config, string, string) error {
	return func(c *Config, v, name string) error {
		n, err := strconv.Atoi(v)
		switch {
		case max > 0 && (err != nil || n < min || n > max):
			return fmt.Errorf("%s must be an integer from %d to %d", name, min, max)
		case err != nil || n < min:
			return fmt.Errorf("%s must be an integer >= %d", name, min)
		}
		*field(c) = n
		return nil
	}
}

func duration(field func(*Config) *time.Duration, example string) func(*Config, string, string) error {
	return func(c *Config, v, name string) error {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s must be a duration like %s", name, example)
		}
		*field(c) = d
		return nil
	}
}

// off reports whether v turns a setting off.
func off(v string) bool {
	switch strings.ToLower(v) {
	case "off", "none", "false":
		return true
	}
	return false
}

// For /settings.

func always(f func(Config) string) func(Config) []string {
	return func(c Config) []string { return []string{f(c)} }
}

func when(cond func(Config) bool, f func(Config) string) func(Config) []string {
	return func(c Config) []string {
		if !cond(c) {
			return nil
		}
		return []string{f(c)}
	}
}

func withServers(f func(Config) string) func(Config) []string {
	return when(func(c Config) bool { return len(c.MCPServers) > 0 }, f)
}

func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}

func masked(ids map[string]bool) string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, mask(id))
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

func describeServer(s toolbox.Server, def toolbox.Write) string {
	var extra []string
	if s.Token != "" {
		extra = append(extra, "token set")
	}
	if s.Knowledge != "" {
		extra = append(extra, "knowledge "+s.Knowledge)
	}
	if s.Write != def {
		extra = append(extra, "allow_write "+s.Write.String())
	}
	if len(s.Tools) > 0 {
		extra = append(extra, "tools "+strings.Join(slices.Sorted(maps.Keys(s.Tools)), " "))
	}
	if len(s.ReadOnly) > 0 {
		extra = append(extra, "read_only "+strings.Join(slices.Sorted(maps.Keys(s.ReadOnly)), " "))
	}
	v := s.Name + "=" + safeURL(s.URL)
	if len(extra) > 0 {
		v += " (" + strings.Join(extra, ", ") + ")"
	}
	return v
}

func describeHook(h Hook) string {
	parts := []string{"sends events on unchecked"}
	if h.Task != "" {
		parts = []string{"task: " + h.Task}
	}
	if h.OnlyIf != "" {
		parts = append(parts, "only if: "+h.OnlyIf)
	}
	if len(h.To)+len(h.Groups) > 0 {
		var to []string
		for _, r := range h.To {
			to = append(to, mask(r))
		}
		for _, g := range h.Groups {
			to = append(to, "group "+mask(g))
		}
		parts = append(parts, "to "+strings.Join(to, " "))
	}
	if h.Wake {
		parts = append(parts, "wakes Ollama")
	}
	if h.Model != "" {
		parts = append(parts, "model "+h.Model)
	}
	if h.Think != "" {
		parts = append(parts, "think "+h.Think)
	}
	if len(h.Tools) > 0 {
		parts = append(parts, "tools "+strings.Join(h.Tools, ", "))
	}
	if h.Title != "" {
		parts = append(parts, "title "+h.Title)
	}
	parts = append(parts, "group "+h.Group.String())
	if h.Cooldown > 0 {
		parts = append(parts, "cooldown "+h.Cooldown.String())
	}
	return "/hooks/" + h.Name + " (" + strings.Join(parts, "; ") + ")"
}
