package tunnel

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/tunnelflow"
)

// TestWriteFailure_DeadlineRemap pins writeFailure's contract, including the nil
// deadline the goroutine-free no-deadline send path now passes (replacing the old
// shared always-false neverExceeded sentinel). A nil deadline must be treated as
// "never exceeded" -- surfacing the error unremapped -- rather than dereferenced.
func TestWriteFailure_DeadlineRemap(t *testing.T) {
	base := errors.New("send failed")

	// The no-deadline fast path passes nil: the error surfaces as-is.
	assert.Equal(t, base, writeFailure(base, nil), "a nil deadline must pass the error through, not panic")

	// A deadline that has not fired also passes through.
	var d atomic.Bool
	assert.Equal(t, base, writeFailure(base, &d), "an un-exceeded deadline passes the error through")

	// A fired deadline remaps to os.ErrDeadlineExceeded so callers can test it via
	// os.IsTimeout / net.Error.Timeout.
	d.Store(true)
	assert.ErrorIs(t, writeFailure(base, &d), os.ErrDeadlineExceeded, "an exceeded deadline remaps to a timeout error")
}

// errTargetWriteFailed stands in for the error runAckLoop latches when the
// worker NAKs a frame because its write to the target failed.
var errTargetWriteFailed = errors.New("tunnel write rejected: broken pipe")

// fillSendWindow writes tunnelflow.WriteWindowFrames single-byte frames, leaving the send
// window full and every frame outstanding, so the next Write must park.
func fillSendWindow(t *testing.T, tc *Conn, ch *windowTestChannel) {
	t.Helper()
	for range tunnelflow.WriteWindowFrames {
		n, err := tc.Write([]byte("x"))
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
	require.Equal(t, tunnelflow.WriteWindowFrames, ch.pendingAcks(), "the full window is outstanding")
}

func (c *recordingChannel) frameCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func (c *seqRecordingChannel) frameCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

// A Write parked on a full send window must abort when a worker NAK latches
// writeErr while it waits. The send path polls writeErr exactly once on entry
// and the park is unbounded, so without watching the latch the parked chunk was
// marshalled and sent to a target the worker had already given up on -- and
// Write counted its bytes as written, reporting success for bytes that never
// reach the target, which is precisely what the entry poll exists to prevent.
func TestConnWriteParkedOnFullWindowAbortsOnLatchedNAK(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &windowTestChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)
	fillSendWindow(t, tc, ch)

	type writeResult struct {
		n   int
		err error
	}
	done := make(chan writeResult, 1)
	go func() {
		n, err := tc.Write([]byte("blocked"))
		done <- writeResult{n, err}
	}()
	select {
	case <-done:
		t.Fatal("Write returned even though the send window was full")
	case <-time.After(50 * time.Millisecond):
	}

	// The worker NAKs an outstanding frame: its target write failed. That both
	// latches writeErr and frees a window slot, so the parked Write may well win
	// the slot -- it must still refuse to send.
	framesOutstanding := ch.pendingAcks()
	require.True(t, ch.nakOne(13, "write: broken pipe"), "the worker NAKs an outstanding frame")

	select {
	case res := <-done:
		require.Error(t, res.err, "the parked Write must abort once the NAK latches")
		assert.ErrorContains(t, res.err, "broken pipe",
			"the parked Write must surface the worker's terminal error")
		assert.Zero(t, res.n,
			"no byte of the parked chunk reached the target, so none may be reported written")
	case <-time.After(2 * time.Second):
		t.Fatal("a Write parked on a full send window ignored the latched write error")
	}

	assert.Equal(t, framesOutstanding-1, ch.pendingAcks(),
		"the aborted Write must put no further frame on the wire; only the NAKed one left the window")
}

// A NAK already latched when Write is called must stop the frame outright, even
// with the window wide open: the acquire's slot arm is always ready then, so the
// re-check AFTER winning a slot -- not the park's writeErr arm -- is what covers
// this.
func TestConnWriteWithLatchedNAKEmitsNoFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &windowTestChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	tc.writeErr.Set(errTargetWriteFailed)
	n, err := tc.Write([]byte("doomed"))
	assert.ErrorIs(t, err, errTargetWriteFailed)
	assert.Zero(t, n)
	assert.Zero(t, ch.pendingAcks(), "no frame may reach the wire once writeErr is latched")
	assert.Zero(t, tc.sendWindow.InUse(), "the refused write must hold no window slot")
}

