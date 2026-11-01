package config

import (
	"strings"
	"testing"
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
