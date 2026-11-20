package control_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli/control"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
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
	resp, err := ipc.Whoami(context.Background(), &leapmuxv1.WhoamiRequest{})
	require.NoError(t, err)
	assert.Equal(t, "u-1", resp.GetUserId())
	assert.Equal(t, "worker-A", resp.GetWorkerId())
	assert.Equal(t, "term-1", resp.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, resp.GetTabType())
}

// TestNewClientFromEnv_LocalStreamingAttachesAuth covers the
// server-streaming half of the per-agent IPC contract: the bearer
// token must reach the IPC server on streaming RPCs too, not just
// unary ones. A client interceptor that stamps the header only on
// unary calls is a no-op on the streaming path, so the stream open
// drops the `X-LeapMux-Token` header and the IPC server's withAuth
// middleware rejects the request with HTTP 401 — surfaced as
// "unauthenticated: HTTP status 401 Unauthorized" in the CLI. The
// hub's user-event stream (`hub./ws/userevents`) was the production path
// that exposed it (`leapmux control tile list` failed on bootstrap), so we
// exercise StreamInner directly here.
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
		&leapmuxv1.StreamInnerRequest{
			Method:          "worker.NoSuchMethod",
			ClientRequestId: "rid-1",
		},
	)
	require.NoError(t, err, "the stream must open; a refusal surfaces on Receive")
	t.Cleanup(func() { _ = stream.Close() })

	// Drain. The downstream router produces error envelopes for an
	// unknown method, but those travel on a successful (non-401)
	// stream — proving the auth header reached the server. A 401
	// from withAuth short-circuits the request before any envelope
	// is sent, surfacing as a Receive error with code Unauthenticated.
	var streamErr2 error
	for {
		_, err := stream.Receive()
		if err != nil {
			streamErr2 = err
			break
		}
	}
	if errors.Is(streamErr2, io.EOF) {
		streamErr2 = nil
	}
	streamErr := streamErr2
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
	context.Context, *leapmuxv1.GetWorkerHandshakeParamsRequest,
) (*leapmuxv1.GetWorkerHandshakeParamsResponse, error) {
	// A complete key set, as the hub announces one: the channel refuses an
	// incomplete set before any pin decision. Only the X25519 key takes part in
	// a CLASSIC handshake, so the other two keep the fixture bytes.
	keys := workerkeystest.Keys(workerkeystest.SeededWorkerFill)
	keys.X25519 = f.staticPub
	return &leapmuxv1.GetWorkerHandshakeParamsResponse{
		Keys:           keys,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}, nil
}

func (f *fakeChannelHub) OpenChannel(
	context.Context, *leapmuxv1.OpenChannelRequest,
) (*leapmuxv1.OpenChannelResponse, error) {
	return &leapmuxv1.OpenChannelResponse{
		ChannelId:        "ch-1",
		UserId:           f.openUserID,
		HandshakePayload: []byte("not-a-real-message2"),
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
	}, nil
}

