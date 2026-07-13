package tunnel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/tunnelflow"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/util/ctxutil"
	"google.golang.org/protobuf/proto"
)

const tunnelOpenTimeout = 30 * time.Second

// The tunnel data path's flow-control window sizes -- these client-side halves
// (Conn's send window and inbound read buffer) plus the worker-side halves --
// live in the shared tunnelflow package, which enforces their cross-package
// invariants at compile time (see tunnelflow's doc comment). Referenced by
// canonical name at the call sites below so a reader lands on the package's
// own definition -- and its invariant doc -- rather than a local alias.

// Conn implements net.Conn over an E2EE tunnel channel connection.
//
// Its state is grouped into concern-specific substructs (connRead, connWrite,
// connDeadlines, connClose, connAddrs) so the cross-concern invariants the
// method docs explain at length -- the write-path's seq/window/error discipline,
// the read-path's buffer/terminal/credit -- read as units rather than one flat
// field list a future addition has no obvious home in. The substructs are
// embedded by value, so their fields are PROMOTED onto Conn and the methods below
// continue to read them as tc.writeMu / tc.writeErr / tc.terminal / ... without a
// per-call-site rewrite of the concurrency path (which would carry regression
// risk disproportionate to the readability gain); a reader chasing a field lands
// on the substruct that owns its concern.
type Conn struct {
	ch     tunnelRPCChannel
	connID string
	reqID  uint64

	connRead
	connWrite
	connDeadlines
	connClose
	connAddrs
}

// connRead holds the inbound half of the conn: the buffered-frame queue Read
// drains, the partial frame spliced across reads, the terminal-condition latch,
// and the read-credit batcher. None of it is touched by the write path.
type connRead struct {
	readMu   sync.Mutex
	readBuf  chan []byte
	readPart []byte
	// terminal latches the inbound stream's end (EOF, a stream error, or channel
	// death). Read selects on its Done and returns its Err.
	terminal latchedErr
	// credit batches the read-credit grants Read accrues and sends them to the
	// worker off the read path. See readCredit.
	credit *readCredit
}

// connWrite holds the outbound half: the write-seq gate, the send-window, the
// close_write latch, and the first-write-error latch. None of it is touched by
// the read path. The writeMu is a ctxutil.Mutex (not sync.Mutex) so the graceful
// close can bound its acquire against a holder parked on the send permit.
type connWrite struct {
	// writeMu serializes the write path (sequence stamping, the send-window
	// acquire, and the frame's turn on the wire). It is a ctxutil.Mutex, not a
	// sync.Mutex, because sendRemoteClose must bound its acquire: the holder can
	// park arbitrarily long on the channel-wide send permit or inside the
	// WebSocket write, neither of which honours tc.closed, and an unbounded wait
	// there means the graceful CloseTunnelConn is never even attempted.
	writeMu  ctxutil.Mutex
	writeSeq uint64 // per-conn monotonic write sequence, guarded by writeMu
	// closeWriteSent latches once the close_write frame is actually on the wire.
	// It is a plain bool under writeMu (which CloseWrite already holds) rather
	// than a sync.Once because Once.Do consumes the once even when its body
	// fails: a CloseWrite whose send failed (an expired deadline, a transient
	// send error) would then report success on a retry while never having
	// emitted the frame, and the target would never see the read-EOF.
	closeWriteSent bool
	// sendWindow is a semaphore of in-use slots (a value in the channel is a
	// slot held by an unacknowledged SendTunnelData). Write blocks acquiring a
	// slot when tunnelflow.WriteWindowFrames are outstanding; awaitWriteAck releases one
	// when the worker's SendTunnelDataResponse arrives.
	sendWindow chan struct{}
	// writeErr latches the first terminal error the worker reports for a
	// SendTunnelData frame -- an IsError SendTunnelDataResponse means the worker's
	// write to the target failed. Once latched, Write and CloseWrite fail with it,
	// surfacing a broken target to the local source the way TCP surfaces EPIPE on
	// a later write, instead of Write reporting success for bytes the worker then
	// silently drops. awaitWriteAck (its own goroutine) publishes it without the
	// write lock, so only its lock-free Err and Done are used -- sendFrameLocked
	// both polls Err (before and after taking a window slot) and selects on Done
	// while parked on a full window, so a NAK latched mid-park aborts the frame
	// instead of letting it be marshalled and sent to a target the worker has
	// already given up on.
	writeErr latchedErr
}

