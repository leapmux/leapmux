package kimi

import (
	"context"
	"encoding/json"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// kimiStreamRig is one event stream connected to a fake server.
type kimiStreamRig struct {
	fake   *fakeKap
	stream *kimiStream

	mu         sync.Mutex
	frames     []kimiFrame
	reconnects []kimiReconnect
}

// kimiReconnect is one call of the stream's reconnect handler.
type kimiReconnect struct {
	sessionID string
	replayed  bool
}

// newKimiStreamRig starts a stream on the real clock that waits one millisecond
// before a reconnect. The tests wait on the reconnect, never on the delay.
func newKimiStreamRig(t *testing.T) *kimiStreamRig {
	t.Helper()
	return newKimiStreamRigOnClock(t, quartz.NewReal(), time.Millisecond)
}

// newKimiStreamRigOnClock starts a stream whose timers run on clock, and that
// waits backoff before its first reconnect attempt.
func newKimiStreamRigOnClock(t *testing.T, clock quartz.Clock, backoff time.Duration) *kimiStreamRig {
	t.Helper()
	fake, server := newFakeKap(t)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BearerAuth(fakeKapToken))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	rig := &kimiStreamRig{fake: fake}
	rig.stream = newKimiStream(endpoint, "test-agent", clock, func(frame kimiFrame) {
		rig.mu.Lock()
		rig.frames = append(rig.frames, frame)
		rig.mu.Unlock()
	}, func(_ context.Context, sessionID string, replayed bool) {
		rig.mu.Lock()
		rig.reconnects = append(rig.reconnects, kimiReconnect{sessionID: sessionID, replayed: replayed})
		rig.mu.Unlock()
	})
	rig.stream.backoff = backoff
	ctx, cancel := context.WithCancel(context.Background())
	// The cleanups run in reverse: close, then wait, then cancel. A stream that
	// its close did not end fails the test in the wait.
	t.Cleanup(cancel)
	t.Cleanup(rig.stream.wait)
	t.Cleanup(rig.stream.close)
	require.NoError(t, rig.stream.start(ctx, 10*time.Second))
	// The fake records a socket after the handshake, in its own goroutine, so
	// the dial can return first. A push before the record finds no socket.
	waitFor(t, func() bool { return fake.connectionCount() == 1 }, "the fake records the stream's socket")
	return rig
}

func (r *kimiStreamRig) received() []kimiFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]kimiFrame(nil), r.frames...)
}

func (r *kimiStreamRig) reconnected() []kimiReconnect {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]kimiReconnect(nil), r.reconnects...)
}

func (r *kimiStreamRig) cursor(sessionID string) (kimiCursor, bool) {
	r.stream.mu.Lock()
	defer r.stream.mu.Unlock()
	cursor, ok := r.stream.sessions[sessionID]
	return cursor, ok
}

func TestKimiStreamAnswersEveryPing(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)

	// The pong is what keeps the server from closing the socket after 20
	// seconds, so it answers each ping with that ping's nonce.
	rig.fake.push(t, map[string]any{"type": kimiFramePing, "payload": map[string]string{"nonce": "n-1"}})
	rig.fake.push(t, map[string]any{"type": kimiFramePing, "payload": map[string]string{"nonce": "n-2"}})
	// A ping with no payload still gets its pong, or the server closes the socket.
	rig.fake.push(t, map[string]any{"type": kimiFramePing})
	waitFor(t, func() bool { return len(rig.fake.pongNonces()) == 3 }, "every ping gets a pong")
	assert.Equal(t, []string{"n-1", "n-2", ""}, rig.fake.pongNonces())
	assert.Empty(t, rig.received(), "a ping is not an event")
}

