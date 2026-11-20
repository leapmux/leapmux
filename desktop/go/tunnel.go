package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
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
	// The Worker identity this tunnel admits: the user every channel must
	// authenticate as and the exact Worker keys the Hub must announce (the
	// composite hex NewExpectedWorkerKeyPin decodes). Both come from the
	// request; a create that omits either, or carries an unusable
	// expectation, fails before a listener is bound or a channel opened.
	ExpectedUserID        string
	ExpectedWorkerKeysHex string
	// ChannelEpoch is the channel epoch the caller read from a tunnel
	// listing. The create is admitted against that exact epoch: a submission
	// fenced on a listing that a reset has since invalidated is refused before
	// a listener is bound or a channel opened.
	ChannelEpoch string
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
	if cfg.ExpectedUserID == "" {
		return fmt.Errorf("expectedUserId is required")
	}
	if cfg.ExpectedWorkerKeysHex == "" {
		return fmt.Errorf("expectedWorkerKeysHex is required")
	}
	if _, err := tunnelpkg.NewExpectedWorkerKeyPin(cfg.ExpectedWorkerKeysHex); err != nil {
		return err
	}
	if cfg.ChannelEpoch == "" {
		return fmt.Errorf("channelEpoch is required")
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
	// KeyMismatch is set only on a stopped row: the Worker key mismatch that
	// stopped the tunnel. The record survives (until delete or reset) so a
	// hint delivered early or lost still recovers through a listing.
	KeyMismatch *TunnelKeyMismatch `json:"keyMismatch,omitempty"`
}

// TunnelKeyMismatch is the Worker key mismatch that refused a create or
// stopped a tunnel: the fingerprints of the expected key set and of the set
// the Hub announced. The fingerprints are the human-comparison form (the same
// four-word fingerprint the Worker logs); the Worker's log is the comparison
// source.
type TunnelKeyMismatch struct {
	ExpectedFingerprint  string `json:"expectedFingerprint"`
	PresentedFingerprint string `json:"presentedFingerprint"`
}

// keyMismatchOf returns the Worker key mismatch err reports, or nil when err
// is an ordinary failure. It is the one conversion from the tunnel package's
// typed mismatch to the wire-shaped detail both the create result and a
// stopped list row carry.
func keyMismatchOf(err error) *TunnelKeyMismatch {
	var mismatch *tunnelpkg.WorkerKeyMismatchError
	if !errors.As(err, &mismatch) {
		return nil
	}
	return &TunnelKeyMismatch{
		ExpectedFingerprint:  mismatch.ExpectedFingerprint,
		PresentedFingerprint: mismatch.PresentedFingerprint,
	}
}

type tunnelState uint8

const (
	tunnelLive tunnelState = iota
	tunnelStoppingForMismatch
	tunnelRemoving
)

// tunnel holds the listener, owned sockets, and immutable admission identity
// for one tunnel. The manager lock protects state and info.KeyMismatch.
type tunnel struct {
	info          TunnelInfo
	state         tunnelState
	hubURL        string
	listener      net.Listener
	cancel        context.CancelFunc
	expect        channelExpectation
	admittedEpoch string

	// mu serializes handoff admission, connection ownership, and retirement.
	mu          sync.Mutex
	retiring    bool
	connections map[*ownedTunnelConn]struct{}
	handoffs    sync.WaitGroup
	closeDone   chan struct{}
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
	if !l.tunnel.beginHandoff() {
		return nil, net.ErrClosed
	}
	defer l.tunnel.handoffs.Done()

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
	tunnel    *tunnel
	closing   atomic.Bool
	closeDone chan struct{}
	closeErr  error
}

func (c *ownedTunnelConn) Close() error {
	if c.closing.CompareAndSwap(false, true) {
		c.closeErr = c.Conn.Close()
		c.tunnel.removeConnection(c)
		close(c.closeDone)
	} else {
		<-c.closeDone
	}
	return c.closeErr
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
	mu             sync.Mutex
	tunnels        map[string]*tunnel
	retiringEpochs map[string]chan struct{}
	latestReset    <-chan struct{}
	pool           *channelPool
	dial           dialTunnelFunc
	listen         func(network, address string) (net.Listener, error)
	// onChanged publishes a native tunnel status change (a mismatch
	// transition) so the webview re-reads the tunnel list. Set by NewApp
	// before any tunnel exists; nil in a bare manager (tests).
	onChanged func()
}

