// Package telegram is the Telegram transport. It talks to Telegram's Bot API directly (no extra
// container), receiving messages by long polling, so it needs no public URL.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tfielding/llamero/internal/transport"
)

// Prefix is what Telegram people's ids start with in the core: "telegram:12345".
const Prefix = "telegram"

// DefaultURL is Telegram's Bot API.
const DefaultURL = "https://api.telegram.org"

const (
	pollSeconds        = 50       // how long a getUpdates call waits for messages
	maxText            = 4000     // Telegram allows 4096 characters per message; longer replies are split
	MaxAttachmentBytes = 20 << 20 // the Bot API serves files up to 20 MB
	typingEvery        = 4 * time.Second
	typingFor          = 30 * time.Second // typing stops by itself if it isn't renewed (or turned off)
)

// ErrUnauthorized means Telegram rejected the bot token.
var ErrUnauthorized = errors.New("telegram rejected the bot token (check it with @BotFather)")

type Client struct {
	base  string // e.g. https://api.telegram.org
	token string
	botID int64 // from the token, which starts with the bot's id
	http  *http.Client

	mu        sync.Mutex
	connected bool
	since     time.Time
	lastErr   error
	typing    map[string]chan struct{} // chats with a typing indicator running, closed to renew
	groups    transport.GroupLog
}

// New returns a client for the bot with token, at base (DefaultURL, or a self-hosted Bot API
// server).
func New(base, token string) *Client {
	id, _ := strconv.ParseInt(strings.SplitN(token, ":", 2)[0], 10, 64)
	return &Client{base: strings.TrimRight(base, "/"), token: token, botID: id,
		http: &http.Client{Timeout: (pollSeconds + 20) * time.Second}, typing: map[string]chan struct{}{}}
}

// Status reports whether polling is working, since when, and why it last failed.
func (c *Client) Status() (connected bool, since time.Time, lastErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected, c.since, c.lastErr
}

func (c *Client) setStatus(connected bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if connected && !c.connected {
		c.since = time.Now()
	}
	c.connected, c.lastErr = connected, err
}

