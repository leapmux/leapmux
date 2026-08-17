package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/tunnel"
)

// Client is the CLI's connection to a hub. It holds the credential
// (Bearer token), HTTP client (which handles unix:/npipe: hub URLs),
// and ConnectRPC service clients for the hub-side RPCs.
//
// Construct with NewClient; in agent-spawned mode (LEAPMUX_CONTROL_*
// env vars set) use NewLocalClient instead.
//
// HubURL is the user-visible address (`https://hub.example` or a
// `unix:`/`npipe:` IPC URL). connectURL is what ConnectRPC actually
// sees as the base URL: identical to HubURL for hub-bound clients,
// but rewritten to `http://localhost` for local-IPC clients because
// Go's http2.Transport rejects any URL whose scheme isn't http(s)
// with "http2: unsupported scheme" — the socket dial is wired into
// the transport, so the host portion is just a placeholder.
type Client struct {
	HubURL     string
	Bearer     string
	HTTPClient *http.Client
	WSClient   *http.Client // HTTP/1.1 client for /ws/channel upgrade
	// Pins is the per-hub TOFU pin store. Read it through pinStore, which
	// opens it on first use; a test may set the field directly instead.
	Pins       *PinStore
	pinsOnce   sync.Once
	pinsErr    error
	UserID     string
	Username   string
	connectURL string
	// peer identifies what the transport talks to. The URL scheme alone
	// cannot: a `unix:`/`npipe:` URL may address either the hub's own IPC
	// listener or a worker's per-agent IPC server, and the two consume
	// DIFFERENT auth headers (the hub reads Authorization: Bearer; the
	// worker's IPC server reads X-LeapMux-Token). Keying ApplyAuth off the
	// peer rather than the scheme is what lets `--hub unix:.../hub.sock`
	// work.
	peer peerKind
}

// peerKind distinguishes the two transports a Client can carry.
type peerKind int

const (
	// peerHub is the hub itself, over http(s) or its unix:/npipe: listener.
	peerHub peerKind = iota
	// peerWorkerIPC is the worker-local per-agent IPC server.
	peerWorkerIPC
)

// ConnectURL returns the base URL ConnectRPC clients should use.
// Equal to HubURL for hub-bound clients; equal to a placeholder
// `http://localhost` for local-IPC clients (the h2c transport dials
// the real unix/npipe socket regardless of host).
func (c *Client) ConnectURL() string {
	return c.connectURL
}

// defaultHTTPTimeout is the per-request timeout on the unary HTTP
// client used for ConnectRPC unary calls and the auth/version REST
// endpoints. Streaming RPCs (user events, agent-message follow) use
// a separate WebSocket client with no overall timeout.
const defaultHTTPTimeout = 60 * time.Second

// newHubClient builds a hub-bound client for hubURL, carrying creds when
// the caller holds them and running anonymously when creds is nil.
//
// Both constructors below route through it, so a field added to Client
// reaches both. The two differ ONLY in how they treat a credential that
// does not load, which is the difference each one documents.
func newHubClient(hubURL string, creds *CredentialFile) (*Client, error) {
	httpClient, wsClient, connectURL, err := buildHTTPClients(hubURL)
	if err != nil {
		return nil, err
	}
	c := &Client{
		HubURL:     hubURL,
		HTTPClient: httpClient,
		WSClient:   wsClient,
		connectURL: connectURL,
		peer:       peerHub,
	}
	if creds != nil {
		c.Bearer = creds.AccessToken
		c.UserID = creds.UserID
		c.Username = creds.Username
	}
	return c, nil
}

