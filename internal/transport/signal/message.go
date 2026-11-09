package signal

import (
	"encoding/base64"
	"log/slog"
	"strings"

	"github.com/tfielding/llamero/internal/transport"
)

// message turns an envelope into a message for the core, keeping only direct messages with
// text or files from someone else: group chats, sync messages (the bot's own outgoing
// messages), receipts and typing events are dropped.
func (c *Client) message(env Envelope) (transport.Message, bool) {
	dm := env.DataMessage
	if dm == nil {
		return transport.Message{}, false
	}
	if g := dm.GroupInfo; g != nil {
		// signal-cli-rest-api sends to a group by "group." and its internal id in base64 again.
		if id := "group." + base64.StdEncoding.EncodeToString([]byte(g.GroupID)); g.GroupID != "" && c.groups.First(id) {
			slog.Info("ignoring messages in a group chat (the bot only chats one to one); to send hook messages there, put its id in a hook's to", "group", "signal:"+id)
		}
		return transport.Message{}, false
	}
	text := strings.TrimSpace(dm.Message)
	if text == "" && len(dm.Attachments) == 0 {
		return transport.Message{}, false
	}
	sender := env.SourceNumber
	if sender == "" {
		sender = env.Source
	}
	if sender == "" || sender == c.number {
		return transport.Message{}, false
	}
	m := transport.Message{Sender: sender, Text: text, ID: dm.Timestamp}
	if m.ID == 0 {
		m.ID = env.Timestamp
	}
	if env.SourceUUID != "" {
		m.AltIDs = []string{env.SourceUUID}
	}
	if q := dm.Quote; q != nil {
		m.Quote = &transport.Quote{Text: q.Text, FromBot: c.number != "" && (q.AuthorNumber == c.number || q.Author == c.number)}
	}
	for _, a := range dm.Attachments {
		m.Files = append(m.Files, transport.File{ID: a.ID, MIME: a.ContentType, Name: a.Filename, Size: a.Size})
	}
	return m, true
}
