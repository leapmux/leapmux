package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/internal/wskeepalive"
	"github.com/leapmux/leapmux/util/errwrap"
	"google.golang.org/protobuf/proto"
)

// Connect-stream queue limits are sendq.Default* so Hub and worker stay on
// one definition. A wall-clock stall cutoff on a server-to-server connection
// invites a reconnect storm under sustained load, so there is none -- the
// byte budget alone disconnects a Hub that cannot keep up.

// Client manages the connection to the Hub.
type Client struct {
	connector  leapmuxv1connect.WorkerConnectorServiceClient
	dialStream func(context.Context, string) (hubStream, error)
	endpoint   *hubtransport.Endpoint
	authToken  string
	terminals  *terminal.Manager
	channelMgr *channel.Manager

	// OnDeregister is called when the Hub tells this worker to stop for good: a
	// deregistration notification on the stream (the Hub deletes the worker once
	// it acknowledges), or a WebSocket close with worker_deleted. The worker
	// should clear its state and shut down gracefully.
	OnDeregister func()

	// OnConnectionState reports the Hub connection's transitions: true
	// with no error when a stream was admitted (the Hub accepted this
	// worker's identity), false with the cause when the stream ended or
	// a connection attempt failed. Wired by the launcher-facing runtime
	// so "registered and ready" stays distinguishable from "registered
	// and connected".
	OnConnectionState func(connected bool, err error)

	// OnReconcileNudge is called when the Hub asks this worker to run its
	// orphan reconciler now, because a CRDT batch tombstoned a tab this worker
	// hosts. The notification is unsolicited.
	OnReconcileNudge func()

	// OnTabSyncResponse is called when the Hub replies to the connect-time
	// WorkerTabInventory. The reply is deliberately empty: it only signals that
	// the Hub finished its side. Wired by bootstrap.Wire to trigger an immediate
	// orphan-reconciler pass so reconnects converge worker state with the Hub's
	// CRDT-derived `workspace_tab_owned` view without waiting for the periodic
	// interval.
	OnTabSyncResponse func(*leapmuxv1.WorkerTabInventoryResponse)

	// OnWorkerIdentity is called with the owner in the first frame on each
	// worker stream. The Hub enqueues that greeting before publishing the
	// connection. This callback runs before any ChannelOpen on the stream can
	// create a session, and before any handler that requires the owner can run.
	// The Hub sends the message again when the owner's account changes, so the
	// callback can run more than once per connection.
	//
	// The Hub is the only authority: it owns workers.registered_by. The worker keeps
	// no copy, so a reconnect re-establishes the owner rather than trusting a cache
	// that could have gone missing.
	//
	// It reports whether the worker holds an owner after the delivery. A delivery
	// that leaves the worker with no owner, such as an empty owner on a worker that
	// holds none, does not stop the identity watchdog (see identityReceived),
	// because the owner gate then has nothing to admit. A worker that holds a
	// seeded owner, or an owner from an earlier delivery, keeps it through such a
	// delivery, so the watchdog stops.
	OnWorkerIdentity func(identity *leapmuxv1.WorkerIdentity) (holdsOwner bool)

	// PublicKeys is the Worker's static public key set, sent in the WorkerHello
	// that opens every worker stream. The Hub pinned it at registration and
	// refuses a hello that presents any other key.
	PublicKeys *leapmuxv1.WorkerPublicKeys
	// EncryptionMode is the Worker's encryption mode.
	EncryptionMode leapmuxv1.EncryptionMode

	// TabSyncProvider returns the current tab state for WorkerTabInventory
	// on connect. Set by bootstrap.Wire after it builds the worker service.
	TabSyncProvider func() *leapmuxv1.WorkerTabInventory

	mu         sync.Mutex
	writer     *sendq.Writer[*leapmuxv1.ConnectRequest]
	connCancel context.CancelCauseFunc // Ends the current connection with its cause.
	stopOnce   sync.Once
	// identityReceived is set when the Hub delivers a WorkerIdentity on the
	// current connection and OnWorkerIdentity reports that the worker holds an
	// owner. A watchdog (see watchForIdentity) force-closes the stream if none
	// arrives, so a proxy that strips the oneof, a partial upgrade, or a Hub bug
	// that sends an empty owner cannot wedge the owner gate for the connection's
	// life with no recovery short of a reconnect. With no OnWorkerIdentity,
	// nothing consumes the owner, so any delivery sets it.
	identityReceived atomic.Bool

	// hubRetryDelay stores the retry delay (in seconds) requested by the Hub
	// when it sends a HubShuttingDownNotification. Consumed once by
	// connectWithReconnect after the connection drops.
	hubRetryDelay atomic.Int64

	// clock supplies every instant, timer and ticker this client reads, so a
	// test drives a whole reconnect sequence with no sleep and no window to
	// race. New installs the real clock; WithClock replaces it.
	clock quartz.Clock
}

