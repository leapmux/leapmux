package service

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"time"

	"github.com/leapmux/leapmux/internal/util/dbcleanup"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const (
	cleanupInterval  = 1 * time.Hour
	cleanupRetention = 7 * 24 * time.Hour
	cleanupJitter    = 5 * time.Minute
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

	dbcleanup.Step(ctx, "agents", func() (sql.Result, error) { return queries.DeleteClosedAgentsBefore(ctx, cutoff) })
	dbcleanup.Step(ctx, "terminals", func() (sql.Result, error) { return queries.DeleteClosedTerminalsBefore(ctx, cutoff) })
	dbcleanup.Step(ctx, "worktrees", func() (sql.Result, error) { return queries.HardDeleteWorktreesBefore(ctx, cutoff) })
}
