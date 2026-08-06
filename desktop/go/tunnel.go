package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	tunnelpkg "github.com/leapmux/leapmux/tunnel"
	"github.com/leapmux/leapmux/util/ctxutil"
)

const (
	tunnelTypePortForward = "port_forward"
	tunnelTypeSocks5      = "socks5"

	// dialTunnelOpenTimeout bounds a single OpenTunnelConn round-trip. The
	// per-connection dial context is otherwise only the tunnel lifetime (no
	// deadline), so without this a lost/never-sent worker open response would
	// block a port-forward/SOCKS5 dial -- and its accepted local conn -- until
	// the whole tunnel is torn down. Mirrors tunnelpkg.DialTunnel's own timeout.
	dialTunnelOpenTimeout = 30 * time.Second
)

// channelOpenTimeout bounds a single E2EE channel open (the Noise handshake,
// two ConnectRPC calls, and the /ws/channel WebSocket dial). It is the sibling
// of dialTunnelOpenTimeout for the step BEFORE the tunnel dial: the open runs
// under the tunnel-lifetime epoch context (no deadline) and the desktop proxy's
// HTTP clients carry no http.Client.Timeout, so without this a hub that accepts
// the TCP connection but stalls the upgrade/handshake would block the
// create-time open -- and every per-connection re-resolve (liveChannel) --
// indefinitely, hanging the accepted local conn exactly the way
// dialTunnelOpenTimeout was added to prevent for the dial. The opened channel's
// lifetime stays bound to the epoch context, not this deadline.
//
// Sourced from the tunnel package rather than restated: it bounds
// tunnel.OpenChannel, so the figure belongs beside that call, where the open's own
// internal budgets are reasoned against it. The worker's cross-worker client bounds
// the same call and had its own hand-copied 30s. It stays a var so tests can shorten
// it.
var channelOpenTimeout = tunnelpkg.DefaultChannelOpenTimeout

// TunnelConfig is the sidecar's internal tunnel configuration. HubURL is
// injected from the authenticated desktop connection, never accepted from the
// frontend request.
type TunnelConfig struct {
	WorkerID   string
	Type       string // "port_forward" or "socks5"
	TargetAddr string // port_forward only
	TargetPort int    // port_forward only
	BindAddr   string
	BindPort   int
	HubURL     string
}

// validateAndNormalize checks the config's required fields and fills in the
// defaults a caller may omit, mutating the receiver in place. It is separated
// from CreateTunnel's socket/channel work so the well-formed-and-defaulted rules
// -- including the loopback-only bind invariant and the per-type port defaults --
// are unit-testable without binding a listener or opening a channel to a worker.
func (cfg *TunnelConfig) validateAndNormalize() error {
	if cfg.WorkerID == "" {
		return fmt.Errorf("workerId is required")
	}
	if cfg.Type != tunnelTypePortForward && cfg.Type != tunnelTypeSocks5 {
		return fmt.Errorf("type must be '%s' or '%s'", tunnelTypePortForward, tunnelTypeSocks5)
	}
	if cfg.Type == tunnelTypePortForward {
		if cfg.TargetAddr == "" {
			return fmt.Errorf("targetAddr is required for port_forward")
		}
		if cfg.TargetPort < 1 || cfg.TargetPort > 65535 {
			return fmt.Errorf("targetPort must be 1-65535")
		}
	}
	if cfg.BindAddr == "" {
		cfg.BindAddr = defaultTunnelBindAddr
	}
	if err := validateTunnelBindAddr(cfg.BindAddr); err != nil {
		return err
	}
	if cfg.BindPort == 0 {
		if cfg.Type == tunnelTypePortForward {
			cfg.BindPort = cfg.TargetPort
		} else {
			cfg.BindPort = 1080
		}
	}
	if cfg.BindPort < 1 || cfg.BindPort > 65535 {
		return fmt.Errorf("bindPort must be 1-65535")
	}
	return nil
}