const (
	hubReconnectTimeTag       = "hub-reconnect-time"
	hubReconnectTimerTag      = "hub-reconnect"
	hubRequestedRetryTimerTag = "hub-requested-retry"
	hubIdentityTimerTag       = "hub-identity-timeout"
	hubSilenceTimerTag        = "hub-silence"
)

// ClientOption changes one New setting.
type ClientOption func(*Client)

// WithClock sets the clock that supplies client times, timers, and tickers.
func WithClock(clock quartz.Clock) ClientOption {
	if clock == nil {
		panic("hub: clock must not be nil")
	}
	return func(c *Client) {
		c.clock = clock
	}
}

// New creates a new Hub client with integrated lifecycle management.
// It creates agent and terminal managers internally. Build endpoint with
// hubtransport.New, which accepts an http(s) URL, a `unix:` socket path or a
// `npipe:` pipe name.
//
// newConnectorClient states the lane and the protocol that the connector
// client takes, and why.
func New(endpoint *hubtransport.Endpoint, opts ...ClientOption) *Client {
	c := &Client{
		connector: newConnectorClient(endpoint),
		endpoint:  endpoint,
		clock:     quartz.NewReal(),
	}
	c.dialStream = func(ctx context.Context, token string) (hubStream, error) {
		return DialWorkerStream(ctx, c.endpoint.WebSocketClient(), c.endpoint.BaseURL(), token)
	}
	for _, opt := range opts {
		opt(c)
	}
	// After the options, because the terminal Manager takes the clock this
	// client settled on. A test that gives the Client a mock clock therefore
	// drives the reader-drain grace inside its terminals too, and MUST advance
	// that clock, or a reader that never ends parks the exit goroutine for
	// ever. bootstrap.Wire reads the same clock back through Clock() for the
	// Service, so those three share one. The registration retry holds its own,
	// because Register runs before any Client exists.
	c.terminals = terminal.NewManager(terminal.WithClock(c.clock))
	return c
}

// Endpoint returns the transport this client was built from.
//
// Everything else in the process that talks to the same Hub takes it from
// here rather than building its own: the delegation mint, the cross-worker
// channels and the hub bridges a spawned agent uses all share this Client's
// connection pools and its h2c verdict. It is also the ONLY way they can be
// pointed at the same address, since a second Endpoint built from the same URL
// would probe again and pool separately.
func (c *Client) Endpoint() *hubtransport.Endpoint { return c.endpoint }

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

// Clock returns the clock this client and its terminal manager share, so
// anything wired behind the client runs on the same one.
func (c *Client) Clock() quartz.Clock { return c.clock }

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

// ShutdownFlushTimeout is the limit that the worker shutdown gives FlushSends.
// It is generous relative to the work (a handful of small frames onto an open
// socket) because the cost of waiting too long is a slower exit, while the
// cost of not waiting long enough is a user staring at a terminal that
// silently stopped.
const ShutdownFlushTimeout = 5 * time.Second

// FlushSends blocks until everything already enqueued has been handed to the
// worker stream, with limit as its limit. A nil writer (never connected, or
// already torn down) is nothing to wait for and returns immediately.
//
// Send only ENQUEUES, so a caller whose next act is tearing the connection down
// -- graceful shutdown, which broadcasts the terminal "[Worker disconnected]"
// notice as its last step -- is racing the drain goroutine. Losing that race
// discards the frames silently: Writer.Close drops the queue, and the peer that
// needed them is a browser that will never get another chance to hear from this
// process.
//
// The limit is a parameter, although the policy is ShutdownFlushTimeout,
// because the shutdown caps it by the time left before its deadline (see
// lifecycle.ShutdownDrainTimeout).
func (c *Client) FlushSends(limit time.Duration) error {
	w := c.currentWriter()
	if w == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	return w.Flush(ctx)
}

// TrySend enqueues msg if the budget allows and reports whether it did. For
// best-effort sends issued from the worker stream RECEIVE goroutine, which serves
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
	return true
}