// connDeadlines holds the per-direction read/write deadlines. Each is a
// deadlineState that turns its deadline into a context and re-arms when
// SetReadDeadline/SetWriteDeadline fires after a Write parked on a full window.
type connDeadlines struct {
	readDeadline  deadlineState
	writeDeadline deadlineState
}

// connClose holds the one-shot close gate and the closed channel every read and
// write selects on to observe a torn-down conn.
type connClose struct {
	closeOnce sync.Once
	closed    chan struct{}
}

// connAddrs holds the local and remote addresses reported by LocalAddr and
// RemoteAddr. They are constant after construction.
type connAddrs struct {
	localAddr  net.Addr
	remoteAddr net.Addr
}

type tunnelRPCChannel interface {
	Context() context.Context
	SendRPCNoWait(context.Context, string, []byte, RPCHandlers) (uint64, error)
	UnregisterPending(uint64)
	UnregisterStream(uint64)
}

// DialTunnel opens a tunnel connection with the package default timeout.
func DialTunnel(ch *Channel, targetAddr string, targetPort uint32) (*Conn, error) {
	if ch == nil {
		return nil, fmt.Errorf("dial tunnel: channel is required")
	}
	ctx, cancel := context.WithTimeout(ch.Context(), tunnelOpenTimeout)
	defer cancel()
	return DialTunnelContext(ctx, ch, targetAddr, targetPort)
}

// DialTunnelContext opens a tunnel connection owned by ctx.
func DialTunnelContext(ctx context.Context, ch *Channel, targetAddr string, targetPort uint32) (*Conn, error) {
	if ctx == nil {
		return nil, fmt.Errorf("dial tunnel: operation context is required")
	}
	if ch == nil {
		return nil, fmt.Errorf("dial tunnel: channel is required")
	}
	return dialTunnelContext(ctx, ch, targetAddr, targetPort)
}

