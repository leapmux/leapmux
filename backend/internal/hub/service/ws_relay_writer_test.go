package service

import (
	"context"
	"errors"
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
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/prometheus/client_golang/prometheus"
)

func relayTestFrame(correlationID uint64) *leapmuxv1.ChannelMessage {
	return &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-1",
		CorrelationId:   correlationID,
		Ciphertext:      []byte("payload"),
	}
}

func relayFrameOfSize(correlationID uint64, n int) *leapmuxv1.ChannelMessage {
	return &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-1",
		CorrelationId:   correlationID,
		Ciphertext:      make([]byte, n),
	}
}

// TestRelayWriter_ConfigMatchesSendqContract pins the hub-specific
// constants the thin wrapper hands to sendq. Generic queue behaviour
// lives in sendq_test.go; this is the wiring that must not drift.
func TestRelayWriter_ConfigMatchesSendqContract(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(256), sendq.DefaultFrameOverhead)
	assert.Equal(t, 10*time.Second, relayWriteTimeout)
	assert.Equal(t, 30*time.Second, relayMaxStall)
}

// TestRelayWriter_DrawsItsByteBudgetFromThePool pins that the relay has no
// per-connection byte constant left to drift from the pool: a writer given a
// small pool must be bounded by THAT number, which is the whole point of
// replacing the old 32 MiB constant with a shared budget.
func TestRelayWriter_DrawsItsByteBudgetFromThePool(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const capacity = 4 << 20 // 4 MiB -- far below the retired 32 MiB constant
	pool := sendq.NewPool(sendq.PoolConfig{Capacity: capacity})
	w := newRelayWriter(ctx, pool, parkForeverWrite, cancel, "user-1", "conn-1")

	const chunk = 256 << 10
	var (
		err  error
		peak int64
	)
	for i := 0; err == nil && i < (capacity/chunk)+4; i++ {
		if err = w.enqueue(relayFrameOfSize(uint64(i), chunk)); err == nil {
			// Read WHILE the frames are still queued. Sampling after the loop
			// measures nothing: the enqueue that fails tears the writer down,
			// which discards its queue, refunds every byte and detaches -- so
			// pool.Used() is exactly 0 by then and the ceiling assertion below
			// could not fail however badly admission had overshot.
			peak = max(peak, pool.Used())
		}
	}

	require.ErrorIs(t, err, errRelayWriterClosed,
		"the pool's capacity, not a per-connection constant, must bound the queue")
	assert.Positive(t, peak, "the writer must actually have charged the pool")
	assert.LessOrEqual(t, peak, int64(capacity),
		"a lone writer must never charge the pool past its capacity")
}

// TestRelayWriter_LeavesThePoolCleanOnClose pins that a connection returns its
// charge and its membership. A leak here shrinks every surviving connection's
// guaranteed floor for the life of the process, and would only be visible as
// unexplained disconnects days later.
func TestRelayWriter_LeavesThePoolCleanOnClose(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool := sendq.NewMaxBytesPoolForTest()
	w := newRelayWriter(ctx, pool, parkForeverWrite, cancel, "user-1", "conn-1")
	// TWO frames, not one. The charge is released at pop, before the write, so
	// with a single frame the drain goroutine can take it and drop the total
	// back to zero before the precondition below reads it -- which made this
	// test's "there was something to return" fail intermittently, reporting a
	// leak-free writer as broken. parkForeverWrite blocks inside the first
	// write, so exactly one frame can ever be popped and the second is still
	// charged whenever this runs.
	require.NoError(t, w.enqueue(relayFrameOfSize(1, 64<<10)))
	require.NoError(t, w.enqueue(relayFrameOfSize(2, 64<<10)))
	require.Positive(t, pool.Used())

	// Closed twice on purpose: production does exactly this -- the drain
	// goroutine's deferred Close plus the handler's own -- and a
	// non-idempotent detach would double-count both ledgers.
	w.close()
	w.close()

	assert.Zero(t, pool.Used(), "a closed connection must return every charged byte")
	assert.Zero(t, pool.Members(), "a closed connection must return its pool slot")
}

