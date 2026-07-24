package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/tunnelflow"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"google.golang.org/protobuf/proto"
)

// tunnelConn tracks a single tunnel connection to a remote target.
type tunnelConn struct {
	conn   net.Conn
	sender channel.ResponseWriter
	closed atomic.Bool

	// stopCtxWatch detaches the single ctx->close watcher registered in
	// newTunnelConn (context.AfterFunc's stop func); close() calls it so the
	// watcher (and the tunnelConn it closes over) is released when the conn
	// closes for any reason, not only on ctx cancellation.
	stopCtxWatch func() bool

	// writeGate serializes the conn's writes into the client's send order. The
	// client assigns contiguous 0-based seqs under its write lock and the channel
	// delivers reliably in order, so the S-1 frame's goroutine (launched first)
	// always advances the gate. See writeSeqGate.
	writeGate *writeSeqGate

	// credit is the conn's read-send window: the read loop consumes one per inbound
	// data frame and blocks at zero. See tunnelflow.Window.
	credit *tunnelflow.Window
}

// tunnelflow.MaxWriteSeqLookahead (the write-gate NAK bound writeSeqGate.waitTurn enforces) and
// tunnelflow.InitialReadWindow (the read-send credit a conn self-seeds) are the worker's
// halves of the tunnel flow-control windows. They live in the shared tunnelflow
// package, which pairs them with the client-side halves (WriteWindowFrames,
// ReadBufFrames, ReadCreditBatch) and enforces the cross-package invariants at
// compile time (see tunnelflow's doc comment). Referenced by canonical name at
// the call sites below so the type (uint64 for seqs, int64 for credit) is the
// use site's own.

func newTunnelConn(mgr *tunnelManager, connID string, conn net.Conn, sender channel.ResponseWriter, ctx context.Context) *tunnelConn {
	tc := &tunnelConn{
		conn:      conn,
		sender:    sender,
		writeGate: newWriteSeqGate(),
		credit:    tunnelflow.NewWindow(tunnelflow.InitialReadWindow),
	}
	// One per-conn watcher closes the conn AND evicts it from the manager when
	// its lifetime context is cancelled (channel/session teardown), unblocking a
	// stuck target read or write. writeData therefore needs no per-frame deadline
	// watcher, and the read loop no per-loop one -- both are this single AfterFunc.
	//
	// Eviction matters because the target-half-close path (tunnelReadLoop's
	// io.EOF branch) returns WITHOUT evicting, to keep the conn open for the
	// client's remaining writes. Its readLoop has already exited, so on session
	// death this watcher is the only reclaimer: closing the socket alone would
	// leak the entry in the worker-lifetime m.conns map. removeIf is a
	// CompareAndDelete, so it is a harmless no-op when another path already
	// evicted the conn. A nil mgr (focused unit tests) falls back to a bare close.
	if ctx != nil {
		tc.stopCtxWatch = context.AfterFunc(ctx, func() {
			if mgr != nil {
				mgr.closeAndRemove(connID, tc)
			} else {
				tc.close()
			}
		})
	}
	return tc
}

// tunnelManager tracks active tunnel connections for a worker.
type tunnelManager struct {
	mu      sync.Mutex
	conns   sync.Map                      // conn_id -> *tunnelConn (lock-free: read by the target read loop)
	opening map[string]context.CancelFunc // conn_id -> dial cancel (aborts the in-flight dial); mu-owned
	// canceled fences a close that arrived before (or during) its open, so a racing
	// beginOpen/store drops the conn the client already gave up on. mu-owned; see
	// cancelMarkers for its expiry rule.
	canceled cancelMarkers
}

// newTunnelManager creates a new tunnelManager.
func newTunnelManager() *tunnelManager {
	return &tunnelManager{
		opening:  make(map[string]context.CancelFunc),
		canceled: newCancelMarkers(),
	}
}

func (m *tunnelManager) get(connID string) *tunnelConn {
	v, ok := m.conns.Load(connID)
	if !ok {
		return nil
	}
	return v.(*tunnelConn)
}

