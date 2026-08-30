package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The two flows that send mail on demand share ONE gate mechanism: the
// store's unblocked_at column. What these pin are the properties that
// made sharing correct -- the deadline is the value the gate compares, so
// no flow's lifetime (a 30-minute code, a 1-hour link) can reach it, and
// the reported countdown is the deadline itself.
func TestMintUnblockedAt(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

	// The mint arms exactly one cooldown ahead, whatever the flow's token
	// lifetime: the derivation takes no TTL, because the function takes
	// none.
	assert.True(t, mintUnblockedAt(now).Equal(now.Add(resendCooldown)),
		"the mint blocks one resend cooldown ahead")
	assert.Equal(t, time.UTC, mintUnblockedAt(now.In(time.FixedZone("UTC+9", 9*3600))).Location(),
		"the deadline is UTC whatever the caller passed")
}

// The two boundary values of the failure window are what the knob means,
// and the clamp holds the file's own invariant: a failed send NEVER leaves
// more blockade than a successful one would have, whatever the input.
func TestFailedSendUnblockedAt(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	assert.True(t, failedSendUnblockedAt(now, 0).Equal(now),
		"a disabled window leaves no blockade")
	assert.True(t, failedSendUnblockedAt(now, resendCooldown).Equal(now.Add(resendCooldown)),
		"a full-cooldown window leaves exactly the cooldown")
	assert.True(t, failedSendUnblockedAt(now, 10*time.Second).Equal(now.Add(10*time.Second)),
		"a 10s window leaves 10s of blockade")
	assert.True(t, failedSendUnblockedAt(now, time.Hour).Equal(now.Add(resendCooldown)),
		"a window above the resend cooldown clamps to the resend cooldown")
	assert.True(t, failedSendUnblockedAt(now, -time.Minute).Equal(now),
		"a negative window clamps to no blockade")
	assert.Equal(t, time.UTC, failedSendUnblockedAt(now.In(time.FixedZone("UTC+9", 9*3600)), 0).Location(),
		"the deadline is UTC whatever the caller passed")
}

// The failure deadline must derive from a clock read AT the failure, never
// from one taken before the SMTP dial: the dial time eats into the window
// the deadline leaves, and a relay that fails slowly (the dial timeout
// outlives the default 10s window) would erase it entirely.
func TestFailedSendDeadlineOutlivesTheDial(t *testing.T) {
	t.Parallel()

	mint := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	failure := mint.Add(20 * time.Second) // a relay that stalls, then fails

	deadline := failedSendUnblockedAt(failure, 10*time.Second)
	// A retry the instant after the failure is still inside the window...
	retryAt := failure.Add(5 * time.Second)
	assert.True(t, deadline.After(retryAt),
		"a slow failure must still leave the window: the deadline anchors at the failure, not the dial start")
	// ...and the window measures the configured 10s from the failure.
	assert.False(t, deadline.After(failure.Add(10*time.Second)),
		"the window elapses exactly failureCooldown after the failure")
	// The pre-dial read this test pins against would have left nothing:
	// its deadline lands at the retry instant or before it.
	assert.False(t, failedSendUnblockedAt(mint, 10*time.Second).After(retryAt),
		"a pre-dial deadline is already elapsed -- the regression this guards against")
}

// The deadline a response reports is the deadline the row carries -- the
// one the gate compares. The fallback (no pending row was written) reports
// one fresh cooldown, which is exactly what the next mint will arm.
func TestPendingResendDeadline(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	stored := now.Add(30 * time.Second)
	deadline := pendingResendDeadline(now, &stored)
	assert.True(t, deadline.Equal(stored),
		"a stored deadline is reported verbatim: the countdown and the gate read one value")
	assert.True(t, pendingResendDeadline(now, nil).Equal(mintUnblockedAt(now)),
		"a row with no deadline reports the cooldown the next mint will arm")
}
