// Package core is the assistant itself: it answers messages from a transport with Ollama, runs
// tools, hooks and schedules, and handles chat commands. It knows nothing about any one messenger.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/tfielding/llamero/internal/knowledge"
	"github.com/tfielding/llamero/internal/ollama"
	"github.com/tfielding/llamero/internal/schedule"
	"github.com/tfielding/llamero/internal/store"
	"github.com/tfielding/llamero/internal/toolbox"
	"github.com/tfielding/llamero/internal/transport"
)

// Messenger is the messaging app's side: a transport (see package transport).
type Messenger = transport.Messenger

// LLM is the model side (implemented by ollama.Client).
type LLM interface {
	Chat(ctx context.Context, model string, msgs []ollama.Message, think any, tools []ollama.Tool) (ollama.Result, error)
	Models(ctx context.Context) ([]string, error)
	Ping(ctx context.Context) error
	Load(ctx context.Context, model string) error
	Info(ctx context.Context, model string) (ollama.ModelInfo, error)
}

// Tools are the MCP tools the model may use (implemented by toolbox.Box).
type Tools interface {
	Tools(ctx context.Context) []toolbox.Tool
	Call(ctx context.Context, name string, args map[string]any) (toolbox.Output, error)
	Errors() map[string]error
	Guidance(tools []toolbox.Tool, notes map[string]string) string
}

type Options struct {
	Tools           Tools           // nil = no tools
	MaxToolRounds   int             // rounds of tool calls per message before the model must answer; 0 = DefaultMaxToolRounds
	Knowledge       *knowledge.Base // notes from the knowledge folder; nil = none
	Schedules       *schedule.Store // scheduled checks; nil = off
	Hooks           map[string]Hook // hooks by name; nil = none
	Location        *time.Location  // time zone for schedules; nil = time.Local
	ScheduleTick    time.Duration   // how often to look for due schedules (default 15s)
	Allowed         map[string]bool
	DefaultModel    string
	VisionModel     string // answers messages with photos when the chat's model can't see; empty = none
	SystemPrompt    string
	HistoryLimit    int
	Summarize       bool                        // fold messages past HistoryLimit into a running summary instead of dropping them
	SummaryModel    string                      // model that writes summaries; empty = the chat's model
	Settings        string                      // shown by /settings, with secrets already hidden
	Wake            func(context.Context) error // sends Wake-on-LAN to the Ollama machine; nil = off
	WakeWait        time.Duration               // how long to wait for Ollama after a wake-up
	WakePoll        time.Duration               // how often to check while waiting
	ApprovalTimeout time.Duration               // how long to wait for that answer
	Reaction        string                      // emoji shown on a message while it's being answered; empty = off
	TypingRefresh   time.Duration               // how often the typing indicator is renewed (Signal clients hide it after ~15s)
	HookStateFile   string                      // where snoozes and missed hook runs are saved; empty = memory only
}

type Bot struct {
	msg   Messenger
	llm   LLM
	store *store.Store
	opt   Options

	mu        sync.Mutex
	queues    map[string]chan message // one worker per sender keeps replies in order
	scheduled map[string]bool         // scheduled runs waiting or running, by "chat#id"
	boot      time.Time               // when the bot started
	approvals map[string]chan string  // sender's answer to a pending tool approval
	active    map[string]activity     // model call currently running, per sender
	workers   sync.WaitGroup

	hooks hookState // hook runs, cooldowns, snoozes and what happened to them; its own lock
}

// message is an incoming message waiting to be answered, or a scheduled check to run.
type message struct {
	text        string
	attachments []transport.File // photos and files sent with it
	ts          int64            // identifies the message for reactions; 0 = unknown
	schedule    int              // if set, run this schedule instead of answering text
	hook        *hookRun         // if set, handle these hook events instead
}

