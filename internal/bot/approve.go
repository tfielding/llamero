package bot

// Asking the user before running a tool that can change things (MCP_ALLOW_WRITE=ask).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/tfielding/signal-ollama/internal/toolbox"
)

// needsApproval reports whether calling name must be approved first. Tools not in the
// list are treated as able to change things.
func (b *Bot) needsApproval(tools []toolbox.Tool, name string) bool {
	if !b.opt.ConfirmWrites {
		return false
	}
	t, ok := findTool(tools, name)
	return !ok || !t.ReadOnly
}

func findTool(tools []toolbox.Tool, name string) (toolbox.Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return toolbox.Tool{}, false
}

// approve asks sender to allow a tool call and waits for the answer. It returns "" if
// approved, or what to tell the model instead of running the tool. The error is set
// only when ctx ends (e.g. /stop).
//
// While it waits, the sender's next message (other than a command) is the answer:
// see answer, called from Handle.
func (b *Bot) approve(ctx context.Context, sender string, tools []toolbox.Tool, name string, args map[string]any) (string, error) {
	ch := make(chan string, 1)
	b.mu.Lock()
	b.approvals[sender] = ch
	if a, ok := b.active[sender]; ok {
		a.task = "waiting for your OK to run " + name
		b.active[sender] = a
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.approvals, sender)
		if a, ok := b.active[sender]; ok {
			a.task = ""
			b.active[sender] = a
		}
		b.mu.Unlock()
	}()

	if err := b.msg.Send(ctx, sender, approvalPrompt(tools, name, args, b.opt.ApprovalTimeout), false, nil); err != nil {
		slog.Error("send approval request", "err", err)
		return "Couldn't ask the user for approval, so this action was NOT run.", nil
	}
	_ = b.msg.Typing(ctx, sender, false)

	timer := time.NewTimer(b.opt.ApprovalTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		slog.Info("tool approval", "tool", name, "result", "timed out")
		return fmt.Sprintf("The user didn't approve within %s, so this action was NOT run.", b.opt.ApprovalTimeout), nil
	case reply := <-ch:
		if approved(reply) {
			slog.Info("tool approval", "tool", name, "result", "approved")
			return "", nil
		}
		slog.Info("tool approval", "tool", name, "result", "declined")
		msg := "The user declined, so this action was NOT run. Don't try it again unless they ask."
		if !declined(reply) {
			msg += fmt.Sprintf(" They replied: %q", reply)
		}
		return msg, nil
	}
}

// answer hands text to a pending approval for sender. Commands are not answers.
func (b *Bot) answer(sender, text string) bool {
	if strings.HasPrefix(text, "/") {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.approvals[sender]
	if !ok {
		return false
	}
	select {
	case ch <- text:
	default: // already answered
	}
	return true
}

// awaiting reports whether sender is being asked to approve something.
func (b *Bot) awaiting(sender string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.approvals[sender]
	return ok
}

// approvalPrompt shows the call as it will be made: Call removes blank and undeclared
// arguments, so those are listed separately rather than shown as if they'd be used.
func approvalPrompt(tools []toolbox.Tool, name string, args map[string]any, timeout time.Duration) string {
	var sb strings.Builder
	sb.WriteString("Allow this action?\n" + name)
	clean, dropped := args, []string(nil)
	if t, ok := findTool(tools, name); ok {
		if t.Server() != "" {
			sb.WriteString(" (" + t.Server() + ")")
		}
		clean, dropped = t.CleanArgs(args)
	}
	keys := make([]string, 0, len(clean))
	for k := range clean {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString("\n  " + k + ": " + argValue(clean[k]))
	}
	if len(keys) == 0 {
		sb.WriteString("\n  (no arguments)")
	}
	if len(dropped) > 0 {
		sb.WriteString("\n  ignored: " + strings.Join(dropped, ", "))
	}
	fmt.Fprintf(&sb, "\nReply yes to run it, anything else to cancel. (Expires in %s.)", timeout)
	return sb.String()
}

func argValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func approved(reply string) bool {
	switch normalize(reply) {
	case "yes", "y", "yep", "yeah", "ok", "okay", "sure", "approve", "approved", "allow", "go", "do it", "go ahead", "👍":
		return true
	}
	return false
}

func declined(reply string) bool {
	switch normalize(reply) {
	case "no", "n", "nope", "cancel", "stop", "don't", "dont", "deny", "👎":
		return true
	}
	return false
}

func normalize(s string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(s)), ".!?, ")
}