func (m *tunnelManager) store(connID string, tc *tunnelConn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.opening, connID)
	if m.canceled.take(connID) {
		// A close won the race while the dial was completing; the client already
		// gave up, so drop the freshly-opened conn.
		tc.close()
		return false
	}
	m.conns.Store(connID, tc)
	return true
}

func (m *tunnelManager) beginOpen(connID string, dialCancel context.CancelFunc) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.canceled.sweep(time.Now())
	if m.canceled.take(connID) {
		// A close already arrived for this conn_id before its open was processed.
		return false
	}
	if _, loaded := m.opening[connID]; loaded {
		return false
	}
	m.opening[connID] = dialCancel
	if _, loaded := m.conns.Load(connID); loaded {
		delete(m.opening, connID)
		return false
	}
	return true
}

func (m *tunnelManager) abortOpen(connID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.opening, connID)
	// The open finished (success or failure); a cancel marker set during the
	// open is no longer needed and can be cleared without a timer.
	m.canceled.clear(connID)
}

func (m *tunnelManager) cancel(connID string) *tunnelConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.canceled.sweep(now)
	if tc := m.remove(connID); tc != nil {
		return tc
	}
	// Record the close so a racing beginOpen/store drops the conn, and abort an
	// in-flight dial immediately (causal cancel) rather than waiting for the
	// dial timeout. The marker is cleared when an open finishes (store/abort)
	// or when beginOpen rejects a close-before-open.
	//
	// Only a marker with NO in-flight open needs a sweep deadline; one racing an
	// open is cleared by the store/abortOpen that ends it. See cancelMarkers.mark
	// and markDuringOpen.
	dialCancel, opening := m.opening[connID]
	if opening {
		m.canceled.markDuringOpen(connID, now.Add(staleCancelMarkerTTL))
		delete(m.opening, connID)
		if dialCancel != nil {
			dialCancel()
		}
		return nil
	}
	m.canceled.mark(connID, now.Add(staleCancelMarkerTTL))
	return nil
}

func (m *tunnelManager) remove(connID string) *tunnelConn {
	v, ok := m.conns.LoadAndDelete(connID)
	if !ok {
		return nil
	}
	return v.(*tunnelConn)
}

func (m *tunnelManager) removeIf(connID string, tc *tunnelConn) {
	m.conns.CompareAndDelete(connID, tc)
}

// closeAndRemove closes tc and evicts it from the manager. Unlike gating the
// eviction on close()'s return, it removes the entry even when another closer
// -- the per-conn lifetime watcher firing on session/channel death mid-open --
// already closed tc. tunnelReadLoop never starts on the open-response-failure
// path, so this is the only thing that evicts tc there; skipping removeIf when
// the watcher won the close race leaked the dead conn in m.conns for the
// process lifetime. It is also the per-conn lifetime watcher's teardown
// (newTunnelConn), so a conn parked in the target-half-close state -- read loop
// exited, no client CloseTunnelConn yet -- is evicted on session death rather
// than leaking in the worker-lifetime map.
func (m *tunnelManager) closeAndRemove(connID string, tc *tunnelConn) {
	tc.close()
	m.removeIf(connID, tc)
}

const (
	// tunnelReadBufSize is the read buffer size for reading from the target
	// connection. It is DERIVED from the frame bound rather than spelled as its
	// own literal: tunnelReadLoop sends buf[:n] as a single TunnelConnEvent, so a
	// buffer larger than tunnelflow.MaxChunkBytes would emit a frame the client's receiver
	// rejects as oversize -- killing every port-forward/SOCKS5 conn on its first
	// non-trivial download. Sizing it to the bound makes "one target Read never
	// overflows one frame" true by construction, so retuning tunnelflow.MaxChunkBytes
	// cannot leave this behind.
	tunnelReadBufSize = tunnelflow.MaxChunkBytes

	// tunnelDialTimeout bounds how long OpenTunnelConn waits for the target TCP connection.
	tunnelDialTimeout = 30 * time.Second
)

// gracefulCloseTimeout bounds how long a client CloseTunnelConn waits to flush
// pending writes before force-closing the target. A target that stopped
// draining leaves a lower-seq write stuck in writeData, so an unbounded flush
// would wedge the close handler goroutine until the whole channel tears down.
// It is a var so tests can shorten it.
var gracefulCloseTimeout = 5 * time.Second

