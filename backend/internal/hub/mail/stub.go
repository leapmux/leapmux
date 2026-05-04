package mail

import (
	"context"
	"log/slog"
)

// StubSender is the default Sender. It logs every outgoing message at
// info level, including the full body, so developers can copy
// verification codes / registration commands straight from the hub's
// stdout. A production backend will replace this.
type StubSender struct{}

// NewStubSender returns a Sender that logs messages instead of sending.
func NewStubSender() *StubSender { return &StubSender{} }

// Send logs the message and returns nil. It never fails so callers can
// rely on it during development without conditioning behavior on the
// dispatch outcome.
func (s *StubSender) Send(_ context.Context, msg Message) error {
	slog.Info("[mail-stub] would send",
		"to", msg.To,
		"subject", msg.Subject,
		"body", msg.Body,
	)
	return nil
}
