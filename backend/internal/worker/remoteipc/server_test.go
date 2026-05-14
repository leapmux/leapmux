package remoteipc_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/remoteipc"
	"github.com/leapmux/leapmux/locallisten"
)

// shortSocketPath builds a path under os.TempDir() short enough to fit
// the platform's sun_path limit (~104 chars on macOS). t.TempDir()
// produces directories under /var/folders/.../T/<long-test-name>/...
// which routinely exceed the limit.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "lmx-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "ipc.sock")
}

// startTestServer spins up a per-agent IPC server on a unix socket and
// returns a ConnectRPC client dialled over it. The test owns the
// caller's raw token via opts.Token and `clientToken` so it can prove
// auth header round-trips correctly.
func startTestServer(t *testing.T, info remoteipc.TokenInfo, router *remoteipc.Router) (string, leapmuxv1connect.RemoteIPCServiceClient) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("server_test uses unix sockets; npipe variant is exercised separately")
	}

	sockURL := "unix:" + shortSocketPath(t)
	rawToken := remoteipc.MintToken()
	srv, err := remoteipc.Listen(remoteipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: info,
		Router:    router,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	dial, err := locallisten.Dialer(sockURL)
	require.NoError(t, err)
	transport := locallisten.NewLocalH2CTransport(dial)
	httpClient := &http.Client{
		Transport: &injectAuthHeader{token: rawToken, base: transport},
		Timeout:   5 * time.Second,
	}
	return rawToken, leapmuxv1connect.NewRemoteIPCServiceClient(httpClient, "http://leapmux-remote", connect.WithGRPC())
}

// injectAuthHeader is a small RoundTripper wrapper that adds the
// per-agent X-Leapmux-Token header to every request, mirroring what
// the CLI's transport does in production.
type injectAuthHeader struct {
	token string
	base  http.RoundTripper
}

func (i *injectAuthHeader) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(remoteipc.AuthHeader, i.token)
	return i.base.RoundTrip(req)
}