// staleCancelMarkerTTL bounds a cancel marker left for a conn with no in-flight
// open (see tunnelManager.cancel). The marker only has to outlast an open that
// is still dispatching -- and conn_ids are unique per open, so it fences a
// beginOpen racing with a close for the SAME id, which can only happen within
// the ordered WebSocket dispatch window (sub-millisecond). 5s is ~1000x that
// window, so it is generous. Expired markers are GC'd opportunistically by
// sweepCanceledLocked on the next cancel/beginOpen rather than by a per-marker
// timer, so the connection-churn case (a browser or `git fetch` opening many
// short-lived SOCKS5/port-forward conns) does not spawn a runtime timer per
// closed conn. It is a var (not a const) so tests can shorten the sweep window.
var staleCancelMarkerTTL time.Duration = 5 * time.Second

// connRequest constrains a tunnel request message to the shape every tunnel
// handler's prelude needs: a proto.Message pointer that names a conn. The *T
// element lets registerConnHandler allocate the zero value itself, so a handler
// never restates the unmarshal.
type connRequest[T any] interface {
	*T
	proto.Message
	GetConnId() string
}

// registerConnHandler registers a tunnel handler behind the conn_id prelude every
// one of them shares: unmarshal, then refuse an empty conn_id.
//
// It exists for the same reason ownerOnlyRegistrar does (see service.go): the
// prelude was previously hand-written per handler, so the conn_id contract was a
// line each author had to remember and a fifth handler could silently omit --
// admitting an empty conn_id that would then key the manager's maps. Registering
// through this makes the contract a property of WHERE the handler is registered.
// fn receives a request whose conn_id is already known non-empty.
func registerConnHandler[T any, PT connRequest[T]](
	d ownerOnlyRegistrar,
	method string,
	fn func(ctx context.Context, userID userid.UserID, r PT, sender channel.ResponseWriter),
) {
	d.Register(method, func(ctx context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var msg T
		r := PT(&msg)
		if err := unmarshalRequest(req, r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		if r.GetConnId() == "" {
			sendInvalidArgument(sender, "conn_id is required")
			return
		}
		fn(ctx, userID, r, sender)
	})
}

// registerTunnelHandlers registers all tunnel-related inner RPC handlers.
//
// Tunnels are machine-scoped -- they open arbitrary TCP out of the host -- so this
// family takes an ownerOnlyRegistrar and is gated by WHERE it registers, exactly as
// file/git/sysinfo are. It previously took the raw dispatcher and repeated
// requireWorkerOwner in each handler, which a fifth handler could silently omit.
//
// The one manager built here is worker-lifetime and shared across every channel
// session; each handler is a method on it (see registerConnHandler for the prelude
// they share).
func registerTunnelHandlers(d ownerOnlyRegistrar) {
	tunnels := newTunnelManager()
	registerConnHandler(d, "OpenTunnelConn", tunnels.openConn)
	registerConnHandler(d, "SendTunnelData", tunnels.sendData)
	registerConnHandler(d, "CloseTunnelConn", tunnels.closeConn)
	registerConnHandler(d, "GrantTunnelReadCredit", tunnels.grantReadCredit)
}

// openConn dials the target address and starts streaming data back.
func (m *tunnelManager) openConn(ctx context.Context, userID userid.UserID, r *leapmuxv1.OpenTunnelConnRequest, sender channel.ResponseWriter) {
	connID := r.GetConnId()
	dialCtx, dialCancel := context.WithCancel(ctx)
	if !m.beginOpen(connID, dialCancel) {
		dialCancel()
		sendInvalidArgument(sender, "conn_id is canceled or already in use")
		return
	}
	defer func() {
		m.abortOpen(connID)
		dialCancel()
	}()

	targetAddr := r.GetTargetAddr()
	targetPort := r.GetTargetPort()
	if targetAddr == "" {
		sendInvalidArgument(sender, "target_addr is required")
		return
	}
	if targetPort == 0 || targetPort > 65535 {
		sendInvalidArgument(sender, "target_port must be 1-65535")
		return
	}

	addr := net.JoinHostPort(targetAddr, fmt.Sprintf("%d", targetPort))

	dialer := net.Dialer{Timeout: tunnelDialTimeout}
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		slog.Error("failed to dial tunnel target", "addr", addr, "error", err)
		sendInternalError(sender, fmt.Sprintf("dial %s: %v", addr, err))
		return
	}

	tc := newTunnelConn(m, connID, conn, sender, ctx)
	if !m.store(connID, tc) {
		// cancel() won the race while the dial was completing; store already
		// closed tc, so do not announce or stream for it.
		return
	}

	slog.Info("tunnel connection opened", "conn_id", connID, "target", addr, "user_id", userID)

	if err := sendTunnelOpenResponse(sender, connID); err != nil {
		// Close and evict tc unconditionally: the per-conn watcher may have
		// already closed it (session/channel death mid-open), and gating the
		// eviction on winning that race leaked the dead conn in m.conns.
		m.closeAndRemove(connID, tc)
		return
	}

	// Start reading from the target in a goroutine and stream data back.
	go tunnelReadLoop(m, connID, tc)
}

