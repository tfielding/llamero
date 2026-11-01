// Package config loads settings from environment variables.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tfielding/signal-ollama/internal/toolbox"
	"github.com/tfielding/signal-ollama/internal/wol"
)

const defaultSystemPrompt = "You are a helpful assistant chatting over Signal on a phone. " +
	"Keep answers short and conversational unless asked for detail. " +
	"Formatting: you may use **bold**, *italic*, `code` and simple bullet lists. " +
	"Do not use headings, tables or horizontal rules."

type Config struct {
	SignalURL     string          // base URL of signal-cli-rest-api, e.g. http://signal-cli-rest-api:8080
	SignalNumber  string          // the account registered in the container
	OllamaURL     string          // e.g. http://192.168.1.50:11434
	DefaultModel  string          // empty = first model Ollama reports
	Allowed       map[string]bool // phone numbers (or Signal UUIDs) allowed to talk to the bot
	DataDir       string          // where chat history is stored
	HistoryLimit  int             // max messages (user+assistant) kept per chat
	Summarize     bool            // summarize messages past HistoryLimit instead of dropping them
	SummaryModel  string          // model that writes summaries; empty = the chat's model
	SystemPrompt  string
	OllamaTimeout time.Duration
	Reaction      string // emoji shown on a message while it's being answered; empty = off

	WakeMAC  net.HardwareAddr // Ollama machine's MAC for Wake-on-LAN; nil = off
	WakeAddr string           // where to send the magic packet (host:port)
	WakeWait time.Duration    // how long to wait for Ollama after a wake-up

	MCPServers    []toolbox.Server // from MCP_SERVERS=name=url,...; MCP_TOKEN_<NAME>, MCP_HINT_<NAME>
	MCPTools      map[string]bool  // optional allowlist of tool names
	MCPAllowWrite bool             // expose tools not marked read-only
	MCPConfirm    bool             // ask the user before each call to such a tool (MCP_ALLOW_WRITE=ask)
	MCPMaxChars   int              // cap on tool output given to the model
}

func Load() (Config, error) {
	c := Config{
		SignalURL:    strings.TrimRight(env("SIGNAL_URL", "http://signal-cli-rest-api:8080"), "/"),
		SignalNumber: os.Getenv("SIGNAL_NUMBER"),
		OllamaURL:    strings.TrimRight(env("OLLAMA_URL", "http://localhost:11434"), "/"),
		DefaultModel: os.Getenv("OLLAMA_MODEL"),
		Allowed:      map[string]bool{},
		DataDir:      env("DATA_DIR", "/data"),
		SystemPrompt: env("SYSTEM_PROMPT", defaultSystemPrompt),
		SummaryModel: os.Getenv("SUMMARY_MODEL"),
		Reaction:     env("REACTION", "👀"),
	}
	switch strings.ToLower(c.Reaction) {
	case "off", "none", "false":
		c.Reaction = ""
	}

	if c.SignalNumber == "" {
		return c, fmt.Errorf("SIGNAL_NUMBER is required")
	}
	for _, s := range strings.Split(os.Getenv("ALLOWED_SENDERS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.Allowed[s] = true
		}
	}
	if len(c.Allowed) == 0 {
		return c, fmt.Errorf("ALLOWED_SENDERS is required (comma-separated phone numbers)")
	}

	var err error
	if c.HistoryLimit, err = strconv.Atoi(env("HISTORY_LIMIT", "40")); err != nil || c.HistoryLimit < 2 {
		return c, fmt.Errorf("HISTORY_LIMIT must be an integer >= 2")
	}
	if c.Summarize, err = strconv.ParseBool(env("HISTORY_SUMMARY", "true")); err != nil {
		return c, fmt.Errorf("HISTORY_SUMMARY must be true or false")
	}
	if c.OllamaTimeout, err = time.ParseDuration(env("OLLAMA_TIMEOUT", "5m")); err != nil {
		return c, fmt.Errorf("OLLAMA_TIMEOUT: %w", err)
	}

	if v := os.Getenv("OLLAMA_WOL_MAC"); v != "" {
		if c.WakeMAC, err = wol.ParseMAC(v); err != nil {
			return c, fmt.Errorf("OLLAMA_WOL_MAC: %w", err)
		}
	}
	c.WakeAddr = env("OLLAMA_WOL_ADDR", wol.DefaultAddr)
	if _, _, err := net.SplitHostPort(c.WakeAddr); err != nil {
		return c, fmt.Errorf("OLLAMA_WOL_ADDR must be host:port, e.g. 192.168.1.255:9")
	}
	if c.WakeWait, err = time.ParseDuration(env("OLLAMA_WOL_WAIT", "60s")); err != nil || c.WakeWait <= 0 {
		return c, fmt.Errorf("OLLAMA_WOL_WAIT must be a duration like 60s")
	}

	if c.MCPServers, err = toolbox.ParseServers(os.Getenv("MCP_SERVERS"), func(kind, name string) string {
		return os.Getenv("MCP_" + kind + "_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name)))
	}); err != nil {
		return c, err
	}
	c.MCPTools = map[string]bool{}
	for _, t := range strings.Split(os.Getenv("MCP_TOOLS"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			c.MCPTools[t] = true
		}
	}
	if c.MCPMaxChars, err = strconv.Atoi(env("MCP_MAX_RESULT_CHARS", strconv.Itoa(toolbox.DefaultMaxResultChars))); err != nil || c.MCPMaxChars < 1000 {
		return c, fmt.Errorf("MCP_MAX_RESULT_CHARS must be an integer >= 1000")
	}
	switch v := strings.ToLower(os.Getenv("MCP_ALLOW_WRITE")); v {
	case "":
	case "ask":
		c.MCPAllowWrite, c.MCPConfirm = true, true
	default:
		if c.MCPAllowWrite, err = strconv.ParseBool(v); err != nil {
			return c, fmt.Errorf("MCP_ALLOW_WRITE must be false, ask or true")
		}
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Describe lists the effective settings by environment variable, for /settings.
// Secrets are never shown: tokens appear only as "set", phone numbers are masked
// and credentials in URLs are removed.
func (c Config) Describe() string {
	model := c.DefaultModel
	if os.Getenv("OLLAMA_MODEL") == "" {
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
	if len(c.MCPServers) == 0 {
		line("MCP_SERVERS", "none")
	}
	for _, s := range c.MCPServers {
		extra := []string{}
		if s.Token != "" {
			extra = append(extra, "token set")
		}
		if s.Hint != "" {
			extra = append(extra, "hint set")
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
		allowWrite := fmt.Sprint(c.MCPAllowWrite)
		if c.MCPConfirm {
			allowWrite = "ask"
		}
		line("MCP_ALLOW_WRITE", allowWrite)
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

// safeURL drops any user:password from a URL.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	u.User = nil
	return u.String()
}
