package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tfielding/llamero/internal/ollama"
	"github.com/tfielding/llamero/internal/store"
	"github.com/tfielding/llamero/internal/transport"
)

const (
	me    = "+15550000001" // allowed user
	other = "+15550000002" // someone not on the allowlist
)

type fakeMsg struct {
	mu        sync.Mutex
	styled    []bool
	sent      []string
	typing    []bool
	ch        chan string
	reactions []string
	files     map[string][]byte // received attachments by id
	attached  []string          // "type name" of every attachment sent
}

func (f *fakeMsg) Attachment(_ context.Context, id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if data, ok := f.files[id]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("attachment %s: 404 Not Found", id)
}

func (f *fakeMsg) Send(_ context.Context, to, text string, styled bool, att []transport.Attachment) error {
	f.mu.Lock()
	for _, a := range att {
		f.attached = append(f.attached, a.MIME+" "+a.Name)
	}
	f.sent = append(f.sent, to+": "+text)
	f.styled = append(f.styled, styled)
	f.mu.Unlock()
	f.ch <- text
	return nil
}
func (f *fakeMsg) React(_ context.Context, to string, ts int64, emoji string, remove bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op := "+"
	if remove {
		op = "-"
	}
	f.reactions = append(f.reactions, fmt.Sprintf("%s%s %d", op, emoji, ts))
	return nil
}
func (f *fakeMsg) Typing(_ context.Context, _ string, on bool) error {
	f.mu.Lock()
	f.typing = append(f.typing, on)
	f.mu.Unlock()
	return nil
}

type fakeLLM struct {
	mu       sync.Mutex
	last     []ollama.Message
	model    string
	think    any
	calls    int
	gate     chan struct{} // if set, Chat blocks until it's closed
	pingErr  error
	chatErr  error
	failOn   string           // if set, Chat fails when any message contains it
	caps     []string         // capabilities reported for models without other metadata
	toolCall *ollama.ToolCall // if set, the model calls this tool when tools are offered
	retry    bool             // and calls it again after any tool result (an error or a decline)
	answer   string           // if set, the final answer instead of an echo
	offered  []string         // tool names offered in the last call
	blanks   int              // if set, this many answers (not tool calls) come back empty
}

func (f *fakeLLM) Chat(ctx context.Context, model string, msgs []ollama.Message, think any, tools []ollama.Tool) (ollama.Result, error) {
	f.mu.Lock()
	f.last, f.model, f.think = msgs, model, think
	f.calls++
	f.offered = nil
	for _, t := range tools {
		f.offered = append(f.offered, t.Function.Name)
	}
	answer := f.answer
	gate, chatErr := f.gate, f.chatErr
	for _, m := range msgs {
		if f.failOn != "" && strings.Contains(m.Content, f.failOn) {
			chatErr = errors.New("failed on purpose")
		}
	}
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ollama.Result{}, ctx.Err()
		}
	}
	if chatErr != nil {
		return ollama.Result{}, chatErr
	}
	last := msgs[len(msgs)-1]
	if f.toolCall != nil && len(tools) > 0 && (last.Role == "user" || (f.retry && last.Role == "tool")) {
		return ollama.Result{ToolCalls: []ollama.ToolCall{*f.toolCall}}, nil
	}
	content := "echo: " + msgs[len(msgs)-1].Content
	if answer != "" {
		content = answer
	}
	f.mu.Lock()
	if f.blanks > 0 {
		f.blanks--
		content = ""
	}
	f.mu.Unlock()
	return ollama.Result{
		Content: content,
		Stats:   ollama.Stats{EvalCount: 100, EvalDuration: 2 * time.Second, PromptEvalCount: 50, PromptEvalDuration: 500 * time.Millisecond, TotalDuration: 3 * time.Second},
	}, nil
}
func (f *fakeLLM) Models(context.Context) ([]string, error) {
	return []string{"llama3.2:latest", "qwen2.5:7b", "gpt-oss:20b"}, nil
}
func (f *fakeLLM) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}
func (f *fakeLLM) Info(_ context.Context, model string) (ollama.ModelInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pingErr != nil {
		return ollama.ModelInfo{}, f.pingErr
	}
	switch model {
	case "qwen2.5:7b":
		return ollama.ModelInfo{Think: ollama.ThinkInfo{Known: true, Values: []string{"on", "off"}, Default: "on"}}, nil
	case "gpt-oss:20b":
		return ollama.ModelInfo{Think: ollama.ThinkInfo{Known: true, Values: []string{"low", "medium", "high"}, Default: "medium"}}, nil
	}
	return ollama.ModelInfo{Capabilities: f.caps}, nil // no thinking metadata
}

