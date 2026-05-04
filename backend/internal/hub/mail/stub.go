package mail

import "context"

// StubSender is a TEST-ONLY Sender that silently accepts every Message
// and returns nil. Use it in unit tests that need a non-nil Sender but
// don't assert on body content.
//
// Production wiring picks between SMTPSender (when SMTP is configured)
// and disabledSender (when it isn't); StubSender is not used in either
// path. Do not log message bodies here — verification codes are
// credentials, and logging them leaks them into log aggregation
// pipelines.
type StubSender struct{}

// NewStubSender returns a TEST-ONLY Sender that silently succeeds.
func NewStubSender() *StubSender { return &StubSender{} }

// Send silently accepts the message and returns nil. It never fails so
// callers can rely on it during tests without conditioning behavior on
// the dispatch outcome.
func (s *StubSender) Send(_ context.Context, _ Message) error {
	return nil
}
