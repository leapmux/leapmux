package hub

import (
	"context"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

func TestServer_AWorkerRegistersAndConnectsThroughAnHTTP1OnlyProxy(t *testing.T) {
	t.Parallel()
	srv := startTestServer(t, &config.Config{})
	ctx := testutil.DeadlineContext(t)
	owner := storetest.SeedUser(t, srv.store, "proxy-worker-owner")
	key := id.Generate()
	require.NoError(t, srv.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{ID: key, CreatedBy: userid.MustNew(owner.ID), ExpiresAt: time.Now().Add(time.Hour)}))
	dial, err := locallisten.Dialer(srv.localListenURLs[0])
	require.NoError(t, err)
	destination, err := url.Parse(locallisten.LocalConnectURL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(destination)
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	transport := &http.Transport{Protocols: protocols, DialContext: locallisten.HTTPDialContext(dial)}
	t.Cleanup(transport.CloseIdleConnections)
	proxy.Transport = transport
	front := hubtransporttest.NewHTTP1Server(t, proxy)
	endpoint, err := hubtransport.New(front.URL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	requestCtx, call := connect.NewClientContext(ctx)
	call.RequestHeader().Set("Authorization", "Bearer "+key)
	hello := workermgrtest.TestHello()
	credentials, err := workerhub.NewConnectorClient(endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout), endpoint.BaseURL()).Register(requestCtx, &leapmuxv1.RegisterRequest{Keys: hello.GetKeys(), AttemptId: strings.Repeat("a", 32)})
	require.NoError(t, err)
	client := workerhub.New(endpoint)
	t.Cleanup(client.Stop)
	client.PublicKeys = hello.GetKeys()
	client.EncryptionMode = hello.GetEncryptionMode()
	connected := make(chan struct{}, 1)
	client.OnConnectionState = func(ok bool, _ error) {
		if ok {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	}
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.ConnectWithReconnect(connCtx, credentials.GetAuthToken()) }()
	select {
	case <-connected:
	case err := <-result:
		t.Fatalf("worker ended before connecting: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestServer_ShutdownTellsAConnectedWorkerAndEndsItsStream(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	srv, stop := startTestServerIn(t, &config.Config{}, t.TempDir(), locallistentest.UniqueListenURL(t, "worker-shutdown"))
	ctx := testutil.DeadlineContext(t)
	owner := storetest.SeedUser(t, srv.store, "shutdown-worker-owner")
	hello := workermgrtest.TestHello()
	credentials, err := srv.RegisterWorker(ctx, owner.ID, hello.GetKeys())
	require.NoError(t, err)
	endpoint, err := hubtransport.New(srv.localListenURLs[0])
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	stream, err := workerhub.DialWorkerStream(ctx, endpoint.WebSocketClient(), endpoint.BaseURL(), credentials.AuthToken)
	require.NoError(t, err)
	defer func() { _ = stream.CloseNow() }()
	require.NoError(t, stream.Send(ctx, &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_Hello{Hello: hello}}))
	greeting, err := stream.Receive(ctx)
	require.NoError(t, err)
	require.NotNil(t, greeting.GetWorkerIdentity())
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	for {
		notification, err := stream.Receive(ctx)
		require.NoError(t, err)
		if notification.GetHubShuttingDown() != nil {
			assert.Equal(t, int32(10), notification.GetHubShuttingDown().GetRetryDelaySeconds())
			break
		}
		require.NotNil(t, notification.GetWorkerIdentity(), "only an identity redelivery may precede shutdown")
	}
	_, err = stream.Receive(ctx)
	require.Error(t, err)
	assert.True(t, websocket.CloseStatus(err) == websocket.StatusNormalClosure || err == io.EOF, "unexpected stream end: %v", err)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("Hub shutdown did not drain its Worker stream")
	}
	assert.NotContains(t, logs.String(), "websocket handler drain timed out")
}
