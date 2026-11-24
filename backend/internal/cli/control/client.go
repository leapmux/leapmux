package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
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
// net/http rejects any URL whose scheme isn't http(s) — the socket dial
// is wired into the transport, so the host portion is just a
// placeholder. Package hubtransport builds both, and decides which
// protocol each one speaks.
type Client struct {
	HubURL string
	// bearer is the access token presented on every call.
	//
	// UNEXPORTED, and that is the mechanism rather than a style choice: a
	// refresh replaces it mid-flight while a stream on another goroutine
	// reads it, so every access must hold bearerMu. A comment asking callers
	// to use currentBearer and setBearer is a rule somebody can forget; an
	// unexported field is one the compiler keeps. Tests reach it through
	// ApplyAuth, which is what a transport sees anyway.
	bearer string
	// accessExpiresAt is the stored expiry of the token in bearer, so the
	// proactive freshness check can answer from memory. Zero means unknown
	// -- a client built without credentials, or one whose credential file
	// this client did not read yet -- and an unknown expiry falls through to
	// the file.
	accessExpiresAt time.Time
	bearerMu        sync.RWMutex
	HTTPClient      *http.Client
	WSClient        *http.Client // HTTP/1.1 client for the /ws/channel and /ws/userevents upgrades
	// StreamHTTPClient carries a Connect SERVER STREAM, so it holds no overall
	// timeout. It is set for a worker-IPC client only, because that is the one
	// transport whose streams do not ride a WebSocket: `layout watch` over
	// local IPC and `agent messages --follow` both open
	// ControlIPCService.StreamInner, which runs on this HTTP client rather
	// than on WSClient. An http.Client timeout covers the BODY read, and a
	// stream's body ends only when the stream does, so the timed HTTPClient
	// severed both of them after exactly 60 seconds.
	StreamHTTPClient *http.Client
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
	// promptAllowed reports whether this client may run a ceremony that
	// blocks on a person: today, the browser step-up. The constructor
	// decides it ONCE, so every call of one command answers alike.
	//
	// A worker-spawned client is never allowed one. It runs inside an
	// agent, where there is no terminal and nobody to read a URL.
	promptAllowed bool
}

// noPromptEnv refuses every prompt that waits for a person, whatever the
// terminal detection says: the browser step-up and the password prompt
// alike.
//
// Detection answers "is somebody probably there"; this answers "I am a
// script, do not wait for me", which a caller sometimes knows and the
// terminal never does -- a supervisor that gives its child a pseudo-terminal
// is the ordinary case. Presence is the whole test, the way NO_COLOR works:
// ANY non-empty value refuses the prompt.
const noPromptEnv = "LEAPMUX_CONTROL_NO_PROMPT"

// promptsAllowed reports whether this process may open a prompt that ends
// only when a person acts.
func promptsAllowed() bool {
	return os.Getenv(noPromptEnv) == "" && isInteractive()
}

