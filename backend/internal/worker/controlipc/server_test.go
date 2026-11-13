package controlipc_test

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// startTestServer spins up a per-agent IPC server on a local-IPC
// transport (unix socket on POSIX, named pipe on Windows) and returns
// a ConnectRPC client dialled over it. The test owns the caller's raw
// token via opts.Token and `clientToken` so it can prove auth header
// round-trips correctly.
func startTestServer(t *testing.T, info controlipc.TokenInfo, router *controlipc.Router) (string, leapmuxv1connect.ControlIPCServiceClient) {
	t.Helper()
	return listenTestServer(t, controlipc.Options{TokenInfo: info, Router: router})
}

// startTestServerWithOwnerUsername is startTestServer with a Whoami username
// lookup.
func startTestServerWithOwnerUsername(t *testing.T, info controlipc.TokenInfo, router *controlipc.Router, ownerUsername func(userid.UserID) string) leapmuxv1connect.ControlIPCServiceClient {
	t.Helper()
	_, client := listenTestServer(t, controlipc.Options{TokenInfo: info, Router: router, OwnerUsername: ownerUsername})
	return client
}

// listenTestServer starts a server from opts with a fresh socket and token, and
// returns the token and a client that sends it.
func listenTestServer(t *testing.T, opts controlipc.Options) (string, leapmuxv1connect.ControlIPCServiceClient) {
	t.Helper()
	sockURL := locallistentest.UniqueListenURL(t, "leapmux-controlipc-test")
	rawToken := controlipc.MintToken()
	opts.SocketURL = sockURL
	opts.Token = rawToken
	srv, err := controlipc.Listen(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	httpClient, baseURL := ipcClient(t, sockURL)
	httpClient.Transport = &injectAuthHeader{token: rawToken, base: httpClient.Transport}
	return rawToken, leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, baseURL, connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
}

// injectAuthHeader is a small RoundTripper wrapper that adds the
// per-agent X-LeapMux-Token header to every request, mirroring what
// the CLI's transport does in production.
type injectAuthHeader struct {
	token string
	base  http.RoundTripper
}

func (i *injectAuthHeader) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(controlipc.AuthHeader, i.token)
	return i.base.RoundTrip(req)
}

// startTestServerNoAuth returns a client that *does not* attach the
// X-LeapMux-Token header. Used to assert the auth gate rejects
// anonymous callers.
func startTestServerNoAuth(t *testing.T, info controlipc.TokenInfo, router *controlipc.Router) leapmuxv1connect.ControlIPCServiceClient {
	t.Helper()
	sockURL := locallistentest.UniqueListenURL(t, "leapmux-controlipc-test")
	rawToken := controlipc.MintToken()
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: info,
		Router:    router,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	httpClient, baseURL := ipcClient(t, sockURL)
	return leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, baseURL, connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
}

