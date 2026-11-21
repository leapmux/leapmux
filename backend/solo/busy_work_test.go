package solo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/worker"
)

func TestInstanceInspectBusyWork_ZeroValue(t *testing.T) {
	t.Parallel()

	assert.True(t, (*Instance)(nil).InspectBusyWork(context.Background()).IsEmpty())
	assert.True(t, (&Instance{}).InspectBusyWork(context.Background()).IsEmpty())
}

func TestInstanceInspectBusyWork_WiredInspectorAndExitCleanup(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()

	want := worker.BusyWorkSnapshot{
		Agents: []worker.BusyAgent{
			{
				TabID:                 "agent-1",
				Title:                 "Refactor auth",
				ActiveBackgroundTasks: []string{"Run unit tests"},
				ActiveChildAgents:     []string{"Codebase reviewer"},
			},
		},
		Terminals: []worker.BusyTerminal{
			{
				TabID:      "term-1",
				Title:      "Dev server",
				Processes:  []worker.BusyTerminalProcess{{PID: 4242, Name: "node"}},
				TotalCount: 1,
			},
		},
	}

	releaseWorker := make(chan struct{})
	workerDone := make(chan struct{})
	d.runWorker = func(ctx context.Context, runCfg worker.RunConfig) error {
		defer close(workerDone)
		defer func() {
			if runCfg.Directory != nil {
				_ = runCfg.Directory.Close()
			}
		}()
		require.NotNil(t, runCfg.OnBusyWorkInspector)
		runCfg.OnBusyWorkInspector(func(context.Context) worker.BusyWorkSnapshot {
			return want
		})
		if runCfg.OnReady != nil {
			runCfg.OnReady()
		}
		if runCfg.OnConnectionState != nil {
			runCfg.OnConnectionState(worker.ConnectionState{Connected: true, HubURL: runCfg.HubURL})
		}
		select {
		case <-releaseWorker:
			if runCfg.Drained != nil {
				runCfg.Drained()
			}
			return nil
		case <-ctx.Done():
			if runCfg.Drained != nil {
				runCfg.Drained()
			}
			return ctx.Err()
		}
	}

	testCtx := testutil.DeadlineContext(t)
	inst, err := startForTest(testCtx, cfg, d.deps)
	require.NoError(t, err)
	t.Cleanup(func() {
		select {
		case <-releaseWorker:
		default:
			close(releaseWorker)
		}
		_ = inst.Stop()
	})

	assert.Equal(t, want, inst.InspectBusyWork(testCtx))

	close(releaseWorker)
	select {
	case <-workerDone:
	case <-testCtx.Done():
		t.Fatal("stub worker did not exit after release")
	}
	select {
	case <-inst.workerWork.DoneChan():
	case <-testCtx.Done():
		t.Fatal("worker work counter did not drain after stub worker exit")
	}

	assert.True(t, inst.InspectBusyWork(testCtx).IsEmpty(), "inspector must clear when the worker goroutine exits")
}
