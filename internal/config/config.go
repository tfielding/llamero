// Package config loads settings from environment variables and an optional config.yml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tfielding/llamero/internal/app"
	"github.com/tfielding/llamero/internal/toolbox"
	"github.com/tfielding/llamero/internal/transport/telegram"
)

const defaultSystemPrompt = "You are a helpful assistant chatting in a messaging app on a phone. " +
	"Keep answers short and conversational unless asked for detail. " +
	"Formatting: you may use **bold**, *italic*, `code` and simple bullet lists. " +
	"Do not use headings, tables or horizontal rules."

type Config struct {
	ConfigFile       string   // config file that was read; empty = environment variables only
	Signal           Signal   // transports.signal; see Signal.On
	Telegram         Telegram // transports.telegram; see Telegram.On
	OllamaURL        string   // e.g. http://192.168.1.50:11434
	OllamaBackupURL  string   // a backup server, tried when OllamaURL can't be reached; empty = none
	DefaultModel     string   // empty = first model Ollama reports
	VisionModel      string   // answers messages with photos when the chat's model can't see; empty = none
	DefaultModelAuto bool     // no model was set: main picks the first installed one
	DataDir          string   // where chat history is stored
	HistoryLimit     int      // max messages (user+assistant) kept per chat
	Summarize        bool     // summarize messages past HistoryLimit instead of dropping them
	SummaryModel     string   // model that writes summaries; empty = the chat's model
	SystemPrompt     string
	OllamaTimeout    time.Duration
	Reaction         string // emoji shown on a message while it's being answered; empty = off

	WakeMAC  net.HardwareAddr // Ollama machine's MAC for Wake-on-LAN; nil = off
	WakeAddr string           // where to send the magic packet (host:port)
	WakeWait time.Duration    // how long to wait for Ollama after a wake-up

	MCPServers   []toolbox.Server // mcp.servers in the file, or MCP_SERVERS=name=url,...; MCP_TOKEN_<NAME>
	MCPTools     map[string]bool  // optional allowlist of tool names, across servers
	MCPWrite     toolbox.Write    // default for servers without their own allow_write
	MCPMaxChars  int              // cap on tool output given to the model
	MCPMaxRounds int              // rounds of tool calls per message before the model must answer

	Schedules  bool   // scheduled checks (create_schedule tool, /schedules)
	HealthAddr string // where GET /healthz listens, e.g. ":8092"; empty = off

	KnowledgeDir      string // folder of Markdown notes for the model (see package knowledge)
	KnowledgeMaxChars int    // cap on what those notes add to the prompt

	WebhookToken string // what POST /hooks/<name> callers must send as "Authorization: Bearer <token>"
	Hooks        []Hook // webhooks.hooks in the file, by name
}

// Signal is the Signal transport: signal-cli-rest-api and who may use the bot through it.
type Signal struct {
	URL     string          // base URL of signal-cli-rest-api, e.g. http://signal-cli-rest-api:8080
	Number  string          // the account registered in the container
	Allowed map[string]bool // phone numbers (or Signal account ids) allowed to talk to the bot
}

// On reports whether the Signal transport is set up.
func (s Signal) On() bool { return s.Number != "" }

// Telegram is the Telegram transport: a bot, and who may use it.
type Telegram struct {
	URL     string          // the Bot API, https://api.telegram.org unless self-hosted
	Token   string          // from @BotFather
	Allowed map[string]bool // Telegram user ids allowed to talk to the bot, without the prefix
}

// On reports whether the Telegram transport is set up.
func (t Telegram) On() bool { return t.Token != "" }

// Allowed is everyone allowed to talk to the bot, across the transports, as the core names them:
// Signal's as they are, other transports' with their prefix (e.g. "telegram:12345").
func (c Config) Allowed() map[string]bool {
	all := map[string]bool{}
	for id := range c.Signal.Allowed {
		all[id] = true
	}
	for id := range c.Telegram.Allowed {
		all[telegram.Prefix+":"+id] = true
	}
	return all
}

// Hook is one hook: what to do with events sent to POST /hooks/<Name>.
type Hook struct {
	Name     string
	Task     string        // what the model does with the events; empty = send them on unchanged
	OnlyIf   string        // report only when this is true
	To       []string      // people who get the result; empty (and no Groups) = everyone allowed
	Groups   []string      // group chats that get the result: Signal "group.…", Telegram "telegram:-…"; receive only
	Wake     bool          // wake the Ollama machine if it's asleep
	Group    time.Duration // events this soon after the first are handled together
	Cooldown time.Duration // after a message, events (with the same key) are skipped for this long; 0 = off
	Model    string        // the model for its checks; empty = the recipient's chat model
	Think    string        // thinking for its checks (on, off, low, medium, high); empty = the chat's setting
	Tools    []string      // the only tools its checks get; empty = every read-only tool
	Title    string        // the first line of its messages; empty = "🔔 <name>", "none" = no title line
}