// TunnelInfo describes an active tunnel, returned to the frontend.
type TunnelInfo struct {
	ID         string `json:"id"`
	WorkerID   string `json:"workerId"`
	Type       string `json:"type"`
	BindAddr   string `json:"bindAddr"`
	BindPort   int    `json:"bindPort"`
	TargetAddr string `json:"targetAddr"`
	TargetPort int    `json:"targetPort"`
}

// tunnel represents a single active tunnel.
type tunnel struct {
	mu          sync.Mutex
	info        TunnelInfo
	hubURL      string // Hub URL the E2EE channel is keyed on (for per-connection re-resolution)
	listener    net.Listener
	cancel      context.CancelFunc
	connections map[net.Conn]struct{}
	closed      bool
}

// tunnelListener adapts a tunnel's listener for go-socks5's Server.Serve: it
// applies the shared transient-error retry policy (see acceptWithRetry) and hands
// back a connection the tunnel owns, so teardown can hard-close it.
//
// ctx bounds the retry backoff; it is the serve loop's context.
type tunnelListener struct {
	net.Listener
	tunnel *tunnel
	ctx    context.Context
}

func (l tunnelListener) Accept() (net.Conn, error) {
	conn, err := acceptWithRetry(l.ctx, l.Listener, l.tunnel.info.ID)
	if err != nil {
		return nil, err
	}
	owned, ok := l.tunnel.ownConnection(conn)
	if !ok {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	return owned, nil
}

// acceptWithRetry calls ln.Accept, absorbing TRANSIENT failures with a bounded
// backoff so a momentary fd exhaustion does not end an accept loop.
//
// It is the shared home of that policy for the two tunnel types, which accept
// through different loops and BOTH need it: acceptPortForward runs its own loop,
// while the SOCKS5 tunnel hands its listener to go-socks5's Server.Serve, which
// returns on ANY Accept error and has no retry of its own. Leaving the policy in
// only one loop meant an EMFILE spike -- exactly the "browser fanning out through
// SOCKS5" workload the isTemporaryAcceptError files name -- was ridden out by a
// port-forward tunnel but permanently killed a SOCKS5 one, since serveSocks5 fails
// the tunnel on any error Serve returns. The control-socket accept loop
// (acceptBeforeIdleDeadline) keeps its own loop rather than calling this wrapper --
// its accept races an idle timer and a shutdown channel, not a context, so it owns
// its own select shape -- but the two share the predicate, the constants, and the
// wait+backoff step (backoffAfterTemporaryAccept), so only the loop shape differs.
//
// A cancelled ctx returns the error rather than retrying: the caller is tearing down
// and checks ctx.Err() itself.
func acceptWithRetry(ctx context.Context, ln net.Listener, tunnelID string) (net.Conn, error) {
	retryDelay := acceptRetryDelayMin
	for {
		conn, err := ln.Accept()
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil || !isTemporaryAcceptError(err) {
			return nil, err
		}
		slog.Warn("tunnel accept failed; retrying",
			"tunnel_id", tunnelID, "error", err, "retry_in", retryDelay)
		var cancelled bool
		if retryDelay, cancelled = backoffAfterTemporaryAccept(retryDelay, ctx.Done()); cancelled {
			// The caller is tearing down; surface the accept error, not a retry.
			return nil, err
		}
	}
}

type ownedTunnelConn struct {
	net.Conn
	tunnel *tunnel
	once   sync.Once
}

func (c *ownedTunnelConn) Close() error {
	var err error
	c.once.Do(func() {
		c.tunnel.removeConnection(c.Conn)
		err = c.Conn.Close()
	})
	return err
}

// CloseWrite half-closes the underlying connection when it supports it. The
// desktop SOCKS5 server (go-socks5) type-asserts the client connection to
// `interface { CloseWrite() error }` after the remote->client copy finishes so
// the SOCKS5 client observes a prompt read-EOF (HTTP/1.0, chunked-without-
// length, SSH, and line protocols rely on it). Embedding net.Conn as an
// interface would hide *net.TCPConn.CloseWrite from that assertion, so forward
// it explicitly. This is a half-close; the full-close bookkeeping stays in Close.
func (c *ownedTunnelConn) CloseWrite() error {
	return tryCloseWrite(c.Conn)
}

type dialTunnelFunc func(context.Context, *managedChannel, string, uint32) (net.Conn, error)

// TunnelManager manages all active tunnels for the desktop app.
type TunnelManager struct {
	mu      sync.Mutex
	tunnels map[string]*tunnel
	pool    *channelPool
	dial    dialTunnelFunc
}

// NewTunnelManager creates a new TunnelManager.
func NewTunnelManager() *TunnelManager {
	return &TunnelManager{
		tunnels: make(map[string]*tunnel),
		pool: newChannelPool(func(
			ctx context.Context,
			hubURL, workerID string,
			opts *tunnelpkg.OpenChannelOptions,
		) (*managedChannel, error) {
			ch, err := tunnelpkg.OpenChannel(ctx, hubURL, workerID, opts)
			if err != nil {
				return nil, err
			}
			return wrapChannel(ch), nil
		}),
		dial: func(ctx context.Context, ch *managedChannel, targetAddr string, targetPort uint32) (net.Conn, error) {
			return tunnelpkg.DialTunnelContext(ctx, ch.channel, targetAddr, targetPort)
		},
	}
}

// SetChannelOptions sets transport options for opening E2EE channels.
// Holds TunnelManager.mu so CreateTunnel's revision check + tunnel insert
// cannot race a concurrent identity reset (pool.reset only takes pool.mu).
func (m *TunnelManager) SetChannelOptions(opts *tunnelpkg.OpenChannelOptions) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pool.setOptions(opts)
}