// A nil Set must be a no-op rather than latching a terminal state with no
// cause: closing Done while Err()==nil would make every reader selecting on Done
// observe "completed successfully" and return (0, nil), which io.Copy treats as
// "retry" -- a 100%-CPU busy-spin that never yields, EOFs, or errors. Latching
// only real errors keeps that footgun mechanically impossible at the type.
func TestLatchedErrSetNilIsNoOp(t *testing.T) {
	var l latchedErr

	l.Set(nil)
	select {
	case <-l.Done():
		t.Fatal("Done must not close on a nil Set: a reader would observe a bogus nil error and busy-spin")
	default:
	}
	assert.Nil(t, l.Err(), "Err stays nil before any real error is latched")

	// A subsequent real error latches normally -- the nil Set did not consume the
	// setOnce.
	want := errors.New("real failure")
	l.Set(want)
	select {
	case <-l.Done():
	default:
		t.Fatal("Done must close once a real error is latched")
	}
	assert.ErrorIs(t, l.Err(), want)

	// A nil Set after a real latch is a no-op: the first real error is retained.
	l.Set(nil)
	assert.ErrorIs(t, l.Err(), want, "a nil Set after a real latch must not clear it")
}

// CloseWrite must honor SetWriteDeadline when writeMu is contended: a Write
// parked on a full send window holds writeMu against a worker that stopped
// acking, and CloseWrite -- whose deadline handling lives in sendFrameLocked
// AFTER the acquire -- would otherwise block on the acquire forever with no
// escape short of Close. With a write deadline set, the deadline-bounded acquire
// returns os.ErrDeadlineExceeded instead of hanging the caller (a
// port-forward/SOCKS5 copy loop's half-close).
func TestConnCloseWriteHonorsWriteDeadlineWhenWriteMuHeld(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &recordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	// Hold writeMu the way a Write parked on a full send window does. The test
	// releases it in cleanup so the conn can tear down.
	require.NoError(t, tc.writeMu.Lock(context.Background()))
	t.Cleanup(tc.writeMu.Unlock)

	// A short future deadline: CloseWrite blocks on the contended acquire, the
	// deadline watcher fires, and the bounded acquire returns the deadline error.
	require.NoError(t, tc.SetWriteDeadline(time.Now().Add(50*time.Millisecond)))

	done := make(chan error, 1)
	go func() { done <- tc.CloseWrite() }()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, os.ErrDeadlineExceeded,
			"CloseWrite on a contended writeMu must surface the write deadline rather than block indefinitely")
	case <-time.After(2 * time.Second):
		t.Fatal("CloseWrite blocked indefinitely on a contended writeMu instead of honoring the write deadline")
	}
}

// A data Write after CloseWrite must fail: the worker already ran CloseWrite()
// on the target, so it can deliver no further bytes. Framing the data anyway and
// returning (len(b), nil) reports success for bytes the target can never see.
// *net.TCPConn answers the same Write with net.ErrClosed.
func TestConnWriteAfterCloseWriteReturnsErrClosed(t *testing.T) {

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &recordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	require.NoError(t, tc.CloseWrite())
	require.Equal(t, 1, ch.frameCount(), "the half-close is on the wire")

	n, err := tc.Write([]byte("after the FIN"))
	assert.ErrorIs(t, err, net.ErrClosed, "a data write after the half-close must be refused")
	assert.Zero(t, n, "no bytes may be reported written")
	assert.Equal(t, 1, ch.frameCount(), "the refused Write must emit no data frame")
}

// CloseWrite itself stays exempt from the write-after-half-close gate -- it is
// made send-once by its own closeWriteSent check -- so a repeat call is still a
// silent no-op rather than an error, matching *net.TCPConn.CloseWrite.
func TestConnRepeatedCloseWriteStaysNoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &recordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	require.NoError(t, tc.CloseWrite())
	require.NoError(t, tc.CloseWrite(), "a repeat CloseWrite is a no-op")
	assert.Equal(t, 1, ch.frameCount(), "only one half-close frame reaches the wire")
}

