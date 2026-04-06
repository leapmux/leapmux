package dbcleanup

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// BatchDelay is the pause between batched DELETE iterations to avoid
// holding the SQLite write lock continuously.
const BatchDelay = 50 * time.Millisecond

// Step executes fn in a loop until it affects zero rows, logging the
// total count. Each fn call is expected to delete a bounded batch
// (e.g. LIMIT 1000). A short delay is inserted between iterations
// to yield the SQLite write lock for other writers. The loop exits
// early if ctx is canceled.
func Step(ctx context.Context, name string, fn func() (sql.Result, error)) {
	var total int64
loop:
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
		timer := time.NewTimer(BatchDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			break loop
		case <-timer.C:
		}
	}
	if total > 0 {
		slog.Info("cleanup: "+name, "count", total)
	}
}