// testBot binds a Bot to the test's context so tests can call Handle(env).
type testBot struct {
	*Bot
	ctx context.Context
}

func (t testBot) Handle(in transport.Message) { t.Bot.Handle(t.ctx, in) }

func setup(t *testing.T) (testBot, *fakeMsg, *fakeLLM, *store.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeMsg{ch: make(chan string, 10)}
	l := &fakeLLM{}
	b := New(m, l, st, Options{
		Allowed: map[string]bool{me: true}, DefaultModel: "llama3.2:latest",
		SystemPrompt: "sys", HistoryLimit: 4, TypingRefresh: 5 * time.Millisecond,
	})
	t.Cleanup(func() { cancel(); b.Wait() })
	return testBot{b, ctx}, m, l, st
}

func dm(from, text string) transport.Message {
	return transport.Message{Sender: from, Text: text}
}

func reply(t *testing.T, m *fakeMsg) string {
	t.Helper()
	select {
	case r := <-m.ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
		return ""
	}
}

func expectSilence(t *testing.T, m *fakeMsg) {
	t.Helper()
	select {
	case r := <-m.ch:
		t.Fatalf("unexpected reply %q", r)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBoundaries(t *testing.T) {
	b, m, _, _ := setup(t)

	// Group chats, the bot's own messages and receipts are the transport's to drop (see the
	// signal package); the core keeps out senders who aren't allowed, and empty messages.
	b.Handle(dm(other, "hi from someone else"))
	b.Handle(dm("", "no sender"))
	b.Handle(dm(me, "   "))
	expectSilence(t, m)

	b.Handle(dm(me, "hello"))
	if r := reply(t, m); r != "echo: hello" {
		t.Fatalf("got %q", r)
	}
	if !strings.HasPrefix(m.sent[0], me+": ") {
		t.Fatalf("replied to wrong recipient: %q", m.sent[0])
	}
}

func TestAllowByUUID(t *testing.T) {
	b, m, _, _ := setup(t)
	b.opt.Allowed["uuid-123"] = true
	env := dm(other, "hi")
	env.AltIDs = []string{"uuid-123"}
	b.Handle(env)
	if r := reply(t, m); r != "echo: hi" {
		t.Fatalf("got %q", r)
	}
}

func TestHistoryPersistsAndTrims(t *testing.T) {
	b, m, l, st := setup(t)
	for _, s := range []string{"one", "two", "three"} {
		b.Handle(dm(me, s))
		reply(t, m)
	}
	// system + last 4 history msgs (two, echo two... trimmed to limit 4) + new user msg
	l.mu.Lock()
	got := l.last
	l.mu.Unlock()
	if got[0].Role != "system" || got[1].Content != "one" || got[len(got)-1].Content != "three" {
		t.Fatalf("unexpected prompt: %+v", got)
	}
	chat, _ := st.Load(me)
	if len(chat.History) != 4 || chat.History[0].Content != "two" || chat.History[0].Role != "user" {
		t.Fatalf("history not trimmed: %+v", chat.History)
	}

	b.Handle(dm(me, "/reset"))
	if r := reply(t, m); r != "History cleared." {
		t.Fatalf("got %q", r)
	}
	chat, _ = st.Load(me)
	if len(chat.History) != 0 {
		t.Fatal("history not cleared")
	}
}

func TestModelCommands(t *testing.T) {
	b, m, l, st := setup(t)

	b.Handle(dm(me, "/models"))
	if r := reply(t, m); !strings.Contains(r, "* llama3.2:latest") || !strings.Contains(r, "qwen2.5:7b") {
		t.Fatalf("list: %q", r)
	}
	b.Handle(dm(me, "/models nope"))
	if r := reply(t, m); !strings.Contains(r, "No model") {
		t.Fatalf("bad model: %q", r)
	}
	b.Handle(dm(me, "/models qwen2.5:7b"))
	if r := reply(t, m); r != "Switched to qwen2.5:7b." {
		t.Fatalf("switch: %q", r)
	}
	b.Handle(dm(me, "hi"))
	reply(t, m)
	if l.model != "qwen2.5:7b" {
		t.Fatalf("used model %q", l.model)
	}
	if c, _ := st.Load(me); c.Model != "qwen2.5:7b" {
		t.Fatal("model not persisted")
	}
	b.Handle(dm(me, "/model llama3.2")) // old name still works; without tag
	if r := reply(t, m); r != "Switched to llama3.2:latest." {
		t.Fatalf("tagless: %q", r)
	}
	b.Handle(dm(me, "/status"))
	if r := reply(t, m); !strings.Contains(r, "llama3.2:latest") {
		t.Fatalf("status: %q", r)
	}
}

func TestTypingIndicator(t *testing.T) {
	b, m, _, _ := setup(t)
	b.Handle(dm(me, "hello"))
	reply(t, m)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.typing) < 2 || !m.typing[0] || m.typing[len(m.typing)-1] {
		t.Fatalf("typing sequence: %v", m.typing)
	}
}

func TestStatusAnswersMidReply(t *testing.T) {
	b, m, l, _ := setup(t)
	b.Handle(dm(me, "/status"))
	if r := reply(t, m); !strings.Contains(r, "Now: idle") {
		t.Fatalf("idle status: %q", r)
	}

	gate := make(chan struct{})
	l.mu.Lock()
	l.gate = gate
	l.mu.Unlock()
	b.Handle(dm(me, "slow question"))
	b.Handle(dm(me, "second question"))
	// wait until the model call has started
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.mu.Lock()
		_, busy := b.active[me]
		b.mu.Unlock()
		if busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	b.Handle(dm(me, "/status"))
	r := reply(t, m) // must arrive while the model is still blocked
	if !strings.Contains(r, "Now: llama3.2:latest thinking for") || !strings.Contains(r, "1 message(s) waiting") {
		t.Fatalf("busy status: %q", r)
	}
	close(gate)
	if r := reply(t, m); r != "echo: slow question" {
		t.Fatalf("got %q", r)
	}
	reply(t, m)
}

func TestStats(t *testing.T) {
	b, m, _, _ := setup(t)
	b.Handle(dm(me, "/stats"))
	if r := reply(t, m); r != "No replies yet." {
		t.Fatalf("got %q", r)
	}
	b.Handle(dm(me, "hello"))
	reply(t, m)
	b.Handle(dm(me, "/stats"))
	r := reply(t, m)
	for _, want := range []string{"Last reply: llama3.2:latest", "Output: 100 tokens at 50.0 tok/s (2s)", "Prompt: 50 tokens at 100 tok/s", "Total: 3s"} {
		if !strings.Contains(r, want) {
			t.Fatalf("missing %q in %q", want, r)
		}
	}
}

func TestModelRepliesStyledCommandsPlain(t *testing.T) {
	b, m, _, _ := setup(t)
	b.Handle(dm(me, "## Title"))
	if r := reply(t, m); r != "echo: ## Title" { // fake echoes; "##" is mid-line so not a heading
		t.Fatalf("got %q", r)
	}
	b.Handle(dm(me, "/help"))
	reply(t, m)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.styled[0] || m.styled[1] {
		t.Fatalf("styled flags: %v", m.styled)
	}
}

var refused = &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}