// Every failure exit of the send path must return the window slot it acquired.
// A leaked slot permanently shrinks the window (cap tunnelflow.WriteWindowFrames) until
// every write on the conn wedges -- so drive far more failures than the window
// has slots and assert the window is still empty and still usable.
func TestConnSendFailuresDoNotLeakWindowSlots(t *testing.T) {
	const failures = tunnelflow.WriteWindowFrames * 3
	ch := &seqRecordingChannel{failSends: failures}
	tc := newConn(ch, "conn-1", "example.test", 443)

	for range failures {
		_, err := tc.Write([]byte("dropped"))
		require.Error(t, err, "the simulated send failure must surface to the caller")
	}
	assert.Zero(t, tc.sendWindow.InUse(),
		"every failed send must return its window slot; a leak shrinks the window until the conn wedges")

	// The window must still be fully usable: a full window's worth of writes must
	// all proceed without parking. (seqRecordingChannel acks each frame, so slots
	// are also released asynchronously; the point here is that nothing wedges.)
	done := make(chan error, 1)
	go func() {
		for range tunnelflow.WriteWindowFrames {
			if _, err := tc.Write([]byte("x")); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("leaked window slots wedged the conn: a full window's worth of writes could not proceed")
	}
	assert.Equal(t, tunnelflow.WriteWindowFrames, ch.frameCount(), "every post-failure write reached the wire")
}

// The closed-race exit -- win a free slot, then observe tc.closed on the
// re-check -- takes a slot before failing, so it too must return it.
func TestConnWriteLosingCloseRaceReleasesWindowSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &windowTestChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	close(tc.closed)
	_, err := tc.Write([]byte("x"))
	require.ErrorIs(t, err, net.ErrClosed)
	assert.Zero(t, tc.sendWindow.InUse(), "the closed-race exit must return the slot it won")
	assert.Zero(t, ch.pendingAcks(), "a closed conn emits no frame")
}

// stopTimer must tolerate the nil timer armTimer returns when no deadline is
// set -- (*time.Timer).Stop panics on nil, and every deadline wait-loop exit
// calls it.
func TestStopTimerAcceptsNilTimer(t *testing.T) {
	d := newDeadlineState()
	timer, timerCh, _, expired := d.armTimer()
	require.Nil(t, timer, "no deadline set means no timer")
	require.Nil(t, timerCh)
	require.False(t, expired)
	assert.NotPanics(t, func() { stopTimer(timer) })

	d.set(time.Now().Add(time.Hour))
	timer, _, _, expired = d.armTimer()
	require.NotNil(t, timer, "a future deadline arms a timer")
	require.False(t, expired)
	stopTimer(timer)
	assert.False(t, timer.Stop(), "stopTimer must actually stop the timer it is handed")
}

// closeTunnelConnRecorder is a tunnelRPCChannel fake that records the seq of
// every CloseTunnelConn send, so the two sendRemoteClose paths are testable
// without a real worker.
type closeTunnelConnRecorder struct {
	ctx    context.Context
	mu     sync.Mutex
	closes []uint64
}

func (c *closeTunnelConnRecorder) Context() context.Context { return c.ctx }
func (*closeTunnelConnRecorder) UnregisterPending(uint64)   {}
func (*closeTunnelConnRecorder) UnregisterStream(uint64)    {}
func (c *closeTunnelConnRecorder) SendRPCNoWait(_ context.Context, method string, payload []byte, _ RPCHandlers) (uint64, error) {
	if method == "CloseTunnelConn" {
		var r leapmuxv1.CloseTunnelConnRequest
		if err := proto.Unmarshal(payload, &r); err == nil {
			c.mu.Lock()
			c.closes = append(c.closes, r.GetSeq())
			c.mu.Unlock()
		}
	}
	return 1, nil
}

func (c *closeTunnelConnRecorder) recorded() []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]uint64, len(c.closes))
	copy(out, c.closes)
	return out
}

