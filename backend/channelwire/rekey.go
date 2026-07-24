package channelwire

import "time"

const (
	// SessionKeyMaxAge is how long a Noise transport key may live before the
	// initiator should request an in-band rekey. Matches the frontend's former
	// CHANNEL_MAX_AGE_MS close-and-rehandshake ceiling. In-band rotation is
	// HKDF(k), not a fresh DH — see https://github.com/leapmux/leapmux/issues/321
	SessionKeyMaxAge = time.Hour

	// MinRekeyInterval is the earliest a peer may accept another rekey after a
	// successful one (or after the handshake). Ten minutes of headroom under
	// SessionKeyMaxAge stops age-only churn; SoftNonceLimit still bypasses.
	MinRekeyInterval = 50 * time.Minute

	// SessionKeyHardCeiling is the absolute age of a *single key epoch* past which
	// initiators must close and re-handshake instead of serving the channel. Ten
	// minutes past SessionKeyMaxAge covers one Reject backoff / modest clock skew.
	// Successful rekeys reset lastRekeyAt, so this does not bound total channel
	// lifetime — only how long one epoch key may be used.
	SessionKeyHardCeiling = SessionKeyMaxAge + 10*time.Minute

	// DefaultRejectBackoff is what initiators use when RekeyReject.retry_after_ms
	// is unset (0) — legacy peers or an unexpected empty reject.
	DefaultRejectBackoff = time.Minute
)

// AllowRekey reports whether an in-band rekey should be accepted by the worker
// (authoritative) or whether a client may retry after a Reject. lastRekeyAt is
// the handshake time until the first successful rekey. softNonce is true when
// either CipherState has exceeded SoftNonceLimit (NeedsRekeyEither).
func AllowRekey(now, lastRekeyAt time.Time, softNonce bool) bool {
	if softNonce {
		return true
	}
	if lastRekeyAt.IsZero() {
		return false
	}
	return !now.Before(lastRekeyAt.Add(MinRekeyInterval))
}

// RejectRetryAfter is how long an initiator should wait after an age-only Reject
// before retrying. Soft-nonce Requests are never rejected (AllowRekey), so this
// is only consulted on the age/interval path. When lastRekeyAt is zero the
// worker cannot compute an earliest-accept instant; MinRekeyInterval is returned
// as a conservative bound.
func RejectRetryAfter(now, lastRekeyAt time.Time) time.Duration {
	if lastRekeyAt.IsZero() {
		return MinRekeyInterval
	}
	earliest := lastRekeyAt.Add(MinRekeyInterval)
	if !now.Before(earliest) {
		return 0
	}
	return earliest.Sub(now)
}

// ShouldInitiateRekey reports whether the initiator should start a rekey.
// Age uses SessionKeyMaxAge; soft nonce bypasses MinRekeyInterval.
func ShouldInitiateRekey(now, lastRekeyAt time.Time, softNonce bool) bool {
	if softNonce {
		return true
	}
	if lastRekeyAt.IsZero() {
		return false
	}
	return !now.Before(lastRekeyAt.Add(SessionKeyMaxAge))
}

// PastHardCeiling reports whether the key is too old to keep serving: the
// initiator must close and re-handshake rather than encrypt under it.
func PastHardCeiling(now, lastRekeyAt time.Time) bool {
	if lastRekeyAt.IsZero() {
		return false
	}
	return !now.Before(lastRekeyAt.Add(SessionKeyHardCeiling))
}
