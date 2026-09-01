package control_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// shortIPCSocket builds a unix-socket path under os.TempDir() short
// enough to fit the platform's sun_path limit (~104 chars on macOS).
// t.TempDir() routinely produces directories that exceed it.
func shortIPCSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "leapmux-cli-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return "unix:" + filepath.Join(dir, "ipc.sock")
}

// TestNewClientFromEnv_LocalWhoami exercises the full local-IPC path
// the CLI takes when invoked from a remote-enabled terminal tab:
// the CLI parses LEAPMUX_CONTROL_SOCK, the h2c transport dials the unix
// socket, and `control whoami` reaches the per-agent IPC server.
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
	rawToken := controlipc.MintToken()
	info := controlipc.TokenInfo{
		UserID:   userid.MustNew("u-1"),
		WorkerID: "worker-A",
		TabID:    "term-1",
		TabType:  leapmuxv1.TabType_TAB_TYPE_TERMINAL,
	}
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: info,
		Router:    &controlipc.Router{WorkerID: "worker-A", UserID: userid.MustNew("u-1")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
	t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)

	c, err := control.NewClientFromEnv("")
	require.NoError(t, err)
	require.True(t, c.IsWorkerIPC(), "client should be worker-IPC when LEAPMUX_CONTROL_SOCK is set")
	assert.Equal(t, sockURL, c.HubURL, "HubURL preserves the socket URL for display and IsWorkerIPC()")

	ipc, err := c.ControlIPCService()
	require.NoError(t, err)
	resp, err := ipc.Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "u-1", resp.Msg.GetUserId())
	assert.Equal(t, "worker-A", resp.Msg.GetWorkerId())
	assert.Equal(t, "term-1", resp.Msg.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, resp.Msg.GetTabType())
}

// TestNewClientFromEnv_LocalStreamingAttachesAuth covers the
// server-streaming half of the per-agent IPC contract: the bearer
// token must reach the IPC server on streaming RPCs too, not just
// unary ones. `connect.UnaryInterceptorFunc` is a no-op on the
// streaming path, so an AuthInterceptor built that way drops the
// `X-LeapMux-Token` header and the IPC server's withAuth middleware
// rejects the request with HTTP 401 — surfaced as
// "unauthenticated: HTTP status 401 Unauthorized" in the CLI. CRDT
// bootstrap (`hub.WatchUser`) is the production path that exposed it
// (`leapmux control tile list` fails on bootstrap), so we exercise
// StreamInner directly here.
func TestNewClientFromEnv_LocalStreamingAttachesAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}

	sockURL := shortIPCSocket(t)
	rawToken := controlipc.MintToken()
	info := controlipc.TokenInfo{
		UserID:   userid.MustNew("u-1"),
		WorkerID: "worker-A",
		TabID:    "term-1",
		TabType:  leapmuxv1.TabType_TAB_TYPE_TERMINAL,
	}
	// The router accepts any streaming method via a recording local
	// dispatcher. The test doesn't care what the stream returns —
	// only that the call reaches the server at all (i.e. the auth
	// header survived the streaming-client wrap). 401 would short-
	// circuit before any router code runs.
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: info,
		Router: &controlipc.Router{
			WorkerID: "worker-A",
			UserID:   userid.MustNew("u-1"),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
	t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)

	c, err := control.NewClientFromEnv("")
	require.NoError(t, err)

	ipc, err := c.ControlIPCService()
	require.NoError(t, err)
	stream, err := ipc.StreamInner(
		context.Background(),
		connect.NewRequest(&leapmuxv1.StreamInnerRequest{
			Method:          "worker.NoSuchMethod",
			ClientRequestId: "rid-1",
		}),
	)
	require.NoError(t, err, "stream construction returns nil even on transport failure; failures surface via Receive/Err")
	t.Cleanup(func() { _ = stream.Close() })

	// Drain. The downstream router produces error envelopes for an
	// unknown method, but those travel on a successful (non-401)
	// stream — proving the auth header reached the server. A 401
	// from withAuth short-circuits the request before any envelope
	// is sent, surfacing as `stream.Err()` with code Unauthenticated.
	for stream.Receive() {
	}
	streamErr := stream.Err()
	if streamErr != nil {
		assert.NotContains(t, streamErr.Error(), "HTTP status 401",
			"streaming RPC must include the X-LeapMux-Token header that withAuth checks")
		assert.NotEqual(t, connect.CodeUnauthenticated, connect.CodeOf(streamErr),
			"streaming RPC must not be rejected by the IPC server's auth middleware")
	}
}