func TestKimiStreamDeliversEventsInOrder(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	_, err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	for seq := int64(1); seq <= 20; seq++ {
		rig.fake.push(t, map[string]any{"type": "assistant.delta", "seq": seq, "session_id": "session_1", "payload": map[string]any{"type": "assistant.delta"}})
	}
	// A frame that is not JSON is skipped, and the stream reads on.
	rig.fake.push(t, "not a frame")
	rig.fake.push(t, map[string]any{"type": "turn.ended", "seq": 21, "session_id": "session_1", "payload": map[string]any{"type": "turn.ended"}})
	waitFor(t, func() bool { return len(rig.received()) == 21 }, "every event arrives")
	for i, frame := range rig.received() {
		assert.Equal(t, int64(i+1), frame.Seq)
	}
	cursor, tracked := rig.cursor("session_1")
	require.True(t, tracked)
	assert.Equal(t, int64(21), cursor.Seq)
}

func TestKimiStreamSubscribe(t *testing.T) {
	t.Parallel()

	t.Run("adopts the cursor the ack states", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		rig.fake.store("session_1", fakeKapSession{})
		rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
			return 0, kimiAck{Accepted: sub.IDs, Cursors: map[string]kimiCursor{"session_1": {Seq: 7, Epoch: "ep_1"}}}
		})
		ack, err := rig.stream.subscribe(context.Background(), "session_1")
		require.NoError(t, err)
		assert.Equal(t, []string{"session_1"}, ack.Accepted)
		cursor, _ := rig.cursor("session_1")
		assert.Equal(t, kimiCursor{Seq: 7, Epoch: "ep_1"}, cursor)
		subscribes := rig.fake.subscribeFrames()
		require.Len(t, subscribes, 1)
		assert.Empty(t, subscribes[0].Cursors, "a first subscribe states no cursor")
	})

	t.Run("reports a session the server has not loaded", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		_, err := rig.stream.subscribe(context.Background(), "session_unknown")
		require.ErrorIs(t, err, errKimiSessionNotLoaded)
		_, tracked := rig.cursor("session_unknown")
		assert.False(t, tracked, "a session the server refused is not re-subscribed on a reconnect")
	})

	t.Run("reports a refused subscribe", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		rig.fake.setAckFor(func(fakeKapSubscribe) (int, kimiAck) { return 40300, kimiAck{} })
		_, err := rig.stream.subscribe(context.Background(), "session_1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "40300")
	})

	t.Run("returns when its context ends", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		release := make(chan struct{})
		rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
			<-release
			return 0, kimiAck{Accepted: sub.IDs}
		})
		t.Cleanup(func() { close(release) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := rig.stream.subscribe(ctx, "session_1")
		require.ErrorIs(t, err, context.Canceled)
	})

	// coder/websocket closes the whole connection when the context of a write
	// ends, and the stream carries every session of the agent. So a subscribe
	// whose caller gave up must write nothing. The library picks at random
	// whether such a write takes its lock, so one call proves little.
	t.Run("sends nothing and keeps the connection when its context ended", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for range 50 {
			_, err := rig.stream.subscribe(ctx, "session_1")
			require.ErrorIs(t, err, context.Canceled)
		}
		assert.Empty(t, rig.fake.subscribeFrames(), "no subscribe of a caller that gave up reaches the server")

		rig.fake.store("session_2", fakeKapSession{})
		_, err := rig.stream.subscribe(context.Background(), "session_2")
		require.NoError(t, err)
		assert.Equal(t, 1, rig.fake.connectionCount(), "the stream keeps its first connection")
	})

	t.Run("fails when the socket closes before the ack", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		release := make(chan struct{})
		rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
			<-release
			return 0, kimiAck{Accepted: sub.IDs}
		})
		done := make(chan error, 1)
		go func() {
			_, err := rig.stream.subscribe(context.Background(), "session_1")
			done <- err
		}()
		waitFor(t, func() bool { return len(rig.fake.subscribeFrames()) == 1 }, "the subscribe reaches the server")
		rig.fake.closeConnections()
		err := <-done
		close(release)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "closed before it acknowledged")
	})
}

