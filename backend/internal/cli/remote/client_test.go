package remote_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/remoteipc"
)

// shortIPCSocket builds a unix-socket path under os.TempDir() short
// enough to fit the platform's sun_path limit (~104 chars on macOS).
// t.TempDir() routinely produces directories that exceed it.
func shortIPCSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "lmx-cli-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return "unix:" + filepath.Join(dir, "ipc.sock")
}

// TestNewClientFromEnv_LocalWhoami exercises the full local-IPC path
// the CLI takes when invoked from a remote-enabled terminal tab:
// LEAPMUX_REMOTE_SOCK is parsed, the h2c transport dials the unix
// socket, and `remote whoami` reaches the per-agent IPC server.
//
// Regression coverage for "unavailable: http2: unsupported scheme" —
// the http2.Transport rejects any URL whose scheme isn't http(s), so
// passing the raw unix: URL through to connectrpc breaks every local
// RPC. The fix routes connectrpc through a placeholder http:// URL
// while the transport dials the real socket.
func TestNewClientFromEnv_LocalWhoami(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}

	sockURL := shortIPCSocket(t)
	rawToken := remoteipc.MintToken()
	info := remoteipc.TokenInfo{
		UserID:      userid.MustNew("u-1"),
		WorkerID:    "worker-A",
		WorkspaceID: "ws-1",
		TabID:       "term-1",
		TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
	}
	srv, err := remoteipc.Listen(remoteipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: info,
		Router:    &remoteipc.Router{WorkerID: "worker-A", UserID: userid.MustNew("u-1"), WorkspaceIDs: []string{"ws-1"}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("LEAPMUX_REMOTE_SOCK", sockURL)
	t.Setenv("LEAPMUX_REMOTE_TOKEN", rawToken)

	c, err := remote.NewClientFromEnv("")
	require.NoError(t, err)
	require.True(t, c.IsLocal(), "client should be local when LEAPMUX_REMOTE_SOCK is set")
	assert.Equal(t, sockURL, c.HubURL, "HubURL preserves the socket URL for display and IsLocal()")

	resp, err := c.RemoteIPCService().Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "u-1", resp.Msg.GetUserId())
	assert.Equal(t, "worker-A", resp.Msg.GetWorkerId())
	assert.Equal(t, "ws-1", resp.Msg.GetWorkspaceId())
	assert.Equal(t, "term-1", resp.Msg.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, resp.Msg.GetTabType())
}