func (f *fakeChannelHub) CloseChannel(
	context.Context, *leapmuxv1.CloseChannelRequest,
) (*leapmuxv1.CloseChannelResponse, error) {
	return &leapmuxv1.CloseChannelResponse{}, nil
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
	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterChannelServiceHandler(connectServer, &fakeChannelHub{
		staticPub:  priv.PublicKey().Bytes(),
		openUserID: hubUserID,
	})
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
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

// workerSideHubUser is the identity workerSideChannelHub authenticates a
// channel as.
const workerSideHubUser = "u-hub"

// workerSideChannelHub is a local Hub test fixture that completes an E2EE
// channel open end to end: ChannelService answers the handshake with a real
// composite key pair, and the /ws/channel relay drives a real
// channel.Manager — the worker side of the Noise handshake — so the open's
// verification Ping is answered and the pin commit runs. fakeChannelHub
// deliberately fails past the identity cross-check; this fixture exists for
// the paths that must reach a handed-out channel.
type workerSideChannelHub struct {
	leapmuxv1connect.UnimplementedChannelServiceHandler

	ctx        context.Context
	key        *noiseutil.CompositeKeypair
	keys       *leapmuxv1.WorkerPublicKeys
	dispatcher *channel.Dispatcher
	opens      atomic.Int32

	mu       sync.Mutex
	relays   map[string]*websocket.Conn
	managers map[string]*channel.Manager
	channels map[string]*channel.Manager
}

// startWorkerSideChannelHub serves workerSideChannelHub over httptest and
// returns the server. The test points its own client at the server URL.
func startWorkerSideChannelHub(t *testing.T) *httptest.Server {
	t.Helper()
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	keys, err := workerkeys.Of(key)
	require.NoError(t, err)
	dispatcher := channel.NewDispatcher()
	dispatcher.Handle(leapmuxv1connect.WorkerSystemServicePingProcedure, channel.Registration{},
		func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
			_ = w.SendResponse(channelwire.NewPayloadResponse(nil))
		})
	h := &workerSideChannelHub{
		ctx: t.Context(), key: key, keys: keys, dispatcher: dispatcher,
		relays: map[string]*websocket.Conn{}, managers: map[string]*channel.Manager{}, channels: map[string]*channel.Manager{},
	}
	t.Cleanup(h.closeWorkers)

	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterChannelServiceHandler(connectServer, h)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	mux.HandleFunc(contracts.WSRouteChannel, h.relayFrames)
	return hubtransporttest.NewServer(t, mux)
}

func (h *workerSideChannelHub) managerForWorker(workerID string) *channel.Manager {
	h.mu.Lock()
	defer h.mu.Unlock()
	if manager := h.managers[workerID]; manager != nil {
		return manager
	}
	manager := channel.NewManager(workerID, h.key, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, h.sendToClient, h.trySendToClient, 0, 0)
	manager.SetDispatcher(h.dispatcher)
	h.managers[workerID] = manager
	return manager
}

func (h *workerSideChannelHub) closeWorkers() {
	h.mu.Lock()
	managers := make([]*channel.Manager, 0, len(h.managers))
	for _, manager := range h.managers {
		managers = append(managers, manager)
	}
	h.mu.Unlock()
	for _, manager := range managers {
		manager.CloseAll()
	}
}

// sendToClient relays one frame from the worker to the client's socket.
func (h *workerSideChannelHub) sendToClient(msg *leapmuxv1.ConnectRequest) error {
	frame := msg.GetChannelMessageResp()
	if frame == nil {
		return nil
	}
	h.mu.Lock()
	ws := h.relays[frame.GetChannelId()]
	h.mu.Unlock()
	if ws == nil {
		return fmt.Errorf("no relay socket for channel %q", frame.GetChannelId())
	}
	return channelwire.WriteChannelMessage(h.ctx, ws, frame)
}

func (h *workerSideChannelHub) trySendToClient(msg *leapmuxv1.ConnectRequest) bool {
	return h.sendToClient(msg) == nil
}

// relayFrames serves /ws/channel: every frame of one socket goes to the
// worker, and the worker's session closes when the socket ends.
func (h *workerSideChannelHub) relayFrames(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{contracts.WSSubprotocolChannelRelay}})
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(channelwire.WSReadLimit)

	var channelID string
	var manager *channel.Manager
	defer func() {
		if manager != nil {
			manager.HandleClose(channelID)
		}
	}()
	for {
		frame, err := channelwire.ReadChannelMessage(h.ctx, ws)
		if err != nil {
			return
		}
		if channelID == "" {
			channelID = frame.GetChannelId()
			h.mu.Lock()
			h.relays[channelID] = ws
			manager = h.channels[channelID]
			h.mu.Unlock()
			if manager == nil {
				return
			}
		}
		manager.HandleMessage(frame)
	}
}

func (h *workerSideChannelHub) GetWorkerHandshakeParams(
	context.Context, *leapmuxv1.GetWorkerHandshakeParamsRequest,
) (*leapmuxv1.GetWorkerHandshakeParamsResponse, error) {
	return &leapmuxv1.GetWorkerHandshakeParamsResponse{
		Keys:           h.keys,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}, nil
}