// pinStore opens the per-hub TOFU pin store on FIRST USE.
//
// Only OpenE2EEChannel needs a pin, so a pins.json that cannot be read or
// parsed must refuse that call and nothing else. Opening it in the
// constructor made a corrupt file refuse every verb, including each
// `control admin ...` verb, which reports the failure under the
// not_logged_in code — a message that names neither the file nor the
// cause.
func (c *Client) pinStore() (*PinStore, error) {
	// The preset test is INSIDE the Once, so two concurrent opens read the
	// field through the same happens-before edge. Reading it first as a
	// fast path is a data race with the Once body that writes it.
	c.pinsOnce.Do(func() {
		if c.Pins != nil {
			return
		}
		c.Pins, c.pinsErr = NewPinStore(c.HubURL)
	})
	if c.pinsErr != nil {
		return nil, c.pinsErr
	}
	return c.Pins, nil
}

// NewClient constructs a hub client from the on-disk credentials for
// hubURL. Returns ErrNotLoggedIn if no credentials exist.
func NewClient(hubURL string) (*Client, error) {
	creds, err := LoadCredentials(hubURL)
	if err != nil {
		return nil, err
	}
	return newHubClient(hubURL, creds)
}

// NewClientOrAnonymous constructs a hub client that falls back to an
// EMPTY credential when none is STORED. The hub — not the CLI — enforces
// authentication: against a solo hub the interceptor authenticates every
// request as the solo user regardless of headers, so `control admin`
// works there with no login (solo cannot complete a login flow at all);
// against any other hub the credential-less request simply answers
// unauthenticated.
//
// Only ErrNotLoggedIn takes that fallback. A credential file that exists
// but cannot be read or parsed is a fault the operator must see: running
// anonymously instead turns a broken file into an "unauthenticated" reply
// from the hub, which points at the login rather than at the file.
func NewClientOrAnonymous(hubURL string) (*Client, error) {
	creds, err := LoadCredentials(hubURL)
	if err != nil && !errors.Is(err, ErrNotLoggedIn) {
		return nil, err
	}
	return newHubClient(hubURL, creds)
}

// NewClientFromEnv chooses the right transport based on env vars.
// In worker-spawned mode (LEAPMUX_CONTROL_SOCK set) it returns a client
// targeting the local socket. Otherwise it falls back to NewClient
// using the --hub flag (or LEAPMUX_HUB env var).
func NewClientFromEnv(hubFlag string) (*Client, error) {
	if sock := os.Getenv("LEAPMUX_CONTROL_SOCK"); sock != "" {
		return NewLocalClient(sock, os.Getenv("LEAPMUX_CONTROL_TOKEN"))
	}
	url := hubFlag
	if url == "" {
		url = os.Getenv("LEAPMUX_HUB")
	}
	if url == "" {
		return nil, errors.New("no --hub flag or LEAPMUX_HUB / LEAPMUX_CONTROL_SOCK env var; run `leapmux control auth login --hub <url>` or invoke from inside an agent")
	}
	return NewClient(url)
}

// NewLocalClient targets a per-agent local IPC socket. The token is
// presented via the X-LeapMux-Token header on every request.
func NewLocalClient(socketURL, token string) (*Client, error) {
	if socketURL == "" || token == "" {
		return nil, errors.New("local IPC socket and token required")
	}
	httpClient, connectURL, err := locallisten.LocalH2CClient(socketURL, defaultHTTPTimeout)
	if err != nil {
		return nil, err
	}
	return &Client{
		HubURL:     socketURL,
		Bearer:     token,
		HTTPClient: httpClient,
		connectURL: connectURL,
		peer:       peerWorkerIPC,
	}, nil
}

// IsWorkerIPC reports whether this client was spawned inside an agent and
// talks to the worker-local IPC server — the "am I a worker-spawned
// agent" question. It is NOT "is the URL a socket": a hub reached over
// its own `unix:`/`npipe:` listener is a hub peer (Bearer auth), not a
// worker peer.
func (c *Client) IsWorkerIPC() bool {
	return c.peer == peerWorkerIPC
}