// ipcClient builds the client the CLI itself builds for a worker control-IPC
// socket -- hubtransport, h2c over the socket dialer -- and returns it with
// the base URL that transport dials against. Going through the production
// factory keeps these tests on the transport the CLI actually uses.
func ipcClient(t *testing.T, sockURL string) (*http.Client, string) {
	t.Helper()
	endpoint, err := hubtransport.New(sockURL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	return endpoint.UnaryClient(5 * time.Second), endpoint.BaseURL()
}

func TestServer_Whoami_HappyPath(t *testing.T) {
	router := &controlipc.Router{
		WorkerID: "worker-A", UserID: userid.MustNew("u-1"),
	}
	info := controlipc.TokenInfo{
		UserID: userid.MustNew("u-1"), WorkerID: "worker-A", TabID: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}
	_, client := startTestServer(t, info, router)

	resp, err := client.Whoami(context.Background(), &leapmuxv1.WhoamiRequest{})
	require.NoError(t, err)
	assert.Equal(t, "u-1", resp.GetUserId())
	assert.Equal(t, "worker-A", resp.GetWorkerId())
	assert.Equal(t, "agent-1", resp.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, resp.GetTabType())
	assert.Empty(t, resp.GetUsername(), "a server with no username lookup reports none")
}

// Whoami reports the username the worker holds for the token's user. The
// lookup runs at every call, so a process sees its owner's rename.
func TestServer_Whoami_ReportsTheOwnersUsername(t *testing.T) {
	router := &controlipc.Router{
		WorkerID: "worker-A", UserID: userid.MustNew("u-1"),
	}
	info := controlipc.TokenInfo{
		UserID: userid.MustNew("u-1"), WorkerID: "worker-A", TabID: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}
	var name atomic.Pointer[string]
	alice := "alice"
	name.Store(&alice)
	var asked []userid.UserID
	var mu sync.Mutex
	client := startTestServerWithOwnerUsername(t, info, router, func(user userid.UserID) string {
		mu.Lock()
		asked = append(asked, user)
		mu.Unlock()
		return *name.Load()
	})

	resp, err := client.Whoami(context.Background(), &leapmuxv1.WhoamiRequest{})
	require.NoError(t, err)
	assert.Equal(t, "alice", resp.GetUsername())

	renamed := "alice2"
	name.Store(&renamed)
	resp, err = client.Whoami(context.Background(), &leapmuxv1.WhoamiRequest{})
	require.NoError(t, err)
	assert.Equal(t, "alice2", resp.GetUsername(), "the username is read at every call")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, asked, 2)
	assert.True(t, asked[0].MatchesUser(info.UserID), "the lookup is for the token's user")
}

func TestServer_Whoami_RejectsMissingToken(t *testing.T) {
	router := &controlipc.Router{
		WorkerID: "worker-A", UserID: userid.MustNew("u-1"),
	}
	client := startTestServerNoAuth(t, controlipc.TokenInfo{
		UserID: userid.MustNew("u-1"), WorkerID: "worker-A",
	}, router)
	_, err := client.Whoami(context.Background(), &leapmuxv1.WhoamiRequest{})
	require.Error(t, err)
	// The auth middleware returns plain 401 — connect surfaces that as
	// CodeUnknown via the unaryUnknownErrorAdapter, with the body
	// "invalid token" / "missing token" preserved.
	assert.Contains(t, err.Error(), "401")
}

func TestServer_Whoami_RejectsWrongToken(t *testing.T) {
	router := &controlipc.Router{
		WorkerID: "worker-A", UserID: userid.MustNew("u-1"),
	}
	sockURL := locallistentest.UniqueListenURL(t, "leapmux-controlipc-test")
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: sockURL,
		Token:     controlipc.MintToken(),
		TokenInfo: controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A"},
		Router:    router,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	httpClient, baseURL := ipcClient(t, sockURL)
	httpClient.Transport = &injectAuthHeader{
		token: "wrong-token-not-the-real-one",
		base:  httpClient.Transport,
	}
	client := leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, baseURL, connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
	_, err = client.Whoami(context.Background(), &leapmuxv1.WhoamiRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestServer_CallInner_RoutesThroughRouter(t *testing.T) {
	disp := &fakeLocalDispatcher{respPayload: []byte("local-resp")}
	router := &controlipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("u-1"),
		LocalDispatcher: disp,
	}
	info := controlipc.TokenInfo{
		UserID: userid.MustNew("u-1"), WorkerID: "worker-A",
	}
	_, client := startTestServer(t, info, router)

	resp, err := client.CallInner(context.Background(), &leapmuxv1.CallInnerRequest{
		Method:         "worker." + leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure,
		Payload:        []byte(`{"hello":"world"}`),
		TargetWorkerId: "worker-A",
	})
	require.NoError(t, err)
	assert.Equal(t, []byte("local-resp"), resp.GetPayload())
	assert.Equal(t, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, disp.gotMethod)
	assert.Equal(t, "u-1", disp.gotUserID.String())
}

func TestServer_CallInner_AllowsFilesystemOnSpawningWorker(t *testing.T) {
	disp := &fakeLocalDispatcher{respPayload: []byte("file-content")}
	router := &controlipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("u-1"),
		LocalDispatcher: disp,
	}
	info := controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A"}
	_, client := startTestServer(t, info, router)

	resp, err := client.CallInner(context.Background(), &leapmuxv1.CallInnerRequest{
		Method:         "worker." + leapmuxv1connect.WorkerFileServiceReadFileProcedure,
		Payload:        []byte(`{"path":"/etc/hosts"}`),
		TargetWorkerId: "worker-A", // same as spawning worker
	})
	require.NoError(t, err)
	assert.Equal(t, []byte("file-content"), resp.GetPayload())
}

