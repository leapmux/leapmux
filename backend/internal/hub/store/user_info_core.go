package store

import (
	"context"
	"time"
)

// UserInfoCacheFields projects the user columns cached in auth.UserInfo. A
// cross-process user_info cache eviction is required exactly when a user-row
// mutation changes one of these; comparing the projection before and after a
// mutation derives that decision mechanically, so a new cached field (add it
// here) or a new user mutation (route it through RunUserInfoMutation) is
// covered automatically instead of relying on each call site remembering to
// emit -- and staleness of email_verified / is_admin is a live auth gate.
type UserInfoCacheFields struct {
	Username      string
	Email         string
	EmailVerified bool
	IsAdmin       bool
}

// UserInfoCacheFieldsOf projects a full user row to the fields cached in
// auth.UserInfo. It is the single source of truth for "which user columns are
// cached", so adding a field here automatically widens every
// RunUserInfoMutation change check.
func UserInfoCacheFieldsOf(u User) UserInfoCacheFields {
	return UserInfoCacheFields{
		Username:      u.Username,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
		IsAdmin:       u.IsAdmin,
	}
}

// AuthGateReduced reports whether a UserInfoCacheFields transition is a
// privilege reduction on an auth gate (is_admin or email_verified true→false).
// Callers that opt into fencing escalate such a reduction from the soft
// user_info cache signal to a generation-bearing user_tokens revocation, which
// tears down the user's live streams and logs them out. Grants (false→true)
// and unrelated field changes return false.
func AuthGateReduced(before, after UserInfoCacheFields) bool {
	return (before.IsAdmin && !after.IsAdmin) || (before.EmailVerified && !after.EmailVerified)
}

// RunUserInfoMutation runs a user-row mutation inside a transaction and emits a
// durable user_info cache-invalidation event iff the mutation changed a cached
// UserInfo field, derived by comparing the projection loadFields reports before
// and after the change. This makes a forgotten invalidation mechanically
// impossible for any mutation routed through it and removes the need for callers
// to hand-signal "did a cached field change".
//
// When fence is non-nil and AuthGateReduced(before, after) is true, fence runs
// instead of emit so an opted-in auth-gate mutation can escalate a privilege
// reduction to a generation-bearing user_tokens revocation. A nil fence keeps
// the soft emit path for every change (including reductions).
//
// loadFields reports the target row's cached-field projection and whether the
// row exists. mutate runs the UPDATE and returns the row id, the updated_at to
// stamp the event with, and ok=false when it matched no row (a no-op, no event).
// A projection that is unchanged across an existing row emits nothing; if the
// row's existence flips around this id-keyed single-transaction update (not
// expected), the helper emits to stay fail-safe rather than risk serving a
// stale cross-process UserInfo.
func RunUserInfoMutation[C any](
	ctx context.Context,
	inTransaction func(context.Context, func(C) error) error,
	loadFields func(context.Context, C) (fields UserInfoCacheFields, exists bool, err error),
	mutate func(context.Context, C) (userID string, updatedAt time.Time, ok bool, err error),
	emit func(context.Context, C, string, time.Time) error,
	fence func(context.Context, C, string, time.Time) error,
) error {
	return inTransaction(ctx, func(conn C) error {
		before, existedBefore, err := loadFields(ctx, conn)
		if err != nil {
			return err
		}
		userID, updatedAt, ok, err := mutate(ctx, conn)
		if err != nil || !ok {
			return err
		}
		after, existedAfter, err := loadFields(ctx, conn)
		if err != nil {
			return err
		}
		if existedBefore && existedAfter {
			if before == after {
				return nil
			}
			if fence != nil && AuthGateReduced(before, after) {
				return fence(ctx, conn, userID, updatedAt)
			}
			return emit(ctx, conn, userID, updatedAt)
		}
		// Existence flipped around an id-keyed single-row update (not expected):
		// fail-safe to the soft emit, preserving prior behavior. A reduction that
		// somehow reaches here is only soft-emitted, never fenced -- unreachable
		// today because LockUserRow plus the deleted_at-filtered existence reads
		// keep a live mutation's before and after both existing.
		return emit(ctx, conn, userID, updatedAt)
	})
}