const (
	defaultHookGroup = 30 * time.Second
	maxHookGroup     = 10 * time.Minute
	maxHookCooldown  = 24 * time.Hour
	maxHookTitle     = 60
	minWebhookToken  = 16
)

// Load reads the settings: each from its environment variable if set, otherwise from the
// config file (CONFIG_FILE, default /config/config.yml, optional), otherwise its default.
// Anything missing or invalid is an error naming the setting where it was given.
func Load() (Config, error) {
	f, err := loadFile(os.Getenv("CONFIG_FILE"))
	if err != nil {
		return Config{}, err
	}
	s := settings{f}
	c := Config{ConfigFile: f.path}
	for _, st := range table {
		if st.load == nil {
			continue
		}
		if err := st.load(&c, s.get(st.env, st.def), s.name(st.env)); err != nil {
			return c, err
		}
	}
	// What depends on more than one setting.
	if err := s.checkTransports(c); err != nil {
		return c, err
	}
	if c.MCPServers, err = s.servers(c.MCPWrite); err != nil {
		return c, err
	}
	if c.Hooks, err = s.hooks(c); err != nil {
		return c, err
	}
	if len(c.Hooks) > 0 {
		switch {
		case c.HealthAddr == "":
			return c, fmt.Errorf("webhooks.hooks in %s: hooks are served on HEALTH_ADDR (health.addr), which is off", s.f.path)
		case c.WebhookToken == "":
			return c, fmt.Errorf("%s is required when hooks are set: callers send it as \"Authorization: Bearer <token>\"", s.missing("WEBHOOK_TOKEN"))
		case len(c.WebhookToken) < minWebhookToken:
			return c, fmt.Errorf("%s must be at least %d characters (e.g. from: openssl rand -hex 24)", s.name("WEBHOOK_TOKEN"), minWebhookToken)
		}
	}
	return c, nil
}

// checkTransports checks that at least one messaging app is set up, and each one completely:
// giving any of its settings means it's meant to be used.
func (s settings) checkTransports(c Config) error {
	sig, tg := c.Signal, c.Telegram
	switch {
	case sig.Number == "" && len(sig.Allowed) == 0:
	case sig.Number == "":
		return fmt.Errorf("%s is required (the number registered in signal-cli-rest-api)", s.missing("SIGNAL_NUMBER"))
	case len(sig.Allowed) == 0:
		return fmt.Errorf("%s is required (phone numbers allowed to use the bot)", s.missing("SIGNAL_ALLOWED_SENDERS"))
	}
	switch {
	case tg.Token == "" && len(tg.Allowed) == 0:
	case tg.Token == "":
		return fmt.Errorf("%s is required (the bot token from @BotFather)", s.missing("TELEGRAM_TOKEN"))
	case !telegramToken.MatchString(tg.Token):
		return fmt.Errorf("%s doesn't look like a bot token (123456:ABC-...)", s.name("TELEGRAM_TOKEN"))
	case len(tg.Allowed) == 0:
		return fmt.Errorf("%s is required (Telegram user ids allowed to use the bot)", s.missing("TELEGRAM_ALLOWED_SENDERS"))
	}
	if !sig.On() && !tg.On() {
		return fmt.Errorf("no messaging app is set up: add transports.signal (number and allowed_senders) or transports.telegram (token and allowed_senders) to %s", s.fileName())
	}
	return nil
}

