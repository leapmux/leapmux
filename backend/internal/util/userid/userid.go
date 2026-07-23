// Package userid provides a validated, non-empty user identifier.
//
// UserID is minted only at a process boundary (Hub session/bearer decode,
// Worker channel open, Worker local-IPC spawn). Authorization predicates
// compare through Matches/MatchesUser, which fail closed when either side is
// empty or zero -- Go cannot forbid UserID{}, so those methods are the
// load-bearing part of the type: they collapse every hand-written empty-id
// guard into one comparison every predicate routes through.
package userid

import "log/slog"

// UserID is a validated, non-empty user identifier, minted only at a
// process boundary. The zero value is constructible (Go cannot forbid
// UserID{}), so callers MUST compare via Matches or MatchesUser to keep
// empty-vs-empty from matching.
//
// The leading zero-size func field makes UserID NON-COMPARABLE, so `a == b` and
// `map[UserID]T` are compile errors rather than the third, silent comparison
// route. That route is fail-OPEN in exactly the way the type exists to prevent:
// two zero values are `==` equal, so an unminted caller would match another
// unminted one. Matches and MatchesUser both refuse an empty side; `==` cannot,
// and no amount of documentation makes a compiling expression stop compiling.
//
// It is the first field on purpose: Go pads a struct whose LAST field is
// zero-size, so placing it at the end would add a word to every UserID.
type UserID struct {
	_  [0]func()
	id string
}

// New returns a UserID for a non-empty s. ok is false when s is empty.
func New(s string) (UserID, bool) {
	if s == "" {
		return UserID{}, false
	}
	return UserID{id: s}, true
}

// MustNew returns a UserID for a non-empty s, or panics. Mirrors
// auth.SessionCredential: known-good mint sites and tests use this so a
// blank id fails at construction rather than as a silent deny later.
func MustNew(s string) UserID {
	u, ok := New(s)
	if !ok {
		panic("userid: empty user id")
	}
	return u
}

// String returns the underlying id, or "" for the zero value.
func (u UserID) String() string { return u.id }

// IsZero reports whether u was never minted (or was minted from "").
func (u UserID) IsZero() bool { return u.id == "" }

// Matches reports whether u equals an untyped string id -- usually a store
// column, sometimes a request field or a revocation target that has not been
// minted. It fails closed: false if either side is empty. This is the
// load-bearing comparison for predicates that check a typed principal against
// a string that never passed through New.
//
// Callers on an EVICTION path must not rely on it alone: false there means "do
// not revoke", so a blank id would skip every entry and report a revocation
// that evicted nothing. Those entrypoints refuse a blank id up front instead.
func (u UserID) Matches(stored string) bool {
	// `stored != ""` would be redundant -- it follows from u.id != "" and the
	// equality -- so the two clauses here are the whole rule.
	return u.id != "" && u.id == stored
}

// MatchesUser reports whether u names the same user as other. It fails closed:
// false if either is zero. Prefer this over comparing String() results.
//
// The name is deliberately NOT Equal, for two reasons. The obvious one is that
// this type is non-comparable on purpose, and a method called Equal invites the
// reader to assume `==` would have done the same thing -- which is the exact
// fail-open the non-comparability exists to make impossible. The load-bearing
// one is that `Equal` is the single most overloaded method name in Go
// (time.Time, big.Int, netip.Addr, every proto message), so the repo-wide net
// in internal/audit that requires every caller-identity comparison to be
// classified could not tell a UserID comparison from a timestamp one without
// full type resolution -- and so did not scan Equal at all. Both identity
// comparisons now share the Matches prefix and neither name collides with
// anything else in the module, which is what lets that net be exact rather than
// heuristic. See internal/audit/identity.go.
func (u UserID) MatchesUser(other UserID) bool {
	return u.id != "" && other.id != "" && u.id == other.id
}

// LogValue so slog.Info(..., "user_id", uid) needs no .String().
func (u UserID) LogValue() slog.Value {
	return slog.StringValue(u.id)
}