// TestRelayWriter_EnqueueDoesNotBlockOnTheSocket is the property the
// whole type exists for: the hub's per-worker read loop calls this
// inline, so it must return without touching the network. A peer that
// never reads makes the drain goroutine park on Write; enqueue must
// still return promptly.
func TestRelayWriter_EnqueueDoesNotBlockOnTheSocket(t *testing.T) {
	t.Parallel()

	received := make(chan *leapmuxv1.ChannelMessage)
	// received is unbuffered and nobody reads it -- but the client's
	// read loop is what matters for backpressure. Use a pair and stop
	// the client from draining by closing received and not consuming.
	// Leave the client open but stop its reader from freeing the receive
	// window: nothing consumes received, so the reader goroutine blocks on
	// send and the socket eventually fills. Enqueue itself must not wait.
	writer := newRelayWriterPair(t, received)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			if err := writer.enqueue(relayTestFrame(uint64(i))); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked on the socket")
	}
}

// parkForeverWrite is the test Write stub for budget/teardown tests that
// do not need a live socket: the drain parks until cancelled so enqueue
// accounting can be exercised without freeing budget via a successful write.
func parkForeverWrite(ctx context.Context, _ *leapmuxv1.ChannelMessage) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestRelayWriter_DisconnectsAClientOverTheByteBudget pins that the
// hub wrapper's OnGiveUp cancels the connection when sendq blows the
// budget -- the hub-specific teardown path, not the generic accounting.
func TestRelayWriter_DisconnectsAClientOverTheByteBudget(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, sendq.NewMaxBytesPoolForTest(), parkForeverWrite, cancel, "user-1", "conn-1")

	const chunk = 1 << 20 // 1 MiB
	frames := int(sendq.DefaultMaxBytes / int64(chunk))
	var (
		err      error
		admitted int
	)
	for i := 0; i < frames+2; i++ {
		if err = w.enqueue(relayFrameOfSize(uint64(i), chunk)); err != nil {
			break
		}
		admitted++
	}

	require.ErrorIs(t, err, errRelayWriterClosed, "the budget must eventually reject")
	// It has to reject at the budget this test NAMES, not at some fraction of
	// it. A lone member used to settle at about half its pool, so this tripped
	// at 16 of the 32 frames the loop bound is written for and read as
	// exercising a saturated budget while only ever reaching the dynamic
	// branch. The floors NewMaxBytesPoolForTest pins are what make the bound
	// real; one frame of slack for the drain goroutine's pop, which frees its
	// charge before the write it then parks in.
	assert.GreaterOrEqual(t, admitted, frames-1,
		"a lone writer must be granted the whole DefaultMaxBytes, not a share of it")
	assert.True(t, w.inner.IsClosed())
	assert.Zero(t, w.inner.QueuedLen(), "the backlog is released once the budget blows")
	assert.Zero(t, w.inner.QueuedBytes())

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("blowing the byte budget must tear the connection down")
	}
}

// TestRelayWriter_EnqueueAfterCloseReports pins that a caller handing
// frames to a torn-down connection learns about it via the hub's closed
// sentinel, so channelmgr stops routing to a dead sender.
func TestRelayWriter_EnqueueAfterCloseReports(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, sendq.NewMaxBytesPoolForTest(), parkForeverWrite, cancel, "user-1", "conn-1")
	require.NoError(t, w.enqueue(relayTestFrame(1)))
	w.close()

	assert.ErrorIs(t, w.enqueue(relayTestFrame(2)), errRelayWriterClosed)
	assert.Zero(t, w.inner.QueuedLen(), "close discards the backlog rather than pinning it")
}

