package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	stream := dialWorker(t, ctx, env.server, regResp.GetAuthToken())
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
