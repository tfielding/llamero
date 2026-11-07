package signal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

const (
	botNum = "+15550000009"
	me     = "+15550000001"
)

func TestMessageKeepsOnlyDirectMessages(t *testing.T) {
	c := New("http://unused", botNum)
	dm := func(text string) Envelope {
		return Envelope{SourceNumber: me, Source: me, DataMessage: &DataMessage{Message: text}}
	}
	group := dm("hi in a group chat")
	group.DataMessage.GroupInfo = &GroupInfo{GroupID: "abc"}
	own := dm("outgoing alert")
	own.SourceNumber, own.Source = botNum, botNum
	for name, env := range map[string]Envelope{
		"group":   group,
		"own":     own,
		"sync":    {SourceNumber: botNum, SyncMessage: []byte(`{"sentMessage":{"message":"alert"}}`)},
		"receipt": {SourceNumber: me}, // or typing: no dataMessage
		"empty":   dm("   "),
		"nobody":  {DataMessage: &DataMessage{Message: "hi"}},
	} {
		if m, ok := c.message(env); ok {
			t.Errorf("%s: kept %+v", name, m)
		}
	}

	// A reply to the bot, with a photo, from someone with an account id.
	env := dm("  what's this?  ")
	env.SourceUUID, env.Timestamp = "uuid-123", 1730000000400
	env.DataMessage.Timestamp = 1730000000500
	env.DataMessage.Quote = &Quote{AuthorNumber: botNum, Text: "An earlier reply"}
	env.DataMessage.Attachments = []ReceivedAttachment{{ID: "p1.jpg", ContentType: "image/jpeg", Filename: "IMG_1.jpg", Size: 10}}
	m, ok := c.message(env)
	if !ok || m.Sender != me || m.Text != "what's this?" || m.ID != 1730000000500 || len(m.AltIDs) != 1 || m.AltIDs[0] != "uuid-123" {
		t.Fatalf("%+v", m)
	}
	if m.Quote == nil || !m.Quote.FromBot || m.Quote.Text != "An earlier reply" {
		t.Errorf("quote: %+v", m.Quote)
	}
	if len(m.Files) != 1 || m.Files[0].ID != "p1.jpg" || m.Files[0].MIME != "image/jpeg" || m.Files[0].Name != "IMG_1.jpg" || m.Files[0].Size != 10 {
		t.Errorf("files: %+v", m.Files)
	}

	// Quoting their own message; Source when there's no number; the envelope's timestamp.
	env = Envelope{Source: me, Timestamp: 1730000000600, DataMessage: &DataMessage{Message: "and this?", Quote: &Quote{Author: me, Text: "q"}}}
	if m, ok = c.message(env); !ok || m.Sender != me || m.ID != 1730000000600 || m.Quote.FromBot || m.AltIDs != nil {
		t.Errorf("%+v", m)
	}
}

func TestSendMarkdown(t *testing.T) {
	var mu sync.Mutex
	var sends []map[string]any
	failStyled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		sends = append(sends, body)
		if failStyled && body["text_mode"] == "styled" {
			http.Error(w, "bad formatting", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := New(srv.URL, botNum)
	ctx := context.Background()

	// Markdown is converted and sent styled; plain text is sent as it is.
	if err := c.Send(ctx, me, "## Title", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, me, "**not bold**", false, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(sends) != 2 || sends[0]["text_mode"] != "styled" || sends[0]["message"] != ToSignal("## Title") ||
		sends[1]["text_mode"] != "normal" || sends[1]["message"] != "**not bold**" {
		t.Fatalf("%v", sends)
	}
	sends = nil
	failStyled = true
	mu.Unlock()

	// When the styled send fails, the raw text goes out plain.
	if err := c.Send(ctx, me, "## Title", true, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sends) != 2 || sends[1]["text_mode"] != "normal" || sends[1]["message"] != "## Title" {
		t.Fatalf("%v", sends)
	}
}