// TestNewClientFromEnv_LocalStreamingAttachesAuth covers the
// server-streaming half of the per-agent IPC contract: the bearer
// token must reach the IPC server on streaming RPCs too, not just
// unary ones. `connect.UnaryInterceptorFunc` is a no-op on the
// streaming path, so an AuthInterceptor built that way drops the
// `X-Leapmux-Token` header and the IPC server's withAuth middleware
// rejects the request with HTTP 401 — surfaced as
// "unauthenticated: HTTP status 401 Unauthorized" in the CLI. CRDT
// bootstrap (`hub.WatchOrg`) is the production path that exposed it
// (`leapmux remote tile list` fails on bootstrap), so we exercise
// StreamInner directly here.
func TestNewClientFromEnv_LocalStreamingAttachesAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}

	sockURL := shortIPCSocket(t)
	rawToken := remoteipc.MintToken()
	info := remoteipc.TokenInfo{
		UserID:      userid.MustNew("u-1"),
		WorkerID:    "worker-A",
		WorkspaceID: "ws-1",
		TabID:       "term-1",
		TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
	}
	// The router accepts any streaming method via a recording local
	// dispatcher. The test doesn't care what the stream returns —
	// only that the call reaches the server at all (i.e. the auth
	// header survived the streaming-client wrap). 401 would short-
	// circuit before any router code runs.
	srv, err := remoteipc.Listen(remoteipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: info,
		Router: &remoteipc.Router{
			WorkerID:     "worker-A",
			UserID:       userid.MustNew("u-1"),
			WorkspaceIDs: []string{"ws-1"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("LEAPMUX_REMOTE_SOCK", sockURL)
	t.Setenv("LEAPMUX_REMOTE_TOKEN", rawToken)

	c, err := remote.NewClientFromEnv("")
	require.NoError(t, err)

	stream, err := c.RemoteIPCService().StreamInner(
		context.Background(),
		connect.NewRequest(&leapmuxv1.StreamInnerRequest{
			Method:          "worker.NoSuchMethod",
			ClientRequestId: "rid-1",
		}),
	)
	require.NoError(t, err, "stream construction returns nil even on transport failure; failures surface via Receive/Err")
	t.Cleanup(func() { _ = stream.Close() })

	// Drain. The downstream router produces error envelopes for an
	// unknown method, but those ride on a successful (non-401)
	// stream — proving the auth header reached the server. A 401
	// from withAuth short-circuits the request before any envelope
	// is sent, surfacing as `stream.Err()` with code Unauthenticated.
	for stream.Receive() {
	}
	streamErr := stream.Err()
	if streamErr != nil {
		assert.NotContains(t, streamErr.Error(), "HTTP status 401",
			"streaming RPC must include the X-Leapmux-Token header that withAuth checks")
		assert.NotEqual(t, connect.CodeUnauthenticated, connect.CodeOf(streamErr),
			"streaming RPC must not be rejected by the IPC server's auth middleware")
	}
}

// fakeChannelHub is a minimal ChannelService that answers the two calls an
// OpenChannel makes before the identity cross-check: the handshake params
// (a real X25519 static key, CLASSIC mode, so the initiator's message 1
// builds) and the open itself, which reports whatever identity the test
// wants the hub to have authenticated. Everything past the cross-check is
// deliberately left to fail -- the handshake payload is junk -- so the test
// can tell "rejected on identity" apart from "got past identity".
type fakeChannelHub struct {
	leapmuxv1connect.UnimplementedChannelServiceHandler
	staticPub  []byte
	openUserID string
}

func (f *fakeChannelHub) GetWorkerHandshakeParams(
	context.Context,
	*connect.Request[leapmuxv1.GetWorkerHandshakeParamsRequest],
) (*connect.Response[leapmuxv1.GetWorkerHandshakeParamsResponse], error) {
	return connect.NewResponse(&leapmuxv1.GetWorkerHandshakeParamsResponse{
		PublicKey:      f.staticPub,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}), nil
}

func (f *fakeChannelHub) OpenChannel(
	context.Context,
	*connect.Request[leapmuxv1.OpenChannelRequest],
) (*connect.Response[leapmuxv1.OpenChannelResponse], error) {
	return connect.NewResponse(&leapmuxv1.OpenChannelResponse{
		ChannelId:        "ch-1",
		UserId:           f.openUserID,
		HandshakePayload: []byte("not-a-real-message2"),
	}), nil
}

func (f *fakeChannelHub) CloseChannel(
	context.Context,
	*connect.Request[leapmuxv1.CloseChannelRequest],
) (*connect.Response[leapmuxv1.CloseChannelResponse], error) {
	return connect.NewResponse(&leapmuxv1.CloseChannelResponse{}), nil
}

// startFakeChannelHub serves fakeChannelHub over httptest and returns a
// hub-bound Client pointed at it with the given resolved user id.
func startFakeChannelHub(t *testing.T, cliUserID, hubUserID string) *remote.Client {
	t.Helper()

	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)

	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewChannelServiceHandler(&fakeChannelHub{
		staticPub:  priv.PublicKey().Bytes(),
		openUserID: hubUserID,
	})
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &remote.Client{
		HubURL:     srv.URL,
		Bearer:     "lmx_test_secret",
		HTTPClient: srv.Client(),
		WSClient:   srv.Client(),
		Pins:       newPinsForTest(t),
		UserID:     cliUserID,
	}
}

// TestOpenE2EEChannel_RejectsIdentityMismatch pins the ExpectedUserID
// pass-through: the CLI resolves workspaces and workers under its creds'
// user_id, so a hub that authenticates the channel as somebody else must
// abort the open rather than hand back a channel that silently runs every
// later RPC as the wrong user. Without ExpectedUserID wired into
// OpenChannelOptions the open sails past this and fails later (or not at
// all), which is exactly what this asserts against.
func TestOpenE2EEChannel_RejectsIdentityMismatch(t *testing.T) {
	c := startFakeChannelHub(t, "cli-user-1", "someone-else-2")

	ctx := context.Background()
	_, err := c.OpenE2EEChannel(ctx, ctx, "worker-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub authenticated this channel as",
		"the CLI must cross-check the hub's authenticated identity against its creds")
	assert.Contains(t, err.Error(), "someone-else-2")
	assert.Contains(t, err.Error(), "cli-user-1")
}

// TestOpenE2EEChannel_AcceptsMatchingIdentity is the sibling guard: an
// agreeing hub must NOT be rejected. The open still fails -- the fake hub's
// handshake payload is junk -- but it must fail in the handshake, past the
// identity check.
func TestOpenE2EEChannel_AcceptsMatchingIdentity(t *testing.T) {
	c := startFakeChannelHub(t, "cli-user-1", "cli-user-1")

	ctx := context.Background()
	_, err := c.OpenE2EEChannel(ctx, ctx, "worker-1")
	require.Error(t, err, "the fake hub's junk handshake payload cannot complete")
	assert.NotContains(t, err.Error(), "hub authenticated this channel as",
		"a hub that agrees with the CLI's creds must pass the cross-check")
	assert.Contains(t, err.Error(), "handshake2")
}
