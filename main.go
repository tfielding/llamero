// Command signal-ollama connects a Signal number (via signal-cli-rest-api) to an Ollama server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tfielding/signal-ollama/internal/bot"
	"github.com/tfielding/signal-ollama/internal/config"
	"github.com/tfielding/signal-ollama/internal/ollama"
	"github.com/tfielding/signal-ollama/internal/signalcli"
	"github.com/tfielding/signal-ollama/internal/store"
	"github.com/tfielding/signal-ollama/internal/toolbox"
	"github.com/tfielding/signal-ollama/internal/wol"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: signal-ollama [-version]\n\nConfigured with environment variables; see the README.\n")
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("signal-ollama", version)
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	llm := ollama.New(cfg.OllamaURL, cfg.OllamaTimeout)
	var wake func(context.Context) error
	if cfg.WakeMAC != nil {
		wake = func(ctx context.Context) error { return wol.Send(ctx, cfg.WakeMAC, cfg.WakeAddr) }
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel, err = firstModel(ctx, llm, wake, 10*time.Second)
		if err != nil {
			return err
		}
	}

	st, err := store.New(cfg.DataDir)
	if err != nil {
		return err
	}
	sc := signalcli.New(cfg.SignalURL, cfg.SignalNumber)
	var tools bot.Tools
	if len(cfg.MCPServers) > 0 {
		box := toolbox.New(cfg.MCPServers, toolbox.Options{Allowed: cfg.MCPTools, AllowWrite: cfg.MCPAllowWrite, MaxResultChars: cfg.MCPMaxChars})
		defer box.Close()
		names := []string{}
		for _, t := range box.Tools(ctx) {
			names = append(names, t.Name)
		}
		slog.Info("mcp tools", "servers", len(cfg.MCPServers), "tools", names)
		tools = box
	}
	b := bot.New(sc, llm, st, bot.Options{
		Tools:         tools,
		OwnNumber:     cfg.SignalNumber,
		Allowed:       cfg.Allowed,
		DefaultModel:  cfg.DefaultModel,
		SystemPrompt:  cfg.SystemPrompt,
		HistoryLimit:  cfg.HistoryLimit,
		Summarize:     cfg.Summarize,
		SummaryModel:  cfg.SummaryModel,
		Settings:      cfg.Describe(),
		Wake:          wake,
		WakeWait:      cfg.WakeWait,
		ConfirmWrites: cfg.MCPConfirm,
	})

	slog.Info("starting", "version", version, "signal", cfg.SignalURL, "ollama", cfg.OllamaURL, "model", cfg.DefaultModel, "allowed", len(cfg.Allowed), "summaries", cfg.Summarize, "wake_on_lan", cfg.WakeMAC != nil)
	err = sc.Listen(ctx, b.Handle)
	b.Wait()
	if errors.Is(err, context.Canceled) {
		slog.Info("shut down")
		return nil
	}
	return err
}

// wakeEvery is how often firstModel repeats the wake-up while Ollama stays unreachable.
const wakeEvery = 5 * time.Minute

// firstModel waits for Ollama to be reachable and returns its first installed model.
// If wake is set (Wake-on-LAN), it wakes the Ollama machine while waiting.
func firstModel(ctx context.Context, llm interface {
	Models(context.Context) ([]string, error)
}, wake func(context.Context) error, retry time.Duration) (string, error) {
	var woke time.Time
	for {
		models, err := llm.Models(ctx)
		if err == nil && len(models) > 0 {
			slog.Info("OLLAMA_MODEL not set, using first installed model", "model", models[0])
			return models[0], nil
		}
		if err == nil {
			err = errors.New("no models installed (run: ollama pull <model>)")
		} else if wake != nil && time.Since(woke) >= wakeEvery {
			if werr := wake(ctx); werr != nil {
				slog.Error("wake-on-lan failed", "err", werr)
			} else {
				slog.Info("wake-on-lan sent while waiting for ollama")
			}
			woke = time.Now()
		}
		slog.Warn("waiting for ollama", "err", err)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(retry):
		}
	}
}
