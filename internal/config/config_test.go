package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tfielding/signal-ollama/internal/toolbox"
)

func TestMain(m *testing.M) {
	// Don't pick up a real /config/config.yml on the machine running the tests.
	defaultFile = filepath.Join(os.TempDir(), "signal-ollama-test-no-such-config.yml")
	os.Exit(m.Run())
}

func TestLoad(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", " +15550000001 , +15550000002,")
	t.Setenv("OLLAMA_URL", "http://10.0.0.5:11434/")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Allowed) != 2 || !c.Allowed["+15550000002"] {
		t.Fatalf("allowed: %v", c.Allowed)
	}
	if c.OllamaURL != "http://10.0.0.5:11434" || c.HistoryLimit != 40 || !c.Summarize {
		t.Fatalf("%+v", c)
	}

	t.Setenv("HISTORY_SUMMARY", "nope")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for bad HISTORY_SUMMARY")
	}
	t.Setenv("HISTORY_SUMMARY", "")

	t.Setenv("ALLOWED_SENDERS", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error without ALLOWED_SENDERS")
	}
}

func TestDescribeHidesSecrets(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550001234,+15550005678")
	t.Setenv("OLLAMA_URL", "http://user:hunter2@10.0.0.5:11434")
	t.Setenv("MCP_SERVERS", "cams=http://go2rtc:8090/mcp")
	t.Setenv("MCP_TOKEN_CAMS", "s3cret-token")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	d := c.Describe()
	for _, secret := range []string{"s3cret-token", "hunter2", "user:", "+15550001234", "+15550000009"} {
		if strings.Contains(d, secret) {
			t.Errorf("settings show %q:\n%s", secret, d)
		}
	}
	for _, want := range []string{
		"SIGNAL_NUMBER: …0009",
		"ALLOWED_SENDERS: …1234, …5678",
		"OLLAMA_URL: http://10.0.0.5:11434",
		"MCP_SERVERS: cams=http://go2rtc:8090/mcp (token set)",
		"SYSTEM_PROMPT: default",
		"HISTORY_SUMMARY: true",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("settings missing %q:\n%s", want, d)
		}
	}
}

func TestWakeOnLAN(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WakeMAC != nil || c.WakeAddr != "255.255.255.255:9" || c.WakeWait != time.Minute {
		t.Fatalf("defaults: %+v", c)
	}

	t.Setenv("OLLAMA_WOL_MAC", "AA-BB-CC-DD-EE-FF")
	t.Setenv("OLLAMA_WOL_ADDR", "192.168.1.255:9")
	t.Setenv("OLLAMA_WOL_WAIT", "90s")
	if c, err = Load(); err != nil {
		t.Fatal(err)
	}
	if c.WakeMAC.String() != "aa:bb:cc:dd:ee:ff" || c.WakeAddr != "192.168.1.255:9" || c.WakeWait != 90*time.Second {
		t.Fatalf("set: %+v", c)
	}
	if d := c.Describe(); !strings.Contains(d, "OLLAMA_WOL_MAC: aa:bb:cc:dd:ee:ff") {
		t.Fatalf("describe: %s", d)
	}

	for k, bad := range map[string]string{"OLLAMA_WOL_MAC": "nope", "OLLAMA_WOL_ADDR": "192.168.1.255", "OLLAMA_WOL_WAIT": "soon"} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, bad)
			if _, err := Load(); err == nil {
				t.Fatalf("expected error for %s=%s", k, bad)
			}
		})
	}
}

func TestAllowWrite(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	t.Setenv("MCP_SERVERS", "tools=http://tools:9000/mcp")
	for v, want := range map[string]toolbox.Write{"": toolbox.WriteOff, "false": toolbox.WriteOff, "true": toolbox.WriteOn, "ASK": toolbox.WriteAsk} {
		t.Setenv("MCP_ALLOW_WRITE", v)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.MCPWrite != want || c.MCPServers[0].Write != want {
			t.Errorf("MCP_ALLOW_WRITE=%q: default %v, server %v", v, c.MCPWrite, c.MCPServers[0].Write)
		}
	}
	t.Setenv("MCP_ALLOW_WRITE", "ask")
	if c, _ := Load(); !strings.Contains(c.Describe(), "MCP_ALLOW_WRITE: ask") {
		t.Error("describe should show ask")
	}
	t.Setenv("MCP_ALLOW_WRITE", "maybe")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MCP_ALLOW_WRITE") {
		t.Errorf("expected error for MCP_ALLOW_WRITE=maybe, got %v", err)
	}
}

func TestReaction(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	for v, want := range map[string]string{"": "👀", "⏳": "⏳", "off": "", "NONE": ""} {
		t.Setenv("REACTION", v)
		if c, _ := Load(); c.Reaction != want {
			t.Errorf("REACTION=%q: got %q, want %q", v, c.Reaction, want)
		}
	}
}

func TestSafeURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://tools:9000/mcp":                "http://tools:9000/mcp",
		"http://user:pw@tools:9000/mcp":        "http://tools:9000/mcp",
		"http://tools:9000/mcp?token=SECRET":   "http://tools:9000/mcp?…",
		"http://tools:9000/mcp#key=SECRET":     "http://tools:9000/mcp?…",
		"https://u:p@h/mcp?api_key=SECRET#x=y": "https://h/mcp?…",
	} {
		if got := safeURL(in); got != want || strings.Contains(got, "SECRET") {
			t.Errorf("safeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKnowledgeSettings(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.KnowledgeDir != "/config/knowledge" || c.KnowledgeMaxChars != 8000 {
		t.Fatalf("defaults: %q %d", c.KnowledgeDir, c.KnowledgeMaxChars)
	}
	if d := c.Describe(); !strings.Contains(d, "KNOWLEDGE_DIR: /config/knowledge\n") || !strings.Contains(d, "KNOWLEDGE_MAX_CHARS: 8000\n") {
		t.Fatalf("describe:\n%s", d)
	}
	t.Setenv("KNOWLEDGE_DIR", "/opt/notes")
	t.Setenv("KNOWLEDGE_MAX_CHARS", "2000")
	if c, err = Load(); err != nil || c.KnowledgeDir != "/opt/notes" || c.KnowledgeMaxChars != 2000 {
		t.Fatalf("set: %q %d %v", c.KnowledgeDir, c.KnowledgeMaxChars, err)
	}
	t.Setenv("KNOWLEDGE_MAX_CHARS", "100")
	if _, err := Load(); err == nil {
		t.Fatal("KNOWLEDGE_MAX_CHARS=100 accepted")
	}
}

// writeConfig writes a config file and points CONFIG_FILE at it.
func writeConfig(t *testing.T, yml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	return path
}

const fullConfig = `
signal:
  number: ${SIGNAL_NUMBER_SECRET}
  allowed_senders: [+15550000001, "+15550000002"]
ollama:
  url: http://192.168.1.50:11434/
  model: llama3.2:latest
  timeout: 2m
history:
  limit: 20
  summary: false
reaction: "off"
knowledge:
  max_chars: 4000
mcp:
  allow_write: false
  servers:
    frigate:
      url: http://frigate-mcp:8091/mcp
    ha:
      url: http://homeassistant:8123/api/mcp
      token: ${HA_TOKEN}
      allow_write: ask
      tools: [GetLiveContext, HassTurnOn, HassTurnOff]
`

func TestConfigFile(t *testing.T) {
	path := writeConfig(t, fullConfig)
	t.Setenv("SIGNAL_NUMBER_SECRET", "+15550000009")
	t.Setenv("HA_TOKEN", "tok: with $pecial chars")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ConfigFile != path || c.SignalNumber != "+15550000009" || len(c.Allowed) != 2 || !c.Allowed["+15550000002"] ||
		c.OllamaURL != "http://192.168.1.50:11434" || c.DefaultModel != "llama3.2:latest" || c.OllamaTimeout != 2*time.Minute ||
		c.HistoryLimit != 20 || c.Summarize || c.Reaction != "" || c.KnowledgeMaxChars != 4000 ||
		c.SignalURL != "http://signal-cli-rest-api:8080" { // not in the file: default
		t.Fatalf("%+v", c)
	}
	if len(c.MCPServers) != 2 {
		t.Fatalf("servers: %+v", c.MCPServers)
	}
	fr, ha := c.MCPServers[0], c.MCPServers[1]
	if fr.Name != "frigate" || fr.Write != toolbox.WriteOff || fr.Token != "" || len(fr.Tools) != 0 {
		t.Errorf("frigate: %+v", fr)
	}
	if ha.Name != "ha" || ha.Write != toolbox.WriteAsk || ha.Token != "tok: with $pecial chars" || !ha.Tools["HassTurnOff"] || len(ha.Tools) != 3 {
		t.Errorf("ha: %+v", ha)
	}
	d := c.Describe()
	for _, want := range []string{"CONFIG_FILE: " + path, "MCP_SERVERS: ha=http://homeassistant:8123/api/mcp (token set, allow_write ask, tools GetLiveContext HassTurnOff HassTurnOn)",
		"MCP_SERVERS: frigate=http://frigate-mcp:8091/mcp\n", "OLLAMA_MODEL: llama3.2:latest\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("describe lacks %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "tok:") {
		t.Errorf("token shown:\n%s", d)
	}

	// Environment variables win over the file.
	t.Setenv("HISTORY_LIMIT", "8")
	t.Setenv("MCP_TOKEN_HA", "from-env")
	if c, err = Load(); err != nil || c.HistoryLimit != 8 || c.MCPServers[1].Token != "from-env" {
		t.Fatalf("env override: %d %q %v", c.HistoryLimit, c.MCPServers[1].Token, err)
	}
	// MCP_SERVERS replaces the file's servers.
	t.Setenv("MCP_SERVERS", "cams=http://go2rtc-mcp:8090/mcp")
	if c, err = Load(); err != nil || len(c.MCPServers) != 1 || c.MCPServers[0].Name != "cams" {
		t.Fatalf("MCP_SERVERS override: %+v %v", c.MCPServers, err)
	}
}

func TestConfigFileErrors(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER_SECRET", "+15550000009")
	t.Setenv("HA_TOKEN", "x")
	for name, tc := range map[string]struct{ yml, want string }{
		"unknown key":       {"signal:\n  number: \"+1\"\n  alowed_senders: [\"+2\"]\n", `line 3: unknown setting "alowed_senders"`},
		"list for value":    {"signal:\n  number: [\"+1\"]\n", "line 2: found a list, want a single value"},
		"value for section": {"signal: \"+1\"\n", "line 1: found a value, want a section with settings under it"},
		"unset var":         {"signal:\n  number: ${NOPE_NOT_SET}\n", "not set in the environment: ${NOPE_NOT_SET} (line 2)"},
		"bad value":         {"signal: {number: \"+1\", allowed_senders: \"+2\"}\nhistory: {limit: 1}\n", "history.limit in "},
		"bad write": {"signal: {number: \"+1\", allowed_senders: \"+2\"}\nmcp:\n  servers:\n    ha: {url: \"http://ha/mcp\", allow_write: maybe}\n",
			"mcp.servers.ha in "},
		"bad url":     {"signal: {number: \"+1\", allowed_senders: \"+2\"}\nmcp:\n  servers:\n    ha: {url: ha.lan}\n", "url must be like http://host:port/mcp"},
		"required":    {"ollama:\n  model: x\n", "signal.number in "},
		"not yaml":    {"signal: [unclosed\n", "yaml"},
		"var in flow": {"mcp:\n  servers:\n    ha: { url: http://ha/mcp, token: ${HA_TOKEN} }\n", `put ${VAR} in quotes`},
		"bad url 2":   {"signal: {number: \"+1\", allowed_senders: \"+2\"}\nollama: {url: \"192.168.1.50:11434\"}\n", "ollama.url in "},
	} {
		t.Run(name, func(t *testing.T) {
			writeConfig(t, tc.yml)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}

	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "missing.yml"))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "missing.yml") {
		t.Errorf("missing CONFIG_FILE: %v", err)
	}
}

