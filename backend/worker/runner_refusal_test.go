package worker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/worker"
)

// refusingHub refuses every Connect the way the Hub refuses a Worker for good.
type refusingHub struct {
	leapmuxv1connect.UnimplementedWorkerConnectorServiceHandler
	code     connect.Code
	reason   leapmuxv1.WorkerConnectRefusalReason
	connects atomic.Int32
}

func (h *refusingHub) Connect(context.Context, leapmuxv1connect.WorkerConnectorServiceConnectServerStream) error {
	h.connects.Add(1)
	detail, err := connectproto.NewErrorDetail(&leapmuxv1.WorkerConnectRefusal{Reason: h.reason})
	if err != nil {
		return err
	}
	return connect.NewError(h.code, "the hub refuses this worker").WithDetail(detail)
}

// deregisteringHub accepts the Connect stream, tells the Worker to stop for
// good, and keeps the stream open until the Worker ends it. So the stream cannot
// end Run by itself: only the Worker's own teardown can.
type deregisteringHub struct {
	leapmuxv1connect.UnimplementedWorkerConnectorServiceHandler
}

func (h *deregisteringHub) Connect(ctx context.Context, stream leapmuxv1connect.WorkerConnectorServiceConnectServerStream) error {
	if err := stream.Send(&leapmuxv1.ConnectResponse{
		RequestId: "deregister-1",
		Payload:   &leapmuxv1.ConnectResponse_Deregister{Deregister: &leapmuxv1.DeregisterNotification{}},
	}); err != nil {
		return err
	}
	for {
		if _, err := stream.Receive(); err != nil {
			return ctx.Err()
		}
	}
}

// startFakeHub serves a WorkerConnectorService over h2c, which the Worker's
// Connect stream requires.
func startFakeHub(t *testing.T, service leapmuxv1connect.WorkerConnectorServiceHandler) string {
	t.Helper()
	mux := http.NewServeMux()
	server := connect.NewServer()
	leapmuxv1connect.RegisterWorkerConnectorServiceHandler(server, service)
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	httpServer := httptest.NewUnstartedServer(mux)
	httpServer.Config.Protocols = &http.Protocols{}
	httpServer.Config.Protocols.SetHTTP1(true)
	httpServer.Config.Protocols.SetUnencryptedHTTP2(true)
	httpServer.Start()
	t.Cleanup(httpServer.Close)
	return httpServer.URL
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
	canonical, err := lifecycle.CanonicalDir(t.TempDir())
	require.NoError(t, err)
	lease, leaseErr := lifecycle.AcquireDirectory(
		lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, canonical), lifecycle.RoleWorker, canonical)
	require.NoError(t, leaseErr)
	return worker.RunConfig{
		HubURL:         hubURL,
		Directory:      lease,
		AuthToken:      "token",
		CompositeKey:   key,
		WorkerID:       "worker-1",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		OnDeregister: func() {
			trace.deregistered.Add(1)
			trace.shutdownAtDeregister.Store(strings.Contains(logs.String(), shutdownStartedLog))
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
		code:   connect.CodeUnauthenticated,
		reason: leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED,
	}
	hubURL := startFakeHub(t, hubService)
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
		code:   connect.CodeUnauthenticated,
		reason: leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED,
	}
	hubURL := startFakeHub(t, hubService)
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
	hubURL := startFakeHub(t, &deregisteringHub{})
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
	hubURL := startFakeHub(t, &refusingHub{
		code:   connect.CodeUnauthenticated,
		reason: leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED,
	})
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

// HubEndpoint lets a caller share the endpoint that it registered through, so the
// Connect stream reuses its connection pool. HubURL then states nothing, and an
// invalid one must not matter.
func TestRunUsesTheHubEndpointItIsGiven(t *testing.T) {
	t.Parallel()
	hubService := &refusingHub{
		code:   connect.CodeUnauthenticated,
		reason: leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED,
	}
	endpoint, err := hubtransport.New(startFakeHub(t, hubService))
	require.NoError(t, err)
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	require.NoError(t, worker.Run(t.Context(), worker.RunConfig{
		HubEndpoint:    endpoint,
		Directory:      workerLeaseForTest(t),
		AuthToken:      "token",
		CompositeKey:   key,
		WorkerID:       "worker-1",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	}))
	assert.Equal(t, int32(1), hubService.connects.Load(), "Run must connect through the given endpoint")
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
		code:   connect.CodePermissionDenied,
		reason: leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_KEYS_DIFFER,
	}
	hubURL := startFakeHub(t, hubService)
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
		OnDeregister:   func() { deregistered.Add(1) },
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
	require.Eventually(t, func() bool { return !goroutinesRunning("worker.Run.func") },
		30*time.Second, 10*time.Millisecond, "a goroutine of Run outlived it")
	assert.Zero(t, drained.Load(), "Drained ran after Run returned")
}
