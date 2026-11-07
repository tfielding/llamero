package telegram

import (
	"strconv"
	"strings"

	"github.com/tfielding/llamero/internal/transport"
)

// update is the subset of a Bot API update we use.
type update struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int64      `json:"message_id"`
	From      *user      `json:"from"`
	Chat      chat       `json:"chat"`
	Text      string     `json:"text"`
	Caption   string     `json:"caption"`
	Photo     []photo    `json:"photo"` // the same picture in several sizes, smallest first
	Document  *file      `json:"document"`
	Voice     *file      `json:"voice"`
	Audio     *file      `json:"audio"`
	Video     *file      `json:"video"`
	Animation *file      `json:"animation"`
	Sticker   *file      `json:"sticker"`
	ReplyTo   *tgMessage `json:"reply_to_message"`
}

type user struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private, group, supergroup or channel
}

type photo struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
}

type file struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MIMEType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

// message turns an update into a message for the core, keeping only messages people send the bot
// in a private chat, with text or files: groups, channels, other bots and service messages are
// dropped.
func (c *Client) message(u update) (transport.Message, bool) {
	m := u.Message
	if m == nil || m.Chat.Type != "private" || m.From == nil || m.From.IsBot {
		return transport.Message{}, false
	}
	out := transport.Message{
		Sender: strconv.FormatInt(m.From.ID, 10),
		Text:   strings.TrimSpace(firstNonEmpty(m.Text, m.Caption)),
		ID:     m.MessageID,
	}
	if len(m.Photo) > 0 {
		p := m.Photo[len(m.Photo)-1] // the largest
		out.Files = append(out.Files, transport.File{ID: p.FileID, MIME: "image/jpeg", Size: p.FileSize})
	}
	for _, f := range []struct {
		f    *file
		mime string // when Telegram doesn't say
	}{{m.Document, "application/octet-stream"}, {m.Voice, "audio/ogg"}, {m.Audio, "audio/mpeg"},
		{m.Video, "video/mp4"}, {m.Animation, "video/mp4"}, {m.Sticker, "image/webp"}} {
		if f.f != nil {
			out.Files = append(out.Files, transport.File{ID: f.f.FileID, MIME: firstNonEmpty(f.f.MIMEType, f.mime),
				Name: f.f.FileName, Size: f.f.FileSize})
		}
	}
	if out.Text == "" && len(out.Files) == 0 {
		return transport.Message{}, false
	}
	if r := m.ReplyTo; r != nil {
		out.Quote = &transport.Quote{Text: firstNonEmpty(r.Text, r.Caption), FromBot: r.From != nil && r.From.ID == c.botID}
	}
	return out, true
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
