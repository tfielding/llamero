package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCheck(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"llama3.2:latest"}]}`))
	}))
	defer ollama.Close()
	s := mcp.NewServer(&mcp.Implementation{Name: "ha", Version: "1"}, nil)
	for _, name := range []string{"history__get_history", "search__search"} {
		mcp.AddTool(s, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{}, nil, nil
			})
	}
	ha := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	defer ha.Close()

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yml")
	yml := "transports:\n  signal: {number: \"+15550000009\", allowed_senders: \"+15550000001\"}\n" +
		"ollama: {url: \"" + ollama.URL + "\", model: llama3.2, vision_model: gemma3:4b}\n" +
		"data_dir: " + filepath.Join(dir, "data") + "\n" +
		"knowledge: {dir: " + dir + "}\n" +
		"mcp:\n  servers:\n    ha_history:\n      url: \"" + ha.URL + "\"\n      tools: [history__get_history, search]\n"
	if err := os.WriteFile(cfg, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", cfg)
	var out strings.Builder
	if code := runCheck(&out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	for _, want := range []string{
		"✓ config: " + cfg,
		"✓ data folder " + filepath.Join(dir, "data") + " is writable",
		"✓ Ollama at " + ollama.URL + ": 1 models",
		"! model gemma3:4b (ollama.vision_model) isn't installed", // llama3.2 is, as llama3.2:latest
		"✓ MCP server ha_history: 1 tools offered: history__get_history",
		"! MCP server ha_history: tools lists search, which the server doesn't have (did you mean search__search?)",
		"The bot would start, with 2 warning(s) above.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}

	if err := os.WriteFile(cfg, []byte("transports:\n  signal:\n    numbr: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runCheck(&out); code != 1 || !strings.Contains(out.String(), `✗ config: `) || !strings.Contains(out.String(), `did you mean "number"?`) {
		t.Fatalf("bad config: exit %d:\n%s", code, out.String())
	}
}
