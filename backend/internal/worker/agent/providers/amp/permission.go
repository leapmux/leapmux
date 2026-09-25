package amp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The permission bridge.
//
// Amp asks no permission in stream-JSON mode. Its one route to a decision is
// the `delegate` action of a permission rule: for each tool call that its local
// executor runs, Amp starts a program, writes the call's input to its stdin,
// and reads the exit code -- 0 allows, 2 or more rejects, and the stderr text
// of a refusal reaches the model as the reason.
//
// The worker's own executable is that program (see helper.go). It connects to
// this bridge -- a Unix domain socket in the agent's private directory -- and
// sends two lines: the agent's secret, then the request. The agent decides the
// request from its current permission mode, and the bridge writes one decision
// line back. A request that the agent answers with a banner blocks for as long
// as the user needs, and Amp waits for the program for the same time.
//
// The socket's directory is readable by the owner alone, so no other user can
// connect, and no other user can put a socket of their own at the path that a
// helper dials. The secret is a second check. The bridge reads it on a short
// first line, so a connection that does not state it costs the bridge a few
// hundred bytes and a few seconds at most.
//
// The bridge answers every request it holds when the turn ends, when the
// process exits, when the user interrupts, and when the agent stops, so no
// helper -- and so no Amp tool call -- waits for an answer that can no longer
// come. A helper that dies first withdraws its banner.

// helperRequest is the line a helper sends after its secret line.
type helperRequest struct {
	// Tool is the tool that Amp asks about, from AGENT_TOOL_NAME.
	Tool string `json:"tool"`
	// Thread is the thread of the call, from AMP_THREAD_ID.
	Thread string `json:"thread"`
	// Input is the call's input, as Amp wrote it to the helper's stdin.
	Input json.RawMessage `json:"input"`
}

// helperDecision is the one line the bridge answers with.
type helperDecision struct {
	Decision string `json:"decision"`
	// Message is the reason for a refusal. The helper writes it to stderr, where
	// Amp hands it to the model. An empty one refuses with Amp's own wording.
	Message string `json:"message,omitempty"`
}

const (
	decisionAllow  = "allow"
	decisionReject = "reject"
)

func allowDecision() helperDecision { return helperDecision{Decision: decisionAllow} }

func rejectDecision(message string) helperDecision {
	return helperDecision{Decision: decisionReject, Message: message}
}

// bridgeNetwork is the network of the bridge's listener. Go supports Unix
// domain sockets on Windows 10 1803 and later too.
const bridgeNetwork = "unix"

// bridgeSocketName is the bridge's socket inside the agent's directory.
const bridgeSocketName = "bridge.sock"

// maxSecretLineBytes caps the first line of a connection, which holds the
// secret alone.
const maxSecretLineBytes = 256

// secretReadTimeout limits the wait for the secret line. A helper writes it
// the moment it connects.
const secretReadTimeout = 5 * time.Second

// maxUnauthenticatedConns caps the connections that did not state the secret
// yet. The bridge closes each one past the cap at once. A helper states its
// secret at once, so only a stranger stays under the cap for long.
const maxUnauthenticatedConns = 16

// maxHelperMessageBytes caps one line on the bridge. A request carries a tool
// call's whole input, and an apply_patch call can hold a large patch, but Amp
// itself refuses a model turn far below this.
const maxHelperMessageBytes = 32 << 20

// requestReadTimeout limits the wait for a helper's request line, after the
// secret line. A helper writes both lines the moment it connects.
const requestReadTimeout = 30 * time.Second

// pendingPermission is one request that waits for the user.
//
// The waiter publishes the banner AFTER it registers the request, so a refusal
// can arrive before the banner exists. Exactly one side withdraws the banner:
// the refusal when the banner exists already, and the waiter when the refusal
// came first (see markPublished). The bridge's mu guards both flags.
type pendingPermission struct {
	answer chan helperDecision
	// published is true once the request's banner exists.
	published bool
	// refused is true once the bridge refused the request.
	refused bool
}