func TestServer_StreamInner_DeliversFramesAndCancels(t *testing.T) {
	disp := &fakeLocalDispatcher{
		emitStream: [][]byte{[]byte("e1"), []byte("e2"), []byte("e3")},
	}
	router := &controlipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("u-1"),
		LocalDispatcher: disp,
	}
	info := controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A"}
	_, client := startTestServer(t, info, router)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := client.StreamInner(ctx, &leapmuxv1.StreamInnerRequest{
		Method:          "worker." + leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure,
		Payload:         []byte(`{}`),
		TargetWorkerId:  "worker-A",
		ClientRequestId: "req-stream-1",
	})
	require.NoError(t, err)

	// Read the three frames the dispatcher emits, then cancel — the
	// router treats stream lifetime as ctx-scoped (matches the
	// production WatchEvents handler).
	for i := 0; i < 3; i++ {
		msg, err := stream.Receive()
		require.NoError(t, err, "expected frame %d", i)
		switch i {
		case 0:
			assert.Equal(t, []byte("e1"), msg.GetPayload())
		case 1:
			assert.Equal(t, []byte("e2"), msg.GetPayload())
		case 2:
			assert.Equal(t, []byte("e3"), msg.GetPayload())
		}
	}
	cancel()
	_ = stream.Close()
}

type closeTestDispatcher func(context.Context, channel.ResponseWriter)

func (f closeTestDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	f(ctx, w)
}

func TestServer_CloseCancelsActiveStream(t *testing.T) {
	captured := make(chan context.Context, 1)
	exited := make(chan struct{})
	router := &controlipc.Router{WorkerID: "worker-A", UserID: userid.MustNew("u-1")}
	router.LocalDispatcher = closeTestDispatcher(func(ctx context.Context, w channel.ResponseWriter) {
		captured <- ctx
		_ = w.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("ready")})
		<-ctx.Done()
		close(exited)
	})
	socketURL, token := locallistentest.UniqueListenURL(t, "leapmux-controlipc-test"), controlipc.MintToken()
	srv, err := controlipc.Listen(controlipc.Options{SocketURL: socketURL, Token: token, TokenInfo: controlipc.TokenInfo{UserID: router.UserID, WorkerID: router.WorkerID}, Router: router})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	httpClient, baseURL := ipcClient(t, socketURL)
	httpClient.Transport = &injectAuthHeader{token: token, base: httpClient.Transport}
	client := leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, baseURL, connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	t.Cleanup(cancel)
	stream, err := client.StreamInner(ctx, &leapmuxv1.StreamInnerRequest{Method: "worker." + leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, ClientRequestId: "closing"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	frame, err := stream.Receive()
	require.NoError(t, err)
	require.Equal(t, "ready", string(frame.GetPayload()))
	handlerCtx := <-captured
	require.NoError(t, srv.Close())
	assert.ErrorIs(t, handlerCtx.Err(), context.Canceled)
	// Cancel the client after checking server cancellation so a failed regression
	// cannot strand the handler or the test's transport.
	cancel()
	select {
	case <-exited:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the cancelled dispatcher did not exit")
	}
	_, err = stream.Receive()
	assert.Error(t, err)
	require.NoError(t, srv.Close())
}

func TestServer_HandlerCanCloseItsOwnServer(t *testing.T) {
	server := make(chan *controlipc.Server, 1)
	closed := make(chan error, 1)
	router := &controlipc.Router{WorkerID: "worker-A", UserID: userid.MustNew("u-1")}
	router.LocalDispatcher = closeTestDispatcher(func(_ context.Context, _ channel.ResponseWriter) { closed <- (<-server).Close() })
	socketURL, token := locallistentest.UniqueListenURL(t, "leapmux-controlipc-test"), controlipc.MintToken()
	srv, err := controlipc.Listen(controlipc.Options{SocketURL: socketURL, Token: token, TokenInfo: controlipc.TokenInfo{UserID: router.UserID, WorkerID: router.WorkerID}, Router: router})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	server <- srv
	httpClient, baseURL := ipcClient(t, socketURL)
	httpClient.Transport = &injectAuthHeader{token: token, base: httpClient.Transport}
	client := leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, baseURL, connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
	ctx := testutil.DeadlineContext(t)
	_, _ = client.CallInner(ctx, &leapmuxv1.CallInnerRequest{Method: "worker." + leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure})
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("closing the server waited for its own handler")
	}
}

