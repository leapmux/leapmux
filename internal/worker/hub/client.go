package hub

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v5"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"golang.org/x/net/http2"
)

// Client manages the connection to the Hub.
type Client struct {
	connector  leapmuxv1connect.WorkerConnectorServiceClient
	hubURL     string
	agents     *agent.Manager
	terminals  *terminal.Manager
	channelMgr *channel.Manager

	// OnDeregister is called when the Hub sends a deregistration notification.
	// The worker should clear its state and shut down gracefully.
	OnDeregister func()

	// PublicKey is the Worker's X25519 public key for E2EE channels.
	// Sent to the Hub with the initial heartbeat.
	PublicKey []byte
	// MlkemPublicKey is the Worker's ML-KEM-1024 public key.
	MlkemPublicKey []byte
	// SlhdsaPublicKey is the Worker's SLH-DSA-SHAKE-256f public key.
	SlhdsaPublicKey []byte
	// EncryptionMode is the Worker's encryption mode.
	EncryptionMode leapmuxv1.EncryptionMode

	// TabSyncProvider returns the current tab state for WorkspaceTabsSync
	// on connect. Set by the runner after initializing the service context.
	TabSyncProvider func() *leapmuxv1.WorkspaceTabsSync

	mu           sync.Mutex
	stream       *connect.BidiStreamForClient[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse]
	connCancel   context.CancelFunc // cancel function for current connection context
	lastSendTime time.Time          // last time a message was sent (for idle heartbeat)
	stopOnce     sync.Once

	// hubRetryDelay stores the retry delay (in seconds) requested by the Hub
	// when it sends a HubShuttingDownNotification. Consumed once by
	// connectWithReconnect after the connection drops.
	hubRetryDelay atomic.Int64
}

// New creates a new Hub client with integrated lifecycle management.
// It creates agent and terminal managers internally.
// If hubURL starts with "unix:", it creates a Unix domain socket transport automatically.
func New(hubURL string) *Client {
	if strings.HasPrefix(hubURL, "unix:") {
		socketPath := strings.TrimPrefix(hubURL, "unix:")
		return NewWithHTTPClient(newUnixSocketClient(socketPath), hubURL)
	}
	return NewWithHTTPClient(newH2CClient(), hubURL)
}

// NewWithHTTPClient creates a new Hub client that uses the provided HTTP client
// for ConnectRPC communication. This allows callers to provide a custom transport
// (e.g. one that dials via Unix domain socket).
func NewWithHTTPClient(httpClient *http.Client, hubURL string) *Client {
	// When connecting via Unix domain socket, hubURL is "unix:<path>".
	// ConnectRPC needs a valid HTTP base URL, so use http://localhost instead.
	connectURL := hubURL
	if strings.HasPrefix(hubURL, "unix:") {
		connectURL = "http://localhost"
	}

	c := &Client{
		connector: leapmuxv1connect.NewWorkerConnectorServiceClient(
			httpClient,
			connectURL,
			connect.WithGRPC(),
		),
		hubURL:    hubURL,
		terminals: terminal.NewManager(),
	}

	c.agents = agent.NewManager(func(agentID string, exitCode int, err error) {
		if err != nil {
			slog.Info("agent exited with error", "agent_id", agentID, "exit_code", exitCode, "error", err)
		} else {
			slog.Info("agent exited", "agent_id", agentID, "exit_code", exitCode)
		}
	})

	return c
}

// Stop gracefully stops all managers.
// Safe to call multiple times.
func (c *Client) Stop() {
	c.stopOnce.Do(func() {
		// Close all encrypted channels.
		if c.channelMgr != nil {
			c.channelMgr.CloseAll()
		}
		c.terminals.StopAll()
		c.agents.StopAll()
	})
}

// SetChannelMgr sets the encrypted channel manager for E2EE channel handling.
func (c *Client) SetChannelMgr(mgr *channel.Manager) {
	c.channelMgr = mgr
}

