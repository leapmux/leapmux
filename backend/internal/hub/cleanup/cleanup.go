package cleanup

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
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
func StartLoop(ctx context.Context, st store.Store) {
	go func() {
		jitteredRun(ctx, st)

		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				jitteredRun(ctx, st)
			}
		}
	}()
}

func jitteredRun(ctx context.Context, st store.Store) {
	jitter := time.Duration(rand.Int64N(int64(cleanupJitter)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}
	run(ctx, st)
}

func run(ctx context.Context, st store.Store) {
	cutoff := time.Now().UTC().Add(-cleanupRetention)
	cs := st.Cleanup()

	// Order respects FK dependencies: child rows before parent rows.
	// workspaces/workers reference users; users reference orgs.
	cleanupStep("expired sessions", func() (int64, error) { return cs.HardDeleteExpiredSessions(ctx) })
	cleanupStep("workspaces", func() (int64, error) { return cs.HardDeleteWorkspacesBefore(ctx, cutoff) })
	cleanupStep("workers", func() (int64, error) { return cs.HardDeleteWorkersBefore(ctx, cutoff) })
	cleanupStep("expired registration keys", func() (int64, error) { return cs.HardDeleteExpiredRegistrationKeysBefore(ctx, cutoff) })
	cleanupStep("stale pending emails", func() (int64, error) { return cs.ClearStalePendingEmails(ctx, cutoff) })
	cleanupStep("users", func() (int64, error) { return cs.HardDeleteUsersBefore(ctx, cutoff) })
	cleanupStep("orgs", func() (int64, error) { return cs.HardDeleteOrgsBefore(ctx, cutoff) })
	cleanupStep("expired oauth states", func() (int64, error) { return cs.DeleteExpiredOAuthStates(ctx) })
	cleanupStep("expired pending signups", func() (int64, error) { return cs.DeleteExpiredPendingOAuthSignups(ctx) })
}

func cleanupStep(name string, fn func() (int64, error)) {
	n, err := fn()
	if err != nil {
		slog.Error("cleanup step failed", "step", name, "error", err)
		return
	}
	if n > 0 {
		slog.Info("cleanup step complete", "step", name, "count", n)
	}
}
