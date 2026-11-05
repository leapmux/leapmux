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
	// Limited like maxRevocationCompactionBatches so one pass cannot monopolize
	// the cleanup goroutine; the next tick picks up whatever is left.
	maxOpBatchSweepBatches = 100
	// deregisterGracePeriod is how long a deregistering worker may wait for
	// its DEREGISTER instruction before the hub deletes it without telling
	// it. A worker offline for longer is refused with WORKER_DELETED at its next
	// connect, as a worker of a deleted user is. The worker then stops
	// connecting, and a standalone worker also clears its state. Once the row is
	// hard-deleted (cleanupRetention later), the hub no longer knows the token,
	// so a worker that connects after that retries until an operator stops it.
	deregisterGracePeriod = 7 * 24 * time.Hour
	// Limited like the sweeps above.
	maxStaleDeregistrationBatches = 100
)

// StartLoop starts a background goroutine that periodically hard-deletes
// soft-deleted records whose deletion is older than the retention period.
// It adds a random jitter of up to cleanupJitter before each run to avoid
// contention if multiple instances start simultaneously.
// StartLoop runs the periodic cleanup sweep and returns the channel that
// closes when the loop has stopped, so a shutdown can join a pass in flight
// before the store closes. See periodic.Start for the contract.
func StartLoop(ctx context.Context, st store.Store) <-chan struct{} {
	return periodic.Start(ctx, periodic.Schedule{Interval: cleanupInterval, Jitter: cleanupJitter}, func(ctx context.Context) {
		run(ctx, st)
	})
}