// AgentManager returns the agent manager.
func (c *Client) AgentManager() *agent.Manager {
	return c.agents
}

// TerminalManager returns the terminal manager.
func (c *Client) TerminalManager() *terminal.Manager {
	return c.terminals
}

// newH2CClient creates an HTTP client that supports HTTP/2 cleartext (h2c),
// which is required for gRPC bidirectional streaming over plain HTTP.
func newH2CClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

// newUnixSocketClient creates an h2c HTTP client that dials via a Unix domain socket.
func newUnixSocketClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// Send sends a message to the Hub via the bidi stream.
// The mutex is held for the entire send to prevent concurrent writes,
// which would corrupt the HTTP/2 frame buffer ("short write" errors).
// On send failure, the connection context is canceled to trigger
// immediate reconnection rather than waiting for the Hub's idle timeout.
func (c *Client) Send(msg *leapmuxv1.ConnectRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stream == nil {
		return fmt.Errorf("not connected")
	}
	err := c.stream.Send(msg)
	if err == nil {
		c.lastSendTime = time.Now()
	} else if c.connCancel != nil {
		slog.Warn("bidi stream send failed, canceling connection for reconnect", "error", err)
		c.connCancel()
	}
	return err
}

// Connect establishes the bidirectional streaming connection to the Hub.
func (c *Client) Connect(ctx context.Context, authToken string) error {
	// Create a connection-scoped context so that Send failures can
	// cancel the connection and trigger immediate reconnection.
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	stream := c.connector.Connect(connCtx)
	stream.RequestHeader().Set("Authorization", "Bearer "+authToken)

	c.mu.Lock()
	c.stream = stream
	c.connCancel = connCancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.stream = nil
		c.connCancel = nil
		c.mu.Unlock()

		// Close all channel sessions so watchers are unregistered and
		// the Frontend detects the disconnect. New channels will be
		// opened on reconnection.
		if c.channelMgr != nil {
			c.channelMgr.CloseAll()
		}
	}()

	// Send an initial heartbeat to trigger the Hub's bidi stream handler.
	// ConnectRPC with gRPC protocol only sends HTTP/2 headers on the first Send().
	if err := stream.Send(&leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{
				PublicKey:       c.PublicKey,
				MlkemPublicKey:  c.MlkemPublicKey,
				SlhdsaPublicKey: c.SlhdsaPublicKey,
				EncryptionMode:  c.EncryptionMode,
			},
		},
	}); err != nil {
		return fmt.Errorf("initial heartbeat: %w", err)
	}

	slog.Info("connected to hub", "url", c.hubURL)

	// Send workspace tab sync if a provider is configured.
	if c.TabSyncProvider != nil {
		if tabSync := c.TabSyncProvider(); tabSync != nil {
			if err := stream.Send(&leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_WorkspaceTabsSync{
					WorkspaceTabsSync: tabSync,
				},
			}); err != nil {
				slog.Warn("failed to send workspace tabs sync", "error", err)
			}
		}
	}

	// Start heartbeat goroutine (uses connCtx so it exits on reconnect).
	go c.heartbeatLoop(connCtx)

	// Main receive loop.
	for {
		msg, err := stream.Receive()
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}

		c.handleMessage(msg)
	}
}

func (c *Client) handleMessage(msg *leapmuxv1.ConnectResponse) {
	switch payload := msg.GetPayload().(type) {
	case *leapmuxv1.ConnectResponse_Heartbeat:
		// Ignore heartbeat responses.

	case *leapmuxv1.ConnectResponse_Deregister:
		c.handleDeregister(msg.GetRequestId(), payload.Deregister)

	case *leapmuxv1.ConnectResponse_HubShuttingDown:
		c.handleHubShuttingDown(payload.HubShuttingDown)

	case *leapmuxv1.ConnectResponse_ChannelOpen:
		c.handleChannelOpen(msg.GetRequestId(), payload.ChannelOpen)

	case *leapmuxv1.ConnectResponse_ChannelMessage:
		c.handleChannelMessage(payload.ChannelMessage)

	case *leapmuxv1.ConnectResponse_ChannelClose:
		c.handleChannelClose(payload.ChannelClose)

	default:
		slog.Warn("unhandled hub message", "request_id", msg.GetRequestId(), "payload_type", fmt.Sprintf("%T", msg.GetPayload()))
	}
}

