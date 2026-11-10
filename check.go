package main

// -check: load the configuration and try what the bot will connect to, without starting it, so a
// mistake shows up before a restart instead of as a bot that won't start.

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/tfielding/llamero/internal/config"
	"github.com/tfielding/llamero/internal/core"
	"github.com/tfielding/llamero/internal/ollama"
	"github.com/tfielding/llamero/internal/toolbox"
)

// checkTimeout bounds the whole check, for servers that don't answer.
const checkTimeout = 30 * time.Second

// runCheck prints a report to out and returns the exit code: 1 if the bot couldn't start
// (the config is invalid, or the data folder isn't writable), 0 otherwise. Services that don't
// answer are warnings: the bot copes with them while it runs.
func runCheck(out io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(out, "✗ config: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "✓ config: %s\n", cmp(cfg.ConfigFile, "environment variables only"))
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	warnings := 0
	warn := func(format string, args ...any) {
		warnings++
		fmt.Fprintf(out, "! "+format+"\n", args...)
	}

	failed := false
	if err := writable(cfg.DataDir); err != nil {
		fmt.Fprintf(out, "✗ data folder %s: %v\n", cfg.DataDir, err)
		failed = true
	} else {
		fmt.Fprintf(out, "✓ data folder %s is writable\n", cfg.DataDir)
	}

	// Ollama: each server, and the models the settings name.
	installed := map[string]bool{}
	reached := 0
	for _, u := range slices.DeleteFunc([]string{cfg.OllamaURL, cfg.OllamaBackupURL}, func(s string) bool { return s == "" }) {
		models, err := ollama.New(u, 10*time.Second).Models(ctx)
		if err != nil {
			warn("Ollama at %s: %s", u, oneLine(err))
			continue
		}
		reached++
		for _, m := range models {
			installed[m] = true
		}
		fmt.Fprintf(out, "✓ Ollama at %s: %d models\n", u, len(models))
	}
	if reached > 0 {
		for _, m := range neededModels(cfg) {
			if !installed[m.name] && !installed[m.name+":latest"] {
				warn("model %s (%s) isn't installed", m.name, m.where)
			}
		}
	}

	// MCP servers: what each offers, and names in the settings that match nothing.
	var tools []toolbox.Tool
	if len(cfg.MCPServers) > 0 {
		box := toolbox.New(cfg.MCPServers, toolbox.Options{Allowed: cfg.MCPTools, MaxResultChars: cfg.MCPMaxChars})
		defer box.Close()
		tools = box.Tools(ctx)
		errs := box.Errors()
		for _, s := range cfg.MCPServers {
			if err, down := errs[s.Name]; down {
				warn("MCP server %s: %s", s.Name, oneLine(err))
				continue
			}
			var names []string
			for _, t := range tools {
				if t.Server() == s.Name {
					names = append(names, t.Name)
				}
			}
			fmt.Fprintf(out, "✓ MCP server %s: %d tools offered: %s\n", s.Name, len(names), cmp(strings.Join(names, ", "), "none"))
		}
		for _, p := range box.Problems() {
			warn("MCP server %s", p)
		}
	}

	// Hooks: tools their checks can't use.
	if len(cfg.Hooks) > 0 {
		fmt.Fprintf(out, "✓ hooks: %d\n", len(cfg.Hooks))
		problems := core.HookToolProblems(botHooks(cfg.Hooks), tools)
		for _, h := range cfg.Hooks {
			if p, ok := problems[h.Name]; ok {
				warn("hook %s: tools its checks can't use: %s", h.Name, strings.Join(p, "; "))
			}
		}
	}

	switch {
	case failed:
		fmt.Fprintln(out, "\nThe bot wouldn't start: fix the ✗ above.")
		return 1
	case warnings > 0:
		fmt.Fprintf(out, "\nThe bot would start, with %d warning(s) above.\n", warnings)
	default:
		fmt.Fprintln(out, "\nAll good.")
	}
	return 0
}

type neededModel struct{ name, where string }

// neededModels are the models the settings name, with where.
func neededModels(cfg config.Config) []neededModel {
	var out []neededModel
	add := func(name, where string) {
		if name != "" {
			out = append(out, neededModel{name, where})
		}
	}
	if !cfg.DefaultModelAuto {
		add(cfg.DefaultModel, "ollama.model")
	}
	add(cfg.VisionModel, "ollama.vision_model")
	add(cfg.SummaryModel, "history.summary_model")
	for _, h := range cfg.Hooks {
		add(h.Model, "webhooks.hooks."+h.Name+".model")
	}
	return out
}

// writable checks that the bot can create files in dir (creating it if needed, as it would).
func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".check-*")
	if err != nil {
		return fmt.Errorf("%w (the bot runs as uid %d)", err, os.Getuid())
	}
	f.Close()
	return os.Remove(f.Name())
}

func cmp(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}

func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