func TestOllamaOffline(t *testing.T) {
	b, m, l, st := setup(t)
	l.pingErr = refused
	b.Handle(dm(me, "hello"))
	if r := reply(t, m); r != offlineText {
		t.Fatalf("got %q", r)
	}
	if l.calls != 0 {
		t.Fatal("should not call the model when offline")
	}
	if c, _ := st.Load(me); len(c.History) != 0 {
		t.Fatal("offline message should not be saved")
	}
	b.Handle(dm(me, "/status"))
	if r := reply(t, m); !strings.Contains(r, "Ollama: offline (connect: connection refused)") {
		t.Fatalf("status: %q", r)
	}

	// Goes away mid-reply.
	l.mu.Lock()
	l.pingErr, l.chatErr = nil, refused
	l.mu.Unlock()
	b.Handle(dm(me, "hello"))
	if r := reply(t, m); r != offlineText {
		t.Fatalf("mid-reply: %q", r)
	}
}

func TestThink(t *testing.T) {
	b, m, l, st := setup(t)
	send := func(text string) string { b.Handle(dm(me, text)); return reply(t, m) }

	// Default model has no thinking metadata: generic options.
	if r := send("/think"); !strings.Contains(r, "Thinking: model default") || !strings.Contains(r, "on, off, default") {
		t.Fatalf("think (unknown): %q", r)
	}
	send("/models qwen2.5:7b")
	if r := send("/think"); !strings.Contains(r, "model default (on)") || !strings.Contains(r, "Options for qwen2.5:7b: on, off, default") {
		t.Fatalf("think (qwen): %q", r)
	}
	if r := send("/think off"); r != "Thinking: off." {
		t.Fatalf("set off: %q", r)
	}
	send("hi")
	if l.think != false {
		t.Fatalf("think sent = %#v", l.think)
	}
	if c, _ := st.Load(me); c.Think != "off" {
		t.Fatal("think not persisted")
	}

	// gpt-oss only has levels: switching warns, and the unsupported setting isn't sent.
	if r := send("/models gpt-oss:20b"); !strings.Contains(r, `doesn't support thinking "off"`) {
		t.Fatalf("switch warning: %q", r)
	}
	send("hi")
	if l.think != nil {
		t.Fatalf("unsupported setting sent: %#v", l.think)
	}
	if r := send("/think off"); !strings.Contains(r, `doesn't support "off". Options: low, medium, high, default`) {
		t.Fatalf("reject off: %q", r)
	}
	if r := send("/think low"); r != "Thinking: low." {
		t.Fatalf("set low: %q", r)
	}
	send("hi")
	if l.think != "low" {
		t.Fatalf("think sent = %#v", l.think)
	}
	if r := send("/status"); !strings.Contains(r, "Thinking: low") || !strings.Contains(r, "Ollama: online") {
		t.Fatalf("status: %q", r)
	}
	if r := send("/think default"); r != "Thinking: model default (medium)." {
		t.Fatalf("default: %q", r)
	}
	send("hi")
	if l.think != nil {
		t.Fatalf("default sent %#v", l.think)
	}
}