// sendData writes data to the target connection.
func (m *tunnelManager) sendData(ctx context.Context, _ userid.UserID, r *leapmuxv1.SendTunnelDataRequest, sender channel.ResponseWriter) {
	connID := r.GetConnId()

	tc := m.get(connID)
	if tc == nil {
		sendNotFoundError(sender, "tunnel connection not found: "+connID)
		return
	}

	if tc.closed.Load() {
		sendNotFoundError(sender, "tunnel connection closed: "+connID)
		return
	}

	// Always route through writeData: every SendTunnelData carries a
	// sequence and must advance the per-conn write gate in order, even a
	// bare close_write frame that writes no bytes -- and even one this handler
	// will NAK for breaching the chunk bound, which writeData enforces inside
	// the gate for exactly that reason (see errChunkTooLarge).
	if err := tc.writeData(ctx, r.GetSeq(), r.GetData(), r.GetCloseWrite()); err != nil {
		classifyTunnelWriteError(err, connID, r.GetSeq(), sender)
		return
	}

	sendProtoResponse(sender, &leapmuxv1.SendTunnelDataResponse{})
}

// classifyTunnelWriteError turns a writeData error into the response + log level
// the SendTunnelData handler owes the client. It is its own function so the
// classification is unit-testable without orchestrating the narrow teardown-race
// window that produces net.ErrClosed (a conn closed BETWEEN sendData's closed.Load
// check and writeData's waitTurn -- not reachable through a normal CloseTunnelConn,
// which sets closed.Load() first and short-circuits to NotFound before writeData
// runs).
//
// A client protocol violation is not a worker fault: report it as such so the
// client can tell "you sent a bad frame" from "your target write failed", and so a
// misbehaving peer cannot amplify Error-level operator logs. An oversize frame is
// InvalidArgument; a rejected write seq (replay / duplicate / beyond lookahead) is
// FailedPrecondition -- the NAK the write gate promises. A genuine conn-teardown
// race stays internal per the writeSeqGate doc, but is EXPECTED on any tunnel whose
// channels churn (reconnects, credential rotations, pool evictions), so it logs at
// Debug rather than spamming the operator log per racing frame.
func classifyTunnelWriteError(err error, connID string, seq uint64, sender channel.ResponseWriter) {
	switch {
	case errors.Is(err, errChunkTooLarge):
		sendInvalidArgument(sender, err.Error())
	case errors.Is(err, errWriteSeqRejected):
		slog.Debug("rejected out-of-order tunnel write seq", "conn_id", connID, "seq", seq)
		sendFailedPrecondition(sender, err.Error())
	case errors.Is(err, net.ErrClosed):
		// The writeSeqGate doc promises a teardown race stays internal rather than
		// reading as a client protocol violation; log at Debug so a busy endpoint
		// under churn stays quiet, and surface the internal code the doc promises.
		// The caller's pending read on the conn returns the terminal teardown error
		// regardless.
		slog.Debug("tunnel write raced conn teardown", "conn_id", connID, "seq", seq)
		sendInternalError(sender, "write: conn closed")
	default:
		slog.Error("failed to write to tunnel target", "conn_id", connID, "error", err)
		sendInternalError(sender, fmt.Sprintf("write: %v", err))
	}
}

