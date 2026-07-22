package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func relayTestFrame(correlationID uint64) *leapmuxv1.ChannelMessage {
	return &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-1",
		CorrelationId:   correlationID,
		Ciphertext:      []byte("payload"),
	}
}

// relayFrameOfSize builds a frame whose ciphertext is exactly n bytes, so
// a test can drive the byte budget without queueing millions of frames.
func relayFrameOfSize(correlationID uint64, n int) *leapmuxv1.ChannelMessage {
	return &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-1",
		CorrelationId:   correlationID,
		Ciphertext:      make([]byte, n),
	}
}

// TestRelayWriter_EnqueueDoesNotBlockOnTheSocket is the property the
// whole type exists for: the hub's per-worker read loop calls this
// inline, so it must return without touching the network. A synchronous
// write here let one browser with a full receive window stall the worker
// stream -- and the worker holds a process-global mutex across its Send,
// so that stalled every channel on that worker for every user.
func TestRelayWriter_EnqueueDoesNotBlockOnTheSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// No run() goroutine and a nil conn: if enqueue touched the socket
	// this would panic, and if it blocked the test would time out.
	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")
	for i := 0; i < 1000; i++ {
		require.NoError(t, w.enqueue(relayTestFrame(uint64(i))))
	}

	w.mu.Lock()
	queued := len(w.queue)
	w.mu.Unlock()
	assert.Equal(t, 1000, queued, "every frame is queued, none dropped")
}

// TestRelayWriter_DisconnectsAClientOverTheByteBudget pins what actually
// bounds memory. Ingress is unthrottled -- the hub's read loop hands
// frames over without blocking and terminal output is one frame per PTY
// read -- so a wedged client accumulates at the worker's full production
// rate. A time bound alone is not a memory bound when the rate is
// workload-controlled.
func TestRelayWriter_DisconnectsAClientOverTheByteBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")

	const chunk = 1 << 20 // 1 MiB
	var err error
	for i := 0; i < (relayMaxQueueBytes/chunk)+2; i++ {
		if err = w.enqueue(relayFrameOfSize(uint64(i), chunk)); err != nil {
			break
		}
	}

	require.ErrorIs(t, err, errRelayWriterClosed, "the budget must eventually reject")

	w.mu.Lock()
	queued := len(w.queue)
	bytes := w.queuedBytes
	w.mu.Unlock()
	assert.Zero(t, queued, "the backlog is released, not pinned, once the budget blows")
	assert.Zero(t, bytes)

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("blowing the byte budget must tear the connection down")
	}
}

// TestRelayWriter_ByteBudgetTracksDrainedFrames guards the other side of
// the accounting: a client that keeps up must never trip the budget, no
// matter how many bytes pass through in total.
func TestRelayWriter_ByteBudgetTracksDrainedFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")

	const chunk = 1 << 20
	for i := 0; i < (relayMaxQueueBytes/chunk)*3; i++ {
		require.NoError(t, w.enqueue(relayFrameOfSize(uint64(i), chunk)),
			"a drained queue must not accumulate toward the budget")
		_, ok := w.pop()
		require.True(t, ok)
	}

	w.mu.Lock()
	bytes := w.queuedBytes
	w.mu.Unlock()
	assert.Zero(t, bytes, "pop must return the bytes it removed to the budget")
}

// TestRelayWriter_DisconnectsAStalledClient pins the liveness bound: a
// backlog the client is not draining eventually gives up.
//
// The clock is advanced by run()'s own bookkeeping, not planted, because
// planting lastProgress is what made the original version of this test
// vacuous -- it asserted the check fires whenever the clock has moved,
// which is also true of an idle connection with nothing queued.
func TestRelayWriter_DisconnectsAStalledClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")

	// Queue is non-empty and stays that way: the clock starts when the
	// work arrives, so the stall is real backlog, not idleness.
	require.NoError(t, w.enqueue(relayTestFrame(1)))
	w.lastProgress = time.Now().Add(-relayMaxStall - time.Second)

	// conn is nil, proving the stall check short-circuits before any write.
	err := w.writeFrame(relayFrame{msg: relayTestFrame(1)})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no progress", "the error should name the stall")
}

// TestRelayWriter_IdleConnectionIsNotTreatedAsStalled is the regression
// for the inverse of the bound above.
//
// This socket carries no keepalive, so a tab whose agent is thinking and
// whose terminals are quiet writes nothing for minutes at a time. When
// the stall clock ran from the last successful write and was never
// restarted, the next frame after any such gap failed the check and tore
// down a perfectly healthy connection -- turning the most ordinary state
// in the product into a disconnect.
func TestRelayWriter_IdleConnectionIsNotTreatedAsStalled(t *testing.T) {
	received := make(chan *leapmuxv1.ChannelMessage, 1)
	srv, client, writer, cancel := newRelayWriterPair(t, received)
	defer cancel()
	defer srv.Close()
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

	// The gap lands between run's seed reading and the wake-up, which is
	// what an idle connection produces: nothing was queued while the
	// clock ran on, and the frame is written promptly once it arrives.
	start := time.Now()
	var mu sync.Mutex
	seeded := false
	writer.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if !seeded {
			seeded = true
			return start
		}
		return start.Add(10 * relayMaxStall)
	}

	go writer.run()

	require.NoError(t, writer.enqueue(relayTestFrame(1)))

	select {
	case got := <-received:
		assert.Equal(t, uint64(1), got.GetCorrelationId(),
			"the frame after an idle gap must be delivered")
	case <-time.After(5 * time.Second):
		t.Fatal("an idle connection was torn down instead of delivering its next frame")
	}
	assert.NoError(t, writer.ctx.Err(), "the connection must still be live")
}

