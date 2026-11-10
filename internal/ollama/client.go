// Package ollama is a minimal client for the Ollama HTTP API.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Message struct {
	Role      string     `json:"role"` // system, user, assistant, tool
	Content   string     `json:"content"`
	Thinking  string     `json:"thinking,omitempty"`
	Images    [][]byte   `json:"images,omitempty"` // sent as base64
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"` // on role "tool" messages
}

// Tool is a function the model may call.
type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
}

type ToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

type Client struct {
	bases []string // the main server, then backups
	http  *http.Client

	mu     sync.Mutex
	down   map[int]time.Time // servers that couldn't be reached, until when they're skipped
	active int               // the server the last request reached
}

const (
	dialTimeout = 3 * time.Second // an address that doesn't answer (e.g. a LAN address while away) fails fast
	skipFor     = time.Minute     // an unreachable server is tried after the others for this long
)

// New returns a client for the Ollama server at baseURL. Requests that can't connect to it go to
// the backups in order.
func New(baseURL string, timeout time.Duration, backups ...string) *Client {
	dialer := &net.Dialer{Timeout: dialTimeout}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	return &Client{bases: append([]string{baseURL}, backups...), http: &http.Client{Timeout: timeout, Transport: tr},
		down: map[int]time.Time{}}
}

// Stats are the timings Ollama reports with each reply (durations are nanoseconds in JSON).
type Stats struct {
	TotalDuration      time.Duration `json:"total_duration"`
	LoadDuration       time.Duration `json:"load_duration"`
	PromptEvalCount    int           `json:"prompt_eval_count"`
	PromptEvalDuration time.Duration `json:"prompt_eval_duration"`
	EvalCount          int           `json:"eval_count"`
	EvalDuration       time.Duration `json:"eval_duration"`

	// Set by Add, over several calls (tool use): how many, and the first one's prompt time.
	// Ollama counts prompt tokens it reused from an earlier call too, so after the first call
	// the time says more than the count.
	Calls       int           `json:"calls,omitempty"`
	FirstPrompt time.Duration `json:"first_prompt,omitempty"`
}

func (s Stats) TokensPerSecond() float64       { return rate(s.EvalCount, s.EvalDuration) }
func (s Stats) PromptTokensPerSecond() float64 { return rate(s.PromptEvalCount, s.PromptEvalDuration) }

// Add accumulates another call's stats (for multi-step tool use).
func (s *Stats) Add(o Stats) {
	if s.Calls == 0 {
		s.FirstPrompt = o.PromptEvalDuration
	}
	s.Calls++
	s.TotalDuration += o.TotalDuration
	s.LoadDuration += o.LoadDuration
	s.PromptEvalCount += o.PromptEvalCount
	s.PromptEvalDuration += o.PromptEvalDuration
	s.EvalCount += o.EvalCount
	s.EvalDuration += o.EvalDuration
}

func rate(n int, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / d.Seconds()
}

type Result struct {
	Content   string
	Thinking  string
	ToolCalls []ToolCall
	Stats     Stats
}

// Chat sends the conversation and returns the assistant's reply.
// think is nil (model default), a bool, or a level string such as "low".
// tools may be nil; if the reply asks for tool calls they are in Result.ToolCalls.
func (c *Client) Chat(ctx context.Context, model string, msgs []Message, think any, tools []Tool) (Result, error) {
	req := map[string]any{"model": model, "messages": msgs, "stream": false}
	if think != nil {
		req["think"] = think
	}
	if len(tools) > 0 {
		req["tools"] = tools
	}
	body, _ := json.Marshal(req)
	var out struct {
		Message Message `json:"message"`
		Stats
	}
	if err := c.do(ctx, http.MethodPost, "/api/chat", body, &out); err != nil {
		return Result{}, err
	}
	return Result{
		Content:   strings.TrimSpace(out.Message.Content),
		Thinking:  out.Message.Thinking,
		ToolCalls: out.Message.ToolCalls,
		Stats:     out.Stats,
	}, nil
}

// Ping checks that Ollama is reachable, failing fast (it doesn't wait for a model).
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(len(c.bases))*dialTimeout)
	defer cancel()
	var out struct {
		Version string `json:"version"`
	}
	return c.do(ctx, http.MethodGet, "/api/version", nil, &out)
}

// ThinkInfo describes a model's thinking options, from /api/show.
// Values use "on"/"off" for true/false and keep level names (e.g. "low") as-is.
type ThinkInfo struct {
	Known   bool // false if Ollama didn't report thinking metadata for this model
	Values  []string
	Default string
}

func (t ThinkInfo) Supports(v string) bool {
	for _, x := range t.Values {
		if x == v {
			return true
		}
	}
	return false
}

// ModelInfo is what /api/show tells us about a model.
type ModelInfo struct {
	Think        ThinkInfo
	Capabilities []string // e.g. "completion", "tools", "vision", "thinking"
}

func (m ModelInfo) Has(capability string) bool {
	for _, c := range m.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// Info returns thinking options and capabilities for model.
func (c *Client) Info(ctx context.Context, model string) (ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"model": model})
	var out struct {
		Capabilities []string `json:"capabilities"`
		Thinking     *struct {
			Values  []any `json:"values"`
			Default any   `json:"default"`
		} `json:"thinking"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/show", body, &out); err != nil {
		return ModelInfo{}, err
	}
	info := ModelInfo{Capabilities: out.Capabilities}
	if out.Thinking == nil {
		return info, nil
	}
	info.Think.Default = thinkName(out.Thinking.Default)
	for _, v := range out.Thinking.Values {
		if n := thinkName(v); n != "" {
			info.Think.Values = append(info.Think.Values, n)
		}
	}
	info.Think.Known = len(info.Think.Values) > 0
	return info, nil
}

func thinkName(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "on"
		}
		return "off"
	case string:
		return x
	}
	return ""
}

// ThinkValue converts a setting ("on", "off", "low", ...) to the API value; "" means model default.
func ThinkValue(setting string) any {
	switch setting {
	case "":
		return nil
	case "on":
		return true
	case "off":
		return false
	}
	return setting
}

// Models lists the models installed on the Ollama server.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/tags", nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, len(out.Models))
	for i, m := range out.Models {
		names[i] = m.Name
	}
	return names, nil
}

// do sends a request to the first server that can be reached: servers that recently couldn't
// be are tried last. Only failing to connect moves on to the next; an error after connecting
// (which may mean the request was received) is returned.
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var err error
	for _, i := range c.order() {
		var resp *http.Response
		resp, err = c.send(ctx, c.bases[i], method, path, body)
		if err != nil && cantConnect(err) && ctx.Err() == nil {
			c.mu.Lock()
			c.down[i] = time.Now().Add(skipFor)
			c.mu.Unlock()
			continue
		}
		if err != nil {
			return err
		}
		c.reached(i)
		return decode(resp, path, out)
	}
	return err
}

func (c *Client) send(ctx context.Context, base, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}

// order is the servers to try: those not recently unreachable first, in configured order.
func (c *Client) order() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var up, down []int
	for i := range c.bases {
		if time.Now().Before(c.down[i]) {
			down = append(down, i)
		} else {
			up = append(up, i)
		}
	}
	return append(up, down...)
}

// reached notes the server a request got to, logging when that changes.
func (c *Client) reached(i int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.down, i)
	if i != c.active {
		slog.Info("ollama: switched server", "using", map[bool]string{true: "main", false: fmt.Sprintf("backup %d", i)}[i == 0])
		c.active = i
	}
}

// cantConnect reports whether err means the server couldn't be connected to at all.
func cantConnect(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func decode(resp *http.Response, path string, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("ollama %s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
