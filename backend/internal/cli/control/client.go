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

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/tunnel"
)

// Peer is the sealed sum of the two transports a CLI command can talk to:
// a direct *HubClient or an agent-spawned *WorkerIPCClient.
//
// Commands that talk only to the hub take *HubClient; commands or helpers
// that talk only to the local tab socket take *WorkerIPCClient. Dual-transport
// helpers accept Peer and switch on the concrete type so calling a hub-only
// method on a worker-IPC client (or ControlIPCService on a hub client) is a
// compile error rather than a runtime check.
type Peer interface {
	ConnectURL() string
	ApplyAuth(headers http.Header)
	AuthInterceptor() connect.ClientInterceptor
	controlPeer()
}

// ErrorDecorator transforms transport errors returned by a Peer before callers
// see them (for example, attaching CLI hints or version diagnostics).
type ErrorDecorator func(ctx context.Context, p Peer, err error) error

// HubClient is the CLI's direct connection to a hub, over http(s) or the hub's
// own unix:/npipe: IPC listener. It holds the credential (Bearer token), HTTP
// and WebSocket clients, TOFU pin store, and ConnectRPC service clients for
// hub-side RPCs.
//
// HubURL is the user-visible address (`https://hub.example` or a
// `unix:`/`npipe:` IPC URL). connectURL is what ConnectRPC sees as the base
// URL: identical to HubURL for remote hubs, or a placeholder `http://localhost`
// for local-IPC hubs because net/http rejects non-http(s) schemes while the
// custom dialer dials the real socket.
type HubClient struct {
	HubURL string
	// Source identifies how HubURL was selected when constructed through
	// hub-address resolution.
	Source HubSource
	// bearer is the access token presented on every call.
	//
	// UNEXPORTED, and that is the mechanism rather than a style choice: a
	// refresh replaces it mid-flight while a stream on another goroutine
	// reads it, so every access must hold bearerMu. Tests reach it through
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
	// pins is the per-hub TOFU pin store, opened on first use by pinStore.
	pins             *PinStore
	pinsOnce         sync.Once
	pinsErr          error
	UserID           string
	Username         string
	connectURL       string
	errOutMu         sync.RWMutex
	errOut           io.Writer
	errorDecoratorMu sync.RWMutex
	errorDecorator   ErrorDecorator
	promptAllowed    bool
}

func (*HubClient) controlPeer() {}

// WorkerIPCClient is the CLI's connection to the worker-local per-agent IPC
// socket spawned inside a LeapMux tab. It authenticates with X-LeapMux-Token
// and exposes only ControlIPCService RPCs.
type WorkerIPCClient struct {
	SocketURL  string
	token      string
	HTTPClient *http.Client
	// StreamHTTPClient carries a Connect SERVER STREAM, so it holds no overall
	// timeout: `layout watch` over local IPC and `agent messages --follow` both
	// open ControlIPCService.StreamInner, which runs on this HTTP client rather
	// than on a WebSocket.
	StreamHTTPClient *http.Client
	connectURL       string
	errorDecoratorMu sync.RWMutex
	errorDecorator   ErrorDecorator
}

func (*WorkerIPCClient) controlPeer() {}

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
// only when a person acts on os.Stdin.
func promptsAllowed() bool {
	return promptsAllowedFor(os.Stdin)
}

// promptsAllowedFor reports whether this process may open a prompt on in.
func promptsAllowedFor(in *os.File) bool {
	return os.Getenv(noPromptEnv) == "" && cli.IsTerminalFile(in)
}

// noPromptReason states WHY this process may not prompt, so a refusal can
// state the condition the caller is able to change.
func noPromptReason() string {
	if os.Getenv(noPromptEnv) != "" {
		return noPromptEnv + " is set"
	}
	return "stdin is not a terminal"
}

// ConnectURL returns the base URL ConnectRPC clients should use.
// Equal to HubURL for remote hubs; equal to a placeholder `http://localhost`
// for local-IPC hubs (the h2c transport dials the real unix/npipe socket).
func (c *HubClient) ConnectURL() string {
	return c.connectURL
}

// SetErrOut sets the writer that interactive ceremonies (such as browser
// step-up) write prompts to. Passing nil resets to os.Stderr.
func (c *HubClient) SetErrOut(w io.Writer) {
	c.errOutMu.Lock()
	c.errOut = w
	c.errOutMu.Unlock()
}

