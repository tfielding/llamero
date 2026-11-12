package core

// Answering a chat message: the model call and the tool-use loop.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tfielding/llamero/internal/knowledge"
	"github.com/tfielding/llamero/internal/ollama"
	"github.com/tfielding/llamero/internal/store"
	"github.com/tfielding/llamero/internal/toolbox"
	"github.com/tfielding/llamero/internal/transport"
)

// DefaultMaxToolRounds limits how many times the model can call tools before answering,
// unless Options.MaxToolRounds says otherwise.
const DefaultMaxToolRounds = 6

// maxToolFailures stops tool use early when calls keep failing.
const maxToolFailures = 3

// response is what gets sent back to the user.
type response struct {
	text      string
	fromModel bool // Markdown from the model (the transport formats it for its app)
	images    []toolbox.Image
	summarize bool // history is past the limit: summarize after sending
}

// respond produces the reply for one message.
func (b *Bot) respond(ctx context.Context, sender, text string, atts []transport.File) response {
	chat, err := b.store.Load(sender)
	if err != nil {
		slog.Error("load history", "err", err)
		return response{text: "Couldn't load chat history: " + err.Error()}
	}
	if isRetry(text) {
		// Answer again the last message that failed, or else the last one answered. The
		// old answer is only replaced once the new one is saved, so a failed retry keeps it.
		n := len(chat.History)
		switch {
		case chat.Unanswered != "":
			text = chat.Unanswered
		case n >= 2 && chat.History[n-2].Role == "user" && chat.History[n-1].Role == "assistant":
			text = chat.History[n-2].Content
			chat.History = chat.History[:n-2]
		default:
			return response{text: "Nothing to retry yet."}
		}
	} else if strings.HasPrefix(text, "/") {
		return response{text: b.command(ctx, sender, &chat, text)}
	}

	var photos [][]byte
	if len(atts) > 0 {
		var note string
		photos, note = b.loadPhotos(ctx, atts)
		if len(photos) == 0 && text == "" {
			for _, a := range atts {
				if strings.HasPrefix(strings.ToLower(a.MIME), "image/") {
					return response{text: "Sorry, I couldn't read that photo. Try sending it again."}
				}
			}
			return response{text: "I can only look at photos for now, not files, videos or voice notes."}
		}
		if text == "" {
			text = photoText
		}
		if note != "" {
			text += "\n\n" + note
		}
	}

	model := b.modelOf(chat)
	// Fail fast with a clear message if Ollama is down (e.g. the machine is asleep),
	// or wake it first if Wake-on-LAN is set up.
	if err := b.llm.Ping(ctx); err != nil {
		slog.Warn("ollama unreachable", "err", err)
		if b.opt.Wake == nil {
			return b.unanswered(sender, text, response{text: offlineText})
		}
		switch err := b.wake(ctx, sender); {
		case errors.Is(err, errStopped):
			return b.unanswered(sender, text, response{})
		case errors.Is(err, errWakeSend):
			return b.unanswered(sender, text, response{text: offlineText})
		case err != nil:
			return b.unanswered(sender, text, response{text: wakeFailedText})
		}
	}
	info, _ := b.llm.Info(ctx, model)
	if len(photos) > 0 && !info.Has("vision") {
		// The chat's model can't see: the vision model answers this message, the chat keeps its model.
		if b.opt.VisionModel == "" {
			return response{text: noVisionText(model)}
		}
		vinfo, err := b.llm.Info(ctx, b.opt.VisionModel)
		if err != nil || !vinfo.Has("vision") {
			slog.Warn("vision model can't see pictures or isn't installed", "model", b.opt.VisionModel, "err", err)
			return response{text: fmt.Sprintf("OLLAMA_VISION_MODEL %s can't see pictures, or isn't installed in Ollama. "+
				"Check `ollama list`, and that it's a vision model.", b.opt.VisionModel)}
		}
		slog.Info("photo: using the vision model", "model", b.opt.VisionModel, "chat_model", model, "photos", len(photos))
		model, info = b.opt.VisionModel, vinfo
	}
	think := b.thinkValue(ctx, model, chat.Think)
	var tools []toolbox.Tool
	if info.Has("tools") {
		if b.opt.Tools != nil {
			tools = append(tools, b.opt.Tools.Tools(ctx)...)
		}
		if b.opt.Schedules != nil {
			tools = append(tools, scheduleTools()...)
		}
	}
	msgs := b.prompt(chat, tools, ollama.Message{Role: "user", Content: text, Images: photos})

	slog.Info("asking model", "model", model, "history", len(chat.History), "think", chat.Think, "tools", len(tools))
	start := time.Now()
	b.mu.Lock()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.active[sender] = activity{model: model, started: start, cancel: cancel}
	b.mu.Unlock()
	stopTyping := b.typing(ctx, sender)
	answer, images, stats, err := b.run(callCtx, sender, model, msgs, think, tools, info.Has("vision"))
	stopTyping()
	if na := (noAnswer{}); errors.As(err, &na) {
		answer, err = emptyAnswer(na.failures, na.rounds), nil
	}
	b.mu.Lock()
	delete(b.active, sender)
	b.mu.Unlock()
	if err != nil && callCtx.Err() != nil && ctx.Err() == nil {
		// Cancelled by /stop, which already replied. Only kept for /retry.
		slog.Info("model call stopped", "model", model, "took", time.Since(start).Round(100*time.Millisecond))
		return b.unanswered(sender, text, response{})
	}
	if err != nil {
		slog.Error("ollama chat", "model", model, "took", time.Since(start).Round(100*time.Millisecond), "err", err)
		if unreachable(err) {
			return b.unanswered(sender, text, response{text: offlineText})
		}
		return b.unanswered(sender, text, response{text: "Model error: " + err.Error()})
	}
	slog.Info("model replied", "model", model, "took", time.Since(start).Round(100*time.Millisecond),
		"tokens", stats.EvalCount, "tok_per_s", fmt.Sprintf("%.1f", stats.TokensPerSecond()), "images", len(images))
	answer = stripImageNotes(answer)
	if answer == "" && len(images) == 0 {
		answer = "(empty reply)"
	}

	chat.Last = &store.LastReply{Model: model, At: time.Now(), Stats: stats}
	chat.Unanswered = ""
	saved := answer
	if len(images) > 0 {
		saved += fmt.Sprintf("\n\n[%d image(s) were sent to the user with this reply.]", len(images))
	}
	asked := text
	if len(photos) > 0 {
		// The photo itself isn't kept: later messages have the reply describing it.
		asked += fmt.Sprintf("\n\n[The user sent %d photo(s) with this message.]", len(photos))
	}
	chat.History = trim(append(chat.History,
		ollama.Message{Role: "user", Content: asked},
		ollama.Message{Role: "assistant", Content: strings.TrimSpace(saved)}), b.historyCap())
	if err := b.store.Save(sender, chat); err != nil {
		slog.Error("save history", "err", err)
	}
	return response{text: answer, fromModel: true, images: images,
		summarize: b.opt.Summarize && len(chat.History) > b.opt.HistoryLimit}
}

