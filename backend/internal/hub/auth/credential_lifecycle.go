package auth

import (
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
)

// warnBlankRevocationTarget records that an eviction-lane call was skipped for
// want of a user id.
//
// It exists because a blank target here is a SWALLOWED revocation rather than a
// no-op. This lane's polarity is inverted from an authorization check: on a
// grant path "no id" means deny, which is safe, but on an eviction path it means
// "revoke nothing" while every caller is told the operation succeeded. These
// methods return nothing, so the log is the only trace an operator whose
// containment action quietly did nothing would ever have.
func warnBlankRevocationTarget(op string, attrs ...any) {
	slog.Warn("revocation skipped: no user id", append([]any{"op", op}, attrs...)...)
}

// CredentialChannelCloser is the channel teardown surface required by
// credential lifecycle effects. Implementations must preserve generation
// selectivity for user-wide revocation.
type CredentialChannelCloser interface {
	CloseChannelsBySession(sessionID string) int
	CloseChannelsByBearer(ref BearerRef) int
	CloseChannelsByUserRevocation(userID string, userAuthGeneration int64) int
	// RestampSessionGeneration advances the recorded generation of a session's
	// channels so a following user-wide revocation spares the surviving session.
	RestampSessionGeneration(sessionID string, generation int64)
}

// CredentialLifecycleEffects applies the in-process consequences of a durable
// credential lifecycle event. Store mutation remains with the caller; this
// type centralizes cache invalidation, authenticated-lease cancellation, and
// channel teardown after that mutation commits or the watcher replays it.
type CredentialLifecycleEffects struct {
	contexts *AuthContextRegistry
	channels CredentialChannelCloser
	// reschedule extends (rather than tears down) channel expiry on a bearer
	// rotation, the same interface a session slide uses. Kept separate from
	// channels because rescheduling is a pure channel-manager op with no worker
	// close-notification, unlike the teardown methods on CredentialChannelCloser.
	reschedule ChannelExpiryRescheduler
}

func NewCredentialLifecycleEffects(
	contexts *AuthContextRegistry,
	channels CredentialChannelCloser,
	reschedule ChannelExpiryRescheduler,
) *CredentialLifecycleEffects {
	return &CredentialLifecycleEffects{contexts: contexts, channels: channels, reschedule: reschedule}
}

// SessionRevoked invalidates the session and terminates work authenticated by
// that exact session.
func (e *CredentialLifecycleEffects) SessionRevoked(sessionID string) {
	if e == nil || sessionID == "" {
		return
	}
	if e.contexts != nil {
		e.contexts.Evict(sessionID)
	}
	if e.channels != nil {
		e.channels.CloseChannelsBySession(sessionID)
	}
}

// BearerRevoked invalidates the bearer and terminates work authenticated by
// that exact table-qualified bearer row. A batch of one through
// BearerRevokedBatch, so the single and the batch share one implementation.
func (e *CredentialLifecycleEffects) BearerRevoked(kind BearerKind, tokenID string) {
	e.BearerRevokedBatch(kind, []string{tokenID})
}

// BearerRevokedBatch is BearerRevoked for a SET of rows: the registry
// eviction runs as ONE lock acquisition and ONE revocation-generation bump
// for the whole set, instead of one lock cycle per credential a revoking
// admin edit touched. The channel sweep stays per row -- the channel manager
// has no set-shaped close, and a revoked app's channels must each receive
// their own teardown and worker close-notification.
func (e *CredentialLifecycleEffects) BearerRevokedBatch(kind BearerKind, tokenIDs []string) {
	if e == nil || !kind.IsValid() || len(tokenIDs) == 0 {
		return
	}
	refs := make([]BearerRef, 0, len(tokenIDs))
	for _, tokenID := range tokenIDs {
		if tokenID == "" {
			continue
		}
		refs = append(refs, NewBearerRef(kind, tokenID))
	}
	if len(refs) == 0 {
		return
	}
	if e.contexts != nil {
		e.contexts.EvictBearers(refs)
	}
	if e.channels != nil {
		for _, ref := range refs {
			e.channels.CloseChannelsByBearer(ref)
		}
	}
}

