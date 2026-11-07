// Package transport has what the core and the messaging apps (transports) share: an incoming
// message, files sent and received, and Messenger, what a transport does for the core.
//
// A transport turns its app's events into Messages, keeping only direct messages from other
// people: group chats, its own outgoing messages, receipts and typing events never reach the
// core. Who may use the bot is the core's decision (its allowlist), not the transport's.
package transport

import "context"

// Message is a direct message to the bot.
type Message struct {
	Sender string   // who sent it, as the transport names people (e.g. a phone number)
	AltIDs []string // other names for the sender the allowlist may use (e.g. a Signal account id)
	Text   string   // trimmed; empty if it's only files
	ID     int64    // identifies the message for reactions; 0 if unknown
	Quote  *Quote   // the earlier message this replies to, if any
	Files  []File   // photos and files sent with it
}

// Quote is the earlier message a reply refers to.
type Quote struct {
	Text    string
	FromBot bool // the bot sent it (a reply or a command's output), not the user
}

// File is a file someone sent; fetch its contents with Messenger.Attachment.
type File struct {
	ID   string
	MIME string // e.g. image/jpeg
	Name string // the file name, if it had one
	Size int64
}

// Attachment is a file the bot sends.
type Attachment struct {
	MIME string
	Name string
	Data []byte
}

// Messenger is what a transport does for the core.
type Messenger interface {
	// Send sends text to recipient. With markdown set, text is the model's Markdown, which the
	// transport shows with its app's formatting (or as plain text if that fails); otherwise it's
	// sent as it is.
	Send(ctx context.Context, recipient, text string, markdown bool, attachments []Attachment) error
	// Typing starts or stops the typing indicator shown to recipient.
	Typing(ctx context.Context, recipient string, on bool) error
	// React adds emoji as a reaction to recipient's message id, or removes it.
	React(ctx context.Context, recipient string, id int64, emoji string, remove bool) error
	// Attachment fetches the contents of a received file by its id.
	Attachment(ctx context.Context, id string) ([]byte, error)
}
