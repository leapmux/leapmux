package worker

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/drain"
)

// Run starts the shutdown deadline it was given when its context ends, before
// the shutdown that it then runs: every wait of that shutdown ends at the
// deadline, so the database close starts in time. Before the context ends,
// nothing starts it.
func TestRunStartsTheShutdownDeadlineWhenItsContextEnds(t *testing.T) {
	root, err := lifecycle.CanonicalDir(t.TempDir())
	require.NoError(t, err)
	lease, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, root), lifecycle.RoleWorker, root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lease.Close() })
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	waitCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(waitCtx)
	defer cancel()

	// The mock clock never advances, so a started deadline reports its whole
	// budget left, and an unstarted one reports any limit unchanged.
	deadline := drain.NewDeadline(testutil.NewQuartzMock(t), lifecycle.ShutdownDrainTimeout)
	var leftAtReady, leftAtClose time.Duration
	cleanup := runCleanup{
		closeDatabase: func(database *sql.DB) error {
			leftAtClose = deadline.Cap(time.Hour)
			return database.Close()
		},
		closeDirectory: (*lifecycle.DirectoryLease).Close,
	}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, RunConfig{
			HubURL: "http://127.0.0.1:1", Directory: lease, CompositeKey: key, AuthToken: "token", WorkerID: "worker",
			EncryptionMode:   leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			ShutdownDeadline: deadline,
			OnReady: func() {
				leftAtReady = deadline.Cap(time.Hour)
				cancel()
			},
		}, cleanup)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-waitCtx.Done():
		t.Fatal("Run did not return after its context ended")
	}

	assert.Equal(t, time.Hour, leftAtReady, "a running worker must not have started its shutdown deadline")
	assert.Equal(t, lifecycle.ShutdownDrainTimeout, leftAtClose,
		"the end of the context must start the deadline before the database closes")
}
