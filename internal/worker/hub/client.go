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
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"golang.org/x/net/http2"
)

// terminalMeta holds metadata about a running terminal.
type terminalMeta struct {
	workspaceID string
	cols        uint32
	rows        uint32
}

// Client manages the connection to the Hub.
type Client struct {
	connector leapmuxv1connect.WorkerConnectorServiceClient
	hubURL    string
	dataDir   string
	agents    *agent.Manager
	terminals *terminal.Manager

	// OnDeregister is called when the Hub sends a deregistration notification.
	// The worker should clear its state and shut down gracefully.
	OnDeregister func()

	mu                 sync.Mutex
	stream             *connect.BidiStreamForClient[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse]
	agentWorkspaces    map[string]string                 // agentID -> workspaceID
	terminalWorkspaces map[string]terminalMeta           // terminalID -> meta
	savedTerminals     map[string]terminal.SavedTerminal // terminalID -> saved state from previous run
	lastSendTime       time.Time                         // last time a message was sent (for idle heartbeat)
	stopOnce           sync.Once

	// hubRetryDelay stores the retry delay (in seconds) requested by the Hub
	// when it sends a HubShuttingDownNotification. Consumed once by
	// connectWithReconnect after the connection drops.
	hubRetryDelay atomic.Int64
}

// New creates a new Hub client with integrated lifecycle management.
// It creates agent and terminal managers internally and loads saved terminals from dataDir.
func New(hubURL, dataDir string) *Client {
	return NewWithHTTPClient(newH2CClient(), hubURL, dataDir)
}

// NewWithHTTPClient creates a new Hub client that uses the provided HTTP client
// for ConnectRPC communication. This allows callers to provide a custom transport
// (e.g. one that dials via Unix domain socket).
func NewWithHTTPClient(httpClient *http.Client, hubURL, dataDir string) *Client {
	terminals := terminal.NewManager()

	c := &Client{
		connector: leapmuxv1connect.NewWorkerConnectorServiceClient(
			httpClient,
			hubURL,
			connect.WithGRPC(),
		),
		hubURL:             hubURL,
		dataDir:            dataDir,
		terminals:          terminals,
		agentWorkspaces:    make(map[string]string),
		terminalWorkspaces: make(map[string]terminalMeta),
	}

	c.agents = agent.NewManager(func(agentID string, exitCode int, err error) {
		c.mu.Lock()
		workspaceID := c.agentWorkspaces[agentID]
		delete(c.agentWorkspaces, agentID)
		c.mu.Unlock()

		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		_ = c.Send(&leapmuxv1.ConnectRequest{
			Payload: &leapmuxv1.ConnectRequest_AgentStopped{
				AgentStopped: &leapmuxv1.AgentStopped{
					AgentId:     agentID,
					WorkspaceId: workspaceID,
					ExitCode:    int32(exitCode),
					Error:       errMsg,
				},
			},
		})
	})

	// Load saved terminals from previous run.
	savedTerminals, err := terminal.LoadSavedTerminals(dataDir)
	if err != nil {
		slog.Warn("failed to load saved terminals", "error", err)
	}
	if savedTerminals != nil {
		c.SetSavedTerminals(savedTerminals)
	}

	return c
}

