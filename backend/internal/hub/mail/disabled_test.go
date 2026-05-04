package mail_test

import (
	"context"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/mail"
)

// TestDisabledSender_ReturnsSentinel locks in the no-silent-fallback
// invariant: when SMTP is unconfigured, every Send returns
// mail.ErrEmailDisabled (not nil, not some opaque wrapped error).
// `errors.Is` matchability is part of the contract — future code that
// wants to distinguish "hub email is off" from a transient relay
// failure must be able to use it.
func TestDisabledSender_ReturnsSentinel(t *testing.T) {
	s := mail.NewDisabledSender()
	err := s.Send(context.Background(), mail.Message{
		To:      "alice@example.test",
		Subject: "anything",
		Body:    "anything\n",
	})
	if err == nil {
		t.Fatal("Send returned nil; disabled sender must never silently succeed")
	}
	if !errors.Is(err, mail.ErrEmailDisabled) {
		t.Errorf("err = %v, want errors.Is(_, ErrEmailDisabled)", err)
	}
}