// BearerRotated applies the whole effect of one refresh rotation: the cache, the
// leases, the channel expiries, and -- when the grant NARROWED -- the teardown
// a withdrawal of authority requires.
//
// ONE method taking `narrowed`, rather than a rotation call plus a separate
// rescope call. Split, a narrowing refresh whose caller made only the first
// call would leave every open channel running at the authority its owner had
// just withdrawn, and nothing would report it. Here the caller cannot express
// the rotation without also answering the question.
//
// A NARROWING closes the bearer's channels. An open Noise channel carries the
// scope set announced at its handshake, and the hub cannot renegotiate a session
// it cannot read, so closing it is the only way to take the authority back. The
// client reconnects immediately with the new grant, which costs a round trip;
// the alternative costs the guarantee.
//
// A WIDENING or an unchanged grant extends instead: the durable row remains
// valid under a new secret, so its leases and channels are preserved and
// re-armed at the prolonged deadline rather than the old one.
//
// Split from the watcher's cache-only path (BearerRotatedCacheOnly) so the
// "extend" versus "invalidate only" intent is explicit at each call site rather
// than selected by a zero-time sentinel -- the same zero value means
// "never-expires" in the lease/channel layer, so overloading it here conflated
// two orthogonal meanings.
func (e *CredentialLifecycleEffects) BearerRotated(kind BearerKind, tokenID string, newExpiresAt time.Time, narrowed bool) {
	if e == nil || !kind.IsValid() || tokenID == "" {
		return
	}
	if narrowed {
		// The full teardown, which is BearerRevoked's effect: the row is still
		// live, but every holder authorized under the wider grant must go.
		// Evicting the cache alone would leave an open channel serving the old
		// scope set for the rest of its life.
		e.BearerRevoked(kind, tokenID)
		return
	}
	ref := NewBearerRef(kind, tokenID)
	// The rotated access expiry is a finite store value; carry it as a typed
	// deadline to the lease, channel, and cache arms so they cannot disagree.
	deadline := DeadlineAt(newExpiresAt)
	if e.contexts != nil {
		// Record the extended deadline before evicting the cache, so a channel
		// opening in the validate->index window reads it via CurrentCredentialExpiry
		// and is armed at newExpiresAt rather than the stale connect-time deadline.
		e.contexts.RecordBearerExpiry(ref, deadline)
		e.contexts.InvalidateBearer(ref)
		e.contexts.RenewBearerLeases(ref, deadline)
	}
	if e.reschedule != nil {
		e.reschedule.RescheduleExpiryByBearer(ref, deadline)
	}
}

// BearerRotatedCacheOnly invalidates only the cached secret for a rotated
// bearer, leaving its leases and open channels untouched. Used by the
// watcher's idempotent replay of a rotation: the rotation already ran the
// full in-process effect (via BearerRotated) in the process that committed
// it, so a replay -- whether the same process catching a commit whose effect
// never landed, or the successor hub draining the stream -- only needs to
// drop its own stale cache entry for the bearer id.
//
// A NARROWING rotation replays through here exactly as a plain one does: the
// rotation event carries no widened/narrowed distinction, so the replay drops
// the cache and nothing else. That is sufficient under the singleton runtime
// lease, because only one hub serves at a time and a hub that loses the lease
// tears down with its channels. The residual gap is a rotation written to the
// store OUT OF BAND (a tool, or a predecessor): the serving hub's cache
// refreshes on the replay while its already-open channels ride to their close,
// limited by the channel's own lifetime. Making that immediate needs an event
// kind the watcher replays as a teardown, which this change does not add.
func (e *CredentialLifecycleEffects) BearerRotatedCacheOnly(kind BearerKind, tokenID string) {
	if e == nil || !kind.IsValid() || tokenID == "" {
		return
	}
	if e.contexts != nil {
		e.contexts.InvalidateBearer(NewBearerRef(kind, tokenID))
	}
}

// preserveSession advances the recorded generation of both holders tied to a
// session that is being kept alive across a user-wide generation bump (e.g. the
// acting session during a password change): its authenticated leases (the
// long-lived WebSocket connections) and its open channels. Restamping both is
// required so the subsequent UserRevoked -- which cancels leases and closes
// channels below the new generation -- does not tear down that session's own
// live connections. It must run before UserRevoked; RevokeUserPreservingSession
// pairs the two so that ordering is mechanical rather than caller-enforced.
func (e *CredentialLifecycleEffects) preserveSession(sessionID string, generation int64) {
	if e == nil || sessionID == "" || generation <= 0 {
		return
	}
	if e.contexts != nil {
		e.contexts.RestampSessionLeaseGeneration(sessionID, generation)
	}
	if e.channels != nil {
		e.channels.RestampSessionGeneration(sessionID, generation)
	}
}

