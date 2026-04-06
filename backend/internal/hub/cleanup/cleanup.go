package cleanup

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
)

const (
	cleanupInterval  = 1 * time.Hour
	cleanupRetention = 7 * 24 * time.Hour
)

// StartLoop starts a background goroutine that periodically hard-deletes
// soft-deleted records that have been deleted for longer than the
// retention period.
func StartLoop(ctx context.Context, q *gendb.Queries) {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run(ctx, q)
			}
		}
	}()
}

func run(ctx context.Context, q *gendb.Queries) {
	cutoff := sql.NullTime{
		Time:  time.Now().UTC().Add(-cleanupRetention),
		Valid: true,
	}

	// Hard-delete soft-deleted users.
	if res, err := q.HardDeleteUsersBefore(ctx, cutoff); err != nil {
		slog.Error("cleanup: hard-delete users", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("cleanup: deleted old users", "count", n)
	}

	// Hard-delete soft-deleted orgs.
	if res, err := q.HardDeleteOrgsBefore(ctx, cutoff); err != nil {
		slog.Error("cleanup: hard-delete orgs", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("cleanup: deleted old orgs", "count", n)
	}

	// Hard-delete soft-deleted workspaces.
	if res, err := q.HardDeleteWorkspacesBefore(ctx, cutoff); err != nil {
		slog.Error("cleanup: hard-delete workspaces", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("cleanup: deleted old workspaces", "count", n)
	}

	// Hard-delete soft-deleted workers.
	if res, err := q.HardDeleteWorkersBefore(ctx, cutoff); err != nil {
		slog.Error("cleanup: hard-delete workers", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("cleanup: deleted old workers", "count", n)
	}

	// Hard-delete expired registrations.
	if res, err := q.HardDeleteExpiredRegistrationsBefore(ctx, cutoff.Time); err != nil {
		slog.Error("cleanup: hard-delete expired registrations", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("cleanup: deleted old registrations", "count", n)
	}

	// Hard-delete expired sessions.
	if res, err := q.DeleteExpiredUserSessions(ctx); err != nil {
		slog.Error("cleanup: hard-delete expired sessions", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("cleanup: deleted expired sessions", "count", n)
	}
}