// Stop gracefully stops all managers and persists terminal state.
// Safe to call multiple times.
func (c *Client) Stop() {
	c.stopOnce.Do(func() {
		// Persist terminal screen buffers before stopping.
		if err := c.terminals.SaveScreens(c.dataDir, c.GetTerminalMeta); err != nil {
			slog.Error("failed to save terminal screens", "error", err)
		}
		if err := c.terminals.SaveTerminalMeta(c.dataDir, c.GetTerminalMeta); err != nil {
			slog.Error("failed to save terminal meta", "error", err)
		}
		c.terminals.StopAll()
		c.agents.StopAll()
	})
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

// SetSavedTerminals sets saved terminal state from a previous worker run.
func (c *Client) SetSavedTerminals(saved map[string]terminal.SavedTerminal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.savedTerminals = saved
	// Also populate terminalWorkspaces so handleTerminalList can find them.
	for id, st := range saved {
		c.terminalWorkspaces[id] = terminalMeta{
			workspaceID: st.WorkspaceID,
			cols:        st.Cols,
			rows:        st.Rows,
		}
	}
}

// GetTerminalMeta returns the workspace ID, cols, and rows for a terminal.
func (c *Client) GetTerminalMeta(terminalID string) (string, uint32, uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	meta, ok := c.terminalWorkspaces[terminalID]
	if !ok {
		return "", 0, 0
	}
	return meta.workspaceID, meta.cols, meta.rows
}

// Send sends a message to the Hub via the bidi stream.
// The mutex is held for the entire send to prevent concurrent writes,
// which would corrupt the HTTP/2 frame buffer ("short write" errors).
func (c *Client) Send(msg *leapmuxv1.ConnectRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stream == nil {
		return fmt.Errorf("not connected")
	}
	err := c.stream.Send(msg)
	if err == nil {
		c.lastSendTime = time.Now()
	}
	return err
}

// Connect establishes the bidirectional streaming connection to the Hub.
func (c *Client) Connect(ctx context.Context, authToken string) error {
	stream := c.connector.Connect(ctx)
	stream.RequestHeader().Set("Authorization", "Bearer "+authToken)

	c.mu.Lock()
	c.stream = stream
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.stream = nil
		c.mu.Unlock()
	}()

	// Send an initial heartbeat to trigger the Hub's bidi stream handler.
	// ConnectRPC with gRPC protocol only sends HTTP/2 headers on the first Send().
	if err := stream.Send(&leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{},
		},
	}); err != nil {
		return fmt.Errorf("initial heartbeat: %w", err)
	}

	slog.Info("connected to hub", "url", c.hubURL)

	// Start heartbeat goroutine.
	go c.heartbeatLoop(ctx)

	// Main receive loop.
	for {
		msg, err := stream.Receive()
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}

		c.handleMessage(ctx, msg)
	}
}

func (c *Client) handleMessage(ctx context.Context, msg *leapmuxv1.ConnectResponse) {
	switch payload := msg.GetPayload().(type) {
	case *leapmuxv1.ConnectResponse_Heartbeat:
		// Ignore heartbeat responses.

	case *leapmuxv1.ConnectResponse_AgentStart:
		go c.handleAgentStart(ctx, msg.GetRequestId(), payload.AgentStart)

	case *leapmuxv1.ConnectResponse_AgentInput:
		c.handleAgentInput(msg.GetRequestId(), payload.AgentInput)

	case *leapmuxv1.ConnectResponse_AgentRawInput:
		c.handleAgentRawInput(msg.GetRequestId(), payload.AgentRawInput)

	case *leapmuxv1.ConnectResponse_AgentStop:
		c.handleAgentStop(payload.AgentStop)

	case *leapmuxv1.ConnectResponse_TerminalStart:
		c.handleTerminalStart(msg.GetRequestId(), payload.TerminalStart)

	case *leapmuxv1.ConnectResponse_TerminalInput:
		c.handleTerminalInput(payload.TerminalInput)

	case *leapmuxv1.ConnectResponse_TerminalResize:
		c.handleTerminalResize(payload.TerminalResize)

	case *leapmuxv1.ConnectResponse_TerminalStop:
		c.handleTerminalStop(payload.TerminalStop)

	case *leapmuxv1.ConnectResponse_TerminalList:
		c.handleTerminalList(msg.GetRequestId(), payload.TerminalList)

	case *leapmuxv1.ConnectResponse_ShellList:
		c.handleShellList(msg.GetRequestId())

	case *leapmuxv1.ConnectResponse_FileBrowse:
		c.handleFileBrowse(msg.GetRequestId(), payload.FileBrowse)

	case *leapmuxv1.ConnectResponse_FileRead:
		c.handleFileRead(msg.GetRequestId(), payload.FileRead)

	case *leapmuxv1.ConnectResponse_FileStat:
		c.handleFileStat(msg.GetRequestId(), payload.FileStat)

	case *leapmuxv1.ConnectResponse_GitInfo:
		c.handleGitInfo(msg.GetRequestId(), payload.GitInfo)

	case *leapmuxv1.ConnectResponse_GitWorktreeCreate:
		c.handleGitWorktreeCreate(msg.GetRequestId(), payload.GitWorktreeCreate)

	case *leapmuxv1.ConnectResponse_GitWorktreeRemove:
		c.handleGitWorktreeRemove(msg.GetRequestId(), payload.GitWorktreeRemove)

	case *leapmuxv1.ConnectResponse_Deregister:
		c.handleDeregister(msg.GetRequestId(), payload.Deregister)

	case *leapmuxv1.ConnectResponse_TerminateWorkspaces:
		c.handleTerminateWorkspaces(msg.GetRequestId(), payload.TerminateWorkspaces)

	case *leapmuxv1.ConnectResponse_HubShuttingDown:
		c.handleHubShuttingDown(payload.HubShuttingDown)

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
