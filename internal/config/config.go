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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tfielding/signal-ollama/internal/knowledge"
	"github.com/tfielding/signal-ollama/internal/toolbox"
	"github.com/tfielding/signal-ollama/internal/wol"
)

const defaultSystemPrompt = "You are a helpful assistant chatting over Signal on a phone. " +
	"Keep answers short and conversational unless asked for detail. " +
	"Formatting: you may use **bold**, *italic*, `code` and simple bullet lists. " +
	"Do not use headings, tables or horizontal rules."

type Config struct {
	ConfigFile       string          // config file that was read; empty = environment variables only
	SignalURL        string          // base URL of signal-cli-rest-api, e.g. http://signal-cli-rest-api:8080
	SignalNumber     string          // the account registered in the container
	OllamaURL        string          // e.g. http://192.168.1.50:11434
	DefaultModel     string          // empty = first model Ollama reports
	DefaultModelAuto bool            // no model was set: main picks the first installed one
	Allowed          map[string]bool // phone numbers (or Signal UUIDs) allowed to talk to the bot
	DataDir          string          // where chat history is stored
	HistoryLimit     int             // max messages (user+assistant) kept per chat
	Summarize        bool            // summarize messages past HistoryLimit instead of dropping them
	SummaryModel     string          // model that writes summaries; empty = the chat's model
	SystemPrompt     string
	OllamaTimeout    time.Duration
	Reaction         string // emoji shown on a message while it's being answered; empty = off

	WakeMAC  net.HardwareAddr // Ollama machine's MAC for Wake-on-LAN; nil = off
	WakeAddr string           // where to send the magic packet (host:port)
	WakeWait time.Duration    // how long to wait for Ollama after a wake-up

	MCPServers  []toolbox.Server // mcp.servers in the file, or MCP_SERVERS=name=url,...; MCP_TOKEN_<NAME>
	MCPTools    map[string]bool  // optional allowlist of tool names, across servers
	MCPWrite    toolbox.Write    // default for servers without their own allow_write
	MCPMaxChars int              // cap on tool output given to the model

	Schedules bool // scheduled checks (create_schedule tool, /schedules)

	KnowledgeDir      string // folder of Markdown notes for the model (see package knowledge)
	KnowledgeMaxChars int    // cap on what those notes add to the prompt
}