// closeConn closes a tunnel connection.
func (m *tunnelManager) closeConn(ctx context.Context, _ userid.UserID, r *leapmuxv1.CloseTunnelConnRequest, sender channel.ResponseWriter) {
	connID := r.GetConnId()

	// Graceful close: flush pending writes before tearing down, so a client
	// that writes then closes does not lose the tail. Wait for the close's
	// seq turn (every lower-seq data/half-close frame applied) on the live
	// conn before removing it. A conn that closes underneath us returns early.
	// The wait is bounded by gracefulCloseTimeout so a target that stopped
	// draining (leaving a lower-seq write stuck) cannot wedge this close --
	// and its handler goroutine -- until the whole channel tears down; on
	// timeout the flush is abandoned and the force-close below reclaims the
	// stuck write by closing the target.
	if pending := m.get(connID); pending != nil {
		waitCtx, cancelWait := context.WithTimeout(ctx, gracefulCloseTimeout)
		pending.writeGate.waitReached(waitCtx, r.GetSeq())
		cancelWait()
	}

	tc := m.cancel(connID)
	if tc == nil {
		sendProtoResponse(sender, &leapmuxv1.CloseTunnelConnResponse{})
		return
	}

	tc.close()

	slog.Info("tunnel connection closed by client", "conn_id", connID)
	sendProtoResponse(sender, &leapmuxv1.CloseTunnelConnResponse{})
}

// grantReadCredit replenishes a conn's read-send credit so the read loop can send
// more inbound frames (read flow control).
func (m *tunnelManager) grantReadCredit(_ context.Context, _ userid.UserID, r *leapmuxv1.GrantTunnelReadCreditRequest, sender channel.ResponseWriter) {
	// A grant for an already-gone conn is a benign race (the read loop ended
	// and removed it): ack without error so the client does not treat it as a
	// failure.
	if tc := m.get(r.GetConnId()); tc != nil {
		tc.credit.Grant(r.GetCredit())
	}
	sendProtoResponse(sender, &leapmuxv1.GrantTunnelReadCreditResponse{})
}

// errChunkTooLarge marks a frame whose payload breaches the per-frame chunk bound.
// It is a sentinel so sendData can NAK the client with InvalidArgument rather than
// reporting a protocol violation as a worker-side INTERNAL fault.
var errChunkTooLarge = errors.New("tunnel data frame exceeds the chunk bound")

// errWriteSeqRejected marks a write seq no correct client could have sent (a replay,
// a concurrent duplicate, or one beyond the lookahead window). Like errChunkTooLarge
// it is a sentinel so sendData NAKs the client (FailedPrecondition) rather than
// reporting a client protocol violation as a worker-side INTERNAL fault -- the NAK
// the write gate's doc promises. A genuine conn-teardown race stays net.ErrClosed.
var errWriteSeqRejected = errors.New("tunnel write seq is one no correct client could send")

