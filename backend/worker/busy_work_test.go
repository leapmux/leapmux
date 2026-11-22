package worker

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/drain"
	"github.com/leapmux/leapmux/worker/busywork"
)

func TestRunBusyWorkInspector_LifecycleShutdownDrainAndRaceSafety(t *testing.T) {
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

	gate := &drain.Gate{}
	var orderMu sync.Mutex
	var order []string
	record := func(step string) {
		orderMu.Lock()
		order = append(order, step)
		orderMu.Unlock()
	}

	var inspectorMu sync.Mutex
	var inspector busywork.Inspector
	var gateOpenAtReady atomic.Bool
	var gateOpenAtClose atomic.Bool
	var callers sync.WaitGroup

	cleanup := runCleanup{
		inspectGate: gate,
		closeDatabase: func(database *sql.DB) error {
			if release, ok := gate.Begin(nil); ok {
				gateOpenAtClose.Store(true)
				release()
			}
			return database.Close()
		},
		closeDirectory: (*lifecycle.DirectoryLease).Close,
	}

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, RunConfig{
			HubURL:         "http://127.0.0.1:1",
			Directory:      lease,
			CompositeKey:   key,
			AuthToken:      "token",
			WorkerID:       "worker",
			EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			OnBusyWorkInspector: func(fn busywork.Inspector) {
				record("inspector")
				inspectorMu.Lock()
				inspector = fn
				inspectorMu.Unlock()
			},
			OnReady: func() {
				record("ready")
				if release, ok := gate.Begin(nil); ok {
					gateOpenAtReady.Store(true)
					release()
				}
				inspectorMu.Lock()
				fn := inspector
				inspectorMu.Unlock()
				require.NotNil(t, fn)

				// Call concurrently before and across context cancellation.
				started := make(chan struct{}, 4)
				for range 4 {
					callers.Go(func() {
						started <- struct{}{}
						for range 10 {
							_ = fn(context.Background())
						}
					})
				}
				for range 4 {
					<-started
				}
				_ = fn(context.Background())
				cancel()
			},
		}, cleanup)
	}()

	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-waitCtx.Done():
		t.Fatal("run did not return after its context ended")
	}
	callers.Wait()

	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	assert.Equal(t, []string{"inspector", "ready"}, gotOrder, "OnBusyWorkInspector must fire before OnReady")
	assert.True(t, gateOpenAtReady.Load(), "inspectGate must admit calls during OnReady")
	assert.False(t, gateOpenAtClose.Load(), "inspectGate must be closed before closeDatabase closes sqlDB")

	inspectorMu.Lock()
	savedInspector := inspector
	inspectorMu.Unlock()
	require.NotNil(t, savedInspector)
	assert.Zero(t, savedInspector(context.Background()), "post-shutdown inspector calls must return empty safely")
}