// Load reads the settings: each from its environment variable if set, otherwise from the
// config file (CONFIG_FILE, default /config/config.yml, optional), otherwise its default.
// Anything missing or invalid is an error naming the setting where it was given.
func Load() (Config, error) {
	f, err := loadFile(os.Getenv("CONFIG_FILE"))
	if err != nil {
		return Config{}, err
	}
	s := settings{f}
	c := Config{
		ConfigFile:   f.path,
		SignalURL:    strings.TrimRight(s.get("SIGNAL_URL", "http://signal-cli-rest-api:8080"), "/"),
		SignalNumber: s.get("SIGNAL_NUMBER", ""),
		OllamaURL:    strings.TrimRight(s.get("OLLAMA_URL", "http://localhost:11434"), "/"),
		DefaultModel: s.get("OLLAMA_MODEL", ""),
		Allowed:      map[string]bool{},
		DataDir:      s.get("DATA_DIR", "/data"),
		SystemPrompt: s.get("SYSTEM_PROMPT", defaultSystemPrompt),
		SummaryModel: s.get("SUMMARY_MODEL", ""),
		Reaction:     s.get("REACTION", "👀"),
		KnowledgeDir: s.get("KNOWLEDGE_DIR", "/config/knowledge"),
	}
	c.DefaultModelAuto = c.DefaultModel == ""
	switch strings.ToLower(c.Reaction) {
	case "off", "none", "false":
		c.Reaction = ""
	}
	for _, key := range []string{"SIGNAL_URL", "OLLAMA_URL"} {
		if u, err := url.Parse(s.get(key, "http://x")); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return c, fmt.Errorf("%s must be a URL like http://host:port", s.name(key))
		}
	}

	if c.SignalNumber == "" {
		return c, fmt.Errorf("%s is required (the number registered in signal-cli-rest-api)", s.missing("SIGNAL_NUMBER"))
	}
	for _, v := range strings.Split(s.get("ALLOWED_SENDERS", ""), ",") {
		if v = strings.TrimSpace(v); v != "" {
			c.Allowed[v] = true
		}
	}
	if len(c.Allowed) == 0 {
		return c, fmt.Errorf("%s is required (phone numbers allowed to use the bot)", s.missing("ALLOWED_SENDERS"))
	}

	if c.HistoryLimit, err = strconv.Atoi(s.get("HISTORY_LIMIT", "40")); err != nil || c.HistoryLimit < 2 {
		return c, fmt.Errorf("%s must be an integer >= 2", s.name("HISTORY_LIMIT"))
	}
	if c.Summarize, err = strconv.ParseBool(s.get("HISTORY_SUMMARY", "true")); err != nil {
		return c, fmt.Errorf("%s must be true or false", s.name("HISTORY_SUMMARY"))
	}
	if c.OllamaTimeout, err = time.ParseDuration(s.get("OLLAMA_TIMEOUT", "5m")); err != nil || c.OllamaTimeout <= 0 {
		return c, fmt.Errorf("%s must be a duration like 5m", s.name("OLLAMA_TIMEOUT"))
	}

	if v := s.get("OLLAMA_WOL_MAC", ""); v != "" {
		if c.WakeMAC, err = wol.ParseMAC(v); err != nil {
			return c, fmt.Errorf("%s: %w", s.name("OLLAMA_WOL_MAC"), err)
		}
	}
	c.WakeAddr = s.get("OLLAMA_WOL_ADDR", wol.DefaultAddr)
	if _, _, err := net.SplitHostPort(c.WakeAddr); err != nil {
		return c, fmt.Errorf("%s must be host:port, e.g. 192.168.1.255:9", s.name("OLLAMA_WOL_ADDR"))
	}
	if c.WakeWait, err = time.ParseDuration(s.get("OLLAMA_WOL_WAIT", "60s")); err != nil || c.WakeWait <= 0 {
		return c, fmt.Errorf("%s must be a duration like 60s", s.name("OLLAMA_WOL_WAIT"))
	}

	if c.MCPWrite, err = toolbox.ParseWrite(s.get("MCP_ALLOW_WRITE", "false")); err != nil {
		return c, fmt.Errorf("%s: %w", s.name("MCP_ALLOW_WRITE"), err)
	}
	if c.MCPServers, err = s.servers(c.MCPWrite); err != nil {
		return c, err
	}
	c.MCPTools = map[string]bool{}
	for _, t := range strings.Split(s.get("MCP_TOOLS", ""), ",") {
		if t = strings.TrimSpace(t); t != "" {
			c.MCPTools[t] = true
		}
	}
	if c.Schedules, err = strconv.ParseBool(s.get("SCHEDULES", "true")); err != nil {
		return c, fmt.Errorf("%s must be true or false", s.name("SCHEDULES"))
	}
	if c.KnowledgeMaxChars, err = strconv.Atoi(s.get("KNOWLEDGE_MAX_CHARS", strconv.Itoa(knowledge.DefaultMaxChars))); err != nil || c.KnowledgeMaxChars < 500 {
		return c, fmt.Errorf("%s must be an integer >= 500", s.name("KNOWLEDGE_MAX_CHARS"))
	}
	if c.MCPMaxChars, err = strconv.Atoi(s.get("MCP_MAX_RESULT_CHARS", strconv.Itoa(toolbox.DefaultMaxResultChars))); err != nil || c.MCPMaxChars < 1000 {
		return c, fmt.Errorf("%s must be an integer >= 1000", s.name("MCP_MAX_RESULT_CHARS"))
	}
	return c, nil
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
			return fileKeys[key] + " in " + s.f.path
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
	runAs := fmt.Sprintf("signal-ollama runs as uid %d", os.Getuid())
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

// missing is how to refer to a required setting that wasn't given anywhere.
func (s settings) missing(key string) string {
	if s.f.path != "" {
		return fileKeys[key] + " in " + s.f.path + " (or " + key + ")"
	}
	return key
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
		out = append(out, srv)
	}
	return out, nil
}