// A client retires its stream with an UpdateStream that selects cancel,
// through the same service method that carries its revisions.
func TestServer_UpdateStreamCancelRetiresTheStream(t *testing.T) {
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	router := &controlipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("u-1"),
		LocalDispatcher: &slowStreamDispatcher{stop: stop, emitted: &atomic.Int32{}},
	}
	info := controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A"}
	_, client := startTestServer(t, info, router)

	ctx := testutil.DeadlineContext(t)
	stream, err := client.StreamInner(ctx, &leapmuxv1.StreamInnerRequest{
		Method:          "worker." + leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure,
		ClientRequestId: "req-retire",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	// The first frame proves the router registered the stream.
	_, err = stream.Receive()
	require.NoError(t, err)

	_, err = client.UpdateStream(ctx, &leapmuxv1.UpdateStreamRequest{
		ClientRequestId: "req-retire",
		Action:          &leapmuxv1.UpdateStreamRequest_Cancel{Cancel: &leapmuxv1.StreamCancel{}},
	})
	require.NoError(t, err)

	for {
		if _, err := stream.Receive(); err != nil {
			break
		}
	}
	require.NoError(t, ctx.Err(), "the stream ended because of the cancel, not the test deadline")
	_, registered := router.StreamCancellers.Load("req-retire")
	assert.False(t, registered, "the retired stream leaves no entry behind")
}

func TestServer_TokenRevokedAfterClose(t *testing.T) {
	router := &controlipc.Router{
		WorkerID: "worker-A", UserID: userid.MustNew("u-1"),
	}
	info := controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A"}

	sockURL := locallistentest.UniqueListenURL(t, "leapmux-controlipc-test")
	rawToken := controlipc.MintToken()
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: info,
		Router:    router,
	})
	require.NoError(t, err)

	// Prove the server is up.
	httpClient, baseURL := ipcClient(t, sockURL)
	httpClient.Transport = &injectAuthHeader{token: rawToken, base: httpClient.Transport}
	client := leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, baseURL, connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
	_, err = client.Whoami(context.Background(), &leapmuxv1.WhoamiRequest{})
	require.NoError(t, err)

	// Close: the listener is torn down (and on POSIX the socket file
	// removed; on Windows the named-pipe instance released). Any
	// subsequent dial via the same URL must fail.
	require.NoError(t, srv.Close())

	closed, closedBaseURL := ipcClient(t, sockURL)
	closed.Timeout = 500 * time.Millisecond
	closed.Transport = &injectAuthHeader{token: rawToken, base: closed.Transport}
	closedClient := leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(closed, closedBaseURL, connecthttp.WithReadMaxBytes(0), connecthttp.WithGRPC())))
	_, err = closedClient.Whoami(context.Background(), &leapmuxv1.WhoamiRequest{})
	assert.Error(t, err, "post-close dial must fail")
}

