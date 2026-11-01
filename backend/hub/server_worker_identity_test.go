package hub

import (
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// A rename reaches a connected worker through the Hub's own wiring: the store
// writes a durable user_info event, the revocation watcher applies it, the
// credential lifecycle runs its user-info observer, and the connector sends
// the owner's workers a new WorkerIdentity. The service tests call
// RedeliverWorkerIdentity directly, so only this test fails when server.go
// stops registering the observer.
func TestServer_ARenameReachesTheOwnersConnectedWorker(t *testing.T) {
	t.Parallel()
	srv := startTestServer(t, &config.Config{})
	ctx := testutil.DeadlineContext(t)
	owner := storetest.SeedUser(t, srv.store, "identity-owner")
	credentials, err := srv.RegisterWorker(ctx, owner.ID)
	require.NoError(t, err)

	require.NotEmpty(t, srv.localListenURLs, "startTestServer always binds a local listener")
	endpoint, err := hubtransport.New(srv.localListenURLs[0])
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	connector := leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(connecthttp.NewTransport(
		endpoint.HTTP2OnlyClient(), endpoint.BaseURL(), connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
	streamCtx, call := connect.NewClientContext(ctx)
	call.RequestHeader().Set("Authorization", "Bearer "+credentials.AuthToken)
	stream, err := connector.Connect(streamCtx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.CloseSend() })
	require.NoError(t, stream.Send(&leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Hello{Hello: workermgrtest.TestHello()},
	}))

	greeting, err := stream.Receive()
	require.NoError(t, err)
	require.NotNil(t, greeting.GetWorkerIdentity(), "the first frame is the greeting")
	require.Equal(t, "identity-owner", greeting.GetWorkerIdentity().GetRegisteredByUsername())

	require.NoError(t, srv.store.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
		ID:          owner.ID,
		Username:    "identity-renamed",
		DisplayName: owner.DisplayName,
	}))

	for {
		frame, err := stream.Receive()
		require.NoError(t, err, "the stream ended before the renamed identity arrived")
		if identity := frame.GetWorkerIdentity(); identity != nil {
			assert.Equal(t, owner.ID, identity.GetRegisteredBy())
			assert.Equal(t, "identity-renamed", identity.GetRegisteredByUsername())
			return
		}
	}
}
