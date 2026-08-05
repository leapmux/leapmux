package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/hub/auth"
)

// The close reason is the ONLY thing that tells a refused client which of the
// two policy-violation closes it got, and they call for opposite actions:
// re-authenticate, or close a tab. It is now the outcome's own Label() -- the
// same token the refusal is counted under -- rather than a mapping beside it,
// so this asserts against Label() from the caller's side: every outcome must be
// sendable as a close reason, and only the cap may claim the cap's token.
//
// The mapping this replaced was an `if` over ONE outcome that defaulted every
// other to credential prose, so a third refusal would have reached the wire
// claiming an expired credential -- advice a client cannot act on, for a reason
// nobody has written yet.
func TestLeaseOutcomeLabelIsSendableAsACloseReason(t *testing.T) {
	t.Parallel()

	for _, outcome := range []auth.LeaseOutcome{
		auth.LeaseGranted,
		auth.LeaseRefusedCredential,
		auth.LeaseRefusedTooManyConnections,
		// An outcome nobody has written yet.
		auth.LeaseOutcome(99),
	} {
		reason := outcome.Label()
		require.NotEmpty(t, reason,
			"outcome %d must map to a reason the client can act on, not an empty close frame", outcome)
		// RFC 6455 caps a close reason at 123 bytes and coder/websocket refuses
		// to send a longer one -- which would turn a refusal into a silent drop.
		assert.LessOrEqual(t, len(reason), 123, "outcome %d", outcome)
	}

	assert.Equal(t, channelwire.CloseReasonTooManyConnections,
		auth.LeaseRefusedTooManyConnections.Label())

	// Everything that is not the cap must NOT claim to be: a client shown the
	// cap's token closes a tab, which does nothing for a revoked credential. And
	// each of them is its OWN token rather than a shared fallback, so a third
	// refusal arrives on the wire as itself instead of reading as a credential
	// failure.
	seen := map[string]bool{}
	for _, notTheCap := range []auth.LeaseOutcome{
		auth.LeaseGranted,
		auth.LeaseRefusedCredential,
		auth.LeaseOutcome(99),
	} {
		label := notTheCap.Label()
		assert.NotEqual(t, channelwire.CloseReasonTooManyConnections, label,
			"outcome %d must not be reported as a connection-cap refusal", notTheCap)
		assert.False(t, seen[label],
			"outcome %d shares a close token with another outcome, so a client cannot tell them apart", notTheCap)
		seen[label] = true
	}
}