func TestStop(t *testing.T) {
	b, m, l, st := setup(t)
	b.Handle(dm(me, "/stop"))
	if r := reply(t, m); r != "Nothing is running." {
		t.Fatalf("idle: %q", r)
	}

	gate := make(chan struct{})
	l.mu.Lock()
	l.gate = gate
	l.mu.Unlock()
	b.Handle(dm(me, "long question"))
	b.Handle(dm(me, "queued question"))
	waitBusy(t, b, me)

	b.Handle(dm(me, "/stop"))
	if r := reply(t, m); r != "Stopped, and dropped 1 waiting message(s)." {
		t.Fatalf("stop: %q", r)
	}
	expectSilence(t, m) // no reply for the stopped or dropped message
	if c, _ := st.Load(me); len(c.History) != 0 {
		t.Fatalf("stopped exchange was saved: %+v", c.History)
	}

	// The bot keeps working afterwards.
	l.mu.Lock()
	l.gate = nil
	l.mu.Unlock()
	b.Handle(dm(me, "hello again"))
	if r := reply(t, m); r != "echo: hello again" {
		t.Fatalf("after stop: %q", r)
	}
	close(gate)
}

func waitBusy(t *testing.T, b testBot, sender string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		_, busy := b.active[sender]
		b.mu.Unlock()
		if busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("model call never started")
}

func TestSummary(t *testing.T) {
	b, m, l, st := setup(t)
	b.opt.Summarize = true // HistoryLimit is 4: summarize past 4 messages, keep the newest 2

	for _, s := range []string{"one", "two"} {
		b.Handle(dm(me, s))
		reply(t, m)
	}
	b.Handle(dm(me, "/summary"))
	if r := reply(t, m); !strings.HasPrefix(r, "No summary yet") {
		t.Fatalf("empty summary: %q", r)
	}

	b.Handle(dm(me, "three")) // 6 messages: one and two are summarized after the reply
	reply(t, m)
	chat := waitSummary(t, st)
	b.Handle(dm(me, "/status"))
	if r := reply(t, m); !strings.Contains(r, "Memory: 2 messages (max 4) + summary") {
		t.Fatalf("status: %q", r)
	}
	if len(chat.History) != 2 || chat.History[0].Content != "three" {
		t.Fatalf("history after summary: %+v", chat.History)
	}
	// The fake model echoes the prompt, so the summary shows what it was given.
	for _, want := range []string{"Existing summary:\n(none yet)", "User: one", "Assistant: echo: two"} {
		if !strings.Contains(chat.Summary, want) {
			t.Fatalf("summary missing %q: %q", want, chat.Summary)
		}
	}
	if strings.Contains(chat.Summary, "three") {
		t.Fatalf("summarized kept messages: %q", chat.Summary)
	}

	b.Handle(dm(me, "four"))
	reply(t, m)
	l.mu.Lock()
	got := l.last
	l.mu.Unlock()
	if got[1].Role != "system" || !strings.Contains(got[1].Content, "User: one") || got[2].Content != "three" {
		t.Fatalf("summary not in prompt: %+v", got)
	}

	b.Handle(dm(me, "/summary"))
	if r := reply(t, m); !strings.Contains(r, "User: one") {
		t.Fatalf("/summary: %q", r)
	}
	b.Handle(dm(me, "/reset"))
	reply(t, m)
	if c, _ := st.Load(me); c.Summary != "" || len(c.History) != 0 {
		t.Fatalf("reset kept memory: %+v", c)
	}
}