// prompt builds the messages for the model: instructions, notes, tool guidance (if there are
// tools), the conversation so far, then last.
func (b *Bot) prompt(chat store.Chat, tools []toolbox.Tool, last ollama.Message) []ollama.Message {
	msgs := make([]ollama.Message, 0, len(chat.History)+6)
	if b.opt.SystemPrompt != "" {
		msgs = append(msgs, ollama.Message{Role: "system", Content: b.opt.SystemPrompt})
	}
	var know knowledge.Snapshot
	if b.opt.Knowledge != nil {
		know = b.opt.Knowledge.Get()
	}
	if know.General != "" {
		msgs = append(msgs, ollama.Message{Role: "system", Content: knowledgeIntro + know.General})
	}
	if len(tools) > 0 {
		// After the main system prompt, so it applies even with a custom SYSTEM_PROMPT.
		content := toolGuidance
		if b.opt.Tools != nil {
			if extra := b.opt.Tools.Guidance(tools, know.Servers); extra != "" {
				content += "\n\n" + extra
			}
		}
		msgs = append(msgs, ollama.Message{Role: "system", Content: content})
	}
	if chat.Summary != "" {
		msgs = append(msgs, ollama.Message{Role: "system", Content: "Summary of the earlier conversation " +
			"(those messages are no longer shown):\n" + chat.Summary})
	}
	msgs = append(msgs, forPrompt(chat.History)...)
	// The time goes last, just before the message: models need it for "yesterday" or "this
	// week", and anything that changes every minute near the start would make Ollama read the
	// whole prompt again instead of reusing what it read for the previous message.
	msgs = append(msgs, ollama.Message{Role: "system", Content: currentTime(time.Now().In(b.loc()))})
	return append(msgs, last)
}

