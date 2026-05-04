package service

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/util/periodic"
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

func runCleanup(ctx context.Context, queries *db.Queries) {
	cutoff := sql.NullTime{
		Time:  time.Now().UTC().Add(-cleanupRetention),
		Valid: true,
	}

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
