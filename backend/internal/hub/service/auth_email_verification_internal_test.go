package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// resendAvailableAt reports the cooldown deadline of a pending address, and
// nil once the resend button may work. The deadline itself is the first
// instant a resend may run, so a report at that instant is nil.
func TestResendAvailableAt(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Second)
	past := now.Add(-time.Second)
	pending := func(unblockedAt *time.Time) *store.User {
		return &store.User{PendingEmail: "pending@example.test", PendingEmailUnblockedAt: unblockedAt}
	}

	got := resendAvailableAt(pending(&future), now)
	require.NotNil(t, got, "a cooldown that has not elapsed is reported")
	assert.True(t, got.Equal(future), "the report is the stored deadline")

	assert.Nil(t, resendAvailableAt(pending(&now), now), "the deadline itself admits a resend")
	assert.Nil(t, resendAvailableAt(pending(&past), now), "an elapsed cooldown is not reported")
	assert.Nil(t, resendAvailableAt(pending(nil), now), "a pending address with no deadline has no cooldown")
	assert.Nil(t, resendAvailableAt(&store.User{PendingEmailUnblockedAt: &future}, now),
		"a deadline with no pending address has nothing to resend")

	// The comparison is between instants, so a clock in another zone reads
	// the same deadline.
	tokyo := time.FixedZone("UTC+9", 9*3600)
	assert.NotNil(t, resendAvailableAt(pending(&future), now.In(tokyo)))
	assert.Nil(t, resendAvailableAt(pending(&future), future.In(tokyo)))
}

// reportedVerificationStatus is the one report GetCurrentUser and VerifyEmail
// share. It reports nothing for an account the requirement does not hold back,
// and it never claims that a report sent mail.
func TestReportedVerificationStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	unverified := &store.User{PendingEmail: "pending@example.test", PendingEmailUnblockedAt: &future}

	assert.Equal(t, verificationOutcome{}, reportedVerificationStatus(unverified, false, now),
		"a hub that requires no verification holds nobody back")
	assert.Equal(t, verificationOutcome{}, reportedVerificationStatus(&store.User{EmailVerified: true}, true, now),
		"a verified address satisfies the requirement")
	assert.Equal(t, verificationOutcome{},
		reportedVerificationStatus(&store.User{IsAdmin: true, PendingEmail: "admin@example.test"}, true, now),
		"an administrator is never held back")

	held := reportedVerificationStatus(unverified, true, now)
	assert.True(t, held.Required)
	assert.False(t, held.EmailSent, "a report sends nothing")
	require.NotNil(t, held.NextResendAvailableAt, "the pending cooldown is reported")
	assert.True(t, held.NextResendAvailableAt.Equal(future))

	elapsed := reportedVerificationStatus(unverified, true, future)
	assert.True(t, elapsed.Required)
	assert.Nil(t, elapsed.NextResendAvailableAt, "an elapsed cooldown lets the user ask again at once")
}