// fakeChannelHub is a minimal ChannelService that answers the two calls an
// OpenChannel makes before the identity cross-check: the handshake params
// (a real X25519 static key, CLASSIC mode, so the initiator's message 1
// builds) and the open itself, which reports whatever identity the test
// configured. Everything past the cross-check is
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
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
	}), nil
}

func (f *fakeChannelHub) CloseChannel(
	context.Context,
	*connect.Request[leapmuxv1.CloseChannelRequest],
) (*connect.Response[leapmuxv1.CloseChannelResponse], error) {
	return connect.NewResponse(&leapmuxv1.CloseChannelResponse{}), nil
}

// bearerHeader reports what the client stamps on an outbound request, which
// is the only thing a test needs to know about the credential and the only
// thing a transport ever sees. The field itself is unexported, so the
// refresh mutex cannot be bypassed.
func bearerHeader(c *control.Client) string {
	h := http.Header{}
	c.ApplyAuth(h)
	return h.Get("Authorization")
}

// startFakeChannelHub serves fakeChannelHub over httptest and returns a
// hub-bound Client pointed at it with the given resolved user id.
func startFakeChannelHub(t *testing.T, cliUserID, hubUserID string) *control.Client {
	t.Helper()

	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)

	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewChannelServiceHandler(&fakeChannelHub{
		staticPub:  priv.PublicKey().Bytes(),
		openUserID: hubUserID,
	})
	mux.Handle(path, handler)
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)

	// Through the real constructor over a seeded credential, rather than a
	// struct literal: the bearer is unexported so the refresh mutex is the
	// only way to touch it, and building the client the way production does
	// also stops this helper from drifting past a field the constructor sets.
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_test_secret", UserID: cliUserID,
	}))
	c, err := control.NewClient(srv.URL)
	require.NoError(t, err)
	c.HTTPClient = srv.Client()
	c.WSClient = srv.Client()
	c.Pins = newPinsForTest(t)
	return c
}

// TestNewClientFromEnv_NoHubErrorMentionsControlCommand locks the
// guidance a user sees when the caller supplies no transport input: neither the
// --hub flag, LEAPMUX_HUB, nor LEAPMUX_CONTROL_SOCK. The message points
// at the renamed `leapmux control auth login` command, so a future
// rename that forgets this string fails the test.
func TestNewClientFromEnv_NoHubErrorMentionsControlCommand(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_SOCK", "")
	t.Setenv("LEAPMUX_HUB", "")

	_, err := control.NewClientFromEnv("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leapmux control auth login",
		"the no-transport error must guide the user at the control command")
	assert.NotContains(t, err.Error(), "leapmux remote",
		"the guidance must not reference the retired remote command name")
}

// TestOpenE2EEChannel_RejectsIdentityMismatch pins the ExpectedUserID
// pass-through: the CLI resolves workspaces and workers under its creds'
// user_id, so a hub that authenticates the channel as somebody else must
// abort the open rather than hand back a channel that silently runs every
// later RPC as the wrong user. Without ExpectedUserID wired into
// OpenChannelOptions the open passes this and fails later (or not at
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

// TestHubSocketClientSendsBearerNotIPCHeader pins the peer distinction a
// URL scheme cannot express: a client for a HUB reached over a unix:
// socket must present Authorization: Bearer (the hub's own listener
// reads it), never X-LeapMux-Token (only the worker's IPC server reads
// that). The old IsLocal() keyed the header off the URL scheme, so this
// client sent a header nothing consumed.
func TestHubSocketClientSendsBearerNotIPCHeader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}

	// NewClient needs stored credentials for the hub URL; seed them
	// through the package's own writer so the lookup finds them.
	sockURL := shortIPCSocket(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	require.NoError(t, control.SaveCredentials(sockURL, control.CredentialFile{
		HubURL:       sockURL,
		AccessToken:  "lmx_test_token",
		RefreshToken: "lmx_test_refresh",
		UserID:       "u-1",
		Username:     "tester",
	}))

	c, err := control.NewClient(sockURL)
	require.NoError(t, err)
	assert.False(t, c.IsWorkerIPC(), "a hub client is not a worker-IPC client even over a socket URL")

	header := http.Header{}
	c.ApplyAuth(header)
	assert.Equal(t, "Bearer lmx_test_token", header.Get("Authorization"))
	assert.Empty(t, header.Get("X-LeapMux-Token"), "the hub's listener does not read the IPC header")

	// ControlIPCService refuses: the peer is the hub, not the worker.
	_, err = c.ControlIPCService()
	require.Error(t, err)

	// The WebSocket surfaces refuse a socket hub URL: they build absolute
	// WS URLs from the hub origin, which a socket URL cannot provide.
	_, err = c.OpenUserEvents(context.Background(), nil)
	require.ErrorContains(t, err, "http(s) hub origin")
	_, err = c.OpenE2EEChannel(context.Background(), context.Background(), "worker-1")
	require.ErrorContains(t, err, "http(s) hub origin")
}

