// Command signal-ollama connects a Signal number (via signal-cli-rest-api) to an Ollama server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works in the distroless image, which has no zoneinfo

	"github.com/tfielding/signal-ollama/internal/bot"
	"github.com/tfielding/signal-ollama/internal/config"
	"github.com/tfielding/signal-ollama/internal/health"
	"github.com/tfielding/signal-ollama/internal/knowledge"
	"github.com/tfielding/signal-ollama/internal/ollama"
	"github.com/tfielding/signal-ollama/internal/schedule"
	"github.com/tfielding/signal-ollama/internal/signalcli"
	"github.com/tfielding/signal-ollama/internal/store"
	"github.com/tfielding/signal-ollama/internal/toolbox"
	"github.com/tfielding/signal-ollama/internal/webhook"
	"github.com/tfielding/signal-ollama/internal/wol"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	healthcheck := flag.Bool("healthcheck", false, "check the running bot's /healthz and exit 0 if it's healthy (for Docker)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: signal-ollama [-version] [-healthcheck]\n\nConfigured with environment variables and /config/config.yml; see the README.\n")
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("signal-ollama", version)
		return
	}
	if *healthcheck {
		os.Exit(checkHealth())
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
	sc := signalcli.New(cfg.SignalURL, cfg.SignalNumber)
	var box *toolbox.Box
	if len(cfg.MCPServers) > 0 {
		box = toolbox.New(cfg.MCPServers, toolbox.Options{Allowed: cfg.MCPTools, MaxResultChars: cfg.MCPMaxChars})
		defer box.Close()
	}
	// Hooks are served from the start, and answer "starting up" until the bot is ready.
	var ready atomic.Pointer[bot.Bot]
	var hooks http.Handler
	if len(cfg.Hooks) > 0 {
		hooks = webhook.Handler(cfg.WebhookToken, func(name, text string) error {
			b := ready.Load()
			switch {
			case b == nil:
				return errors.New("starting up, try again shortly")
			case !b.Hook(ctx, name, text):
				return webhook.ErrUnknownHook
			}
			return nil
		})
	}
	// Before waiting for Ollama below, so the bot reports itself as starting, not missing.
	if err := serveHealth(ctx, cfg, sc, llm, box, hooks); err != nil {
		return err
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
	var tools bot.Tools
	if box != nil {
		names := []string{}
		for _, t := range box.Tools(ctx) {
			names = append(names, t.Name)
		}
		slog.Info("mcp tools", "servers", len(cfg.MCPServers), "tools", names)
		tools = box
	}
	know := knowledge.New(cfg.KnowledgeDir, cfg.KnowledgeRefs(), cfg.KnowledgeMaxChars)
	var schedules *schedule.Store
	if cfg.Schedules {
		if schedules, err = schedule.NewStore(filepath.Join(cfg.DataDir, "schedules")); err != nil {
			return err
		}
	}
	know.Get() // log what's there at startup
	b := bot.New(sc, llm, st, bot.Options{
		Tools:        tools,
		Knowledge:    know,
		Schedules:    schedules,
		Hooks:        botHooks(cfg.Hooks),
		OwnNumber:    cfg.SignalNumber,
		Allowed:      cfg.Allowed,
		DefaultModel: cfg.DefaultModel,
		VisionModel:  cfg.VisionModel,
		SystemPrompt: cfg.SystemPrompt,
		HistoryLimit: cfg.HistoryLimit,
		Summarize:    cfg.Summarize,
		SummaryModel: cfg.SummaryModel,
		Settings:     cfg.Describe(),
		Wake:         wake,
		WakeWait:     cfg.WakeWait,
		Reaction:     cfg.Reaction,
	})
	ready.Store(b)

	slog.Info("starting", "version", version, "config_file", cfg.ConfigFile, "schedules", cfg.Schedules, "tz", time.Local.String(), "signal", cfg.SignalURL, "ollama", cfg.OllamaURL, "model", cfg.DefaultModel, "allowed", len(cfg.Allowed), "summaries", cfg.Summarize, "wake_on_lan", cfg.WakeMAC != nil, "hooks", len(cfg.Hooks))
	b.StartScheduler(ctx)
	err = sc.Listen(ctx, b.Handle)
	b.Wait()
	if errors.Is(err, context.Canceled) {
		slog.Info("shut down")
		return nil
	}
	return err
}

// botHooks converts the configured hooks for the bot.
func botHooks(hooks []config.Hook) map[string]bot.Hook {
	if len(hooks) == 0 {
		return nil
	}
	out := map[string]bot.Hook{}
	for _, h := range hooks {
		out[h.Name] = bot.Hook{Name: h.Name, Task: h.Task, OnlyIf: h.OnlyIf, To: h.To, Wake: h.Wake, Group: h.Group}
	}
	return out
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

// serveHealth starts GET /healthz on cfg.HealthAddr (unless it's off) until ctx ends. A port
// that can't be opened is an error at startup.
func serveHealth(ctx context.Context, cfg config.Config, sc *signalcli.Client, llm *ollama.Client, box *toolbox.Box, hooks http.Handler) error {
	if cfg.HealthAddr == "" {
		return nil
	}
	checks := health.Checks{Version: version, Started: time.Now(), Signal: sc.Status, Ollama: llm.Ping}
	if box != nil {
		checks.MCP = func() map[string]error {
			errs := box.Errors()
			out := map[string]error{}
			for _, s := range cfg.MCPServers {
				out[s.Name] = errs[s.Name]
			}
			return out
		}
	}
	ln, err := net.Listen("tcp", cfg.HealthAddr)
	if err != nil {
		return fmt.Errorf("HEALTH_ADDR %s: %w", cfg.HealthAddr, err)
	}
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Handler(checks))
	if hooks != nil {
		mux.Handle("/hooks/", hooks)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("health endpoint stopped", "err", err)
		}
	}()
	slog.Info("http endpoint", "addr", cfg.HealthAddr, "hooks", hooks != nil)
	return nil
}

// checkHealth asks the running bot's /healthz (for a Docker HEALTHCHECK, since the image has no
// curl) and returns the exit code: 0 healthy, 1 not.
func checkHealth() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	if cfg.HealthAddr == "" {
		fmt.Fprintln(os.Stderr, "unhealthy: HEALTH_ADDR is off")
		return 1
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + localAddr(cfg.HealthAddr) + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Print(string(body))
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// localAddr is where to reach a listen address from the same machine: ":8092" and
// "0.0.0.0:8092" become "127.0.0.1:8092".
func localAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port)
}