func (tc *tunnelConn) writeData(ctx context.Context, seq uint64, data []byte, closeWrite bool) error {
	switch tc.writeGate.waitTurn(seq) {
	case writeTurnClosed:
		return net.ErrClosed
	case writeTurnRejected:
		return errWriteSeqRejected
	}
	defer tc.writeGate.advance()
	// Enforce the chunk bound the send window is denominated in. Conn.Write splits
	// at MaxChunkBytes, which is what makes the window a BYTE bound
	// (WriteWindowFrames * MaxChunkBytes) rather than a frame-count bound -- but
	// that is the SENDER's convention, and the worker must not trust the sender. A
	// client that does not split (a buggy or hostile one) is otherwise bounded only
	// by the channel's inner-message limit, and the write gate parks up to
	// tunnelflow.MaxWriteSeqLookahead in-flight seqs per conn, so the ceiling an unchecked
	// worker admits is 256 x that limit -- over 4 GiB of buffered payload, per conn.
	//
	// It lives HERE, after the write-turn wait and under the writeGate.advance defer,
	// rather than in the handler ahead of the gate: seqs are contiguous, so a frame
	// rejected before it takes its turn never advances the gate, and every
	// subsequent seq on that conn parks forever waiting for a turn that cannot
	// come -- one wedged conn per oversize frame, recoverable only by burning a
	// full gracefulCloseTimeout or tearing down the session. Rejecting inside the
	// gate NAKs the frame AND releases seq+1, and keeps one place owning the bound.
	if len(data) > tunnelflow.MaxChunkBytes {
		return fmt.Errorf("%w: frame is %d bytes, exceeding the %d-byte chunk bound",
			errChunkTooLarge, len(data), tunnelflow.MaxChunkBytes)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// No per-frame deadline watcher: a cancelled lifetime context closes the conn
	// via the single per-conn watcher (newTunnelConn), which interrupts a stuck
	// target write. This keeps the ~32 KiB write hot path free of a per-frame
	// context.AfterFunc + channel allocation.
	for len(data) > 0 {
		n, err := tc.conn.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
		data = data[n:]
	}
	if closeWrite {
		// Half-close the target's write side AFTER this conn's data, forwarding
		// the client's read-EOF to the target. The write gate (writeSeqGate.waitTurn)
		// already serialized this turn behind every lower-seq data frame, and
		// the data write above runs first within the turn, so the half-close
		// observes the same ordering as the byte stream it ends. The target is
		// always a *net.TCPConn (dialed "tcp"), which supports it.
		if cw, ok := tc.conn.(interface{ CloseWrite() error }); ok {
			return cw.CloseWrite()
		}
	}
	return nil
}

// close closes the target connection exactly once, reporting whether this call
// won the race (and therefore performed the close). Centralizes the
// CompareAndSwap-then-Close idiom so every teardown site owns the invariant on
// the type instead of re-spelling it.
func (tc *tunnelConn) close() bool {
	if tc.closed.CompareAndSwap(false, true) {
		_ = tc.conn.Close()
		// Detach the ctx watcher so it (and this tunnelConn) is released now that
		// the conn is closed. Safe when close() is itself invoked by the watcher:
		// context.AfterFunc's stop is a no-op once the callback has started.
		if tc.stopCtxWatch != nil {
			tc.stopCtxWatch()
		}
		// Wake everything parked on this conn's flow control so teardown never
		// strands a goroutine: a write (or the graceful close) waiting for a seq
		// turn that will not advance, and the read loop waiting for credit that
		// will not be granted. Each subsystem owns its own wake-up, so adding a
		// third cannot be silently left out of this list.
		tc.writeGate.close()
		tc.credit.Close()
		return true
	}
	return false
}

func sendTunnelOpenResponse(sender channel.ResponseWriter, connID string) error {
	payload, err := proto.Marshal(&leapmuxv1.OpenTunnelConnResponse{ConnId: connID})
	if err != nil {
		return fmt.Errorf("marshal open tunnel response: %w", err)
	}
	if err := sender.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: payload}); err != nil {
		return fmt.Errorf("send open tunnel response: %w", err)
	}
	return nil
}