// TestRelayWriter_MapsSendqClosedToHubSentinel pins that callers see the
// hub-local sentinel, not sendq.ErrClosed -- channelmgr and BindUser
// match on errRelayWriterClosed.
func TestRelayWriter_MapsSendqClosedToHubSentinel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRelayWriter(ctx, sendq.NewMaxBytesPoolForTest(), parkForeverWrite, cancel, "user-1", "conn-1")
	w.close()
	err := w.enqueue(relayTestFrame(1))
	require.ErrorIs(t, err, errRelayWriterClosed)
	assert.False(t, errors.Is(err, sendq.ErrClosed),
		"enqueue must map sendq.ErrClosed to errRelayWriterClosed, not wrap it")
}

// TestRelayWriter_PreservesFrameOrder pins that the hub wrapper still
// delivers an ordered stream through a live websocket -- the end-to-end
// wiring sendq alone cannot assert.
func TestRelayWriter_PreservesFrameOrder(t *testing.T) {
	t.Parallel()

	received := make(chan *leapmuxv1.ChannelMessage, 64)
	writer := newRelayWriterPair(t, received)

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

// newRelayWriterPair stands up a real websocket pair and returns a
// relayWriter bound to the server side. Every message the server writes
// is decoded onto received.
//
// Teardown is owned here, in one t.Cleanup with an explicit order, and the
// order matters. The server handler parks on <-ctx.Done(), so closing the
// CLIENT first writes a close frame to a peer that will never reply, and
// coder/websocket then blocks the full 5s closing-handshake timeout: a flat
// 5s of dead wall time per test. Cancelling first releases the handler, so
// the client's handshake completes immediately.
func newRelayWriterPair(t *testing.T, received chan *leapmuxv1.ChannelMessage) *relayWriter {
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
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))

	client, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	require.NoError(t, err)
	client.SetReadLimit(channelwire.WSReadLimit)

	t.Cleanup(func() {
		cancel()
		_ = client.Close(websocket.StatusNormalClosure, "")
		srv.Close()
	})

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
	write := func(writeCtx context.Context, msg *leapmuxv1.ChannelMessage) error {
		return channelwire.WriteChannelMessage(writeCtx, conn, msg)
	}
	return newRelayWriter(ctx, sendq.NewMaxBytesPoolForTest(), write, cancel, "user-1", "conn-1")
}

// TestRelayWriter_AWedgedTabDoesNotDropAHealthyOne is the property an operator
// cares about: two browser tabs sharing the Hub's budget, one of them wedged.
//
// The naive shared-pool rule -- refuse whoever asks once the budget is full --
// drops whichever connection sends NEXT. Terminal output is one frame per PTY
// read, so that is every active tab at once, each of which reconnects and
// replays from the DB and refills the pool. The healthy tab must survive, and
// the memory must come from the connection that is actually holding it.
func TestRelayWriter_AWedgedTabDoesNotDropAHealthyOne(t *testing.T) {
	t.Parallel()

	pool := sendq.NewPool(sendq.PoolConfig{Capacity: 8 << 20})

	wedgedCtx, wedgedCancel := context.WithCancel(context.Background())
	defer wedgedCancel()
	wedged := newRelayWriter(wedgedCtx, pool, parkForeverWrite, wedgedCancel, "user-1", "wedged")

	healthyCtx, healthyCancel := context.WithCancel(context.Background())
	defer healthyCancel()
	healthy := newRelayWriter(healthyCtx, pool, parkForeverWrite, healthyCancel, "user-2", "healthy")

	// Fill the wedged tab right up to what the pool will grant it. enqueue is
	// the production path, so it stops by tearing the tab down -- which is the
	// correct outcome for the tab that is hoarding, and exactly what must NOT
	// happen to the other one.
	for i := 0; wedged.enqueue(relayFrameOfSize(uint64(i), 256<<10)) == nil; i++ {
	}
	require.True(t, wedged.inner.IsClosed(), "the hoarding tab is the one that goes")

	// Refill the pool from OTHER connections, because the give-up above refunded
	// everything the wedged tab held. Without this the healthy tab meets an
	// EMPTY pool and is admitted by any rule at all -- including the naive
	// "refuse whoever asks next" this test exists to rule out, which is why it
	// used to pass against both.
	var ballast []*sendq.SharedMember
	t.Cleanup(func() {
		for _, m := range ballast {
			held := m.Charged()
			m.Release(held, held)
			m.Detach()
		}
	})
	for pool.Used() < int64(8<<20) {
		m := pool.AttachShared(func(error) bool { return true })
		ballast = append(ballast, m)
		if m.Admit(1<<20, 1<<20) != sendq.Admitted {
			break
		}
	}
	require.GreaterOrEqual(t, pool.Used(), int64(8<<20),
		"the healthy tab must be asking a pool with nothing left")

	// The healthy tab keeps delivering anyway: its guaranteed working set is
	// what a full pool may not take from it.
	for i := range 32 {
		require.NoError(t, healthy.enqueue(relayFrameOfSize(uint64(i), 8<<10)),
			"a tab with a near-empty queue must never be dropped for another tab's backlog")
	}
	assert.False(t, healthy.inner.IsClosed())
	select {
	case <-healthyCtx.Done():
		t.Fatal("the healthy tab's connection was cancelled")
	default:
	}
}

