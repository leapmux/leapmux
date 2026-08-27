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
// arrives by mail and the person clicks it later). The cooldown and the
// derivation do not differ, so they live here.

// resendCooldown caps how often one account can ask the hub to send again.
//
// Without it nothing stops a signed-in user from filling their own inbox --
// or, through an email change, somebody else's. Sixty seconds is long enough
// to stop a held key or a stuck retry and short enough that a person who
// really did lose the first mail does not give up waiting.
const resendCooldown = 60 * time.Second

// mintCutoff is the instant a previous token must have expired at or before
// for a fresh mint to land, which is how the hub enforces the cooldown.
//
// The derivation, once: neither SetPendingEmail nor SetPendingPasswordReset
// stores an issued-at, so the derivation reconstructs issued_at as the
// previous expiry minus the constant TTL that set it. "Issued at least the
// cooldown ago" is then "previous expiry at or before now + (TTL - cooldown)".
// Both instants are on the app clock, the clock that wrote the expiry.
//
// The comparison belongs in the mint STATEMENT rather than in a Go
// read-then-check, and that is why this returns a cutoff instead of a
// boolean: two concurrent resends both pass a Go check and both send.
func mintCutoff(now time.Time, ttl time.Duration) time.Time {
	return now.UTC().Add(ttl - resendCooldown)
}

// issuedAtFromExpiry reconstructs when a pending token was issued, from the
// expiry it set and the constant TTL used to mint it. It is the same
// derivation mintCutoff applies, read from the other end: mintCutoff decides
// whether a mint may land, and this reports when the last one did so the
// client can show a countdown.
func issuedAtFromExpiry(expiresAt time.Time, ttl time.Duration) time.Time {
	return expiresAt.Add(-ttl)
}

// nextResendAt seeds the next-resend timestamp a successful send returns, so
// a client renders a countdown against the same rule the hub enforces.
func nextResendAt(issuedAt time.Time) time.Time {
	return issuedAt.Add(resendCooldown)
}
