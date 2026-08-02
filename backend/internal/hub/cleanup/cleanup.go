package cleanup

import (
	"context"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/periodic"
)

const (
	cleanupInterval                = 1 * time.Hour
	cleanupRetention               = 7 * 24 * time.Hour
	cleanupJitter                  = 5 * time.Minute
	maxRevocationCompactionBatches = 100
	// Bounded like maxRevocationCompactionBatches so one pass cannot monopolize
	// the cleanup goroutine; the next tick picks up whatever is left.
	maxOpBatchSweepBatches = 100
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
	// workspaces/workers reference users.
	cleanupStep("expired sessions", func() (int64, error) { return cs.HardDeleteExpiredSessions(ctx) })
	cleanupStep("workspaces", func() (int64, error) { return cs.HardDeleteWorkspacesBefore(ctx, cutoff) })
	cleanupStep("workers", func() (int64, error) { return cs.HardDeleteWorkersBefore(ctx, cutoff) })
	cleanupStep("expired registration keys", func() (int64, error) { return cs.HardDeleteExpiredRegistrationKeysBefore(ctx, cutoff) })
	cleanupStep("stale pending emails", func() (int64, error) { return cs.ClearStalePendingEmails(ctx, cutoff) })
	cleanupStep("users", func() (int64, error) { return cs.HardDeleteUsersBefore(ctx, cutoff) })
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
	// CRDT op-batch retention. Manager.maybeCompact deletes by HLC on the commit
	// path, but its tick short-circuits once compaction_watermark reaches
	// max_hlc -- so an account that stops being used keeps its final
	// OpRetentionTTL of batches (and their transitions_payload record
	// snapshots) forever. Drain that tail here on the shared schedule.
	//
	// The cutoff is expressed as an HLC physical, the SAME quantity
	// decideResume gates the resume cursor on, so this deletes exactly the set
	// a resume would refuse. Passing a wall clock instead would split one
	// invariant across two time domains; see the query's doc for what that
	// costs.
	opBatchCutoff := crdt.OpRetentionCutoffPhysicalMs(now, crdt.OpRetentionTTL)
	cleanupStep("crdt op batches", func() (int64, error) {
		return drainUntilEmpty(ctx, maxOpBatchSweepBatches, func() (int64, error) {
			return cs.DeleteUserOpBatchesBeforePhysical(ctx, opBatchCutoff)
		})
	})
	cleanupStep("published revocation events", func() (int64, error) {
		return drainUntilEmpty(ctx, maxRevocationCompactionBatches, func() (int64, error) {
			return cs.CompactPublishedRevocationEvents(ctx, store.CompactRevocationEventsParams{
				Cutoff: cutoff,
			})
		})
	})
}

// drainUntilEmpty runs a paged delete until it deletes nothing, a pass fails, the
// context is cancelled, or maxPasses is reached, and reports the running total.
//
// Terminating on deleted==0 rather than on a short page keeps every caller
// correct no matter what its query's internal LIMIT is, instead of duplicating
// that page size as a second source of truth in Go -- which could silently drift
// from the SQL and either stop the sweep early (a slow leak) or fire an extra
// no-op query. maxPasses is the runaway bound, not the expected pass count.
//
// A cancelled context returns the total WITHOUT an error: the rows already
// deleted are committed and the remainder is picked up by the next scheduled
// sweep, so shutdown mid-drain is a pause, not a failure worth logging.
func drainUntilEmpty(ctx context.Context, maxPasses int, pass func() (int64, error)) (int64, error) {
	var total int64
	for range maxPasses {
		if ctx.Err() != nil {
			return total, nil
		}
		deleted, err := pass()
		total += deleted
		if err != nil || deleted == 0 {
			return total, err
		}
	}
	return total, nil
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