// TestRelayWriter_LongBacklogSurvivesWhileItDrains is the regression the
// stall bound exists for. Measuring queue AGE instead would disconnect a
// client working steadily through a big page-refresh replay on a slow
// link -- and the reconnect would replay the same burst and age out
// again, so the workspace could never finish loading.
func TestRelayWriter_LongBacklogSurvivesWhileItDrains(t *testing.T) {
	received := make(chan *leapmuxv1.ChannelMessage, 8)
	srv, client, writer, cancel := newRelayWriterPair(t, received)
	defer cancel()
	defer srv.Close()
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

	// A clock that jumps a third of the stall budget on every reading, so
	// each frame sits far longer than relayMaxStall would allow if age
	// were what mattered.
	var mu sync.Mutex
	now := time.Now()
	writer.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(relayMaxStall / 3)
		return now
	}
	writer.lastProgress = now

	for i := 0; i < 6; i++ {
		require.NoError(t, writer.writeFrame(relayFrame{msg: relayTestFrame(uint64(i))}),
			"a client still making progress must not be disconnected, however old the backlog")
	}
}

// TestRelayWriter_PreservesFrameOrder pins that queueing did not turn an
// ordered stream into an unordered one -- the ciphertext stream depends
// on it.
func TestRelayWriter_PreservesFrameOrder(t *testing.T) {
	received := make(chan *leapmuxv1.ChannelMessage, 64)
	srv, client, writer, cancel := newRelayWriterPair(t, received)
	defer cancel()
	defer srv.Close()
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

	go writer.run()

	const frames = 32
	for i := 0; i < frames; i++ {
		require.NoError(t, writer.enqueue(relayTestFrame(uint64(i))))
	}

	for i := 0; i < frames; i++ {
		select {
		case got := <-received:
			require.Equal(t, uint64(i), got.GetCorrelationId(), "frames must arrive in enqueue order")
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d frames arrived", i, frames)
		}
	}
}

// TestRelayWriter_EnqueueAfterCloseReports pins that a caller handing
// frames to a torn-down connection learns about it, so channelmgr stops
// routing to a dead sender instead of silently accumulating frames.
func TestRelayWriter_EnqueueAfterCloseReports(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")
	require.NoError(t, w.enqueue(relayTestFrame(1)))
	w.close()

	assert.ErrorIs(t, w.enqueue(relayTestFrame(2)), errRelayWriterClosed)

	w.mu.Lock()
	queued := len(w.queue)
	w.mu.Unlock()
	assert.Zero(t, queued, "close discards the backlog rather than pinning it")
}

// TestRelayWriter_CloseAloneReapsTheDrainGoroutine pins that close is
// self-sufficient. It used to touch only the closed flag and the queue,
// so run stayed parked on wake until something ELSE cancelled the
// context -- a coupling nothing in the type enforced. Any owner that
// held the writer's lifetime without holding its context would have
// leaked one goroutine, the websocket, and every frame it pinned.
func TestRelayWriter_CloseAloneReapsTheDrainGoroutine(t *testing.T) {
	// Deliberately NOT cancelled: close must be enough on its own.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")

	done := make(chan struct{})
	go func() { defer close(done); w.run() }()

	// Let run reach its select before closing, so this exercises the
	// parked-on-wake path rather than a pre-closed shortcut.
	time.Sleep(50 * time.Millisecond)
	w.close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not wake the drain goroutine")
	}
}

// TestRelayWriter_RunCancelsTheConnectionWhenItGivesUp pins the teardown
// path: the writer owns the socket, so when it stops writing it must
// unwind ServeHTTP rather than leave a bound-but-dead connection that
// channelmgr keeps routing frames to.
func TestRelayWriter_RunCancelsTheConnectionWhenItGivesUp(t *testing.T) {
	received := make(chan *leapmuxv1.ChannelMessage, 1)
	srv, client, writer, cancel := newRelayWriterPair(t, received)
	defer cancel()
	defer srv.Close()
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

	// A clock that stays put until the drain loop has picked up the work
	// and restarted the stall clock, then jumps past the budget -- i.e. a
	// client that accepted the backlog and then stopped draining it.
	// Freezing it earlier would not do: run seeds lastProgress on entry
	// AND on each wake-up, so both readings must be inside the quiet
	// window for the jump to represent unwritten backlog rather than
	// idleness.
	var mu sync.Mutex
	start := time.Now()
	readings := 0
	writer.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		readings++
		if readings <= 2 {
			return start
		}
		return start.Add(2 * relayMaxStall)
	}

	done := make(chan struct{})

	require.NoError(t, writer.enqueue(relayTestFrame(1)))
	go func() { defer close(done); writer.run() }()

	select {
	case <-writer.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not cancel the connection after giving up")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}

	assert.Empty(t, received, "the stalled frame must not have been written")
}