func dialTunnelContext(ctx context.Context, ch tunnelRPCChannel, targetAddr string, targetPort uint32) (*Conn, error) {
	connID := id.Generate()
	openPayload, err := proto.Marshal(&leapmuxv1.OpenTunnelConnRequest{
		ConnId:     connID,
		TargetAddr: targetAddr,
		TargetPort: targetPort,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	tc := newConn(ch, connID, targetAddr, targetPort)
	// newConn starts the credit loop, but a conn this function abandons is never
	// returned to a caller and so is never Closed. Release it on EVERY failure
	// exit rather than at each return site: the send-failure path below cannot
	// even use unregister() (tc.reqID is unset there, so it would target reqID 0,
	// which allocateReqIDLocked never hands out -- unregistering it would be a
	// meaningless no-op rather than this conn's cleanup), and a future early return
	// added here would otherwise silently leak a goroutine for the shared channel's
	// whole lifetime.
	// credit.stop is idempotent, so the paths that also unregister are fine.
	opened := false
	defer func() {
		if !opened {
			tc.credit.stop()
		}
	}()
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	reqID, err := ch.SendRPCNoWait(ctx, "OpenTunnelConn", openPayload, RPCHandlers{
		Response: respCh,
		Stream:   tc.onStreamMessage,
	})
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	tc.reqID = reqID

	select {
	case resp := <-respCh:
		ch.UnregisterPending(reqID)
		if err := ctx.Err(); err != nil {
			tc.cancelRemoteOpen()
			return nil, err
		}
		if err := ch.Context().Err(); err != nil {
			tc.cancelRemoteOpen()
			return nil, err
		}
		// No nil arm: recvLoop never delivers nil and respCh is never closed, so a
		// channel torn down mid-open surfaces through the ch.Context().Err() check
		// above rather than as a nil response (see Channel.Close).
		if resp.GetIsError() {
			tc.unregister()
			return nil, fmt.Errorf("rpc error (code %d): %s", resp.GetErrorCode(), resp.GetErrorMessage())
		}
		var openResp leapmuxv1.OpenTunnelConnResponse
		if err := proto.Unmarshal(resp.GetPayload(), &openResp); err != nil {
			tc.cancelRemoteOpen()
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
		if openResp.GetConnId() != connID {
			tc.cancelRemoteOpen()
			return nil, fmt.Errorf("open tunnel conn id mismatch: got %q, want %q", openResp.GetConnId(), connID)
		}
		if err := ctx.Err(); err != nil {
			tc.cancelRemoteOpen()
			return nil, err
		}
		// The conn is now the caller's; its credit loop lives until Close.
		opened = true
		return tc, nil

	case <-ctx.Done():
		tc.cancelRemoteOpen()
		return nil, ctx.Err()

	case <-ch.Context().Done():
		tc.unregister()
		return nil, ch.Context().Err()
	}
}

func newConn(ch tunnelRPCChannel, connID, targetAddr string, targetPort uint32) *Conn {
	tc := &Conn{
		ch:     ch,
		connID: connID,
		connRead: connRead{
			readBuf: make(chan []byte, tunnelflow.ReadBufFrames),
		},
		connWrite: connWrite{
			sendWindow: make(chan struct{}, tunnelflow.WriteWindowFrames),
		},
		connDeadlines: connDeadlines{
			readDeadline:  newDeadlineState(),
			writeDeadline: newDeadlineState(),
		},
		connClose: connClose{
			closed: make(chan struct{}),
		},
		connAddrs: connAddrs{
			localAddr:  &net.TCPAddr{IP: net.IPv4zero},
			remoteAddr: tunnelAddr{address: net.JoinHostPort(targetAddr, fmt.Sprintf("%d", targetPort))},
		},
	}
	tc.credit = newReadCredit(ch.Context(), tunnelflow.ReadCreditBatch, tc.sendReadCredit)
	return tc
}

type tunnelAddr struct{ address string }

func (tunnelAddr) Network() string  { return "tcp" }
func (a tunnelAddr) String() string { return a.address }

func (tc *Conn) cancelRemoteOpen() {
	tc.unregister()
	go tc.sendRemoteClose()
}

// remoteCloseLockBudget bounds how long sendRemoteClose waits to acquire
// writeMu (to order the close after every prior write). The holder can be parked
// on the channel-wide send permit or inside the WebSocket write, neither of
// which honours tc.closed, so an unbounded Lock could block indefinitely. A var
// so tests can shorten it.
var remoteCloseLockBudget = 5 * time.Second

// remoteCloseSendBudget bounds the best-effort CloseTunnelConn send itself.
var remoteCloseSendBudget = 5 * time.Second

// sendRemoteClose tells the worker to tear down its half of the conn.
//
// It tries to order the close after every prior write by reading the write
// sequence under writeMu (so the worker flushes all lower-seq frames first --
// graceful close). If writeMu cannot be acquired in budget, a Write is parked on
// a full send window -- the conn is wedged against a target that stopped
// draining, the graceful flush is hopeless, and abandoning the close would leak
// the worker's tunnelConn (its read loop parks on read credit forever, pinning
// the target TCP conn and a map entry until the whole channel dies -- hours, for
// a pooled channel). So the timeout path sends a best-effort CloseTunnelConn
// with seq 0 instead: the worker force-closes the conn (its waitReached returns
// immediately for seq 0), reclaiming the stuck target and NAKing the parked write
// so the client unparks too. Graceful ordering is lost, but there is nothing
// left to flush on a wedged conn.
//
// The writeMu wedge itself is the deeper bug tracked in
// https://github.com/leapmux/leapmux/issues/276; this forceful close keeps the
// conn from leaking until that wedge is bounded.
func (tc *Conn) sendRemoteClose() {
	seq, ok := tc.readWriteSeqForClose()
	tc.sendCloseTunnelConn(seq, !ok)
}

// readWriteSeqForClose reads the write sequence under writeMu so the close can
// be ordered after every prior write. It returns ok=false when the lock could
// not be acquired in budget, signalling sendRemoteClose to send a forceful
// (seq-0) close instead.
func (tc *Conn) readWriteSeqForClose() (seq uint64, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteCloseLockBudget)
	defer cancel()
	if err := tc.writeMu.Lock(ctx); err != nil {
		return 0, false
	}
	defer tc.writeMu.Unlock()
	return tc.writeSeq, true
}

// sendCloseTunnelConn marshals and sends CloseTunnelConn. forceful reports that
// the close could not be ordered (writeMu timed out), for logging only -- the
// wire message is the same shape, just with seq 0 so the worker force-closes.
func (tc *Conn) sendCloseTunnelConn(seq uint64, forceful bool) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteCloseSendBudget)
	defer cancel()
	payload, err := proto.Marshal(&leapmuxv1.CloseTunnelConnRequest{ConnId: tc.connID, Seq: seq})
	if err != nil {
		slog.Warn("tunnel conn close not sent: marshal failed",
			"conn_id", tc.connID, "error", err)
		return
	}
	// Log rather than discard: a dropped close leaks the worker-side conn, and a
	// silent drop leaves nothing to diagnose it with. There is no retry -- the conn
	// is already gone locally, and the channel's own death reaps the worker side.
	if _, err := tc.ch.SendRPCNoWait(ctx, "CloseTunnelConn", payload, RPCHandlers{}); err != nil {
		if forceful {
			slog.Warn("tunnel conn forceful close not sent",
				"conn_id", tc.connID, "error", err)
		} else {
			slog.Warn("tunnel conn graceful close not sent",
				"conn_id", tc.connID, "error", err)
		}
	}
}

