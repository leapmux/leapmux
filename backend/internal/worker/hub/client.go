package hub

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/locallisten"
	"google.golang.org/protobuf/proto"
)

// Connect-stream queue bounds are sendq.Default* so Hub and worker stay on
// one definition. A wall-clock stall cutoff on a server-to-server connection
// invites a reconnect storm under sustained load, so there is none -- the
// byte budget alone disconnects a Hub that cannot keep up.

// Client manages the connection to the Hub.
type Client struct {
	connector  leapmuxv1connect.WorkerConnectorServiceClient
	reconciler leapmuxv1connect.WorkerReconcilerServiceClient
	hubURL     string
	authToken  string
	agents     *agent.Manager
	terminals  *terminal.Manager
	channelMgr *channel.Manager

	// OnDeregister is called when the Hub sends a deregistration notification.
	// The worker should clear its state and shut down gracefully.
	OnDeregister func()

	// OnReconcileNudge is called when the Hub asks this worker to run its
	// orphan reconciler now, because a CRDT batch tombstoned a tab this worker
	// hosts. Unsolicited, like the heartbeat.
	OnReconcileNudge func()

	// OnTabSyncResponse is called when the Hub replies to the connect-
	// time WorkerTabInventory with its orphan / reassignment
	// classification. Wired by the runner to trigger an immediate
	// orphan-reconciler pass so reconnects converge worker state with
	// the hub's CRDT-derived `workspace_tab_owned` view without
	// waiting for the periodic interval.
	OnTabSyncResponse func(*leapmuxv1.WorkerTabInventoryResponse)

	// OnWorkerIdentity is called with the owner the Hub delivers as the FIRST
	// message on every Connect stream, before it publishes the connection -- so it
	// fires before any ChannelOpen on the same stream can create a session, and
	// therefore before any handler requireWorkerOwner gates can be dispatched.
	//
	// The Hub is the only authority: it owns workers.registered_by. The worker keeps
	// no copy, so a reconnect re-establishes the owner rather than trusting a cache
	// that could have gone missing.
	OnWorkerIdentity func(registeredBy string)

	// PublicKey is the Worker's X25519 public key for E2EE channels.
	// Sent to the Hub with the initial heartbeat.
	PublicKey []byte
	// MlkemPublicKey is the Worker's ML-KEM-1024 public key.
	MlkemPublicKey []byte
	// SlhdsaPublicKey is the Worker's SLH-DSA-SHAKE-256f public key.
	SlhdsaPublicKey []byte
	// EncryptionMode is the Worker's encryption mode.
	EncryptionMode leapmuxv1.EncryptionMode

	// TabSyncProvider returns the current tab state for WorkerTabInventory
	// on connect. Set by the runner after initializing the worker service.
	TabSyncProvider func() *leapmuxv1.WorkerTabInventory

	mu           sync.Mutex
	stream       *connect.BidiStreamForClient[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse]
	writer       *sendq.Writer[*leapmuxv1.ConnectRequest]
	connCancel   context.CancelFunc // cancel function for current connection context
	lastSendTime time.Time          // last time a message was ENQUEUED (for idle heartbeat)
	stopOnce     sync.Once
	// identityReceived is set when the Hub delivers WorkerIdentity on the
	// current connection. A watchdog (see watchForIdentity) force-closes the
	// stream if it never arrives, so a proxy that strips the oneof, a partial
	// upgrade, or a Hub bug cannot wedge requireWorkerOwner for the
	// connection's life with no recovery short of a reconnect.
	identityReceived atomic.Bool

	// hubRetryDelay stores the retry delay (in seconds) requested by the Hub
	// when it sends a HubShuttingDownNotification. Consumed once by
	// connectWithReconnect after the connection drops.
	hubRetryDelay atomic.Int64
}