// TrySendOrReset enqueues a must-deliver control frame on the receive
// goroutine via the control-reserve budget (TryEnqueueControl), and reports
// whether it did. It is trySendControl for a caller that needs only that
// answer, such as channel.TrySendFunc.
func (c *Client) TrySendOrReset(msg *leapmuxv1.ConnectRequest) bool {
	return c.trySendControl(msg) == controlQueued
}

// controlSendOutcome is what trySendControl did with a must-deliver control
// frame.
type controlSendOutcome int

const (
	// controlQueued: the frame is queued.
	controlQueued controlSendOutcome = iota
	// controlConnectionEnding: the frame is dropped because the connection is
	// already ending. Its writer is gone or closed, and nothing is cancelled:
	// the receive loop reports why the connection ended.
	controlConnectionEnding
	// controlSaturated: the frame is dropped because even the control reserve
	// is exhausted, and the connection is cancelled, so the reconnect recovers
	// the handshake.
	controlSaturated
)

// trySendControl enqueues a must-deliver control frame on the receive
// goroutine via the control-reserve budget (TryEnqueueControl). A frame that
// the reserve cannot hold cancels the connection so reconnect recovers the
// handshake -- that path is reached only when even the reserved headroom is
// exhausted.
//
// A writer that is gone or closed is not saturated: the connection is ending
// already. Its context ended, or the writer gave up. A writer gives up WITHOUT
// cancelling when the Hub ended the stream (see writerGiveUpCancelsConnection),
// so the receive loop can still report the Hub's status, and a cancel here
// would replace that status with Canceled. So the frame is dropped quietly, and
// no cancel follows. A gone writer has no connection left to cancel: Connect
// clears the writer and the cancel function together.
func (c *Client) trySendControl(msg *leapmuxv1.ConnectRequest) controlSendOutcome {
	w := c.currentWriter()
	if w == nil {
		slog.Debug("dropped a hub control frame: the connection is gone")
		return controlConnectionEnding
	}
	if w.TryEnqueueControl(msg) {
		return controlQueued
	}
	if w.IsClosed() {
		slog.Debug("dropped a hub control frame: the connection is ending")
		return controlConnectionEnding
	}
	// The only place a saturated control reserve turns into a reconnect
	// without any other trace. Logged because the disconnect it causes
	// would otherwise look spontaneous from the outside.
	slog.Warn("hub connect stream control queue saturated; resetting connection")
	c.cancelConn(errControlQueueSaturated)
	return controlSaturated
}

func (c *Client) currentWriter() *sendq.Writer[*leapmuxv1.ConnectRequest] {
	c.mu.Lock()
	w := c.writer
	c.mu.Unlock()
	return w
}

// ListOwnedTabsForWorker calls the hub's WorkerConnectorService.ListOwnedTabsForWorker.
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
	req := &leapmuxv1.ListOwnedTabsForWorkerRequest{}
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+token)
	resp, err := c.connector.ListOwnedTabsForWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Connect establishes the bidirectional streaming connection to the Hub.