// NewTunnelManager creates a new TunnelManager.
func NewTunnelManager() *TunnelManager {
	return &TunnelManager{
		tunnels:        make(map[string]*tunnel),
		retiringEpochs: make(map[string]chan struct{}),
		listen:         net.Listen,
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
			target, err := tunnelpkg.DialTunnelContext(ctx, ch.channel, targetAddr, targetPort)
			if target == nil {
				// Preserve nil when converting from *tunnel.Conn to net.Conn.
				return nil, err
			}
			return target, err
		},
	}
}

// SetChannelOptions retires the current tunnels and replaces the transport
// options. A tunnel admitted under the previous epoch cannot reuse them.
func (m *TunnelManager) SetChannelOptions(opts *tunnelpkg.OpenChannelOptions) {
	m.resetTunnelEpoch(nil, func() *channelEpoch { return m.pool.setOptions(opts) })
}

// currentEpoch returns the pool's invalidation epoch id -- the value
// ListTunnels reports as channel_epoch. Tests and CreateTunnel capture it
// before awaiting an open so a concurrent reset can fence registration.
func (m *TunnelManager) currentEpoch() string {
	return m.pool.currentEpoch()
}

// CreateTunnel creates and starts a new tunnel.
func (m *TunnelManager) CreateTunnel(operationCtx, lifetimeCtx context.Context, cfg TunnelConfig) (*TunnelInfo, error) {
	if operationCtx == nil {
		return nil, fmt.Errorf("operation context is required")
	}
	if lifetimeCtx == nil {
		return nil, fmt.Errorf("lifetime context is required")
	}
	if err := cfg.validateAndNormalize(); err != nil {
		return nil, err
	}
	admittedEpoch := cfg.ChannelEpoch
	if err := m.awaitAdmission(operationCtx, lifetimeCtx, admittedEpoch); err != nil {
		return nil, err
	}
	expect := channelExpectation{
		userID:  cfg.ExpectedUserID,
		keysHex: cfg.ExpectedWorkerKeysHex,
	}

	// Ensure we have an E2EE channel to the worker.
	opened, err := m.pool.getOrOpen(operationCtx, admittedEpoch, cfg.HubURL, cfg.WorkerID, expect)
	if err != nil {
		slog.Error("failed to open channel to worker", "worker_id", cfg.WorkerID, "error", err)
		return nil, fmt.Errorf("open channel to worker: %w", err)
	}

	// Bind and register under the same lock as epoch rotation. Reset must
	// either capture the listener or prevent the old create from binding.
	bindAddr := net.JoinHostPort(cfg.BindAddr, strconv.Itoa(cfg.BindPort))
	m.mu.Lock()
	if err := m.checkAdmissionLocked(operationCtx, lifetimeCtx, admittedEpoch); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	listener, err := m.listen("tcp", bindAddr)
	if err != nil {
		m.mu.Unlock()
		slog.Error("failed to bind tunnel listener", "bind_addr", bindAddr, "error", err)
		return nil, fmt.Errorf("bind %s: %w", bindAddr, err)
	}
	if err := m.checkAdmissionLocked(operationCtx, lifetimeCtx, admittedEpoch); err != nil {
		// Cancellation can race the numeric bind. Close the unregistered
		// listener before allowing reset or another create to proceed.
		_ = listener.Close()
		m.mu.Unlock()
		return nil, err
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
		info:          info,
		hubURL:        cfg.HubURL,
		listener:      listener,
		cancel:        cancel,
		expect:        expect,
		admittedEpoch: admittedEpoch,
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

// awaitAdmission waits for any older epoch to finish cleanup. The lock
// protects the epoch check and the snapshot of the pending cleanup.
func (m *TunnelManager) awaitAdmission(operationCtx, lifetimeCtx context.Context, epoch string) error {
	for {
		m.mu.Lock()
		err := m.checkAdmissionLocked(operationCtx, lifetimeCtx, epoch)
		pending := m.latestReset
		m.mu.Unlock()

		if err != nil || pending == nil {
			return err
		}
		select {
		case <-operationCtx.Done():
			return operationCtx.Err()
		case <-lifetimeCtx.Done():
			return lifetimeCtx.Err()
		case <-pending:
		}
	}
}

func (m *TunnelManager) checkAdmissionLocked(operationCtx, lifetimeCtx context.Context, epoch string) error {
	if err := operationCtx.Err(); err != nil {
		return err
	}
	if err := lifetimeCtx.Err(); err != nil {
		return err
	}
	if epoch != m.pool.currentEpoch() {
		return fmt.Errorf("stale channel epoch: the tunnel listing has been reset")
	}
	return nil
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
//
// channelEpoch is the epoch the caller's listing reported. A delete fenced on
// a listing a reset has since invalidated is refused before the lookup: the
// caller's row set is stale and it must re-read before mutating.
func (m *TunnelManager) DeleteTunnel(tunnelID string, channelEpoch string) error {
	m.mu.Lock()
	if channelEpoch != m.pool.currentEpoch() {
		m.mu.Unlock()
		return fmt.Errorf("stale channel epoch: the tunnel listing has been reset")
	}
	t, ok := m.tunnels[tunnelID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tunnel not found: %s", tunnelID)
	}
	t.state = tunnelRemoving
	t.stopAdmission()
	m.mu.Unlock()

	m.closeAndRemove(t)
	slog.Info("tunnel deleted", "tunnel_id", tunnelID)
	return nil
}

// ListTunnels returns the current channel epoch and the tunnels owned by
// expectedUser -- active rows and stopped rows alike -- from one manager
// snapshot. The epoch and the rows are read under one lock (every epoch
// rotation holds it too), so a caller can fence its next mutation against
// exactly the listing it saw.
//
// The owner is the expected user a create was admitted under; a listing that
// names no user is refused rather than silently answered for one owner.
func (m *TunnelManager) ListTunnels(expectedUser string) (string, []TunnelInfo, error) {
	if expectedUser == "" {
		return "", nil, fmt.Errorf("expectedUserId is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]TunnelInfo, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		if t.expect.userID != expectedUser || t.state == tunnelRemoving {
			continue
		}
		result = append(result, t.info)
	}
	return m.pool.currentEpoch(), result, nil
}

// CloseAll retires the current epoch and joins all earlier cleanup.
func (m *TunnelManager) CloseAll() {
	m.resetTunnelEpoch(nil, m.pool.rotate)
}

// ResetTunnels retires the requested epoch. A replay joins any cleanup still
// running for that epoch and leaves the successor unchanged.
func (m *TunnelManager) ResetTunnels(channelEpoch string) {
	m.resetTunnelEpoch(&channelEpoch, m.pool.rotate)
}

// resetTunnelEpoch rotates the pool, detaches the tunnel records, and
// publishes the shared cleanup completion under the same manager lock.
func (m *TunnelManager) resetTunnelEpoch(expectedEpoch *string, rotate func() *channelEpoch) {
	m.mu.Lock()
	epoch := m.pool.currentEpoch()
	if expectedEpoch != nil && *expectedEpoch != epoch {
		done := m.retiringEpochs[*expectedEpoch]
		m.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}

	// Every reset joins its predecessor. Waiting for the latest reset also
	// waits for every earlier one.
	predecessor := m.latestReset
	done := make(chan struct{})
	m.latestReset = done
	m.retiringEpochs[epoch] = done
	tunnels := m.tunnels
	m.tunnels = make(map[string]*tunnel)
	for _, t := range tunnels {
		t.stopAdmission()
	}
	retired := rotate()
	m.mu.Unlock()

	var cleanup sync.WaitGroup
	for _, t := range tunnels {
		cleanup.Go(t.close)
	}
	retired.close()
	cleanup.Wait()
	if predecessor != nil {
		<-predecessor
	}

	m.mu.Lock()
	delete(m.retiringEpochs, epoch)
	if m.latestReset == done {
		m.latestReset = nil
	}
	close(done)
	m.mu.Unlock()
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
		return m.dialTunnelTarget(dialCtx, t, ch, host, port)
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

// failTunnel evicts a tunnel whose listener stopped. A late accept
// failure cannot remove a row whose mismatch or deletion already claimed it.
func (m *TunnelManager) failTunnel(t *tunnel, reason error) {
	if !m.retireLiveTunnel(t, tunnelRemoving) {
		return
	}
	slog.Error("tunnel listener failed; removing the tunnel",
		"tunnel_id", t.info.ID, "error", reason)
	m.closeAndRemove(t)
}

func (m *TunnelManager) retireLiveTunnel(t *tunnel, state tunnelState) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tunnels[t.info.ID] != t || t.state != tunnelLive || !t.stopAdmission() {
		return false
	}
	t.state = state
	return true
}

func (m *TunnelManager) closeAndRemove(t *tunnel) {
	t.close()
	m.mu.Lock()
	if m.tunnels[t.info.ID] == t {
		delete(m.tunnels, t.info.ID)
	}
	m.mu.Unlock()
}

// stopWithKeyMismatch retains the stopped row so a missed change hint can
// recover through the next list. Delete and reset can supersede the transition
// while it waits for cleanup.
func (m *TunnelManager) stopWithKeyMismatch(t *tunnel, mismatch *TunnelKeyMismatch) {
	if !m.retireLiveTunnel(t, tunnelStoppingForMismatch) {
		return
	}
	t.close()

	m.mu.Lock()
	if m.tunnels[t.info.ID] != t || t.state != tunnelStoppingForMismatch {
		m.mu.Unlock()
		return
	}
	t.info.KeyMismatch = mismatch
	m.mu.Unlock()

	slog.Error("tunnel stopped: the hub announced unexpected worker keys",
		"tunnel_id", t.info.ID,
		"expected_fingerprint", mismatch.ExpectedFingerprint,
		"presented_fingerprint", mismatch.PresentedFingerprint)
	if m.onChanged != nil {
		m.onChanged()
	}
}

// acceptPortForward accepts TCP connections and forwards them to a fixed target.
func (m *TunnelManager) acceptPortForward(ctx context.Context, t *tunnel, ch *managedChannel) {
	listener := tunnelListener{Listener: t.listener, tunnel: t, ctx: ctx}
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				m.failTunnel(t, err)
			}
			return
		}
		go m.handlePortForward(ctx, conn, t, ch)
	}
}

