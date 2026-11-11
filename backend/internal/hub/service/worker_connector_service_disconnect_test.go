package service_test

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// Tests that capture slog's default logger do not call t.Parallel().
// The process-global logger would also capture messages from concurrent tests.
// These tests use this package's registration fixtures; the internal service
// test package cannot use those fixtures.

// TestConnect_DisconnectLogCarriesReasonWhenWorkerClosesStream drives the
// worker WebSocket handler over HTTP/1.1 and pins the reason on the
// teardown log for the one quiet exit every healthy reconnect produces: the
// worker hanging up cleanly. Before the reason field this line said only
// THAT the worker disconnected, and diagnosing solo's 10s reconnect loop
// started from a log that could not tell a clean close, a transport failure,
// and a fenced connection apart.
func TestConnect_DisconnectLogCarriesReasonWhenWorkerClosesStream(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	env := setupRegKeyEnv(t)

	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)
	worker, err := env.store.Workers().GetByID(context.Background(), regResp.GetWorkerId())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := dialWorker(t, ctx, env.server, worker.AuthToken)
	// The worker sends its hello before the Hub greets it.
	require.NoError(t, stream.Send(helloRequest(classicHello(0x31))))
	_, err = stream.Receive() // the WorkerIdentity greeting
	require.NoError(t, err)
	require.NoError(t, stream.Close()) // the worker hangs up cleanly

	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), `msg="worker disconnected"`) &&
			strings.Contains(logs.String(), "worker_id="+worker.ID) &&
			strings.Contains(logs.String(), `reason="worker closed the stream"`)
	}, 5*time.Second, 10*time.Millisecond,
		"the disconnect log must name the worker and classify its exit")
}

// TestConnect_LogsAHelloPhaseRefusal pins the log line of a stream that ends
// before a valid hello. The worker retries every such refusal, and before this
// line the Hub recorded nothing, so a worker that looped on a malformed hello
// left no trace of why.
func TestConnect_LogsAHelloPhaseRefusal(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	env := setupRegKeyEnv(t)

	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)

	stream := dialWorker(t, testutil.DeadlineContext(t), env.server, regResp.GetAuthToken())
	t.Cleanup(func() { _ = stream.Close() })
	// A tab inventory where the hello must be.
	require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{}},
	}))
	_, err = stream.Receive()
	require.Error(t, err)
	requireWorkerClose(t, err, websocket.StatusProtocolError, channelwire.WorkerCloseReasonInvalidMessage)

	// serveWorkerStream logs before returning to WorkerStreamHandler, which
	// then sends the close frame. The line is already in the buffer.
	line := logs.String()
	require.Contains(t, line, `level=WARN msg="worker stream ended before a valid hello"`)
	require.Contains(t, line, "worker_id="+regResp.GetWorkerId())
	require.Contains(t, line, "must be a hello", "the log carries the reason")
}

// parkingMarkStore parks the deregistration deletion, so the deliver
// goroutine -- and with it the connection's notifyDone -- stays inside the
// deletion until the test releases it.
type parkingMarkStore struct {
	store.Store
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *parkingMarkStore) Workers() store.WorkerStore {
	return &parkingMarkWorkers{WorkerStore: s.Store.Workers(), park: s}
}

type parkingMarkWorkers struct {
	store.WorkerStore
	park *parkingMarkStore
}

func (w *parkingMarkWorkers) MarkDeleted(ctx context.Context, workerID string) error {
	w.park.once.Do(func() { close(w.park.entered) })
	// Only the gate completes this simulated in-flight write. A deadline
	// must not release it before the test observes the handler's join.
	<-w.park.release
	return w.park.Store.Workers().MarkDeleted(context.WithoutCancel(ctx), workerID)
}