// permissionBridge is the socket listener that serves one agent's helpers.
type permissionBridge struct {
	listener net.Listener
	path     string
	secret   []byte
	// decide answers one request. It blocks while the request waits for the
	// user, and it returns when ctx ends.
	decide func(ctx context.Context, request helperRequest) helperDecision
	// withdraw retracts the banner of a request that no answer can reach.
	withdraw func(requestID string)
	// secretTimeout limits the wait for a connection's secret line. It is
	// secretReadTimeout. A test sets a longer one, so a connection that ends
	// at the deadline cannot pass for one that the bridge ends at once.
	secretTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	nextID atomic.Uint64
	// idPrefix makes request ids unique across the agent's restarts: a stored
	// request from an earlier process must not match a new one.
	idPrefix string

	mu     sync.Mutex
	closed bool
	// conns holds every open connection. A connection maps to true once it
	// stated the secret.
	conns map[net.Conn]bool
	// unauthenticated counts the connections of conns that did not state the
	// secret yet.
	unauthenticated int
	pending         map[string]*pendingPermission
	// noteToolCall closes toolCalls and replaces it whenever a tool call
	// appears on stdout, so a request that waits for its call wakes.
	toolCalls chan struct{}
}

// newPermissionBridge opens the listener at the socket inside dir, which must
// be a directory that only the owner can read. It serves nothing until serve
// runs.
func newPermissionBridge(dir string) (*permissionBridge, error) {
	path := filepath.Join(dir, bridgeSocketName)
	if err := checkSocketPath(path); err != nil {
		return nil, err
	}
	listener, err := net.Listen(bridgeNetwork, path)
	if err != nil {
		return nil, fmt.Errorf("open the Amp permission bridge: %w", err)
	}
	if err := restrictSocket(path); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("restrict the Amp permission bridge: %w", err)
	}
	secret, err := providerkit.NewServerSecret()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	prefix := make([]byte, 4)
	if _, err := rand.Read(prefix); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("generate a permission request prefix: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &permissionBridge{
		listener:      listener,
		path:          path,
		secret:        []byte(secret),
		secretTimeout: secretReadTimeout,
		ctx:           ctx,
		cancel:        cancel,
		idPrefix:      "amp-permission-" + hex.EncodeToString(prefix) + "-",
		conns:         make(map[net.Conn]bool),
		pending:       make(map[string]*pendingPermission),
		toolCalls:     make(chan struct{}),
	}, nil
}

// endpoint is the socket path that a helper dials.
func (b *permissionBridge) endpoint() string { return b.path }

// secretText is the credential a helper sends.
func (b *permissionBridge) secretText() string { return string(b.secret) }

// serve starts accepting helpers.
func (b *permissionBridge) serve(decide func(ctx context.Context, request helperRequest) helperDecision, withdraw func(requestID string)) {
	b.decide = decide
	b.withdraw = withdraw
	b.wg.Add(1)
	go b.acceptLoop()
}

func (b *permissionBridge) acceptLoop() {
	defer b.wg.Done()
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				slog.Warn("amp permission bridge accept failed", "error", err)
			}
			return
		}
		switch b.track(conn) {
		case connTracked:
			b.wg.Add(1)
			go b.handleConn(conn)
		case connOverCap:
			slog.Warn("amp permission bridge closed a connection past the cap of unauthenticated connections")
			_ = conn.Close()
		case connAfterClose:
			_ = conn.Close()
			return
		}
	}
}

// trackResult is the answer of track.
type trackResult int

const (
	connTracked trackResult = iota
	connOverCap
	connAfterClose
)

// track records an open connection that did not state the secret yet, so close
// can end it. It refuses one past the cap, and one that arrives after close.
func (b *permissionBridge) track(conn net.Conn) trackResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return connAfterClose
	}
	if b.unauthenticated >= maxUnauthenticatedConns {
		return connOverCap
	}
	b.conns[conn] = false
	b.unauthenticated++
	return connTracked
}

// authenticate records that conn stated the secret.
func (b *permissionBridge) authenticate(conn net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if authenticated, ok := b.conns[conn]; ok && !authenticated {
		b.conns[conn] = true
		b.unauthenticated--
	}
}