func TestKimiStreamCursor(t *testing.T) {
	t.Parallel()

	stream := newKimiStream(nil, "test-agent", quartz.NewReal(), nil, nil)
	stream.sessions["session_1"] = kimiCursor{Seq: 5, Epoch: "ep_1"}

	stream.noteCursor(kimiFrame{SessionID: "session_1", Seq: 9, Epoch: "ep_1", Volatile: true})
	assert.Equal(t, kimiCursor{Seq: 5, Epoch: "ep_1"}, stream.sessions["session_1"], "a volatile event moves no cursor")

	stream.noteCursor(kimiFrame{SessionID: "session_1", Seq: 3, Epoch: "ep_1"})
	assert.Equal(t, kimiCursor{Seq: 5, Epoch: "ep_1"}, stream.sessions["session_1"], "an older seq moves no cursor")

	stream.noteCursor(kimiFrame{SessionID: "session_1", Seq: 6})
	assert.Equal(t, kimiCursor{Seq: 6, Epoch: "ep_1"}, stream.sessions["session_1"], "a frame with no epoch keeps the epoch")

	stream.noteCursor(kimiFrame{SessionID: "session_1", Seq: 2, Epoch: "ep_2"})
	assert.Equal(t, kimiCursor{Seq: 2, Epoch: "ep_2"}, stream.sessions["session_1"], "a new epoch restarts the numbering")

	stream.noteCursor(kimiFrame{SessionID: "session_2", Seq: 4})
	_, tracked := stream.sessions["session_2"]
	assert.False(t, tracked, "an event of a session nobody subscribed tracks nothing")

	stream.noteCursor(kimiFrame{Seq: 4})
	stream.noteCursor(kimiFrame{SessionID: "session_1"})
	assert.Equal(t, kimiCursor{Seq: 2, Epoch: "ep_2"}, stream.sessions["session_1"])

	stream.adoptAckCursors(kimiAck{Cursors: map[string]kimiCursor{
		"session_1": {Seq: 1, Epoch: "ep_2"},
		"session_3": {Seq: 1},
	}})
	assert.Equal(t, kimiCursor{Seq: 2, Epoch: "ep_2"}, stream.sessions["session_1"], "an ack behind the stream does not move it back")
	_, tracked = stream.sessions["session_3"]
	assert.False(t, tracked)
}

func TestKimiStreamReconnectsAndReplays(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	_, err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)
	rig.fake.push(t, map[string]any{"type": "turn.started", "seq": 4, "epoch": "ep_1", "session_id": "session_1", "payload": map[string]any{"type": "turn.started"}})
	waitFor(t, func() bool { return len(rig.received()) == 1 }, "the event arrives")

	rig.fake.closeConnections()
	waitFor(t, func() bool { return len(rig.fake.subscribeFrames()) == 2 }, "the stream re-subscribes on a new socket")
	resubscribe := rig.fake.subscribeFrames()[1]
	assert.Equal(t, []string{"session_1"}, resubscribe.IDs)
	assert.Equal(t, map[string]kimiCursor{"session_1": {Seq: 4, Epoch: "ep_1"}}, resubscribe.Cursors,
		"the re-subscribe states the last durable event, so the server replays only what the socket missed")

	waitFor(t, func() bool { return len(rig.reconnected()) == 1 }, "the reconnect handler runs once the server answered")
	assert.Equal(t, []kimiReconnect{{sessionID: "session_1", replayed: true}}, rig.reconnected(),
		"the server replayed every durable event, and only the volatile ones are lost")

	rig.fake.push(t, map[string]any{"type": "turn.ended", "seq": 5, "epoch": "ep_1", "session_id": "session_1", "payload": map[string]any{"type": "turn.ended"}})
	waitFor(t, func() bool { return len(rig.received()) == 2 }, "events flow on the new socket")
}

