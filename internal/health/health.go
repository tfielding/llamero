// Package health serves GET /healthz: whether the bot is working, for Docker, Uptime Kuma,
// Home Assistant and the like.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Checks are where the report's facts come from.
type Checks struct {
	Version string
	Started time.Time
	// Transports report each messaging app's connection, by name: connected, since when, and why
	// it last dropped.
	Transports map[string]func() (connected bool, since time.Time, lastErr error)
	// Ollama checks Ollama is reachable. It's reported but doesn't make the bot unhealthy:
	// with Wake-on-LAN a sleeping Ollama is normal.
	Ollama func(ctx context.Context) error
	// MCP lists the MCP servers and the last error of each (nil = working).
	MCP func() map[string]error
}

// Report is the /healthz body.
type Report struct {
	Status     string                     `json:"status"` // "ok" or "unhealthy"
	Version    string                     `json:"version"`
	Uptime     string                     `json:"uptime"`
	Transports map[string]TransportReport `json:"transports"`
	Ollama     OllamaReport               `json:"ollama"`
	MCP        map[string]string          `json:"mcp,omitempty"` // server -> "ok" or "unavailable (why)"
}

type TransportReport struct {
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since,omitzero"`
	Error     string    `json:"error,omitempty"`
}

type OllamaReport struct {
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

// Handler serves GET /healthz: 200 when the bot can receive messages on every transport it uses
// (Signal, Telegram), 503 when it can't. Ollama and MCP servers are reported in the body only.
func Handler(c Checks) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		rep := Report{Status: "ok", Version: c.Version, Uptime: time.Since(c.Started).Round(time.Second).String()}
		healthy := len(c.Transports) > 0
		rep.Transports = map[string]TransportReport{}
		for name, status := range c.Transports {
			ok, since, err := status()
			t := TransportReport{Connected: ok, Error: reason(err)}
			if ok {
				t.Since, t.Error = since.UTC(), ""
			} else if t.Error == "" {
				t.Error = "not connected yet"
			}
			rep.Transports[name] = t
			healthy = healthy && ok
		}
		if c.Ollama != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			err := c.Ollama(ctx)
			cancel()
			rep.Ollama = OllamaReport{Reachable: err == nil, Error: reason(err)}
		}
		if c.MCP != nil {
			servers := c.MCP()
			names := make([]string, 0, len(servers))
			for name := range servers {
				names = append(names, name)
			}
			sort.Strings(names)
			rep.MCP = map[string]string{}
			for _, name := range names {
				rep.MCP[name] = "ok"
				if err := servers[name]; err != nil {
					rep.MCP[name] = "unavailable (" + reason(err) + ")"
				}
			}
		}
		code := http.StatusOK
		if !healthy {
			rep.Status, code = "unhealthy", http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(rep)
	})
	return mux
}

// reason says why something failed without host names, IPs or URLs: the report may be shown
// in a monitoring tool, and those details stay in the bot's log.
func reason(err error) string {
	if err == nil {
		return ""
	}
	var dns *net.DNSError
	var ne net.Error
	var op *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timed out"
	case errors.As(err, &dns):
		return "host name not found"
	case errors.As(err, &op) && op.Err != nil:
		return clean(op.Err.Error())
	}
	return clean(err.Error())
}

// clean keeps a short error message and drops anything that looks like an address.
func clean(s string) string {
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:] // the innermost cause: "dial tcp 10.0.0.5:11434: connect: connection refused" → "connection refused"
	}
	for _, f := range strings.Fields(s) {
		if strings.Contains(f, "://") || strings.Count(f, ".") >= 3 || strings.Count(f, ":") >= 2 {
			return "error (details in the log)"
		}
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