// currentRevision returns the pool's invalidation epoch revision. Tests and
// CreateTunnel capture it before awaiting an open so a concurrent reset can
// fence registration.
func (m *TunnelManager) currentRevision() uint64 {
	return m.pool.currentRevision()
}

// CreateTunnel creates and starts a new tunnel.
func (m *TunnelManager) CreateTunnel(operationCtx, lifetimeCtx context.Context, cfg TunnelConfig) (*TunnelInfo, error) {
	if operationCtx == nil {
		return nil, fmt.Errorf("operation context is required")
	}
	if lifetimeCtx == nil {
		return nil, fmt.Errorf("lifetime context is required")
	}
	revision := m.pool.currentRevision()

	if err := cfg.validateAndNormalize(); err != nil {
		return nil, err
	}

	// Ensure we have an E2EE channel to the worker.
	opened, err := m.pool.getOrOpen(operationCtx, revision, cfg.HubURL, cfg.WorkerID)
	if err != nil {
		slog.Error("failed to open channel to worker", "worker_id", cfg.WorkerID, "error", err)
		return nil, fmt.Errorf("open channel to worker: %w", err)
	}

	// Create TCP listener.
	bindAddr := net.JoinHostPort(cfg.BindAddr, fmt.Sprintf("%d", cfg.BindPort))
	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		slog.Error("failed to bind tunnel listener", "bind_addr", bindAddr, "error", err)
		return nil, fmt.Errorf("bind %s: %w", bindAddr, err)
	}

	actualAddr := listener.Addr().(*net.TCPAddr)
	tunnelID := uuid.New().String()
	info := TunnelInfo{
		ID:         tunnelID,
		WorkerID:   cfg.WorkerID,
		Type:       cfg.Type,
		BindAddr:   actualAddr.IP.String(),
		BindPort:   actualAddr.Port,
		TargetAddr: cfg.TargetAddr,
		TargetPort: cfg.TargetPort,
	}

	ctx, cancel := context.WithCancel(lifetimeCtx)
	t := &tunnel{
		info:        info,
		hubURL:      cfg.HubURL,
		listener:    listener,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}

	m.mu.Lock()
	// A cancelled operation/lifetime, or an epoch reset that raced this create,
	// means the tunnel must not be registered -- undo the listener + lifetime.
	startErr := operationCtx.Err()
	if startErr == nil {
		startErr = lifetimeCtx.Err()
	}
	if startErr == nil && revision != m.pool.currentRevision() {
		startErr = fmt.Errorf("tunnel manager reset while creating tunnel")
	}
	if startErr != nil {
		m.mu.Unlock()
		cancel()
		_ = listener.Close()
		return nil, startErr
	}
	m.tunnels[tunnelID] = t
	m.mu.Unlock()

	if cfg.Type == tunnelTypeSocks5 {
		go m.serveSocks5(ctx, t, opened)
	} else {
		go m.acceptPortForward(ctx, t, opened)
	}

	slog.Info("tunnel created",
		"tunnel_id", tunnelID, "type", cfg.Type,
		"bind", actualAddr.String(),
		"target", fmt.Sprintf("%s:%d", cfg.TargetAddr, cfg.TargetPort),
	)
	return &info, nil
}