// startTestServerNoAuth returns a client that *does not* attach the
// X-Leapmux-Token header. Used to assert the auth gate rejects
// anonymous callers.
func startTestServerNoAuth(t *testing.T, info remoteipc.TokenInfo, router *remoteipc.Router) leapmuxv1connect.RemoteIPCServiceClient {
	t.Helper()
	sockURL := "unix:" + shortSocketPath(t)
	rawToken := remoteipc.MintToken()
	srv, err := remoteipc.Listen(remoteipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: info,
		Router:    router,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	dial, err := locallisten.Dialer(sockURL)
	require.NoError(t, err)
	transport := locallisten.NewLocalH2CTransport(dial)
	return leapmuxv1connect.NewRemoteIPCServiceClient(
		&http.Client{Transport: transport, Timeout: 5 * time.Second},
		"http://leapmux-remote", connect.WithGRPC())
}

func TestServer_Whoami_HappyPath(t *testing.T) {
	router := &remoteipc.Router{
		WorkerID: "worker-A", UserID: "u-1", WorkspaceIDs: []string{"ws-1"},
	}
	info := remoteipc.TokenInfo{
		UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A", TabID: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	}
	_, client := startTestServer(t, info, router)

	resp, err := client.Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "u-1", resp.Msg.GetUserId())
	assert.Equal(t, "ws-1", resp.Msg.GetWorkspaceId())
	assert.Equal(t, "worker-A", resp.Msg.GetWorkerId())
	assert.Equal(t, "agent-1", resp.Msg.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, resp.Msg.GetTabType())
	require.NotNil(t, resp.Msg.GetScope())
	assert.Equal(t, []string{"ws-1"}, resp.Msg.GetScope().GetWorkspaceIds())
}

func TestServer_Whoami_RejectsMissingToken(t *testing.T) {
	router := &remoteipc.Router{
		WorkerID: "worker-A", UserID: "u-1", WorkspaceIDs: []string{"ws-1"},
	}
	client := startTestServerNoAuth(t, remoteipc.TokenInfo{
		UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A",
	}, router)
	_, err := client.Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
	require.Error(t, err)
	// The auth middleware returns plain 401 — connect surfaces that as
	// CodeUnknown via the unaryUnknownErrorAdapter, with the body
	// "invalid token" / "missing token" preserved.
	assert.Contains(t, err.Error(), "401")
}

func TestServer_Whoami_RejectsWrongToken(t *testing.T) {
	router := &remoteipc.Router{
		WorkerID: "worker-A", UserID: "u-1", WorkspaceIDs: []string{"ws-1"},
	}
	sockURL := "unix:" + shortSocketPath(t)
	srv, err := remoteipc.Listen(remoteipc.Options{
		SocketURL: sockURL,
		Token:     remoteipc.MintToken(),
		TokenInfo: remoteipc.TokenInfo{UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A"},
		Router:    router,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	dial, err := locallisten.Dialer(sockURL)
	require.NoError(t, err)
	httpClient := &http.Client{
		Transport: &injectAuthHeader{
			token: "wrong-token-not-the-real-one",
			base:  locallisten.NewLocalH2CTransport(dial),
		},
		Timeout: 5 * time.Second,
	}
	client := leapmuxv1connect.NewRemoteIPCServiceClient(httpClient, "http://leapmux-remote", connect.WithGRPC())
	_, err = client.Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestServer_CallInner_RoutesThroughRouter(t *testing.T) {
	disp := &fakeLocalDispatcher{respPayload: []byte("local-resp")}
	router := &remoteipc.Router{
		WorkerID:        "worker-A",
		UserID:          "u-1",
		WorkspaceIDs:    []string{"ws-1"},
		LocalDispatcher: disp,
	}
	info := remoteipc.TokenInfo{
		UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A",
	}
	_, client := startTestServer(t, info, router)

	resp, err := client.CallInner(context.Background(), connect.NewRequest(&leapmuxv1.CallInnerRequest{
		Method:         "worker.OpenAgent",
		Payload:        []byte(`{"hello":"world"}`),
		TargetWorkerId: "worker-A",
		WorkspaceId:    "ws-1",
	}))
	require.NoError(t, err)
	assert.Equal(t, []byte("local-resp"), resp.Msg.GetPayload())
	assert.Equal(t, "OpenAgent", disp.gotMethod)
	assert.Equal(t, "u-1", disp.gotUserID)
}

func TestServer_CallInner_AllowsFilesystemOnSpawningWorker(t *testing.T) {
	disp := &fakeLocalDispatcher{respPayload: []byte("file-content")}
	router := &remoteipc.Router{
		WorkerID:        "worker-A",
		UserID:          "u-1",
		WorkspaceIDs:    []string{"ws-1"},
		LocalDispatcher: disp,
	}
	info := remoteipc.TokenInfo{UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A"}
	_, client := startTestServer(t, info, router)

	resp, err := client.CallInner(context.Background(), connect.NewRequest(&leapmuxv1.CallInnerRequest{
		Method:         "worker.ReadFile",
		Payload:        []byte(`{"path":"/etc/hosts"}`),
		TargetWorkerId: "worker-A", // same as spawning worker
		WorkspaceId:    "ws-1",
	}))
	require.NoError(t, err)
	assert.Equal(t, []byte("file-content"), resp.Msg.GetPayload())
}

func TestServer_StreamInner_DeliversFramesAndCancels(t *testing.T) {
	disp := &fakeLocalDispatcher{
		emitStream: [][]byte{[]byte("e1"), []byte("e2"), []byte("e3")},
	}
	router := &remoteipc.Router{
		WorkerID:        "worker-A",
		UserID:          "u-1",
		WorkspaceIDs:    []string{"ws-1"},
		LocalDispatcher: disp,
	}
	info := remoteipc.TokenInfo{UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A"}
	_, client := startTestServer(t, info, router)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := client.StreamInner(ctx, connect.NewRequest(&leapmuxv1.StreamInnerRequest{
		Method:          "worker.WatchEvents",
		Payload:         []byte(`{}`),
		TargetWorkerId:  "worker-A",
		WorkspaceId:     "ws-1",
		ClientRequestId: "req-stream-1",
	}))
	require.NoError(t, err)

	// Read the three frames the dispatcher emits, then cancel — the
	// router treats stream lifetime as ctx-scoped (matches the
	// production WatchEvents handler).
	for i := 0; i < 3; i++ {
		require.True(t, stream.Receive(), "expected frame %d", i)
		switch i {
		case 0:
			assert.Equal(t, []byte("e1"), stream.Msg().GetPayload())
		case 1:
			assert.Equal(t, []byte("e2"), stream.Msg().GetPayload())
		case 2:
			assert.Equal(t, []byte("e3"), stream.Msg().GetPayload())
		}
	}
	cancel()
	_ = stream.Close()
}

func TestServer_TokenRevokedAfterClose(t *testing.T) {
	router := &remoteipc.Router{
		WorkerID: "worker-A", UserID: "u-1", WorkspaceIDs: []string{"ws-1"},
	}
	info := remoteipc.TokenInfo{UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A"}

	sockURL := "unix:" + shortSocketPath(t)
	rawToken := remoteipc.MintToken()
	srv, err := remoteipc.Listen(remoteipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: info,
		Router:    router,
	})
	require.NoError(t, err)

	// Prove the server is up.
	dial, err := locallisten.Dialer(sockURL)
	require.NoError(t, err)
	httpClient := &http.Client{
		Transport: &injectAuthHeader{token: rawToken, base: locallisten.NewLocalH2CTransport(dial)},
		Timeout:   2 * time.Second,
	}
	client := leapmuxv1connect.NewRemoteIPCServiceClient(httpClient, "http://leapmux-remote", connect.WithGRPC())
	_, err = client.Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
	require.NoError(t, err)

	// Close: socket file is removed and listener is torn down. Any
	// subsequent dial via the same path must fail.
	require.NoError(t, srv.Close())

	dial2, err := locallisten.Dialer(sockURL)
	require.NoError(t, err)
	closed := &http.Client{
		Transport: &injectAuthHeader{token: rawToken, base: locallisten.NewLocalH2CTransport(dial2)},
		Timeout:   500 * time.Millisecond,
	}
	closedClient := leapmuxv1connect.NewRemoteIPCServiceClient(closed, "http://leapmux-remote", connect.WithGRPC())
	_, err = closedClient.Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
	assert.Error(t, err, "post-close dial must fail")
}

// Compile-time import sanity for http2 — the transport helper uses
// it transitively, but importing here keeps gofmt from rotating it.
var _ = http2.Transport{}

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
	router := &remoteipc.Router{
		WorkerID: "worker-A", UserID: "u-1", WorkspaceIDs: []string{"ws-1"},
	}
	info := remoteipc.TokenInfo{UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A"}
	sockPath := shortSocketPath(t)
	srv, err := remoteipc.Listen(remoteipc.Options{
		SocketURL: "unix:" + sockPath,
		Token:     remoteipc.MintToken(),
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

// TestServer_CallInner_RejectsCrossWorkspaceScope verifies that a
// request whose workspace_id does not match the bearer's scope is
// rejected before the underlying dispatcher is called. This is the
// "cross-user / cross-workspace denied" guarantee the plan calls out
// — the client cannot bypass scope by passing a different
// workspace_id in the request body.
func TestServer_CallInner_RejectsCrossWorkspaceScope(t *testing.T) {
	disp := &fakeLocalDispatcher{respPayload: []byte("MUST-NOT-FIRE")}
	// Router scope: only ws-1. Filter rejects anything else.
	router := &remoteipc.Router{
		WorkerID:        "worker-A",
		UserID:          "u-1",
		WorkspaceIDs:    []string{"ws-1"},
		LocalDispatcher: disp,
		WorkspaceFilter: func(ws string) bool { return ws == "ws-1" },
	}
	info := remoteipc.TokenInfo{
		UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A",
	}
	_, client := startTestServer(t, info, router)

	_, err := client.CallInner(context.Background(), connect.NewRequest(&leapmuxv1.CallInnerRequest{
		Method:         "worker.OpenAgent",
		Payload:        []byte(`{}`),
		TargetWorkerId: "worker-A",
		WorkspaceId:    "ws-2-other-tenant", // out of scope
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	// Dispatcher must never have been touched on a denied call,
	// otherwise the deny is informational, not enforcing.
	assert.Equal(t, "", disp.gotMethod)
}

// TestServer_StreamInner_AuthorizerLifecycle exercises the synthetic
// stream-id register/unregister cycle the plan calls out: when a
// streaming RPC starts, the router registers a `localipc:...` id with
// the LocalIPCAuthorizer; when the client cancels (or the server
// closes), that registration must be torn down. Without this, the
// WatchEvents handler would leak a workspace authorizer per stream.
//
// We use the production server (not the bare router) so the test
// covers the ConnectRPC → router path, not just the router unit test
// already present in router_test.go.
func TestServer_StreamInner_AuthorizerLifecycle(t *testing.T) {
	disp := &fakeLocalDispatcher{
		emitStream: [][]byte{[]byte("frame-1")},
	}
	authorizers := &serverAuthorizerSpy{}
	router := &remoteipc.Router{
		WorkerID:        "worker-A",
		UserID:          "u-1",
		WorkspaceIDs:    []string{"ws-1"},
		LocalDispatcher: disp,
		Authorizers:     authorizers,
	}
	info := remoteipc.TokenInfo{UserID: "u-1", WorkspaceID: "ws-1", WorkerID: "worker-A"}
	_, client := startTestServer(t, info, router)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.StreamInner(ctx, connect.NewRequest(&leapmuxv1.StreamInnerRequest{
		Method:          "worker.WatchEvents",
		Payload:         []byte(`{}`),
		TargetWorkerId:  "worker-A",
		WorkspaceId:     "ws-1",
		ClientRequestId: "req-stream-life",
	}))
	require.NoError(t, err)

	// Drain at least one frame so we know the dispatcher has been
	// invoked and the registration must have happened.
	require.True(t, stream.Receive(), "expected at least one frame")
	cancel()
	_ = stream.Close()

	// The cleanup runs asynchronously (ctx cancellation) so poll for a
	// short window before declaring the test failed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		regs := authorizers.snapshotRegistered()
		unregs := authorizers.snapshotUnregistered()
		if len(regs) > 0 && len(unregs) > 0 && regs[0] == unregs[0] {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	regs := authorizers.snapshotRegistered()
	unregs := authorizers.snapshotUnregistered()
	require.Len(t, regs, 1, "expected exactly one register call")
	require.Len(t, unregs, 1, "expected matching unregister on cancel")
	assert.Equal(t, regs[0], unregs[0], "register/unregister must use the same synthetic stream id")
	// The synthetic id must follow the documented "localipc:..." shape
	// so cleanup downstream can recognise it as a local stream.
	assert.True(t, len(regs[0]) > len("localipc:") && regs[0][:len("localipc:")] == "localipc:",
		"synthetic stream id should start with 'localipc:'; got %q", regs[0])
}

// serverAuthorizerSpy is a goroutine-safe implementation of the
// LocalIPCAuthorizer registration interface. The router_test.go fake
// is package-private; this duplicate keeps server_test.go independent.
type serverAuthorizerSpy struct {
	mu           sync.Mutex
	registered   []string
	unregistered []string
}

func (s *serverAuthorizerSpy) RegisterLocalAuthorizer(streamID string, _ []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = append(s.registered, streamID)
}
func (s *serverAuthorizerSpy) UnregisterLocalAuthorizer(streamID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unregistered = append(s.unregistered, streamID)
}
func (s *serverAuthorizerSpy) snapshotRegistered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.registered...)
}
func (s *serverAuthorizerSpy) snapshotUnregistered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.unregistered...)
}

// TestDefaultSocketPath_FitsSunPathLimit pins the production path
// shape's fit against the most restrictive unix sun_path limit (104
// bytes on macOS / *BSD). Worker and agent IDs in production are
// 48-char nanoids (`internal/util/id`), and macOS's default $TMPDIR
// is `/var/folders/<2 chars>/<32 chars>/T/` (~49 chars). Together
// these blow past 104 unless DefaultSocketPath truncates the IDs.
func TestDefaultSocketPath_FitsSunPathLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("npipe path is unaffected by sun_path")
	}
	// Mimic macOS's default $TMPDIR ($XDG_RUNTIME_DIR is unset there).
	const macTempDir = "/var/folders/ph/0qx1sm5d2w3dmmgckzz91wqr0000gn/T/"
	t.Setenv("TMPDIR", macTempDir)
	t.Setenv("XDG_RUNTIME_DIR", "")

	// Two distinct 48-char IDs in the production alphanumeric alphabet.
	workerID := "Q1RxdwkFExperhgYzjwosFq3PKOTUjVznHtStbfBp3MFxTAn"
	agentID := "hfMyi1Y4kjH2ZNfNd5j3H8z1OMQpxcbENB1IkzUF2aOk1XLk"
	require.Len(t, workerID, 48)
	require.Len(t, agentID, 48)

	socketURL := remoteipc.DefaultSocketPath(workerID, remoteipc.SocketKindAgent, agentID)
	require.True(t, len(socketURL) > len("unix:"))
	path := socketURL[len("unix:"):]

	// macOS / *BSD sun_path is 104 bytes including the NUL terminator,
	// so the path itself must be <= 103 chars.
	const sunPathLimit = 104
	assert.LessOrEqualf(t, len(path), sunPathLimit-1,
		"DefaultSocketPath produced %d-byte path %q, exceeds sun_path budget of %d",
		len(path), path, sunPathLimit-1)
}