// resolveWorkingDir resolves a working directory path:
//   - Expands leading "~" or "~/" to the user's home directory
//   - Resolves to an absolute path via filepath.Abs
//   - Cleans the path via filepath.Clean
//   - Validates the directory exists
func resolveWorkingDir(path string) (string, error) {
	// Expand tilde: only "~" alone or "~/..." are tilde prefixes.
	// NOT "/foo/~/bar", "~~", "~~/foo", etc.
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = home
	} else if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}

	// Resolve to absolute and clean.
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	resolved := filepath.Clean(abs)

	// Validate directory exists.
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat working directory %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", resolved)
	}

	return resolved, nil
}

const heartbeatIdleTimeout = 5 * time.Second

func (c *Client) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			idle := time.Since(c.lastSendTime)
			c.mu.Unlock()

			if idle >= heartbeatIdleTimeout {
				if err := c.Send(&leapmuxv1.ConnectRequest{
					Payload: &leapmuxv1.ConnectRequest_Heartbeat{
						Heartbeat: &leapmuxv1.Heartbeat{},
					},
				}); err != nil {
					slog.Warn("heartbeat send failed", "error", err)
					return
				}
			}
		}
	}
}

// connectFn is a function that establishes a connection to the Hub.
// Used for dependency injection in tests.
type connectFn func(ctx context.Context, authToken string) error

// ConnectWithReconnect wraps Connect with automatic reconnection using
// exponential backoff. Starts at 1s, doubles up to 60s, resets on
// successful connection lasting longer than resetThreshold.
func (c *Client) ConnectWithReconnect(ctx context.Context, authToken string) {
	c.connectWithReconnect(ctx, authToken, c.Connect, newDefaultBackoff(), resetThreshold)
}

func (c *Client) connectWithReconnect(ctx context.Context, authToken string, connect connectFn, bo backoff.BackOff, threshold time.Duration) {
	for {
		start := time.Now()
		err := connect(ctx, authToken)
		if ctx.Err() != nil {
			return
		}

		// If the Hub returns Unauthenticated, the worker has been deleted.
		// Don't retry — call OnDeregister and exit.
		if isCodeUnauthenticated(err) {
			slog.Warn("authentication rejected by hub, worker may be deleted", "error", err)
			if c.OnDeregister != nil {
				c.OnDeregister()
			}
			return
		}

		// If the Hub sent a shutdown notification with a retry delay,
		// honour it before reconnecting. Consume the value (swap to 0)
		// so it only applies once.
		if delay := c.hubRetryDelay.Swap(0); delay > 0 {
			slog.Info("hub requested reconnect delay", "delay_seconds", delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(delay) * time.Second):
			}
			bo.Reset()
			continue
		}

		// If connection lasted long enough, reset backoff.
		if time.Since(start) >= threshold {
			bo.Reset()
		}

		interval := bo.NextBackOff()
		slog.Warn("disconnected from hub, reconnecting...", "error", err, "backoff", interval)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// isCodeUnauthenticated checks if an error is a ConnectRPC Unauthenticated error.
func isCodeUnauthenticated(err error) bool {
	if err == nil {
		return false
	}
	if connectErr, ok := err.(*connect.Error); ok {
		return connectErr.Code() == connect.CodeUnauthenticated
	}
	// The error may be wrapped - check for the string pattern as fallback.
	return strings.Contains(err.Error(), "unauthenticated")
}