// When writeMu is free, sendRemoteClose orders the close after every prior write
// by reading writeSeq under the lock, so the worker can flush pending frames
// before tearing the conn down (graceful close).
func TestSendRemoteCloseGracefulOrdersAfterPriorWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &closeTunnelConnRecorder{ctx: ctx}
	tc := newConn(ch, "conn-graceful", "example.test", 443)
	tc.writeSeq = 5 // stand in for five prior writes

	tc.sendRemoteClose()

	closes := ch.recorded()
	require.Equal(t, []uint64{5}, closes,
		"a graceful close carries the write sequence so the worker flushes lower-seq frames first")
}

// When writeMu cannot be acquired in budget -- a Write is parked on a full send
// window against a target that stopped draining -- sendRemoteClose must still send
// a CloseTunnelConn (seq 0) so the worker force-closes the conn instead of leaking
// it for the channel's lifetime. Abandoning the close silently is the leak.
func TestSendRemoteCloseForcefulWhenWriteMuHeld(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &closeTunnelConnRecorder{ctx: ctx}
	tc := newConn(ch, "conn-wedge", "example.test", 443)
	tc.writeSeq = 5 // prior writes exist, but the close cannot wait them out

	// Hold writeMu the way a Write parked on a full send window does.
	require.NoError(t, tc.writeMu.Lock(context.Background()))
	defer tc.writeMu.Unlock()

	// Shorten the lock budget so the test does not wait the production 5s. Restore
	// it on cleanup so it cannot leak into other tests in this package.
	prev := remoteCloseLockBudget
	remoteCloseLockBudget = 20 * time.Millisecond
	t.Cleanup(func() { remoteCloseLockBudget = prev })

	tc.sendRemoteClose()

	closes := ch.recorded()
	require.Len(t, closes, 1, "a wedged conn must still send a forceful close, not abandon it")
	assert.Equal(t, uint64(0), closes[0],
		"the forceful close carries seq 0 so the worker force-closes without waiting on a flush")
}

// The ack loop must grant exactly one send-window slot per ack. Under-granting
// permanently shrinks the window; over-granting would admit more outstanding
// frames than WriteWindowFrames.
func TestConnAckLoopGrantsOneSlotPerAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &windowTestChannel{ctx: ctx}
	tc := newConn(ch, "conn-ack-one", "example.test", 443)

	_, err := tc.Write([]byte("x"))
	require.NoError(t, err)
	assert.Equal(t, 1, tc.sendWindow.InUse())
	require.True(t, ch.ackOne())
	require.Eventually(t, func() bool { return tc.sendWindow.InUse() == 0 },
		2*time.Second, 5*time.Millisecond, "one ack must free exactly one slot")

	for range 5 {
		_, err := tc.Write([]byte("y"))
		require.NoError(t, err)
	}
	assert.Equal(t, 5, tc.sendWindow.InUse())
	for range 5 {
		require.True(t, ch.ackOne())
	}
	require.Eventually(t, func() bool { return tc.sendWindow.InUse() == 0 },
		2*time.Second, 5*time.Millisecond, "N acks must free exactly N slots")
	assert.Equal(t, tunnelflow.WriteWindowFrames, tc.sendWindow.Available(),
		"the window must be fully restored, never over-granted")
}

// Saturated write+ack cycles must not grow goroutines: runAckLoop is one
// long-lived consumer for every frame's ack.
func TestConnWriteAckCyclesDoNotGrowGoroutines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &seqRecordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-ack-goroutines", "example.test", 443)

	settled := func() int {
		runtime.GC()
		return runtime.NumGoroutine()
	}
	time.Sleep(50 * time.Millisecond)
	baseline := settled()

	const cycles = tunnelflow.WriteWindowFrames * 4
	for range cycles {
		_, err := tc.Write([]byte("x"))
		require.NoError(t, err)
	}
	// seqRecordingChannel acks each frame inline, so slots release asynchronously
	// through runAckLoop; wait for the window to drain before sampling.
	require.Eventually(t, func() bool { return tc.sendWindow.InUse() == 0 },
		2*time.Second, 5*time.Millisecond)

	require.Eventually(t, func() bool { return settled() <= baseline+2 },
		2*time.Second, 10*time.Millisecond,
		"write+ack cycles must not spawn a goroutine per frame (baseline=%d now=%d)",
		baseline, settled())
}
