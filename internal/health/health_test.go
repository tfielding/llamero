package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, c Checks) (int, Report, string) {
	t.Helper()
	srv := httptest.NewServer(Handler(c))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var rep Report
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	return resp.StatusCode, rep, string(body)
}

var refused = &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 11434},
	Err: errors.New("connect: connection refused")}

func TestHealthy(t *testing.T) {
	since := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	code, rep, _ := get(t, Checks{
		Version: "v1.2.3", Started: time.Now().Add(-90 * time.Minute),
		Transports: map[string]func() (bool, time.Time, error){"signal": func() (bool, time.Time, error) { return true, since, nil }},
		Ollama:     func(context.Context) error { return nil },
		MCP:        func() map[string]error { return map[string]error{"frigate": nil, "ha": nil} },
	})
	if code != 200 || rep.Status != "ok" || rep.Version != "v1.2.3" || rep.Uptime != "1h30m0s" ||
		!rep.Transports["signal"].Connected || !rep.Transports["signal"].Since.Equal(since) || !rep.Ollama.Reachable ||
		rep.MCP["frigate"] != "ok" || rep.MCP["ha"] != "ok" {
		t.Fatalf("%d %+v", code, rep)
	}
}

func TestTransportDownIsUnhealthy(t *testing.T) {
	up := func() (bool, time.Time, error) { return true, time.Now(), nil }
	down := func() (bool, time.Time, error) { return false, time.Time{}, refused }
	code, rep, _ := get(t, Checks{Transports: map[string]func() (bool, time.Time, error){"signal": up, "telegram": down}})
	if code != 503 || rep.Status != "unhealthy" || !rep.Transports["signal"].Connected || rep.Transports["telegram"].Connected ||
		rep.Transports["telegram"].Error != "connection refused" {
		t.Fatalf("%d %+v", code, rep)
	}
	// At startup, before the first connection attempt has finished.
	code, rep, _ = get(t, Checks{Transports: map[string]func() (bool, time.Time, error){"signal": func() (bool, time.Time, error) { return false, time.Time{}, nil }}})
	if code != 503 || rep.Transports["signal"].Error != "not connected yet" {
		t.Fatalf("%d %+v", code, rep)
	}
	// No transports at all can't receive anything.
	if code, _, _ = get(t, Checks{}); code != 503 {
		t.Fatalf("%d %+v", code, rep)
	}
}

func TestOllamaAndMCPDontMakeItUnhealthy(t *testing.T) {
	code, rep, _ := get(t, Checks{
		Transports: map[string]func() (bool, time.Time, error){"signal": func() (bool, time.Time, error) { return true, time.Now(), nil }},
		Ollama:     func(context.Context) error { return refused },
		MCP: func() map[string]error {
			return map[string]error{"ha": fmt.Errorf("list tools: %w", context.DeadlineExceeded), "frigate": nil}
		},
	})
	if code != 200 || rep.Ollama.Reachable || rep.Ollama.Error != "connection refused" ||
		rep.MCP["ha"] != "unavailable (timed out)" || rep.MCP["frigate"] != "ok" {
		t.Fatalf("%d %+v", code, rep)
	}
}

func TestNoAddressesInTheReport(t *testing.T) {
	_, _, body := get(t, Checks{
		Transports: map[string]func() (bool, time.Time, error){"signal": func() (bool, time.Time, error) {
			return false, time.Time{}, &net.DNSError{Err: "no such host", Name: "signal-cli.home.example"}
		}},
		Ollama: func(context.Context) error { return refused },
		MCP: func() map[string]error {
			return map[string]error{"ha": errors.New(`Get "http://homeassistant:8123/api/mcp?token=abc": EOF from 10.0.0.7:8123`)}
		},
	})
	for _, leak := range []string{"10.0.0", "homeassistant:8123", "home.example", "token", "11434"} {
		if strings.Contains(body, leak) {
			t.Errorf("report contains %q:\n%s", leak, body)
		}
	}
	if !strings.Contains(body, `"host name not found"`) {
		t.Errorf("DNS error not explained:\n%s", body)
	}
}

func TestOnlyGET(t *testing.T) {
	srv := httptest.NewServer(Handler(Checks{}))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/healthz", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
}