func (b *permissionBridge) untrack(conn net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	authenticated, ok := b.conns[conn]
	if !ok {
		return
	}
	delete(b.conns, conn)
	if !authenticated {
		b.unauthenticated--
	}
}

// handleConn serves one helper: it checks the secret, reads the request, asks
// for a decision and writes it back.
//
// The handler closes a connection that states the wrong secret, or no request
// at all, with no answer, so a stranger learns nothing. It reads nothing past
// the short secret line until the secret matches. A helper that disconnects
// while its request waits cancels the request, which withdraws its banner.
func (b *permissionBridge) handleConn(conn net.Conn) {
	defer b.wg.Done()
	defer b.untrack(conn)
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(b.secretTimeout))
	secret, err := readSecretLine(conn)
	if err != nil {
		slog.Debug("amp permission bridge read of the secret failed", "error", err)
		return
	}
	if subtle.ConstantTimeCompare(secret, b.secret) != 1 {
		slog.Warn("amp permission bridge refused a connection with a wrong secret")
		return
	}
	b.authenticate(conn)

	_ = conn.SetReadDeadline(time.Now().Add(requestReadTimeout))
	reader := bufio.NewReader(io.LimitReader(conn, maxHelperMessageBytes+1))
	request, err := readHelperLine[helperRequest](reader)
	if err != nil {
		slog.Debug("amp permission bridge read failed", "error", err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	go func() {
		// A helper sends nothing after its request, so a read returns only when
		// the helper goes away -- or when this handler closes the connection.
		var one [1]byte
		_, _ = conn.Read(one[:])
		cancel()
	}()

	decision := b.decide(ctx, request)
	encoded, err := json.Marshal(decision)
	if err != nil {
		slog.Error("amp permission bridge encode failed", "error", err)
		return
	}
	if _, err := conn.Write(append(encoded, '\n')); err != nil {
		slog.Debug("amp permission bridge write failed", "error", err)
	}
}

// newRequestID mints the id of one control request. The id is unique, and it
// is not a secret: a random prefix and a counter make it predictable. Only an
// authenticated user of the hub can answer a request, so no code may treat the
// id as a proof of anything.
func (b *permissionBridge) newRequestID() string {
	return fmt.Sprintf("%s%d", b.idPrefix, b.nextID.Add(1))
}

// register holds one request until an answer or a cancellation reaches it. It
// refuses a request after close.
func (b *permissionBridge) register(requestID string) (*pendingPermission, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, false
	}
	pending := &pendingPermission{answer: make(chan helperDecision, 1)}
	b.pending[requestID] = pending
	return pending, true
}

// unregister drops one request that no longer waits.
func (b *permissionBridge) unregister(requestID string) {
	b.mu.Lock()
	delete(b.pending, requestID)
	b.mu.Unlock()
}

// answer delivers the user's decision to one request. It reports false for a
// request that no longer waits.
func (b *permissionBridge) answer(requestID string, decision helperDecision) bool {
	b.mu.Lock()
	pending, ok := b.pending[requestID]
	delete(b.pending, requestID)
	b.mu.Unlock()
	if !ok {
		return false
	}
	pending.answer <- decision
	return true
}

// pendingCount reports how many requests wait for the user.
func (b *permissionBridge) pendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// markPublished records that the banner of one request exists. It reports
// false when the bridge refused the request first. That refusal found no banner
// to withdraw, so the caller withdraws the banner that it just published.
func (b *permissionBridge) markPublished(pending *pendingPermission) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if pending.refused {
		return false
	}
	pending.published = true
	return true
}

// cancelAll refuses every request that waits, with reason, and withdraws each
// banner.
func (b *permissionBridge) cancelAll(reason error) {
	b.mu.Lock()
	taken, published := b.takePendingLocked()
	b.mu.Unlock()
	b.refuse(taken, published, reason)
}