func TestEmptyConfigFileUsesEnv(t *testing.T) {
	writeConfig(t, "# nothing yet\n")
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	if c, err := Load(); err != nil || c.SignalNumber != "+15550000009" || c.HistoryLimit != 40 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join("..", "..", "config.example.yml"))
	know := t.TempDir()
	os.WriteFile(filepath.Join(know, "ha.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(know, "frigate.md"), []byte("x"), 0o644)
	t.Setenv("KNOWLEDGE_DIR", know)
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001,+15550000002")
	t.Setenv("HA_TOKEN", "x")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.MCPServers) != 3 || c.MCPServers[2].Name != "ha" || c.MCPServers[2].Write != toolbox.WriteAsk || len(c.Allowed) != 2 {
		t.Fatalf("%+v", c.MCPServers)
	}
}

func TestServerKnowledge(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	know := t.TempDir()
	os.WriteFile(filepath.Join(know, "home-assistant.md"), []byte("ha notes"), 0o644)
	os.Mkdir(filepath.Join(know, "dir.md"), 0o755)
	t.Setenv("KNOWLEDGE_DIR", know)
	servers := "mcp:\n  servers:\n    cams:\n      url: http://cams/mcp\n    ha:\n      url: http://ha/mcp\n      knowledge: %s\n"

	writeConfig(t, fmt.Sprintf(servers, "home-assistant.md"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	refs := c.KnowledgeRefs()
	if len(refs) != 2 || refs["home-assistant.md"][0] != "ha" || refs["cams.md"][0] != "cams" { // cams: default name
		t.Fatalf("refs: %v", refs)
	}
	if !strings.Contains(c.Describe(), "ha=http://ha/mcp (knowledge home-assistant.md)") {
		t.Errorf("describe:\n%s", c.Describe())
	}

	for ref, want := range map[string]string{
		"missing.md":     `"missing.md": no such file in ` + know,
		"dir.md":         `"dir.md": no such file`,
		"notes.txt":      `"notes.txt": want a .md file name`,
		"../etc/x.md":    `want just a file name in`,
		"/abs/path/x.md": `want just a file name in`,
	} {
		writeConfig(t, fmt.Sprintf(servers, ref))
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "mcp.servers.ha in ") || !strings.Contains(err.Error(), want) {
			t.Errorf("knowledge %q: got %v, want %q", ref, err, want)
		}
	}
}