func TestSummaryFailureKeepsMessages(t *testing.T) {
	b, m, l, st := setup(t)
	b.opt.Summarize = true
	l.failOn = "Existing summary:"

	for _, s := range []string{"one", "two", "three", "four", "five"} {
		b.Handle(dm(me, s))
		if r := reply(t, m); r != "echo: "+s {
			t.Fatalf("got %q", r)
		}
	}
	chat, _ := st.Load(me)
	// Nothing summarized, so nothing dropped until twice the limit.
	if chat.Summary != "" || len(chat.History) != 8 || chat.History[0].Content != "two" {
		t.Fatalf("after failed summaries: summary %q, history %+v", chat.Summary, chat.History)
	}
}

func waitSummary(t *testing.T, st *store.Store) store.Chat {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, _ := st.Load(me); c.Summary != "" {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no summary saved")
	return store.Chat{}
}

func TestSplitPoint(t *testing.T) {
	h := []ollama.Message{{Role: "user"}, {Role: "assistant"}, {Role: "user"}, {Role: "assistant"},
		{Role: "user"}, {Role: "assistant"}}
	for _, c := range []struct{ n, limit, want int }{
		{4, 4, 0}, // not over the limit
		{6, 4, 4}, // keep the newest 2
		{6, 2, 4}, // keeping 1 would start with a reply, so keep the whole last exchange
		{5, 4, 2}, // kept part starts with a user message
	} {
		if got := splitPoint(h[:c.n], c.limit); got != c.want {
			t.Errorf("splitPoint(%d msgs, limit %d) = %d, want %d", c.n, c.limit, got, c.want)
		}
	}
}

func TestSettings(t *testing.T) {
	b, m, _, _ := setup(t)
	b.Handle(dm(me, "/settings"))
	if r := reply(t, m); r != "No settings to show." {
		t.Fatalf("empty: %q", r)
	}
	b.opt.Settings = "HISTORY_LIMIT: 4"
	b.Handle(dm(me, "/settings"))
	if r := reply(t, m); r != "Settings:\nHISTORY_LIMIT: 4" {
		t.Fatalf("settings: %q", r)
	}
}

// setupWake makes Ollama start offline and gives the bot a Wake that counts calls.
// If wakes is true, a wake-up brings Ollama online.
func setupWake(t *testing.T, wakes bool) (testBot, *fakeMsg, *fakeLLM, *int) {
	t.Helper()
	b, m, l, _ := setup(t)
	l.pingErr = refused
	sent := new(int)
	b.opt.Wake = func(context.Context) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		*sent++
		if wakes {
			l.pingErr = nil
		}
		return nil
	}
	b.opt.WakeWait, b.opt.WakePoll = 300*time.Millisecond, 5*time.Millisecond
	return b, m, l, sent
}

func TestWake(t *testing.T) {
	b, m, _, sent := setupWake(t, true)
	b.Handle(dm(me, "hello"))
	if r := reply(t, m); r != "Ollama is asleep, waking it up…" {
		t.Fatalf("notice: %q", r)
	}
	if r := reply(t, m); r != "echo: hello" {
		t.Fatalf("after wake: %q", r)
	}
	if *sent != 1 {
		t.Fatalf("wake sent %d times", *sent)
	}

	b.Handle(dm(me, "/wake"))
	if r := reply(t, m); r != "Ollama is already awake." {
		t.Fatalf("/wake when awake: %q", r)
	}
}

