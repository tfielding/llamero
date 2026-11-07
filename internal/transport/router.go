package transport

import (
	"context"
	"fmt"
	"strings"
)

// Router is a Messenger over several transports, so the core can talk on all of them at once.
// Each transport has a prefix that its people's ids (and its file ids) get in the core, like
// "telegram:12345", so the same person on two apps counts as two people, each with their own
// chat. One transport may have no prefix: its ids stay as they are (Signal's, so existing
// allowlists and chats keep working).
type Router struct {
	routes map[string]Messenger // by prefix; "" = the transport without one
}

func NewRouter() *Router { return &Router{routes: map[string]Messenger{}} }

// Add registers a transport under prefix: lowercase letters and digits, or "" for the one
// transport whose ids aren't prefixed.
func (r *Router) Add(prefix string, m Messenger) error {
	for _, c := range prefix {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return fmt.Errorf("transport prefix %q: use lowercase letters and digits", prefix)
		}
	}
	if _, ok := r.routes[prefix]; ok {
		return fmt.Errorf("transport prefix %q is already used", prefix)
	}
	r.routes[prefix] = m
	return nil
}

// Inbound wraps the core's handler for the transport added under prefix: the message's ids get
// the prefix before the core sees them.
func (r *Router) Inbound(prefix string, handle func(context.Context, Message)) func(context.Context, Message) {
	if prefix == "" {
		return handle
	}
	return func(ctx context.Context, m Message) {
		m.Sender = prefix + ":" + m.Sender
		alt := make([]string, len(m.AltIDs))
		for i, id := range m.AltIDs {
			alt[i] = prefix + ":" + id
		}
		m.AltIDs = alt
		files := make([]File, len(m.Files))
		for i, f := range m.Files {
			f.ID = prefix + ":" + f.ID
			files[i] = f
		}
		m.Files = files
		handle(ctx, m)
	}
}

// route finds the transport for an id from the core, and the id as that transport knows it.
func (r *Router) route(id string) (Messenger, string, error) {
	if prefix, rest, ok := strings.Cut(id, ":"); ok {
		if m, ok := r.routes[prefix]; ok && prefix != "" {
			return m, rest, nil
		}
	}
	if m, ok := r.routes[""]; ok {
		return m, id, nil
	}
	return nil, "", fmt.Errorf("no transport for %q", id)
}

func (r *Router) Send(ctx context.Context, recipient, text string, markdown bool, attachments []Attachment) error {
	m, id, err := r.route(recipient)
	if err != nil {
		return err
	}
	return m.Send(ctx, id, text, markdown, attachments)
}

func (r *Router) Typing(ctx context.Context, recipient string, on bool) error {
	m, id, err := r.route(recipient)
	if err != nil {
		return err
	}
	return m.Typing(ctx, id, on)
}

func (r *Router) React(ctx context.Context, recipient string, msgID int64, emoji string, remove bool) error {
	m, id, err := r.route(recipient)
	if err != nil {
		return err
	}
	return m.React(ctx, id, msgID, emoji, remove)
}

func (r *Router) Attachment(ctx context.Context, fileID string) ([]byte, error) {
	m, id, err := r.route(fileID)
	if err != nil {
		return nil, err
	}
	return m.Attachment(ctx, id)
}