// Listen polls Telegram for messages and calls handle for every direct message from a person
// (see message), until ctx is cancelled. Network trouble is retried with backoff; a rejected
// token is returned as ErrUnauthorized, since retrying can't fix it.
func (c *Client) Listen(ctx context.Context, handle func(context.Context, transport.Message)) error {
	var me struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	backoff := time.Second
	for {
		err := c.call(ctx, "getMe", nil, &me)
		if err == nil {
			break
		}
		c.setStatus(false, err)
		if errors.Is(err, ErrUnauthorized) || ctx.Err() != nil {
			return err
		}
		slog.Warn("telegram unreachable, retrying", "err", err, "in", backoff)
		if !sleep(ctx, backoff) {
			return ctx.Err()
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	c.botID = me.ID
	slog.Info("connected to telegram", "bot", "@"+me.Username)

	offset := int64(0)
	backoff = time.Second
	for {
		var updates []update
		err := c.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": pollSeconds,
			"allowed_updates": []string{"message"}}, &updates)
		if ctx.Err() != nil {
			c.setStatus(false, ctx.Err())
			return ctx.Err()
		}
		if err != nil {
			c.setStatus(false, err)
			if errors.Is(err, ErrUnauthorized) {
				return err
			}
			slog.Warn("telegram polling failed, retrying", "err", err, "in", backoff)
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		c.setStatus(true, nil)
		backoff = time.Second
		for _, u := range updates {
			offset = max(offset, u.UpdateID+1)
			if m, ok := c.message(u); ok {
				handle(ctx, m)
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// Send sends text, then any attachments, to the chat with recipient (a user id). With markdown
// set, text is converted to Telegram's HTML; if Telegram rejects that, the raw text is sent
// instead. Text past Telegram's limit is split into several messages.
func (c *Client) Send(ctx context.Context, recipient, text string, markdown bool, attachments []transport.Attachment) error {
	for _, part := range split(text, maxText) {
		if err := c.sendText(ctx, recipient, part, markdown); err != nil {
			return err
		}
	}
	for _, a := range attachments {
		method, field := "sendDocument", "document"
		switch a.MIME {
		case "image/jpeg", "image/png", "image/webp":
			method, field = "sendPhoto", "photo"
		case "image/gif":
			method, field = "sendAnimation", "animation"
		}
		if err := c.upload(ctx, method, field, recipient, a); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) sendText(ctx context.Context, chat, text string, markdown bool) error {
	noPreview := map[string]bool{"is_disabled": true}
	if markdown {
		err := c.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": ToHTML(text), "parse_mode": "HTML",
			"link_preview_options": noPreview}, nil)
		if err == nil || errors.Is(err, ErrUnauthorized) || ctx.Err() != nil {
			return err
		}
		slog.Warn("formatted send failed, retrying as plain text", "err", err)
	}
	return c.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text, "link_preview_options": noPreview}, nil)
}

// split cuts text into parts of at most n characters, at blank lines or line ends if it can.
func split(text string, n int) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var parts []string
	for len([]rune(text)) > n {
		r := []rune(text)
		cut := string(r[:n])
		at := strings.LastIndex(cut, "\n\n")
		if at < n/2 {
			at = strings.LastIndex(cut, "\n")
		}
		if at < n/2 {
			at = len(cut)
		}
		parts = append(parts, strings.TrimSpace(text[:at]))
		text = strings.TrimSpace(text[at:])
	}
	if text != "" {
		parts = append(parts, text)
	}
	return parts
}

// Typing shows "typing…" in the chat with recipient until it's turned off. Telegram shows it for
// about five seconds per request, so it's renewed until then (or for typingFor at most, in case
// it's never turned off).
func (c *Client) Typing(ctx context.Context, recipient string, on bool) error {
	c.mu.Lock()
	renew, running := c.typing[recipient]
	if !on {
		if running {
			delete(c.typing, recipient)
			close(renew)
		}
		c.mu.Unlock()
		return nil
	}
	if running { // renew the running one
		close(renew)
	}
	renew = make(chan struct{})
	c.typing[recipient] = renew
	c.mu.Unlock()
	if !running {
		go c.keepTyping(context.WithoutCancel(ctx), recipient)
	}
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": recipient, "action": "typing"}, nil)
}

func (c *Client) keepTyping(ctx context.Context, chat string) {
	until := time.Now().Add(typingFor)
	t := time.NewTicker(typingEvery)
	defer t.Stop()
	for {
		c.mu.Lock()
		renew, on := c.typing[chat]
		c.mu.Unlock()
		if !on {
			return
		}
		select {
		case <-renew: // renewed or turned off: look again
			c.mu.Lock()
			_, on = c.typing[chat]
			c.mu.Unlock()
			if !on {
				return
			}
			until = time.Now().Add(typingFor)
		case <-t.C:
			if time.Now().After(until) {
				c.mu.Lock()
				stale := c.typing[chat] == renew // not renewed meanwhile
				if stale {
					delete(c.typing, chat)
				}
				c.mu.Unlock()
				if stale {
					return
				}
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = c.call(cctx, "sendChatAction", map[string]any{"chat_id": chat, "action": "typing"}, nil)
			cancel()
		}
	}
}

// React adds emoji as a reaction to message id in the chat with recipient, or removes the bot's
// reaction.
func (c *Client) React(ctx context.Context, recipient string, id int64, emoji string, remove bool) error {
	reaction := []map[string]string{{"type": "emoji", "emoji": emoji}}
	if remove {
		reaction = []map[string]string{}
	}
	return c.call(ctx, "setMessageReaction", map[string]any{"chat_id": recipient, "message_id": id, "reaction": reaction}, nil)
}

// Attachment downloads a received file by its id.
func (c *Client) Attachment(ctx context.Context, id string) ([]byte, error) {
	var file struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := c.call(ctx, "getFile", map[string]string{"file_id": id}, &file); err != nil {
		return nil, err
	}
	if file.FileSize > MaxAttachmentBytes {
		return nil, fmt.Errorf("file is larger than %d MB", MaxAttachmentBytes>>20)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/file/bot"+c.token+"/"+file.FilePath, nil)
	if err != nil {
		return nil, c.redact(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.redact(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("file download: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxAttachmentBytes+1))
	if err != nil {
		return nil, c.redact(err)
	}
	if len(data) > MaxAttachmentBytes {
		return nil, fmt.Errorf("file is larger than %d MB", MaxAttachmentBytes>>20)
	}
	return data, nil
}

// upload sends a file with method (sendPhoto, sendAnimation or sendDocument).
func (c *Client) upload(ctx context.Context, method, field, chat string, a transport.Attachment) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("chat_id", chat)
	name := a.Name
	if name == "" {
		name = "file"
	}
	part, err := w.CreateFormFile(field, name)
	if err != nil {
		return err
	}
	part.Write(a.Data)
	if err := w.Close(); err != nil {
		return err
	}
	return c.post(ctx, method, w.FormDataContentType(), &buf, nil)
}

// call calls a Bot API method with a JSON body, decoding the result into out (if not nil).
func (c *Client) call(ctx context.Context, method string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if body == nil {
		b = []byte("{}")
	}
	return c.post(ctx, method, "application/json", bytes.NewReader(b), out)
}

func (c *Client) post(ctx context.Context, method, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, body)
	if err != nil {
		return c.redact(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		return c.redact(err)
	}
	defer resp.Body.Close()
	var res struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&res); err != nil {
		return fmt.Errorf("%s: %s: unreadable answer", method, resp.Status)
	}
	if !res.OK {
		switch {
		case res.ErrorCode == http.StatusUnauthorized || res.ErrorCode == http.StatusNotFound && method == "getMe":
			return ErrUnauthorized
		case res.ErrorCode == http.StatusConflict:
			return fmt.Errorf("%s: %s (another program is using this bot token, or a webhook is set)", method, res.Description)
		}
		return fmt.Errorf("%s: %s", method, res.Description)
	}
	if out != nil {
		return json.Unmarshal(res.Result, out)
	}
	return nil
}

// redact removes the bot token from errors, which can include the request URL.
func (c *Client) redact(err error) error {
	if err == nil || c.token == "" || !strings.Contains(err.Error(), c.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "<token>"))
}