// ApplyAuth stamps the credential header the PEER consumes. Worker-IPC
// peers read X-LeapMux-Token; hub peers read Authorization: Bearer —
// including a hub addressed by a unix:/npipe: URL, whose listener is the
// hub's own and expects the same Bearer as its http(s) face. Exported so
// callers outside this package can apply the same auth shape to a
// hand-constructed request (the same rationale as AuthInterceptor).
func (c *Client) ApplyAuth(headers http.Header) {
	if c.Bearer == "" {
		return
	}
	if c.IsWorkerIPC() {
		headers.Set("X-LeapMux-Token", c.Bearer)
	} else {
		headers.Set("Authorization", "Bearer "+c.Bearer)
	}
}

// WorkspaceService returns a ConnectRPC client for the hub-side
// WorkspaceService. Auth headers are injected via an interceptor.
func (c *Client) WorkspaceService() leapmuxv1connect.WorkspaceServiceClient {
	return leapmuxv1connect.NewWorkspaceServiceClient(
		c.HTTPClient,
		c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	)
}

// WorkerManagementService returns a ConnectRPC client for ListWorkers.
func (c *Client) WorkerManagementService() leapmuxv1connect.WorkerManagementServiceClient {
	return leapmuxv1connect.NewWorkerManagementServiceClient(
		c.HTTPClient, c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	)
}

// UserCRDT returns a ConnectRPC client for the unary SubmitOps and
// UpdatePresence calls. The user-event subscription (formerly the
// `WatchUser` streaming RPC) lives on `/ws/userevents` — see
// `OpenUserEvents`. Auth headers are injected via an interceptor.
func (c *Client) UserCRDT() leapmuxv1connect.UserCRDTClient {
	return leapmuxv1connect.NewUserCRDTClient(
		c.HTTPClient, c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	)
}

// ChannelService returns a ConnectRPC client for OpenChannel /
// GetWorkerHandshakeParams when the CLI runs an E2EE inner RPC
// directly (rare; most callers use OpenE2EEChannel below).
func (c *Client) ChannelService() leapmuxv1connect.ChannelServiceClient {
	return leapmuxv1connect.NewChannelServiceClient(
		c.HTTPClient, c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	)
}

// ControlIPCService returns a ConnectRPC client for the worker-local
// IPC service. Only valid for clients constructed via NewLocalClient
// (worker-spawned agents).
//
// The restriction is ENFORCED, not merely documented, matching
// OpenUserEvents and OpenE2EEChannel which both refuse the wrong transport.
// Every caller already checks IsWorkerIPC() first, so this is a guard against a
// future one that forgets: a hub-bound client here would aim worker-namespace
// CallInner requests at the hub's connect URL, which answers 404 rather than
// anything a caller could diagnose.
func (c *Client) ControlIPCService() (leapmuxv1connect.ControlIPCServiceClient, error) {
	if !c.IsWorkerIPC() {
		return nil, errors.New("ControlIPCService is only valid for worker-IPC clients constructed via NewLocalClient")
	}
	return leapmuxv1connect.NewControlIPCServiceClient(
		c.HTTPClient, c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	), nil
}

// UserEventsStream is a read-only WebSocket subscription to the hub's
// `/ws/userevents` endpoint. Each `Recv` returns the next decoded
// `WatchUserEvent` proto (the first call always returns an `Initial`
// event). Close cancels the stream and tears down the underlying WS.
type UserEventsStream struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

// Recv reads the next event from the stream. Returns io.EOF when the
// peer closes cleanly; any other transport error is returned verbatim.
func (s *UserEventsStream) Recv() (*leapmuxv1.WatchUserEvent, error) {
	if s == nil || s.ws == nil {
		return nil, io.EOF
	}
	// Wire format mirrors `writeUserEvent` in ws_userevents.go:
	// [4-byte big-endian length][protobuf-encoded WatchUserEvent].
	payload, err := channelwire.ReadFramedBytes(s.ctx, s.ws)
	if err != nil {
		if channelwire.IsUserEventsCloseError(err) {
			return nil, io.EOF
		}
		return nil, err
	}
	var evt leapmuxv1.WatchUserEvent
	if err := proto.Unmarshal(payload, &evt); err != nil {
		return nil, fmt.Errorf("userevents: decode event: %w", err)
	}
	return &evt, nil
}

