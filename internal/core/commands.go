package core

// Slash commands (/status, /models, /stop, ...).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tfielding/llamero/internal/store"
)

func instant(text string) bool {
	f := strings.Fields(strings.ToLower(text))
	return len(f) > 0 && (f[0] == "/status" || f[0] == "/stats" || f[0] == "/stop" || f[0] == "/summary" || f[0] == "/settings" ||
		f[0] == "/knowledge" || f[0] == "/schedules" || f[0] == "/hooks" || f[0] == "/snooze")
}

func isRetry(text string) bool {
	f := strings.Fields(strings.ToLower(text))
	return len(f) > 0 && f[0] == "/retry"
}

func (b *Bot) command(ctx context.Context, sender string, chat *store.Chat, text string) string {
	fields := strings.Fields(text)
	cmd, args := strings.ToLower(fields[0]), fields[1:]
	current := chat.Model
	if current == "" {
		current = b.opt.DefaultModel
	}

	switch cmd {
	case "/help":
		return helpText
	case "/reset":
		chat.History, chat.Summary, chat.Unanswered = nil, "", ""
		if err := b.store.Save(sender, *chat); err != nil {
			return "Couldn't reset: " + err.Error()
		}
		return "History cleared."
	case "/status":
		ollamaState := "online"
		thinking := "unknown (Ollama offline)"
		if err := b.llm.Ping(ctx); err != nil {
			ollamaState = "offline (" + short(err) + ")"
		} else if info, err := b.thinkInfo(ctx, current); err == nil {
			thinking = thinkLabel(chat.Think, info)
		}
		memory := fmt.Sprintf("%d messages (max %d)", len(chat.History), b.opt.HistoryLimit)
		if chat.Summary != "" {
			memory += " + summary of older ones"
		}
		schedules := ""
		if b.opt.Schedules != nil {
			schedules = "Schedules: " + b.schedulesSummary(sender) + "\n"
		}
		return fmt.Sprintf("Model: %s\nThinking: %s\nMemory: %s\nKnowledge: %s\n%sOllama: %s\n%sNow: %s",
			current, thinking, memory, b.knowledgeSummary(), schedules, ollamaState, b.toolsStatus(ctx), b.now(sender))
	case "/schedules", "/schedule":
		return b.schedulesCommand(ctx, sender, args)
	case "/knowledge":
		return b.knowledgeText()
	case "/hooks":
		return b.hooksCommand(args)
	case "/snooze":
		return b.snoozeCommand(sender, args)
	case "/think":
		return b.thinkCommand(ctx, sender, chat, current, args)
	case "/stop":
		return b.stop(sender)
	case "/stats":
		return statsText(chat.Last)
	case "/summary":
		return b.summaryText(chat.Summary)
	case "/wake":
		return b.wakeCommand(ctx)
	case "/settings":
		if b.opt.Settings == "" {
			return "No settings to show."
		}
		return "Settings:\n" + b.opt.Settings
	case "/models", "/model": // /model is the old name, still accepted
		models, err := b.llm.Models(ctx)
		if err != nil {
			return "Couldn't list models: " + err.Error()
		}
		if len(args) == 0 {
			var sb strings.Builder
			sb.WriteString("Models (* = current):\n")
			for _, m := range models {
				mark := "  "
				if m == current {
					mark = "* "
				}
				sb.WriteString(mark + m + "\n")
			}
			sb.WriteString("\nSwitch with /models <name>")
			return sb.String()
		}
		name, ok := match(models, args[0])
		if !ok {
			return fmt.Sprintf("No model named %q. Send /models to see the list.", args[0])
		}
		chat.Model = name
		if name == b.opt.DefaultModel {
			chat.Model = ""
		}
		if err := b.store.Save(sender, *chat); err != nil {
			return "Couldn't save: " + err.Error()
		}
		msg := "Switched to " + name + "."
		if chat.Think != "" {
			if info, err := b.thinkInfo(ctx, name); err == nil && info.Known && !info.Supports(chat.Think) {
				msg += fmt.Sprintf("\nIt doesn't support thinking %q, so it will use its default. Options: %s", chat.Think, options(info))
			}
		}
		return msg
	default:
		return "Unknown command. " + helpText
	}
}

const helpText = `Commands:
/models - list models
/models <name> - switch model
/retry - answer your last message again
/reset - clear conversation memory
/summary - what it remembers from older messages
/status - model, memory, and what's running now
/think - show or change reasoning (on, off, low, medium, high, default)
/stop - cancel the reply in progress (and anything waiting)
/stats - speed of the last reply
/settings - current configuration (secrets hidden)
/knowledge - notes the bot has from the knowledge folder
/hooks - the latest hook checks and what each decided (/hooks stats for totals)
/snooze - mute hooks for a while (/snooze 2h, /snooze <hook> 30m, /snooze off)
/schedules - scheduled checks (delete, pause, resume or run with a number)
/wake - wake the Ollama machine (Wake-on-LAN)
/help - this message`