// noPromptReason states WHY this process may not prompt, so a refusal can
// state the condition the caller is able to change.
func noPromptReason() string {
	if os.Getenv(noPromptEnv) != "" {
		return noPromptEnv + " is set"
	}
	return "stdin is not a terminal"
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
		HubURL:        hubURL,
		HTTPClient:    httpClient,
		WSClient:      wsClient,
		connectURL:    connectURL,
		peer:          peerHub,
		promptAllowed: promptsAllowed(),
	}
	if creds != nil {
		c.bearer = creds.AccessToken
		c.accessExpiresAt = creds.ExpiresAt
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
// not_logged_in code — a message that states neither the file nor the
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
// EMPTY credential when none is STORED. The hub -- not the CLI -- enforces
// authentication. A solo hub authenticates its LOCAL IPC socket as the solo
// user with no credential, so `control admin` works there with no login.
// Every other connection, a solo hub's TCP addresses included, answers
// unauthenticated to a credential-less request; run
// `leapmux auth login --hub <url>` first.
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

// NewClientFromEnv chooses the transport of a command. The order of
// precedence is:
//
//  1. An explicit --hub flag. The caller passes it only when the user typed
//     it, so a tab's own socket cannot override a hub the user asked for.
//  2. The tab's own socket, LEAPMUX_CONTROL_SOCK, inside a LeapMux tab.
//  3. LEAPMUX_HUB.
//  4. The default hub of the stored credentials (see DefaultHub).
func NewClientFromEnv(explicitHub string) (*Client, error) {
	if explicitHub != "" {
		if strings.HasPrefix(explicitHub, "unix:") || strings.HasPrefix(explicitHub, "npipe:") {
			return NewClientOrAnonymous(explicitHub)
		}
		return NewClient(explicitHub)
	}
	if sock := os.Getenv(controlenv.SocketVariable); sock != "" {
		return NewLocalClient(sock, os.Getenv(controlenv.TokenVariable))
	}
	hub, err := DefaultHub()
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(hub, "unix:") || strings.HasPrefix(hub, "npipe:") {
		return NewClientOrAnonymous(hub)
	}
	return NewClient(hub)
}

// NewLocalClient targets a per-agent local IPC socket. The token is
// presented via the X-LeapMux-Token header on every request.
func NewLocalClient(socketURL, token string) (*Client, error) {
	if socketURL == "" || token == "" {
		return nil, errors.New("local IPC socket and token required")
	}
	endpoint, err := hubtransport.New(socketURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		HubURL:     socketURL,
		bearer:     token,
		HTTPClient: endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout),
		// Both lanes come from ONE endpoint, so the timed unary calls and the
		// untimed streams share its connection pool and its protocol verdict.
		StreamHTTPClient: endpoint.StreamClient(),
		connectURL:       endpoint.BaseURL(),
		peer:             peerWorkerIPC,
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

// applyAuthTo stamps the credential header the PEER consumes through set.
// Worker-IPC peers read X-LeapMux-Token; hub peers read Authorization:
// Bearer — including a hub addressed by a unix:/npipe: URL, whose listener
// is the hub's own and expects the same Bearer as its http(s) face.
//
// It takes a setter rather than a header so the one decision serves both
// header shapes this package touches: http.Header on a hand-built form
// post, and the *connect.Header that carries Connect request metadata.
func (c *Client) applyAuthTo(set func(key, value string)) {
	bearer := c.currentBearer()
	if bearer == "" {
		return
	}
	if c.IsWorkerIPC() {
		set("X-LeapMux-Token", bearer)
		return
	}
	set("Authorization", "Bearer "+bearer)
}

// ApplyAuth stamps the credential header the PEER consumes. Exported so
// callers outside this package can apply the same auth shape to a
// hand-constructed request (the same rationale as AuthInterceptor).
func (c *Client) ApplyAuth(headers http.Header) {
	c.applyAuthTo(headers.Set)
}

// currentBearer reads the access token under the lock a refresh writes it
// under.
func (c *Client) currentBearer() string {
	c.bearerMu.RLock()
	defer c.bearerMu.RUnlock()
	return c.bearer
}

// setBearer adopts a rotated access token and its expiry. The two move
// together, so the cached expiry can never describe a different token from
// the one the header carries.
func (c *Client) setBearer(bearer string, expiresAt time.Time) {
	c.bearerMu.Lock()
	defer c.bearerMu.Unlock()
	c.bearer = bearer
	c.accessExpiresAt = expiresAt
}

// bearerNeedsRenewal reports whether the proactive check must read the
// credential file at all.
//
// An UNKNOWN expiry (the zero value) answers true, so a client that never
// learned one still consults the file. That is what keeps this a pure
// short-circuit: it can skip a read, never a renewal.
func (c *Client) bearerNeedsRenewal() bool {
	c.bearerMu.RLock()
	defer c.bearerMu.RUnlock()
	return c.accessExpiresAt.IsZero() || time.Until(c.accessExpiresAt) <= refreshSkew
}

// newConnectClient applies the shared authentication and response-size policy.
// The caller chooses the HTTP client because subscriptions need no overall timeout.
func (c *Client) newConnectClient(httpClient *http.Client) *connect.Client {
	return connect.NewClient(connecthttp.NewTransport(httpClient, c.connectURL,
		connecthttp.WithReadMaxBytes(0)), c.AuthInterceptor())
}

// WorkspaceService returns a ConnectRPC client for the hub-side
// WorkspaceService. An interceptor injects the auth headers.
func (c *Client) WorkspaceService() leapmuxv1connect.WorkspaceServiceClient {
	return leapmuxv1connect.NewWorkspaceServiceClient(c.newConnectClient(c.HTTPClient))
}

// WorkerManagementService returns a ConnectRPC client for ListWorkers.
func (c *Client) WorkerManagementService() leapmuxv1connect.WorkerManagementServiceClient {
	return leapmuxv1connect.NewWorkerManagementServiceClient(c.newConnectClient(c.HTTPClient))
}

// UserCRDT returns a ConnectRPC client for the unary SubmitOps and
// UpdatePresence calls. The user-event subscription (formerly the
// `WatchUser` streaming RPC) lives on `/ws/userevents` — see
// `OpenUserEvents`. An interceptor injects the auth headers.
func (c *Client) UserCRDT() leapmuxv1connect.UserCRDTClient {
	return leapmuxv1connect.NewUserCRDTClient(c.newConnectClient(c.HTTPClient))
}

// ChannelService returns a ConnectRPC client for OpenChannel /
// GetWorkerHandshakeParams when the CLI runs an E2EE inner RPC
// directly (rare; most callers use OpenE2EEChannel below).
func (c *Client) ChannelService() leapmuxv1connect.ChannelServiceClient {
	return leapmuxv1connect.NewChannelServiceClient(c.newConnectClient(c.HTTPClient))
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
	return c.controlIPCService(c.HTTPClient)
}

// ControlIPCStreamService is ControlIPCService for a SERVER STREAM.
//
// StreamInner's body ends only when the stream does, and an http.Client
// timeout covers the body read — so the timed client severed `layout watch`
// over local IPC and `agent messages --follow` after exactly 60 seconds, with
// a transport error rather than an end of stream. This client holds no
// overall timeout; the caller's context is what ends the subscription.
func (c *Client) ControlIPCStreamService() (leapmuxv1connect.ControlIPCServiceClient, error) {
	return c.controlIPCService(c.StreamHTTPClient)
}

func (c *Client) controlIPCService(httpClient *http.Client) (leapmuxv1connect.ControlIPCServiceClient, error) {
	if !c.IsWorkerIPC() {
		return nil, errors.New("ControlIPCService is only valid for worker-IPC clients constructed via NewLocalClient")
	}
	if httpClient == nil {
		return nil, errors.New("ControlIPCService: this client carries no HTTP client for that lane")
	}
	return leapmuxv1connect.NewControlIPCServiceClient(c.newConnectClient(httpClient)), nil
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
// peer closes cleanly, and returns any other transport error verbatim.
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
// this hub. The Authorization header carries the bearer; the bearer
// implies the user, so the request sends no user_id. The returned stream's first
// event is always `UserMaterialized` (the bootstrap snapshot). Only valid
// for non-local clients — local-IPC clients should use the worker's
// per-agent delegation bearer to reach the hub directly (the worker is
// not in this path).
func (c *Client) OpenUserEvents(ctx context.Context, workspaceIDs []string) (*UserEventsStream, error) {
	if c.IsWorkerIPC() {
		return nil, errors.New("OpenUserEvents is only valid for hub-bound clients; use the agent-spawned hub URL + delegation bearer to subscribe directly")
	}
	// A long-lived subscription must not open on a token that is about to
	// lapse: the WebSocket carries no retry, so it would simply close. The
	// renewal and the read are ONE call, so a later caller cannot do the
	// second without the first.
	bearer, err := c.freshBearer(ctx)
	if err != nil {
		return nil, err
	}
	dialCtx, dialCancel := context.WithCancel(ctx)
	ws, err := channelwire.OpenUserEventsWS(dialCtx, c.WSClient, c.connectURL, bearer, workspaceIDs, nil, 0)
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
	// A nil *PinStore would still satisfy tunnel.KeyPinStore as a typed-nil
	// interface, so tunnel.OpenChannel's `pinStore != nil` guard would call
	// straight into a nil receiver. Open the store here and report a failure
	// instead: a hub-bound open that skipped TOFU verification silently is a
	// downgrade, not a fallback.
	pins, err := c.pinStore()
	if err != nil {
		return nil, fmt.Errorf("open TOFU pin store: %w", err)
	}
	// Same reason as OpenUserEvents: a channel outlives one request, and the
	// upgrade carries the bearer exactly once.
	bearer, err := c.freshBearer(operationCtx)
	if err != nil {
		return nil, err
	}
	return tunnel.OpenChannel(operationCtx, c.connectURL, workerID, &tunnel.OpenChannelOptions{
		HTTPClient:          c.HTTPClient,
		WebSocketHTTPClient: c.WSClient,
		LifetimeContext:     lifetimeCtx,
		BearerToken:         bearer,
		// The CLI resolves workspaces/workers under c.UserID (see resolve.Resolver
		// and cmd/workspace.go), so it DOES have an expectation: creds whose bearer
		// and user_id no longer match -- a rotated or reassigned token -- would make
		// it resolve as X while it runs channel RPCs as Y. Empty c.UserID (creds
		// predating user_id resolution) leaves the cross-check disabled, which is
		// exactly the no-expectation case OpenChannel skips.
		ExpectedUserID: c.UserID,
		KeyPin:         pins,
	})
}

// UserService returns a ConnectRPC client for the caller's OWN account
// (its CLI credentials, and the passkey and password surface the browser
// drives). Not an Admin* client: every procedure on it acts on the account
// the bearer belongs to.
func (c *Client) UserService() leapmuxv1connect.UserServiceClient {
	return leapmuxv1connect.NewUserServiceClient(c.newConnectClient(c.HTTPClient))
}

// AuthService returns a ConnectRPC client for the session surface
// (GetCurrentUser). `auth status` and `whoami` call it as their hub check:
// one authenticated read settles whether the credential still opens the
// hub, and the interceptor beneath it makes the check self-healing -- a
// stale access token rotates on the way through, and a revoked one ends in
// invalid_grant with the credential file already deleted.
func (c *Client) AuthService() leapmuxv1connect.AuthServiceClient {
	return leapmuxv1connect.NewAuthServiceClient(c.newConnectClient(c.HTTPClient))
}

// SystemService returns a ConnectRPC client for what the hub reports about
// itself: the public system information and the timeout configuration.
func (c *Client) SystemService() leapmuxv1connect.SystemServiceClient {
	return leapmuxv1connect.NewSystemServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminSettingsService returns a ConnectRPC client for the hub's
// AdminSettingsService. Admin commands deliberately build a hub client
// directly rather than routing through the worker-IPC bridge: the
// hubrpc.Registry table is a typing device, not a security boundary
// (any worker-spawned agent can call whatever it lists), so no admin
// procedure is registered there.
func (c *Client) AdminSettingsService() leapmuxv1connect.AdminSettingsServiceClient {
	return leapmuxv1connect.NewAdminSettingsServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminUserService returns a ConnectRPC client for AdminUserService
// (users, sessions, api-tokens, delegation-tokens).
func (c *Client) AdminUserService() leapmuxv1connect.AdminUserServiceClient {
	return leapmuxv1connect.NewAdminUserServiceClient(c.newConnectClient(c.HTTPClient))
}

// AppService is the app REGISTRATION surface.
//
// It is NOT an admin service, although `control admin app` is where the CLI
// puts it: an ordinary user registers apps for themself through the same RPCs,
// and ownership rather than a role decides what each caller sees. Only two
// verbs need an administrator -- a hub-wide registration and a vouch -- and
// each says so in its own handler.
func (c *Client) AppService() leapmuxv1connect.AppServiceClient {
	return leapmuxv1connect.NewAppServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminWorkerService returns a ConnectRPC client for AdminWorkerService
// (cross-user worker administration and registration keys).
func (c *Client) AdminWorkerService() leapmuxv1connect.AdminWorkerServiceClient {
	return leapmuxv1connect.NewAdminWorkerServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminIdPService returns a ConnectRPC client for AdminIdPService.
func (c *Client) AdminIdPService() leapmuxv1connect.AdminIdPServiceClient {
	return leapmuxv1connect.NewAdminIdPServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminNetworkService returns a ConnectRPC client for AdminNetworkService.
func (c *Client) AdminNetworkService() leapmuxv1connect.AdminNetworkServiceClient {
	return leapmuxv1connect.NewAdminNetworkServiceClient(c.newConnectClient(c.HTTPClient))
}