// TestWorkerIPCClientSendsIPCHeader is the mirror: a worker-spawned
// client (NewLocalClient) presents X-LeapMux-Token, not Bearer.
func TestWorkerIPCClientSendsIPCHeader(t *testing.T) {
	c, err := control.NewLocalClient("unix:/tmp/does-not-exist.sock", "ipc-token")
	require.NoError(t, err)
	assert.True(t, c.IsWorkerIPC())

	header := http.Header{}
	c.ApplyAuth(header)
	assert.Equal(t, "ipc-token", header.Get("X-LeapMux-Token"))
	assert.Empty(t, header.Get("Authorization"), "the worker IPC server does not read the Bearer header")
}

// The TOFU pin store opens on FIRST USE, so a pins.json that cannot be
// parsed refuses the one caller that needs a pin and nothing else.
//
// Opening it in the constructor made a corrupt file refuse every verb --
// `control workspace list` and each `control admin ...` verb alike, none of
// which touches a pin. The admin verbs reported it as `not_logged_in`,
// which states neither the file nor the cause.
func TestPinStore_ACorruptFileRefusesOnlyTheChannel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	host, err := control.HubHost(testHub)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, host), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, host, "pins.json"), []byte("{ not json"), 0o644))

	c, err := control.NewClientOrAnonymous(testHub)
	require.NoError(t, err, "a verb that never opens a channel must not fail on a pin file")

	require.NoError(t, control.SaveCredentials(testHub, control.CredentialFile{
		HubURL: testHub, AccessToken: "lmx_a_test",
	}))
	_, err = control.NewClient(testHub)
	require.NoError(t, err, "the credentialed constructor behaves the same way")

	_, err = c.OpenE2EEChannel(context.Background(), context.Background(), "worker-1")
	require.Error(t, err, "the E2EE channel is the one caller that needs a pin")
	assert.Contains(t, err.Error(), "open TOFU pin store",
		"the refusal must name the store, not fail somewhere in the handshake")
}

// A credential file that exists but cannot be read is a fault the operator
// must see. Collapsing it into the anonymous fallback turned a broken file
// into an "unauthenticated" reply from the hub, which points at the login
// rather than at the file.
func TestNewClientOrAnonymous_ReportsACredentialFileItCannotParse(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	path, err := control.CredentialsPath(testHub)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("{ truncated"), 0o600))

	_, err = control.NewClientOrAnonymous(testHub)
	require.Error(t, err, "a broken credential must not read as 'no credential'")
	assert.Contains(t, err.Error(), "parse credentials")
	assert.Contains(t, err.Error(), path, "the message states the file to repair or delete")

	// Only "no credential stored" takes the anonymous fallback, which is
	// what a solo hub needs.
	require.NoError(t, os.Remove(path))
	c, err := control.NewClientOrAnonymous(testHub)
	require.NoError(t, err)
	assert.Empty(t, bearerHeader(c), "no credential means no bearer, not a refusal")
	assert.Empty(t, c.Username)
}