// knowledgeSummary is the "Knowledge:" value for /status.
func (b *Bot) knowledgeSummary() string {
	if b.opt.Knowledge == nil {
		return "off"
	}
	k := b.opt.Knowledge.Get()
	switch {
	case k.Err != nil:
		return "folder unreadable (" + short(k.Err) + ")"
	case len(k.Files) == 0:
		return "none"
	}
	return fmt.Sprintf("%s, %d characters", plural(len(k.Files), "file"), k.Chars)
}

// knowledgeText answers /knowledge: which notes are loaded, and when each is used.
func (b *Bot) knowledgeText() string {
	if b.opt.Knowledge == nil {
		return "The knowledge folder is off."
	}
	k := b.opt.Knowledge.Get()
	dir := b.opt.Knowledge.Dir()
	switch {
	case k.Err != nil:
		return fmt.Sprintf("Couldn't read the knowledge folder %s: %s", dir, short(k.Err))
	case len(k.Files) == 0:
		return fmt.Sprintf("No notes yet. Put Markdown files in %s: any name for things the bot should always know, "+
			"or name one after an MCP server (e.g. frigate.md) for notes used with its tools.", dir)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Notes in %s:\n", dir)
	for _, f := range k.Files {
		when := "always"
		if len(f.Servers) > 0 {
			when = "with the " + strings.Join(f.Servers, " and ") + " tools"
		}
		fmt.Fprintf(&sb, "- %s: %d characters, %s", f.Name, f.Chars, when)
		if f.Cut {
			sb.WriteString(" (cut off: over the limit)")
		}
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "Total %d of %d characters. Edits apply from the next message.", k.Chars, b.opt.Knowledge.MaxChars())
	return sb.String()
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// stop cancels the model call in progress for sender and drops queued messages.
func (b *Bot) stop(sender string) string {
	b.mu.Lock()
	a, busy := b.active[sender]
	q := b.queues[sender]
	b.mu.Unlock()

	dropped := 0
	if q != nil {
	drain:
		for {
			select {
			case <-q:
				dropped++
			default:
				break drain
			}
		}
	}
	if busy {
		a.cancel()
	}

	switch {
	case busy && dropped > 0:
		return fmt.Sprintf("Stopped, and dropped %d waiting message(s).", dropped)
	case busy:
		return "Stopped."
	case dropped > 0:
		return fmt.Sprintf("Dropped %d waiting message(s).", dropped)
	}
	return "Nothing is running."
}

// toolsStatus is the "Tools:" line for /status (empty if no MCP servers are configured).
func (b *Bot) toolsStatus(ctx context.Context) string {
	if b.opt.Tools == nil {
		return ""
	}
	tools := b.opt.Tools.Tools(ctx)
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	line := "Tools: none"
	if len(names) > 0 {
		line = "Tools: " + strings.Join(names, ", ")
	}
	for server, err := range b.opt.Tools.Errors() {
		line += fmt.Sprintf("\n%s: unavailable (%s)", server, short(err))
	}
	return line + "\n"
}

// now describes what the bot is doing for sender right now.
func (b *Bot) now(sender string) string {
	b.mu.Lock()
	a, busy := b.active[sender]
	waiting := 0
	if q := b.queues[sender]; q != nil {
		waiting = len(q)
	}
	b.mu.Unlock()

	s := "idle"
	if busy {
		s = fmt.Sprintf("%s thinking for %s", a.model, time.Since(a.started).Round(time.Second))
		if a.task != "" {
			s = fmt.Sprintf("%s %s for %s", a.model, a.task, time.Since(a.started).Round(time.Second))
		}
		if a.tool != "" {
			s += " (running " + a.tool + ")"
		}
	}
	if waiting > 0 {
		s += fmt.Sprintf(", %d message(s) waiting", waiting)
	}
	return s
}

func statsText(l *store.LastReply) string {
	if l == nil {
		return "No replies yet."
	}
	s := l.Stats
	var sb strings.Builder
	fmt.Fprintf(&sb, "Last reply: %s, %s ago\n", l.Model, time.Since(l.At).Round(time.Second))
	fmt.Fprintf(&sb, "Output: %d tokens at %.1f tok/s (%s)\n", s.EvalCount, s.TokensPerSecond(), round(s.EvalDuration))
	fmt.Fprintf(&sb, "Prompt: %d tokens at %.0f tok/s (%s)\n", s.PromptEvalCount, s.PromptTokensPerSecond(), round(s.PromptEvalDuration))
	fmt.Fprintf(&sb, "Model load: %s\n", round(s.LoadDuration))
	fmt.Fprintf(&sb, "Total: %s\n", round(s.TotalDuration))
	sb.WriteString("(Output includes thinking tokens.)")
	return sb.String()
}

func round(d time.Duration) time.Duration { return d.Round(100 * time.Millisecond) }

// match finds a model by exact name, or by name without the ":tag" (e.g. "llama3.2" -> "llama3.2:latest").
func match(models []string, want string) (string, bool) {
	for _, m := range models {
		if m == want {
			return m, true
		}
	}
	for _, m := range models {
		if base, _, _ := strings.Cut(m, ":"); base == want {
			return m, true
		}
	}
	return "", false
}
