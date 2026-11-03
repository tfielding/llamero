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

// needsApproval reports whether calling name must be approved first: its server asks for
// approval of tools that can change things. Tools not in the list need it too (run rejects
// them before this anyway).
func (b *Bot) needsApproval(tools []toolbox.Tool, name string) bool {
	t, ok := findTool(tools, name)
	return !ok || t.Confirm
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
// approved, or what to tell the model instead of running the tool, and whether the
// request timed out. The error is set
// only when ctx ends (e.g. /stop). lastFailure, if set, is the error from the tool call
// just before, shown so a retry doesn't come out of nowhere.
//
// While it waits, the sender's next message (other than a command) is the answer:
// see answer, called from Handle.
func (b *Bot) approve(ctx context.Context, sender string, tools []toolbox.Tool, name string, args map[string]any, lastFailure string) (decline string, timedOut bool, err error) {
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

	if err := b.msg.Send(ctx, sender, approvalPrompt(tools, name, args, lastFailure, b.opt.ApprovalTimeout), false, nil); err != nil {
		slog.Error("send approval request", "err", err)
		return "Couldn't ask the user for approval, so this action was NOT run.", false, nil
	}
	_ = b.msg.Typing(ctx, sender, false)

	timer := time.NewTimer(b.opt.ApprovalTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case <-timer.C:
		slog.Info("tool approval", "tool", name, "result", "timed out")
		return fmt.Sprintf("The user didn't approve within %s, so this action was NOT run.", b.opt.ApprovalTimeout), true, nil
	case reply := <-ch:
		if approved(reply) {
			slog.Info("tool approval", "tool", name, "result", "approved")
			return "", false, nil
		}
		slog.Info("tool approval", "tool", name, "result", "declined")
		msg := "The user declined, so this action was NOT run. Don't try it again unless they ask."
		if !declined(reply) {
			msg += fmt.Sprintf(" They replied: %q", reply)
		}
		return msg, false, nil
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
		return true
	default: // already answered: handle this message normally
		return false
	}
}

// awaiting reports whether sender is being asked to approve something.
func (b *Bot) awaiting(sender string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.approvals[sender]
	return ok
}

// approvalPrompt shows the call as it will be made. Call leaves out blank arguments
// and ones the tool doesn't declare; blank ones are just noise, but a real value being
// left out can widen an action, so those are listed.
func approvalPrompt(tools []toolbox.Tool, name string, args map[string]any, lastFailure string, timeout time.Duration) string {
	var sb strings.Builder
	if lastFailure != "" {
		sb.WriteString("The last try failed: " + snippet(lastFailure, 200) + "\n\n")
	}
	fmt.Fprintf(&sb, "Allow %s?", displayName(name))
	clean, ignored := args, []string(nil)
	if t, ok := findTool(tools, name); ok {
		var dropped []string
		clean, dropped = t.CleanArgs(args)
		for _, k := range dropped {
			if !toolbox.Blank(args[k]) {
				ignored = append(ignored, k+": "+argValue(args[k]))
			}
		}
	}
	keys := make([]string, 0, len(clean))
	for k := range clean {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString("\n• " + k + ": " + argValue(clean[k]))
	}
	if len(ignored) > 0 {
		sb.WriteString("\n• left out, the tool doesn't take it: " + strings.Join(ignored, "; "))
	}
	fmt.Fprintf(&sb, "\nReply yes to run it, anything else cancels (%s).", shortDuration(timeout))
	return sb.String()
}

// displayName drops a namespace prefix such as "devices__" from a tool name.
func displayName(name string) string {
	if i := strings.LastIndex(name, "__"); i >= 0 && i+2 < len(name) {
		return name[i+2:]
	}
	return name
}

// argValue shows strings as is, lists of strings comma-separated, and anything else as JSON.
func argValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return jsonValue(v)
			}
			parts[i] = s
		}
		return strings.Join(parts, ", ")
	}
	return jsonValue(v)
}

func jsonValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func shortDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", d/time.Minute)
	}
	return d.String()
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