// defaultTunnelBindAddr is where a tunnel listens when the caller names no
// address: loopback, reachable only from this machine.
const defaultTunnelBindAddr = "127.0.0.1"

// validateTunnelBindAddr refuses to bind a tunnel anywhere but loopback.
//
// Both tunnel types forward INTO the worker's network, and neither listener
// authenticates -- the SOCKS5 server is built with no auth methods, so go-socks5
// accepts anyone. Their whole security rests on being reachable only from this
// machine. BindAddr is frontend-supplied (the sibling fields are all validated;
// this one was only defaulted), so a value of "0.0.0.0" -- from hostile content in
// the webview, or a user who simply typed it -- turned the laptop into an open,
// unauthenticated gateway that any host on the LAN could pivot through into the
// worker's private network.
//
// Exposing a tunnel beyond loopback is not inherently wrong, but it is a
// deliberate act that needs authentication attached; until the listener has any,
// refuse rather than let it happen silently.
func validateTunnelBindAddr(bindAddr string) error {
	ip := net.ParseIP(bindAddr)
	if ip == nil {
		return fmt.Errorf("bindAddr must be an IP address, got %q", bindAddr)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("bindAddr must be a loopback address (the tunnel listener is unauthenticated), got %q", bindAddr)
	}
	return nil
}

// DeleteTunnel stops and removes a tunnel.
func (m *TunnelManager) DeleteTunnel(tunnelID string) error {
	m.mu.Lock()
	t, ok := m.tunnels[tunnelID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tunnel not found: %s", tunnelID)
	}
	delete(m.tunnels, tunnelID)
	m.mu.Unlock()

	t.close()
	slog.Info("tunnel deleted", "tunnel_id", tunnelID)
	return nil
}

// ListTunnels returns info about all active tunnels.
func (m *TunnelManager) ListTunnels() []TunnelInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]TunnelInfo, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		result = append(result, t.info)
	}
	return result
}

// CloseAll closes all tunnels and channels.
func (m *TunnelManager) CloseAll() {
	m.mu.Lock()
	tunnels := m.tunnels
	m.tunnels = make(map[string]*tunnel)
	// Reset the pool while holding m.mu so CreateTunnel cannot pass its
	// revision check and insert between the map clear and the epoch bump.
	m.pool.reset()
	m.mu.Unlock()

	for _, t := range tunnels {
		t.close()
	}
	// Only when there was something to close. CloseAll runs on every disconnect
	// and every shutdown, so an unconditional line announces a non-event on the
	// overwhelmingly common path where no tunnel was ever created.
	if len(tunnels) > 0 {
		slog.Info("all tunnels closed", "count", len(tunnels))
	}
}