// UserRevoked invalidates and closes credentials older than the committed user
// authentication generation. A non-positive generation means the committed
// generation is unknown (a malformed or legacy user_tokens event): rather than
// fail OPEN and silently lose the revocation, it is passed through to fail SAFE
// -- the registry and channel manager both treat a non-positive generation as
// "drop every current credential" via auth.ShouldEvictForUserGeneration. Only a
// genuinely absent target (nil effects or empty userID) is a no-op.
//
// The empty-userID guard below is POLARITY, not redundancy with userid.UserID:
// Matches is tuned for grant semantics, where false means "not authorized", but
// on this path false would mean "do not revoke" -- so a blank id must be
// refused up front rather than allowed to reach a comparison that silently
// evicts nothing while the caller reports a revocation. Same rule as
// interceptor.go's RevokeUserAuthContextAtGeneration and channelmgr's
// CloseByUserRevocation.
func (e *CredentialLifecycleEffects) UserRevoked(userID string, userAuthGeneration int64) {
	if e == nil {
		return
	}
	if userID == "" {
		warnBlankRevocationTarget("UserRevoked", "generation", userAuthGeneration)
		return
	}
	if e.contexts != nil {
		e.contexts.RevokeUserAuthContextAtGeneration(userID, userAuthGeneration)
	}
	if e.channels != nil {
		e.channels.CloseChannelsByUserRevocation(userID, userAuthGeneration)
	}
}

// RevokeUserPreservingSession runs the "preserve before revoke" sequence as a
// single atomic effect: it restamps the surviving session's leases and channels
// to the new generation (preserveSession) before revoking every credential of
// userID below that generation (UserRevoked). Folding the two into one method
// makes the required ordering mechanical -- a caller can no longer revoke before
// preserving, nor drop the preserve step. The sub-methods keep their own
// nil/empty guards, so an empty sessionID (a non-cookie caller) simply skips the
// preserve step while the user-wide revocation still runs.
//
// A blank userID is refused HERE rather than delegated to UserRevoked's own
// guard. Delegating would run preserveSession and skip the revocation --
// ADVANCING the acting session's lease and channel generation with no
// revocation below it to justify the restamp, so ShouldEvictForUserGeneration
// would then spare those holders from a later legitimate revocation at that
// generation. A revoke-path call whose only surviving effect is to extend
// credentials is the eviction polarity inverted: on this path "no target" must
// mean "do nothing at all", never "do the half that needs no target".
func (e *CredentialLifecycleEffects) RevokeUserPreservingSession(userID, sessionID string, generation int64) {
	if e == nil {
		return
	}
	if userID == "" {
		warnBlankRevocationTarget("RevokeUserPreservingSession", "generation", generation)
		return
	}
	e.preserveSession(sessionID, generation)
	e.UserRevoked(userID, generation)
}

// BearerRescoped applies a grant change made OUTSIDE a rotation.
//
// It takes BOTH sets rather than the new one alone, so the caller cannot pick
// the wrong effect: whether a change is a widening or a narrowing is a property
// of the pair, and a method that took only `after` would make every caller
// re-derive it, which is where the two would disagree.
//
// A WIDENING is cache-only: nothing already running exceeds the new grant.
// A NARROWING is the full teardown, for the reason BearerRotated gives -- an
// open Noise channel carries the scope set announced at its handshake, and the
// Hub cannot renegotiate a session it cannot read, so closing it is the only
// way to withdraw authority.
//
// The one write that narrows a stored grant today is the REFRESH leg, and it
// goes through BearerRotated instead, which folds this effect into the
// rotation so a narrowing refresh cannot leave channels running at the
// withdrawn authority. An app registration's `scopes` edit reaches this method
// through applyCeilingChange, which passes the grant NARROWED TO the ceiling
// either side of the edit: the stored consent column is untouched, so
// restoring the permission restores what the account already agreed to, and
// only each credential's reachable set moves. This is the effect rule for any
// write that moves one, kept beside the rotation it mirrors so the two cannot
// answer differently.
func (e *CredentialLifecycleEffects) BearerRescoped(kind BearerKind, tokenID string, before, after authscope.ScopeSet) {
	e.BearerRescopedBatch(kind, []RescopeOp{{TokenID: tokenID, Before: before, After: after}})
}