func TestKimiStreamReplaysASessionThatHadNoDurableEvent(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	_, err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	rig.fake.closeConnections()
	waitFor(t, func() bool { return len(rig.reconnected()) == 1 }, "the stream re-subscribes on a new socket")
	assert.Equal(t, map[string]kimiCursor{"session_1": {Epoch: fakeKapEpoch}}, rig.fake.subscribeFrames()[1].Cursors,
		"a cursor at seq 0 replays every event of the epoch, so the events of the gap are not lost")
	assert.Equal(t, []kimiReconnect{{sessionID: "session_1", replayed: true}}, rig.reconnected())
}

func TestKimiStreamReportsASessionWithNoCursorAsNotReplayed(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	// An ack that states no cursor leaves the stream nothing to replay from.
	rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) { return 0, kimiAck{Accepted: sub.IDs} })
	_, err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	rig.fake.closeConnections()
	waitFor(t, func() bool { return len(rig.reconnected()) == 1 }, "the stream re-subscribes on a new socket")
	assert.Empty(t, rig.fake.subscribeFrames()[1].Cursors)
	assert.Equal(t, []kimiReconnect{{sessionID: "session_1", replayed: false}}, rig.reconnected(),
		"the server subscribed the session again and replayed nothing")
}

func TestKimiStreamResyncsWhatItCannotReplay(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	rig.fake.store("session_2", fakeKapSession{})
	_, err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)
	_, err = rig.stream.subscribe(context.Background(), "session_2")
	require.NoError(t, err)

	// The server lists a session that it accepted but could not replay under
	// both accepted and resync_required.
	rig.fake.setAckFor(func(fakeKapSubscribe) (int, kimiAck) {
		return 0, kimiAck{Accepted: []string{"session_1"}, ResyncRequired: []string{"session_1"}, NotFound: []string{"session_2"}}
	})
	rig.fake.closeConnections()
	waitFor(t, func() bool { return len(rig.reconnected()) == 2 }, "both sessions resync")
	assert.Equal(t, []kimiReconnect{{sessionID: "session_1", replayed: false}, {sessionID: "session_2", replayed: false}}, rig.reconnected(),
		"each session is reported once, as not replayed")
}

// kimiGoroutineStacks returns the stack of every goroutine in the process.
func kimiGoroutineStacks() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

// kimiStreamLoops counts the goroutines in the process that run the reader
// loop or the dispatcher loop of an event stream.
func kimiStreamLoops() (readers, dispatchers int) {
	stacks := kimiGoroutineStacks()
	return strings.Count(stacks, "kimi.(*kimiStream).runReader("), strings.Count(stacks, "kimi.(*kimiStream).runDispatcher(")
}

// waitForKimiStreamLoops waits until at most readers readers and dispatchers
// dispatchers run. A goroutine that closes its done channel from a deferred call
// is still on its stack for a moment after, so the count can lag the close.
func waitForKimiStreamLoops(t *testing.T, readers, dispatchers int, msg string) {
	t.Helper()
	waitFor(t, func() bool {
		r, d := kimiStreamLoops()
		return r <= readers && d <= dispatchers
	}, msg)
}

// waitForStreamWait waits until stream.wait returns, which it does once every
// goroutine of the stream returned.
func waitForStreamWait(t *testing.T, stream *kimiStream) {
	t.Helper()
	waited := make(chan struct{})
	go func() {
		stream.wait()
		close(waited)
	}()
	waitFor(t, func() bool {
		select {
		case <-waited:
			return true
		default:
			return false
		}
	}, "wait returns once the goroutines of the stream return")
}