// TestRelayWriter_RefusesToBuildWithoutABudget pins that a composition which
// forgot the pool fails at wiring time.
//
// The failure it prevents is silent: a nil pool would mean an unbounded
// per-connection queue -- precisely the defect the pool exists to close -- and
// nothing about a running Hub would look wrong until it ran out of memory.
func TestRelayWriter_RefusesToBuildWithoutABudget(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	assert.PanicsWithValue(t, "newRelayWriter: pool is required", func() {
		newRelayWriter(ctx, nil, parkForeverWrite, cancel, "user-1", "conn-1")
	})
	assert.PanicsWithValue(t, "newRelayWriter: write is required", func() {
		newRelayWriter(ctx, sendq.NewMaxBytesPoolForTest(), nil, cancel, "user-1", "conn-1")
	})
}

// The `pool` label on leapmux_sendq_giveups_total must use the SAME vocabulary
// as leapmux_sendq_pool_*, because the two describe the same three classes of
// connection and an operator correlating "which pool is under pressure" with
// "which connections are being dropped" has to join them.
//
// They used to disagree: `writer="channel_relay"` against `pool="relay"`, and
// `writer="worker_conn"` against `pool="worker"`. Only the third pair matched
// literally, so a dashboard built on the obvious join silently produced no rows
// for the two classes most likely to page someone. Bare literals at each call
// site are how that happened, so this asserts the values themselves rather than
// trusting the constants to be used.
func TestRelayGiveUpsUseThePoolLabelVocabulary(t *testing.T) {
	t.Parallel()

	// Drive a real give-up so the series exists.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := sendq.NewPool(sendq.PoolConfig{Capacity: 1 << 20})
	w := newRelayWriter(ctx, pool, parkForeverWrite, cancel, "user-1", "conn-1")
	for w.enqueue(relayFrameOfSize(1, 256<<10)) == nil { //nolint:revive // filling to the ceiling is the point
	}
	require.True(t, w.inner.IsClosed(), "precondition: the writer must have given up")

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	allowed := map[string]bool{
		metrics.PoolRelay:      true,
		metrics.PoolWorker:     true,
		metrics.PoolUserEvents: true,
	}
	seen := 0
	for _, f := range families {
		if f.GetName() != "leapmux_sendq_giveups_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() != "pool" {
					continue
				}
				seen++
				assert.True(t, allowed[l.GetValue()],
					"give-up label %q is not one of the pool names, so it cannot be joined to leapmux_sendq_pool_*",
					l.GetValue())
			}
		}
	}
	assert.Positive(t, seen, "the give-up series must exist for this assertion to mean anything")
}