// unregister detaches the conn from its channel: it drops the channel's handlers
// and releases the credit loop. Close routes through it, as does every
// dialTunnelContext failure path that has a reqID to unregister -- those abandon
// a conn that was never returned to a caller and so is never Closed, and
// releasing the credit loop only in Close would leak a goroutine per failed dial for
// the shared channel's whole lifetime. dialTunnelContext additionally guards its
// pre-reqID exits with its own deferred credit.stop (see there).
func (tc *Conn) unregister() {
	tc.credit.stop()
	tc.ch.UnregisterPending(tc.reqID)
	tc.ch.UnregisterStream(tc.reqID)
}

func (tc *Conn) onStreamMessage(msg *leapmuxv1.InnerStreamMessage) {
	select {
	case <-tc.closed:
		return
	case <-tc.terminal.Done():
		return
	default:
	}
	if msg.GetIsError() {
		tc.terminal.Set(fmt.Errorf("tunnel stream: %s", msg.GetErrorMessage()))
		return
	}

	var event leapmuxv1.TunnelConnEvent
	if err := proto.Unmarshal(msg.GetPayload(), &event); err != nil {
		tc.terminal.Set(fmt.Errorf("decode tunnel event: %w", err))
		return
	}
	if event.GetEof() {
		tc.terminal.Set(io.EOF)
		return
	}
	if event.GetError() != "" {
		tc.terminal.Set(fmt.Errorf("tunnel error: %s", event.GetError()))
		return
	}
	if len(event.GetData()) > 0 {
		// Enforce the chunk bound the read window is denominated in, exactly as the
		// worker's SendTunnelData handler does for the other direction. The worker
		// frames at its 32 KiB read buffer, which is what makes InitialReadWindow a
		// BYTE bound (InitialReadWindow * MaxChunkBytes) -- but that is the SENDER's
		// convention, and this client must not trust it any more than the worker
		// trusts ours. A worker that does not chunk (a buggy or compromised one) is
		// otherwise bounded only by the channel's 16 MiB inner-message limit, and
		// readBuf holds ReadBufFrames of them: 256 x 16 MiB = 4 GiB pinned in the
		// client, per conn, against a 4 MiB design target.
		//
		// Latch it as terminal rather than dropping the frame: a peer violating the
		// framing contract is not something this conn can resynchronise with, and a
		// silent drop would hand the local socket a corrupt byte stream. It kills
		// this tunnel conn only -- the shared channel and every other conn on it are
		// untouched.
		if len(event.GetData()) > tunnelflow.MaxChunkBytes {
			tc.terminal.Set(fmt.Errorf(
				"tunnel data frame is %d bytes, exceeding the %d-byte chunk bound",
				len(event.GetData()), tunnelflow.MaxChunkBytes))
			return
		}
		// event.Data is a fresh proto.Unmarshal allocation (bytes fields are
		// never aliased to the wire buffer), so it can be handed to readBuf
		// directly -- no defensive copy needed.
		select {
		case tc.readBuf <- event.GetData():
		case <-tc.closed:
		case <-tc.ch.Context().Done():
		}
	}
}

// consumeFrame copies a frame just dequeued from readBuf into b, parks any
// remainder in readPart, and replenishes one read-credit -- the frame left
// readBuf, so a buffer slot (and the worker's matching read window) is free.
func (tc *Conn) consumeFrame(b, data []byte) int {
	n := copy(b, data)
	if n < len(data) {
		tc.readPart = data[n:]
	}
	tc.credit.consume(1)
	return n
}