// TestKimiStreamCloseEndsItsGoroutines observes the goroutines themselves, so
// a close that only sets a flag does not pass it.
//
// It counts goroutines in the whole process, so it does not call t.Parallel:
// the streams of a parallel test would change the count. The parallel tests of
// the package start only after every sequential test ends.
func TestKimiStreamCloseEndsItsGoroutines(t *testing.T) {
	t.Run("while the reader reads", func(t *testing.T) {
		readers, dispatchers := kimiStreamLoops()
		rig := newKimiStreamRig(t)
		// start returns before its two goroutines run, and a goroutine that has not
		// run yet shows its closure on its stack rather than the loop. Wait until
		// both loops run, then check that each runs once.
		waitFor(t, func() bool {
			r, d := kimiStreamLoops()
			return r > readers && d > dispatchers
		}, "the stream starts its reader and its dispatcher")
		r, d := kimiStreamLoops()
		require.Equal(t, readers+1, r, "the stream runs one reader")
		require.Equal(t, dispatchers+1, d, "the stream runs one dispatcher")

		rig.stream.close()
		rig.stream.close()
		waitForStreamWait(t, rig.stream)
		waitForKimiStreamLoops(t, readers, dispatchers, "the reader and the dispatcher return once the stream closes")
		require.Error(t, rig.stream.write(map[string]any{"type": "noop"}), "a closed stream writes nothing")
		// An unsubscribe after the close only forgets the session.
		rig.stream.unsubscribe("session_1")
	})

	t.Run("while the reader waits to reconnect", func(t *testing.T) {
		readers, dispatchers := kimiStreamLoops()
		ctx := testutil.DeadlineContext(t)
		// The mock clock never advances, so only the close can end the wait.
		clock := testutil.NewQuartzMock(t)
		armed := clock.Trap().NewTimer(kimiStreamReconnectTimerTag)
		defer armed.Close()
		rig := newKimiStreamRigOnClock(t, clock, kimiStreamFirstBackoff)
		rig.fake.closeConnections()
		assert.Equal(t, kimiStreamFirstBackoff, testutil.WaitForTimer(t, ctx, armed), "the reader waits before it dials again")

		rig.stream.close()
		waitForStreamWait(t, rig.stream)
		waitForKimiStreamLoops(t, readers, dispatchers, "the close ends the wait, and both goroutines return")
		assert.Equal(t, 1, rig.fake.socketDialCount(), "the closed stream never dials again")
	})
}

func TestKimiStreamUnsubscribe(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	_, err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	rig.stream.unsubscribe("session_1")
	waitFor(t, func() bool { return len(rig.fake.unsubscribeFrames()) == 1 }, "the unsubscribe reaches the server")
	assert.Equal(t, [][]string{{"session_1"}}, rig.fake.unsubscribeFrames())
	_, tracked := rig.cursor("session_1")
	assert.False(t, tracked, "a reconnect does not re-subscribe it")
}

func TestKimiStreamStartFailsForARefusedSocket(t *testing.T) {
	t.Parallel()
	_, server := newFakeKap(t)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BearerAuth("wrong-token"))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	stream := newKimiStream(endpoint, "test-agent", quartz.NewReal(), func(kimiFrame) {}, nil)
	err = stream.start(context.Background(), 10*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open the Kimi event stream")
}

func TestKimiStreamStartAfterCloseFails(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BearerAuth(fakeKapToken))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	stream := newKimiStream(endpoint, "test-agent", quartz.NewReal(), func(kimiFrame) {}, nil)

	stream.close()
	require.ErrorIs(t, stream.start(context.Background(), 10*time.Second), errKimiStreamClosed)
	stream.wait()
	assert.Zero(t, fake.socketDialCount(), "a stream that its close ended dials nothing")
}

// A server that restarts refuses the socket for a moment. The reader keeps
// dialing until a dial succeeds, and the re-subscribe then runs as usual.
func TestKimiStreamKeepsDialingAfterAFailedReconnect(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	_, err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	rig.fake.refuseNextSockets(3)
	rig.fake.closeConnections()
	waitFor(t, func() bool { return len(rig.reconnected()) == 1 }, "the stream re-subscribes once a dial succeeds")
	assert.Equal(t, 5, rig.fake.socketDialCount(), "the first socket, three refused dials, and the one that succeeded")
	assert.Equal(t, []kimiReconnect{{sessionID: "session_1", replayed: true}}, rig.reconnected())
}

