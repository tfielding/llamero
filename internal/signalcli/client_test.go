package signalcli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
