package cleanup

import (
	"context"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/periodic"
)

const (
	cleanupInterval                = 1 * time.Hour
	cleanupRetention               = 7 * 24 * time.Hour
	cleanupJitter                  = 5 * time.Minute
	maxRevocationCompactionBatches = 100
)

// StartLoop starts a background goroutine that periodically hard-deletes
// soft-deleted records that have been deleted for longer than the
// retention period. A random jitter of up to cleanupJitter is added
// before each run to avoid contention if multiple instances start
// simultaneously.
func StartLoop(ctx context.Context, st store.Store) {
	periodic.Start(ctx, periodic.Schedule{Interval: cleanupInterval, Jitter: cleanupJitter}, func(ctx context.Context) {
		run(ctx, st)
	})
}

func run(ctx context.Context, st store.Store) {
	now := time.Now().UTC()
	cutoff := now.Add(-cleanupRetention)
	cs := st.Cleanup()

	// Order respects FK dependencies: child rows before parent rows.
	// workspaces/workers reference users; users reference orgs.
	cleanupStep("expired sessions", func() (int64, error) { return cs.HardDeleteExpiredSessions(ctx) })
	cleanupStep("workspaces", func() (int64, error) { return cs.HardDeleteWorkspacesBefore(ctx, cutoff) })
	cleanupStep("workers", func() (int64, error) { return cs.HardDeleteWorkersBefore(ctx, cutoff) })
	cleanupStep("expired registration keys", func() (int64, error) { return cs.HardDeleteExpiredRegistrationKeysBefore(ctx, cutoff) })
	cleanupStep("stale pending emails", func() (int64, error) { return cs.ClearStalePendingEmails(ctx, cutoff) })
	cleanupStep("users", func() (int64, error) { return cs.HardDeleteUsersBefore(ctx, cutoff) })
	cleanupStep("orgs", func() (int64, error) { return cs.HardDeleteOrgsBefore(ctx, cutoff) })
	cleanupStep("expired oauth states", func() (int64, error) { return cs.DeleteExpiredOAuthStates(ctx) })
	cleanupStep("expired pending signups", func() (int64, error) { return cs.DeleteExpiredPendingOAuthSignups(ctx) })
	cleanupStep("expired device authorizations", func() (int64, error) { return cs.DeleteExpiredDeviceAuthorizations(ctx, now) })
	cleanupStep("expired CLI authorization codes", func() (int64, error) {
		return cs.DeleteExpiredCLIAuthorizationCodes(ctx, now)
	})
	// Hard-delete API tokens that have been revoked for longer than the
	// retention window. Same pattern as workspaces/users.
	cleanupStep("revoked API tokens", func() (int64, error) { return cs.DeleteRevokedAPITokensBefore(ctx, cutoff) })
	// Delegation tokens are short-lived and high-churn (one per agent
	// spawn). Hard-delete revoked rows after the retention window so the
	// table doesn't grow without bound.
	cleanupStep("revoked delegation tokens", func() (int64, error) { return cs.DeleteRevokedDelegationTokensBefore(ctx, cutoff) })
	// Expired delegation tokens (TTL passed without an explicit revoke)
	// are also worth pruning eagerly since they accumulate one-per-spawn.
	cleanupStep("expired delegation tokens", func() (int64, error) { return cs.DeleteExpiredDelegationTokensBefore(ctx, now) })
	cleanupStep("published revocation events", func() (int64, error) {
		var total int64
		for range maxRevocationCompactionBatches {
			if ctx.Err() != nil {
				return total, nil
			}
			deleted, err := cs.CompactPublishedRevocationEvents(ctx, store.CompactRevocationEventsParams{
				Cutoff: cutoff,
			})
			total += deleted
			// Drain until a batch deletes nothing rather than stopping on a partial
			// page. The delete query caps each batch at its own internal LIMIT;
			// terminating on deleted==0 keeps this loop correct no matter what that
			// page size is, instead of assuming it equals the shared CleanupBatchLimit
			// constant -- which is a separate source of truth that could silently
			// drift from the SQL LIMIT and either stop compaction early (a slow leak)
			// or fire an extra no-op query.
			if err != nil || deleted == 0 {
				return total, err
			}
		}
		return total, nil
	})
}

func cleanupStep(name string, fn func() (int64, error)) {
	n, err := fn()
	if err != nil {
		slog.Error("cleanup step failed", "step", name, "error", err)
		return
	}
	if n > 0 {
		slog.Info("cleanup step complete", "step", name, "count", n)
	}
}