// Each refused dial doubles the wait before the next one, up to the cap. The
// test reads the delay that the reader ASKS the clock for, so the host's timer
// granularity cannot blur two steps.
func TestKimiStreamReconnectBackoffDoublesUpToItsCap(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	armed := clock.Trap().NewTimer(kimiStreamReconnectTimerTag)
	defer armed.Close()
	rig := newKimiStreamRigOnClock(t, clock, kimiStreamFirstBackoff)

	want := []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second,
		kimiStreamMaxBackoff, kimiStreamMaxBackoff, kimiStreamMaxBackoff,
	}
	rig.fake.refuseNextSockets(len(want) - 1)
	rig.fake.closeConnections()
	for i, delay := range want {
		require.Equal(t, delay, testutil.WaitForTimer(t, ctx, armed), "the wait before dial %d", i+1)
		clock.Advance(delay).MustWait(ctx)
	}
	waitFor(t, func() bool { return rig.fake.connectionCount() == 1 }, "the last dial opens a socket")
	assert.Equal(t, 1+len(want), rig.fake.socketDialCount(), "the first socket, the refused dials, and the one that succeeded")
}

// The server pings every 10 seconds, so a connection that sends nothing for the
// read timeout is dead. The reader drops it and reconnects, and the new socket
// delivers events.
func TestKimiStreamReconnectsAConnectionThatFallsSilent(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	reads := clock.Trap().AfterFunc(kimiStreamReadTimerTag)
	defer reads.Close()
	armed := clock.Trap().NewTimer(kimiStreamReconnectTimerTag)
	defer armed.Close()
	rig := newKimiStreamRigOnClock(t, clock, kimiStreamFirstBackoff)

	// The first read returns the fake's hello, and the second read then waits on
	// a socket that sends nothing more.
	//
	// The assertion runs only after both calls are released and the trap is
	// closed. A trapped call blocks the reader until the test releases it, so a
	// failed assertion that left one trapped would hang the cleanup's wait
	// instead of failing the test.
	var asked []time.Duration
	for range 2 {
		call := reads.MustWait(ctx)
		call.MustRelease(ctx)
		asked = append(asked, call.Duration)
	}
	reads.Close()
	require.Equal(t, []time.Duration{kimiStreamReadTimeout, kimiStreamReadTimeout}, asked)

	clock.Advance(kimiStreamReadTimeout - time.Nanosecond).MustWait(ctx)
	left, pending := clock.Peek()
	require.True(t, pending, "the read still waits")
	assert.Equal(t, time.Nanosecond, left, "a silence one nanosecond short of the timeout keeps the socket")
	assert.Equal(t, 1, rig.fake.socketDialCount())

	clock.Advance(time.Nanosecond).MustWait(ctx)
	assert.Equal(t, kimiStreamFirstBackoff, testutil.WaitForTimer(t, ctx, armed), "the silent socket is dropped, and the reader waits to reconnect")
	clock.Advance(kimiStreamFirstBackoff).MustWait(ctx)
	waitFor(t, func() bool { return rig.fake.connectionCount() == 2 }, "the reader opens a new socket")
	assert.Equal(t, 2, rig.fake.socketDialCount())

	rig.fake.push(t, map[string]any{"type": "turn.ended", "seq": 1, "session_id": "session_1", "payload": map[string]any{"type": "turn.ended"}})
	waitFor(t, func() bool { return len(rig.received()) == 1 }, "the new socket delivers events")
}

