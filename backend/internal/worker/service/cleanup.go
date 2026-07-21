package service

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/util/periodic"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const (
	cleanupInterval   = 1 * time.Hour
	cleanupRetention  = 7 * 24 * time.Hour
	cleanupJitter     = 5 * time.Minute
	cleanupBatchDelay = 50 * time.Millisecond
)

// StartCleanupLoop starts a background goroutine that periodically
// hard-deletes agents and terminals that have been closed for longer
// than the retention period. A random jitter of up to cleanupJitter is
// added before each run.
func StartCleanupLoop(ctx context.Context, queries *db.Queries) {
	periodic.Start(ctx, periodic.Schedule{Interval: cleanupInterval, Jitter: cleanupJitter}, func(ctx context.Context) {
		runCleanup(ctx, queries)
	})
}

// StartOrphanSweepLoop starts a background goroutine that periodically reclaims the
// in-memory tracker state of agents the DB no longer lists as open (see
// SweepOrphanedAgentState). Shares the cleanup cadence and jitter.
func (svc *Context) StartOrphanSweepLoop(ctx context.Context) {
	periodic.Start(ctx, periodic.Schedule{Interval: cleanupInterval, Jitter: cleanupJitter}, func(context.Context) {
		svc.SweepOrphanedAgentState()
	})
}

// SweepOrphanedAgentState reclaims the in-memory tracker state of agents that are
// gone for good -- closed or deleted in the DB and not running -- but were never
// routed through a cleanup path. It backstops the per-exit handler, which
// deliberately keeps the trackers (ClearPendingControlRequests, not CleanupAgent) so
// a relaunch can consolidate notifications. Open-but-inactive agents are LEFT ALONE:
// their state is intentionally retained for a possible relaunch; only agents the DB no
// longer lists as open are reclaimed.
func (svc *Context) SweepOrphanedAgentState() {
	tracked := svc.Output.TrackedAgentIDs()
	if len(tracked) == 0 {
		return
	}
	openIDs, err := svc.Queries.ListAllOpenAgentIDs(bgCtx())
	if err != nil {
		slog.Error("orphan sweep: list open agents", "error", err)
		return
	}
	open := make(map[string]struct{}, len(openIDs))
	for _, id := range openIDs {
		open[id] = struct{}{}
	}
	var swept int
	for _, id := range tracked {
		if _, isOpen := open[id]; isOpen {
			continue // open agent: state retained for a possible relaunch
		}
		if svc.Agents.HasAgent(id) {
			continue // still running (defensive; a running agent should be open)
		}
		svc.Output.CleanupAgent(id)
		swept++
	}
	if swept > 0 {
		slog.Info("orphan sweep: reclaimed in-memory agent state", "count", swept)
	}
}

func runCleanup(ctx context.Context, queries *db.Queries) {
	// Bound as a SQLiteNullTime: the sweeps compare closed_at/deleted_at as raw
	// strings, so the cutoff must be byte-exact against the stored bytes.
	// SQLiteNullTime.Value() emits the canonical strftime layout; a raw time.Time
	// bind would serialize in the driver's own layout (and is now a compile
	// error) and skip every same-day row.
	cutoff := sqltime.SQLiteNullTimeOf(time.Now().Add(-cleanupRetention))

	cleanupStep(ctx, "agents", func() (sql.Result, error) { return queries.DeleteClosedAgentsBefore(ctx, cutoff) })
	cleanupStep(ctx, "terminals", func() (sql.Result, error) { return queries.DeleteClosedTerminalsBefore(ctx, cutoff) })
	cleanupStep(ctx, "worktrees", func() (sql.Result, error) { return queries.HardDeleteWorktreesBefore(ctx, cutoff) })
}

func cleanupStep(ctx context.Context, name string, fn func() (sql.Result, error)) {
	var total int64

loop:
	for {
		res, err := fn()
		if err != nil {
			slog.Error("cleanup step failed", "step", name, "error", err)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			break
		}
		total += n

		timer := time.NewTimer(cleanupBatchDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			break loop
		case <-timer.C:
		}
	}

	if total > 0 {
		slog.Info("cleanup step complete", "step", name, "count", total)
	}
}