func (c *Client) Connect(ctx context.Context, authToken string) error {
	c.mu.Lock()
	c.authToken = authToken
	c.mu.Unlock()

	connCtx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(nil)
	stream, err := c.dialStream(connCtx, authToken)
	if err != nil {
		return err
	}
	defer func() { _ = stream.CloseNow() }()
	// A stop uses the handshake so FlushSends' final frames reach the Hub.
	// Reads and writes use a background context because coder/websocket closes
	// its socket as soon as an operation's context ends. This callback owns
	// teardown instead, including interruption of a blocked write.
	stop := context.AfterFunc(connCtx, func() {
		if ctx.Err() != nil {
			_ = stream.Close()
		} else {
			_ = stream.CloseNow()
		}
	})
	defer stop()

	// The hello is written before the queue exists, so it is always first.
	if err := stream.Send(context.Background(), &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Hello{Hello: &leapmuxv1.WorkerHello{
			Keys: c.PublicKeys, EncryptionMode: c.EncryptionMode,
		}},
	}); err != nil {
		// A failed hello can race a refusal already sent by the Hub. Read its
		// close frame before deciding what the reconnect policy should do.
		for {
			if _, readErr := stream.Receive(context.Background()); readErr != nil {
				return fmt.Errorf("hello: %w", receiveEndError(readErr, ctx, connCtx))
			}
		}
	}
	silenceTimer := c.clock.NewTimer(hubSilenceTimeout, hubSilenceTimerTag)
	go c.watchHubSilence(connCtx, silenceTimer, stream.PingReceived(), cancelCause)

	writer := sendq.New(connCtx, sendq.Config[*leapmuxv1.ConnectRequest]{
		Write: func(_ context.Context, m *leapmuxv1.ConnectRequest) error {
			return stream.Send(context.Background(), m)
		},
		Size: func(m *leapmuxv1.ConnectRequest) int {
			return proto.Size(m)
		},
		MaxBytes:       sendq.DefaultMaxBytes,
		ControlReserve: sendq.DefaultControlReserve,
		FrameOverhead:  sendq.DefaultFrameOverhead,
		WriteTimeout:   sendq.DefaultWriteTimeout,
		OnGiveUp:       c.connectWriterGiveUpHook(cancelCause),
		OnDiscard: func(frames int, bytes int64) {
			slog.Debug("hub connect stream discarded queued frames",
				"frames", frames, "bytes", bytes)
		},
	})

	c.mu.Lock()
	c.writer = writer
	c.connCancel = cancelCause
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		w := c.writer
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

	slog.Info("connected to hub", "url", c.endpoint.URL())

	// Reset identity tracking for this connection and arm the watchdog that
	// force-closes the stream if the Hub never delivers a WorkerIdentity that
	// leaves the worker with an owner. The Hub enqueues it before publishing the
	// connection (worker_connector_service.go), so its absence within the budget
	// signals a stripped, dropped or empty greeting.
	c.identityReceived.Store(false)
	go c.watchForIdentity(connCtx, cancelCause)

	// The read limit applies to the entire request, including its envelope.
	if c.TabSyncProvider != nil {
		if inventory := c.TabSyncProvider(); inventory != nil {
			req := &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: inventory}}
			size := proto.Size(req)
			if size > channelwire.WorkerStreamMaxMessageBytes {
				slog.Error("skipping an oversize worker tab inventory; the periodic reconciler still converges",
					"tabs", len(inventory.GetTabs()), "size", size, "limit", channelwire.WorkerStreamMaxMessageBytes)
			} else if err := c.Send(req); err != nil {
				slog.Log(context.Background(), sendFailureLevel(err), "failed to send workspace tabs sync", "error", err)
			}
		}
	}

	// Main receive loop.
	for {
		msg, err := stream.Receive(context.Background())
		if err != nil {
			return fmt.Errorf("receive: %w", receiveEndError(err, ctx, connCtx))
		}

		c.handleMessage(msg)
	}
}

