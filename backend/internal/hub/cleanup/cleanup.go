package cleanup

import (
	"context"
	"database/sql"
	"time"

	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/util/dbcleanup"
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
		run(ctx, q)

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

	dbcleanup.Step(ctx, "users", func() (sql.Result, error) { return q.HardDeleteUsersBefore(ctx, cutoff) })
	dbcleanup.Step(ctx, "orgs", func() (sql.Result, error) { return q.HardDeleteOrgsBefore(ctx, cutoff) })
	dbcleanup.Step(ctx, "workspaces", func() (sql.Result, error) { return q.HardDeleteWorkspacesBefore(ctx, cutoff) })
	dbcleanup.Step(ctx, "workers", func() (sql.Result, error) { return q.HardDeleteWorkersBefore(ctx, cutoff) })
	dbcleanup.Step(ctx, "expired registrations", func() (sql.Result, error) { return q.HardDeleteExpiredRegistrationsBefore(ctx, cutoff.Time) })
	dbcleanup.Step(ctx, "expired sessions", func() (sql.Result, error) { return q.DeleteExpiredUserSessions(ctx) })
}