// Close shuts the stream down.
func (s *UserEventsStream) Close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.ws != nil {
		return s.ws.Close(websocket.StatusNormalClosure, "")
	}
	return nil
}

// OpenUserEvents opens a `/ws/userevents` WebSocket subscription against
// this hub. Bearer auth is added via the Authorization header; the bearer
// implies the user, so no user_id is sent. The returned stream's first
// event is always `UserMaterialized` (the bootstrap snapshot). Only valid
// for non-local clients — local-IPC clients should use the worker's
// per-agent delegation bearer to reach the hub directly (the worker is
// not in this path).
func (c *Client) OpenUserEvents(ctx context.Context, workspaceIDs []string) (*UserEventsStream, error) {
	if c.IsWorkerIPC() {
		return nil, errors.New("OpenUserEvents is only valid for hub-bound clients; use the agent-spawned hub URL + delegation bearer to subscribe directly")
	}
	// Both the WebSocket handshake and the absolute URL the subscription
	// builds need an HTTP origin; a socket hub URL has none.
	if locallisten.IsLocal(c.HubURL) {
		return nil, errors.New("OpenUserEvents needs an http(s) hub origin; a unix:/npipe: hub URL cannot carry a WebSocket subscription")
	}
	dialCtx, dialCancel := context.WithCancel(ctx)
	ws, err := channelwire.OpenUserEventsWS(dialCtx, c.WSClient, c.HubURL, c.Bearer, workspaceIDs, nil, 0)
	if err != nil {
		dialCancel()
		return nil, err
	}
	return &UserEventsStream{ws: ws, ctx: dialCtx, cancel: dialCancel}, nil
}

// OpenE2EEChannel opens a Noise_NK E2EE channel to the named worker
// via the hub relay. Uses the credential's bearer token and the
// per-hub TOFU pin store.
func (c *Client) OpenE2EEChannel(operationCtx, lifetimeCtx context.Context, workerID string) (*tunnel.Channel, error) {
	if c.IsWorkerIPC() {
		return nil, errors.New("OpenE2EEChannel is only valid for hub-bound clients")
	}
	// Same origin constraint as OpenUserEvents: the channel's WebSocket
	// upgrade builds an absolute URL from the hub origin.
	if locallisten.IsLocal(c.HubURL) {
		return nil, errors.New("OpenE2EEChannel needs an http(s) hub origin; a unix:/npipe: hub URL cannot carry a WebSocket channel")
	}
	// A nil *PinStore would still satisfy tunnel.KeyPinStore as a typed-nil
	// interface, so tunnel.OpenChannel's `pinStore != nil` guard would call
	// straight into a nil receiver. Open the store here and report a failure
	// instead: a hub-bound open that skipped TOFU verification silently is a
	// downgrade, not a fallback.
	pins, err := c.pinStore()
	if err != nil {
		return nil, fmt.Errorf("open TOFU pin store: %w", err)
	}
	return tunnel.OpenChannel(operationCtx, c.HubURL, workerID, &tunnel.OpenChannelOptions{
		HTTPClient:          c.HTTPClient,
		WebSocketHTTPClient: c.WSClient,
		LifetimeContext:     lifetimeCtx,
		BearerToken:         c.Bearer,
		// The CLI resolves workspaces/workers under c.UserID (see resolve.Resolver
		// and cmd/workspace.go), so it DOES have an expectation: creds whose bearer
		// and user_id have decoupled -- a rotated or reassigned token -- would have
		// it resolving as X while running channel RPCs as Y. Empty c.UserID (creds
		// predating user_id resolution) leaves the cross-check disabled, which is
		// exactly the no-expectation case OpenChannel skips.
		ExpectedUserID: c.UserID,
		KeyPin:         pins,
	})
}