// Describe lists the effective settings by environment variable, for /settings.
// Secrets are never shown: tokens appear only as "set", phone numbers are masked
// and credentials and query strings in URLs are removed.
func (c Config) Describe() string {
	model := c.DefaultModel
	if c.DefaultModelAuto {
		model += " (first installed model)"
	}
	allowed := make([]string, 0, len(c.Allowed))
	for s := range c.Allowed {
		allowed = append(allowed, mask(s))
	}
	sort.Strings(allowed)
	prompt := "default"
	if c.SystemPrompt != defaultSystemPrompt {
		prompt = fmt.Sprintf("custom (%d characters)", len(c.SystemPrompt))
	}
	summaryModel := c.SummaryModel
	if summaryModel == "" {
		summaryModel = "the chat's model"
	}

	var sb strings.Builder
	line := func(k string, v any) { fmt.Fprintf(&sb, "%s: %v\n", k, v) }
	if c.ConfigFile == "" {
		line("CONFIG_FILE", "none (environment variables only)")
	} else {
		line("CONFIG_FILE", c.ConfigFile)
	}
	line("SIGNAL_URL", safeURL(c.SignalURL))
	line("SIGNAL_NUMBER", mask(c.SignalNumber))
	line("ALLOWED_SENDERS", strings.Join(allowed, ", "))
	line("OLLAMA_URL", safeURL(c.OllamaURL))
	line("OLLAMA_MODEL", model)
	line("OLLAMA_TIMEOUT", c.OllamaTimeout)
	if c.WakeMAC == nil {
		line("OLLAMA_WOL_MAC", "not set (Wake-on-LAN off)")
	} else {
		line("OLLAMA_WOL_MAC", c.WakeMAC)
		line("OLLAMA_WOL_ADDR", c.WakeAddr)
		line("OLLAMA_WOL_WAIT", c.WakeWait)
	}
	line("HISTORY_LIMIT", c.HistoryLimit)
	line("HISTORY_SUMMARY", c.Summarize)
	line("SUMMARY_MODEL", summaryModel)
	line("SYSTEM_PROMPT", prompt)
	reaction := c.Reaction
	if reaction == "" {
		reaction = "off"
	}
	line("REACTION", reaction)
	line("DATA_DIR", c.DataDir)
	line("SCHEDULES", c.Schedules)
	line("TZ", time.Local.String())
	line("KNOWLEDGE_DIR", c.KnowledgeDir)
	line("KNOWLEDGE_MAX_CHARS", c.KnowledgeMaxChars)
	if len(c.MCPServers) == 0 {
		line("MCP_SERVERS", "none")
	}
	for _, s := range c.MCPServers {
		extra := []string{}
		if s.Token != "" {
			extra = append(extra, "token set")
		}
		if s.Knowledge != "" {
			extra = append(extra, "knowledge "+s.Knowledge)
		}
		if s.Write != c.MCPWrite {
			extra = append(extra, "allow_write "+s.Write.String())
		}
		if len(s.Tools) > 0 {
			names := make([]string, 0, len(s.Tools))
			for t := range s.Tools {
				names = append(names, t)
			}
			sort.Strings(names)
			extra = append(extra, "tools "+strings.Join(names, " "))
		}
		v := s.Name + "=" + safeURL(s.URL)
		if len(extra) > 0 {
			v += " (" + strings.Join(extra, ", ") + ")"
		}
		line("MCP_SERVERS", v)
	}
	if len(c.MCPServers) > 0 {
		tools := "all"
		if len(c.MCPTools) > 0 {
			names := make([]string, 0, len(c.MCPTools))
			for t := range c.MCPTools {
				names = append(names, t)
			}
			sort.Strings(names)
			tools = strings.Join(names, ", ")
		}
		line("MCP_TOOLS", tools)
		line("MCP_ALLOW_WRITE", c.MCPWrite)
		line("MCP_MAX_RESULT_CHARS", c.MCPMaxChars)
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
