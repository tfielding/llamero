package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tfielding/llamero/internal/ollama"
	"github.com/tfielding/llamero/internal/toolbox"
	"github.com/tfielding/llamero/internal/transport"
)

type fakeTools struct {
	mu    sync.Mutex
	tools []toolbox.Tool
	calls []map[string]any
	fail  string            // if set, every call returns this error
	notes map[string]string // last per-server notes passed to Guidance
	out   []toolbox.Image   // pictures every successful call returns
}

func (f *fakeTools) Tools(context.Context) []toolbox.Tool { return f.tools }
func (f *fakeTools) Call(_ context.Context, name string, args map[string]any) (toolbox.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	if f.fail != "" {
		return toolbox.Output{Text: f.fail, IsError: true}, nil
	}
	return toolbox.Output{Text: "turned off", Images: f.out}, nil
}
func (f *fakeTools) Errors() map[string]error { return nil }
func (f *fakeTools) Guidance(_ []toolbox.Tool, notes map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = notes
	return ""
}
func (f *fakeTools) called() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

var lightSchema = map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}

// setupApproval gives the model a write tool, turn_off, which it calls with
// {"name": "kitchen", "bogus": "x"} for every message.
func setupApproval(t *testing.T, confirm bool) (testBot, *fakeMsg, *fakeLLM, *fakeTools) {
	t.Helper()
	b, m, l, _ := setup(t)
	ft := &fakeTools{tools: []toolbox.Tool{
		{Name: "turn_off", Schema: lightSchema, Confirm: confirm},
		{Name: "get_state", Schema: lightSchema, ReadOnly: true},
	}}
	b.opt.Tools, b.opt.ApprovalTimeout = ft, time.Minute
	tc := &ollama.ToolCall{}
	tc.Function.Name = "turn_off"
	tc.Function.Arguments = map[string]any{"name": "kitchen", "bogus": "x"}
	l.caps, l.toolCall = []string{"tools"}, tc
	return b, m, l, ft
}

// lastTool returns what the model was told about the tool call.
func lastTool(l *fakeLLM) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.last) - 1; i >= 0; i-- {
		if l.last[i].Role == "tool" {
			return l.last[i].Content
		}
	}
	return ""
}

func TestApprovalYes(t *testing.T) {
	b, m, _, ft := setupApproval(t, true)
	b.Handle(dm(me, "turn off the kitchen light"))
	ask := reply(t, m)
	want := "Allow turn_off?\n• name: kitchen\n• left out, the tool doesn't take it: bogus: x\nReply yes to run it, anything else cancels (1 min)."
	if ask != want {
		t.Fatalf("prompt:\n%s\nwant\n%s", ask, want)
	}
	if ft.called() != 0 {
		t.Fatal("ran before approval")
	}

	b.Handle(dm(me, "/status")) // commands still work and aren't taken as the answer
	if r := reply(t, m); !strings.Contains(r, "waiting for your OK to run turn_off") {
		t.Fatalf("status: %q", r)
	}

	b.Handle(dm(me, "Yes!"))
	if r := reply(t, m); r != "echo: turned off" {
		t.Fatalf("after yes: %q", r)
	}
	if ft.called() != 1 {
		t.Fatalf("calls: %d", ft.called())
	}
	if b.awaiting(me) {
		t.Fatal("approval still pending")
	}
}

func TestApprovalDeclined(t *testing.T) {
	b, m, l, ft := setupApproval(t, true)
	b.Handle(dm(me, "turn off the kitchen light"))
	reply(t, m) // prompt
	b.Handle(dm(me, "no, the bedroom one"))
	reply(t, m) // model's answer
	if ft.called() != 0 {
		t.Fatal("ran after being declined")
	}
	if got := lastTool(l); !strings.Contains(got, "declined") || !strings.Contains(got, `"no, the bedroom one"`) {
		t.Fatalf("model told: %q", got)
	}
	// The answer was consumed: nothing left queued as a new message.
	expectSilence(t, m)
}

