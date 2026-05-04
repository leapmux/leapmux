// Package mail dispatches transactional emails (verification codes,
// worker registration instructions). The Sender interface lets us swap
// in a real SMTP/SES backend later without touching call sites.
package mail

import "context"

// Message is a plain-text email payload.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender delivers a Message. Implementations must be safe for concurrent
// use. Send may block on network I/O; callers should pass an appropriate
// context.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}