func TestWakeTimesOut(t *testing.T) {
	b, m, l, _ := setupWake(t, false)
	b.Handle(dm(me, "hello"))
	reply(t, m) // notice
	if r := reply(t, m); r != wakeFailedText {
		t.Fatalf("got %q", r)
	}
	if l.calls != 0 {
		t.Fatal("should not call the model when it never woke")
	}

	b.Handle(dm(me, "/wake"))
	if r := reply(t, m); !strings.HasPrefix(r, "Wake-up sent.") {
		t.Fatalf("/wake: %q", r)
	}
}

func TestWakeStatusAndStop(t *testing.T) {
	b, m, _, _ := setupWake(t, false)
	b.opt.WakeWait = time.Minute
	b.Handle(dm(me, "hello"))
	reply(t, m) // notice
	waitBusy(t, b, me)

	b.Handle(dm(me, "/status"))
	if r := reply(t, m); !strings.Contains(r, "Now: Ollama waking up for") {
		t.Fatalf("status: %q", r)
	}
	b.Handle(dm(me, "/stop"))
	if r := reply(t, m); r != "Stopped." {
		t.Fatalf("stop: %q", r)
	}
	expectSilence(t, m) // no "didn't wake" reply after /stop
}

func TestWakeNotConfigured(t *testing.T) {
	b, m, _, _ := setup(t)
	b.Handle(dm(me, "/wake"))
	if r := reply(t, m); !strings.Contains(r, "isn't set up") {
		t.Fatalf("got %q", r)
	}
}

func TestReplyContext(t *testing.T) {
	b, m, l, st := setup(t)
	lastUser := func() string {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.last[len(l.last)-1].Content
	}
	send := func(text string, q *transport.Quote) string {
		env := dm(me, text)
		env.Quote = q
		b.Handle(env)
		return reply(t, m)
	}

	send("explain the second part", &transport.Quote{FromBot: true, Text: "First part.\nSecond part."})
	want := "(The user is replying to this message the bot sent earlier, either your reply or the output of a command:\n" +
		"> First part.\n> Second part.)\n\nexplain the second part"
	if got := lastUser(); got != want {
		t.Fatalf("bot quote:\n%q\nwant\n%q", got, want)
	}
	if c, _ := st.Load(me); c.History[0].Content != want {
		t.Fatalf("saved without quote: %q", c.History[0].Content)
	}

	send("and this?", &transport.Quote{Text: "my question"})
	if got := lastUser(); !strings.HasPrefix(got, "(The user is replying to their own earlier message:\n> my question)") {
		t.Fatalf("own quote: %q", got)
	}

	send("hi", &transport.Quote{FromBot: true, Text: strings.Repeat("x", 1500)})
	if got := lastUser(); !strings.Contains(got, strings.Repeat("x", 1000)+"…)") || strings.Contains(got, strings.Repeat("x", 1001)) {
		t.Fatal("long quote not cut at 1000 characters")
	}

	send("plain", &transport.Quote{FromBot: true, Text: "  "}) // quote without text (e.g. an image)
	if got := lastUser(); got != "plain" {
		t.Fatalf("empty quote: %q", got)
	}

	if r := send("/stats", &transport.Quote{FromBot: true, Text: "something"}); !strings.HasPrefix(r, "Last reply:") {
		t.Fatalf("command with quote: %q", r)
	}
}