// SetErrorDecorator registers a transport-level error decorator invoked on
// ConnectRPC and WebSocket errors returned by c. Passing nil clears it.
func (c *HubClient) SetErrorDecorator(fn ErrorDecorator) {
	c.errorDecoratorMu.Lock()
	c.errorDecorator = fn
	c.errorDecoratorMu.Unlock()
}

func (c *HubClient) decorateError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	c.errorDecoratorMu.RLock()
	fn := c.errorDecorator
	c.errorDecoratorMu.RUnlock()
	if fn == nil {
		return err
	}
	return fn(ctx, c, err)
}

// SetErrorDecorator registers a transport-level error decorator invoked on
// ConnectRPC errors returned by c. Passing nil clears it.
func (c *WorkerIPCClient) SetErrorDecorator(fn ErrorDecorator) {
	c.errorDecoratorMu.Lock()
	c.errorDecorator = fn
	c.errorDecoratorMu.Unlock()
}

func (c *WorkerIPCClient) decorateError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	c.errorDecoratorMu.RLock()
	fn := c.errorDecorator
	c.errorDecoratorMu.RUnlock()
	if fn == nil {
		return err
	}
	return fn(ctx, c, err)
}

func newErrorDecoratorInterceptor(decorate func(context.Context, error) error) connect.ClientInterceptor {
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			stream, err := next(ctx, spec)
			if err != nil {
				return nil, decorate(ctx, err)
			}
			return &errorDecoratingStream{
				ClientStream: stream,
				ctx:          ctx,
				decorate:     decorate,
			}, nil
		}
	}
}

type errorDecoratingStream struct {
	connect.ClientStream
	ctx      context.Context
	decorate func(context.Context, error) error
}

func (s *errorDecoratingStream) SendHeaders() error {
	return s.decorate(s.ctx, s.ClientStream.SendHeaders())
}

func (s *errorDecoratingStream) Send(msg any) error {
	return s.decorate(s.ctx, s.ClientStream.Send(msg))
}

func (s *errorDecoratingStream) CloseSend() error {
	return s.decorate(s.ctx, s.ClientStream.CloseSend())
}

func (s *errorDecoratingStream) Receive(msg any) error {
	err := s.ClientStream.Receive(msg)
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	return s.decorate(s.ctx, err)
}

// errWriter returns the configured error writer, defaulting to os.Stderr.
func (c *HubClient) errWriter() io.Writer {
	c.errOutMu.RLock()
	w := c.errOut
	c.errOutMu.RUnlock()
	if w != nil {
		return w
	}
	return os.Stderr
}