// currentTime tells the model the date and time, so it needn't call a tool for them.
func currentTime(t time.Time) string {
	return "Current date and time: " + t.Format("Monday 2 January 2006, 15:04 (MST, UTC-07:00)") + "."
}

// historyCap is how many messages a chat keeps. With summaries on, messages past the limit wait
// to be summarized after the reply is sent; if summaries keep failing, the oldest are dropped
// at twice the limit.
func (b *Bot) historyCap() int {
	if b.opt.Summarize {
		return b.opt.HistoryLimit * 2
	}
	return b.opt.HistoryLimit
}

// imageNote is saved after a reply that sent images, so later turns know they were sent.
var imageNote = regexp.MustCompile(`\s*\[(\d+) image\(s\) were sent to the user with this reply\.\]\s*$`)

// anyImageNote matches the image notes in any wording the model might copy into a reply.
var anyImageNote = regexp.MustCompile(`(?i)\s*\[\d+ images?(\(s\))? (were|was|are|will be|returned|attached)[^\]\n]*\]`)

// forPrompt prepares saved history for the model. The image note saved with a reply is moved
// out of the reply into a system line: left in, the model copies it into its own replies.
func forPrompt(history []ollama.Message) []ollama.Message {
	out := make([]ollama.Message, 0, len(history))
	for _, m := range history {
		if m.Role != "assistant" {
			out = append(out, m)
			continue
		}
		loc := imageNote.FindStringSubmatchIndex(m.Content)
		if loc == nil {
			out = append(out, m)
			continue
		}
		n := m.Content[loc[2]:loc[3]]
		m.Content = strings.TrimSpace(m.Content[:loc[0]])
		out = append(out, m, ollama.Message{Role: "system", Content: "(That reply also sent " + n + " image(s) to the user.)"})
	}
	return out
}

// stripImageNotes removes image notes the model copied into its answer.
func stripImageNotes(answer string) string {
	return strings.TrimSpace(anyImageNote.ReplaceAllString(answer, ""))
}

// unanswered records text as the message /retry should answer, and returns r. It reloads
// the chat so a failed /retry doesn't save the history with the old answer removed.
func (b *Bot) unanswered(sender, text string, r response) response {
	chat, err := b.store.Load(sender)
	if err != nil {
		slog.Error("load history", "err", err)
		return r
	}
	chat.Unanswered = text
	if err := b.store.Save(sender, chat); err != nil {
		slog.Error("save history", "err", err)
	}
	return r
}

