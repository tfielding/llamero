package signalcli

import (
	"encoding/json"
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