var telegramToken = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]+$`)

// signalPrefix may start any Signal id in the config (signal:+15551234567, signal:group.…), like
// telegram: for Telegram's. Signal ids are used without it: they're the unprefixed ones.
const signalPrefix = "signal:"

// Group chats a hook can send to: signal-cli-rest-api's group ids, and Telegram's group chat ids
// (negative).
var (
	signalGroup   = regexp.MustCompile(`^group\.[A-Za-z0-9+/=]+$`)
	telegramGroup = regexp.MustCompile(`^telegram:-\d+$`)
)

// hooks returns webhooks.hooks from the file, checked: valid names, a task for only_if, and
// recipients who are allowed senders.
func (s settings) hooks(c Config) ([]Hook, error) {
	names := make([]string, 0, len(s.f.hooks))
	for name := range s.f.hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Hook
	for _, name := range names {
		fh := s.f.hooks[name]
		where := fmt.Sprintf("webhooks.hooks.%s in %s", name, s.f.path)
		if !validHookName(name) {
			return nil, fmt.Errorf("webhooks.hooks in %s: %q is not a valid hook name (letters, digits, - and _)", s.f.path, name)
		}
		h := Hook{Name: name, Task: strings.TrimSpace(fh.Task), OnlyIf: strings.TrimSpace(fh.OnlyIf), Group: defaultHookGroup,
			Model: strings.TrimSpace(fh.Model), Think: strings.ToLower(strings.TrimSpace(fh.Think)), Tools: fh.Tools, Title: strings.TrimSpace(fh.Title)}
		if strings.ContainsAny(h.Title, "\r\n") || len([]rune(h.Title)) > maxHookTitle {
			return nil, fmt.Errorf("%s: title must be one line of at most %d characters", where, maxHookTitle)
		}
		if h.OnlyIf != "" && h.Task == "" {
			return nil, fmt.Errorf("%s: only_if needs a task (without a task, events are sent on unchecked)", where)
		}
		if h.Task == "" && (h.Model != "" || h.Think != "" || len(h.Tools) > 0) {
			return nil, fmt.Errorf("%s: model, think and tools need a task (without a task, events are sent on unchecked)", where)
		}
		switch h.Think {
		case "", "on", "off", "low", "medium", "high":
		case "true", "false": // YAML booleans, as in think: false
			h.Think = map[string]string{"true": "on", "false": "off"}[h.Think]
		default:
			return nil, fmt.Errorf("%s: think must be on, off, low, medium or high", where)
		}
		for _, r := range fh.To {
			r = strings.TrimPrefix(strings.TrimSpace(r), signalPrefix)
			switch {
			case c.Allowed()[r]:
				h.To = append(h.To, r)
			case signalGroup.MatchString(r) && c.Signal.On(), telegramGroup.MatchString(r) && c.Telegram.On():
				h.Groups = append(h.Groups, r)
			case signalGroup.MatchString(r):
				return nil, fmt.Errorf("%s: to: %s is a Signal group, but Signal isn't set up", where, mask(r))
			case telegramGroup.MatchString(r):
				return nil, fmt.Errorf("%s: to: %s is a Telegram group, but Telegram isn't set up", where, mask(r))
			default:
				return nil, fmt.Errorf("%s: to: %s is not allowed on any transport (allowed_senders; Telegram ids as telegram:<id>) "+
					"and isn't a group (signal:group.…, telegram:-<id>)", where, mask(r))
			}
		}
		if v := strings.TrimSpace(fh.Wake); v != "" {
			var err error
			if h.Wake, err = strconv.ParseBool(v); err != nil {
				return nil, fmt.Errorf("%s: wake must be true or false", where)
			}
			if h.Wake && c.WakeMAC == nil {
				return nil, fmt.Errorf("%s: wake needs Wake-on-LAN (ollama.wake_on_lan.mac)", where)
			}
		}
		if v := strings.TrimSpace(fh.Cooldown); v != "" && v != "0" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 || d > maxHookCooldown {
				return nil, fmt.Errorf("%s: cooldown must be a duration from 0 to 24h, like 5m", where)
			}
			h.Cooldown = d
		}
		if v := strings.TrimSpace(fh.Group); v != "" {
			d, err := time.ParseDuration(v)
			if v == "0" {
				d, err = 0, nil
			}
			if err != nil || d < 0 || d > maxHookGroup {
				return nil, fmt.Errorf("%s: group must be a duration from 0 to %s, like 30s", where, maxHookGroup)
			}
			h.Group = d
		}
		out = append(out, h)
	}
	return out, nil
}

func validHookName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// settings looks a setting up in the environment, then in the config file.
type settings struct{ f file }

func (s settings) get(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if v, ok := s.f.values[key]; ok {
		return v
	}
	return def
}

// name is how to refer to a setting in an error: its environment variable, or its place in
// the config file if that's where it came from.
func (s settings) name(key string) string {
	if strings.TrimSpace(os.Getenv(key)) == "" {
		if _, ok := s.f.values[key]; ok {
			return pathOf(key) + " in " + s.f.path
		}
	}
	return key
}

// checkKnowledge checks a server's knowledge file: a .md file name in the knowledge folder,
// which must exist and be readable. Errors say which of those failed, and what's there instead.
func (s settings) checkKnowledge(name string) error {
	dir := s.get("KNOWLEDGE_DIR", "/config/knowledge")
	switch {
	case !strings.EqualFold(filepath.Ext(name), ".md"):
		return fmt.Errorf("%q: want a .md file name, e.g. ha.md", name)
	case filepath.Base(name) != name:
		return fmt.Errorf("%q: want just a file name in %s, e.g. ha.md", name, dir)
	}
	runAs := fmt.Sprintf("%s runs as uid %d", app.Name, os.Getuid())
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%q: the folder %s doesn't exist (is /config mounted?)", name, dir)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%q: can't open the folder %s: permission denied (%s; the folder needs r-x for it, e.g. chmod 755)", name, dir, runAs)
	case err != nil:
		return fmt.Errorf("%q: can't read the folder %s: %w", name, dir, err)
	}
	var mds []string
	for _, e := range entries {
		if strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			mds = append(mds, e.Name())
		}
	}
	f, err := os.Open(filepath.Join(dir, name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		there := "no .md files"
		if len(mds) > 0 {
			there = strings.Join(mds, ", ")
		}
		return fmt.Errorf("%q: no such file in %s (it has: %s)", name, dir, there)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%q: can't read it: permission denied (%s; the file needs r-- for it, e.g. chmod 644)", name, runAs)
	case err != nil:
		return fmt.Errorf("%q: can't read it: %w", name, err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%q in %s is not a regular file", name, dir)
	}
	return nil
}

// KnowledgeRefs assigns knowledge files to MCP servers: each server's knowledge file, or by
// default the file named after it (ha.md for "ha").
func (c Config) KnowledgeRefs() map[string][]string {
	refs := map[string][]string{}
	for _, s := range c.MCPServers {
		file := s.Knowledge
		if file == "" {
			file = s.Name + ".md"
		}
		refs[file] = append(refs[file], s.Name)
	}
	return refs
}

// fileName is the config file read, or where one would go.
func (s settings) fileName() string {
	if s.f.path != "" {
		return s.f.path
	}
	return "config.yml"
}

// missing is how to refer to a required setting that wasn't given anywhere.
func (s settings) missing(key string) string {
	if s.f.path != "" {
		return pathOf(key) + " in " + s.f.path + " (or " + key + ")"
	}
	return key
}

// pathOf is a setting's place in config.yml.
func pathOf(env string) string {
	st, _ := lookup(env)
	return st.path
}

// servers returns the MCP servers: from MCP_SERVERS if set (which replaces the file's list),
// otherwise from mcp.servers in the file. MCP_TOKEN_<NAME> overrides a server's token.
// def is the write setting for servers that don't have their own.
func (s settings) servers(def toolbox.Write) ([]toolbox.Server, error) {
	envToken := func(name string) string {
		return os.Getenv("MCP_TOKEN_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name)))
	}
	if spec := os.Getenv("MCP_SERVERS"); strings.TrimSpace(spec) != "" {
		servers, err := toolbox.ParseServers(spec, func(_, name string) string { return envToken(name) })
		for i := range servers {
			servers[i].Write = def
		}
		return servers, err
	}
	names := make([]string, 0, len(s.f.servers))
	for name := range s.f.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []toolbox.Server
	for _, name := range names {
		fs := s.f.servers[name]
		where := fmt.Sprintf("mcp.servers.%s in %s", name, s.f.path)
		u, err := url.Parse(strings.TrimSpace(fs.URL))
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " ,=") {
			return nil, fmt.Errorf("mcp.servers in %s: %q is not a valid server name (letters, digits, - and _)", s.f.path, name)
		}
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%s: url must be like http://host:port/mcp", where)
		}
		srv := toolbox.Server{Name: name, URL: u.String(), Token: strings.TrimSpace(fs.Token), Write: def}
		if t := envToken(name); t != "" {
			srv.Token = t
		}
		if strings.TrimSpace(fs.AllowWrite) != "" {
			if srv.Write, err = toolbox.ParseWrite(fs.AllowWrite); err != nil {
				return nil, fmt.Errorf("%s: allow_write %w", where, err)
			}
		}
		if k := strings.TrimSpace(fs.Knowledge); k != "" {
			if err := s.checkKnowledge(k); err != nil {
				return nil, fmt.Errorf("%s: knowledge %w", where, err)
			}
			srv.Knowledge = k
		}
		if len(fs.Tools) > 0 {
			srv.Tools = map[string]bool{}
			for _, t := range fs.Tools {
				srv.Tools[t] = true
			}
		}
		for _, t := range fs.ReadOnly {
			if t = strings.TrimSpace(t); t != "" {
				if srv.ReadOnly == nil {
					srv.ReadOnly = map[string]bool{}
				}
				srv.ReadOnly[t] = true
			}
		}
		out = append(out, srv)
	}
	return out, nil
}

// Describe lists the effective settings by environment variable, for /settings.
// Secrets are never shown: tokens appear only as "set", phone numbers are masked
// and credentials and query strings in URLs are removed.
func (c Config) Describe() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "CONFIG_FILE: %s\n", or(c.ConfigFile, "none (environment variables only)"))
	for _, st := range table {
		for _, v := range st.show(c) {
			fmt.Fprintf(&sb, "%s: %s\n", st.env, v)
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// mask hides all but the last 4 characters of a phone number or UUID.
func mask(s string) string {
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}

// safeURL drops the parts of a URL that can hold secrets: user:password, the query
// string (e.g. ?token=…) and the fragment.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	hidden := u.RawQuery != "" || u.Fragment != ""
	u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
	if hidden {
		return u.String() + "?…"
	}
	return u.String()
}
