package channelwire

import (
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

// Session-key rotation timing is owned by contracts/wire.json
// (contracts.SessionKeyMaxAge and friends; the browser's SESSION_KEY_*_MS
// constants come from the same file), and the derivations -- hard ceiling =
// max age + overrun -- are computed by the generator, not re-derived here.

// AllowRekey reports whether an in-band rekey should be accepted by the worker
// (authoritative) or whether a client may retry after a Reject. lastRekeyAt is
// the handshake time until the first successful rekey. softNonce is true when
// either CipherState has exceeded contracts.SoftNonceLimit (NeedsRekeyEither).
func AllowRekey(now, lastRekeyAt time.Time, softNonce bool) bool {
	if softNonce {
		return true
	}
	if lastRekeyAt.IsZero() {
		return false
	}
	return !now.Before(lastRekeyAt.Add(contracts.MinRekeyInterval))
}

// RejectRetryAfter is how long an initiator should wait after an age-only Reject
// before retrying. Soft-nonce Requests are never rejected (AllowRekey), so this
// is only consulted on the age/interval path. When lastRekeyAt is zero the
// worker cannot compute an earliest-accept instant; MinRekeyInterval is returned
// as a conservative bound.
func RejectRetryAfter(now, lastRekeyAt time.Time) time.Duration {
	if lastRekeyAt.IsZero() {
		return contracts.MinRekeyInterval
	}
	earliest := lastRekeyAt.Add(contracts.MinRekeyInterval)
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
	return !now.Before(lastRekeyAt.Add(contracts.SessionKeyMaxAge))
}

// PastHardCeiling reports whether the key is too old to keep serving: the
// initiator must close and re-handshake rather than encrypt under it.
func PastHardCeiling(now, lastRekeyAt time.Time) bool {
	if lastRekeyAt.IsZero() {
		return false
	}
	return !now.Before(lastRekeyAt.Add(contracts.SessionKeyHardCeiling))
}
