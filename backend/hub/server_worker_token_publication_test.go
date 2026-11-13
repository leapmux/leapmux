package hub

import (
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/testutil"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"
)

func TestServerLocalReissueRefusesAnOldPendingWorkerHello(t *testing.T) {
	server := startTestServer(t, &config.Config{})
	ctx := testutil.DeadlineContext(t)
	owner := storetest.SeedUser(t, server.store, "local-publication-owner")
	hello := workermgrtest.TestHello()
	credentials, err := server.RegisterWorker(ctx, owner.ID, hello.GetKeys())
	require.NoError(t, err)
	endpoint, err := hubtransport.New(server.localListenURLs[0])
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	dial := func(token string) *workerhub.WorkerStream {
		peer, err := workerhub.DialWorkerStream(ctx, endpoint.WebSocketClient(), endpoint.BaseURL(), token)
		require.NoError(t, err)
		t.Cleanup(func() { _ = peer.CloseNow() })
		return peer
	}
	old := dial(credentials.AuthToken)
	replacement, err := server.ReissueLocalWorkerToken(ctx, credentials.WorkerID)
	require.NoError(t, err)
	current := dial(replacement)
	message := &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Hello{Hello: hello}}
	require.NoError(t, current.Send(ctx, message))
	greeting, err := current.Receive(ctx)
	require.NoError(t, err)
	require.NotNil(t, greeting.GetWorkerIdentity())
	published := server.workerMgr.ConnForTrustedPath(credentials.WorkerID)
	require.NotNil(t, published)
	require.NoError(t, old.Send(ctx, message))
	_, err = old.Receive(ctx)
	require.Error(t, err, "a pre-reissue upgrade must not displace the current stream")
	var closed websocket.CloseError
	require.ErrorAs(t, err, &closed)
	assert.Equal(t, websocket.StatusPolicyViolation, closed.Code)
	assert.Equal(t, "worker_credential_replaced", closed.Reason)
	assert.Same(t, published, server.workerMgr.ConnForTrustedPath(credentials.WorkerID))
	select {
	case <-published.Done():
		t.Fatal("the stale hello fenced the current local Worker")
	default:
	}
}

func TestServerLocalReissueEndsAnEstablishedWorkerStream(t *testing.T) {
	server := startTestServer(t, &config.Config{})
	ctx := testutil.DeadlineContext(t)
	owner := storetest.SeedUser(t, server.store, "local-stream-cutoff-owner")
	hello := workermgrtest.TestHello()
	credentials, err := server.RegisterWorker(ctx, owner.ID, hello.GetKeys())
	require.NoError(t, err)
	endpoint, err := hubtransport.New(server.localListenURLs[0])
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	peer, err := workerhub.DialWorkerStream(ctx, endpoint.WebSocketClient(), endpoint.BaseURL(), credentials.AuthToken)
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.CloseNow() })
	require.NoError(t, peer.Send(ctx, &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Hello{Hello: hello}}))
	greeting, err := peer.Receive(ctx)
	require.NoError(t, err)
	require.NotNil(t, greeting.GetWorkerIdentity())
	established := server.workerMgr.ConnForTrustedPath(credentials.WorkerID)
	require.NotNil(t, established)
	replacement, err := server.ReissueLocalWorkerToken(ctx, credentials.WorkerID)
	require.NoError(t, err)
	require.NotEmpty(t, replacement)
	select {
	case <-established.Done():
	default:
		t.Fatal("successful local reissue must end the established stream")
	}
}
