package config

import (
	"strings"
	"testing"
	"time"
)

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
