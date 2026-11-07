package core

// Folding old messages into a running summary, so long chats keep their context.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tfielding/llamero/internal/ollama"
)

const summaryInstructions = "You keep a running summary of a conversation between a user and an AI assistant, " +
	"so the assistant can remember it after the messages themselves are deleted. " +
	"Merge the new messages into the existing summary. Keep facts about the user, their preferences, " +
	"names, decisions, open questions and anything they asked to remember. Drop small talk and details " +
	"that no longer matter. Write plain sentences in the third person (\"The user ...\"), at most 200 words. " +
	"Reply with the summary only."

// summarize folds the oldest messages of sender's chat into its summary once the history
// is longer than HistoryLimit, keeping the newest half verbatim. It runs in the sender's
// worker after the reply is sent, so it never delays a reply or races another save.
// If it fails, the messages are kept and it is tried again after the next reply.
func (b *Bot) summarize(ctx context.Context, sender string) {
	chat, err := b.store.Load(sender)
	if err != nil {
		slog.Error("load history for summary", "err", err)
		return
	}
	cut := splitPoint(chat.History, b.opt.HistoryLimit)
	if cut == 0 {
		return
	}
	model := b.opt.SummaryModel
	if model == "" {
		model = chat.Model
	}
	if model == "" {
		model = b.opt.DefaultModel
	}
	if err := b.llm.Ping(ctx); err != nil {
		slog.Info("ollama unreachable, summary postponed", "err", err)
		return
	}
	info, _ := b.llm.Info(ctx, model)

	start := time.Now()
	b.mu.Lock()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.active[sender] = activity{model: model, started: start, task: "updating memory", cancel: cancel}
	b.mu.Unlock()
	res, err := b.llm.Chat(callCtx, model, summaryPrompt(chat.Summary, chat.History[:cut]), summaryThink(info.Think), nil)
	b.mu.Lock()
	delete(b.active, sender)
	b.mu.Unlock()
	took := time.Since(start).Round(100 * time.Millisecond)
	if err != nil {
		slog.Warn("summary failed, keeping messages", "model", model, "took", took, "err", err)
		return
	}
	summary := strings.TrimSpace(res.Content)
	if summary == "" {
		slog.Warn("summary was empty, keeping messages", "model", model, "took", took)
		return
	}

	chat.Summary = summary
	chat.History = append([]ollama.Message(nil), chat.History[cut:]...)
	if err := b.store.Save(sender, chat); err != nil {
		slog.Error("save summary", "err", err)
		return
	}
	slog.Info("history summarized", "model", model, "messages", cut, "kept", len(chat.History), "chars", len(summary), "took", took)
}

// splitPoint returns how many of the oldest messages to summarize: none until the history
// is longer than limit, then all but the newest half of limit, moved back if needed so the
// kept part starts with a user message.
func splitPoint(h []ollama.Message, limit int) int {
	if len(h) <= limit {
		return 0
	}
	cut := len(h) - limit/2
	for cut > 0 && h[cut].Role != "user" {
		cut--
	}
	return cut
}

func summaryPrompt(summary string, msgs []ollama.Message) []ollama.Message {
	if summary == "" {
		summary = "(none yet)"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Existing summary:\n%s\n\nNew messages:\n", summary)
	for _, m := range msgs {
		who := "User"
		if m.Role == "assistant" {
			who = "Assistant"
		}
		fmt.Fprintf(&sb, "\n%s: %s\n", who, m.Content)
	}
	return []ollama.Message{
		{Role: "system", Content: summaryInstructions},
		{Role: "user", Content: sb.String()},
	}
}

// summaryThink keeps reasoning to a minimum for summaries: off, or the lowest level the model has.
func summaryThink(info ollama.ThinkInfo) any {
	if !info.Known {
		return nil
	}
	for _, v := range []string{"off", "low"} {
		if info.Supports(v) {
			return ollama.ThinkValue(v)
		}
	}
	return nil
}

// summaryText is the reply to /summary.
func (b *Bot) summaryText(summary string) string {
	switch {
	case summary != "":
		return "What I remember from earlier in this chat:\n\n" + summary
	case !b.opt.Summarize:
		return "Summaries are off (HISTORY_SUMMARY=false): older messages are dropped."
	}
	return fmt.Sprintf("No summary yet. Once this chat has more than %d messages, older ones are summarized here.", b.opt.HistoryLimit)
}