// sendReadCredit sends a read-credit grant to the worker without awaiting a
// response. A lost grant only slows the inbound stream (the channel is reliable,
// and a dead channel or closed conn ends the stream anyway), so its error is
// ignored. It blocks on the shared send path, so only readCredit's loop may call it.
func (tc *Conn) sendReadCredit(ctx context.Context, credit uint64) {
	payload, err := proto.Marshal(&leapmuxv1.GrantTunnelReadCreditRequest{ConnId: tc.connID, Credit: credit})
	if err != nil {
		return
	}
	_, _ = tc.ch.SendRPCNoWait(ctx, "GrantTunnelReadCredit", payload, RPCHandlers{})
}

// Read implements net.Conn.
func (tc *Conn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	tc.readMu.Lock()
	defer tc.readMu.Unlock()

	for {
		select {
		case <-tc.closed:
			return 0, net.ErrClosed
		default:
		}
		if len(tc.readPart) > 0 {
			n := copy(b, tc.readPart)
			tc.readPart = tc.readPart[n:]
			return n, nil
		}
		select {
		case data := <-tc.readBuf:
			return tc.consumeFrame(b, data), nil
		default:
		}
		select {
		case <-tc.terminal.Done():
			return 0, tc.terminal.Err()
		default:
		}

		timer, timerCh, changed, expired := tc.readDeadline.armTimer()
		if expired {
			return 0, os.ErrDeadlineExceeded
		}
		select {
		case data := <-tc.readBuf:
			stopTimer(timer)
			return tc.consumeFrame(b, data), nil
		case <-tc.terminal.Done():
			stopTimer(timer)
			continue
		case <-tc.closed:
			stopTimer(timer)
			return 0, net.ErrClosed
		case <-tc.ch.Context().Done():
			stopTimer(timer)
			tc.terminal.Set(io.ErrUnexpectedEOF)
			continue
		case <-changed:
			stopTimer(timer)
		case <-timerCh:
			return 0, os.ErrDeadlineExceeded
		}
	}
}

// Write implements net.Conn.
//
// Flow control: Write acquires a send-window slot (tunnelflow.WriteWindowFrames) before
// putting a frame on the wire and releases it once the worker acknowledges the
// frame (awaitWriteAck). A slow or non-draining target stops the worker acking,
// the window fills, and Write blocks -- backpressuring the local source through
// io.Copy instead of letting the worker buffer unbounded data.
//
// Note on deadlines: SetWriteDeadline bounds entry -- send-permit acquisition,
// the pre-write liveness check, AND the send-window wait, so a Write parked on a
// full window returns os.ErrDeadlineExceeded when its deadline fires, including
// when the deadline is set AFTER the Write parked (net.Conn requires a deadline
// to apply to pending I/O, so deadlineState's watcher re-arms on every change).
// Once a slot is held the underlying WebSocket write runs under the E2EE
// channel's LIFETIME context (see Channel.sendInnerContext), not this deadline,
// because binding it to a per-Write deadline would let one caller's expired
// deadline force-close the shared transport for every in-flight RPC. So a frame
// already on the wire but stuck in the WebSocket layer can still outlive the
// deadline; the current callers (io.Copy in the port-forward/SOCKS5 proxies)
// never set a write deadline, so this does not bite in practice.
func (tc *Conn) Write(b []byte) (int, error) {
	select {
	case <-tc.closed:
		return 0, net.ErrClosed
	default:
	}
	if len(b) == 0 {
		return 0, nil
	}
	// context.Background() cannot expire, so this acquire cannot fail: the write
	// deadline bounds the send path from inside sendFrameLocked (which is where a
	// deadline set mid-Write can still be observed), not this lock.
	_ = tc.writeMu.Lock(context.Background())
	defer tc.writeMu.Unlock()
	// Split at tunnelflow.MaxChunkBytes so the send window bounds in-flight BYTES, not
	// just frames: net.Conn.Write accepts a buffer of any size, so a caller that
	// hands over a large one (a bufio.Writer, bytes.Buffer.WriteTo, an
	// http.Transport flushing a big body) must neither pin tunnelflow.WriteWindowFrames *
	// 16 MiB on the worker nor fail outright against the channel's inner-message
	// limit. Each chunk takes its own sequence and window slot, so the worker still
	// applies them in order and backpressure still reaches this caller.
	written := 0
	for len(b) > 0 {
		chunk := b
		if len(chunk) > tunnelflow.MaxChunkBytes {
			chunk = chunk[:tunnelflow.MaxChunkBytes]
		}
		if err := tc.sendFrameLocked(chunk, false); err != nil {
			// Report the bytes that did reach the wire: net.Conn.Write must return
			// n < len(b) with the error that stopped it.
			return written, err
		}
		written += len(chunk)
		b = b[len(chunk):]
	}
	return written, nil
}