type activity struct {
	model   string
	started time.Time
	tool    string             // tool currently running, if any
	task    string             // background work instead of a reply, e.g. "updating memory"
	cancel  context.CancelFunc // stops this model call (/stop)
}

func New(m Messenger, l LLM, s *store.Store, opt Options) *Bot {
	if opt.TypingRefresh == 0 {
		opt.TypingRefresh = 10 * time.Second
	}
	if opt.WakeWait == 0 {
		opt.WakeWait = time.Minute
	}
	if opt.WakePoll == 0 {
		opt.WakePoll = 2 * time.Second
	}
	if opt.ApprovalTimeout == 0 {
		opt.ApprovalTimeout = 2 * time.Minute
	}
	if opt.MaxToolRounds <= 0 {
		opt.MaxToolRounds = DefaultMaxToolRounds
	}
	b := &Bot{msg: m, llm: l, store: s, opt: opt,
		queues: map[string]chan message{}, approvals: map[string]chan string{}, active: map[string]activity{},
		scheduled: map[string]bool{}, boot: time.Now(), hooks: newHookState()}
	b.loadHookState()
	return b
}

// Handle is called for every message from the transport. It never blocks on the model.
// ctx governs the whole bot: when it is cancelled, workers stop.
func (b *Bot) Handle(ctx context.Context, in transport.Message) {
	if !b.allowed(in) {
		slog.Info("ignoring message from sender not on allowlist", "from", allowlistID(in.Sender))
		return
	}
	sender, text, atts := in.Sender, strings.TrimSpace(in.Text), in.Files
	if text == "" && len(atts) == 0 {
		return
	}
	// While a tool waits for approval, the next message is the answer. It's checked
	// before the quote is added: a swipe-reply "yes" to the prompt is still a yes.
	if len(atts) == 0 && b.answer(sender, text) {
		return
	}
	if text != "" {
		text = withQuote(text, in.Quote)
	}
	msg := message{text: text, attachments: atts, ts: in.ID}
	// Read-only commands skip the queue so they answer even mid-reply.
	if instant(msg.text) {
		b.workers.Add(1)
		go func() {
			defer b.workers.Done()
			b.process(ctx, sender, msg, 0)
		}()
		return
	}
	b.enqueue(ctx, sender, msg)
}

// enqueue adds msg to sender's queue, starting its worker if needed. Messages from one
// sender are handled in order. It reports false if the queue is full.
func (b *Bot) enqueue(ctx context.Context, sender string, msg message) bool {
	b.mu.Lock()
	q, exists := b.queues[sender]
	if !exists {
		q = make(chan message, 16)
		b.queues[sender] = q
		b.workers.Add(1)
		go b.worker(ctx, sender, q)
	}
	b.mu.Unlock()

	select {
	case q <- msg:
		return true
	default:
		slog.Warn("queue full, dropping message", "sender", mask(sender))
		return false
	}
}

// Wait blocks until all workers have exited (after ctx is cancelled).
func (b *Bot) Wait() { b.workers.Wait() }

// allowed reports whether the sender is on the allowlist, by any of their names. (The transport
// has already dropped group chats, the bot's own messages, receipts and the like.)
func (b *Bot) allowed(in transport.Message) bool {
	if in.Sender == "" {
		return false
	}
	if b.opt.Allowed[in.Sender] {
		return true
	}
	for _, id := range in.AltIDs {
		if id != "" && b.opt.Allowed[id] {
			return true
		}
	}
	return false
}

// maxQuoteChars caps how much of a quoted message is passed on.
const maxQuoteChars = 1000

