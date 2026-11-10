// Package toolbox connects to MCP servers and exposes their tools to the model.
// It knows nothing about any particular tool: servers are configured by URL.
package toolbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tfielding/llamero/internal/app"
)

const (
	refreshEvery = time.Minute
	listTimeout  = 5 * time.Second // don't hold up a reply for a server that's down
	callTimeout  = 60 * time.Second
	// DefaultMaxResultChars caps tool output given to the model (about 8k tokens).
	DefaultMaxResultChars = 32000
)

// Server is one MCP server (Streamable HTTP).
type Server struct {
	Name  string
	URL   string
	Token string          // optional bearer token
	Write Write           // what to do with its tools that aren't marked read-only
	Tools map[string]bool // if non-empty, only these of its tools (names as the server gives them)
	// ReadOnly is its tools to treat as read-only although the server doesn't mark them (names as
	// the server gives them): they're offered without asking, and hooks and schedules get them.
	ReadOnly map[string]bool
	// Knowledge is the file in the knowledge folder with notes for its tools; empty = <Name>.md if it exists.
	Knowledge string
}

// Write is what happens with tools that can change things (not marked read-only).
type Write int

const (
	WriteOff Write = iota // hidden from the model
	WriteAsk              // offered; each call waits for the user's approval
	WriteOn               // offered and run without asking
)

// ParseWrite reads false, ask or true (also off/no and on/yes).
func ParseWrite(s string) (Write, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "off", "no", "0":
		return WriteOff, nil
	case "ask":
		return WriteAsk, nil
	case "true", "on", "yes", "1":
		return WriteOn, nil
	}
	return WriteOff, fmt.Errorf("%q: want false, ask or true", s)
}

func (w Write) String() string {
	return [...]string{"false", "ask", "true"}[w]
}

// maxGuidanceChars caps the instructions each server sends, in the prompt.
const maxGuidanceChars = 2000

// Tool is a tool as exposed to the model.
type Tool struct {
	Name        string // name the model sees (prefixed with the server name only on collisions)
	Description string
	Schema      any
	ReadOnly    bool
	Confirm     bool // calls wait for the user's approval (it can change things, and its server says ask)
	server      string
	remote      string // name on the server
}

// Image is image content returned by a tool.
type Image struct {
	MIME string
	Data []byte
}

// Output is a tool result, reduced to what the bot needs.
type Output struct {
	Text      string
	Images    []Image
	IsError   bool
	Dropped   []string // arguments removed because the tool doesn't accept them
	Truncated int      // characters cut from Text (0 = complete)
	Retried   string   // if set, the call with arguments failed with this error and was retried without them
}

type Options struct {
	Allowed        map[string]bool // if non-empty, only these tool names are exposed (across servers)
	MaxResultChars int             // 0 = DefaultMaxResultChars
}

type Box struct {
	servers []Server
	opt     Options
	client  *mcp.Client

	mu       sync.Mutex
	sessions map[string]*mcp.ClientSession
	tools    []Tool
	errs     map[string]error // last listing error per server
	fetched  time.Time
	hidden   map[string]bool   // tools already logged as hidden
	instr    map[string]string // instructions each server sent when we connected
}

func New(servers []Server, opt Options) *Box {
	return &Box{
		servers:  servers,
		opt:      opt,
		client:   mcp.NewClient(&mcp.Implementation{Name: app.Name, Version: "1"}, nil),
		sessions: map[string]*mcp.ClientSession{},
		errs:     map[string]error{},
		hidden:   map[string]bool{},
		instr:    map[string]string{},
	}
}

