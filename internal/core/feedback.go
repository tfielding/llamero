package core

// Feedback on hooks: the user says a 🔔 message was a false alarm (or right, or that one was
// missed), the model saves it with hook_feedback, and later checks of that hook get these
// verdicts as examples. Saved as Markdown in the data folder, one file per hook.

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tfielding/llamero/internal/toolbox"
)

const (
	toolHookFeedback = "hook_feedback"

	maxLearned      = 30   // verdicts kept per hook; older ones are dropped
	maxLearnedChars = 3000 // of them given to a check, newest first
	maxFeedbackText = 500  // longest event or note saved
)

var verdicts = map[string]string{
	"false_alarm": "false alarm: reported, but it shouldn't have been",
	"correct":     "correct: rightly reported",
	"missed":      "missed: not reported, but it should have been",
}

// hookResult is a hook's last checked run, for feedback that doesn't repeat the event.
type hookResult struct {
	events, answer string
}

func (b *Bot) feedbackOn() bool { return b.opt.LearnedDir != "" && len(b.opt.Hooks) > 0 }

func (b *Bot) feedbackTool() toolbox.Tool {
	names := make([]string, 0, len(b.opt.Hooks))
	for name := range b.opt.Hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return toolbox.Tool{
		Name: toolHookFeedback,
		Description: "Save the user's verdict on a message from a hook (one starting with 🔔 and the hook's name), so " +
			"later checks of that hook learn from it. Use it when the user says such a message was a false alarm, " +
			"that it was right, or that something should have been reported. Put their reason in note.",
		Schema: map[string]any{"type": "object", "required": []string{"hook", "verdict"}, "properties": map[string]any{
			"hook":    map[string]any{"type": "string", "enum": names, "description": "the hook's name, from the 🔔 message"},
			"verdict": map[string]any{"type": "string", "enum": []string{"false_alarm", "correct", "missed"}},
			"event": str("optional: the event it was about, as in the 🔔 message or the user's words; " +
				"leave out for the hook's latest message"),
			"note": str("optional: the user's reason, in their words, e.g. \"it's the plant, not a person\""),
		}},
	}
}

// callFeedbackTool saves a verdict.
func (b *Bot) callFeedbackTool(args map[string]any) toolbox.Output {
	hook := strings.TrimSpace(argString(args, "hook"))
	if _, ok := b.opt.Hooks[hook]; !ok {
		return toolbox.Output{Text: fmt.Sprintf("there is no hook named %q", hook), IsError: true}
	}
	verdict := strings.TrimSpace(argString(args, "verdict"))
	what, ok := verdicts[verdict]
	if !ok {
		return toolbox.Output{Text: "verdict must be false_alarm, correct or missed", IsError: true}
	}
	event, answer := cut(argString(args, "event")), ""
	b.mu.Lock()
	last, known := b.lastHook[hook]
	b.mu.Unlock()
	if known && (event == "" || strings.Contains(last.events, event)) {
		event, answer = cut(last.events), cut(last.answer)
	}
	if event == "" {
		return toolbox.Output{Text: "say which event it was about: there's no recent message from " + hook, IsError: true}
	}
	line := fmt.Sprintf("- %s, %s. Event: %s", time.Now().In(b.loc()).Format("2006-01-02 15:04"), what, oneLine(event))
	if answer != "" {
		line += fmt.Sprintf(". The check said: %q", oneLine(answer))
	}
	if note := cut(argString(args, "note")); note != "" {
		line += fmt.Sprintf(". The user: %q", oneLine(note))
	}
	if err := b.addLearned(hook, line); err != nil {
		slog.Error("saving hook feedback", "hook", hook, "err", err)
		return toolbox.Output{Text: "couldn't save it: " + err.Error(), IsError: true}
	}
	slog.Info("hook feedback saved", "hook", hook, "verdict", verdict)
	return toolbox.Output{Text: fmt.Sprintf("Saved. Later %s checks will take it into account. The user can see "+
		"these with /learned.", hook)}
}

func (b *Bot) learnedPath(hook string) string { return filepath.Join(b.opt.LearnedDir, hook+".md") }

// learned returns a hook's saved verdicts, oldest first.
func (b *Bot) learned(hook string) []string {
	data, err := os.ReadFile(b.learnedPath(hook))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("reading hook feedback", "hook", hook, "err", err)
		}
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "- ") {
			out = append(out, l)
		}
	}
	return out
}

func (b *Bot) addLearned(hook, line string) error {
	b.learnMu.Lock()
	defer b.learnMu.Unlock()
	lines := append(b.learned(hook), line)
	if len(lines) > maxLearned {
		lines = lines[len(lines)-maxLearned:]
	}
	return b.writeLearned(hook, lines)
}

func (b *Bot) writeLearned(hook string, lines []string) error {
	if err := os.MkdirAll(b.opt.LearnedDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("# What the user said about %s checks\n\n%s\n", hook, strings.Join(lines, "\n"))
	tmp := b.learnedPath(hook) + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.learnedPath(hook))
}

// learnedForCheck is the part of a hook's prompt with the user's earlier verdicts: the newest
// that fit, oldest first.
func (b *Bot) learnedForCheck(hook string) string {
	if b.opt.LearnedDir == "" {
		return ""
	}
	lines := b.learned(hook)
	var keep []string
	size := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if size+len(lines[i]) > maxLearnedChars {
			break
		}
		size += len(lines[i]) + 1
		keep = append([]string{lines[i]}, keep...)
	}
	if len(keep) == 0 {
		return ""
	}
	return "The user's verdicts on earlier events from this hook. Judge this one the same way, " +
		"and above all don't repeat a false alarm like these:\n" + strings.Join(keep, "\n") + "\n\n"
}

// learnedCommand answers /learned [clear <hook>].
func (b *Bot) learnedCommand(args []string) string {
	if !b.feedbackOn() {
		return "Learning from feedback needs hooks (webhooks.hooks in config.yml)."
	}
	if len(args) == 2 && strings.EqualFold(args[0], "clear") {
		if _, ok := b.opt.Hooks[args[1]]; !ok {
			return fmt.Sprintf("There's no hook named %s.", args[1])
		}
		b.learnMu.Lock()
		err := os.Remove(b.learnedPath(args[1]))
		b.learnMu.Unlock()
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "Couldn't clear it: " + err.Error()
		}
		return fmt.Sprintf("Cleared what %s had learned.", args[1])
	}
	if len(args) > 0 {
		return "Use /learned, or /learned clear <hook>."
	}
	names := make([]string, 0, len(b.opt.Hooks))
	for name := range b.opt.Hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, name := range names {
		lines := b.learned(name)
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "%s (%d):\n%s\n\n", name, len(lines), strings.Join(lines, "\n"))
	}
	if sb.Len() == 0 {
		return "Nothing learned yet. Tell me when a 🔔 message was a false alarm (or right), e.g. by replying to it."
	}
	return strings.TrimSpace(sb.String()) + "\n\nForget a hook's with /learned clear <hook>."
}

func cut(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxFeedbackText {
		return string(r[:maxFeedbackText]) + "…"
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
