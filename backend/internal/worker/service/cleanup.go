package service

import (
	"context"
	"database/sql"
	"time"

	"github.com/leapmux/leapmux/internal/util/dbcleanup"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const (
	cleanupInterval  = 1 * time.Hour
	cleanupRetention = 7 * 24 * time.Hour
)

// StartCleanupLoop starts a background goroutine that periodically
// hard-deletes agents and terminals that have been closed for longer
// than the retention period.
func StartCleanupLoop(ctx context.Context, queries *db.Queries) {
	go func() {
		runCleanup(ctx, queries)

		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCleanup(ctx, queries)
			}
		}
	}()
}

func runCleanup(ctx context.Context, queries *db.Queries) {
	cutoff := sql.NullTime{
		Time:  time.Now().UTC().Add(-cleanupRetention),
		Valid: true,
	}

	dbcleanup.Step("agents", func() (sql.Result, error) { return queries.DeleteClosedAgentsBefore(ctx, cutoff) })
	dbcleanup.Step("terminals", func() (sql.Result, error) { return queries.DeleteClosedTerminalsBefore(ctx, cutoff) })
	dbcleanup.Step("worktrees", func() (sql.Result, error) { return queries.HardDeleteWorktreesBefore(ctx, cutoff) })
}