// serveSocks5 runs a SOCKS5 server on the tunnel's listener.
// Each CONNECT request is dialed through the E2EE channel via tunnel.DialTunnelContext.
func (m *TunnelManager) serveSocks5(ctx context.Context, t *tunnel, ch *managedChannel) {
	server := newSocks5Server(func(requestCtx context.Context, _, addr string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}
		p, err := strconv.ParseUint(portStr, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q: %w", portStr, err)
		}
		port := uint32(p)
		dialCtx, cancelDial := ctxutil.WithLinkedCancel(ctx, requestCtx)
		defer cancelDial()
		live, err := m.pool.live(dialCtx, ch, t.hubURL, t.info.WorkerID)
		if err != nil {
			return nil, err
		}
		openCtx, cancelOpen := context.WithTimeout(dialCtx, dialTunnelOpenTimeout)
		target, err := m.dial(openCtx, live, host, port)
		cancelOpen()
		if err != nil {
			return nil, err
		}
		owned, ok := t.ownConnection(target)
		if !ok {
			_ = target.Close()
			return nil, net.ErrClosed
		}
		return owned, nil
	})

	// Serve returns only when the listener is finished. On a clean teardown the
	// tunnel is already going away; otherwise its accept loop is gone for good, so
	// fail the tunnel rather than leave a registered one that can never accept.
	err := server.Serve(tunnelListener{Listener: t.listener, tunnel: t, ctx: ctx})
	if ctx.Err() != nil || err == nil {
		slog.Debug("socks5 server stopped", "tunnel_id", t.info.ID, "error", err)
		return
	}
	m.failTunnel(t, err)
}

// acceptRetryDelay bounds the backoff an accept loop applies to a TRANSIENT
// Accept failure. It mirrors net/http.Server.Serve's 5ms..1s doubling window:
// long enough that an fd exhaustion spike is not spun on, short enough that the
// tunnel resumes serving as soon as descriptors free up.
const (
	acceptRetryDelayMin = 5 * time.Millisecond
	acceptRetryDelayMax = 1 * time.Second
)

// nextAcceptRetryDelay doubles the accept backoff up to acceptRetryDelayMax. It
// is the one place the backoff curve lives, shared by acceptWithRetry (the
// tunnel accept loops) and the control-socket accept loop in socket.go: both
// describe themselves as "the same policy" as the predicate and constants above,
// so a future change to the curve (jitter, a different cap) lands here rather
// than being mirrored in two loops that can drift apart.
func nextAcceptRetryDelay(current time.Duration) time.Duration {
	return min(current*2, acceptRetryDelayMax)
}

// backoffAfterTemporaryAccept waits out one backoff step (delay) after a transient
// Accept failure, returning early if cancel fires, and reports the next delay to
// use. cancelled is true iff cancel won, so the caller abandons its accept loop.
//
// It is the shared wait+backoff step of the two accept-retry loops -- acceptWithRetry
// (the tunnel loops, cancelled by a context's Done channel) and
// acceptBeforeIdleDeadline (the control socket, cancelled by a shutdown channel).
// ONLY this step is shared: each caller keeps its own loop, cancel source, warn
// message, and abandon action (return the error vs signal a result channel), because
// those genuinely differ -- extracting the whole loop would trade the duplication for
// a worse tangle of policy closures. Sharing the timer step alone puts the one subtle
// bit in one place: the stopped timer (not time.After, which would pin a delay's
// worth of heap per call when cancel wins -- the ordinary teardown path here, not a
// rare one; see drain.WaitBounded), and advancing the delay only when the wait completed.
func backoffAfterTemporaryAccept(delay time.Duration, cancel <-chan struct{}) (next time.Duration, cancelled bool) {
	timer := time.NewTimer(delay)
	select {
	case <-timer.C:
		return nextAcceptRetryDelay(delay), false
	case <-cancel:
		timer.Stop()
		return delay, true
	}
}