// New creates a new Hub client with integrated lifecycle management.
// It creates agent and terminal managers internally.
// hubURL may be:
//   - http[s]://host:port — a remote Hub reached over TCP
//   - unix:<socket-path>  — a local Hub reached over a Unix domain socket
//   - npipe:<pipe-name>   — a local Hub reached over a Windows named pipe
func New(hubURL string) *Client {
	httpClient, connectURL := clientForHubURL(hubURL)
	c := &Client{
		connector: leapmuxv1connect.NewWorkerConnectorServiceClient(
			httpClient,
			connectURL,
			connect.WithGRPC(),
		),
		reconciler: leapmuxv1connect.NewWorkerReconcilerServiceClient(
			httpClient,
			connectURL,
		),
		hubURL:    hubURL,
		terminals: terminal.NewManager(),
	}
	c.agents = agent.NewManager(func(agentID string, exitCode int, err error, _ bool) {
		if err != nil {
			slog.Info("agent exited with error", "agent_id", agentID, "exit_code", exitCode, "error", err)
		} else {
			slog.Info("agent exited", "agent_id", agentID, "exit_code", exitCode)
		}
	})
	return c
}

// clientForHubURL picks the HTTP client and ConnectRPC URL for hubURL.
// Local-IPC schemes (unix:/npipe:) get a dialer-backed h2c client and a
// placeholder "http://localhost" route (the transport dials the real
// endpoint); remote URLs pass through to a plain h2c client.
func clientForHubURL(hubURL string) (*http.Client, string) {
	return locallisten.SelectClient(
		hubURL,
		func() (*http.Client, string, error) { return locallisten.LocalH2CClient(hubURL, 0) },
		func() (*http.Client, string) {
			transport := &http2.Transport{
				AllowHTTP: true,
				DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, network, addr)
				},
			}
			return &http.Client{Transport: transport}, hubURL
		},
	)
}

