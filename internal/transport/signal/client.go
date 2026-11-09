// Package signal is the Signal transport. It talks to bbernhard/signal-cli-rest-api running in MODE=json-rpc.
package signal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/tfielding/llamero/internal/transport"
)

// Envelope is the subset of signal-cli's receive payload we care about.
type Envelope struct {
	Source       string          `json:"source"`
	SourceNumber string          `json:"sourceNumber"`
	SourceUUID   string          `json:"sourceUuid"`
	SourceName   string          `json:"sourceName"`
	Timestamp    int64           `json:"timestamp"`
	DataMessage  *DataMessage    `json:"dataMessage"`
	SyncMessage  json.RawMessage `json:"syncMessage"`
}

type DataMessage struct {
	Timestamp   int64                `json:"timestamp"`
	Message     string               `json:"message"`
	GroupInfo   *GroupInfo           `json:"groupInfo"`
	Quote       *Quote               `json:"quote"`       // set when the message is a reply to an earlier one
	Attachments []ReceivedAttachment `json:"attachments"` // photos and files sent with it
}

// ReceivedAttachment is a file someone sent; fetch its contents with Client.Attachment.
type ReceivedAttachment struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType"` // e.g. image/jpeg
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
}

// Quote is the earlier message a reply refers to.
type Quote struct {
	ID           int64  `json:"id"` // timestamp of the quoted message
	Author       string `json:"author"`
	AuthorNumber string `json:"authorNumber"`
	AuthorUUID   string `json:"authorUuid"`
	Text         string `json:"text"`
}

type GroupInfo struct {
	GroupID string `json:"groupId"`
}

type Client struct {
	base   string
	number string
	http   *http.Client

	pingEvery   time.Duration // how often to check the receive websocket is still alive
	pingTimeout time.Duration

	mu        sync.Mutex
	connected bool
	since     time.Time // when the current connection was made
	lastErr   error     // why the last connection ended (or failed)
	groups    transport.GroupLog
}

func New(baseURL, number string) *Client {
	return &Client{base: baseURL, number: number, http: &http.Client{Timeout: 30 * time.Second},
		pingEvery: 30 * time.Second, pingTimeout: 10 * time.Second}
}

// Status reports whether the receive websocket is connected, since when, and why the last
// connection ended or failed.
func (c *Client) Status() (connected bool, since time.Time, lastErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected, c.since, c.lastErr
}

func (c *Client) setStatus(connected bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected, c.lastErr = connected, err
	if connected {
		c.since = time.Now()
	}
}

// Listen connects to the receive websocket and calls handle for every direct message from someone
// else (see message). It reconnects with backoff until ctx is cancelled.
func (c *Client) Listen(ctx context.Context, handle func(context.Context, transport.Message)) error {
	return c.listen(ctx, func(ctx context.Context, env Envelope) {
		if m, ok := c.message(env); ok {
			handle(ctx, m)
		}
	})
}

// listen calls handle for every envelope.
func (c *Client) listen(ctx context.Context, handle func(context.Context, Envelope)) error {
	wsURL := strings.Replace(c.base, "http", "ws", 1) + "/v1/receive/" + url.PathEscape(c.number)
	backoff := time.Second
	for {
		err := c.listenOnce(ctx, wsURL, handle, func() { backoff = time.Second })
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("signal websocket disconnected, retrying", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (c *Client) listenOnce(ctx context.Context, wsURL string, handle func(context.Context, Envelope), connected func()) (err error) {
	defer func() { c.setStatus(false, err) }()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)
	slog.Info("connected to signal", "url", wsURL)
	c.setStatus(true, nil)
	connected()

	// A connection can die without either side noticing (e.g. after a network blip), leaving
	// Read waiting forever. Pinging finds that out, and closing makes Read return so we reconnect.
	pingCtx, stopPing := context.WithCancel(ctx)
	defer stopPing()
	go func() {
		t := time.NewTicker(c.pingEvery)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
			}
			pctx, cancel := context.WithTimeout(pingCtx, c.pingTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil && pingCtx.Err() == nil {
				slog.Warn("signal websocket not answering pings, reconnecting", "err", err)
				conn.CloseNow()
				return
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg struct {
			Envelope Envelope `json:"envelope"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("skipping unparseable message", "err", err)
			continue
		}
		handle(ctx, msg.Envelope)
	}
}

// Send sends a message to a single recipient. With markdown set, text is converted to Signal's
// formatting (ToSignal); if that send fails, the raw text is sent instead.
func (c *Client) Send(ctx context.Context, recipient, text string, markdown bool, attachments []transport.Attachment) error {
	if !markdown {
		return c.send(ctx, recipient, text, false, attachments)
	}
	if err := c.send(ctx, recipient, ToSignal(text), true, attachments); err != nil {
		slog.Warn("styled send failed, retrying as plain text", "err", err)
		return c.send(ctx, recipient, text, false, attachments)
	}
	return nil
}

// send sends a message to a single recipient. With styled set, the container
// turns **bold**, *italic*, ~strike~, `mono` and ||spoiler|| into Signal formatting.
func (c *Client) send(ctx context.Context, recipient, text string, styled bool, attachments []transport.Attachment) error {
	mode := "normal"
	if styled {
		mode = "styled"
	}
	body := map[string]any{
		"number":     c.number,
		"recipients": []string{recipient},
		"message":    text,
		"text_mode":  mode,
	}
	if len(attachments) > 0 {
		enc := make([]string, len(attachments))
		for i, a := range attachments {
			enc[i] = fmt.Sprintf("data:%s;filename=%s;base64,%s", a.MIME, a.Name, base64.StdEncoding.EncodeToString(a.Data))
		}
		body["base64_attachments"] = enc
	}
	return c.do(ctx, http.MethodPost, "/v2/send", body)
}

// React adds emoji as a reaction to the message recipient sent at ts (Signal identifies messages
// by their timestamp), or removes it.
func (c *Client) React(ctx context.Context, recipient string, ts int64, emoji string, remove bool) error {
	method := http.MethodPost
	if remove {
		method = http.MethodDelete
	}
	return c.do(ctx, method, "/v1/reactions/"+url.PathEscape(c.number), map[string]any{
		"recipient": recipient, "reaction": emoji, "target_author": recipient, "timestamp": ts,
	})
}

// Typing starts or stops the typing indicator shown to recipient.
func (c *Client) Typing(ctx context.Context, recipient string, on bool) error {
	method := http.MethodPut
	if !on {
		method = http.MethodDelete
	}
	return c.do(ctx, method, "/v1/typing-indicator/"+url.PathEscape(c.number), map[string]string{"recipient": recipient})
}

// MaxAttachmentBytes caps a downloaded attachment.
const MaxAttachmentBytes = 20 << 20

// Attachment downloads a received attachment's contents by its id.
func (c *Client) Attachment(ctx context.Context, id string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/attachments/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("attachment %s: %s: %s", id, resp.Status, strings.TrimSpace(string(msg)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxAttachmentBytes {
		return nil, fmt.Errorf("attachment %s is larger than %d MB", id, MaxAttachmentBytes>>20)
	}
	return data, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