// Every handler exit joins an acknowledged DEREGISTER delivery before it
// unregisters. A worker that already removed its registration must not leave a
// deregistering Hub row because its detached deletion outlived the handler.
func TestConnectWaitsForDeregisterCompletionBeforeDisconnect(t *testing.T) {
	for _, cause := range []string{"shutdown", "worker EOF", "second hello", "EOF before acknowledgement"} {
		t.Run(cause, func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseFn()
			// Wrapped GLOBALLY: the delivery's MarkDeleted runs on the notifier's
			// store, which the connector-only wrapper never reaches.
			real := hubtestutil.OpenTestStore(t)
			park := &parkingMarkStore{Store: real, release: release, entered: make(chan struct{})}
			env := regKeyEnvFromStore(t, park, testConfig(), seedSMTP, regKeyEnvOptions{}, nil)

			token := env.login(t, "admin", "admin123")
			createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
				&leapmuxv1.CreateRegistrationKeyRequest{}, token))
			require.NoError(t, err)
			regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
			require.NoError(t, err)
			worker, err := env.store.Workers().GetByID(context.Background(), regResp.GetWorkerId())
			require.NoError(t, err)
			// The deregistering status is what starts the delivery on connect: the
			// row's deregistering_at is the durable intent.
			// The concrete store carries the test helper; the wrapper hides it behind
			// the plain interface.
			helperStore, ok := park.Store.(interface {
				TestHelper() store.TestHelper
			})
			require.True(t, ok, "the test store must expose the test helper")
			require.NoError(t, helperStore.TestHelper().SetDeregisteringAt(context.Background(), worker.ID, time.Now().UTC()))

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			stream := dialWorker(t, ctx, env.server, worker.AuthToken)
			require.NoError(t, stream.Send(helloRequest(classicHello(0x31))))
			_, err = stream.Receive() // the WorkerIdentity greeting
			require.NoError(t, err)
			defer func() { _ = stream.Close() }()
			conn := env.wMgr.ConnForTrustedPath(worker.ID)
			require.NotNil(t, conn)

			// The DEREGISTER instruction arrives on this same stream. The worker
			// acknowledges it, and the delivery walks into the parked deletion.
			var dereg *leapmuxv1.ConnectResponse
			for {
				frame, receiveErr := stream.Receive()
				require.NoError(t, receiveErr, "the DEREGISTER instruction must arrive")
				if frame.GetDeregister() != nil {
					dereg = frame
					break
				}
			}
			if cause == "EOF before acknowledgement" {
				require.NoError(t, stream.Close())
				_, receiveErr := stream.Receive()
				require.ErrorIs(t, receiveErr, net.ErrClosed)
				row, readErr := env.store.Workers().GetByIDIncludeDeleted(ctx, worker.ID)
				require.NoError(t, readErr)
				require.Nil(t, row.DeletedAt, "a worker must not be deleted without acknowledgement")
				select {
				case <-park.entered:
					t.Fatal("an unacknowledged delivery attempted deletion")
				default:
				}
				testutil.AssertEventually(t, func() bool { return strings.Contains(logs.String(), "worker did not acknowledge DEREGISTER") })
				require.NotContains(t, logs.String(), context.DeadlineExceeded.Error(), "EOF must cancel the pending send before its deadline")
				return
			}
			require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{
				RequestId: dereg.GetRequestId(),
				Payload:   &leapmuxv1.ConnectRequest_DeregisterAck{DeregisterAck: &leapmuxv1.DeregisterAck{}},
			}))
			select {
			case <-park.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the delivery never reached MarkDeleted")
			}

			switch cause {
			case "shutdown":
				env.wMgr.FenceAll()
			case "worker EOF":
				require.NoError(t, stream.Close())
			case "second hello":
				require.NoError(t, stream.Send(helloRequest(classicHello(0x31))))
			}
			select {
			case <-conn.Done():
			case <-ctx.Done():
				t.Fatal("the connection did not enter teardown")
			}
			// EOF and protocol errors unregister before fencing in the old code.
			// A joined delivery cancels first and keeps membership until the write ends.
			require.True(t, env.wMgr.OnlineForTrustedPath(worker.ID),
				"handler teardown must join the in-flight deletion before unregistering")
			require.NotContains(t, logs.String(), `msg="worker disconnected"`)

			// The deletion completes, and only then does the handler return: it
			// unregisters, and the disconnect log carries the joined-delivery exit.
			releaseFn()
			require.Eventually(t, func() bool {
				return !env.wMgr.OnlineForTrustedPath(worker.ID) &&
					strings.Contains(logs.String(), `msg="worker disconnected"`)
			}, 10*time.Second, 10*time.Millisecond,
				"the handler must return once the delivery finished")
			row, err := env.store.Workers().GetByIDIncludeDeleted(ctx, worker.ID)
			require.NoError(t, err)
			require.NotNil(t, row.DeletedAt, "deletion must commit before handler teardown finishes")
			_, receiveErr := stream.Receive()
			switch cause {
			case "worker EOF":
				require.ErrorIs(t, receiveErr, net.ErrClosed)
			case "second hello":
				requireWorkerClose(t, receiveErr, websocket.StatusProtocolError, channelwire.WorkerCloseReasonInvalidMessage)
			default:
				requireWorkerClose(t, receiveErr, websocket.StatusNormalClosure, "")
			}
		})
	}
}
