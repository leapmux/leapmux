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

	step("users", func() (sql.Result, error) { return q.HardDeleteUsersBefore(ctx, cutoff) })
	step("orgs", func() (sql.Result, error) { return q.HardDeleteOrgsBefore(ctx, cutoff) })
	step("workspaces", func() (sql.Result, error) { return q.HardDeleteWorkspacesBefore(ctx, cutoff) })
	step("workers", func() (sql.Result, error) { return q.HardDeleteWorkersBefore(ctx, cutoff) })
	step("expired registrations", func() (sql.Result, error) { return q.HardDeleteExpiredRegistrationsBefore(ctx, cutoff.Time) })
	step("expired sessions", func() (sql.Result, error) { return q.DeleteExpiredUserSessions(ctx) })
}

func step(name string, fn func() (sql.Result, error)) {
	res, err := fn()
	if err != nil {
		slog.Error("cleanup: "+name, "error", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("cleanup: "+name, "count", n)
	}
}