func (c *Client) handleMessage(msg *leapmuxv1.ConnectResponse) {
	switch payload := msg.GetPayload().(type) {
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
		if c.OnWorkerIdentity == nil || c.OnWorkerIdentity(payload.WorkerIdentity) {
			c.identityReceived.Store(true)
			// The accepted identity is the connection transition: the
			// Hub admitted this worker's hello, keys and all.
			if c.OnConnectionState != nil {
				c.OnConnectionState(true, nil)
			}
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

// workerIdentityTimeout limits how long the worker waits for the Hub's
// connect-time WorkerIdentity greeting before force-closing the stream. The
// Hub enqueues it before publishing the connection, so its absence within this
// budget signals a stripped/dropped greeting -- which would otherwise leave
// the owner gate denying every machine-scoped RPC (file, git, sysinfo,
// tunnel) for the connection's life, indistinguishable from a genuine
// cross-tenant refusal. Force-closing triggers ConnectWithReconnect's
// backoff, which re-runs the greeting on a fresh stream.
const workerIdentityTimeout = 10 * time.Second

// watchForIdentity force-closes the connection if the Hub does not deliver,
// within workerIdentityTimeout, a WorkerIdentity after which OnWorkerIdentity
// reports an owner. cancel must belong to this connection so a delayed
// watchdog cannot cancel a later connection. See Client.identityReceived.
func (c *Client) watchForIdentity(ctx context.Context, cancel context.CancelCauseFunc) {
	timer := c.clock.NewTimer(workerIdentityTimeout, hubIdentityTimerTag)
	defer timer.Stop(hubIdentityTimerTag)
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		if c.identityReceived.Load() {
			return
		}
		slog.Warn("hub did not deliver a usable worker identity within timeout; forcing reconnect",
			"timeout", workerIdentityTimeout)
		cancel(errNoWorkerIdentity)
	}
}

// cancelConn cancels the current connection's context if one is active. Safe
// to call from any goroutine; a no-op when no connection is active.
func (c *Client) cancelConn(cause error) {
	c.mu.Lock()
	cancel := c.connCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel(cause)
	}
}

// bareUnauthenticatedRemedyThreshold is the number of consecutive HTTP 401
// upgrade refusals at which a worker logs its remedy. One such
// refusal can be a proxy that restarts. Several in a row mean that the Hub does
// not know this worker's token, and the worker keeps retrying for ever, so only
// the log can tell the operator what to do.
const bareUnauthenticatedRemedyThreshold = 5

// connectFn is a function that establishes a connection to the Hub.
// Used for dependency injection in tests.
type connectFn func(ctx context.Context, authToken string) error

// ErrKeysRefused reports that the Hub refused this Worker's public keys because
// they differ from the keys it registered with. Reconnecting cannot help: the
// remedy is a new registration, which creates a new Worker.
var ErrKeysRefused = errors.New("the hub refused this worker's public keys")

// ConnectWithReconnect wraps Connect with automatic reconnection using
// exponential backoff. Starts at 1s, doubles up to 180s, and resets after a
// connection lasts longer than resetThreshold.
//
// It returns nil when ctx ends, and when the Hub tells this worker it was
// deleted (after running OnDeregister). A refusal of the worker's keys returns
// an error wrapping ErrKeysRefused. Every other refusal is retried, because a
// bare status code -- a proxy's 401 or 403, a Hub that does not know the token
// -- must never destroy the worker's identity or stop it. When such refusals
// repeat, the worker logs the remedy once (bareUnauthenticatedRemedyThreshold)
// and keeps retrying.
func (c *Client) ConnectWithReconnect(ctx context.Context, authToken string) error {
	return c.connectWithReconnect(ctx, authToken, c.Connect, newDefaultBackoff(), resetThreshold)
}

func (c *Client) connectWithReconnect(ctx context.Context, authToken string, connectToHub connectFn, bo backoff, threshold time.Duration) error {
	// The number of consecutive HTTP 401 upgrade refusals.
	bareUnauthenticated := 0
	for {
		start := c.clock.Now(hubReconnectTimeTag)
		err := connectToHub(ctx, authToken)
		connectionErr := err
		if ctx.Err() != nil && errwrap.IsCancellation(err) {
			connectionErr = nil
		}

		// The stream ended, whatever the cause: report the
		// disconnection with it. The connection transition reports from
		// the identity greeting, the moment the Hub admits the worker.
		if c.OnConnectionState != nil {
			c.OnConnectionState(false, connectionErr)
		}
		if ctx.Err() != nil {
			return nil
		}

		// Only a Hub close with a permanent reason stops the loop.
		switch classifyStreamRefusal(err) {
		case refusalRetry:
			// A transient refusal retries after the delay below.
			if isUnauthorizedUpgrade(err) {
				bareUnauthenticated++
			} else {
				bareUnauthenticated = 0
			}
			// The count reaches the threshold exactly once per run of refusals,
			// so the retries that follow do not repeat this record.
			if bareUnauthenticated == bareUnauthenticatedRemedyThreshold {
				slog.Error("the hub does not know this worker's token. "+
					"Check the saved Hub endpoint and any proxy in front of the Hub. "+
					"If the hub deleted this worker more than 7 days ago, stop it, delete state.json, and register it again.",
					"refusals", bareUnauthenticated, "error", err)
			}
		case refusalWorkerDeleted:
			slog.Warn("the hub reports that this worker was deleted; stopping", "error", err)
			if c.OnDeregister != nil {
				c.OnDeregister()
			}
			return nil
		case refusalKeysDiffer:
			slog.Error("the hub refused this worker's public keys; stopping", "error", err)
			return fmt.Errorf("%w: %w", ErrKeysRefused, err)
		}

		// If the Hub sent a shutdown notification with a retry delay,
		// honour it before reconnecting. Consume the value (swap to 0)
		// so it only applies once.
		if delay := c.hubRetryDelay.Swap(0); delay > 0 {
			slog.Info("hub requested reconnect delay", "delay_seconds", delay)
			if !waitOrCancel(ctx, c.clock, time.Duration(delay)*time.Second, hubRequestedRetryTimerTag) {
				return nil
			}
			bo.Reset()
			continue
		}

		// If connection lasted long enough, reset backoff.
		if c.clock.Now(hubReconnectTimeTag).Sub(start) >= threshold {
			bo.Reset()
		}

		interval := bo.Next()
		slog.Warn("disconnected from hub, reconnecting...", "error", err, "backoff", interval)
		if !waitOrCancel(ctx, c.clock, interval, hubReconnectTimerTag) {
			return nil
		}
	}
}

type streamRefusal int

const (
	refusalRetry streamRefusal = iota
	refusalWorkerDeleted
	refusalKeysDiffer
)

// Only an exact Hub close status and reason can stop reconnecting.
func classifyStreamRefusal(err error) streamRefusal {
	var closed websocket.CloseError
	if !errors.As(err, &closed) || closed.Code != websocket.StatusPolicyViolation {
		return refusalRetry
	}
	switch closed.Reason {
	case channelwire.WorkerCloseReasonDeleted:
		return refusalWorkerDeleted
	case channelwire.WorkerCloseReasonKeysDiffer:
		return refusalKeysDiffer
	default:
		return refusalRetry
	}
}
func isUnauthorizedUpgrade(err error) bool {
	var refused *UpgradeRefusedError
	return errors.As(err, &refused) && refused.StatusCode == 401
}

// A closed socket leaves the Hub's close status to the receive loop. Other
// write failures cancel promptly rather than waiting for the silence timer.
func writerGiveUpCancelsConnection(reason sendq.GiveUpReason, err error) bool {
	return reason != sendq.GiveUpWriteError || (!errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled))
}

