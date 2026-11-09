package transport

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fake records what the router passes to a transport.
type fake struct {
	name  string
	calls []string
}

func (f *fake) Send(_ context.Context, to, text string, markdown bool, att []Attachment) error {
	f.calls = append(f.calls, fmt.Sprintf("send %s %q md=%v files=%d", to, text, markdown, len(att)))
	return nil
}
func (f *fake) Typing(_ context.Context, to string, on bool) error {
	f.calls = append(f.calls, fmt.Sprintf("typing %s %v", to, on))
	return nil
}
func (f *fake) React(_ context.Context, to string, id int64, emoji string, remove bool) error {
	f.calls = append(f.calls, fmt.Sprintf("react %s %d %s %v", to, id, emoji, remove))
	return nil
}
func (f *fake) Attachment(_ context.Context, id string) ([]byte, error) {
	f.calls = append(f.calls, "file "+id)
	return []byte(f.name + ":" + id), nil
}

func TestRouterRoutesByPrefix(t *testing.T) {
	sig, tg := &fake{name: "signal"}, &fake{name: "telegram"}
	r := NewRouter()
	if err := r.Add("", sig); err != nil {
		t.Fatal(err)
	}
	if err := r.Add("telegram", tg); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Unprefixed ids (and ids with a colon that isn't a known prefix) go to the default transport.
	r.Send(ctx, "+15550000001", "hi", true, []Attachment{{MIME: "image/jpeg"}})
	r.Typing(ctx, "+15550000001", true)
	r.React(ctx, "uuid:with:colons", 42, "👀", false)
	// Prefixed ones go to their transport, without the prefix.
	r.Send(ctx, "telegram:12345", "hello", false, nil)
	r.React(ctx, "telegram:12345", 7, "👀", true)
	data, err := r.Attachment(ctx, "telegram:file-1")
	if err != nil || string(data) != "telegram:file-1" {
		t.Fatalf("attachment: %q %v", data, err)
	}

	if got := strings.Join(sig.calls, "\n"); got != "send +15550000001 \"hi\" md=true files=1\ntyping +15550000001 true\nreact uuid:with:colons 42 👀 false" {
		t.Errorf("signal got:\n%s", got)
	}
	if got := strings.Join(tg.calls, "\n"); got != "send 12345 \"hello\" md=false files=0\nreact 12345 7 👀 true\nfile file-1" {
		t.Errorf("telegram got:\n%s", got)
	}
}

func TestRouterInbound(t *testing.T) {
	r := NewRouter()
	r.Add("", &fake{})
	r.Add("telegram", &fake{})
	var got Message
	handle := func(_ context.Context, m Message) { got = m }

	// The default transport's messages reach the core as they are.
	in := Message{Sender: "+15550000001", AltIDs: []string{"uuid-1"}, Files: []File{{ID: "p1"}}}
	r.Inbound("", handle)(context.Background(), in)
	if got.Sender != "+15550000001" || got.AltIDs[0] != "uuid-1" || got.Files[0].ID != "p1" {
		t.Errorf("default: %+v", got)
	}

	// A prefixed transport's ids get the prefix, without changing the transport's message.
	in = Message{Sender: "12345", AltIDs: []string{"someone"}, Text: "hi", ID: 9, Files: []File{{ID: "f1", MIME: "image/jpeg"}}}
	r.Inbound("telegram", handle)(context.Background(), in)
	if got.Sender != "telegram:12345" || got.AltIDs[0] != "telegram:someone" || got.Files[0].ID != "telegram:f1" ||
		got.Files[0].MIME != "image/jpeg" || got.Text != "hi" || got.ID != 9 {
		t.Errorf("prefixed: %+v", got)
	}
	if in.Files[0].ID != "f1" || in.AltIDs[0] != "someone" {
		t.Errorf("transport's message changed: %+v", in)
	}
}

func TestRouterErrors(t *testing.T) {
	r := NewRouter()
	for _, bad := range []string{"Telegram", "tele:gram", "tele gram"} {
		if err := r.Add(bad, &fake{}); err == nil {
			t.Errorf("prefix %q accepted", bad)
		}
	}
	r.Add("telegram", &fake{})
	if err := r.Add("telegram", &fake{}); err == nil {
		t.Error("prefix used twice")
	}
	// No default transport: unprefixed ids have nowhere to go.
	if err := r.Send(context.Background(), "+15550000001", "hi", false, nil); err == nil || !strings.Contains(err.Error(), "no transport") {
		t.Errorf("got %v", err)
	}
}

func TestGroupLog(t *testing.T) {
	var g GroupLog
	if !g.First("group.a") || g.First("group.a") || !g.First("group.b") {
		t.Error("each id should be first once")
	}
}