// RescopeOp pairs one credential's row id with the grant either side of the
// edit that moved it. Both sets, for the reason BearerRescoped states:
// widening-versus-narrowing is a property of the pair.
type RescopeOp struct {
	TokenID string
	Before  authscope.ScopeSet
	After   authscope.ScopeSet
}

// BearerRescopedBatch applies ONE ceiling edit to every credential it moved,
// with the widen-or-narrow decision stated once: the set is partitioned by the
// same `after.Contains(before)` rule the single states, the cache-only rows
// invalidate as one batch, and the narrowed rows take the full teardown as one
// batch. An edit touching N credentials therefore costs the validation hot
// path's mutex twice -- once per effect class -- rather than N times, and a
// future edit to the partition rule lands here alone.
func (e *CredentialLifecycleEffects) BearerRescopedBatch(kind BearerKind, ops []RescopeOp) {
	if e == nil || !kind.IsValid() || len(ops) == 0 {
		return
	}
	var cacheOnly, revoked []BearerRef
	for _, op := range ops {
		if op.TokenID == "" {
			continue
		}
		ref := NewBearerRef(kind, op.TokenID)
		if op.After.Contains(op.Before) {
			cacheOnly = append(cacheOnly, ref)
		} else {
			revoked = append(revoked, ref)
		}
	}
	if e.contexts != nil {
		if len(cacheOnly) > 0 {
			e.contexts.InvalidateBearers(cacheOnly)
		}
		if len(revoked) > 0 {
			e.contexts.EvictBearers(revoked)
		}
	}
	if e.channels != nil {
		for _, ref := range revoked {
			e.channels.CloseChannelsByBearer(ref)
		}
	}
}

// BearerElevationPolicyChanged drops the cached UserInfo of one credential
// after its APP's elevation_allowed flag moved.
//
// The flag is read at VALIDATION -- loadBearer zeroes the credential's
// elevation window when the app lacks it -- so turning it off is meant to
// close every live window on the next request. That is only true if the next
// request actually reaches loadBearer, and validateTokenCached holds the whole
// UserInfo for 30 seconds. Without this call the property the column's own
// comment states is off by up to that window, on exactly the write an owner
// makes because they no longer trust the app.
//
// CACHE-ONLY in both directions, and that is the difference from
// BearerRescoped. An elevation window controls Hub-side writes; it is not
// announced into a Noise channel and no open channel carries it, so there is
// nothing to tear down -- withdrawing it needs the next validation to read the
// row, and nothing more.
//
// Its body coincides with BearerRotatedCacheOnly's today, and it is a separate
// method rather than a call to it because the two answer different questions:
// that one is the watcher's idempotent replay of a rotation, and a change to
// either reason must not silently move the other.
func (e *CredentialLifecycleEffects) BearerElevationPolicyChanged(kind BearerKind, tokenID string) {
	e.BearerElevationPolicyChangedBatch(kind, []string{tokenID})
}

// BearerElevationPolicyChangedBatch is BearerElevationPolicyChanged for a SET
// of rows, as one registry invalidation: one lock acquisition and one
// generation bump for the whole flag change, instead of one lock cycle per
// credential of the app whose flag moved.
func (e *CredentialLifecycleEffects) BearerElevationPolicyChangedBatch(kind BearerKind, tokenIDs []string) {
	if e == nil || !kind.IsValid() || len(tokenIDs) == 0 {
		return
	}
	refs := make([]BearerRef, 0, len(tokenIDs))
	for _, tokenID := range tokenIDs {
		if tokenID == "" {
			continue
		}
		refs = append(refs, NewBearerRef(kind, tokenID))
	}
	if len(refs) > 0 && e.contexts != nil {
		e.contexts.InvalidateBearers(refs)
	}
}

// UserInfoInvalidated drops cached profile data without revoking credentials,
// canceling leases, or closing channels.
func (e *CredentialLifecycleEffects) UserInfoInvalidated(userID string) {
	if e == nil || e.contexts == nil {
		return
	}
	if userID == "" {
		warnBlankRevocationTarget("UserInfoInvalidated")
		return
	}
	e.contexts.EvictByUserID(userID)
}
