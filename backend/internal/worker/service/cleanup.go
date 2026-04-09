package service

import (
	"context"
	"database/sql"
	"log/slog"
	"math/rand/v2"
	"time"

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
// than the retention period. A random jitter of up to 5 minutes is
// added before each run.
func StartCleanupLoop(ctx context.Context, queries *db.Queries) {
	go func() {
		jitteredRunCleanup(ctx, queries)

		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				jitteredRunCleanup(ctx, queries)
			}
		}
	}()
}

func jitteredRunCleanup(ctx context.Context, queries *db.Queries) {
	jitter := time.Duration(rand.Int64N(int64(cleanupJitter)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}
	runCleanup(ctx, queries)
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
