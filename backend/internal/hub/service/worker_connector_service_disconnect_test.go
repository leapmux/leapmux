package service_test

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
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
	createResp, err := env.mgmtClient.CreateRegistrationKey(context.Background(),
		authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token))
	require.NoError(t, err)
	regResp, err := env.registerWithKey(t, createResp.Msg.GetRegistrationKey())
	require.NoError(t, err)
	worker, err := env.store.Workers().GetByID(context.Background(), regResp.Msg.GetWorkerId())
	require.NoError(t, err)

	connectorClient := leapmuxv1connect.NewWorkerConnectorServiceClient(
		env.serveOverUnixSocket(t, "hub-disconnect-reason"), "http://localhost", connect.WithGRPC())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := connectorClient.Connect(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+worker.AuthToken)
	// ConnectRPC only sends headers on the first Send, so the worker speaks first.
	require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))
	_, err = stream.Receive() // the WorkerIdentity greeting
	require.NoError(t, err)
	require.NoError(t, stream.CloseRequest()) // the worker hangs up cleanly

	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), `msg="worker disconnected"`) &&
			strings.Contains(logs.String(), "worker_id="+worker.ID) &&
			strings.Contains(logs.String(), `reason="worker closed the stream"`)
	}, 5*time.Second, 10*time.Millisecond,
		"the disconnect log must name the worker and classify its exit")
}
