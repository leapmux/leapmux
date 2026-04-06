package cleanup

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"time"

	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/util/dbcleanup"
)

const (
	cleanupInterval  = 1 * time.Hour
	cleanupRetention = 7 * 24 * time.Hour
	cleanupJitter    = 5 * time.Minute
)

// StartLoop starts a background goroutine that periodically hard-deletes
// soft-deleted records that have been deleted for longer than the
// retention period. A random jitter of up to 5 minutes is added before
// each run to avoid contention if multiple instances start simultaneously.
func StartLoop(ctx context.Context, q *gendb.Queries) {
	go func() {
		jitteredRun(ctx, q)

		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				jitteredRun(ctx, q)
			}
		}
	}()
}

func jitteredRun(ctx context.Context, q *gendb.Queries) {
	jitter := time.Duration(rand.Int64N(int64(cleanupJitter)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}
	run(ctx, q)
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