// connectWriterGiveUpHook builds the connect writer's OnGiveUp hook. It is a
// method so a test can drive the production hook, and it counts every give-up
// whether or not it cancels.
func (c *Client) connectWriterGiveUpHook(connCancel context.CancelCauseFunc) func(sendq.GiveUpReason, error) {
	return func(reason sendq.GiveUpReason, err error) {
		// Counted like every other sendq writer. The worker serves no /metrics
		// of its own, but solo runs it in the Hub's process against the Hub's
		// registry, so this is scrapeable there -- and an uncounted give-up is
		// indistinguishable from one that never happened, which is the wrong
		// answer to "why did that reconnect?"
		metrics.CountSendqGiveUp(metrics.PoolWorkerClient, reason.Label())
		if !writerGiveUpCancelsConnection(reason, err) {
			slog.Debug("hub connect stream writer gave up because the hub ended the stream; the receive loop reports why",
				"reason", reason.Label(), "error", err)
			return
		}
		slog.Warn("hub connect stream writer gave up; reconnecting",
			"reason", reason.Label(), "error", err)
		connCancel(fmt.Errorf("outbound queue gave up (%s): %w", reason.Label(), err))
	}
}

// hubStream is the stream seam used by deterministic tests of teardown races.
type hubStream interface {
	Send(context.Context, *leapmuxv1.ConnectRequest) error
	Receive(context.Context) (*leapmuxv1.ConnectResponse, error)
	Close() error
	CloseNow() error
	PingReceived() <-chan struct{}
}

var _ hubStream = (*WorkerStream)(nil)

const hubSilenceTimeout = wskeepalive.Interval*wskeepalive.FailuresBeforeDrop + wskeepalive.Timeout

var (
	errHubStoppedPinging     = errors.New("hub stopped pinging")
	errNoWorkerIdentity      = errors.New("hub did not deliver a usable worker identity")
	errControlQueueSaturated = errors.New("hub control queue saturated")
)

func (c *Client) watchHubSilence(ctx context.Context, timer *quartz.Timer, pings <-chan struct{}, cancel context.CancelCauseFunc) {
	defer timer.Stop(hubSilenceTimerTag)
	for {
		select {
		case <-ctx.Done():
			return
		case <-pings:
			timer.Reset(hubSilenceTimeout, hubSilenceTimerTag)
		case <-timer.C:
			cancel(errHubStoppedPinging)
			return
		}
	}
}

// A stop takes precedence over a transport error. The Hub's close takes
// precedence over a local failure so a raced refusal still reaches policy.
func receiveEndError(readErr error, ctx, connCtx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("worker stopped: %w", context.Cause(ctx))
	}
	var closed websocket.CloseError
	if errors.As(readErr, &closed) {
		return readErr
	}
	if connCtx.Err() != nil {
		return fmt.Errorf("connection ended: %w", context.Cause(connCtx))
	}
	return readErr
}
