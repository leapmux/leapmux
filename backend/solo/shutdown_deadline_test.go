package solo

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/drain"
	"github.com/leapmux/leapmux/worker"
)

// The combined shutdown starts the one deadline that the Hub and the Worker
// share, before it cancels either of them.
func TestInstanceShutdown_StartsTheSharedDeadline(t *testing.T) {
	t.Parallel()

	// The mock clock never advances, so a started deadline reports its whole
	// budget left, and an unstarted one reports any limit unchanged.
	deadline := drain.NewDeadline(testutil.NewQuartzMock(t), lifecycle.ShutdownDrainTimeout)
	hubDone := make(chan struct{})
	close(hubDone)
	var leftAtWorkerCancel time.Duration
	inst := &Instance{
		cancelWorker:     func() { leftAtWorkerCancel = deadline.Cap(time.Hour) },
		cancelHub:        func() {},
		hubDone:          hubDone,
		shutdownDeadline: deadline,
	}

	inst.shutdown()

	assert.Equal(t, lifecycle.ShutdownDrainTimeout, leftAtWorkerCancel,
		"the deadline must start before the Worker is cancelled")
}

// The Worker drain takes its limit from the shared deadline too, so a drain
// that the deadline leaves no time for stops the Hub at once instead of
// holding it for workerDrainTimeout.
func TestInstanceShutdown_CapsTheWorkerDrainByTheDeadline(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	deadline := drain.NewDeadline(clock, lifecycle.ShutdownDrainTimeout)
	deadline.Start()
	clock.Advance(lifecycle.ShutdownDrainTimeout).MustWait(ctx)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hubDone := make(chan struct{})
	close(hubDone)
	var hubCancelled atomic.Bool
	inst := &Instance{
		cancelWorker:     func() {},
		cancelHub:        func() { hubCancelled.Store(true) },
		hubDone:          hubDone,
		shutdownDeadline: deadline,
	}
	// A Worker that never reports draining, against the full 30s backstop.
	inst.workerWork.Add()
	inst.workerDrained.Add()
	go func() {
		defer inst.workerWork.Done()
		<-release
	}()

	shutdownDone := make(chan struct{})
	logs := testutil.CaptureStderr(t, func() {
		logging.Setup()
		go func() {
			defer close(shutdownDone)
			inst.shutdown()
		}()
		select {
		case <-shutdownDone:
		case <-ctx.Done():
			t.Fatal("the Worker drain held the Hub past the shutdown deadline")
		}
	})

	assert.True(t, hubCancelled.Load(), "the Hub must be cancelled once the deadline leaves the drain no time")
	assert.Contains(t, logs, "did not finish before the hub shutdown deadline", "the drain must report that it gave up")
}

// One deadline for the whole combined instance: the Hub reads it from its
// configuration, and the Worker from its RunConfig. Two deadlines would give
// the Hub teardown a fresh budget after the Worker drain spent part of the
// first, and the persistence steps could then start past
// lifecycle.ShutdownDrainTimeout.
func TestSoloStart_SharesOneShutdownDeadlineBetweenTheHubAndTheWorker(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	workerDeadline := make(chan *drain.Deadline, 1)
	d.runWorker = func(ctx context.Context, rc worker.RunConfig) error {
		defer func() { _ = rc.Directory.Close() }()
		workerDeadline <- rc.ShutdownDeadline
		if rc.OnReady != nil {
			rc.OnReady()
		}
		if rc.OnConnectionState != nil {
			rc.OnConnectionState(worker.ConnectionState{Connected: true})
		}
		<-ctx.Done()
		if rc.Drained != nil {
			rc.Drained()
		}
		return nil
	}

	prepared, err := Prepare(cfg)
	require.NoError(t, err)
	root, err := lifecycle.AcquireDirectory(
		lifecycle.NewDirectoryOwner(prepared.role, prepared.dataDir), prepared.role, prepared.dataDir)
	require.NoError(t, err)
	inst, err := start(context.Background(), cfg, d.deps, prepared, root)
	require.NoError(t, err)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = inst.Stop()
		}
	})

	var fromWorker *drain.Deadline
	select {
	case fromWorker = <-workerDeadline:
	case <-time.After(soloTestTimeout):
		t.Fatal("the Worker never started")
	}
	require.NotNil(t, inst.shutdownDeadline)
	assert.Same(t, inst.shutdownDeadline, prepared.hubCfg.ShutdownDeadline, "the Hub must read the instance's deadline")
	assert.Same(t, inst.shutdownDeadline, fromWorker, "the Worker must read the instance's deadline")
	assert.Equal(t, time.Hour, inst.shutdownDeadline.Cap(time.Hour), "a running instance must not have started its deadline")

	stopped = true
	require.NoError(t, inst.Stop())
	left := inst.shutdownDeadline.Cap(time.Hour)
	assert.Positive(t, left, "an ordinary shutdown must finish well before the deadline")
	assert.LessOrEqual(t, left, lifecycle.ShutdownDrainTimeout, "Stop must start the shared deadline")
}