// A read that receives no frame for the read timeout fails with
// errKimiStreamSilent. A read whose caller's context ends fails with the
// context's error instead, so the log states the real cause.
func TestKimiStreamRead(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	_, server := newFakeKap(t)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BearerAuth(fakeKapToken))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	stream := newKimiStream(endpoint, "test-agent", clock, func(kimiFrame) {}, nil)

	// readAsync opens a socket, reads the fake's hello from it, and then starts a
	// second read, which waits: the fake sends nothing more. armed catches the
	// timer of that second read. The trap is set only after the hello, because a
	// trapped call blocks its caller, and the first read runs on this goroutine.
	readAsync := func(t *testing.T, ctx context.Context) <-chan error {
		t.Helper()
		conn, err := stream.dial(ctx, 10*time.Second)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.CloseNow() })
		data, err := stream.read(ctx, conn)
		require.NoError(t, err, "a frame that arrives in time is read")
		var hello kimiFrame
		require.NoError(t, json.Unmarshal(data, &hello))
		require.Equal(t, kimiFrameServerHello, hello.Type)

		armed := clock.Trap().AfterFunc(kimiStreamReadTimerTag)
		t.Cleanup(armed.Close)
		result := make(chan error, 1)
		go func() {
			_, err := stream.read(ctx, conn)
			result <- err
		}()
		call := armed.MustWait(ctx)
		// Release before the assertion: a call left trapped blocks the read, and a
		// failed test would then hang rather than fail.
		call.MustRelease(ctx)
		armed.Close()
		require.Equal(t, kimiStreamReadTimeout, call.Duration)
		return result
	}
	awaitResult := func(t *testing.T, result <-chan error) error {
		t.Helper()
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			require.FailNow(t, "the read never returned")
			return nil
		}
	}

	t.Run("a silent socket", func(t *testing.T) {
		result := readAsync(t, ctx)
		clock.Advance(kimiStreamReadTimeout).MustWait(ctx)
		require.ErrorIs(t, awaitResult(t, result), errKimiStreamSilent)
		_, pending := clock.Peek()
		assert.False(t, pending, "the failed read leaves no timer behind")
	})

	t.Run("a caller that gives up", func(t *testing.T) {
		readCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		result := readAsync(t, readCtx)
		cancel()
		err := awaitResult(t, result)
		require.Error(t, err)
		assert.NotErrorIs(t, err, errKimiStreamSilent)
		_, pending := clock.Peek()
		assert.False(t, pending, "the ended read leaves no timer behind")
	})
}

func TestKimiStreamResubscribeAll(t *testing.T) {
	t.Parallel()

	subscribed := func(t *testing.T, ids ...string) *kimiStreamRig {
		t.Helper()
		rig := newKimiStreamRig(t)
		for _, id := range ids {
			rig.fake.store(id, fakeKapSession{})
			_, err := rig.stream.subscribe(context.Background(), id)
			require.NoError(t, err)
		}
		return rig
	}

	t.Run("reports nothing when the server refuses the re-subscribe", func(t *testing.T) {
		t.Parallel()
		rig := subscribed(t, "session_1")
		rig.fake.setAckFor(func(fakeKapSubscribe) (int, kimiAck) { return 40300, kimiAck{} })
		rig.stream.resubscribeAll(context.Background())
		assert.Len(t, rig.fake.subscribeFrames(), 2, "the re-subscribe reached the server")
		assert.Empty(t, rig.reconnected(), "no session was re-subscribed, so none is reported")
	})

	t.Run("reports a session once when the ack lists it twice", func(t *testing.T) {
		t.Parallel()
		rig := subscribed(t, "session_1")
		rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
			return 0, kimiAck{Accepted: sub.IDs, ResyncRequired: sub.IDs, NotFound: sub.IDs}
		})
		rig.stream.resubscribeAll(context.Background())
		assert.Equal(t, []kimiReconnect{{sessionID: "session_1", replayed: false}}, rig.reconnected())
	})

	t.Run("re-subscribes every tracked session in one frame, in id order", func(t *testing.T) {
		t.Parallel()
		rig := subscribed(t, "session_b", "session_a")
		rig.stream.resubscribeAll(context.Background())
		frames := rig.fake.subscribeFrames()
		require.Len(t, frames, 3)
		assert.Equal(t, []string{"session_a", "session_b"}, frames[2].IDs)
		assert.Equal(t, []kimiReconnect{{sessionID: "session_a", replayed: true}, {sessionID: "session_b", replayed: true}}, rig.reconnected())
	})

	t.Run("sends nothing when no session is tracked", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		rig.stream.resubscribeAll(context.Background())
		assert.Empty(t, rig.fake.subscribeFrames())
		assert.Empty(t, rig.reconnected())
	})

	t.Run("runs no handler when the stream has none", func(t *testing.T) {
		t.Parallel()
		rig := subscribed(t, "session_1")
		rig.stream.reconnected = nil
		assert.NotPanics(t, func() { rig.stream.resubscribeAll(context.Background()) })
		assert.Len(t, rig.fake.subscribeFrames(), 2)
	})
}