// handlePortForward relays a local connection already owned by its tunnel.
func (m *TunnelManager) handlePortForward(ctx context.Context, local net.Conn, t *tunnel, ch *managedChannel) {
	defer func() { _ = local.Close() }()
	remote, err := m.dialTunnelTarget(ctx, t, ch, t.info.TargetAddr, uint32(t.info.TargetPort))
	if err != nil {
		slog.Error("dial tunnel failed", "tunnel_id", t.info.ID, "error", err)
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

func (m *TunnelManager) dialTunnelTarget(ctx context.Context, t *tunnel, ch *managedChannel, host string, port uint32) (net.Conn, error) {
	live, err := m.pool.live(ctx, ch, t.admittedEpoch, t.hubURL, t.info.WorkerID, t.expect)
	if err != nil {
		// Claim a mismatch before starting a target handoff. Its cleanup
		// must be able to wait for all outstanding handoffs.
		if mismatch := keyMismatchOf(err); mismatch != nil {
			m.stopWithKeyMismatch(t, mismatch)
		}
		return nil, fmt.Errorf("resolve tunnel channel: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !t.beginHandoff() {
		return nil, net.ErrClosed
	}
	defer t.handoffs.Done()

	openCtx, cancelOpen := context.WithTimeout(ctx, dialTunnelOpenTimeout)
	target, err := m.dial(openCtx, live, host, port)
	cancelOpen()
	if err != nil {
		if target != nil {
			_ = target.Close()
		}
		return nil, err
	}
	owned, ok := t.ownConnection(target)
	if !ok {
		_ = target.Close()
		return nil, net.ErrClosed
	}
	return owned, nil
}

// beginHandoff reserves ownership for an accept or dial whose socket has
// yet to be registered. Retirement waits for every reservation to finish.
func (t *tunnel) beginHandoff() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retiring {
		return false
	}
	t.handoffs.Add(1)
	return true
}

func (t *tunnel) stopAdmission() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retiring {
		return false
	}
	t.retiring = true
	return true
}

func (t *tunnel) ownConnection(conn net.Conn) (*ownedTunnelConn, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retiring {
		return nil, false
	}
	if t.connections == nil {
		t.connections = make(map[*ownedTunnelConn]struct{})
	}
	owned := &ownedTunnelConn{Conn: conn, tunnel: t, closeDone: make(chan struct{})}
	t.connections[owned] = struct{}{}
	return owned, true
}

func (t *tunnel) removeConnection(conn *ownedTunnelConn) {
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
	t.mu.Lock()
	if done := t.closeDone; done != nil {
		t.mu.Unlock()
		<-done
		return
	}
	t.retiring = true
	done := make(chan struct{})
	t.closeDone = done
	connections := make([]*ownedTunnelConn, 0, len(t.connections))
	for conn := range t.connections {
		connections = append(connections, conn)
	}
	t.mu.Unlock()

	if t.cancel != nil {
		t.cancel()
	}
	if t.listener != nil {
		_ = t.listener.Close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	// A pending accept or dial either registered before retirement or
	// closes its socket before it gives up the reservation.
	t.handoffs.Wait()
	close(done)
}