// TestRelayWriter_WriteTimeoutTearsDownAWedgedClient covers the other
// half of the writer's bound, which nothing exercised before: a client
// that accepts the connection and then never reads. Without the per-write
// timeout the drain goroutine parks inside the write forever, which is
// precisely the wedge the whole type exists to remove.
func TestRelayWriter_WriteTimeoutTearsDownAWedgedClient(t *testing.T) {
	// A raw TCP peer that completes the websocket handshake and then
	// never reads, so the kernel send buffer fills and the write blocks.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srvConnCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, acceptErr := websocket.Accept(w, r, nil)
		if acceptErr != nil {
			return
		}
		srvConnCh <- c
		<-ctx.Done()
	}))
	srv.Start()
	defer srv.Close()

	client, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()
	// Never read from client: that is the wedge.

	var conn *websocket.Conn
	select {
	case conn = <-srvConnCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not accept the websocket")
	}

	w := newRelayWriter(ctx, conn, cancel, "user-1", "conn-1")

	done := make(chan struct{})
	go func() { defer close(done); w.run() }()

	// Enough frames to overrun the socket buffers so a write genuinely
	// blocks, but far short of the byte budget so THIS bound is what
	// fires.
	go func() {
		for i := 0; i < 4096; i++ {
			if w.enqueue(relayFrameOfSize(uint64(i), 4096)) != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(relayWriteTimeout + 20*time.Second):
		t.Fatal("a wedged client was never timed out; the drain goroutine is stuck in a write")
	}
}

// newRelayWriterPair stands up a real websocket pair and returns a
// relayWriter bound to the server side. Every message the server writes
// is decoded onto received.
func newRelayWriterPair(t *testing.T, received chan *leapmuxv1.ChannelMessage) (
	*httptest.Server, *websocket.Conn, *relayWriter, context.CancelFunc,
) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	var (
		mu       sync.Mutex
		srvConn  *websocket.Conn
		accepted = make(chan struct{})
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		srvConn = c
		mu.Unlock()
		close(accepted)
		<-ctx.Done()
	}))

	client, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	client.SetReadLimit(channelwire.WSReadLimit)

	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not accept the websocket")
	}

	go func() {
		for {
			msg, err := channelwire.ReadChannelMessage(ctx, client)
			if err != nil {
				return
			}
			select {
			case received <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()

	mu.Lock()
	conn := srvConn
	mu.Unlock()
	return srv, client, newRelayWriter(ctx, conn, cancel, "user-1", "conn-1"), cancel
}

// TestRelayWriter_ChargesPerFrameOverhead pins the slot bound.
//
// Charging only ciphertext made a frame carrying little or none free, so
// the queue length was unbounded even though its bytes were capped --
// each slot still pins a *ChannelMessage. Counting a fixed overhead per
// frame is what turns the byte budget into a bound on both.
func TestRelayWriter_ChargesPerFrameOverhead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")
	const frames = 10
	for i := 0; i < frames; i++ {
		require.NoError(t, w.enqueue(relayFrameOfSize(uint64(i), 0)))
	}

	w.mu.Lock()
	queued := w.queuedBytes
	w.mu.Unlock()
	assert.Equal(t, frames*relayFrameOverhead, queued,
		"an empty-ciphertext frame must still cost its slot")
}

// TestRelayWriter_OverBudgetTeardownMatchesClose pins that the two ways a
// writer stops agree on what they reset.
//
// The byte-budget kill duplicated close's teardown, and had already
// drifted from it: it never signalled wake, so it depended on the very
// external cancel that close's own contract exists to stop relying on.
func TestRelayWriter_OverBudgetTeardownMatchesClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, nil, cancel, "user-1", "conn-1")
	// One frame over the budget in a single enqueue.
	err := w.enqueue(relayFrameOfSize(1, relayMaxQueueBytes))

	require.ErrorIs(t, err, errRelayWriterClosed)
	assert.True(t, w.isClosed(), "the budget kill must close the writer")

	w.mu.Lock()
	queued, queuedBytes := len(w.queue), w.queuedBytes
	w.mu.Unlock()
	assert.Zero(t, queued, "the backlog must be discarded, as close does")
	assert.Zero(t, queuedBytes, "the byte count must be reset, as close does")

	select {
	case <-w.wake:
	default:
		t.Fatal("the budget kill must wake the drain goroutine rather than rely on the context")
	}
}