func TestApprovalTimeout(t *testing.T) {
	b, m, l, ft := setupApproval(t, true)
	b.opt.ApprovalTimeout = 50 * time.Millisecond
	b.Handle(dm(me, "turn off the kitchen light"))
	reply(t, m) // prompt
	reply(t, m) // model's answer
	if ft.called() != 0 {
		t.Fatal("ran without approval")
	}
	if got := lastTool(l); !strings.Contains(got, "didn't approve within 50ms") {
		t.Fatalf("model told: %q", got)
	}
}

func TestApprovalStop(t *testing.T) {
	b, m, _, ft := setupApproval(t, true)
	b.Handle(dm(me, "turn off the kitchen light"))
	reply(t, m) // prompt
	b.Handle(dm(me, "/stop"))
	if r := reply(t, m); r != "Stopped." {
		t.Fatalf("stop: %q", r)
	}
	expectSilence(t, m)
	if ft.called() != 0 || b.awaiting(me) {
		t.Fatal("tool ran or approval still pending after /stop")
	}
}

func TestNoApprovalWhenOff(t *testing.T) {
	b, m, _, ft := setupApproval(t, false) // MCP_ALLOW_WRITE=true
	b.Handle(dm(me, "turn off the kitchen light"))
	if r := reply(t, m); r != "echo: turned off" {
		t.Fatalf("got %q", r)
	}
	if ft.called() != 1 {
		t.Fatal("tool not run")
	}
}

func TestReadOnlyToolsNeedNoApproval(t *testing.T) {
	b, m, l, ft := setupApproval(t, true)
	l.toolCall.Function.Name = "get_state"
	b.Handle(dm(me, "is the kitchen light on?"))
	if r := reply(t, m); r != "echo: turned off" {
		t.Fatalf("got %q", r)
	}
	if ft.called() != 1 {
		t.Fatal("read-only tool not run")
	}
}

func TestApprovalWords(t *testing.T) {
	for _, s := range []string{"yes", "Y", "ok.", "Sure!", "go ahead", "👍"} {
		if !approved(s) {
			t.Errorf("%q should approve", s)
		}
	}
	for _, s := range []string{"no", "yes but the bedroom", "maybe", ""} {
		if approved(s) {
			t.Errorf("%q should not approve", s)
		}
	}
}

func TestApprovalPrompt(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"room": map[string]any{}, "name": map[string]any{}, "kind": map[string]any{}, "tags": map[string]any{}}}
	tools := []toolbox.Tool{{Name: "devices__switch_on", Schema: schema}}
	// Empty filters and a blank undeclared "floor", as small models often send.
	args := map[string]any{"room": "Office", "name": "Light", "kind": []any{}, "tags": []any{}, "floor": ""}
	want := "Allow switch_on?\n• name: Light\n• room: Office\nReply yes to run it, anything else cancels (2 min)."
	if got := approvalPrompt(tools, "devices__switch_on", args, "", 2*time.Minute); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	args = map[string]any{"name": "Light", "kind": []any{"lamp", "plug"}, "floor": "Upstairs"}
	want = "Allow switch_on?\n• kind: lamp, plug\n• name: Light\n• left out, the tool doesn't take it: floor: Upstairs\n" +
		"Reply yes to run it, anything else cancels (1m30s)."
	if got := approvalPrompt(tools, "devices__switch_on", args, "", 90*time.Second); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApprovalSwipeReply(t *testing.T) {
	b, m, _, ft := setupApproval(t, true)
	b.Handle(dm(me, "turn off the kitchen light"))
	ask := reply(t, m)
	env := dm(me, "Yes")
	env.Quote = &transport.Quote{FromBot: true, Text: ask}
	b.Handle(env)
	if r := reply(t, m); r != "echo: turned off" {
		t.Fatalf("after swipe-reply yes: %q", r)
	}
	if ft.called() != 1 {
		t.Fatalf("calls: %d", ft.called())
	}
}

