package service

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

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

	cleanupStep("agents", func() (sql.Result, error) { return queries.DeleteClosedAgentsBefore(ctx, cutoff) })
	cleanupStep("terminals", func() (sql.Result, error) { return queries.DeleteClosedTerminalsBefore(ctx, cutoff) })
	cleanupStep("worktrees", func() (sql.Result, error) { return queries.HardDeleteWorktreesBefore(ctx, cutoff) })
}

func cleanupStep(name string, fn func() (sql.Result, error)) {
	var total int64
	for {
		res, err := fn()
		if err != nil {
			slog.Error("cleanup: "+name, "error", err)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			break
		}
		total += n
	}
	if total > 0 {
		slog.Info("cleanup: "+name, "count", total)
	}
}
