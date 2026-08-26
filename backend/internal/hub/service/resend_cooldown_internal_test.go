package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The two flows that send mail on demand share ONE cooldown and ONE
// derivation. They used to carry a constant and a formula each, with a
// comment in both naming the other as its twin.
//
// What these pin is the property that made sharing correct: the TTLs differ
// and the cooldown does not, so the cutoff must move with the TTL and the
// window must not.
func TestMintCutoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

	// A previous token that expires at or before the cutoff was issued at
	// least the cooldown ago, so a fresh mint may land. The cutoff is the
	// TTL out, less the cooldown.
	assert.Equal(t, now.Add(pendingEmailExpiry-resendCooldown), mintCutoff(now, pendingEmailExpiry))
	assert.Equal(t, now.Add(passwordResetExpiry-resendCooldown), mintCutoff(now, passwordResetExpiry))

	// The two flows differ by exactly their TTLs, and by nothing else. A
	// second cooldown constant would break this.
	assert.Equal(t,
		passwordResetExpiry-pendingEmailExpiry,
		mintCutoff(now, passwordResetExpiry).Sub(mintCutoff(now, pendingEmailExpiry)))

	// UTC whatever the caller passed, because the cutoff is compared against
	// a stored expiry the app clock wrote.
	loc := time.FixedZone("UTC+9", 9*3600)
	assert.Equal(t, time.UTC, mintCutoff(now.In(loc), pendingEmailExpiry).Location())
	assert.True(t, mintCutoff(now.In(loc), pendingEmailExpiry).Equal(mintCutoff(now, pendingEmailExpiry)))
}

// issuedAtFromExpiry and mintCutoff read the SAME derivation from opposite
// ends, and this is what keeps them consistent: a token minted exactly at the
// cutoff instant is a token issued exactly one cooldown ago.
func TestIssuedAtFromExpiryAgreesWithMintCutoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, ttl := range []time.Duration{pendingEmailExpiry, passwordResetExpiry} {
		// A previous token sitting exactly on the cutoff.
		expiry := mintCutoff(now, ttl)
		issued := issuedAtFromExpiry(expiry, ttl)
		assert.Equalf(t, now.Add(-resendCooldown), issued,
			"ttl %s: a token expiring at the cutoff was issued one cooldown ago", ttl)
		// So the countdown a client renders for it has just run out.
		assert.Equalf(t, now, nextResendAt(issued), "ttl %s", ttl)
	}
}

// nextResendAt is what a client counts down against, so it must be the same
// window mintCutoff enforces. A client that showed a shorter one would offer
// a button the hub refuses.
func TestNextResendAtUsesTheEnforcedWindow(t *testing.T) {
	t.Parallel()

	issued := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, issued.Add(resendCooldown), nextResendAt(issued))
}
