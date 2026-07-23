package auth

import (
	"context"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// ErrDelegationMinterUnknown is returned when a delegation credential records no
// minting worker at all. Callers map it to a permission denial; it is a permanent
// condition, never a retryable one.
//
// A minter that merely stopped being USABLE -- deleted, or no longer ACTIVE -- does
// not land here. That is not an unknown minter but a known one which lends no reach
// beyond itself, and ResolveDelegationWorkerScope says so with a minter-only scope
// rather than an error. Only "no minter recorded" is unanswerable, and detecting it
// needs no store round trip (see delegationMinter).
var ErrDelegationMinterUnknown = errors.New("delegation token has no usable minting worker")

// ErrDelegationWorkerOutOfScope is returned when a delegation bearer names a worker
// its minting worker is not entitled to reach -- a worker the token's identity was
// merely lent to. Callers map it to a permission denial.
var ErrDelegationWorkerOutOfScope = errors.New("delegation token cannot reach another user's worker")

// DelegationWorkerScope bounds WHICH workers a delegation bearer may reach.
//
// A worker mints delegation tokens carrying the identity of whichever user its
// tab was spawned for. Workspace access is owner-only and tab placement is
// gated on worker registration, so today that user is always the minting
// worker's own registrant -- but the token is a bearer credential, and nothing
// in WorkerCanUse alone stops a leaked one from being aimed at ANY worker its
// user owns: the worker would see sess.UserID == that user and treat the
// caller as them, handing out tunnels and files on a machine the minting
// worker was never meant to reach. The minting worker is the missing fact.
//
// Two cases are legitimate, and only these:
//   - the target IS the minting worker: an agent talking back to the host it runs
//     on, which is the common `leapmux remote` case; and
//   - the token's user owns the minting worker: the agent is that user's own, on
//     that user's own machine, so reaching their other workers is the cross-worker
//     feature working as intended.
//
// Anything else is a worker reaching a machine it was merely lent an identity for.
//
// The scope is a VALUE resolved once per request from the credential, rather than a
// predicate each entrypoint calls, because the two questions it answers live in
// different packages: the channel service asks it about one target worker, and the
// CRDT validator asks it about whichever worker ids a batch of ops references --
// and `crdt` deliberately does not import this package. A value both can hold keeps
// the RULE in one place while letting each caller apply it where it makes sense.
// The zero value is scopeDenyAll -- FAIL-CLOSED. A non-delegation credential's
// UNBOUNDED scope is therefore constructed explicitly (UnboundedScope), never left
// to the zero value, so a forgotten constructor or a dropped resolve error denies a
// delegation bearer every worker rather than handing it unbounded reach.
type DelegationWorkerScope struct {
	// kind is the scope's state; see scopeKind. Its zero value (scopeDenyAll) is
	// fail-closed. A single field, so contradictory states are unrepresentable.
	kind scopeKind
	// minterID is the minting worker, consulted only by scopeMinterOnly (whose
	// Allows also admits the minter itself). Empty for every other kind.
	minterID string
}

// scopeKind is the closed set of states a DelegationWorkerScope can hold. Encoding
// the state as one field (rather than three independent bools) makes contradictory
// combinations impossible to represent, and choosing scopeDenyAll as the zero value
// makes an unconstructed scope fail closed.
type scopeKind uint8

const (
	// scopeDenyAll reaches NO worker. It is the zero value, so a dropped error or a
	// default-constructed DelegationWorkerScope{} fails closed; DenyAllScope names it.
	scopeDenyAll scopeKind = iota
	// scopeUnbounded reaches EVERY worker: a non-delegation credential (session or
	// API token), which is not a delegation bearer at all. Must be constructed
	// explicitly (UnboundedScope) because the zero value now denies.
	scopeUnbounded
	// scopeMinterOnly reaches only the minting worker itself -- the token still talks
	// back to the host it runs on but has lost every cross-worker reach (its minter is
	// deregistering/UNSPECIFIED/deleted, or is not owned by the token's user).
	scopeMinterOnly
	// scopeOwnsMinter reaches every worker the token's user may use: the bearer is
	// that user's own agent on that user's own machine.
	scopeOwnsMinter
)

// DenyAllScope is the fail-closed scope every delegation-resolve error path returns
// alongside its error. It equals the zero value (scopeDenyAll), so a caller that
// drops the error still denies -- but naming it keeps the fail-closed intent legible
// at the call site, and pairs with UnboundedScope so the two poles read symmetrically.
func DenyAllScope() DelegationWorkerScope {
	return DelegationWorkerScope{kind: scopeDenyAll}
}

// UnboundedScope reaches every worker. It MUST be constructed explicitly for a
// non-delegation credential rather than left to the zero value, since the zero value
// now fails closed -- so a resolve path that forgets to call this denies (an
// availability failure the tests catch) rather than silently granting unbounded reach.
func UnboundedScope() DelegationWorkerScope {
	return DelegationWorkerScope{kind: scopeUnbounded}
}

// minterReachScope builds the scope for a resolved delegation bearer: scopeOwnsMinter
// (reaches every worker the token's user may use) when ownsMinter, else scopeMinterOnly
// (reaches only the minting worker itself).
func minterReachScope(minterID string, ownsMinter bool) DelegationWorkerScope {
	if ownsMinter {
		return DelegationWorkerScope{kind: scopeOwnsMinter}
	}
	return DelegationWorkerScope{kind: scopeMinterOnly, minterID: minterID}
}

// delegationMinter returns the minting worker recorded on user's credential.
//
// bounded=false means the credential is not a delegation bearer and carries no
// worker bound at all. A delegation credential with no recorded minter cannot be
// scoped, so it yields ErrDelegationMinterUnknown rather than being trusted --
// delegation_tokens.worker_id is NOT NULL and the sole mint path always records it,
// so an empty one means a code path dropped it.
//
// It is store-free by construction: every fact it reads is on the credential.
func delegationMinter(user *UserInfo) (minterID string, bounded bool, err error) {
	if user == nil || !user.Credential.IsDelegation() {
		return "", false, nil
	}
	minterID = user.Credential.WorkerScopeID()
	if minterID == "" {
		return "", false, fmt.Errorf("%w: no minting worker recorded", ErrDelegationMinterUnknown)
	}
	return minterID, true, nil
}

// CheckDelegationWorkerScope reports whether user may reach targetWorkerID,
// returning nil when it may and ErrDelegationWorkerOutOfScope when it may not.
//
// It is the single-target form used by worker-directed entrypoints that know their
// target up front. It answers the two arms that need no store round trip without
// one -- a non-delegation credential, and a target that IS the minter -- so the
// common `leapmux remote` case (an agent talking back to the host it runs on) pays
// for no query. That fast path is a strict SUBSET of DelegationWorkerScope.Allows'
// own target-is-minter clause, so the two can never disagree; it exists only to skip
// the lookup. It also means a deregistering minter stays reachable BY ITSELF, which
// is correct and costs nothing: reaching it still requires the target to be ACTIVE,
// which WorkerReachAuthorizer.AuthorizeWorkerReach checks separately.
func CheckDelegationWorkerScope(ctx context.Context, st store.Store, user *UserInfo, targetWorkerID string) error {
	minterID, bounded, err := delegationMinter(user)
	if err != nil {
		return err
	}
	if !bounded || minterID == targetWorkerID {
		return nil
	}
	scope, err := ResolveDelegationWorkerScope(ctx, st, user)
	if err != nil {
		return err
	}
	if !scope.Allows(targetWorkerID) {
		return ErrDelegationWorkerOutOfScope
	}
	return nil
}

// ResolveDelegationWorkerScope computes the worker bound for user's credential as a
// VALUE, for callers that must apply it to worker ids they do not know up front
// (the CRDT validator, which sees them one op at a time). Entrypoints with a single
// known target should use CheckDelegationWorkerScope, which avoids a store round
// trip on the common path.
//
// A non-delegation credential yields the zero (unbounded) scope. Every other
// credential fails closed, in one of two SHAPES -- and which one is deliberate:
//   - No recorded minter yields ErrDelegationMinterUnknown. Such a credential cannot
//     be scoped at all, so it is refused rather than trusted.
//     delegation_tokens.worker_id is NOT NULL and the sole mint path always records
//     it, so an empty one means a code path dropped it.
//   - A minter that is deregistering, UNSPECIFIED, or deleted yields a MINTER-ONLY
//     scope -- ownsMinter stays false -- and NOT an error: the token still reaches
//     the minter itself and loses every cross-worker reach. Narrowing the scope
//     rather than refusing is what makes this a permanent answer instead of one that
//     reads like a retryable fault; a deleted worker never comes back, so a client
//     must not be invited to hammer a decision that can never change.
//
// The cross-worker bar exists because deregistering a worker is the operator's one
// containment action against a compromised one: if its outstanding tokens kept
// reaching that user's OTHER machines for the rest of their TTL, the action would be
// inert. AuthorizeWorkerReach already refuses a non-ACTIVE TARGET; the minter is held
// to the same bar. Note this costs the compromised worker nothing it still has: the
// target-IS-minter case is separately gated on the target being ACTIVE, so a
// deregistering worker cannot be reached through it either way.
//
// A store fault is surfaced as-is so the caller can treat it as retryable rather
// than as a permanent deny-or-allow.
func ResolveDelegationWorkerScope(ctx context.Context, st store.Store, user *UserInfo) (DelegationWorkerScope, error) {
	minterID, bounded, err := delegationMinter(user)
	if err != nil {
		// A delegation bearer we cannot scope fails CLOSED, not open. DenyAllScope is
		// the zero value, so even a caller that drops this error and reads the returned
		// value still denies every worker -- the fail-closed guarantee is a property of
		// the type, not a convention each call site must uphold. Named here for legibility.
		return DenyAllScope(), err
	}
	if !bounded {
		// A non-delegation credential is UNBOUNDED. Constructed explicitly because the
		// zero value now fails closed (see UnboundedScope).
		return UnboundedScope(), nil
	}
	minter, err := st.Workers().GetByID(ctx, minterID)
	if err != nil {
		if isNotFound(err) {
			// GetByID filters soft-deleted rows, so a deleted minter lands here. It
			// is a permanent answer, not a store fault: scope it to the minter alone
			// rather than reporting a retryable internal error.
			return minterReachScope(minterID, false), nil
		}
		return DenyAllScope(), fmt.Errorf("load minting worker: %w", err)
	}
	ownsMinter := user.ID.Matches(minter.RegisteredBy) && minter.Status == leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE
	return minterReachScope(minterID, ownsMinter), nil
}

// Allows reports whether targetWorkerID is within this scope.
//
// An unbounded scope allows everything (see the type doc). An empty targetWorkerID
// is allowed by every scope EXCEPT deny-all, so callers clearing a worker reference
// need no special case; the caller's own predicate still decides whether an empty id
// means anything. A deny-all scope allows nothing, empty target included.
func (s DelegationWorkerScope) Allows(targetWorkerID string) bool {
	switch s.kind {
	case scopeUnbounded, scopeOwnsMinter:
		return true
	case scopeMinterOnly:
		// The token still reaches the minter itself (and a cleared, empty ref), but
		// nothing else.
		return targetWorkerID == "" || targetWorkerID == s.minterID
	default: // scopeDenyAll, including the fail-closed zero value.
		return false
	}
}

// IsBounded reports whether this scope constrains anything -- i.e. whether it is
// anything other than the unbounded scope a non-delegation credential gets.
//
// A deny-all scope IS bounded: it constrains everything (Allows returns false for
// every target). This MUST be reported here, not just honored in Allows, because
// the sole value-consumer -- workerScopePredicate -- collapses an !IsBounded scope
// to a nil (UNBOUNDED) predicate. Were deny-all to read as unbounded, a fail-closed
// scope handed to that predicate by a caller who dropped the resolve error would
// silently grant a delegation bearer every worker its user owns -- the exact hole
// the fail-closed zero value exists to close. Only scopeUnbounded is unbounded, so
// this is simply "not unbounded", and the deny-all-is-bounded guarantee is a
// property of the kind rather than of caller discipline.
func (s DelegationWorkerScope) IsBounded() bool { return s.kind != scopeUnbounded }
