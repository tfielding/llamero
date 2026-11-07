package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tfielding/llamero/internal/core"
	"github.com/tfielding/llamero/internal/ollama"
	"github.com/tfielding/llamero/internal/store"
	"github.com/tfielding/llamero/internal/transport"
	"github.com/tfielding/llamero/internal/transport/telegram"
)

// TestTelegramEndToEnd runs the real Telegram client, router and core against fake Telegram and
// Ollama servers: an allowed person's message is answered in their chat, a stranger's isn't.
func TestTelegramEndToEnd(t *testing.T) {
	const token = "4242:test-token"
	var mu sync.Mutex
	var sends []map[string]any
	sent := make(chan struct{}, 4)
	polled := false

	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bot"+token+"/")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch method {
		case "getMe":
			w.Write([]byte(`{"ok":true,"result":{"id":4242,"is_bot":true,"username":"llamero_bot"}}`))
		case "getUpdates":
			mu.Lock()
			first := !polled
			polled = true
			mu.Unlock()
			if first {
				w.Write([]byte(`{"ok":true,"result":[
					{"update_id":1,"message":{"message_id":10,"from":{"id":99},"chat":{"id":99,"type":"private"},"text":"hi, I'm a stranger"}},
					{"update_id":2,"message":{"message_id":11,"from":{"id":7},"chat":{"id":7,"type":"private"},"text":"what is 2+2"}}]}`))
				return
			}
			select {
			case <-r.Context().Done():
			case <-time.After(100 * time.Millisecond):
			}
			w.Write([]byte(`{"ok":true,"result":[]}`))
		case "sendMessage":
			mu.Lock()
			sends = append(sends, body)
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":{}}`))
			sent <- struct{}{}
		default: // typing, reactions
			w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer tg.Close()

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "**4**"}})
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer llm.Close()

	client := telegram.New(tg.URL, token)
	router := transport.NewRouter()
	router.Add(telegram.Prefix, client)
	st, _ := store.New(t.TempDir())
	b := core.New(router, ollama.New(llm.URL, 5*time.Second), st, core.Options{
		Allowed: map[string]bool{"telegram:7": true}, DefaultModel: "m", HistoryLimit: 10,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- listen(ctx, router, []listener{{name: "telegram", prefix: telegram.Prefix, listen: client.Listen}}, b.Handle)
	}()
	select {
	case <-sent:
	case <-time.After(3 * time.Second):
		t.Fatal("no reply sent")
	}
	time.Sleep(100 * time.Millisecond) // nothing else should be sent (the stranger gets no answer)
	cancel()
	<-done
	b.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(sends) != 1 || sends[0]["chat_id"] != "7" || sends[0]["text"] != "<b>4</b>" || sends[0]["parse_mode"] != "HTML" {
		t.Fatalf("sends: %v", sends)
	}
	if c, _ := st.Load("telegram:7"); len(c.History) != 2 {
		t.Errorf("history not saved under telegram:7: %+v", c.History)
	}
}