func TestReactions(t *testing.T) {
	b, m, _, _ := setup(t)
	b.opt.Reaction = "👀"
	reactions := func() []string {
		m.mu.Lock()
		defer m.mu.Unlock()
		return append([]string(nil), m.reactions...)
	}
	waitReactions := func(n int) []string {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if r := reactions(); len(r) >= n {
				return r
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("reactions: %v, want %d", reactions(), n)
		return nil
	}
	send := func(text string, ts int64) {
		env := dm(me, text)
		env.ID = ts
		b.Handle(env)
		reply(t, m)
	}

	send("hello", 1001)
	if r := waitReactions(2); strings.Join(r, ",") != "+👀 1001,-👀 1001" {
		t.Fatalf("chat: %v", r)
	}
	send("/status", 1002) // commands answer right away: no reaction
	send("/retry", 1003)  // runs the model: reaction
	if r := waitReactions(4); strings.Join(r[2:], ",") != "+👀 1003,-👀 1003" {
		t.Fatalf("retry: %v", r)
	}
	send("no timestamp", 0)
	expectSilence(t, m)
	if r := reactions(); len(r) != 4 {
		t.Fatalf("reacted without a timestamp: %v", r)
	}
}

func TestRetry(t *testing.T) {
	b, m, l, st := setup(t)
	b.Handle(dm(me, "/retry"))
	if r := reply(t, m); r != "Nothing to retry yet." {
		t.Fatalf("empty: %q", r)
	}

	b.Handle(dm(me, "hi"))
	reply(t, m)
	b.Handle(dm(me, "/retry"))
	if r := reply(t, m); r != "echo: hi" {
		t.Fatalf("retry: %q", r)
	}
	l.mu.Lock()
	calls, prompt := l.calls, l.last
	l.mu.Unlock()
	// The old exchange is not in the prompt, only the question again.
	if calls != 2 || len(prompt) != 2 || prompt[1].Content != "hi" {
		t.Fatalf("calls %d, prompt %+v", calls, prompt)
	}
	if c, _ := st.Load(me); len(c.History) != 2 || c.History[0].Content != "hi" {
		t.Fatalf("history: %+v", c.History)
	}

	// A failed retry keeps the old answer.
	l.mu.Lock()
	l.chatErr = errors.New("boom")
	l.mu.Unlock()
	b.Handle(dm(me, "/retry"))
	if r := reply(t, m); !strings.HasPrefix(r, "Model error") {
		t.Fatalf("failed retry: %q", r)
	}
	if c, _ := st.Load(me); len(c.History) != 2 || c.History[1].Content != "echo: hi" {
		t.Fatalf("history after failed retry: %+v", c.History)
	}
}

func TestRetryAfterFailure(t *testing.T) {
	b, m, l, st := setup(t)
	b.Handle(dm(me, "first"))
	reply(t, m)
	l.mu.Lock()
	l.chatErr = errors.New("boom")
	l.mu.Unlock()
	b.Handle(dm(me, "second"))
	if r := reply(t, m); !strings.HasPrefix(r, "Model error") {
		t.Fatalf("got %q", r)
	}
	l.mu.Lock()
	l.chatErr = nil
	l.mu.Unlock()
	b.Handle(dm(me, "/retry"))
	if r := reply(t, m); r != "echo: second" {
		t.Fatalf("retried the wrong message: %q", r)
	}
	c, _ := st.Load(me)
	if c.Unanswered != "" || len(c.History) != 4 || c.History[0].Content != "first" || c.History[2].Content != "second" {
		t.Fatalf("history: %+v, unanswered %q", c.History, c.Unanswered)
	}
}

func TestWakeSendFails(t *testing.T) {
	b, m, l, _ := setup(t)
	l.pingErr = refused
	b.opt.Wake = func(context.Context) error { return errors.New("network unreachable") }
	b.Handle(dm(me, "hello"))
	if r := reply(t, m); r != offlineText {
		t.Fatalf("got %q", r)
	}
}

func TestImageNotesStayOutOfReplies(t *testing.T) {
	history := []ollama.Message{
		{Role: "user", Content: "show me the driveway"},
		{Role: "assistant", Content: "Here it is.\n\n[2 image(s) were sent to the user with this reply.]"},
		{Role: "user", Content: "thanks"},
		{Role: "assistant", Content: "You're welcome."},
	}
	got := forPrompt(history)
	if len(got) != 5 || got[1].Content != "Here it is." || got[2].Role != "system" ||
		got[2].Content != "(That reply also sent 2 image(s) to the user.)" || got[4].Content != "You're welcome." {
		t.Fatalf("%+v", got)
	}
	if history[1].Content != "Here it is.\n\n[2 image(s) were sent to the user with this reply.]" {
		t.Error("saved history was changed")
	}
	for in, want := range map[string]string{
		"Here's the grid.\n\n[1 image(s) were sent to the user with this reply.]":                          "Here's the grid.",
		"[1 image(s) returned. They will be sent to the user with your reply; you cannot see them.] Done.": "Done.",
		"Two cars today. [2 images were sent to the user.]":                                                "Two cars today.",
		"Use [brackets] normally.": "Use [brackets] normally.",
	} {
		if got := stripImageNotes(in); got != want {
			t.Errorf("stripImageNotes(%q) = %q, want %q", in, got, want)
		}
	}
}
