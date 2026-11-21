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
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/drain"
)

func TestBusyWorkSnapshot_IsEmptyAndFromService(t *testing.T) {
	t.Parallel()

	assert.True(t, (BusyWorkSnapshot{}).IsEmpty())
	assert.False(t, (BusyWorkSnapshot{
		Agents: []BusyAgent{{TabID: "agent-1", Title: "Refactor auth"}},
	}).IsEmpty())
	assert.False(t, (BusyWorkSnapshot{
		Terminals: []BusyTerminal{{TabID: "term-1", Title: "Dev server"}},
	}).IsEmpty())

	src := service.BusyWorkSnapshot{
		Agents: []service.BusyAgent{
			{
				TabID:                 "agent-1",
				Title:                 "Refactor auth",
				ActiveBackgroundTasks: []string{"Run unit tests"},
				ActiveChildAgents:     []string{"Codebase reviewer"},
			},
		},
		Terminals: []service.BusyTerminal{
			{
				TabID: "term-1",
				Title: "Dev server",
				Processes: []service.BusyTerminalProcess{
					{PID: 51234, Name: "node"},
					{PID: 51240, Name: "esbuild"},
				},
				TotalCount: 2,
			},
		},
	}

	got := busyWorkFromService(src)
	assert.False(t, got.IsEmpty())
	assert.Equal(t, BusyWorkSnapshot{
		Agents: []BusyAgent{
			{
				TabID:                 "agent-1",
				Title:                 "Refactor auth",
				ActiveBackgroundTasks: []string{"Run unit tests"},
				ActiveChildAgents:     []string{"Codebase reviewer"},
			},
		},
		Terminals: []BusyTerminal{
			{
				TabID: "term-1",
				Title: "Dev server",
				Processes: []BusyTerminalProcess{
					{PID: 51234, Name: "node"},
					{PID: 51240, Name: "esbuild"},
				},
				TotalCount: 2,
			},
		},
	}, got)
}

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
	var inspector BusyWorkInspector
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
			OnBusyWorkInspector: func(fn BusyWorkInspector) {
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
	assert.True(t, savedInspector(context.Background()).IsEmpty(), "post-shutdown inspector calls must return empty safely")
}
