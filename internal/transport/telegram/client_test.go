package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tfielding/llamero/internal/transport"
)

const token = "4242:SECRET-token"

// fakeAPI is a Bot API server: it records calls and answers with canned results.
type fakeAPI struct {
	mu      sync.Mutex
	calls   []call
	updates []string // getUpdates answers, one per call (JSON arrays); then it waits
	fail    map[string]string
	srv     *httptest.Server
}

type call struct {
	method string
	body   map[string]any // JSON bodies
	form   map[string]string
	file   string // uploaded file's field and name
}

func newFake(t *testing.T) *fakeAPI {
	f := &fakeAPI{fail: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/file/bot"+token+"/") {
		w.Write([]byte("jpeg bytes"))
		return
	}
	method, ok := strings.CutPrefix(r.URL.Path, "/bot"+token+"/")
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
		return
	}
	c := call{method: method}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.ParseMultipartForm(1 << 20)
		c.form = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			c.form[k] = v[0]
		}
		for k, v := range r.MultipartForm.File {
			c.file = k + " " + v[0].Filename
		}
	} else {
		json.NewDecoder(r.Body).Decode(&c.body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	desc, failing := f.fail[method]
	if failing && method == "sendMessage" && c.body["parse_mode"] == nil {
		failing = false // only formatted sends fail
	}
	var next string
	if method == "getUpdates" && len(f.updates) > 0 {
		next, f.updates = f.updates[0], f.updates[1:]
	}
	f.mu.Unlock()

	switch {
	case failing:
		w.Write([]byte(`{"ok":false,"error_code":400,"description":"` + desc + `"}`))
	case method == "getMe":
		w.Write([]byte(`{"ok":true,"result":{"id":4242,"is_bot":true,"username":"llamero_bot"}}`))
	case method == "getUpdates" && next != "":
		w.Write([]byte(`{"ok":true,"result":` + next + `}`))
	case method == "getUpdates":
		select { // long poll: nothing new
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
		w.Write([]byte(`{"ok":true,"result":[]}`))
	case method == "getFile":
		w.Write([]byte(`{"ok":true,"result":{"file_id":"f1","file_path":"photos/f1.jpg","file_size":10}}`))
	default:
		w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

func (f *fakeAPI) called(method string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func TestListen(t *testing.T) {
	f := newFake(t)
	f.updates = []string{`[
		{"update_id": 10, "message": {"message_id": 1, "from": {"id": 7}, "chat": {"id": 7, "type": "private"}, "text": "  hello  "}},
		{"update_id": 11, "message": {"message_id": 2, "from": {"id": 7}, "chat": {"id": -99, "type": "group"}, "text": "in a group"}},
		{"update_id": 12, "message": {"message_id": 3, "from": {"id": 8, "is_bot": true}, "chat": {"id": 8, "type": "private"}, "text": "a bot"}},
		{"update_id": 13, "message": {"message_id": 4, "from": {"id": 7}, "chat": {"id": 7, "type": "private"}}},
		{"update_id": 14},
		{"update_id": 15, "message": {"message_id": 5, "from": {"id": 7}, "chat": {"id": 7, "type": "private"}, "caption": "what's this?",
			"photo": [{"file_id": "small", "file_size": 1}, {"file_id": "big", "file_size": 99}],
			"reply_to_message": {"message_id": 4, "from": {"id": 4242, "is_bot": true}, "text": "an earlier reply"}}},
		{"update_id": 16, "message": {"message_id": 6, "from": {"id": 7}, "chat": {"id": 7, "type": "private"}, "text": "and this",
			"document": {"file_id": "d1", "file_name": "invoice.pdf", "mime_type": "application/pdf", "file_size": 5},
			"reply_to_message": {"message_id": 5, "from": {"id": 7}, "caption": "mine"}}}
	]`}
	c := New(f.srv.URL, token)
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var got []transport.Message
	done := make(chan error)
	go func() {
		done <- c.Listen(ctx, func(_ context.Context, m transport.Message) {
			mu.Lock()
			got = append(got, m)
			mu.Unlock()
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		polls := len(f.called("getUpdates"))
		if n >= 3 && polls >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ok, since, _ := c.Status(); !ok || since.IsZero() {
		t.Error("not connected")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Listen: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d messages: %+v", len(got), got)
	}
	if m := got[0]; m.Sender != "7" || m.Text != "hello" || m.ID != 1 || m.Quote != nil || len(m.Files) != 0 {
		t.Errorf("text: %+v", m)
	}
	if m := got[1]; m.Text != "what's this?" || len(m.Files) != 1 || m.Files[0].ID != "big" || m.Files[0].MIME != "image/jpeg" ||
		m.Quote == nil || !m.Quote.FromBot || m.Quote.Text != "an earlier reply" {
		t.Errorf("photo: %+v", m)
	}
	if m := got[2]; len(m.Files) != 1 || m.Files[0].Name != "invoice.pdf" || m.Files[0].MIME != "application/pdf" || m.Quote.FromBot || m.Quote.Text != "mine" {
		t.Errorf("document: %+v", m)
	}
	// The next poll asks for updates after the last one seen.
	if polls := f.called("getUpdates"); polls[1].body["offset"] != float64(17) || polls[0].body["timeout"] != float64(pollSeconds) {
		t.Errorf("polls: %+v", polls)
	}
}

func TestBadToken(t *testing.T) {
	f := newFake(t)
	c := New(f.srv.URL, "1:wrong")
	err := c.Listen(context.Background(), func(context.Context, transport.Message) {})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if _, _, last := c.Status(); !errors.Is(last, ErrUnauthorized) {
		t.Errorf("status: %v", last)
	}
}

func TestSend(t *testing.T) {
	f := newFake(t)
	c := New(f.srv.URL, token)
	ctx := context.Background()

	// Markdown goes as HTML; plain text as it is.
	if err := c.Send(ctx, "7", "**hi** <there>", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, "7", "**as is**", false, nil); err != nil {
		t.Fatal(err)
	}
	sends := f.called("sendMessage")
	if len(sends) != 2 || sends[0].body["parse_mode"] != "HTML" || sends[0].body["text"] != "<b>hi</b> &lt;there&gt;" ||
		sends[0].body["chat_id"] != "7" || sends[1].body["parse_mode"] != nil || sends[1].body["text"] != "**as is**" {
		t.Fatalf("%+v", sends)
	}

	// Telegram rejects the HTML: the raw text goes out plain.
	f.mu.Lock()
	f.calls, f.fail["sendMessage"] = nil, "Bad Request: can't parse entities"
	f.mu.Unlock()
	if err := c.Send(ctx, "7", "**hi**", true, nil); err != nil {
		t.Fatal(err)
	}
	if sends = f.called("sendMessage"); len(sends) != 2 || sends[1].body["text"] != "**hi**" || sends[1].body["parse_mode"] != nil {
		t.Fatalf("fallback: %+v", sends)
	}
	delete(f.fail, "sendMessage")

	// Long text is split; pictures follow the text, each the right way.
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	long := strings.Repeat("a line of text\n", 400) // 6000 characters
	err := c.Send(ctx, "7", long, false, []transport.Attachment{
		{MIME: "image/jpeg", Name: "image-1.jpg", Data: []byte("j")},
		{MIME: "image/gif", Name: "image-2.gif", Data: []byte("g")},
		{MIME: "application/pdf", Name: "a.pdf", Data: []byte("p")},
	})
	if err != nil {
		t.Fatal(err)
	}
	sends = f.called("sendMessage")
	if len(sends) != 2 || len(sends[0].body["text"].(string)) > maxText || !strings.HasSuffix(sends[1].body["text"].(string), "a line of text") {
		t.Errorf("split into %d", len(sends))
	}
	photo, anim, doc := f.called("sendPhoto"), f.called("sendAnimation"), f.called("sendDocument")
	if len(photo) != 1 || photo[0].file != "photo image-1.jpg" || photo[0].form["chat_id"] != "7" ||
		len(anim) != 1 || anim[0].file != "animation image-2.gif" || len(doc) != 1 || doc[0].file != "document a.pdf" {
		t.Errorf("uploads: %+v %+v %+v", photo, anim, doc)
	}
}

func TestReactTypingAttachment(t *testing.T) {
	f := newFake(t)
	c := New(f.srv.URL, token)
	ctx := context.Background()

	c.React(ctx, "7", 5, "👀", false)
	c.React(ctx, "7", 5, "👀", true)
	r := f.called("setMessageReaction")
	if len(r) != 2 || r[0].body["message_id"] != float64(5) || r[0].body["chat_id"] != "7" ||
		len(r[0].body["reaction"].([]any)) != 1 || len(r[1].body["reaction"].([]any)) != 0 {
		t.Errorf("reactions: %+v", r)
	}

	if err := c.Typing(ctx, "7", true); err != nil {
		t.Fatal(err)
	}
	c.Typing(ctx, "7", true) // renewed: still one indicator
	c.Typing(ctx, "7", false)
	c.mu.Lock()
	running := len(c.typing)
	c.mu.Unlock()
	if a := f.called("sendChatAction"); len(a) != 2 || a[0].body["action"] != "typing" || running != 0 {
		t.Errorf("typing: %d calls, %d running", len(a), running)
	}

	data, err := c.Attachment(ctx, "f1")
	if err != nil || string(data) != "jpeg bytes" {
		t.Errorf("attachment: %q %v", data, err)
	}
	if g := f.called("getFile"); len(g) != 1 || g[0].body["file_id"] != "f1" {
		t.Errorf("getFile: %+v", g)
	}
}

func TestTokenNotInErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing listening
	c := New(srv.URL, token)
	err := c.Send(context.Background(), "7", "hi", false, nil)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("got %v", err)
	}
	_, err = c.Attachment(context.Background(), "f1")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("attachment: %v", err)
	}
}

func TestSplit(t *testing.T) {
	if got := split("  ", 10); got != nil {
		t.Errorf("blank: %q", got)
	}
	if got := split("short", 10); len(got) != 1 || got[0] != "short" {
		t.Errorf("short: %q", got)
	}
	got := split("para one here\n\npara two is here", 20)
	if len(got) != 2 || got[0] != "para one here" || got[1] != "para two is here" {
		t.Errorf("paragraphs: %q", got)
	}
	got = split(strings.Repeat("é", 25), 10) // no line breaks: cut by characters, not bytes
	if len(got) != 3 || got[0] != strings.Repeat("é", 10) {
		t.Errorf("runes: %q", got)
	}
}