// Tools returns the exposed tools, refreshing the list at most once a minute.
// Servers that are down are skipped (see Errors).
func (b *Box) Tools(ctx context.Context) []Tool {
	b.mu.Lock()
	fresh := time.Since(b.fetched) < refreshEvery
	tools := b.tools
	b.mu.Unlock()
	if fresh {
		return tools
	}

	var all []Tool
	errs := map[string]error{}
	for _, s := range b.servers {
		lctx, cancel := context.WithTimeout(ctx, listTimeout)
		ts, err := b.list(lctx, s)
		cancel()
		if err != nil {
			slog.Warn("mcp server unavailable", "server", s.Name, "err", err)
			errs[s.Name] = err
			continue
		}
		b.checkNames(s, ts)
		all = append(all, ts...)
	}
	// Filter first, so a tool a server's tools: list leaves out doesn't make another server's
	// tool of the same name get a prefix.
	all = dedupe(b.filter(all))
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })

	b.mu.Lock()
	b.tools, b.errs, b.fetched = all, errs, time.Now()
	b.mu.Unlock()
	return all
}

// Errors returns the servers whose last listing failed.
func (b *Box) Errors() map[string]error {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]error{}
	for k, v := range b.errs {
		out[k] = v
	}
	return out
}

// Call runs a tool by its exposed name.
func (b *Box) Call(ctx context.Context, name string, args map[string]any) (Output, error) {
	var tool *Tool
	for _, t := range b.Tools(ctx) {
		if t.Name == name {
			tool = &t
			break
		}
	}
	if tool == nil {
		return Output{}, fmt.Errorf("no tool named %q is available", name)
	}
	srv, _ := b.server(tool.server)
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	args, dropped := cleanArgs(tool.Schema, args)

	call := func(args map[string]any) (*mcp.CallToolResult, error) {
		var res *mcp.CallToolResult
		err := b.withSession(ctx, srv, func(cs *mcp.ClientSession) error {
			var err error
			res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: tool.remote, Arguments: args})
			return err
		})
		return res, err
	}
	res, err := call(args)
	if err != nil {
		return Output{Dropped: dropped}, err
	}
	out := convert(res, b.maxChars())
	out.Dropped = dropped

	// Small models often guess optional values that don't exist ("area: All"). If every
	// parameter is optional, a call without them is valid, so retry once and tell the
	// model what happened.
	// Only for read-only tools: retrying an action with its arguments removed could
	// widen what it does (e.g. "turn off" without an area).
	if out.IsError && tool.ReadOnly && len(args) > 0 && !hasRequired(tool.Schema) {
		failure := strings.TrimSpace(out.Text)
		if res2, err2 := call(map[string]any{}); err2 == nil && !res2.IsError {
			all := convert(res2, b.maxChars())
			all.Dropped = dropped
			all.Retried = failure
			all.Text = fmt.Sprintf("[The call with your optional parameters failed (%s), so this is the result without them. Pick out what the user asked for.]\n%s", failure, all.Text)
			return all, nil
		}
	}
	return out, nil
}

// Blank reports whether an argument value is empty: nil, a blank string, or an empty list or object.
func Blank(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// hasRequired reports whether the input schema lists any required parameters.
func hasRequired(schema any) bool {
	raw, err := json.Marshal(schema)
	if err != nil {
		return true
	}
	var s struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return true
	}
	return len(s.Required) > 0
}

// cleanArgs removes arguments the tool's input schema doesn't declare. Small models
// often invent parameters (e.g. passing "area" to a tool that takes none), which
// makes the server reject the call. Tools that accept extra properties are left alone.
func cleanArgs(schema any, args map[string]any) (map[string]any, []string) {
	clean := map[string]any{}
	// Blank values are almost always the model filling in optional parameters it
	// doesn't mean to use ("area": "", "domain": []), which some servers treat as a filter.
	var blank []string
	for k, v := range args {
		if Blank(v) {
			blank = append(blank, k)
		}
	}
	if len(blank) > 0 {
		args = maps.Clone(args)
		for _, k := range blank {
			delete(args, k)
		}
	}
	if len(args) == 0 {
		sort.Strings(blank)
		return clean, blank
	}
	args, dropped := cleanUndeclared(schema, args)
	args = wrapLists(schema, args)
	dropped = append(dropped, blank...)
	sort.Strings(dropped)
	return args, dropped
}

