package service

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/settings"
)

// The pending-mail gate, in ONE place, for every mail the hub sends on
// demand.
//
// It limits two flows -- the email-verification code and the
// account-recovery link -- and each flow carried its own copy: two constants
// holding the same 60 seconds, and two spellings of the same cutoff formula.
// Each carried a comment that identified the other as its twin, which is the
// shape a second source of truth takes just before the two drift.
//
// The token LIFETIMES stay per-flow and differ on purpose (a verification code
// is short because it is short to type; a recovery link is longer because it
// arrives by mail and the person clicks it later). The gate does not differ,
// so its one mechanism lives here: the store's unblocked_at column, one
// deadline that both the mint gate compares and the reported countdown
// shows. A mint arms it for one resend cooldown; a failed-send clear arms it
// for the failure window; every other clear NULLs it.

// resendCooldown caps how often one account can ask the hub to send again.
//
// Without it nothing stops a signed-in user from filling their own inbox --
// or, through an email change, somebody else's. Sixty seconds is long enough
// to stop a held key or a stuck retry and short enough that a person who
// really did lose the first mail does not give up waiting.
const resendCooldown = 60 * time.Second

// mintUnblockedAt is the deadline a mint arms: one resend cooldown from
// the instant it lands. The store gate compares this deadline directly,
// and the countdown a client renders IS it, so the enforced and the
// reported window cannot disagree.
func mintUnblockedAt(now time.Time) time.Time {
	return now.UTC().Add(resendCooldown)
}

// failedSendUnblockedAt is the deadline a failed-send clear arms: the
// failure window from the instant the relay answered. The caller reads the
// clock AT the failure, never before the SMTP dial -- a deadline derived
// from a pre-dial read leaves max(0, window - dial) of blockade, and a
// relay that fails slowly (the dial timeout is 20s against a default 10s
// window) eats the whole window.
//
// The window is settings-driven (mail_limits.failure_cooldown_seconds) and
// is held at or under the resend cooldown: validation refuses more, and
// this clamp holds the invariant even if that bound and this constant ever
// drift apart. A failed send NEVER leaves more blockade than a successful
// one would have.
func failedSendUnblockedAt(now time.Time, failureCooldown time.Duration) time.Time {
	if failureCooldown > resendCooldown {
		failureCooldown = resendCooldown
	}
	if failureCooldown < 0 {
		failureCooldown = 0
	}
	return now.UTC().Add(failureCooldown)
}

// pendingResendDeadline is the countdown a response reports: the stored
// deadline when the row carries one, and one fresh cooldown when it does
// not (no pending row was written, but the response still reports a
// deadline the next mint will enforce).
func pendingResendDeadline(now time.Time, blockedUntil *time.Time) time.Time {
	if blockedUntil != nil {
		return *blockedUntil
	}
	return mintUnblockedAt(now)
}

// mailFailureCooldown reads the failed-send window (mail_limits) from the
// settings snapshot. Every failed-send deadline derivation goes through
// this one read, so the knob has a single read path beside the gate
// machinery that consumes it.
func mailFailureCooldown(ctx context.Context, set *settings.Manager) time.Duration {
	return settings.EmailFailureCooldown(set.Snapshot(ctx))
}