// TestServer_SocketIsMode0600 verifies the unix-socket file mode is
// 0600 after Listen restricts it. This is the only thing keeping a
// sibling-uid process on the same machine from connecting; if we ever
// regress to "default umask" mode the entire local-IPC threat model
// breaks. Skipped on Windows where named pipes use a different ACL
// model.
func TestServer_SocketIsMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes use DACLs, not POSIX file modes")
	}
	router := &controlipc.Router{
		WorkerID: "worker-A", UserID: userid.MustNew("u-1"),
	}
	info := controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A"}
	sockURL := locallistentest.UniqueListenURL(t, "leapmux-controlipc-test")
	sockPath := strings.TrimPrefix(sockURL, "unix:")
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: sockURL,
		Token:     controlipc.MintToken(),
		TokenInfo: info,
		Router:    router,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	st, err := os.Stat(sockPath)
	require.NoError(t, err)
	mode := st.Mode().Perm()
	assert.Equal(t, os.FileMode(0o600), mode,
		"socket file must be readable+writable by owner only; got %o", mode)
}

func TestServer_StreamInner_ReleasesLocalStreamAfterCancellation(t *testing.T) {
	dispatched := make(chan string, 1)
	releases := &serverLocalStreamReleaseSpy{done: make(chan struct{}, 1)}
	router := &controlipc.Router{
		WorkerID: "worker-A", UserID: userid.MustNew("u-1"),
		LocalDispatcher: serverStreamIdentityDispatcher{dispatched: dispatched}, Streams: releases,
	}
	info := controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A"}
	_, client := startTestServer(t, info, router)
	deadline := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(deadline)
	t.Cleanup(cancel)
	stream, err := client.StreamInner(ctx, &leapmuxv1.StreamInnerRequest{
		Method: "worker." + leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, Payload: []byte(`{}`),
		TargetWorkerId: "worker-A", ClientRequestId: "req-stream-life",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	frame, err := stream.Receive()
	require.NoError(t, err)
	assert.Equal(t, []byte("frame-1"), frame.GetPayload())
	var streamID string
	select {
	case streamID = <-dispatched:
	case <-deadline.Done():
		t.Fatal(deadline.Err())
	}
	require.NotEmpty(t, strings.TrimPrefix(streamID, "localipc:"))
	require.True(t, strings.HasPrefix(streamID, "localipc:"))
	assert.Empty(t, releases.snapshot())
	_, registered := router.StreamCancellers.Load("req-stream-life")
	require.True(t, registered)
	cancel()
	select {
	case <-releases.done:
	case <-deadline.Done():
		t.Fatal("local stream was not released: ", deadline.Err())
	}
	require.Eventually(t, func() bool {
		_, present := router.StreamCancellers.Load("req-stream-life")
		return !present
	}, 30*time.Second, time.Millisecond)
	assert.Equal(t, []string{streamID}, releases.snapshot())
}

// serverLocalStreamReleaseSpy observes only the release operation the router owns.
type serverLocalStreamReleaseSpy struct {
	mu       sync.Mutex
	released []string
	done     chan struct{}
}

func (s *serverLocalStreamReleaseSpy) ReleaseLocalStream(streamID string) {
	s.mu.Lock()
	s.released = append(s.released, streamID)
	s.mu.Unlock()
	select {
	case s.done <- struct{}{}:
	default:
	}
}
func (s *serverLocalStreamReleaseSpy) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.released...)
}

type serverStreamIdentityDispatcher struct{ dispatched chan<- string }

func (d serverStreamIdentityDispatcher) DispatchWith(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	d.dispatched <- writer.ChannelID()
	_ = writer.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("frame-1")})
}

func TestSocketURL_PlacesTheSocketInTheGivenDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses named pipes")
	}
	dir := "/private/control"
	url := controlipc.SocketURL(dir, strings.Repeat("w", 48), controlipc.SocketKindAgent, strings.Repeat("a", 48))
	assert.Equal(t, "unix:"+dir+"/a-aaaaaaaa.sock", url)
}

func TestSocketURL_DefaultMacOSLayoutFitsTheLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses named pipes")
	}
	dir := "/Users/" + strings.Repeat("u", 32) + "/.config/leapmux/solo/worker/run/control"
	url := controlipc.SocketURL(dir, strings.Repeat("w", 48), controlipc.SocketKindAgent, strings.Repeat("a", 48))
	assert.LessOrEqual(t, len(strings.TrimPrefix(url, "unix:")), 103)
}