// An ack whose payload does not decode fails its own subscribe. The real ack
// that arrives after it finds no subscribe waiting, and the stream reads on.
func TestKimiStreamReportsAnAckThatDoesNotDecode(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	rig.fake.store("session_2", fakeKapSession{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAck := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAck)
	rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) {
		<-release
		return 0, kimiAck{Accepted: sub.IDs}
	})

	done := make(chan error, 1)
	go func() {
		_, err := rig.stream.subscribe(context.Background(), "session_1")
		done <- err
	}()
	waitFor(t, func() bool { return len(rig.fake.subscribeFrames()) == 1 }, "the subscribe reaches the server")
	// The first subscribe of a stream carries the id 1.
	rig.fake.push(t, map[string]any{"type": kimiFrameAck, "id": "1", "payload": "not an ack"})
	require.ErrorContains(t, <-done, "decode the subscribe ack")

	releaseAck()
	_, err := rig.stream.subscribe(context.Background(), "session_2")
	require.NoError(t, err, "the late ack of the failed subscribe does not answer the next one")
	assert.Empty(t, rig.received(), "an ack is never an event")
}

// Each subscribe waits for the ack that carries its own id, so concurrent
// subscribes never take each other's answer.
func TestKimiStreamRoutesConcurrentSubscribes(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	const count = 16
	ids := make([]string, count)
	for i := range ids {
		ids[i] = "session_" + strconv.Itoa(i)
		rig.fake.store(ids[i], fakeKapSession{})
	}

	acks := make([]kimiAck, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			acks[i], errs[i] = rig.stream.subscribe(context.Background(), id)
		})
	}
	wg.Wait()
	for i, id := range ids {
		require.NoError(t, errs[i], id)
		assert.Equal(t, []string{id}, acks[i].Accepted, "the ack of %s answers its own subscribe", id)
		_, tracked := rig.cursor(id)
		assert.True(t, tracked, id)
	}
}

// A subscribe that fails leaves no session behind that it added: a reconnect
// would re-subscribe it, and the server would stream a session that no agent
// drives. A session that was tracked before the call stays tracked.
func TestKimiStreamSubscribeThatFailsTracksNoNewSession(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	_, err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	rig.fake.setAckFor(func(fakeKapSubscribe) (int, kimiAck) { return 40300, kimiAck{} })
	rig.fake.store("session_2", fakeKapSession{})
	_, err = rig.stream.subscribe(context.Background(), "session_2")
	require.Error(t, err)
	_, tracked := rig.cursor("session_2")
	assert.False(t, tracked, "the session that the failed subscribe added is not tracked")

	_, err = rig.stream.subscribe(context.Background(), "session_1")
	require.Error(t, err)
	_, tracked = rig.cursor("session_1")
	assert.True(t, tracked, "a failed subscribe of a tracked session keeps it for the next reconnect")

	// A resync subscribes the session that the agent drives again. A server that
	// still does not find it must not remove it from the next reconnect.
	rig.fake.setAckFor(func(sub fakeKapSubscribe) (int, kimiAck) { return 0, kimiAck{NotFound: sub.IDs} })
	_, err = rig.stream.subscribe(context.Background(), "session_1")
	require.ErrorIs(t, err, errKimiSessionNotLoaded)
	_, tracked = rig.cursor("session_1")
	assert.True(t, tracked, "a tracked session that the server did not find stays tracked")
}