// takePendingLocked removes every waiting request and marks each one refused.
// It returns the requests, and the ids of the banners that exist now. The
// caller holds mu.
func (b *permissionBridge) takePendingLocked() (taken []*pendingPermission, published []string) {
	taken = make([]*pendingPermission, 0, len(b.pending))
	for requestID, pending := range b.pending {
		pending.refused = true
		taken = append(taken, pending)
		if pending.published {
			published = append(published, requestID)
		}
	}
	b.pending = make(map[string]*pendingPermission)
	return taken, published
}

// refuse answers each taken request with reason, and withdraws each banner in
// published. A request whose banner did not exist yet withdraws that banner
// itself once it exists (see markPublished).
func (b *permissionBridge) refuse(taken []*pendingPermission, published []string, reason error) {
	decision := rejectDecision("LeapMux withdrew the permission request: " + reason.Error())
	for _, pending := range taken {
		pending.answer <- decision
	}
	if b.withdraw == nil {
		return
	}
	for _, requestID := range published {
		b.withdraw(requestID)
	}
}

// close refuses every waiting request, ends every connection, and stops the
// listener. It returns once every handler returned. It is idempotent.
//
// It ends each connection that did not state the secret at once: such a
// connection gets no refusal, and it must not delay the agent's stop.
func (b *permissionBridge) close(reason error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	taken, published := b.takePendingLocked()
	conns := make([]net.Conn, 0, len(b.conns))
	for conn, authenticated := range b.conns {
		conns = append(conns, conn)
		if !authenticated {
			_ = conn.Close()
		}
	}
	b.mu.Unlock()

	_ = b.listener.Close()
	// Refuse first, so each waiting handler writes its refusal before its
	// connection closes. The helper then states the reason rather than a
	// broken connection.
	b.refuse(taken, published, reason)
	b.cancel()
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(requestReadTimeout):
		// A handler stuck in a write to a helper that stopped reading: close
		// every connection so the handlers return.
		for _, conn := range conns {
			_ = conn.Close()
		}
		<-done
	}
}

// noteToolCall wakes every request that waits for its tool call to appear.
func (b *permissionBridge) noteToolCall() {
	b.mu.Lock()
	close(b.toolCalls)
	b.toolCalls = make(chan struct{})
	b.mu.Unlock()
}

// toolCallSignal returns the channel that the next tool call closes.
func (b *permissionBridge) toolCallSignal() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.toolCalls
}

// checkSocketPath refuses a socket path that the platform cannot bind, with an
// error that states the limit instead of the bind's own "invalid argument". An
// agent directory leaves room for the socket (agentDirSpec states its name),
// so this refuses only a directory from somewhere else.
func checkSocketPath(path string) error {
	if limit := agentdir.MaxSocketPathBytes(); len(path) > limit {
		return fmt.Errorf("the Amp permission bridge path %s is %d bytes, and a socket path on this platform takes at most %d", path, len(path), limit)
	}
	return nil
}

// readSecretLine reads the first line of a connection, which holds the secret
// alone. It reads one byte at a time, so it takes nothing of the request line
// that follows, and it refuses a line longer than maxSecretLineBytes.
func readSecretLine(conn io.Reader) ([]byte, error) {
	line := make([]byte, 0, 64)
	var one [1]byte
	for {
		if _, err := io.ReadFull(conn, one[:]); err != nil {
			return nil, err
		}
		if one[0] == '\n' {
			return line, nil
		}
		if len(line) == maxSecretLineBytes {
			return nil, fmt.Errorf("the secret line exceeds %d bytes", maxSecretLineBytes)
		}
		line = append(line, one[0])
	}
}

// readHelperLine reads one newline-terminated JSON value.
func readHelperLine[T any](reader *bufio.Reader) (T, error) {
	var value T
	line, err := reader.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > maxHelperMessageBytes {
			return value, fmt.Errorf("the line exceeds %d bytes", maxHelperMessageBytes)
		}
		return value, err
	}
	if len(line) > maxHelperMessageBytes {
		return value, fmt.Errorf("the line exceeds %d bytes", maxHelperMessageBytes)
	}
	if err := json.Unmarshal(line, &value); err != nil {
		return value, fmt.Errorf("decode the line: %w", err)
	}
	return value, nil
}