func TestApprovalShowsLastFailure(t *testing.T) {
	tools := []toolbox.Tool{{Name: "switch_on", Schema: lightSchema}}
	got := approvalPrompt(tools, "switch_on", map[string]any{"name": "Office"}, "Error calling tool:   No entities\n matched 'Office'", time.Minute)
	want := "The last try failed: Error calling tool: No entities matched 'Office'\n\n" +
		"Allow switch_on?\n• name: Office\nReply yes to run it, anything else cancels (1 min)."
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApprovalRetryShowsError(t *testing.T) {
	b, m, l, ft := setupApproval(t, true)
	ft.fail, l.retry = "No entities matched 'kitchen'", true
	b.Handle(dm(me, "turn off the kitchen light"))
	if r := reply(t, m); strings.Contains(r, "last try failed") {
		t.Fatalf("first prompt shows a failure: %q", r)
	}
	b.Handle(dm(me, "yes"))
	// The fake model calls the tool again after the failure.
	if r := reply(t, m); !strings.HasPrefix(r, "The last try failed: No entities matched 'kitchen'\n\nAllow turn_off?") {
		t.Fatalf("retry prompt: %q", r)
	}
}

func TestUnknownToolNotOfferedForApproval(t *testing.T) {
	b, m, l, ft := setupApproval(t, true)
	l.toolCall.Function.Name = "media__unpause" // not a real tool
	b.Handle(dm(me, "unpause the speakers"))
	r := reply(t, m)
	if strings.Contains(r, "Allow") {
		t.Fatalf("asked to approve a tool that doesn't exist: %q", r)
	}
	if ft.called() != 0 {
		t.Fatal("called a tool that doesn't exist")
	}
	want := `Tool failed: there is no tool named "media__unpause", so nothing was run. Use one of these exact names: turn_off, get_state.`
	if got := lastTool(l); got != want {
		t.Fatalf("model told:\n%s\nwant:\n%s", got, want)
	}
}

func TestTimeoutEndsToolUse(t *testing.T) {
	b, m, l, ft := setupApproval(t, true)
	ft.fail, l.retry = "failed", true
	b.opt.ApprovalTimeout = 30 * time.Millisecond
	b.Handle(dm(me, "turn off the kitchen light"))
	reply(t, m) // prompt
	if r := reply(t, m); strings.HasPrefix(r, "Allow") {
		t.Fatalf("asked again after a timeout: %q", r)
	}
}

func TestTwoDeclinesEndToolUse(t *testing.T) {
	b, m, l, ft := setupApproval(t, true)
	l.retry = true
	ft.fail = "failed" // so the model keeps retrying
	b.Handle(dm(me, "turn off the kitchen light"))
	reply(t, m) // prompt 1
	b.Handle(dm(me, "yes"))
	reply(t, m) // prompt 2, after the failure
	b.Handle(dm(me, "no"))
	reply(t, m) // prompt 3
	b.Handle(dm(me, "no"))
	if r := reply(t, m); strings.HasPrefix(r, "Allow") || strings.HasPrefix(r, "The last try") {
		t.Fatalf("asked again after two declines: %q", r)
	}
	expectSilence(t, m)
}

func TestSecondMessageDuringApprovalIsKept(t *testing.T) {
	b, _, _, _ := setup(t)
	ch := make(chan string, 1)
	ch <- "no" // answered, but approve hasn't picked it up yet
	b.mu.Lock()
	b.approvals[me] = ch
	b.mu.Unlock()
	if b.answer(me, "turn on the bedroom one instead") {
		t.Fatal("second message was swallowed by the approval")
	}
}

func TestSnippetKeepsRunesWhole(t *testing.T) {
	s := snippet("é"+strings.Repeat("é", 10), 5) // 2 bytes each: byte 5 is mid-rune
	if !utf8.ValidString(s) || s != "éé…" {
		t.Fatalf("got %q", s)
	}
}
