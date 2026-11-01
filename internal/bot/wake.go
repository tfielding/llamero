package bot

// Waking the Ollama machine with Wake-on-LAN when it's asleep.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// errStopped means /stop cancelled the wait; /stop has already replied.
var errStopped = errors.New("stopped")

// errWakeSend means the wake-up couldn't be sent at all.
var errWakeSend = errors.New("couldn't send wake-on-lan")

// wake sends a wake-up to the Ollama machine, tells sender, and waits until Ollama
// answers or WakeWait passes. /status shows the wait and /stop cancels it.
func (b *Bot) wake(ctx context.Context, sender string) error {
	if err := b.opt.Wake(ctx); err != nil {
		slog.Error("wake-on-lan failed", "err", err)
		return fmt.Errorf("%w: %v", errWakeSend, err)
	}
	slog.Info("wake-on-lan sent, waiting for ollama", "wait", b.opt.WakeWait)
	if err := b.msg.Send(ctx, sender, "Ollama is asleep, waking it up…", false, nil); err != nil {
		slog.Warn("send wake notice", "err", err)
	}

	start := time.Now()
	b.mu.Lock()
	waitCtx, cancel := context.WithTimeout(ctx, b.opt.WakeWait)
	defer cancel()
	b.active[sender] = activity{model: "Ollama", started: start, task: "waking up", cancel: cancel}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.active, sender)
		b.mu.Unlock()
	}()
	stopTyping := b.typing(ctx, sender)
	defer stopTyping()

	t := time.NewTicker(b.opt.WakePoll)
	defer t.Stop()
	for {
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.Canceled) && ctx.Err() == nil {
				return errStopped
			}
			slog.Warn("ollama didn't wake up in time", "waited", b.opt.WakeWait)
			return fmt.Errorf("not awake after %s", b.opt.WakeWait)
		case <-t.C:
			if b.llm.Ping(waitCtx) == nil {
				slog.Info("ollama is awake", "took", time.Since(start).Round(100*time.Millisecond))
				return nil
			}
		}
	}
}

// wakeCommand is /wake: send a wake-up without waiting.
func (b *Bot) wakeCommand(ctx context.Context) string {
	if b.opt.Wake == nil {
		return "Wake-on-LAN isn't set up (OLLAMA_WOL_MAC)."
	}
	if b.llm.Ping(ctx) == nil {
		return "Ollama is already awake."
	}
	if err := b.opt.Wake(ctx); err != nil {
		return "Couldn't send the wake-up: " + err.Error()
	}
	return "Wake-up sent. It usually takes a few seconds to a minute."
}
