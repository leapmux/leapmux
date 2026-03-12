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

	agentResult, err := queries.DeleteClosedAgentsBefore(ctx, cutoff)
	if err != nil {
		slog.Error("cleanup: failed to delete old agents", "error", err)
	} else if n, _ := agentResult.RowsAffected(); n > 0 {
		slog.Info("cleanup: deleted old agents", "count", n)
	}

	termResult, err := queries.DeleteClosedTerminalsBefore(ctx, cutoff)
	if err != nil {
		slog.Error("cleanup: failed to delete old terminals", "error", err)
	} else if n, _ := termResult.RowsAffected(); n > 0 {
		slog.Info("cleanup: deleted old terminals", "count", n)
	}
}
