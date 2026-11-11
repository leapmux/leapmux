package worker_test

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/worker/hub/fakehubtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/worker"
)

// refusingHub records each connection that receives a permanent refusal.
type refusingHub struct {
	reason   string
	connects atomic.Int32
}

func (h *refusingHub) start(t *testing.T) string {
	t.Helper()
	return fakehubtest.Start(t, func(_ context.Context, stream *fakehubtest.Stream) {
		h.connects.Add(1)
		_ = stream.Refuse(websocket.StatusPolicyViolation, h.reason)
	})
}

// deregisterWorker keeps the stream open until the Worker's own teardown ends it.
func deregisterWorker(ctx context.Context, stream *fakehubtest.Stream) {
	if _, err := stream.Receive(ctx); err != nil {
		return
	}
	if err := stream.Greet(ctx, "owner"); err != nil {
		return
	}
	if err := stream.Send(ctx, &leapmuxv1.ConnectResponse{RequestId: "deregister-1", Payload: &leapmuxv1.ConnectResponse_Deregister{Deregister: &leapmuxv1.DeregisterNotification{}}}); err != nil {
		return
	}
	_ = stream.ReadUntilClosed(ctx)
}

// The lines that a Run logs around its shutdown.
const (
	shutdownStartedLog  = "draining worker state for graceful shutdown"
	shutdownFinishedLog = "worker state drained"
	handlersJoinedLog   = "worker handlers joined"
)

// deregisterTrace records what the teardown had finished when each hook ran.
type deregisterTrace struct {
	deregistered, drained  atomic.Int32
	shutdownAtDeregister   atomic.Bool
	shutdownDoneAtDrained  atomic.Bool
	shutdownStartBeforeEnd atomic.Bool
}

// runConfig returns a RunConfig whose hooks fill the trace from the captured
// log, which is the one place a Run reports its shutdown from.
func (trace *deregisterTrace) runConfig(t *testing.T, logs *testutil.LogBuffer, hubURL string) worker.RunConfig {
	t.Helper()
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	return worker.RunConfig{
		HubURL:         hubURL,
		Directory:      workerLeaseForTest(t),
		AuthToken:      "token",
		CompositeKey:   key,
		WorkerID:       "worker-1",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		OnDeregister: func() error {
			trace.deregistered.Add(1)
			trace.shutdownAtDeregister.Store(strings.Contains(logs.String(), shutdownStartedLog))
			return nil
		},
		Drained: func() {
			trace.drained.Add(1)
			logged := logs.String()
			started := strings.Index(logged, shutdownStartedLog)
			finished := strings.Index(logged, shutdownFinishedLog)
			trace.shutdownDoneAtDrained.Store(finished >= 0)
			trace.shutdownStartBeforeEnd.Store(started >= 0 && started < finished)
			// Eventually because a joined goroutine can hold the frame for the
			// instant between its last statement and its return. The context is
			// live here, so only the shutdown can have ended the loops. A failure
			// does not stop this goroutine, which is what a hook needs.
			assert.Eventually(t, func() bool { return !goroutinesRunning(periodicLoopFrame) },
				10*time.Second, time.Millisecond,
				"the shutdown must end the periodic loops, because Run closes their database next")
		},
	}
}

// assertOrderedTeardown checks one deregistration: OnDeregister first, then the
// shutdown, then Drained, each exactly once.
func (trace *deregisterTrace) assertOrderedTeardown(t *testing.T) {
	t.Helper()
	assert.Equal(t, int32(1), trace.deregistered.Load(), "OnDeregister must run once")
	assert.False(t, trace.shutdownAtDeregister.Load(), "OnDeregister must run before the shutdown")
	assert.Equal(t, int32(1), trace.drained.Load(), "Drained must run once")
	assert.True(t, trace.shutdownDoneAtDrained.Load(), "Drained must run after the shutdown finished")
	assert.True(t, trace.shutdownStartBeforeEnd.Load(), "the shutdown must start before it finishes")
}