// acquireSendSlot blocks until a send-window slot is free, honoring a write
// deadline and surfacing a concurrent close or latched NAK, and returns the context
// the rest of the send (the send-permit acquire inside SendRPCNoWait) must run
// under, plus the flag writeFailure consults to remap an expiry.
//
// The common case -- NO write deadline, which is every production caller (the
// port-forward/SOCKS5 io.Copy never sets one) -- takes a goroutine-free fast path:
// it parks inline on the same latches and watches `changed`, so a deadline set WHILE
// it parks upgrades to the deadline path rather than being missed (net.Conn requires
// a deadline to apply to pending I/O). Only the deadline path pays for
// writeDeadline.context's per-frame watcher goroutine, whose sole job is to re-arm
// on every deadline change and bound both this park and the permit acquire below.
//
// The park must watch writeErr as well as the slot: the caller polls writeErr once
// on entry and the park is unbounded, so a NAK latched by an in-flight awaitWriteAck
// while this frame waits would otherwise be missed -- the frame marshalled and sent
// to a target the worker has already given up on, its bytes counted as written.
//
// On success the caller owns one send-window slot and must release it; cancel is
// always non-nil and must be deferred. deadline is nil on the goroutine-free
// no-deadline fast path (there is no expiry to remap); writeFailure treats a nil
// deadline as "never exceeded".
func (tc *Conn) acquireSendSlot() (ctx context.Context, cancel context.CancelFunc, deadline *atomic.Bool, err error) {
	if deadline0, changed := tc.writeDeadline.snapshot(); deadline0.IsZero() {
		select {
		case tc.sendWindow <- struct{}{}:
			return tc.ch.Context(), func() {}, nil, nil
		case <-tc.closed:
			return nil, func() {}, nil, net.ErrClosed
		case <-tc.writeErr.Done():
			return nil, func() {}, nil, tc.writeErr.Err()
		case <-tc.ch.Context().Done():
			return nil, func() {}, nil, net.ErrClosed
		case <-changed:
			// A deadline landed while we parked: take the deadline path below, which
			// observes it (and any later change) for the rest of the send.
		}
	}
	ctx, cancel, deadline = tc.writeDeadline.context(tc.ch.Context())
	select {
	case tc.sendWindow <- struct{}{}:
		return ctx, cancel, deadline, nil
	case <-tc.closed:
		cancel()
		return nil, func() {}, nil, net.ErrClosed
	case <-tc.writeErr.Done():
		cancel()
		return nil, func() {}, nil, tc.writeErr.Err()
	case <-ctx.Done():
		cancel()
		return nil, func() {}, nil, writeFailure(net.ErrClosed, deadline)
	}
}