// withQuote puts the message being replied to (e.g. Signal's swipe-to-reply) in front of text,
// so "explain this part" refers to the right message. Commands are left alone.
func withQuote(text string, q *transport.Quote) string {
	if q == nil || strings.HasPrefix(text, "/") {
		return text
	}
	quoted := strings.TrimSpace(q.Text)
	if quoted == "" {
		return text
	}
	if r := []rune(quoted); len(r) > maxQuoteChars {
		quoted = string(r[:maxQuoteChars]) + "…"
	}
	// Messages from the bot aren't always the model's: command replies like /models are too.
	who := "their own earlier message"
	if q.FromBot {
		who = "this message the bot sent earlier, either your reply or the output of a command"
	}
	return "(The user is replying to " + who + ":\n> " + strings.ReplaceAll(quoted, "\n", "\n> ") + ")\n\n" + text
}

func (b *Bot) worker(ctx context.Context, sender string, q chan message) {
	defer b.workers.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-q:
			b.process(ctx, sender, msg, len(q))
		}
	}
}

// process answers one message and sends the reply.
func (b *Bot) process(ctx context.Context, sender string, msg message, queued int) {
	if msg.schedule != 0 {
		b.runSchedule(ctx, sender, msg.schedule)
		return
	}
	if msg.hook != nil {
		b.runHook(ctx, sender, msg.hook)
		return
	}
	start := time.Now()
	text := msg.text
	kind := "chat"
	if strings.HasPrefix(text, "/") {
		kind = "command"
	}
	slog.Info("message received", "from", mask(sender), "kind", kind, "chars", len(text), "attachments", len(msg.attachments), "queued", queued)
	unreact := func() {}
	if kind == "chat" || isRetry(text) {
		unreact = b.react(ctx, sender, msg.ts)
	}
	r := b.respond(ctx, sender, text, msg.attachments)
	if r.text == "" && len(r.images) == 0 {
		unreact()
		return
	}
	err := b.send(ctx, sender, r)
	unreact()
	if err != nil {
		slog.Error("send failed", "to", mask(sender), "err", err)
		return
	}
	slog.Info("reply sent", "to", mask(sender), "chars", len(r.text), "images", len(r.images), "took", time.Since(start).Round(100*time.Millisecond))
	if r.summarize {
		b.summarize(ctx, sender)
	}
}

// react shows the reaction emoji on the sender's message while it's being answered,
// and returns a func that removes it. Failures are logged and otherwise ignored.
func (b *Bot) react(ctx context.Context, sender string, ts int64) (remove func()) {
	if b.opt.Reaction == "" || ts == 0 {
		return func() {}
	}
	if err := b.msg.React(ctx, sender, ts, b.opt.Reaction, false); err != nil {
		slog.Warn("reaction failed", "err", err)
		return func() {}
	}
	return func() {
		// The reply may have been stopped or the bot be shutting down: use a fresh context.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := b.msg.React(rctx, sender, ts, b.opt.Reaction, true); err != nil {
			slog.Warn("removing reaction failed", "err", err)
		}
	}
}

// send delivers a reply. Model replies are Markdown, which the transport formats for its app.
func (b *Bot) send(ctx context.Context, to string, r response) error {
	var att []transport.Attachment
	for i, img := range r.images {
		att = append(att, transport.Attachment{MIME: img.MIME, Name: fmt.Sprintf("image-%d%s", i+1, ext(img.MIME)), Data: img.Data})
	}
	return b.msg.Send(ctx, to, r.text, r.fromModel, att)
}

func ext(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ""
}

// allowlistID is how a sender not on the allowlist is logged: in full if it has a transport
// prefix (e.g. telegram:12345, which the user needs to copy into allowed_senders), masked if it's
// a phone number.
func allowlistID(id string) string {
	if prefix, _, ok := strings.Cut(id, ":"); ok && prefix != "" && !strings.HasPrefix(id, "+") {
		return id
	}
	return mask(id)
}

// mask hides all but the last 4 characters of a phone number in logs.
func mask(s string) string {
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}

// modelOf is the model a chat uses: the one picked with /models, or the default.
func (b *Bot) modelOf(chat store.Chat) string {
	if chat.Model != "" {
		return chat.Model
	}
	return b.opt.DefaultModel
}