// run asks the model, executing any tool calls it makes, until it gives a final answer.
// Images returned by tools are collected to send to the user, and shown to the model
// too if it supports vision.
func (b *Bot) run(ctx context.Context, sender, model string, msgs []ollama.Message, think any,
	tools []toolbox.Tool, vision bool) (string, []toolbox.Image, ollama.Stats, error) {

	r := &toolRun{b: b, sender: sender, tools: tools, vision: vision, msgs: msgs}
	r.defs = make([]ollama.Tool, len(tools))
	for i, t := range tools {
		r.defs[i] = ollama.Tool{Type: "function", Function: ollama.ToolFunction{Name: t.Name, Description: t.Description, Parameters: t.Schema}}
	}
	for round := 0; ; round++ {
		r.limit(round)
		res, err := b.llm.Chat(ctx, model, r.msgs, think, r.defs)
		if err != nil {
			return "", nil, r.stats, err
		}
		r.stats.Add(res.Stats)
		if len(res.ToolCalls) == 0 || len(r.defs) == 0 {
			if strings.TrimSpace(res.Content) != "" || len(r.images) > 0 {
				return res.Content, r.images, r.stats, nil
			}
			if round > 0 && !r.nudged {
				r.nudge(round)
				continue
			}
			return "", nil, r.stats, noAnswer{failures: r.failures, rounds: round}
		}
		r.msgs = append(r.msgs, ollama.Message{Role: "assistant", Content: res.Content, Thinking: res.Thinking, ToolCalls: res.ToolCalls})
		for _, tc := range res.ToolCalls {
			if err := r.call(ctx, tc.Function.Name, tc.Function.Arguments); err != nil {
				return "", nil, r.stats, err
			}
		}
	}
}

// toolRun is the state of one run's tool use: the conversation so far, and what it has cost.
type toolRun struct {
	b      *Bot
	sender string
	tools  []toolbox.Tool
	vision bool // the model sees pictures

	msgs        []ollama.Message
	defs        []ollama.Tool // the tools offered; nil once tool use is over
	images      []toolbox.Image
	stats       ollama.Stats
	failures    int
	nudged      bool   // asked once for an answer after the model wrote none
	lastFailure string // error from the previous tool call, shown if the next one needs approval
	declines    int
	toolsOver   bool // the user declined twice or didn't answer: no more tool calls
}

// limit ends tool use when it has gone on long enough: too many rounds or failures, or the
// user said no. The model is then asked for a final answer.
func (r *toolRun) limit(round int) {
	if round != r.b.opt.MaxToolRounds && r.failures < maxToolFailures && !r.toolsOver {
		return
	}
	if len(r.defs) > 0 {
		slog.Warn("no more tool calls for this message, asking for a final answer", "rounds", round, "failures", r.failures)
		r.msgs = append(r.msgs, ollama.Message{Role: "system", Content: "Tool use is over for this message. " +
			"Answer now with what you have. If the tools failed, say so plainly; do not guess values."})
	}
	r.defs = nil
}

// nudge asks once more for an answer: some models stop after their tool calls without
// writing anything.
func (r *toolRun) nudge(round int) {
	slog.Warn("model wrote no answer after using tools, asking again", "rounds", round)
	r.nudged, r.defs = true, nil
	r.msgs = append(r.msgs, ollama.Message{Role: "system", Content: "You used tools but wrote no answer. " +
		"Answer now, in words, with what the tools returned. If they found nothing, say so."})
}

// call handles one tool call: runs it if it may, and tells the model the result or why not.
func (r *toolRun) call(ctx context.Context, name string, args map[string]any) error {
	refusal, err := r.gate(ctx, name, args)
	if err != nil {
		return err
	}
	if refusal != "" {
		r.msgs = append(r.msgs, ollama.Message{Role: "tool", ToolName: name, Content: refusal})
		return nil
	}
	out, err := r.b.runTool(ctx, r.sender, name, args)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.lastFailure = ""
	if err != nil || out.IsError {
		r.failures++
		r.lastFailure = out.Text
		if err != nil {
			r.lastFailure = err.Error()
		}
	}
	r.msgs = append(r.msgs, r.result(name, out, err))
	return nil
}