// failTunnel tears a tunnel down and evicts it from the manager after its
// listener died for good.
//
// A dead accept loop must not leave the tunnel registered: ListTunnels would keep
// reporting it, the UI would keep showing it active, and every new local
// connection would be refused with nothing to explain why. Removing it makes the
// death observable -- the tunnel disappears -- instead of silently inert.
func (m *TunnelManager) failTunnel(t *tunnel, reason error) {
	slog.Error("tunnel listener failed; removing the tunnel",
		"tunnel_id", t.info.ID, "error", reason)
	if err := m.DeleteTunnel(t.info.ID); err != nil {
		// Already gone (a concurrent DeleteTunnel/CloseAll): nothing to reclaim.
		slog.Debug("tunnel already removed", "tunnel_id", t.info.ID, "error", err)
	}
}

// acceptPortForward accepts TCP connections and forwards them to a fixed target.
func (m *TunnelManager) acceptPortForward(ctx context.Context, t *tunnel, ch *managedChannel) {
	for {
		// acceptWithRetry absorbs transient failures (an fd spike), so an error here
		// means the listener is done for. It does not own the connection: that is
		// handlePortForward's job, which is why this loop calls the listener directly
		// rather than going through tunnelListener.
		conn, err := acceptWithRetry(ctx, t.listener, t.info.ID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Fail the tunnel so it stops reporting healthy.
			m.failTunnel(t, err)
			return
		}
		// handlePortForward owns the connection (tracks it and wraps it for
		// CloseWrite); if the tunnel is closing it closes the conn and returns.
		go m.handlePortForward(ctx, conn, t, ch)
	}
}

// handlePortForward handles a single port-forward TCP connection.
func (m *TunnelManager) handlePortForward(ctx context.Context, conn net.Conn, t *tunnel, ch *managedChannel) {
	// Own both ends so teardown (t.close) can hard-close them and so each side
	// supports CloseWrite: the local end forwards to *net.TCPConn, and the
	// tunneled remote Conn forwards to its CloseWrite, which sends a close_write
	// frame the worker applies to the target -- so the port-forward copy
	// propagates a half-close in each direction. ownedTunnelConn.Close removes
	// the conn from tracking and closes it.
	local, ok := t.ownConnection(conn)
	if !ok {
		_ = conn.Close()
		return
	}
	defer func() { _ = local.Close() }()

	live, err := m.pool.live(ctx, ch, t.hubURL, t.info.WorkerID)
	if err != nil {
		slog.Error("resolve tunnel channel failed", "tunnel_id", t.info.ID, "error", err)
		return
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, dialTunnelOpenTimeout)
	target, err := m.dial(dialCtx, live, t.info.TargetAddr, uint32(t.info.TargetPort))
	cancelDial()
	if err != nil {
		slog.Error("dial tunnel failed", "tunnel_id", t.info.ID, "error", err)
		return
	}
	remote, ok := t.ownConnection(target)
	if !ok {
		_ = target.Close()
		return
	}
	defer func() { _ = remote.Close() }()

	// Bidirectional copy with half-close: when a direction finishes CLEANLY, forward
	// a write half-close to the peer instead of full-closing, so an in-flight
	// response on the other direction is delivered (e.g. an HTTP/1.0 client that
	// half-closes after the request -- the previous full-close-on-first-done
	// truncated it). Both ends are full-closed once both directions complete.
	//
	// A direction that ends in an ERROR is the opposite case and must NOT linger:
	// io.Copy returns nil only on a graceful FIN (Read -> io.EOF) and non-nil on an
	// RST or a failed write, which means the peer is gone and there is no response
	// left to deliver. Without cancelConn the surviving copy stays parked in Read
	// forever -- nothing else bounds it (no read deadline on this path, and ctx is
	// the whole tunnel's lifetime), pinning the goroutine, both fds, the tunneled
	// Conn's send-window slot and its worker-side conn-map entry until the tunnel is
	// torn down. A browsing session accumulates one per aborted connection.
	//
	// A blanket read deadline would be the wrong bound: it would also kill
	// legitimately idle half-open SSH/DB sessions this half-close exists to serve.
	//
	// Force-close both ends when the conn's context fires -- an error-path
	// cancelConn (below), the parent tunnel ctx (teardown), or this handler's
	// return -- to unblock a copy parked in Read. context.AfterFunc registers the
	// close without a parked goroutine, unlike a goroutine blocked on
	// connCtx.Done() (the 4th per accepted conn, pure overhead while the conn is
	// healthy); the worker side uses the same pattern per tunnelConn
	// (backend/internal/worker/service/tunnel.go's stopCtxWatch). Closing both
	// ends is safe to race the deferred closes above: ownedTunnelConn.Close is
	// once-guarded. Because stopForceClose is deferred after cancelConn, a normal
	// return detaches the callback before cancelConn fires, so the happy path
	// never runs it.
	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()
	stopForceClose := context.AfterFunc(connCtx, func() {
		_ = local.Close()
		_ = remote.Close()
	})
	defer stopForceClose()

	copyDone := make(chan struct{}, 2)
	copyHalf := func(dst, src net.Conn) {
		defer func() { copyDone <- struct{}{} }()
		if _, err := io.Copy(dst, src); err != nil {
			cancelConn() // the peer died hard: no response can arrive, so do not linger
			return
		}
		// Clean EOF: forward the write-close and let the other direction drain.
		// Errors are ignored -- half-close is best-effort teardown aid, not a
		// reported result.
		_ = tryCloseWrite(dst)
	}
	go copyHalf(remote, local) // local client -> remote target
	go copyHalf(local, remote) // remote target -> local client
	<-copyDone
	<-copyDone
}

