package service_test

import (
	"context"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// Tests in this file do NOT call t.Parallel(), unlike the rest of this
// package. They swap slog's default handler to capture log output, and that
// handler is process-global: run alongside a sibling, the buffer they assert
// on collects whatever the sibling logged too. Same convention as
// worker_connector_service_internal_test.go, which cannot host this test
// because it needs this package's registration fixtures.

// TestConnect_DisconnectLogCarriesReasonWhenWorkerClosesStream drives the
// real Connect handler over a unix socket and pins the reason on the
// teardown log for the one quiet exit every healthy reconnect produces: the
// worker hanging up cleanly. Before the reason field this line said only
// THAT the worker disconnected, and diagnosing solo's 10s reconnect loop
// started from a log that could not tell a clean close, a transport failure,
// and a fenced connection apart.
func TestConnect_DisconnectLogCarriesReasonWhenWorkerClosesStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows")
	}
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

	connectorClient := leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(connecthttp.NewTransport(
		env.serveOverUnixSocket(t, "hub-disconnect-reason"), "http://localhost", connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	streamCtx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+worker.AuthToken)
	stream, err := connectorClient.Connect(streamCtx)
	if err != nil {
		t.Fatal(err)
	}
	// ConnectRPC only sends headers on the first Send, so the worker speaks first.
	require.NoError(t, stream.Send(helloRequest(classicHello(0x31))))
	_, err = stream.Receive() // the WorkerIdentity greeting
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend()) // the worker hangs up cleanly

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
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows")
	}
	logs := testutil.CaptureDefaultLogger(t)
	env := setupRegKeyEnv(t)

	token := env.login(t, "admin", "admin123")
	createResp, err := env.mgmtClient.CreateRegistrationKey(authedReq(context.Background(),
		&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.GetRegistrationKey())
	require.NoError(t, err)

	connectorClient := leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(connecthttp.NewTransport(
		env.serveOverUnixSocket(t, "hub-hello-refusal"), "http://localhost", connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
	streamCtx, info := connect.NewClientContext(testutil.DeadlineContext(t))
	info.RequestHeader().Set("Authorization", "Bearer "+regResp.GetAuthToken())
	stream, err := connectorClient.Connect(streamCtx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	// A heartbeat where the hello must be.
	require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))
	_, err = stream.Receive()
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Connect logs before it returns, and the refusal reaches the worker only
	// after Connect returns, so the line is already in the buffer.
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
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not available on Windows")
	}
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
			streamCtx, info := connect.NewClientContext(ctx)
			info.RequestHeader().Set("Authorization", "Bearer "+worker.AuthToken)
			connectorClient := leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(connecthttp.NewTransport(
				env.serveOverUnixSocket(t, "hub-deregister-wait"), "http://localhost", connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
			stream, err := connectorClient.Connect(streamCtx)
			require.NoError(t, err)
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
				require.NoError(t, stream.CloseSend())
				_, receiveErr := stream.Receive()
				require.ErrorIs(t, receiveErr, io.EOF)
				row, readErr := env.store.Workers().GetByIDIncludeDeleted(ctx, worker.ID)
				require.NoError(t, readErr)
				require.Nil(t, row.DeletedAt, "a worker must not be deleted without acknowledgement")
				select {
				case <-park.entered:
					t.Fatal("an unacknowledged delivery attempted deletion")
				default:
				}
				require.Contains(t, logs.String(), "worker did not acknowledge DEREGISTER")
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
				require.NoError(t, stream.CloseSend())
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
			if cause == "second hello" {
				require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(receiveErr))
			} else {
				require.ErrorIs(t, receiveErr, io.EOF)
			}
		})
	}
}
