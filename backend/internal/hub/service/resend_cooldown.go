package service

import "time"

// The resend cooldown, in ONE place, for every mail the hub sends on demand.
//
// It limits two flows -- the email-verification code and the
// password-reset link -- and each flow carried its own copy: two constants
// holding the same 60 seconds, and two spellings of the same cutoff formula.
// Each carried a comment that identified the other as its twin, which is the
// shape a second source of truth takes just before the two drift.
//
// The token LIFETIMES stay per-flow and differ on purpose (a verification code
// is short because it is short to type; a reset link is longer because it
// arrives by mail and the person clicks it later). The cooldown and its
// cutoff do not differ, so they live here.

// resendCooldown caps how often one account can ask the hub to send again.
//
// Without it nothing stops a signed-in user from filling their own inbox --
// or, through an email change, somebody else's. Sixty seconds is long enough
// to stop a held key or a stuck retry and short enough that a person who
// really did lose the first mail does not give up waiting.
const resendCooldown = 60 * time.Second

// mintCutoff is the instant a previous code or link must have been issued
// at or before for a fresh mint to land, which is how the hub enforces the
// cooldown.
//
// The gate reads the issued-at column the mint wrote, never the expiry.
// The expiry cannot stand in for the issue time: ConsumeVerificationAttempt
// and ConsumeRecoveryAttemptByToken force-expire a burned code or link by
// moving the expiry to now, so a cutoff derived as "expiry minus TTL"
// would read a five-second-old burned code as issued a full lifetime ago
// and let the holder re-mint inside the cooldown -- burning the guess
// budget would reset the cooldown, and every burn-and-resend cycle would
// send another mail. The issued-at column moves only when a mint lands, so
// no attempt pattern can hurry it.
//
// The comparison belongs in the mint STATEMENT rather than in a Go
// read-then-check, and that is why this returns a cutoff instead of a
// boolean: two concurrent resends both pass a Go check and both send.
func mintCutoff(now time.Time) time.Time {
	return now.UTC().Add(-resendCooldown)
}

// nextResendAt seeds the next-resend timestamp a successful send returns, so
// a client renders a countdown against the same rule the hub enforces.
func nextResendAt(issuedAt time.Time) time.Time {
	return issuedAt.Add(resendCooldown)
}