// tunnelReadLoop reads data from the target connection and sends TunnelConnEvent
// stream messages back to the caller.
func tunnelReadLoop(mgr *tunnelManager, connID string, tc *tunnelConn) {
	// The conn is closed on lifetime-context cancellation by the single per-conn
	// watcher in newTunnelConn, which unblocks the target Read below -- no
	// separate per-loop AfterFunc is needed here.
	buf := make([]byte, tunnelReadBufSize)
	for {
		n, err := tc.conn.Read(buf)
		if n > 0 {
			// Read flow control: block for a client credit before sending a data
			// frame, so the worker never buffers more inbound frames than the
			// client can hold. A client that stops reading stops granting credit,
			// which stalls this loop and, in turn, backpressures the target's
			// send buffer -- rather than letting one stalled tunnel consumer fill
			// the shared channel and starve every other stream on it.
			if err := tc.credit.Acquire(context.Background()); err != nil {
				break // conn closed while waiting for credit
			}
			// buf[:n] is handed to sendTunnelEvent directly -- no defensive copy.
			// sendTunnelEvent calls proto.Marshal synchronously, which copies Data
			// into its own wire payload before returning; nothing downstream retains
			// buf[:n] (SendStream carries the marshaled payload, not this slice), so
			// the next tc.conn.Read reusing buf cannot corrupt an in-flight frame.
			if err := sendTunnelEvent(tc.sender, &leapmuxv1.TunnelConnEvent{
				ConnId: connID,
				Data:   buf[:n],
			}); err != nil {
				break
			}
		}
		if err != nil {
			if tc.closed.Load() {
				// Connection was closed by the client side; no need to send EOF.
				break
			}
			if err == io.EOF {
				// The target half-closed its write side (sent a FIN); it can still
				// RECEIVE. Forward the read-EOF to the client, then stop this read
				// goroutine WITHOUT full-closing the conn: the client->target
				// direction may still be uploading, and closing the target socket
				// (or evicting the conn) here would NAK those in-flight writes and
				// truncate the upload -- defeating the target->client half of the
				// half-close the port-forward/SOCKS5 copy propagates. The conn
				// stays registered and open for writes; the client's graceful
				// CloseTunnelConn (which now flushes the pending upload) or the
				// per-conn lifetime watcher reclaims it. If the target's read side
				// is also gone (a full close, not a half-close), the next
				// writeData fails and NAKs the client naturally, the way TCP
				// surfaces EPIPE.
				if sendErr := sendTunnelEvent(tc.sender, &leapmuxv1.TunnelConnEvent{
					ConnId: connID,
					Eof:    true,
				}); sendErr != nil {
					// Relaying the EOF failed. The client's Conn.Read parks on this
					// event with no read deadline (io.Copy sets none), so a lost
					// EOF would surface the target's clean half-close as a silent
					// hang lasting the channel's lifetime -- a port-forward copy
					// observing a completed HTTP-1.0 response as a truncation. A
					// failed send here means the channel itself is failing (a
					// coder/websocket write does not recover), so fall through to
					// the full close + evict below rather than `return`ing with
					// the conn still registered. This sacrifices the upload
					// direction, but a channel that cannot relay an EOF cannot
					// relay the upload's writes either, so the client's next
					// writeData fails and surfaces the loss promptly instead of
					// wedging.
					slog.Warn("failed to relay target half-close EOF; closing tunnel conn",
						"conn_id", connID, "error", sendErr)
					break
				}
				slog.Info("tunnel read loop ended on target half-close", "conn_id", connID)
				return
			}
			slog.Error("tunnel target read error", "conn_id", connID, "error", err)
			if sendErr := sendTunnelEvent(tc.sender, &leapmuxv1.TunnelConnEvent{
				ConnId: connID,
				Error:  err.Error(),
			}); sendErr != nil {
				slog.Warn("failed to relay target read error; closing tunnel conn",
					"conn_id", connID, "read_error", err, "send_error", sendErr)
			}
			break
		}
	}

	// Clean up: close the connection and remove from manager. Reached on a target
	// read ERROR (target gone), when the conn was already closed (client close or
	// lifetime-watcher cancellation), or when the target half-close's EOF could not
	// be relayed -- all mean the conn is dead to the client, so full-close and
	// evict. A SUCCESSFUL target half-close (io.EOF) returns above without this so
	// the client->target write direction survives.
	tc.close()
	mgr.removeIf(connID, tc)
	slog.Info("tunnel read loop ended", "conn_id", connID)
}

// sendTunnelEvent sends a TunnelConnEvent as a stream message.
func sendTunnelEvent(sender channel.ResponseWriter, event *leapmuxv1.TunnelConnEvent) error {
	payload, err := proto.Marshal(event)
	if err != nil {
		slog.Error("failed to marshal tunnel event", "error", err)
		return err
	}
	return sender.SendStream(&leapmuxv1.InnerStreamMessage{
		Payload: payload,
	})
}
