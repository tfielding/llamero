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
		"missing.md":     `"missing.md": no such file in ` + know + ` (it has: dir.md, home-assistant.md)`,
		"dir.md":         `"dir.md" in ` + know + ` is not a regular file`,
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

func TestServerKnowledgeUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read anything")
	}
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	know := t.TempDir()
	os.WriteFile(filepath.Join(know, "ha.md"), []byte("notes"), 0o000)
	t.Setenv("KNOWLEDGE_DIR", know)
	writeConfig(t, "mcp:\n  servers:\n    ha:\n      url: http://ha/mcp\n      knowledge: ha.md\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), `"ha.md": can't read it: permission denied`) {
		t.Errorf("unreadable file: %v", err)
	}

	os.Chmod(filepath.Join(know, "ha.md"), 0o644)
	os.Chmod(know, 0o000)
	defer os.Chmod(know, 0o755)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "can't open the folder") || !strings.Contains(err.Error(), "chmod 755") {
		t.Errorf("unreadable folder: %v", err)
	}

	os.Chmod(know, 0o755)
	t.Setenv("KNOWLEDGE_DIR", filepath.Join(know, "nope"))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "doesn't exist (is /config mounted?)") {
		t.Errorf("missing folder: %v", err)
	}
}

func TestSchedulesSetting(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	c, err := Load()
	if err != nil || !c.Schedules || !strings.Contains(c.Describe(), "SCHEDULES: true\n") {
		t.Fatalf("default: %v %v", c.Schedules, err)
	}
	t.Setenv("SCHEDULES", "false")
	if c, err = Load(); err != nil || c.Schedules {
		t.Fatalf("SCHEDULES=false: %v %v", c.Schedules, err)
	}
	t.Setenv("SCHEDULES", "")
	writeConfig(t, "schedules:\n  enabled: false\n")
	if c, err = Load(); err != nil || c.Schedules {
		t.Fatalf("schedules.enabled: false: %v %v", c.Schedules, err)
	}
	writeConfig(t, "schedules:\n  enabled: sometimes\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "schedules.enabled in ") {
		t.Fatalf("bad value: %v", err)
	}
}

func TestHealthAddr(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	for in, want := range map[string]string{"": ":8092", ":9000": ":9000", "127.0.0.1:8092": "127.0.0.1:8092", "OFF": "", "none": ""} {
		t.Setenv("HEALTH_ADDR", in)
		c, err := Load()
		if err != nil || c.HealthAddr != want {
			t.Errorf("HEALTH_ADDR=%q: %q %v", in, c.HealthAddr, err)
		}
	}
	t.Setenv("HEALTH_ADDR", "")
	if c, _ := Load(); !strings.Contains(c.Describe(), "HEALTH_ADDR: :8092\n") {
		t.Error("describe lacks HEALTH_ADDR")
	}
	for _, bad := range []string{"8092", ":0", ":99999", "localhost", ":http"} {
		t.Setenv("HEALTH_ADDR", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "HEALTH_ADDR must be [host]:port like :8092, or off") {
			t.Errorf("HEALTH_ADDR=%q: %v", bad, err)
		}
	}
	t.Setenv("HEALTH_ADDR", "")
	writeConfig(t, "health:\n  addr: off\n")
	if c, err := Load(); err != nil || c.HealthAddr != "" {
		t.Errorf("health.addr: off: %q %v", c.HealthAddr, err)
	}
}

func TestVisionModel(t *testing.T) {
	t.Setenv("SIGNAL_NUMBER", "+15550000009")
	t.Setenv("ALLOWED_SENDERS", "+15550000001")
	if c, err := Load(); err != nil || c.VisionModel != "" || !strings.Contains(c.Describe(), "OLLAMA_VISION_MODEL: not set") {
		t.Fatalf("default: %q %v", c.VisionModel, err)
	}
	writeConfig(t, "ollama:\n  vision_model: gemma3:4b\n")
	if c, err := Load(); err != nil || c.VisionModel != "gemma3:4b" || !strings.Contains(c.Describe(), "OLLAMA_VISION_MODEL: gemma3:4b\n") {
		t.Fatalf("from the file: %q %v", c.VisionModel, err)
	}
	t.Setenv("OLLAMA_VISION_MODEL", "qwen2.5vl:7b")
	if c, err := Load(); err != nil || c.VisionModel != "qwen2.5vl:7b" {
		t.Fatalf("env wins: %q %v", c.VisionModel, err)
	}
}