// The two constructors build the SAME client apart from the credential, so
// a field added to one reaches the other. `peer` had to be added twice.
func TestNewClientAndNewClientOrAnonymous_BuildTheSameTransport(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, control.SaveCredentials(testHub, control.CredentialFile{
		HubURL: testHub, AccessToken: "lmx_a_test", UserID: "u-1", Username: "tester",
	}))

	withCreds, err := control.NewClient(testHub)
	require.NoError(t, err)
	anonymous, err := control.NewClientOrAnonymous(testHub)
	require.NoError(t, err)

	for _, c := range []*control.Client{withCreds, anonymous} {
		assert.Equal(t, testHub, c.HubURL)
		assert.Equal(t, testHub, c.ConnectURL())
		assert.False(t, c.IsWorkerIPC(), "a hub URL is never the worker-IPC peer")
		assert.NotNil(t, c.HTTPClient)
	}
	assert.Equal(t, "Bearer lmx_a_test", bearerHeader(withCreds))
	assert.Equal(t, "tester", withCreds.Username)
	assert.Equal(t, "Bearer lmx_a_test", bearerHeader(anonymous), "a stored credential is used when one exists")
}

// TestNewClient_RemoteHubGetsAWebSocketClient pins the transport pair the CLI
// hands its two lanes.
//
// The remote branch used to return a NIL WebSocket client, which made
// coder/websocket fall back to http.DefaultClient: no timeout of its own, and
// the process-global connection pool shared with every other caller in the
// program. It worked only because net/http steers a WebSocket upgrade onto
// HTTP/1.1 by itself -- a correct outcome that nobody chose.
func TestNewClient_RemoteHubGetsAWebSocketClient(t *testing.T) {
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_test_secret", UserID: "u-1",
	}))

	c, err := control.NewClient(srv.URL)
	require.NoError(t, err)

	require.NotNil(t, c.WSClient, "a remote hub must get its own WebSocket client")
	require.NotNil(t, c.HTTPClient)
	assert.NotSame(t, c.HTTPClient, c.WSClient, "the two lanes need different protocols and different timeouts")
	assert.NotZero(t, c.HTTPClient.Timeout, "a unary hub call must be bounded")
	assert.Zero(t, c.WSClient.Timeout, "a WebSocket must not be bounded by a client timeout")
	assert.Equal(t, srv.URL, c.ConnectURL(), "a remote hub URL passes through verbatim")

	// Both clients reach the hub, which is what proves neither is a placeholder.
	for name, client := range map[string]*http.Client{"unary": c.HTTPClient, "websocket": c.WSClient} {
		req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/version", nil)
		require.NoError(t, reqErr, name)
		resp, doErr := client.Do(req)
		require.NoError(t, doErr, name)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusNoContent, resp.StatusCode, name)
	}
}

// TestWorkerIPCStreamLaneCarriesNoTimeout pins the lane a control-IPC SERVER
// STREAM must run on.
//
// `events --local` and `agent messages --follow` both open
// ControlIPCService.StreamInner, which rides the HTTP client rather than a
// WebSocket. An http.Client timeout covers the BODY read and a stream's body
// ends only when the stream does, so the timed unary client severed both
// subscriptions after exactly its timeout, reporting a transport error instead
// of an end of stream.
func TestWorkerIPCStreamLaneCarriesNoTimeout(t *testing.T) {
	socketURL := locallistentest.UniqueListenURL(t, "ctlstream")
	c, err := control.NewLocalClient(socketURL, "tok_local")
	require.NoError(t, err)

	require.NotNil(t, c.HTTPClient, "the unary lane")
	require.NotNil(t, c.StreamHTTPClient, "the stream lane")
	assert.NotZero(t, c.HTTPClient.Timeout,
		"a unary control-IPC call must be limited: a worker that never answers otherwise hangs the CLI for ever")
	assert.Zero(t, c.StreamHTTPClient.Timeout,
		"a control-IPC server stream must NOT be limited by a client timeout, which covers the body read")
	assert.NotSame(t, c.HTTPClient, c.StreamHTTPClient, "the two lanes hold different timeouts")

	// Both accessors answer, and each one carries its own lane's client.
	unary, err := c.ControlIPCService()
	require.NoError(t, err)
	require.NotNil(t, unary)
	stream, err := c.ControlIPCStreamService()
	require.NoError(t, err)
	require.NotNil(t, stream)
}

// A hub-bound client has no control-IPC lane at all, and the stream accessor
// must refuse it for the same reason the unary one does.
func TestControlIPCStreamServiceRefusesAHubClient(t *testing.T) {
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_test_secret", UserID: "u-1",
	}))
	c, err := control.NewClient(srv.URL)
	require.NoError(t, err)

	_, err = c.ControlIPCStreamService()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker-IPC")
}
