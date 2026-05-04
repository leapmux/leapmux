package mail

import (
	"context"
	"errors"
)

// ErrEmailDisabled is returned by the no-op Sender that the hub wires up
// when SMTP is not configured. Callers can match it via errors.Is to
// distinguish "the hub has no mail backend at all" from a transient SMTP
// failure. Validation in the config layer prevents email-using features
// (verification, worker registration email) from being reachable when
// SMTP is unconfigured, so this error should not surface during normal
// operation; it exists as a loud, matchable signal in case any code path
// slips past those gates.
var ErrEmailDisabled = errors.New("hub email is not configured")

// disabledSender is the production no-SMTP sender. It returns
// ErrEmailDisabled from every Send. Unlike StubSender (TEST-ONLY), it
// never silently succeeds — silent success here would mean verification
// codes were "sent" while no mail server was wired up.
type disabledSender struct{}

// NewDisabledSender returns the production no-SMTP Sender. Use this
// when the operator has not configured any SMTP host; the hub server
// already does so at backend/hub/server.go.
func NewDisabledSender() Sender { return disabledSender{} }

// Send returns ErrEmailDisabled. The argument is ignored — there is no
// transport to deliver it on.
func (disabledSender) Send(_ context.Context, _ Message) error {
	return ErrEmailDisabled
}
