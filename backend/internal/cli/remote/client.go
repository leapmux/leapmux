package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
// Construct with NewClient; in agent-spawned mode (LEAPMUX_REMOTE_*
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
	Pins       *PinStore
	UserID     string
	Username   string
	connectURL string
}

// ConnectURL returns the base URL ConnectRPC clients should use.
// Equal to HubURL for hub-bound clients; equal to a placeholder
// `http://localhost` for local-IPC clients (the h2c transport dials
// the real unix/npipe socket regardless of host).
func (c *Client) ConnectURL() string {
	return c.connectURL
}

// defaultHTTPTimeout is the per-request timeout on the unary HTTP
// client used for ConnectRPC unary calls and the auth/version REST
// endpoints. Streaming RPCs (org events, agent-message follow) use
// a separate WebSocket client with no overall timeout.
const defaultHTTPTimeout = 60 * time.Second

// NewClient constructs a hub client from the on-disk credentials for
// hubURL. Returns ErrNotLoggedIn if no credentials exist.
func NewClient(hubURL string) (*Client, error) {
	creds, err := LoadCredentials(hubURL)
	if err != nil {
		return nil, err
	}
	httpClient, wsClient, connectURL, err := buildHTTPClients(hubURL)
	if err != nil {
		return nil, err
	}
	pins, err := NewPinStore(hubURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		HubURL:     hubURL,
		Bearer:     creds.AccessToken,
		HTTPClient: httpClient,
		WSClient:   wsClient,
		Pins:       pins,
		UserID:     creds.UserID,
		Username:   creds.Username,
		connectURL: connectURL,
	}, nil
}

// NewClientFromEnv chooses the right transport based on env vars.
// In worker-spawned mode (LEAPMUX_REMOTE_SOCK set) it returns a client
// targeting the local socket. Otherwise it falls back to NewClient
// using the --hub flag (or LEAPMUX_HUB env var).
func NewClientFromEnv(hubFlag string) (*Client, error) {
	if sock := os.Getenv("LEAPMUX_REMOTE_SOCK"); sock != "" {
		return NewLocalClient(sock, os.Getenv("LEAPMUX_REMOTE_TOKEN"))
	}
	url := hubFlag
	if url == "" {
		url = os.Getenv("LEAPMUX_HUB")
	}
	if url == "" {
		return nil, errors.New("no --hub flag or LEAPMUX_HUB / LEAPMUX_REMOTE_SOCK env var; run `leapmux remote auth login --hub <url>` or invoke from inside an agent")
	}
	return NewClient(url)
}

// NewLocalClient targets a per-agent local IPC socket. The token is
// presented via the X-Leapmux-Token header on every request.
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
	}, nil
}

// IsLocal reports whether this client targets a per-agent IPC socket
// rather than a hub.
func (c *Client) IsLocal() bool {
	return locallisten.IsLocal(c.HubURL)
}