func TestWebhookConfig(t *testing.T) {
	base := "signal: {number: \"+15550000009\", allowed_senders: [\"+15550000001\", \"+15550000002\"]}\n" +
		"ollama: {wake_on_lan: {mac: \"aa:bb:cc:dd:ee:ff\"}}\n"
	writeConfig(t, base+`webhooks:
  token: ${HOOK_SECRET}
  hooks:
    camera_alert:
      task: Look at the alert and say what happened.
      only_if: a person is at the door at night
      to: ["+15550000001"]
      wake: true
      group: 1m
      cooldown: 5m
      model: small-vision
      think: false
      tools: [review_alert, get_camera_frame]
    leak:
      group: 0
`)
	t.Setenv("HOOK_SECRET", "0123456789abcdef0123")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WebhookToken != "0123456789abcdef0123" || len(c.Hooks) != 2 {
		t.Fatalf("%q %+v", c.WebhookToken, c.Hooks)
	}
	cam, leak := c.Hooks[0], c.Hooks[1]
	if cam.Name != "camera_alert" || cam.Task != "Look at the alert and say what happened." || cam.OnlyIf != "a person is at the door at night" ||
		len(cam.To) != 1 || cam.To[0] != "+15550000001" || !cam.Wake || cam.Group != time.Minute || cam.Cooldown != 5*time.Minute ||
		cam.Model != "small-vision" || cam.Think != "off" || strings.Join(cam.Tools, ",") != "review_alert,get_camera_frame" {
		t.Errorf("camera_alert: %+v", cam)
	}
	if leak.Name != "leak" || leak.Task != "" || leak.Group != 0 || leak.Wake || len(leak.To) != 0 {
		t.Errorf("leak: %+v", leak)
	}
	d := c.Describe()
	for _, want := range []string{"WEBHOOK_TOKEN: set\n",
		"HOOK: /hooks/camera_alert (task: Look at the alert and say what happened.; only if: a person is at the door at night; to …0001; wakes Ollama; model small-vision; think off; tools review_alert, get_camera_frame; group 1m0s; cooldown 5m0s)",
		"HOOK: /hooks/leak (sends events on unchecked; group 0s)"} {
		if !strings.Contains(d, want) {
			t.Errorf("settings lack %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "0123456789abcdef") {
		t.Error("token shown in settings")
	}

	// The default group, and WEBHOOK_TOKEN from the environment.
	writeConfig(t, base+"webhooks:\n  hooks:\n    door: {task: say who it is}\n")
	t.Setenv("WEBHOOK_TOKEN", "fedcba9876543210fedc")
	if c, err = Load(); err != nil || c.WebhookToken != "fedcba9876543210fedc" || c.Hooks[0].Group != 30*time.Second {
		t.Fatalf("%v %+v", err, c.Hooks)
	}
	t.Setenv("WEBHOOK_TOKEN", "")

	for name, tc := range map[string]struct{ yml, want string }{
		"no token":      {"webhooks:\n  hooks:\n    door: {}\n", "webhooks.token in "},
		"short token":   {"webhooks:\n  token: short\n  hooks:\n    door: {}\n", "must be at least 16 characters"},
		"bad name":      {"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    \"front door\": {}\n", `"front door" is not a valid hook name`},
		"only_if":       {"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {only_if: x}\n", "only_if needs a task"},
		"stranger":      {"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {to: [\"+15550009999\"]}\n", "to: …9999 is not in signal.allowed_senders"},
		"bad wake":      {"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {wake: often}\n", "wake must be true or false"},
		"bad cooldown":  {"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {cooldown: 2d}\n", "cooldown must be a duration from 0 to 24h"},
		"bad group":     {"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {group: 2h}\n", "group must be a duration from 0 to 10m0s"},
		"health off":    {"health: {addr: \"off\"}\nwebhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {}\n", "which is off"},
		"bad think":     {"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {task: x, think: lots}\n", "think must be on, off, low, medium or high"},
		"model no task": {"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {model: m}\n", "model, think and tools need a task"},
		"unknown key":   {"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {tsk: x}\n", `unknown setting "tsk"`},
	} {
		t.Run(name, func(t *testing.T) {
			writeConfig(t, base+tc.yml)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
	writeConfig(t, "signal: {number: \"+15550000009\", allowed_senders: \"+15550000001\"}\n"+
		"webhooks:\n  token: 0123456789abcdef0\n  hooks:\n    door: {wake: true}\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "wake needs Wake-on-LAN") {
		t.Errorf("wake without WoL: %v", err)
	}
}