// AdminSettingsService returns a ConnectRPC client for the hub's
// AdminSettingsService. Admin commands deliberately build a hub client
// directly rather than routing through the worker-IPC bridge: the
// hubrpc.Registry table is a typing device, not a security boundary
// (any worker-spawned agent can call whatever it lists), so no admin
// procedure is registered there.
func (c *Client) AdminSettingsService() leapmuxv1connect.AdminSettingsServiceClient {
	return leapmuxv1connect.NewAdminSettingsServiceClient(
		c.HTTPClient, c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	)
}

// AdminUserService returns a ConnectRPC client for AdminUserService
// (users, sessions, api-tokens, delegation-tokens).
func (c *Client) AdminUserService() leapmuxv1connect.AdminUserServiceClient {
	return leapmuxv1connect.NewAdminUserServiceClient(
		c.HTTPClient, c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	)
}

// AdminWorkerService returns a ConnectRPC client for AdminWorkerService
// (cross-user worker administration and registration keys).
func (c *Client) AdminWorkerService() leapmuxv1connect.AdminWorkerServiceClient {
	return leapmuxv1connect.NewAdminWorkerServiceClient(
		c.HTTPClient, c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	)
}

// AdminOAuthService returns a ConnectRPC client for AdminOAuthService.
func (c *Client) AdminOAuthService() leapmuxv1connect.AdminOAuthServiceClient {
	return leapmuxv1connect.NewAdminOAuthServiceClient(
		c.HTTPClient, c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	)
}

// AuthInterceptor adds the Authorization (or X-LeapMux-Token) header
// to every outgoing request. Exported so callers outside this package
// (e.g. cmd's hubrpc dispatch) can apply the same auth shape to a
// generically-constructed connect.NewClient.
//
// Wraps both unary AND streaming clients. `connect.UnaryInterceptorFunc`
// alone is a no-op on the streaming paths, which silently drops the
// `X-LeapMux-Token` / `Authorization` header from `StreamInner`,
// `OpenChannel`, and friends — the IPC server (or hub) then responds
// 401 and the CLI surfaces it as "unauthenticated: HTTP status 401
// Unauthorized". The streaming path matters here because CRDT
// bootstrap (`hub.WatchUser`) and any future server-streaming RPC
// flow through it.
func (c *Client) AuthInterceptor() connect.Interceptor {
	return &authInterceptor{client: c}
}

// authInterceptor stamps c.ApplyAuth on every outgoing connect call,
// unary or streaming. WrapStreamingHandler is a no-op because the CLI
// only acts as a client.
type authInterceptor struct{ client *Client }

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		a.client.ApplyAuth(req.Header())
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		a.client.ApplyAuth(conn.RequestHeader())
		return conn
	}
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// buildHTTPClients returns the HTTP/2-h2c client, the HTTP/1.1
// WebSocket client, and the base URL ConnectRPC should target for
// hubURL. Local-IPC hubs ("unix:" / "npipe:") get
// unix/npipe-dialer-backed transports plus the placeholder
// "http://localhost" base — http2.Transport / http.Transport reject
// any URL whose scheme isn't http(s); the dial is wired into the
// transport, so the host portion is purely cosmetic. Remote hubs get
// the default transport and pass hubURL through.
func buildHTTPClients(hubURL string) (*http.Client, *http.Client, string, error) {
	if locallisten.IsLocal(hubURL) {
		h2c, connectURL, err := locallisten.LocalH2CClient(hubURL, defaultHTTPTimeout)
		if err != nil {
			return nil, nil, "", err
		}
		// WS reads can be long-lived; no overall timeout here.
		ws, _, err := locallisten.LocalHTTPClient(hubURL, 0)
		if err != nil {
			return nil, nil, "", err
		}
		return h2c, ws, connectURL, nil
	}
	return &http.Client{Timeout: defaultHTTPTimeout}, nil, hubURL, nil
}