// newHubClient builds a hub-bound client for hubURL, carrying creds when
// the caller holds them and running anonymously when creds is nil.
//
// Both constructors below route through it, so a field added to HubClient
// reaches both. The two differ ONLY in how they treat a credential that
// does not load, which is the difference each one documents.
//
// Both HTTP clients come from one hubtransport.Endpoint, so they share its
// connection pools and its h2c verdict. The WebSocket client is never nil,
// including for a remote hub: a nil one made coder/websocket fall back to
// http.DefaultClient, which carries no timeout of its own and shares the
// process-global pool with every other caller in the program.
func newHubClient(hubURL string, creds *CredentialFile) (*HubClient, error) {
	endpoint, err := hubtransport.New(hubURL)
	if err != nil {
		return nil, err
	}
	c := &HubClient{
		HubURL:        hubURL,
		HTTPClient:    endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout),
		WSClient:      endpoint.WebSocketClient(),
		connectURL:    endpoint.BaseURL(),
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
func (c *HubClient) pinStore() (*PinStore, error) {
	// The preset test is INSIDE the Once, so two concurrent opens read the
	// field through the same happens-before edge. Reading it first as a
	// fast path is a data race with the Once body that writes it.
	c.pinsOnce.Do(func() {
		if c.pins != nil {
			return
		}
		c.pins, c.pinsErr = NewPinStore(c.HubURL)
	})
	if c.pinsErr != nil {
		return nil, c.pinsErr
	}
	return c.pins, nil
}

// NewClient constructs a hub client from the on-disk credentials for
// hubURL. Returns ErrNotLoggedIn if no credentials exist.
func NewClient(hubURL string) (*HubClient, error) {
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
func NewClientOrAnonymous(hubURL string) (*HubClient, error) {
	creds, err := LoadCredentials(hubURL)
	if err != nil && !errors.Is(err, ErrNotLoggedIn) {
		return nil, err
	}
	return newHubClient(hubURL, creds)
}

// NewClientForHub constructs a hub client for hubURL, allowing anonymous access
// on local IPC sockets (where a solo Hub authenticates the local socket as the
// solo user) and requiring stored credentials on remote HTTP(S) hubs.
func NewClientForHub(hubURL string) (*HubClient, error) {
	if locallisten.IsLocal(hubURL) {
		return NewClientOrAnonymous(hubURL)
	}
	return NewClient(hubURL)
}

// NewClientFromEnv chooses the transport of a command. The order of
// precedence is:
//
//  1. An explicit --hub flag. The caller passes it only when the user typed
//     it, so a tab's own socket cannot override a hub the user asked for.
//  2. The tab's own socket, LEAPMUX_CONTROL_SOCK, inside a LeapMux tab.
//  3. LEAPMUX_HUB.
//  4. The default hub of the stored credentials (see DefaultHub).
//
// The returned Peer is either a *HubClient or a *WorkerIPCClient.
func NewClientFromEnv(explicitHub string) (Peer, error) {
	if explicitHub != "" {
		c, err := NewClientForHub(explicitHub)
		if err != nil {
			return nil, err
		}
		c.Source = HubSourceFlag
		return c, nil
	}
	if sock := os.Getenv(controlenv.SocketVariable); sock != "" {
		return NewLocalClient(sock, os.Getenv(controlenv.TokenVariable))
	}
	hub, source, creds, err := resolveDefaultHub(true)
	if err != nil {
		return nil, err
	}
	var c *HubClient
	if creds != nil {
		c, err = newHubClient(hub, creds)
	} else {
		c, err = NewClientForHub(hub)
	}
	if err != nil {
		return nil, err
	}
	c.Source = source
	return c, nil
}

// NewLocalClient targets a per-agent local IPC socket. The token is
// presented via the X-LeapMux-Token header on every request.
func NewLocalClient(socketURL, token string) (*WorkerIPCClient, error) {
	if socketURL == "" || token == "" {
		return nil, errors.New("local IPC socket and token required")
	}
	endpoint, err := hubtransport.New(socketURL)
	if err != nil {
		return nil, err
	}
	return &WorkerIPCClient{
		SocketURL:  socketURL,
		token:      token,
		HTTPClient: endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout),
		// Both lanes come from ONE endpoint, so the timed unary calls and the
		// untimed streams share its connection pool and its protocol verdict.
		StreamHTTPClient: endpoint.StreamClient(),
		connectURL:       endpoint.BaseURL(),
	}, nil
}

// ConnectURL returns the placeholder HTTP base URL ConnectRPC uses over the
// worker IPC socket transport.
func (c *WorkerIPCClient) ConnectURL() string {
	return c.connectURL
}

func (c *WorkerIPCClient) applyAuthTo(set func(key, value string)) {
	if c.token == "" {
		return
	}
	set("X-LeapMux-Token", c.token)
}

// ApplyAuth stamps the X-LeapMux-Token header consumed by the worker IPC server.
func (c *WorkerIPCClient) ApplyAuth(headers http.Header) {
	c.applyAuthTo(headers.Set)
}

func (c *WorkerIPCClient) newConnectClient(httpClient *http.Client) *connect.Client {
	return connect.NewClient(connecthttp.NewTransport(httpClient, c.connectURL,
		connecthttp.WithReadMaxBytes(0)), newErrorDecoratorInterceptor(c.decorateError), c.AuthInterceptor())
}

// ControlIPCService returns a ConnectRPC client for unary calls to the
// worker-local IPC service.
func (c *WorkerIPCClient) ControlIPCService() (leapmuxv1connect.ControlIPCServiceClient, error) {
	return c.controlIPCService(c.HTTPClient)
}

// ControlIPCStreamService is ControlIPCService for a SERVER STREAM.
//
// StreamInner's body ends only when the stream does, and an http.Client
// timeout covers the body read — so the timed client severed `layout watch`
// over local IPC and `agent messages --follow` after exactly 60 seconds, with
// a transport error rather than an end of stream. This client holds no
// overall timeout; the caller's context is what ends the subscription.
func (c *WorkerIPCClient) ControlIPCStreamService() (leapmuxv1connect.ControlIPCServiceClient, error) {
	return c.controlIPCService(c.StreamHTTPClient)
}

func (c *WorkerIPCClient) controlIPCService(httpClient *http.Client) (leapmuxv1connect.ControlIPCServiceClient, error) {
	if httpClient == nil {
		return nil, errors.New("ControlIPCService: this client carries no HTTP client for that lane")
	}
	return leapmuxv1connect.NewControlIPCServiceClient(c.newConnectClient(httpClient)), nil
}

// applyAuthTo stamps Authorization: Bearer through set when c holds a bearer.
func (c *HubClient) applyAuthTo(set func(key, value string)) {
	bearer := c.currentBearer()
	if bearer == "" {
		return
	}
	set("Authorization", "Bearer "+bearer)
}

// ApplyAuth stamps Authorization: Bearer for a hub request. Exported so
// callers outside this package can apply the same auth shape to a
// hand-constructed request (the same rationale as AuthInterceptor).
func (c *HubClient) ApplyAuth(headers http.Header) {
	c.applyAuthTo(headers.Set)
}

// currentBearer reads the access token under the lock a refresh writes it
// under.
func (c *HubClient) currentBearer() string {
	c.bearerMu.RLock()
	defer c.bearerMu.RUnlock()
	return c.bearer
}

// setBearer adopts a rotated access token and its expiry. The two move
// together, so the cached expiry can never describe a different token from
// the one the header carries.
func (c *HubClient) setBearer(bearer string, expiresAt time.Time) {
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
func (c *HubClient) bearerNeedsRenewal() bool {
	c.bearerMu.RLock()
	defer c.bearerMu.RUnlock()
	return c.accessExpiresAt.IsZero() || time.Until(c.accessExpiresAt) <= refreshSkew
}

// newConnectClient applies the shared error-decoration, authentication, and
// response-size policy.
func (c *HubClient) newConnectClient(httpClient *http.Client) *connect.Client {
	return connect.NewClient(connecthttp.NewTransport(httpClient, c.connectURL,
		connecthttp.WithReadMaxBytes(0)), newErrorDecoratorInterceptor(c.decorateError), c.AuthInterceptor())
}

// ConnectClient returns a ConnectRPC client using c.HTTPClient with the shared
// authentication and response-size policy.
func (c *HubClient) ConnectClient() *connect.Client {
	return c.newConnectClient(c.HTTPClient)
}

// WorkspaceService returns a ConnectRPC client for the hub-side
// WorkspaceService. An interceptor injects the auth headers.
func (c *HubClient) WorkspaceService() leapmuxv1connect.WorkspaceServiceClient {
	return leapmuxv1connect.NewWorkspaceServiceClient(c.newConnectClient(c.HTTPClient))
}

// WorkerManagementService returns a ConnectRPC client for ListWorkers.
func (c *HubClient) WorkerManagementService() leapmuxv1connect.WorkerManagementServiceClient {
	return leapmuxv1connect.NewWorkerManagementServiceClient(c.newConnectClient(c.HTTPClient))
}

// UserEventsStream is a read-only WebSocket subscription to the hub's
// `/ws/userevents` endpoint. Each `Recv` returns the next decoded
// `WatchUserEvent` proto; the first event depends on the subscribed topics and
// resume cursor (for example, `Initial`, `Delta`, `LayoutEvents`, or
// `WorkersChanged`). Close cancels the stream and tears down the underlying
// WebSocket.
type UserEventsStream struct {
	ws       *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	decorate func(context.Context, error) error
}

func wrapHubConnectionError(err error) error {
	if mapped, ok := channelwire.ConnectErrorFromWebSocket(err); ok {
		return mapped
	}
	return err
}

// Recv reads the next event from the stream. Returns io.EOF when the peer
// closes cleanly, and wraps classified Hub close errors while keeping the
// underlying error reachable through Unwrap.
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
		err = wrapHubConnectionError(err)
		if s.decorate != nil {
			err = s.decorate(s.ctx, err)
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

// OpenUserEvents opens a `/ws/userevents` WebSocket subscription against this
// hub with explicit topic, workspace, and cursor options. The Authorization
// header carries the bearer; the bearer implies the user, so the request sends
// no user_id.
func (c *HubClient) OpenUserEvents(ctx context.Context, opts channelwire.UserEventsOptions) (*UserEventsStream, error) {
	// A long-lived subscription must not open on a token that is about to
	// lapse: the WebSocket carries no retry, so it would simply close. The
	// renewal and the read are ONE call, so a later caller cannot do the
	// second without the first.
	bearer, err := c.freshBearer(ctx)
	if err != nil {
		return nil, c.decorateError(ctx, err)
	}
	dialCtx, dialCancel := context.WithCancel(ctx)
	ws, err := channelwire.OpenUserEventsWS(dialCtx, c.WSClient, c.connectURL, bearer, opts)
	if err != nil {
		dialCancel()
		return nil, c.decorateError(ctx, wrapHubConnectionError(err))
	}
	return &UserEventsStream{ws: ws, ctx: dialCtx, cancel: dialCancel, decorate: c.decorateError}, nil
}

// OpenE2EEChannel opens a Noise_NK E2EE channel to the named worker
// via the hub relay. Uses the credential's bearer token and the
// per-hub TOFU pin store.
func (c *HubClient) OpenE2EEChannel(operationCtx, lifetimeCtx context.Context, workerID string) (*tunnel.Channel, error) {
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
		return nil, c.decorateError(operationCtx, err)
	}
	ch, err := tunnel.OpenChannel(operationCtx, c.connectURL, workerID, &tunnel.OpenChannelOptions{
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
	if err != nil {
		return nil, c.decorateError(operationCtx, wrapHubConnectionError(err))
	}
	return ch, nil
}

// UserService returns a ConnectRPC client for the caller's OWN account
// (its CLI credentials, and the passkey and password surface the browser
// drives). Not an Admin* client: every procedure on it acts on the account
// the bearer belongs to.
func (c *HubClient) UserService() leapmuxv1connect.UserServiceClient {
	return leapmuxv1connect.NewUserServiceClient(c.newConnectClient(c.HTTPClient))
}

// AuthService returns a ConnectRPC client for the session surface
// (GetCurrentUser). `auth status` and `whoami` call it as their hub check:
// one authenticated read settles whether the credential still opens the
// hub, and the interceptor beneath it makes the check self-healing -- a
// stale access token rotates on the way through, and a revoked one ends in
// invalid_grant with the credential file already deleted.
func (c *HubClient) AuthService() leapmuxv1connect.AuthServiceClient {
	return leapmuxv1connect.NewAuthServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminSettingsService returns a ConnectRPC client for the hub's
// AdminSettingsService. Admin commands deliberately build a hub client
// directly rather than routing through the worker-IPC bridge: the
// hubrpc.Registry table is a typing device, not a security boundary
// (any worker-spawned agent can call whatever it lists), so no admin
// procedure is registered there.
func (c *HubClient) AdminSettingsService() leapmuxv1connect.AdminSettingsServiceClient {
	return leapmuxv1connect.NewAdminSettingsServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminUserService returns a ConnectRPC client for AdminUserService
// (users, sessions, api-tokens, delegation-tokens).
func (c *HubClient) AdminUserService() leapmuxv1connect.AdminUserServiceClient {
	return leapmuxv1connect.NewAdminUserServiceClient(c.newConnectClient(c.HTTPClient))
}

// AppService is the app REGISTRATION surface.
//
// It is NOT an admin service, although `control admin app` is where the CLI
// puts it: an ordinary user registers apps for themself through the same RPCs,
// and ownership rather than a role decides what each caller sees. Only two
// verbs need an administrator -- a hub-wide registration and a vouch -- and
// each says so in its own handler.
func (c *HubClient) AppService() leapmuxv1connect.AppServiceClient {
	return leapmuxv1connect.NewAppServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminWorkerService returns a ConnectRPC client for AdminWorkerService
// (cross-user worker administration and registration keys).
func (c *HubClient) AdminWorkerService() leapmuxv1connect.AdminWorkerServiceClient {
	return leapmuxv1connect.NewAdminWorkerServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminIdPService returns a ConnectRPC client for AdminIdPService.
func (c *HubClient) AdminIdPService() leapmuxv1connect.AdminIdPServiceClient {
	return leapmuxv1connect.NewAdminIdPServiceClient(c.newConnectClient(c.HTTPClient))
}

// AdminNetworkService returns a ConnectRPC client for AdminNetworkService.
func (c *HubClient) AdminNetworkService() leapmuxv1connect.AdminNetworkServiceClient {
	return leapmuxv1connect.NewAdminNetworkServiceClient(c.newConnectClient(c.HTTPClient))
}