func run(ctx context.Context, st store.Store) {
	now := time.Now().UTC()
	cutoff := now.Add(-cleanupRetention)
	cs := st.Cleanup()

	// step skips everything after a cancelled context, so a shutdown that ends
	// the loop mid-pass does not walk the remaining steps into a closed store,
	// logging "cleanup step failed" at Error for each one. The order below
	// still respects FK dependencies: child rows before parent rows, and
	// workspaces/workers reference users.
	step := func(name string, fn func() (int64, error)) {
		if ctx.Err() != nil {
			return
		}
		cleanupStep(name, fn)
	}
	step("expired sessions", func() (int64, error) { return cs.HardDeleteExpiredSessions(ctx, now) })
	step("workspaces", func() (int64, error) { return cs.HardDeleteWorkspacesBefore(ctx, cutoff) })
	// Before the hard delete, so a worker deleted here waits out the normal
	// retention like every other soft-deleted worker.
	deregisterCutoff := now.Add(-deregisterGracePeriod)
	step("stale deregistrations", func() (int64, error) {
		return DrainUntilEmpty(ctx, maxStaleDeregistrationBatches, func() (int64, error) {
			return cs.CompleteStaleDeregistrations(ctx, deregisterCutoff)
		})
	})
	step("workers", func() (int64, error) { return cs.HardDeleteWorkersBefore(ctx, cutoff) })
	step("expired registration keys", func() (int64, error) { return cs.HardDeleteExpiredRegistrationKeysBefore(ctx, cutoff) })
	step("stale pending emails", func() (int64, error) { return cs.ClearStalePendingEmails(ctx, cutoff) })
	step("users", func() (int64, error) { return cs.HardDeleteUsersBefore(ctx, cutoff) })
	step("expired oauth states", func() (int64, error) { return cs.DeleteExpiredOAuthStates(ctx, now) })
	step("expired pending signups", func() (int64, error) { return cs.DeleteExpiredPendingOAuthSignups(ctx, now) })
	step("expired webauthn sessions", func() (int64, error) { return cs.DeleteExpiredWebAuthnSessions(ctx, now) })
	step("expired device authorizations", func() (int64, error) { return cs.DeleteExpiredDeviceAuthorizations(ctx, now) })
	step("expired OAuth authorization codes", func() (int64, error) {
		return cs.DeleteExpiredOAuthAuthorizationCodes(ctx, now)
	})
	// Hard-delete API tokens whose revocation is older than the retention
	// window. Same pattern as workspaces/users.
	step("revoked API tokens", func() (int64, error) { return cs.DeleteRevokedAPITokensBefore(ctx, cutoff) })
	// A live token whose access expiry passed AND whose refresh window closed
	// can never authenticate or renew again, so the row only records history.
	// BOTH deadlines, because bearer validation reads expires_at alone: an
	// administrator can issue a credential with a year of access and ninety
	// days of refresh, and sweeping on the refresh column alone would delete
	// it while it still worked. The sweep uses the SAME retention margin as
	// the revoked rows, deliberately: a user whose CLI stopped working asks
	// days later, and the row is what answers them.
	step("expired API tokens", func() (int64, error) { return cs.DeleteExpiredAPITokensBefore(ctx, cutoff) })
	// Delegation tokens are short-lived and high-churn (one per agent
	// spawn). Hard-delete revoked rows after the retention window so the
	// table does not grow without limit.
	step("revoked delegation tokens", func() (int64, error) { return cs.DeleteRevokedDelegationTokensBefore(ctx, cutoff) })
	// Expired delegation tokens (TTL passed without an explicit revoke)
	// are also worth pruning eagerly since they accumulate one-per-spawn.
	step("expired delegation tokens", func() (int64, error) { return cs.DeleteExpiredDelegationTokensBefore(ctx, now) })
	// Consumed captcha salts past their challenge expiry can no longer
	// verify anything, so the rows only add to table growth until this
	// sweep drops them.
	step("expired altcha salts", func() (int64, error) { return cs.DeleteExpiredAltchaSalts(ctx) })
	// CRDT op-batch retention. Manager.maybeCompact deletes by HLC on the commit
	// path, but its tick short-circuits once compaction_watermark reaches
	// max_hlc -- so an account that nobody uses any more keeps its final
	// OpRetentionTTL of batches (and their transitions_payload record
	// snapshots) forever. Drain that tail here on the shared schedule.
	//
	// The cutoff is expressed as an HLC physical, the SAME quantity
	// decideResume tests the resume cursor against, so this deletes exactly the set
	// a resume would refuse. Passing a wall clock instead would split one
	// invariant across two time domains; see the query's doc for what that
	// costs.
	opBatchCutoff := crdt.OpRetentionCutoffPhysicalMs(now, crdt.OpRetentionTTL)
	step("crdt op batches", func() (int64, error) {
		return DrainUntilEmpty(ctx, maxOpBatchSweepBatches, func() (int64, error) {
			return cs.DeleteUserOpBatchesBeforePhysical(ctx, opBatchCutoff)
		})
	})
	step("published revocation events", func() (int64, error) {
		return DrainUntilEmpty(ctx, maxRevocationCompactionBatches, func() (int64, error) {
			return cs.CompactPublishedRevocationEvents(ctx, store.CompactRevocationEventsParams{
				Cutoff: cutoff,
			})
		})
	})
}

// DrainUntilEmpty runs a paged delete until it deletes nothing, a pass fails,
// the caller cancels the context, or it reaches maxPasses, and reports the
// running total.
//
// Terminating on deleted==0 rather than on a short page keeps every caller
// correct no matter what its query's internal LIMIT is, instead of duplicating
// that page size as a second source of truth in Go -- which could silently drift
// from the SQL and either stop the sweep early (a slow leak) or fire an extra
// no-op query. maxPasses is the runaway limit, not the expected pass count.
//
// A cancelled context returns the total WITHOUT an error: the store already
// committed the deleted rows and the next scheduled sweep picks up the
// remainder, so shutdown mid-drain is a pause, not a failure worth logging.
func DrainUntilEmpty(ctx context.Context, maxPasses int, pass func() (int64, error)) (int64, error) {
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
