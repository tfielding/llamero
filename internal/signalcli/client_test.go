package signalcli

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseQuote(t *testing.T) {
	// A reply as signal-cli-rest-api (json-rpc mode) sends it on the receive websocket.
	raw := `{"envelope":{"source":"+15550000001","sourceNumber":"+15550000001","timestamp":1730000000500,
		"dataMessage":{"timestamp":1730000000500,"message":"explain that",
		"quote":{"id":1730000000000,"author":"+15550000009","authorNumber":"+15550000009",
		"authorUuid":"abc-123","text":"An earlier reply","attachments":[]}}}}`
	var msg struct {
		Envelope Envelope `json:"envelope"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	q := msg.Envelope.DataMessage.Quote
	if q == nil || q.ID != 1730000000000 || q.AuthorNumber != "+15550000009" || q.AuthorUUID != "abc-123" || q.Text != "An earlier reply" {
		t.Fatalf("quote: %+v", q)
	}
}

func TestReact(t *testing.T) {
	type req struct {
		method, path string
		body         map[string]any
	}
	var got []req
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, req{r.Method, r.URL.Path, body})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "+15550000009")
	if err := c.React(context.Background(), "+15550000001", "+15550000001", 1730000000500, "👀", false); err != nil {
		t.Fatal(err)
	}
	if err := c.React(context.Background(), "+15550000001", "+15550000001", 1730000000500, "👀", true); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].method != "POST" || got[1].method != "DELETE" || got[0].path != "/v1/reactions/+15550000009" {
		t.Fatalf("requests: %+v", got)
	}
	b := got[0].body
	if b["recipient"] != "+15550000001" || b["target_author"] != "+15550000001" || b["reaction"] != "👀" || b["timestamp"] != float64(1730000000500) {
		t.Fatalf("body: %v", b)
	}
}

// waitFor polls cond until it's true or a second has passed.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStatusFollowsTheConnection(t *testing.T) {
	var mu sync.Mutex
	var conns []*websocket.Conn
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		conns = append(conns, conn)
		mu.Unlock()
		conn.Read(r.Context()) // answers pings until the connection closes
	}))
	defer srv.Close()

	c := New(srv.URL, "+15550000009")
	if ok, _, _ := c.Status(); ok {
		t.Fatal("connected before Listen")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Listen(ctx, func(context.Context, Envelope) {})
	waitFor(t, "connected", func() bool { ok, since, _ := c.Status(); return ok && !since.IsZero() })

	// The server drops the connection: not connected, with the reason; then it reconnects.
	mu.Lock()
	conns[0].Close(websocket.StatusGoingAway, "restarting")
	mu.Unlock()
	waitFor(t, "disconnected", func() bool { ok, _, err := c.Status(); return !ok && err != nil })
	waitFor(t, "reconnected", func() bool { ok, _, _ := c.Status(); return ok })
}

func TestDeadConnectionIsNoticed(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	var accepted atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := websocket.Accept(w, r, nil); err != nil {
			return
		}
		accepted.Add(1)
		<-block // never reads, so pings go unanswered: like a connection that died silently
	}))
	defer srv.Close()

	c := New(srv.URL, "+15550000009")
	c.pingEvery, c.pingTimeout = 20*time.Millisecond, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Listen(ctx, func(context.Context, Envelope) {})
	waitFor(t, "connected", func() bool { ok, _, _ := c.Status(); return ok })
	waitFor(t, "dead connection noticed", func() bool { ok, _, err := c.Status(); return !ok && err != nil })
	// Backoff starts at 1s before reconnecting.
	waitFor(t, "a new connection", func() bool { return accepted.Load() >= 2 })
}

func TestAttachments(t *testing.T) {
	// A photo as signal-cli-rest-api (json-rpc mode) sends it on the receive websocket.
	raw := `{"envelope":{"sourceNumber":"+15550000001","timestamp":1730000000500,"dataMessage":{"timestamp":1730000000500,
		"message":null,"attachments":[{"contentType":"image/jpeg","filename":"IMG_0001.jpg","id":"Abc123.jpg","size":204800,
		"width":4032,"height":3024,"caption":null,"uploadTimestamp":1730000000400}]}}}`
	var msg struct {
		Envelope Envelope `json:"envelope"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	atts := msg.Envelope.DataMessage.Attachments
	if len(atts) != 1 || atts[0].ID != "Abc123.jpg" || atts[0].ContentType != "image/jpeg" || atts[0].Size != 204800 {
		t.Fatalf("%+v", atts)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/attachments/Abc123.jpg":
			w.Write([]byte("jpeg bytes"))
		case "/v1/attachments/huge.jpg":
			w.Write(make([]byte, MaxAttachmentBytes+1))
		default:
			http.Error(w, `{"error":"attachment not found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "+15550000009")
	if data, err := c.Attachment(context.Background(), "Abc123.jpg"); err != nil || string(data) != "jpeg bytes" {
		t.Fatalf("%q %v", data, err)
	}
	if _, err := c.Attachment(context.Background(), "gone.jpg"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing: %v", err)
	}
	if _, err := c.Attachment(context.Background(), "huge.jpg"); err == nil || !strings.Contains(err.Error(), "larger than 20 MB") {
		t.Errorf("huge: %v", err)
	}
}