func (h *workerSideChannelHub) OpenChannel(
	_ context.Context, req *leapmuxv1.OpenChannelRequest,
) (*leapmuxv1.OpenChannelResponse, error) {
	channelID := "channel-" + strconv.Itoa(int(h.opens.Add(1)))
	manager := h.managerForWorker(req.GetWorkerId())
	opened := manager.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        channelID,
		UserId:           workerSideHubUser,
		HandshakePayload: req.GetHandshakePayload(),
		MaxMessageSize:   uint64(contracts.MaxMessageSize),
		GrantedScopes:    []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_ALL},
	})
	if reason := opened.GetError(); reason != "" {
		return nil, connect.NewError(connect.CodeInternal, reason)
	}
	h.mu.Lock()
	h.channels[channelID] = manager
	h.mu.Unlock()
	return &leapmuxv1.OpenChannelResponse{
		ChannelId:        channelID,
		HandshakePayload: opened.GetHandshakePayload(),
		UserId:           workerSideHubUser,
		MaxMessageSize:   opened.GetMaxMessageSize(),
	}, nil
}

func (h *workerSideChannelHub) CloseChannel(
	_ context.Context, req *leapmuxv1.CloseChannelRequest,
) (*leapmuxv1.CloseChannelResponse, error) {
	h.mu.Lock()
	manager := h.channels[req.GetChannelId()]
	delete(h.channels, req.GetChannelId())
	h.mu.Unlock()
	if manager != nil {
		manager.HandleClose(req.GetChannelId())
	}
	return &leapmuxv1.CloseChannelResponse{}, nil
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

// The TOFU pin store opens on FIRST USE, and every operation reads its
// file again, so a pins.json that cannot be parsed refuses the one caller
// that needs a pin — at resolve time — and nothing else.
//
// Opening it in the constructor made a corrupt file refuse every verb --
// `control workspace list` and each `control admin ...` verb alike, none of
// which touches a pin. The admin verbs reported it as `not_logged_in`,
// which states neither the file nor the cause.
//
// A repair must recover the SAME client: the lazy store handle is not
// stuck with the failure its first read saw.
func TestPinStore_ACorruptFileRefusesOnlyTheChannel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	hub := startWorkerSideChannelHub(t)
	host, err := control.HubHost(hub.URL)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, host), 0o755))
	pinsPath := filepath.Join(dir, host, "pins.json")
	require.NoError(t, os.WriteFile(pinsPath, []byte("{ not json"), 0o644))

	c, err := control.NewClientOrAnonymous(hub.URL)
	require.NoError(t, err, "a verb that never opens a channel must not fail on a pin file")

	require.NoError(t, control.SaveCredentials(hub.URL, control.CredentialFile{
		HubURL: hub.URL, AccessToken: "lmx_a_test",
	}))
	_, err = control.NewClient(hub.URL)
	require.NoError(t, err, "the credentialed constructor behaves the same way")
	c.HTTPClient = hub.Client()
	c.WSClient = hub.Client()

	ctx := context.Background()
	_, err = c.OpenE2EEChannel(ctx, ctx, "worker-1")
	require.Error(t, err, "the E2EE channel is the one caller that needs a pin")
	assert.Contains(t, err.Error(), "worker key pin: parse pins",
		"the refusal must be the pin resolve that identifies the unreadable document, not a failure elsewhere in the handshake")

	// Repair the document; the same client must recover, because every
	// store operation reads the file again.
	require.NoError(t, os.WriteFile(pinsPath, []byte(`{"pins":{}}`), 0o644))
	ch, err := c.OpenE2EEChannel(ctx, ctx, "worker-1")
	require.NoError(t, err, "the same client recovers once the pin file is repaired")
	t.Cleanup(ch.Close)

	// The verified open records the pin the repair left absent.
	after, err := control.NewPinStore(hub.URL)
	require.NoError(t, err)
	pins, err := after.List(ctx)
	require.NoError(t, err)
	require.Len(t, pins, 1)
	assert.Equal(t, "worker-1", pins[0].WorkerID)
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
// `events watch` over local IPC and `agent messages --follow` both open
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