// A WORKER_DELETED refusal stops the Worker for good. Run lets the caller clear
// its state first, then runs the one shutdown sequence, and then reports Drained
// before it returns nil, with the caller's context still live.
//
// Not parallel: it captures the process-global default logger.
func TestRunStopsInOrderWhenTheHubDeletedTheWorker(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	hubService := &refusingHub{

		reason: channelwire.WorkerCloseReasonDeleted,
	}
	hubURL := hubService.start(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	trace := &deregisterTrace{}

	require.NoError(t, worker.Run(ctx, trace.runConfig(t, logs, hubURL)))

	require.NoError(t, ctx.Err(), "Run returned on the refusal, not on its context")
	assert.Equal(t, int32(1), hubService.connects.Load(), "a deletion is not retried")
	trace.assertOrderedTeardown(t)
}

// Run closes its database last, so it must join the handlers first. The join
// sits in a defer that Run registers before Wire assigns the Wiring, which makes
// this a test of the wiring as well: a defer that captured the nil Wiring would
// skip the join with no other trace.
//
// Not parallel: it captures the process-global default logger.
func TestRunJoinsHandlersAfterItsShutdown(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	hubService := &refusingHub{

		reason: channelwire.WorkerCloseReasonDeleted,
	}
	hubURL := hubService.start(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	trace := &deregisterTrace{}

	require.NoError(t, worker.Run(ctx, trace.runConfig(t, logs, hubURL)))

	logged := logs.String()
	joined := strings.Index(logged, handlersJoinedLog)
	require.GreaterOrEqual(t, joined, 0, "Run must join the handlers before it closes the database")
	assert.Greater(t, joined, strings.Index(logged, shutdownFinishedLog),
		"the join must follow the shutdown, which is what closes admission")
}

// The Hub's deregistration notification reaches the Worker on a live stream. The
// stream stays open, so Run ends only because the deregistration ended it.
//
// Not parallel: it captures the process-global default logger.
func TestRunStopsInOrderWhenTheHubDeregistersTheWorker(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	hubURL := fakehubtest.Start(t, deregisterWorker)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	trace := &deregisterTrace{}

	require.NoError(t, worker.Run(ctx, trace.runConfig(t, logs, hubURL)))

	// The acknowledgement is not asserted. The Hub deregisters whether or not it
	// lands, and this teardown can cancel the stream while the frame is in flight.
	require.NoError(t, ctx.Err(), "Run returned on the notification, not on its context")
	trace.assertOrderedTeardown(t)
}

// A Worker with no OnDeregister hook still stops on a deletion: the hook only
// adds work to the teardown.
func TestRunStopsWhenTheHubDeletedAWorkerWithNoHook(t *testing.T) {
	t.Parallel()
	hubURL := (&refusingHub{reason: channelwire.WorkerCloseReasonDeleted}).start(t)
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	var drained atomic.Int32

	require.NoError(t, worker.Run(t.Context(), worker.RunConfig{
		HubURL:         hubURL,
		Directory:      workerLeaseForTest(t),
		AuthToken:      "token",
		CompositeKey:   key,
		WorkerID:       "worker-1",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		Drained:        func() { drained.Add(1) },
	}))
	assert.Equal(t, int32(1), drained.Load())
}

// HubEndpoint supplies the Hub destination and every reported connection URL.
// HubURL must not override it, even when HubURL is empty, invalid, or different.
func TestRunUsesTheHubEndpointItIsGiven(t *testing.T) {
	t.Parallel()
	for _, connection := range []struct {
		name  string
		greet bool
	}{{"refused", false}, {"greeted", true}} {
		t.Run(connection.name, func(t *testing.T) {
			for _, configured := range []struct {
				name string
				url  string
			}{{"empty", ""}, {"invalid", "not a URL"}, {"different", "http://unused.invalid"}} {
				t.Run(configured.name, func(t *testing.T) {
					t.Parallel()
					var connects atomic.Int32
					hubURL := fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
						connects.Add(1)
						if connection.greet {
							deregisterWorker(ctx, stream)
						} else {
							_ = stream.Refuse(websocket.StatusPolicyViolation, channelwire.WorkerCloseReasonDeleted)
						}
					})
					endpoint, err := hubtransport.New(hubURL)
					require.NoError(t, err)
					key, err := noiseutil.GenerateCompositeKeypair()
					require.NoError(t, err)
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					var states []worker.ConnectionState
					require.NoError(t, worker.Run(ctx, worker.RunConfig{
						HubURL:            configured.url,
						HubEndpoint:       endpoint,
						Directory:         workerLeaseForTest(t),
						AuthToken:         "token",
						CompositeKey:      key,
						WorkerID:          "worker-1",
						EncryptionMode:    leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
						OnConnectionState: func(state worker.ConnectionState) { states = append(states, state) },
					}))
					assert.Equal(t, int32(1), connects.Load())
					want := []bool{false}
					if connection.greet {
						want = []bool{true, false}
					}
					require.Len(t, states, len(want))
					for i, state := range states {
						assert.Equal(t, endpoint.URL(), state.HubURL)
						assert.Equal(t, want[i], state.Connected)
					}
				})
			}
		})
	}
}

