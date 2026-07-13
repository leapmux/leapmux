package auth

import "time"

// CredentialDeadline is a credential's teardown deadline with an explicit
// three-way meaning, replacing a bare time.Time whose zero value was overloaded
// to mean BOTH "never expires" and "clear the deadline", and a *time.Time whose
// nil-vs-zero-pointer distinguished "no deadline on file" from "cleared". Making
// the three states -- Unset, NeverExpires, and At(t) -- representable in the type
// keeps a caller from conflating them, and keeps the lease and channel sides of a
// credential's lifetime from disagreeing on what a zero meant (the bug the
// lease/channel reschedule split had to guard against by hand).
type CredentialDeadline struct {
	at   time.Time
	kind deadlineKind
}

type deadlineKind uint8

const (
	// deadlineUnset is the zero CredentialDeadline: no deadline is on file. Used
	// by a channel's pendingExpiry to mean "no reschedule landed in the open
	// window", distinct from a reschedule that cleared the deadline (NeverExpires).
	deadlineUnset deadlineKind = iota
	// deadlineNever: the credential never expires.
	deadlineNever
	// deadlineAt: the credential expires at a specific, non-zero instant.
	deadlineAt
)

// UnsetDeadline is the zero CredentialDeadline: no deadline on file.
func UnsetDeadline() CredentialDeadline { return CredentialDeadline{} }

// NeverExpires is the deadline of a credential that never expires.
func NeverExpires() CredentialDeadline { return CredentialDeadline{kind: deadlineNever} }

// DeadlineAt is the deadline of a credential that expires at t. A zero t maps to
// NeverExpires, centralizing the "zero time == never expires" convention at the
// store boundary where DB expiry values enter the credential layer.
func DeadlineAt(t time.Time) CredentialDeadline {
	if t.IsZero() {
		return NeverExpires()
	}
	return CredentialDeadline{at: t, kind: deadlineAt}
}

// IsUnset reports whether no deadline is on file.
func (d CredentialDeadline) IsUnset() bool { return d.kind == deadlineUnset }

// IsNever reports whether the credential never expires.
func (d CredentialDeadline) IsNever() bool { return d.kind == deadlineNever }

// At returns the finite expiry instant and true when the deadline is At(t), or
// (zero, false) for Unset and NeverExpires. Callers arming a timer or ordering
// the expiry heap use the ok result to skip the never/unset cases.
func (d CredentialDeadline) At() (time.Time, bool) {
	return d.at, d.kind == deadlineAt
}

// IsCurrent reports whether a credential with this deadline is still live at now:
// only an At(t) deadline can expire (it requires now strictly before t, an
// exclusive upper bound). A deadline with no finite instant -- NeverExpires or the
// zero-value Unset -- never expires, so the zero-value UserInfo.CredentialExpiresAt
// reads as never-expires exactly as the old zero time.Time did. This is the single
// source of truth for credential-expiry semantics shared by the auth cache and the
// channel service, so the two can never disagree at the exact expiry instant.
func (d CredentialDeadline) IsCurrent(now time.Time) bool {
	at, ok := d.At()
	if !ok {
		return true
	}
	return now.Before(at)
}

// Later returns the more permissive of two deadlines. A deadline with no finite
// instant (NeverExpires or the zero-value Unset) never expires and so is most
// permissive; between two At deadlines the later instant wins. Used so a channel
// is never armed earlier than any deadline known for its credential. (The
// Unset-vs-NeverExpires distinction is meaningful only for a channel's
// pendingExpiry, via IsUnset -- here both read as "no finite deadline".)
func (d CredentialDeadline) Later(other CredentialDeadline) CredentialDeadline {
	da, dok := d.At()
	oa, ook := other.At()
	if !dok || !ook {
		return NeverExpires()
	}
	if oa.After(da) {
		return other
	}
	return d
}

// AdoptReschedule returns the deadline to apply when rescheduling a credential
// from d (its current deadline) to newExpiry: newExpiry verbatim, EXCEPT it
// never regresses a live finite deadline to an EARLIER finite instant. Two
// concurrent extensions of the same credential (e.g. same-bearer refresh
// rotations, or a slide racing a rotation) can be delivered to the lease timer
// and channel heap out of order; without this, the later-arriving-but-earlier
// deadline would win and tear a still-valid channel/lease down early. A clear
// (NeverExpires) is always adopted -- it is the most permissive outcome -- as is
// a never/unset -> finite transition, so the only case this guards is At-vs-At
// regression. Mirrors the monotonic compare-and-swap RecordBearerExpiry already
// applies to the cache-side per-bearer deadline, so the three sides cannot
// disagree on a credential's live deadline.
func (d CredentialDeadline) AdoptReschedule(newExpiry CredentialDeadline) CredentialDeadline {
	cur, curOK := d.At()
	next, nextOK := newExpiry.At()
	if curOK && nextOK && cur.After(next) {
		return d
	}
	return newExpiry
}

// Equal reports whether two deadlines carry the same kind and, for At, the same
// instant (comparing instants, not wall-clock representation). Used to skip a
// no-op store in the monotonic bearer-expiry record.
func (d CredentialDeadline) Equal(other CredentialDeadline) bool {
	if d.kind != other.kind {
		return false
	}
	if d.kind == deadlineAt {
		return d.at.Equal(other.at)
	}
	return true
}