// gate decides whether a tool call may run. If not, refusal is what the model is told instead:
// the tool doesn't exist, the schedule is invalid, or the user said no.
func (r *toolRun) gate(ctx context.Context, name string, args map[string]any) (refusal string, err error) {
	if r.toolsOver {
		return "Not run: no more actions for this message.", nil
	}
	// Small models invent tool names. Don't ask the user to approve something that
	// can't run: tell the model what exists so it can correct the name.
	if _, ok := findTool(r.tools, name); !ok {
		slog.Warn("model called a tool that doesn't exist", "tool", name)
		r.failures++
		r.lastFailure = fmt.Sprintf("there is no tool named %q", name)
		return unknownTool(name, r.tools), nil
	}
	if isScheduleTool(name) {
		if problem := r.b.checkScheduleCall(r.sender, name, args); problem != "" {
			r.failures++
			r.lastFailure = problem
			return problem, nil
		}
	}
	if !r.b.needsApproval(r.tools, name) {
		return "", nil
	}
	decline, timedOut, err := r.b.approve(ctx, r.sender, r.tools, name, args, r.lastFailure)
	if err != nil {
		return "", err
	}
	r.lastFailure = ""
	if decline != "" {
		// Don't keep asking someone who said no twice or isn't there.
		r.declines++
		r.toolsOver = timedOut || r.declines >= 2
	}
	return decline, nil
}

// result is the message telling the model what a tool returned. Pictures are kept to send to
// the user, and attached for the model if it can see them.
func (r *toolRun) result(name string, out toolbox.Output, err error) ollama.Message {
	msg := ollama.Message{Role: "tool", ToolName: name, Content: out.Text}
	if err != nil {
		msg.Content = "Tool failed: " + err.Error()
	} else if out.IsError {
		msg.Content = "Tool returned an error: " + out.Text
	}
	unseen := 0
	for _, img := range out.Images {
		r.images = append(r.images, img)
		// Vision models take still JPEG or PNG pictures; a clip (GIF) is only for the user.
		if r.vision && stillImage(img.MIME) {
			msg.Images = append(msg.Images, img.Data)
		} else {
			unseen++
		}
	}
	if seen := len(out.Images) - unseen; seen > 0 {
		msg.Content = strings.TrimSpace(msg.Content + "\n" +
			fmt.Sprintf("[%d image(s) attached. They will also be sent to the user with your reply.]", seen))
	}
	if unseen > 0 {
		msg.Content = strings.TrimSpace(msg.Content + "\n" +
			fmt.Sprintf("[%d image(s) or clip(s) returned. They will be sent to the user with your reply; you cannot see them.]", unseen))
	}
	return msg
}

// runTool runs a tool (an MCP server's, or one of the schedule tools) and logs the call.
func (b *Bot) runTool(ctx context.Context, sender, name string, args map[string]any) (toolbox.Output, error) {
	b.setTool(sender, name)
	start := time.Now()
	var out toolbox.Output
	var err error
	if isScheduleTool(name) {
		out = b.callScheduleTool(sender, name, args)
	} else {
		out, err = b.opt.Tools.Call(ctx, name, args)
	}
	b.setTool(sender, "")
	attrs := []any{"tool", name, "args", argKeys(args), "took", time.Since(start).Round(time.Millisecond),
		"images", len(out.Images), "is_error", out.IsError, "chars", len(out.Text)}
	if len(out.Dropped) > 0 {
		attrs = append(attrs, "dropped_args", out.Dropped)
	}
	if out.Retried != "" {
		attrs = append(attrs, "retried_without_args", snippet(out.Retried, 120))
	}
	if out.Truncated > 0 {
		attrs = append(attrs, "truncated_chars", out.Truncated)
	}
	if err != nil {
		attrs = append(attrs, "err", err)
	} else if out.IsError {
		attrs = append(attrs, "error_text", snippet(out.Text, 200))
	}
	if ctx.Err() == nil {
		slog.Info("tool call", attrs...)
	}
	return out, err
}

// unknownTool tells the model a tool doesn't exist and lists the ones that do.
func unknownTool(name string, tools []toolbox.Tool) string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return fmt.Sprintf("Tool failed: there is no tool named %q, so nothing was run. Use one of these exact names: %s.",
		name, strings.Join(names, ", "))
}