// With neither a HubEndpoint nor a usable HubURL, Run has nothing to connect to.
func TestRunRefusesAnInvalidHubURLWithoutAnEndpoint(t *testing.T) {
	t.Parallel()
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	err = worker.Run(t.Context(), worker.RunConfig{
		HubURL:         "not a url",
		Directory:      workerLeaseForTest(t),
		AuthToken:      "token",
		CompositeKey:   key,
		WorkerID:       "worker-1",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub URL")
}

// periodicLoopFrame is the stack frame of a periodic.Start loop.
const periodicLoopFrame = "periodic.start"

// goroutinesRunning reports whether any goroutine's stack holds a frame whose
// function name contains frame.
func goroutinesRunning(frame string) bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Contains(string(buf[:n]), frame)
		}
		buf = make([]byte, 2*len(buf))
	}
}

// When the Hub refuses the Worker's keys, Run returns while its context is still
// live. What Run started must not wait for that context. The shutdown joined the
// loops that read the database, and Run joined the goroutine that waits for the
// context to run the shutdown. That goroutine must not call Drained after Run
// returned, because Drained tells the launcher that a LIVE shutdown finished, and
// Run already tore down the database that the shutdown reads. The delegation
// janitor and the clock-jump detector read no database, so Run only cancels them,
// and they end shortly after it returns.
//
// Not parallel: it reads the stacks of every goroutine in the process, so a
// parallel test's loops would read as this Run's.
func TestRunEndsWhatItStartedWhenTheHubRefusesItsKeys(t *testing.T) {
	hubService := &refusingHub{

		reason: channelwire.WorkerCloseReasonKeysDiffer,
	}
	hubURL := hubService.start(t)
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var drained, deregistered atomic.Int32

	err = worker.Run(ctx, worker.RunConfig{
		HubURL:         hubURL,
		Directory:      workerLeaseForTest(t),
		AuthToken:      "token",
		CompositeKey:   key,
		WorkerID:       "worker-1",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		Drained:        func() { drained.Add(1) },
		OnDeregister:   func() error { deregistered.Add(1); return nil },
	})
	require.ErrorIs(t, err, hub.ErrKeysRefused)
	assert.Zero(t, deregistered.Load(), "a key refusal must keep the worker's state")
	require.Equal(t, int32(1), hubService.connects.Load(), "a key refusal is not retried")
	require.NoError(t, ctx.Err(), "Run returned on the refusal, not on its context")

	for _, frame := range []string{periodicLoopFrame, "RunJanitor", "clockjump.StartLoop"} {
		require.Eventually(t, func() bool { return !goroutinesRunning(frame) },
			30*time.Second, 10*time.Millisecond, "%s outlived Run", frame)
	}

	// Ending the context now must reach nothing of the Run that already returned.
	cancel()
	require.Eventually(t, func() bool { return !goroutinesRunning("worker.run.func") },
		30*time.Second, 10*time.Millisecond, "a goroutine of Run outlived it")
	assert.Zero(t, drained.Load(), "Drained ran after Run returned")
}