// authHeader returns the appropriate Authorization header for this
// client's transport. Local IPC clients use X-Leapmux-Token; hub
// clients use a Bearer header.
func (c *Client) applyAuth(headers http.Header) {
	if c.Bearer == "" {
		return
	}
	if c.IsLocal() {
		headers.Set("X-Leapmux-Token", c.Bearer)
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

// OrgCRDT returns a ConnectRPC client for the unary SubmitOps and
// UpdatePresence calls. The org-event subscription (formerly the
// `WatchOrg` streaming RPC) lives on `/ws/orgevents` — see
// `OpenOrgEvents`. Auth headers are injected via an interceptor.
func (c *Client) OrgCRDT() leapmuxv1connect.OrgCRDTClient {
	return leapmuxv1connect.NewOrgCRDTClient(
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

// RemoteIPCService returns a ConnectRPC client for the worker-local
// IPC service. Only valid for clients constructed via NewLocalClient.
func (c *Client) RemoteIPCService() leapmuxv1connect.RemoteIPCServiceClient {
	return leapmuxv1connect.NewRemoteIPCServiceClient(
		c.HTTPClient, c.connectURL,
		connect.WithInterceptors(c.AuthInterceptor()),
	)
}

// OrgEventsStream is a read-only WebSocket subscription to the hub's
// `/ws/orgevents` endpoint. Each `Recv` returns the next decoded
// `WatchOrgEvent` proto (the first call always returns an `Initial`
// event). Close cancels the stream and tears down the underlying WS.
type OrgEventsStream struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

// Recv reads the next event from the stream. Returns io.EOF when the
// peer closes cleanly; any other transport error is returned verbatim.
func (s *OrgEventsStream) Recv() (*leapmuxv1.WatchOrgEvent, error) {
	if s == nil || s.ws == nil {
		return nil, io.EOF
	}
	// Wire format mirrors `writeOrgEvent` in ws_orgevents.go:
	// [4-byte big-endian length][protobuf-encoded WatchOrgEvent].
	payload, err := channelwire.ReadFramedBytes(s.ctx, s.ws)
	if err != nil {
		if channelwire.IsOrgEventsCloseError(err) {
			return nil, io.EOF
		}
		return nil, err
	}
	var evt leapmuxv1.WatchOrgEvent
	if err := proto.Unmarshal(payload, &evt); err != nil {
		return nil, fmt.Errorf("orgevents: decode event: %w", err)
	}
	return &evt, nil
}

// Close shuts the stream down.
func (s *OrgEventsStream) Close() error {
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

// OpenOrgEvents opens a `/ws/orgevents` WebSocket subscription against
// this hub. Bearer auth is added via the Authorization header. The
// returned stream's first event is always `OrgMaterialized` (the
// bootstrap snapshot). Only valid for non-local clients — local-IPC
// clients should use the worker's per-agent delegation bearer to
// reach the hub directly (the worker is not in this path).
func (c *Client) OpenOrgEvents(ctx context.Context, orgID string, workspaceIDs []string) (*OrgEventsStream, error) {
	if c.IsLocal() {
		return nil, errors.New("OpenOrgEvents is only valid for hub-bound clients; use the agent-spawned hub URL + delegation bearer to subscribe directly")
	}
	if orgID == "" {
		return nil, errors.New("OpenOrgEvents: org_id required")
	}
	dialCtx, dialCancel := context.WithCancel(ctx)
	ws, err := channelwire.OpenOrgEventsWS(dialCtx, c.WSClient, c.HubURL, c.Bearer, orgID, workspaceIDs)
	if err != nil {
		dialCancel()
		return nil, err
	}
	return &OrgEventsStream{ws: ws, ctx: dialCtx, cancel: dialCancel}, nil
}

// OpenE2EEChannel opens a Noise_NK E2EE channel to the named worker
// via the hub relay. Uses the credential's bearer token and the
// per-hub TOFU pin store.
func (c *Client) OpenE2EEChannel(operationCtx, lifetimeCtx context.Context, workerID string) (*tunnel.Channel, error) {
	if c.IsLocal() {
		return nil, errors.New("OpenE2EEChannel is only valid for hub-bound clients")
	}
	// A nil *PinStore would still satisfy tunnel.KeyPinStore as a typed-nil
	// interface, so tunnel.OpenChannel's `pinStore != nil` guard would call
	// straight into a nil receiver. Refuse loudly instead: NewClient always
	// builds a pin store, and a hub-bound open that skipped TOFU verification
	// silently is a downgrade, not a fallback.
	if c.Pins == nil {
		return nil, errors.New("OpenE2EEChannel: client has no TOFU pin store")
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
		KeyPin:         c.Pins,
	})
}

// AuthInterceptor adds the Authorization (or X-Leapmux-Token) header
// to every outgoing request. Exported so callers outside this package
// (e.g. cmd's hubrpc dispatch) can apply the same auth shape to a
// generically-constructed connect.NewClient.
//
// Wraps both unary AND streaming clients. `connect.UnaryInterceptorFunc`
// alone is a no-op on the streaming paths, which silently drops the
// `X-Leapmux-Token` / `Authorization` header from `StreamInner`,
// `OpenChannel`, and friends — the IPC server (or hub) then responds
// 401 and the CLI surfaces it as "unauthenticated: HTTP status 401
// Unauthorized". The streaming path matters here because CRDT
// bootstrap (`hub.WatchOrg`) and any future server-streaming RPC
// flow through it.
func (c *Client) AuthInterceptor() connect.Interceptor {
	return &authInterceptor{client: c}
}

// authInterceptor stamps c.applyAuth on every outgoing connect call,
// unary or streaming. WrapStreamingHandler is a no-op because the CLI
// only acts as a client.
type authInterceptor struct{ client *Client }

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		a.client.applyAuth(req.Header())
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		a.client.applyAuth(conn.RequestHeader())
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