// noAnswer is run's error when the model wrote nothing, even after being asked again. A chat
// reply explains it (emptyAnswer); a check nobody is waiting on reports it as a failure.
type noAnswer struct{ failures, rounds int }

func (e noAnswer) Error() string { return "the model gave no answer" }

// emptyAnswer explains a blank reply instead of sending nothing useful.
func emptyAnswer(failures, rounds int) string {
	if failures > 0 {
		return fmt.Sprintf("Sorry, I couldn't get an answer: %d tool call(s) failed. Try rephrasing, or check the bot's logs for the tool error.", failures)
	}
	if rounds > 0 {
		return "Sorry, the model returned no answer after using its tools. Try asking again."
	}
	return "Sorry, the model returned an empty answer. Try asking again, or /reset if this keeps happening."
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		return s[:n] + "…"
	}
	return s
}

func (b *Bot) setTool(sender, tool string) {
	b.mu.Lock()
	if a, ok := b.active[sender]; ok {
		a.tool = tool
		b.active[sender] = a
	}
	b.mu.Unlock()
}

// argKeys logs argument names only, not values.
func argKeys(args map[string]any) []string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	return keys
}

// typing shows the indicator until the returned func is called.
func (b *Bot) typing(ctx context.Context, to string) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(b.opt.TypingRefresh)
		defer t.Stop()
		paused := false
		for {
			if b.awaiting(to) {
				// Off while the user is asked to approve a tool call. approve turns it off
				// too, but this catches an "on" sent at the same moment.
				if !paused {
					_ = b.msg.Typing(ctx, to, false)
					paused = true
				}
			} else if err := b.msg.Typing(ctx, to, true); err != nil {
				slog.Warn("typing indicator failed", "err", err)
			} else {
				paused = false
			}
			select {
			case <-done:
				// The call may have been cancelled (/stop), so use a fresh context.
				offCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_ = b.msg.Typing(offCtx, to, false)
				cancel()
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done); <-finished }
}

// toolGuidance is added to the prompt whenever tools are available. It is deliberately
// generic: nothing here knows what the tools are.
// knowledgeIntro introduces the notes from the knowledge folder.
const knowledgeIntro = "Background the user wrote for you (people, places, routines). Use it when it's relevant; " +
	"don't repeat it unprompted.\n\n"

const toolGuidance = "You have tools that can look things up or take actions. " +
	"Use them whenever an answer depends on information they can provide, instead of guessing or saying you can't. " +
	"Information from tools can change: call them again rather than relying on earlier messages in this conversation. " +
	"Pass only the parameters a tool defines, and leave optional parameters out unless you're sure of the exact value. " +
	"If a tool returns an error or nothing useful, read the message and try a different call (often with fewer parameters); do not repeat the same call. " +
	"Never invent facts or data: report only what the tools returned, and say so if they failed. " +
	"Only offer to do things you have a tool for."

const offlineText = "Can't reach Ollama right now. The machine running it may be asleep or offline. Try again in a bit."

const wakeFailedText = "Sent a wake-up, but Ollama didn't come online in time. The machine may be off " +
	"or not set up for Wake-on-LAN. Try again in a bit, or send /wake."

// unreachable reports whether err means Ollama couldn't be contacted (as opposed to a slow or failed reply).
func unreachable(err error) bool {
	var oe *net.OpError
	return errors.As(err, &oe) && (oe.Op == "dial" || !oe.Timeout()) // couldn't connect, even if that timed out
}

func short(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timed out"
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	return err.Error()
}

// trim keeps the newest limit messages, starting on a user turn.
func trim(h []ollama.Message, limit int) []ollama.Message {
	if len(h) <= limit {
		return h
	}
	h = h[len(h)-limit:]
	for len(h) > 0 && h[0].Role != "user" {
		h = h[1:]
	}
	return append([]ollama.Message(nil), h...)
}