// Stop gracefully stops all managers.
// Safe to call multiple times.
func (c *Client) Stop() {
	c.stopOnce.Do(func() {
		// Close all encrypted channels.
		if c.channelMgr != nil {
			c.channelMgr.CloseAll()
		}
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

// Send hands msg to the connection's writer. A nil return means QUEUED, not on
// the wire: the writer owns the only stream.Send, so no caller holds a lock
// across network I/O any more.
//
// It BLOCKS while the writer is over its byte budget, which is deliberate --
// handler-originated data (agent output, event broadcasts, tunnel downloads) has
// no application-level window of its own, so parking the producer is what makes
// the upstream source throttle itself. Callers on the shared receive goroutine
// must use TrySend or TrySendOrReset instead.
//
// This is channel.SendFunc, so a send with no live stream returns that
// interface's sentinel, channel.ErrTransportGone -- which is what lets the
// layers above tell an ordinary disconnect from a real send failure.
//
// A gone transport has TWO doors, and both map to the sentinel. The writer is
// nil once Connect's teardown defer has cleared it, but for the whole window
// before that it is still installed and merely CLOSED: sendq's run() closes it
// via defer the moment connCtx ends, and giveUp closes it on a write timeout or
// a blown budget. Handlers that snapshotted the writer a moment earlier then get
// sendq.ErrClosed, which is the same fact ("the connection is gone") wearing the
// transport's own name -- so it is translated here, at the only layer that knows
// what a closed writer means, rather than at each log site that has to tell an
// ordinary disconnect from a fault.
//
// ErrOverBudget is deliberately NOT translated: a frame too large to ever fit
// is a real fault on a live connection, and must keep reporting itself as one.
func (c *Client) Send(msg *leapmuxv1.ConnectRequest) error {
	w := c.currentWriter()
	if w == nil {
		return channel.ErrTransportGone
	}
	if err := w.EnqueueWait(context.Background(), msg); err != nil {
		if errors.Is(err, sendq.ErrClosed) {
			return fmt.Errorf("%w: %w", channel.ErrTransportGone, err)
		}
		return err
	}
	c.markEnqueued()
	return nil
}

// sendFailureLevel picks the level for a failed Send: Debug when the connection
// was on its way out, Warn for a genuine fault. Every reporter of a Send failure
// asks the channel package the same question, so a new send site cannot quietly
// reintroduce a per-disconnect alarm.
func sendFailureLevel(err error) slog.Level {
	if channel.IsTransportTeardown(err) {
		return slog.LevelDebug
	}
	return slog.LevelWarn
}

// ShutdownFlushTimeout bounds FlushSends. Generous relative to the work (a
// handful of small frames onto an open socket) because the cost of waiting too
// long is a slower exit, while the cost of not waiting long enough is a user
// staring at a terminal that silently stopped.
const ShutdownFlushTimeout = 5 * time.Second

// FlushSends blocks until everything already enqueued has been handed to the
// Connect stream, bounded by ShutdownFlushTimeout. A nil writer (never
// connected, or already torn down) is nothing to wait for and returns
// immediately.
//
// Send only ENQUEUES, so a caller whose next act is tearing the connection down
// -- graceful shutdown, which broadcasts the terminal "[Worker disconnected]"
// notice as its last step -- is racing the drain goroutine. Losing that race
// discards the frames silently: Writer.Close drops the queue, and the peer that
// needed them is a browser that will never get another chance to hear from this
// process.
//
// The bound lives here rather than in a parameter because there is one policy
// and two entry points that would otherwise each restate it.
func (c *Client) FlushSends() error {
	w := c.currentWriter()
	if w == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownFlushTimeout)
	defer cancel()
	return w.Flush(ctx)
}

// TrySend enqueues msg if the budget allows and reports whether it did. For
// best-effort sends issued from the Connect RECEIVE goroutine, which serves
// every channel on this worker and must never block. A false return does NOT
// tear the connection down -- use TrySendOrReset for must-deliver control
// frames (open responses, access acks, deregister acks, teardown CLOSE).
func (c *Client) TrySend(msg *leapmuxv1.ConnectRequest) bool {
	w := c.currentWriter()
	if w == nil {
		return false
	}
	if !w.TryEnqueue(msg) {
		return false
	}
	c.markEnqueued()
	return true
}

// TrySendOrReset enqueues a must-deliver control frame on the receive
// goroutine via the control-reserve budget (TryEnqueueControl). A drop
// still cancels the connection so reconnect recovers the handshake -- that
// path is reached only when even the reserved headroom is exhausted.
func (c *Client) TrySendOrReset(msg *leapmuxv1.ConnectRequest) bool {
	w := c.currentWriter()
	if w == nil {
		c.cancelConn()
		return false
	}
	if !w.TryEnqueueControl(msg) {
		// The only place a saturated control reserve turns into a reconnect
		// without any other trace. Logged because the disconnect it causes
		// would otherwise look spontaneous from the outside.
		slog.Warn("hub connect stream control queue saturated; resetting connection")
		c.cancelConn()
		return false
	}
	c.markEnqueued()
	return true
}

func (c *Client) currentWriter() *sendq.Writer[*leapmuxv1.ConnectRequest] {
	c.mu.Lock()
	w := c.writer
	c.mu.Unlock()
	return w
}

func (c *Client) markEnqueued() {
	c.mu.Lock()
	c.lastSendTime = time.Now()
	c.mu.Unlock()
}

// ListOwnedTabsForWorker calls the hub's WorkerReconcilerService.
// Authenticated by the worker's auth token (last seen on Connect).
// Returns nil + error if Connect hasn't been called yet.
//
// The whole response is returned, not just its tabs: the list is scoped to one
// owner (the worker's registrant) and the caller's orphan reap is only valid
// for that owner, so OwnerUserId has to travel with the rows it qualifies.
func (c *Client) ListOwnedTabsForWorker(ctx context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
	c.mu.Lock()
	token := c.authToken
	c.mu.Unlock()
	if token == "" {
		return nil, errors.New("hub client: no auth token (call Connect first)")
	}
	req := connect.NewRequest(&leapmuxv1.ListOwnedTabsForWorkerRequest{})
	req.Header().Set("Authorization", "Bearer "+token)
	resp, err := c.reconciler.ListOwnedTabsForWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Connect establishes the bidirectional streaming connection to the Hub.
func (c *Client) Connect(ctx context.Context, authToken string) error {
	c.mu.Lock()
	c.authToken = authToken
	c.mu.Unlock()

	// Create a connection-scoped context so that write failures can
	// cancel the connection and trigger immediate reconnection.
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	stream := c.connector.Connect(connCtx)
	stream.RequestHeader().Set("Authorization", "Bearer "+authToken)

	// Send an initial heartbeat INLINE on the stream before the writer exists.
	// ConnectRPC with gRPC protocol only sends HTTP/2 headers on the first
	// Send(), and nothing else may enqueue until the writer is published.
	//
	// When the Hub refuses before that write finishes (fenced registry, worker
	// cap, …), Send wraps io.EOF and Receive carries the real status — see
	// connect.BidiStreamForClient.Send. Surface that status so reconnect logic
	// sees Unavailable/ResourceExhausted instead of a bare Unknown/EOF.
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
		if errors.Is(err, io.EOF) {
			if _, recvErr := stream.Receive(); recvErr != nil {
				return fmt.Errorf("initial heartbeat: %w", recvErr)
			}
		}
		return fmt.Errorf("initial heartbeat: %w", err)
	}

	writer := sendq.New(connCtx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write: func(_ context.Context, m *leapmuxv1.ConnectRequest) error {
			return stream.Send(m)
		},
		Size: func(m *leapmuxv1.ConnectRequest) int {
			return proto.Size(m)
		},
		MaxBytes:       sendq.DefaultMaxBytes,
		ControlReserve: sendq.DefaultControlReserve,
		FrameOverhead:  sendq.DefaultFrameOverhead,
		WriteTimeout:   sendq.DefaultWriteTimeout,
		OnGiveUp: func(reason sendq.GiveUpReason, err error) {
			// Counted like every other sendq writer. The worker serves no
			// /metrics of its own, but solo runs it in the Hub's process
			// against the Hub's registry, so this is scrapeable there -- and
			// an uncounted give-up is indistinguishable from one that never
			// happened, which is the wrong answer to "why did that reconnect?"
			metrics.CountSendqGiveUp(metrics.PoolWorkerClient, reason.Label())
			slog.Warn("hub connect stream writer gave up; reconnecting",
				"reason", reason.Label(), "error", err)
			connCancel()
		},
		OnDiscard: func(frames int, bytes int64) {
			slog.Debug("hub connect stream discarded queued frames",
				"frames", frames, "bytes", bytes)
		},
	})

	c.mu.Lock()
	c.stream = stream
	c.writer = writer
	c.connCancel = connCancel
	c.lastSendTime = time.Now()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		w := c.writer
		c.stream = nil
		c.writer = nil
		c.connCancel = nil
		c.mu.Unlock()
		if w != nil {
			w.Close()
		}

		// Close all channel sessions so watchers are unregistered and
		// the Frontend detects the disconnect. New channels will be
		// opened on reconnection.
		if c.channelMgr != nil {
			c.channelMgr.CloseAll()
		}
	}()

	go c.closeStreamOnCancel(connCtx, stream)

	slog.Info("connected to hub", "url", c.hubURL)

	// Reset identity tracking for this connection and arm the watchdog that
	// force-closes the stream if the Hub never delivers WorkerIdentity. The
	// Hub sends it before publishing the connection (worker_connector_service.go),
	// so its absence within the budget signals a stripped/dropped greeting.
	c.identityReceived.Store(false)
	go c.watchForIdentity(connCtx)

	// Send workspace tab sync if a provider is configured.
	if c.TabSyncProvider != nil {
		if tabSync := c.TabSyncProvider(); tabSync != nil {
			if err := c.Send(&leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{
					WorkerTabInventory: tabSync,
				},
			}); err != nil {
				slog.Log(context.Background(), sendFailureLevel(err), "failed to send workspace tabs sync", "error", err)
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

	case *leapmuxv1.ConnectResponse_ReconcileNudge:
		if c.OnReconcileNudge != nil {
			c.OnReconcileNudge()
		}

	case *leapmuxv1.ConnectResponse_WorkerTabInventoryResp:
		if c.OnTabSyncResponse != nil {
			c.OnTabSyncResponse(payload.WorkerTabInventoryResp)
		}

	case *leapmuxv1.ConnectResponse_WorkerIdentity:
		c.identityReceived.Store(true)
		if c.OnWorkerIdentity != nil {
			c.OnWorkerIdentity(payload.WorkerIdentity.GetRegisteredBy())
		}

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
		return "", fmt.Errorf(`stat working directory "%s": %w`, resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf(`working directory "%s" is not a directory`, resolved)
	}

	return resolved, nil
}

const heartbeatIdleTimeout = 5 * time.Second

// workerIdentityTimeout bounds how long the worker waits for the Hub's
// connect-time WorkerIdentity greeting before force-closing the stream. The
// Hub sends it before publishing the connection, so its absence within this
// budget signals a stripped/dropped greeting -- which would otherwise leave
// requireWorkerOwner denying every machine-scoped RPC (file, git, sysinfo,
// tunnel) for the connection's life, indistinguishable from a genuine
// cross-tenant refusal. Force-closing triggers ConnectWithReconnect's
// backoff, which re-runs the greeting on a fresh stream. A var so tests can
// shorten it.
var workerIdentityTimeout = 10 * time.Second

// closeStreamOnCancel ends the bidi stream when ctx fires, because cancelling
// it is NOT enough on its own. connect-go checks the context before each read,
// so a Receive already parked inside the HTTP/2 response body stays parked
// until a frame arrives -- and a Hub that is shutting down sends none. The
// Hub's Connect handler then sees nothing until its own 10s idle timeout, and
// because an unencrypted-HTTP/2 connection counts as ACTIVE for its whole life
// (net/http marks it so in maybeServeUnencryptedHTTP2), that handler holds
// http.Server.Shutdown's drain open for the same 10s.
//
// Closing turns every cancellation -- reconnect, identity watchdog, worker
// shutdown -- into an immediate stream end the Hub observes at once: END_STREAM
// when the writer is idle, a reset or a truncated final frame when a send was
// in flight. Both surface to the Hub as a receive error, which is what releases
// its handler. Truncation is unavoidable here rather than sloppy: flushing the
// writer first would re-park on the very stall being escaped.
//
// Connect's `defer connCancel()` guarantees this goroutine exits with the
// connection. Both Close errors are structurally nil (a pipe close and a
// response-body close), which is why connect-go's own client discards them the
// same way.
func (c *Client) closeStreamOnCancel(
	ctx context.Context,
	stream *connect.BidiStreamForClient[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse],
) {
	<-ctx.Done()
	_ = stream.CloseRequest()
	_ = stream.CloseResponse()
}

// watchForIdentity force-closes the connection if the Hub does not deliver
// WorkerIdentity within workerIdentityTimeout. See Client.identityReceived.
func (c *Client) watchForIdentity(ctx context.Context) {
	timer := time.NewTimer(workerIdentityTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		if c.identityReceived.Load() {
			return
		}
		slog.Warn("hub did not deliver worker identity within timeout; forcing reconnect",
			"timeout", workerIdentityTimeout)
		c.cancelConn()
	}
}

// cancelConn cancels the current connection's context if one is active. Safe
// to call from any goroutine; a no-op when no connection is active.
func (c *Client) cancelConn() {
	c.mu.Lock()
	cancel := c.connCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

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
						Heartbeat: &leapmuxv1.Heartbeat{
							EncryptionMode: c.EncryptionMode,
						},
					},
				}); err != nil {
					// Debug when the connection was simply going away: the loop races
					// its own teardown (select gives no ordering when both ctx.Done
					// and the tick are ready), so one more heartbeat after the writer
					// closed is the ORDINARY end of a disconnect, not a fault.
					slog.Log(context.Background(), sendFailureLevel(err), "heartbeat send failed", "error", err)
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

func (c *Client) connectWithReconnect(ctx context.Context, authToken string, connect connectFn, bo backoff, threshold time.Duration) {
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

		interval := bo.Next()
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
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr.Code() == connect.CodeUnauthenticated
	}
	return false
}
