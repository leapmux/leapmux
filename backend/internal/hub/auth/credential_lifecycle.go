package auth

import "time"

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
// that exact table-qualified bearer row.
func (e *CredentialLifecycleEffects) BearerRevoked(kind BearerKind, tokenID string) {
	if e == nil || !kind.IsValid() || tokenID == "" {
		return
	}
	ref := NewBearerRef(kind, tokenID)
	if e.contexts != nil {
		e.contexts.EvictBearer(ref)
	}
	if e.channels != nil {
		e.channels.CloseChannelsByBearer(ref)
	}
}

// BearerRotatedExtending invalidates the cached secret for a rotated bearer and
// extends its leases and open-channel expiries to newExpiresAt. The durable
// bearer row remains valid under a new secret, so its leases and channels are
// preserved (not closed) and re-armed at the prolonged deadline instead of the
// old one. Used by the in-process refresh path, which knows that deadline.
//
// Split from the watcher's cache-only path (BearerRotatedCacheOnly) so the
// "extend" versus "invalidate only" intent is explicit at each call site rather
// than selected by a zero-time sentinel -- the same zero value means
// "never-expires" in the lease/channel layer, so overloading it here conflated
// two orthogonal meanings.
func (e *CredentialLifecycleEffects) BearerRotatedExtending(kind BearerKind, tokenID string, newExpiresAt time.Time) {
	if e == nil || !kind.IsValid() || tokenID == "" {
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
// cross-process watcher backstop replaying a rotation performed on another Hub:
// the affected leases and channels live on the Hub that rotated (which already
// extended them in-process via BearerRotatedExtending), so this Hub only needs
// to drop its own stale cache entry for the bearer id.
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
func (e *CredentialLifecycleEffects) UserRevoked(userID string, userAuthGeneration int64) {
	if e == nil || userID == "" {
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
func (e *CredentialLifecycleEffects) RevokeUserPreservingSession(userID, sessionID string, generation int64) {
	e.preserveSession(sessionID, generation)
	e.UserRevoked(userID, generation)
}

// UserInfoInvalidated drops cached profile data without revoking credentials,
// canceling leases, or closing channels.
func (e *CredentialLifecycleEffects) UserInfoInvalidated(userID string) {
	if e == nil || userID == "" || e.contexts == nil {
		return
	}
	e.contexts.EvictByUserID(userID)
}