// sendFrameLocked puts one SendTunnelData frame -- carrying data, a close_write
// half-close, or both -- on the wire and spawns awaitWriteAck to release its
// window slot and latch a worker NAK. It is the shared core of Write and
// CloseWrite so the half-close rides the exact same per-conn sequence,
// flow-control window, and ack/NAK path as data: a failed target write OR a
// failed target write-half-close both latch writeErr and surface on the next
// write the way TCP surfaces EPIPE, rather than being reported as success.
// Caller holds writeMu; the call returns once the frame is on the wire (it does
// not block on the ack).
func (tc *Conn) sendFrameLocked(data []byte, closeWrite bool) error {
	// A prior close_write already told the worker to close the target's write
	// half (its writeData ran CloseWrite() on the target), so it can deliver no
	// further data: framing one anyway and returning (len(b), nil) would report
	// success for bytes the target can never see. *net.TCPConn answers the same
	// Write with net.ErrClosed, so match it. The gate lives here rather than in
	// Write so no future data sender under writeMu can reintroduce the violation.
	// The half-close frame itself is exempt -- CloseWrite's own closeWriteSent
	// check already makes it send-once.
	if tc.closeWriteSent && !closeWrite {
		return net.ErrClosed
	}

	// A prior frame's worker NAK (target write failed) makes every further write
	// futile: the worker drops them. Surface the latched error the way TCP
	// surfaces EPIPE on a later write, instead of reporting success for bytes that
	// never reach the target.
	if err := tc.writeErr.Err(); err != nil {
		return err
	}

	// Stamp a per-conn write sequence (under writeMu, so it matches send order).
	// The worker applies a conn's writes in seq order, since it dispatches each
	// SendTunnelData on its own goroutine and would otherwise reorder them.
	seq := tc.writeSeq

	// Acquire a send-window slot before putting a frame on the wire. ctx is what
	// the rest of the send (the permit acquire inside SendRPCNoWait below) runs
	// under, and deadline reports whether an expiry ended it.
	ctx, cancel, deadline, err := tc.acquireSendSlot()
	if err != nil {
		return err
	}
	defer cancel()
	// From here the slot is held, and EVERY exit -- success or failure -- must
	// account for it. A missed release permanently shrinks the window (cap
	// tunnelflow.WriteWindowFrames) until every write on this conn wedges, so make the
	// release a property of leaving the function rather than something each new
	// failure exit has to remember: the defer frees it unless the success path
	// below hands ownership to awaitWriteAck.
	slotHeld := true
	defer func() {
		if slotHeld {
			<-tc.sendWindow
		}
	}()

	// Won the slot -- but a concurrent Close (which frees slots via awaitWriteAck)
	// or a NAK latched while parked may have fired at the same moment, and a
	// select that can take the slot arm always may take it regardless. Re-check
	// both latches now that the slot is held, so a Write racing Close returns
	// ErrClosed and one racing a NAK surfaces it, instead of sending on a dead
	// conn or to a target the worker has given up on.
	select {
	case <-tc.closed:
		return net.ErrClosed
	default:
	}
	if err := tc.writeErr.Err(); err != nil {
		return err
	}

	// Advance writeSeq ONLY after the frame is on the wire. A send that fails
	// while the shared channel stays alive delivers no frame; consuming the seq
	// anyway would leave a permanent gap the worker's exact-turn gate
	// (waitWriteTurn) waits on forever, stranding every later write and the
	// graceful close for this conn. Leaving writeSeq unadvanced lets the next
	// write reuse the seq, keeping the sequence the worker observes contiguous.
	payload, err := proto.Marshal(&leapmuxv1.SendTunnelDataRequest{ConnId: tc.connID, Data: data, CloseWrite: closeWrite, Seq: seq})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// Register the ACK handler so awaitWriteAck can release the window slot when
	// the worker's SendTunnelDataResponse arrives, and latch a NAK for the next
	// write to surface.
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	reqID, err := tc.ch.SendRPCNoWait(ctx, "SendTunnelData", payload, RPCHandlers{Response: respCh})
	if err != nil {
		return writeFailure(err, deadline)
	}
	tc.writeSeq = seq + 1
	// The frame is on the wire: its slot now belongs to awaitWriteAck, which frees
	// it when the ack arrives (or when the conn/channel dies). This is the ONLY
	// path that hands the slot off, so it is the only one that disarms the defer.
	//
	// One goroutine + one cap-1 channel per chunk (Conn.Write splits at
	// tunnelflow.MaxChunkBytes = 32 KiB, so ~640/sec on a saturated tunnel) is
	// allocation/scheduling overhead the window bound (WriteWindowFrames = 64
	// concurrent) caps but does not eliminate. A single long-lived per-conn acker
	// would remove it; see https://github.com/leapmux/leapmux/issues/278.
	slotHeld = false
	go tc.awaitWriteAck(reqID, respCh)
	return nil
}

// writeFailure remaps a send failure to os.ErrDeadlineExceeded when the write
// deadline is what ended the send context. net.Conn's contract is that an expired
// deadline surfaces as os.ErrDeadlineExceeded (callers test it via
// net.Error.Timeout / os.IsTimeout), but a cancelled context reaches the send path
// as whatever error the cancellation produced downstream, so the remap belongs at
// every failure exit that a deadline can cause -- stated once here rather than
// re-derived per exit, which is how the two copies of it came to sit next to
// differing fallbacks.
func writeFailure(err error, deadline *atomic.Bool) error {
	// A nil deadline is the no-deadline fast path: there is no expiry to remap, so
	// the failure surfaces as-is (equivalent to the old always-false shared flag).
	if deadline != nil && deadline.Load() {
		return os.ErrDeadlineExceeded
	}
	return err
}