func (t *tunnel) addConnection(conn net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	// connections is created in CreateTunnel and only nilled by close(), which
	// sets closed first -- so a live tunnel always has a non-nil map here.
	t.connections[conn] = struct{}{}
	return true
}

func (t *tunnel) ownConnection(conn net.Conn) (net.Conn, bool) {
	if !t.addConnection(conn) {
		return nil, false
	}
	return &ownedTunnelConn{Conn: conn, tunnel: t}, true
}

func (t *tunnel) removeConnection(conn net.Conn) {
	t.mu.Lock()
	delete(t.connections, conn)
	t.mu.Unlock()
}

// tryCloseWrite half-closes conn's write side when it supports it, forwarding a
// read-EOF to the peer, and reports nothing to do (nil) when it does not -- such a
// conn degrades to waiting for the peer's natural close.
//
// Single home of the capability probe, because the assertion is needed in two
// places that would otherwise drift: embedding net.Conn as an INTERFACE hides
// *net.TCPConn.CloseWrite from any assertion made on the wrapper, and the tunneled
// Conn implements CloseWrite by sending a close_write frame the worker applies to
// the target. Both ends of the port-forward copy therefore support it.
func tryCloseWrite(conn net.Conn) error {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (t *tunnel) close() {
	// Cancel BEFORE flipping `closed`, not after unlocking. cancel is idempotent
	// and needs no lock, and the order is what the serve loops read: once closed is
	// set, tunnelListener.Accept rejects a freshly accepted conn (ownConnection
	// fails) with net.ErrClosed, go-socks5's Serve returns on it, and serveSocks5
	// decides between "clean teardown" and "the listener died" by testing
	// ctx.Err(). Cancelling afterwards left a window where ctx.Err() was still nil,
	// so an ordinary user-initiated DeleteTunnel logged a spurious "tunnel listener
	// failed" ERROR and sent operators hunting a failure that never happened.
	if t.cancel != nil {
		t.cancel()
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	connections := make([]net.Conn, 0, len(t.connections))
	for conn := range t.connections {
		connections = append(connections, conn)
	}
	t.connections = nil
	t.mu.Unlock()

	if t.listener != nil {
		_ = t.listener.Close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
}