// wrapLists turns a single value into a one-item list where the schema wants a list.
// Small models often send "domain": "light" for a parameter typed as an array, which
// the server rejects, and they repeat the same call when told so.
func wrapLists(schema any, args map[string]any) map[string]any {
	raw, err := json.Marshal(schema)
	if err != nil {
		return args
	}
	var s struct {
		Properties map[string]struct {
			Type any `json:"type"` // "array", or a list of types
		} `json:"properties"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return args
	}
	var out map[string]any
	for k, v := range args {
		if _, isList := v.([]any); isList || !onlyArray(s.Properties[k].Type) {
			continue
		}
		if out == nil {
			out = maps.Clone(args)
		}
		out[k] = []any{v}
	}
	if out == nil {
		return args
	}
	return out
}

// onlyArray reports whether a JSON schema "type" allows an array and nothing else
// that a single value could already be (e.g. "array", or ["array", "null"]).
func onlyArray(t any) bool {
	switch x := t.(type) {
	case string:
		return x == "array"
	case []any:
		hasArray := false
		for _, e := range x {
			switch e {
			case "array":
				hasArray = true
			case "null":
			default:
				return false
			}
		}
		return hasArray
	}
	return false
}

func cleanUndeclared(schema any, args map[string]any) (map[string]any, []string) {
	clean := map[string]any{}
	raw, err := json.Marshal(schema)
	if err != nil {
		return args, nil
	}
	var s struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return args, nil
	}
	extraOK := len(s.AdditionalProperties) > 0 && string(s.AdditionalProperties) != "false"
	if extraOK || (s.Properties == nil && len(s.AdditionalProperties) == 0 && !emptyObject(raw)) {
		return args, nil // schema allows anything, or says nothing we can rely on
	}
	var dropped []string
	for k, v := range args {
		if _, ok := s.Properties[k]; ok {
			clean[k] = v
		} else {
			dropped = append(dropped, k)
		}
	}
	sort.Strings(dropped)
	return clean, dropped
}

// emptyObject reports whether a schema is {"type":"object"} with no properties at all,
// which for tool inputs means "takes no arguments".
func emptyObject(raw []byte) bool {
	var s map[string]any
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	_, hasProps := s["properties"]
	return s["type"] == "object" && !hasProps
}

func (b *Box) maxChars() int {
	if b.opt.MaxResultChars > 0 {
		return b.opt.MaxResultChars
	}
	return DefaultMaxResultChars
}

func convert(res *mcp.CallToolResult, max int) Output {
	out := Output{IsError: res.IsError}
	var text []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			text = append(text, unwrap(v.Text))
		case *mcp.ImageContent:
			out.Images = append(out.Images, Image{MIME: v.MIMEType, Data: v.Data})
		}
	}
	out.Text = strings.Join(text, "\n")
	if len(out.Text) > max {
		out.Truncated = len(out.Text) - max
		cut := max
		for cut > 0 && !utf8.RuneStart(out.Text[cut]) {
			cut--
		}
		out.Text = out.Text[:cut] + fmt.Sprintf("\n…(truncated: %d more characters not shown)", out.Truncated)
	}
	return out
}

// unwrap turns a JSON object holding a single string field, like {"result": "line\nline"},
// into "result:" followed by that string (the key is kept, since it can carry meaning). Some servers double-encode text this way, which hides line breaks
// as escaped "\n" and costs extra tokens. Anything else is returned unchanged.
func unwrap(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") {
		return s
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(t), &obj) != nil || len(obj) != 1 {
		return s
	}
	for key, raw := range obj {
		var inner string
		if json.Unmarshal(raw, &inner) == nil {
			return key + ":\n" + inner
		}
	}
	return s
}

// Server returns the name of the MCP server that provides t.
func (t Tool) Server() string { return t.server }

// CleanArgs returns args as Call will send them to t: blank values and parameters
// the schema doesn't declare are removed and listed in dropped.
func (t Tool) CleanArgs(args map[string]any) (clean map[string]any, dropped []string) {
	return cleanArgs(t.Schema, args)
}

// Guidance returns per-server usage notes for the servers behind tools: the instructions
// each server sent itself, plus serverNotes (by server name, from the knowledge folder, already
// size-limited). Empty if there are none.
func (b *Box) Guidance(tools []Tool, serverNotes map[string]string) string {
	seen := map[string]bool{}
	var parts []string
	for _, t := range tools {
		if seen[t.server] {
			continue
		}
		seen[t.server] = true
		b.mu.Lock()
		text := strings.TrimSpace(b.instr[t.server])
		b.mu.Unlock()
		if len(text) > maxGuidanceChars {
			cut := maxGuidanceChars
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
			text = text[:cut] + "…"
		}
		if extra := strings.TrimSpace(serverNotes[t.server]); extra != "" {
			text = strings.TrimSpace(text + "\n\n" + extra)
		}
		if text == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("Notes for the %q tools:\n%s", t.server, text))
	}
	return strings.Join(parts, "\n\n")
}

func (b *Box) list(ctx context.Context, s Server) ([]Tool, error) {
	var tools []Tool
	err := b.withSession(ctx, s, func(cs *mcp.ClientSession) error {
		tools = nil
		for t, err := range cs.Tools(ctx, nil) {
			if err != nil {
				return err
			}
			ro := t.Annotations != nil && t.Annotations.ReadOnlyHint || s.ReadOnly[t.Name]
			tools = append(tools, Tool{
				Name: t.Name, Description: t.Description, Schema: t.InputSchema,
				ReadOnly: ro, server: s.Name, remote: t.Name,
			})
		}
		return nil
	})
	return tools, err
}

// withSession runs fn on a session to s, reconnecting once if the session went bad
// (e.g. the server restarted).
func (b *Box) withSession(ctx context.Context, s Server, fn func(*mcp.ClientSession) error) error {
	for attempt := 0; ; attempt++ {
		cs, err := b.session(ctx, s)
		if err != nil {
			return err
		}
		err = fn(cs)
		if err == nil || attempt > 0 || ctx.Err() != nil {
			return err
		}
		slog.Info("mcp call failed, reconnecting", "server", s.Name, "err", err)
		b.drop(s.Name, cs)
	}
}

func (b *Box) session(ctx context.Context, s Server) (*mcp.ClientSession, error) {
	b.mu.Lock()
	cs := b.sessions[s.Name]
	b.mu.Unlock()
	if cs != nil {
		return cs, nil
	}
	hc := &http.Client{Timeout: callTimeout + 5*time.Second}
	if s.Token != "" {
		hc.Transport = bearer{s.Token}
	}
	cs, err := b.client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: s.URL, HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if existing := b.sessions[s.Name]; existing != nil { // lost a race; keep the first
		b.mu.Unlock()
		cs.Close()
		return existing, nil
	}
	b.sessions[s.Name] = cs
	if ir := cs.InitializeResult(); ir != nil {
		b.instr[s.Name] = strings.TrimSpace(ir.Instructions)
	}
	b.mu.Unlock()
	slog.Info("connected to mcp server", "server", s.Name, "url", s.URL)
	return cs, nil
}

func (b *Box) drop(name string, cs *mcp.ClientSession) {
	b.mu.Lock()
	if b.sessions[name] == cs {
		delete(b.sessions, name)
	}
	b.mu.Unlock()
	cs.Close()
}

func (b *Box) server(name string) (Server, bool) {
	for _, s := range b.servers {
		if s.Name == name {
			return s, true
		}
	}
	return Server{}, false
}

// filter applies the allowlists and each server's write setting.
func (b *Box) filter(tools []Tool) []Tool {
	var out []Tool
	for _, t := range tools {
		srv, _ := b.server(t.server)
		// mcp.tools takes the name as the server gives it, or with the server prefix that
		// collisions get (as earlier versions required).
		if len(b.opt.Allowed) > 0 && !b.opt.Allowed[t.Name] && !b.opt.Allowed[t.server+"_"+t.Name] {
			continue
		}
		if len(srv.Tools) > 0 && !srv.Tools[t.remote] {
			continue
		}
		if !t.ReadOnly && srv.Write == WriteOff {
			b.mu.Lock()
			first := !b.hidden[t.server+"/"+t.Name]
			b.hidden[t.server+"/"+t.Name] = true
			b.mu.Unlock()
			if first {
				slog.Info("hiding tool not marked read-only (allow with the server's allow_write: ask or true)", "tool", t.Name, "server", t.server)
			}
			continue
		}
		t.Confirm = !t.ReadOnly && srv.Write == WriteAsk
		out = append(out, t)
	}
	return out
}

// checkNames logs, once each, names in s's read_only and tools lists that s doesn't have: a
// misspelled name would otherwise leave a tool out of hooks and schedules (read_only) or out
// altogether (tools) without a word.
func (b *Box) checkNames(s Server, ts []Tool) {
	if len(s.ReadOnly) == 0 && len(s.Tools) == 0 {
		return
	}
	have := map[string]bool{}
	names := make([]string, len(ts))
	for i, t := range ts {
		have[t.remote] = true
		names[i] = t.remote
	}
	for _, list := range []struct {
		setting string
		names   map[string]bool
	}{{"read_only", s.ReadOnly}, {"tools", s.Tools}} {
		for _, name := range slices.Sorted(maps.Keys(list.names)) {
			if have[name] {
				continue
			}
			key := list.setting + "/" + s.Name + "/" + name
			b.mu.Lock()
			first := !b.hidden[key]
			b.hidden[key] = true
			b.mu.Unlock()
			if !first {
				continue
			}
			args := []any{"server", s.Name, "tool", name}
			if alt := Suggest(name, names); alt != "" {
				args = append(args, "did_you_mean", alt)
			}
			slog.Warn(list.setting+" lists a tool the server doesn't have", args...)
		}
	}
}

// Suggest is the name in names that name was probably meant to be: the same but for case, the
// same with a prefix (servers like name__ or server_ prefixes), or one or two typos away. ""
// if none is close.
func Suggest(name string, names []string) string {
	sorted := slices.Sorted(slices.Values(names))
	want := strings.ToLower(name)
	for _, n := range sorted {
		if l := strings.ToLower(n); l == want || strings.HasSuffix(l, "_"+want) {
			return n
		}
	}
	if len([]rune(want)) < 5 { // short names are too close to everything
		return ""
	}
	best, bestDist := "", 3
	for _, n := range sorted {
		if d := editDistance(want, strings.ToLower(n)); d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

// editDistance is the number of single-character edits from a to b (Levenshtein).
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := range ra {
		cur := make([]int, len(rb)+1)
		cur[0] = i + 1
		for j := range rb {
			cost := 1
			if ra[i] == rb[j] {
				cost = 0
			}
			cur[j+1] = min(prev[j+1]+1, cur[j]+1, prev[j]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// dedupe prefixes tool names with their server name when two servers use the same name.
func dedupe(tools []Tool) []Tool {
	count := map[string]int{}
	for _, t := range tools {
		count[t.Name]++
	}
	for i := range tools {
		if count[tools[i].Name] > 1 {
			tools[i].Name = tools[i].server + "_" + tools[i].Name
		}
	}
	return tools
}

// Close ends all sessions.
func (b *Box) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for name, cs := range b.sessions {
		cs.Close()
		delete(b.sessions, name)
	}
}

// ParseServers parses "name=url,name2=url2". setting(kind, name) returns per-server
// settings; kind is "TOKEN" (bearer token).
func ParseServers(spec string, setting func(kind, name string) string) ([]Server, error) {
	var out []Server
	seen := map[string]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, url, ok := strings.Cut(part, "=")
		name, url = strings.TrimSpace(name), strings.TrimSpace(url)
		if !ok || name == "" || !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
			return nil, fmt.Errorf("MCP_SERVERS entry %q: want name=http://host:port/mcp", part)
		}
		if seen[name] {
			return nil, fmt.Errorf("MCP_SERVERS: duplicate name %q", name)
		}
		seen[name] = true
		out = append(out, Server{Name: name, URL: url, Token: setting("TOKEN", name)})
	}
	return out, nil
}

type bearer struct{ token string }

func (t bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}