// awaitWriteAck waits for the worker's SendTunnelDataResponse (success OR error
// -- either way the frame is no longer in flight) and then frees the frame's
// send-window slot and unregisters its pending handler. A conn close or channel
// death also frees the slot, so teardown never strands the goroutine. An IsError
// response means the worker's target write failed: latch it so the next Write /
// CloseWrite surfaces the broken target instead of draining bytes the worker
// silently drops.
func (tc *Conn) awaitWriteAck(reqID uint64, respCh chan *leapmuxv1.InnerRpcResponse) {
	select {
	case resp := <-respCh:
		if resp.GetIsError() {
			tc.writeErr.Set(fmt.Errorf("tunnel write rejected (code %d): %s", resp.GetErrorCode(), resp.GetErrorMessage()))
		}
	case <-tc.closed:
	case <-tc.ch.Context().Done():
	}
	tc.ch.UnregisterPending(reqID)
	<-tc.sendWindow
}

// Close implements net.Conn.
func (tc *Conn) Close() error {
	tc.closeOnce.Do(func() {
		close(tc.closed)
		// unregister stops the credit loop and aborts any grant parked on the shared
		// send permit: the conn is gone, so its read window needs no replenishing.
		tc.unregister()
		go tc.sendRemoteClose()
	})
	return nil
}

// CloseWrite half-closes the write side of the tunnel: it signals the worker to
// CloseWrite the target connection so a request/response protocol that ends its
// request with a TCP FIN (HTTP/1.0, SSH, `nc -N`, line protocols) delivers that
// read-EOF to the target. The read side stays open for the response. It takes
// writeMu so the half-close frame is sent AFTER every prior Write's frame is on
// the wire, preserving client-side order; the worker applies it under the same
// per-conn write lock as data (see tunnelConn.writeData). Sent once; later calls
// are no-ops, matching *net.TCPConn.CloseWrite. Satisfies the
// `interface{ CloseWrite() error }` the desktop port-forward/SOCKS5 copy
// forwards through owned connections.
//
// The half-close rides sendFrameLocked, exactly as data does, so it acquires a
// send-window slot and registers an ack handler: a worker NAK (the target's
// write-side close failed) latches writeErr and surfaces on a subsequent write
// the way TCP surfaces EPIPE, rather than being silently dropped and reported to
// the caller as success.
func (tc *Conn) CloseWrite() error {
	// Bound the write-lock acquire by the write deadline, mirroring Write's
	// respect for SetWriteDeadline: a prior Write parked on a full send window
	// (the #276 wedge) holds writeMu indefinitely against a worker that stopped
	// acking, and CloseWrite -- whose own deadline handling lives in
	// sendFrameLocked, AFTER the acquire -- would otherwise block on the acquire
	// forever with no escape short of Close (which bypasses the lock entirely via
	// sendRemoteClose). With no write deadline set the derived context never
	// expires, so the acquire stays unbounded the same way Write's does; with one
	// set, a timeout returns os.ErrDeadlineExceeded the way a timed-out Write
	// would, so a port-forward/SOCKS5 copy loop's half-close honors the deadline
	// its caller configured instead of hanging the goroutine.
	ctx, cancel, exceeded := tc.writeDeadline.context(context.Background())
	defer cancel()
	if err := tc.writeMu.Lock(ctx); err != nil {
		// ctx is Background-derived and we never cancel it ourselves, so the only
		// way Lock fails is the deadline watcher firing -- surface it as the
		// standard deadline error rather than a bare context error.
		return os.ErrDeadlineExceeded
	}
	defer tc.writeMu.Unlock()
	_ = exceeded // the deadline watcher's flag; the ctx error above is the signal
	if tc.closeWriteSent {
		return nil
	}
	select {
	case <-tc.closed:
		return net.ErrClosed
	default:
	}
	if err := tc.sendFrameLocked(nil, true); err != nil {
		// Leave closeWriteSent false so a caller that retries after a transient
		// failure (an expired deadline, a momentarily-full send window) still gets
		// the frame out. Reporting nil here would tell the caller the target saw
		// the read-EOF when no frame was ever emitted.
		return err
	}
	tc.closeWriteSent = true
	return nil
}

func (tc *Conn) LocalAddr() net.Addr  { return tc.localAddr }
func (tc *Conn) RemoteAddr() net.Addr { return tc.remoteAddr }

func (tc *Conn) SetDeadline(deadline time.Time) error {
	tc.readDeadline.set(deadline)
	tc.writeDeadline.set(deadline)
	return nil
}

func (tc *Conn) SetReadDeadline(deadline time.Time) error {
	tc.readDeadline.set(deadline)
	return nil
}

func (tc *Conn) SetWriteDeadline(deadline time.Time) error {
	tc.writeDeadline.set(deadline)
	return nil
}
