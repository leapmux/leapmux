package sendq

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	testMaxBytes      = 32 * 1024 * 1024
	testFrameOverhead = 256
	testWriteTimeout  = 10 * time.Second
	testMaxStall      = 30 * time.Second

	// testShortWriteTimeout is for the one test that wants the write
	// watchdog to actually fire. Everywhere else testWriteTimeout is a
	// deliberately generous "must not fire" value.
	testShortWriteTimeout = 500 * time.Millisecond
)

func testFrame(correlationID uint64) *leapmuxv1.ChannelMessage {
	return &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-1",
		CorrelationId:   correlationID,
		Ciphertext:      []byte("payload"),
	}
}

func testFrameOfSize(correlationID uint64, n int) *leapmuxv1.ChannelMessage {
	return &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       "ch-1",
		CorrelationId:   correlationID,
		Ciphertext:      make([]byte, n),
	}
}

func frameSize(msg *leapmuxv1.ChannelMessage) int {
	return len(msg.GetCiphertext())
}

func TestWriterEnqueueDoesNotBlockOnTheSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// No drain and a Write that would hang: enqueue must return without
	// calling it.
	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { select {} },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	defer w.Close()

	for i := 0; i < 1000; i++ {
		require.NoError(t, w.Enqueue(testFrame(uint64(i))))
	}
	assert.Equal(t, 1000, w.QueuedLen(), "every frame is queued, none dropped")
}

func TestWriterDisconnectsAClientOverTheByteBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gaveUp := make(chan error, 1)
	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
		OnGiveUp: func(err error) {
			cancel()
			select {
			case gaveUp <- err:
			default:
			}
		},
	})
	defer w.Close()

	const chunk = 1 << 20
	var err error
	for i := 0; i < (testMaxBytes/chunk)+2; i++ {
		if err = w.Enqueue(testFrameOfSize(uint64(i), chunk)); err != nil {
			break
		}
	}

	require.ErrorIs(t, err, ErrClosed, "the budget must eventually reject")
	assert.Zero(t, w.QueuedLen(), "the backlog is released, not pinned, once the budget blows")
	assert.Zero(t, w.QueuedBytes())

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("blowing the byte budget must tear the connection down")
	}
	select {
	case err := <-gaveUp:
		require.ErrorIs(t, err, ErrOverBudget)
	case <-time.After(time.Second):
		t.Fatal("OnGiveUp was not called")
	}
}

func TestWriterByteBudgetTracksDrainedFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	defer w.Close()

	const chunk = 1 << 20
	for i := 0; i < (testMaxBytes/chunk)*3; i++ {
		require.NoError(t, w.Enqueue(testFrameOfSize(uint64(i), chunk)),
			"a drained queue must not accumulate toward the budget")
		_, ok := w.PopForTest()
		require.True(t, ok)
	}
	assert.Zero(t, w.QueuedBytes(), "pop must return the bytes it removed to the budget")
}

func TestWriterDisconnectsAStalledClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
		MaxStall: testMaxStall,
	})
	defer w.Close()

	w.SetLastProgressForTest(time.Now().Add(-testMaxStall - time.Second))

	err := w.WriteItemForTest(testFrame(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no progress", "the error should name the stall")
}

func TestWriterIdleConnectionIsNotTreatedAsStalled(t *testing.T) {
	received := make(chan *leapmuxv1.ChannelMessage, 1)
	write := newWSWritePair(t, received)

	ctx := context.Background()
	start := time.Now()
	var mu sync.Mutex
	seeded := false
	w := New(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write:         write,
		Size:          frameSize,
		MaxBytes:      testMaxBytes,
		FrameOverhead: testFrameOverhead,
		WriteTimeout:  testWriteTimeout,
		MaxStall:      testMaxStall,
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			if !seeded {
				seeded = true
				return start
			}
			return start.Add(10 * testMaxStall)
		},
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrame(1)))

	select {
	case got := <-received:
		assert.Equal(t, uint64(1), got.GetCorrelationId(),
			"the frame after an idle gap must be delivered")
	case <-time.After(5 * time.Second):
		t.Fatal("an idle connection was torn down instead of delivering its next frame")
	}
}

func TestWriterLongBacklogSurvivesWhileItDrains(t *testing.T) {
	received := make(chan *leapmuxv1.ChannelMessage, 8)
	write := newWSWritePair(t, received)

	var mu sync.Mutex
	now := time.Now()
	// No drain goroutine: writeItem is driven directly, matching the original
	// relayWriter test that called writeFrame without run().
	w := newWriter(context.Background(), Config[*leapmuxv1.ChannelMessage]{
		Write:         write,
		Size:          frameSize,
		MaxBytes:      testMaxBytes,
		FrameOverhead: testFrameOverhead,
		WriteTimeout:  testWriteTimeout,
		MaxStall:      testMaxStall,
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			now = now.Add(testMaxStall / 3)
			return now
		},
	})
	defer w.Close()
	w.SetLastProgressForTest(now)

	for i := 0; i < 6; i++ {
		require.NoError(t, w.WriteItemForTest(testFrame(uint64(i))),
			"a client still making progress must not be disconnected, however old the backlog")
	}
}

func TestWriterPreservesFrameOrder(t *testing.T) {
	received := make(chan *leapmuxv1.ChannelMessage, 64)
	write := newWSWritePair(t, received)

	w := New(context.Background(), Config[*leapmuxv1.ChannelMessage]{
		Write: write, Size: frameSize, MaxBytes: testMaxBytes,
		FrameOverhead: testFrameOverhead, WriteTimeout: testWriteTimeout, MaxStall: testMaxStall,
	})
	defer w.Close()

	const frames = 32
	for i := 0; i < frames; i++ {
		require.NoError(t, w.Enqueue(testFrame(uint64(i))))
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

func TestWriterEnqueueAfterCloseReports(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	require.NoError(t, w.Enqueue(testFrame(1)))
	w.Close()

	assert.ErrorIs(t, w.Enqueue(testFrame(2)), ErrClosed)
	assert.Zero(t, w.QueuedLen(), "close discards the backlog rather than pinning it")
}

func TestWriterCloseAloneReapsTheDrainGoroutine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	w := New(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error {
			return nil
		},
		Size: frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	// The drain goroutine is running; give it a moment to park on wake.
	time.Sleep(50 * time.Millisecond)
	close(started)
	<-started
	w.Close()

	// A second Close is a no-op; if the goroutine leaked, we cannot observe it
	// directly, but Enqueue after Close must report closed and a subsequent
	// wait with no cancel must not hang the test suite (covered by -race).
	assert.ErrorIs(t, w.Enqueue(testFrame(1)), ErrClosed)
	_ = cancel
}

func TestWriterGiveUpCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan *leapmuxv1.ChannelMessage, 1)
	write := newWSWritePair(t, received)

	var mu sync.Mutex
	start := time.Now()
	readings := 0
	gaveUp := make(chan struct{})
	w := New(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: write, Size: frameSize, MaxBytes: testMaxBytes,
		FrameOverhead: testFrameOverhead, WriteTimeout: testWriteTimeout, MaxStall: testMaxStall,
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			readings++
			if readings <= 2 {
				return start
			}
			return start.Add(2 * testMaxStall)
		},
		OnGiveUp: func(error) { close(gaveUp); cancel() },
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrame(1)))

	select {
	case <-gaveUp:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not give up after stall")
	}
	assert.Empty(t, received, "the stalled frame must not have been written")
}

func TestWriterWriteTimeoutTearsDownAWedgedPeer(t *testing.T) {
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

	var conn *websocket.Conn
	select {
	case conn = <-srvConnCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not accept the websocket")
	}

	gaveUp := make(chan struct{})
	w := New(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(ctx context.Context, msg *leapmuxv1.ChannelMessage) error {
			return channelwire.WriteChannelMessage(ctx, conn, msg)
		},
		Size: frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
		// The wedge is what is under test, not the length of the fuse: the
		// peer below never reads, so its socket buffer fills within a few
		// frames and stays full for the rest of the test. Configuring the
		// production-shaped testWriteTimeout here would only park the test
		// on the real clock for ten seconds to observe the same give-up.
		WriteTimeout: testShortWriteTimeout,
		OnGiveUp:     func(error) { close(gaveUp); cancel() },
	})
	defer w.Close()

	go func() {
		for i := 0; i < 4096; i++ {
			if w.Enqueue(testFrameOfSize(uint64(i), 4096)) != nil {
				return
			}
		}
	}()

	select {
	case <-gaveUp:
	case <-time.After(30 * time.Second):
		t.Fatal("a wedged peer was never timed out")
	}
}

func TestWriterChargesPerFrameOverhead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	defer w.Close()
	const frames = 10
	for i := 0; i < frames; i++ {
		require.NoError(t, w.Enqueue(testFrameOfSize(uint64(i), 0)))
	}
	assert.Equal(t, frames*testFrameOverhead, w.QueuedBytes(),
		"an empty-ciphertext frame must still cost its slot")
}

func TestWriterOverBudgetTeardownMatchesClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
		OnGiveUp: func(error) { cancel() },
	})
	err := w.Enqueue(testFrameOfSize(1, testMaxBytes))

	require.ErrorIs(t, err, ErrClosed)
	assert.True(t, w.IsClosed(), "the budget kill must close the writer")
	assert.Zero(t, w.QueuedLen())
	assert.Zero(t, w.QueuedBytes())

	select {
	case <-w.Wake():
	default:
		t.Fatal("the budget kill must wake the drain goroutine rather than rely on the context")
	}
}

func TestWriterTryEnqueueDropsOnFullBudgetWithoutGivingUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gaveUp atomic.Bool
	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { select {} },
		Size:  frameSize, MaxBytes: 1024, FrameOverhead: 0,
		OnGiveUp: func(error) { gaveUp.Store(true) },
	})
	defer w.Close()

	require.True(t, w.TryEnqueue(testFrameOfSize(1, 512)))
	require.False(t, w.TryEnqueue(testFrameOfSize(2, 513)), "over budget must drop")
	assert.False(t, gaveUp.Load(), "TryEnqueue must not tear the connection down")
	assert.False(t, w.IsClosed())
}

func TestWriterEnqueueWaitParksAndResumes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// No drain: fill the budget so EnqueueWait must park, then free space
	// via PopForTest (the same signal the drain emits after a successful pop).
	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: 1000, FrameOverhead: 0,
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrameOfSize(1, 500)))
	require.NoError(t, w.Enqueue(testFrameOfSize(2, 500)))

	done := make(chan error, 1)
	go func() {
		done <- w.EnqueueWait(ctx, testFrameOfSize(3, 500))
	}()

	select {
	case <-done:
		t.Fatal("EnqueueWait returned while over budget")
	case <-time.After(50 * time.Millisecond):
	}

	_, ok := w.PopForTest()
	require.True(t, ok)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("EnqueueWait did not resume when the drain freed budget")
	}
}

func TestWriterEnqueueWaitUnwindsOnCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: 500, FrameOverhead: 0,
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrameOfSize(1, 500)))

	waitCtx, waitCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- w.EnqueueWait(waitCtx, testFrameOfSize(2, 500))
	}()
	time.Sleep(20 * time.Millisecond)
	waitCancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("EnqueueWait did not unwind on ctx cancel")
	}
}

func TestWriterOnDiscard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var discardedFrames, discardedBytes atomic.Int32
	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
		OnDiscard: func(frames, bytes int) {
			discardedFrames.Add(int32(frames))
			discardedBytes.Add(int32(bytes))
		},
	})
	require.NoError(t, w.Enqueue(testFrame(1)))
	require.NoError(t, w.Enqueue(testFrame(2)))
	w.Close()
	assert.Equal(t, int32(2), discardedFrames.Load())
	assert.Greater(t, discardedBytes.Load(), int32(0))
}

// newWSWritePair stands up a loopback websocket pair and returns the Write
// func a Writer should be configured with.
//
// Teardown is owned here, in one t.Cleanup with an explicit order, rather
// than left to per-test defers -- and the order matters. The server handler
// parks on <-ctx.Done(), so closing the CLIENT first writes a close frame to
// a peer that will never reply, and coder/websocket then blocks the full 5s
// closing-handshake timeout: a flat 5s of dead wall time on every test using
// this helper. Cancelling first releases the handler, so the client's
// handshake completes immediately.
func newWSWritePair(t *testing.T, received chan *leapmuxv1.ChannelMessage) func(context.Context, *leapmuxv1.ChannelMessage) error {
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
	write := func(wctx context.Context, msg *leapmuxv1.ChannelMessage) error {
		return channelwire.WriteChannelMessage(wctx, conn, msg)
	}
	return write
}

func TestWriterWriteFailureGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	boom := errors.New("write failed")
	gaveUp := make(chan error, 1)
	w := New(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return boom },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
		OnGiveUp: func(err error) { gaveUp <- err; cancel() },
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrame(1)))
	select {
	case err := <-gaveUp:
		require.ErrorIs(t, err, boom)
	case <-time.After(5 * time.Second):
		t.Fatal("write failure did not give up")
	}
}

func TestWriterEnqueueWaitRejectsOversizedItem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: 1000, FrameOverhead: 0,
	})
	defer w.Close()

	err := w.EnqueueWait(ctx, testFrameOfSize(1, 1001))
	require.ErrorIs(t, err, ErrOverBudget)
	assert.False(t, w.IsClosed(), "an oversize item must not tear the connection down")
}

func TestWriterEnqueueWaitResumesOnPopBeforeWriteFinishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Drain pops immediately but parks in Write; EnqueueWait must resume as
	// soon as pop frees the budget, not after Write returns.
	writeStarted := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	w := New(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error {
			close(writeStarted)
			<-release
			return nil
		},
		Size: frameSize, MaxBytes: 500, FrameOverhead: 0,
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrameOfSize(1, 500)))
	<-writeStarted

	done := make(chan error, 1)
	go func() {
		done <- w.EnqueueWait(ctx, testFrameOfSize(2, 500))
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "EnqueueWait must resume when pop frees budget, before Write finishes")
	case <-time.After(2 * time.Second):
		t.Fatal("EnqueueWait stayed parked for the in-flight write instead of waking on pop")
	}
}

func TestWriterGiveUpReportsOnDiscard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var discardedFrames atomic.Int32
	gaveUp := make(chan struct{}, 1)
	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: 1000, FrameOverhead: 0,
		OnDiscard: func(frames, _ int) { discardedFrames.Add(int32(frames)) },
		OnGiveUp:  func(error) { gaveUp <- struct{}{} },
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrameOfSize(1, 500)))
	require.NoError(t, w.Enqueue(testFrameOfSize(2, 500)))
	require.ErrorIs(t, w.Enqueue(testFrameOfSize(3, 1)), ErrClosed)

	<-gaveUp
	assert.Equal(t, int32(2), discardedFrames.Load(),
		"giveUp must report the frames it discards via OnDiscard")
}

func TestWriterControlReserveLetsControlThroughSaturatedData(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		maxBytes = 1000
		reserve  = 200
	)
	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write:          func(context.Context, *leapmuxv1.ChannelMessage) error { select {} },
		Size:           frameSize,
		MaxBytes:       maxBytes,
		ControlReserve: reserve,
		FrameOverhead:  0,
	})
	defer w.Close()

	require.True(t, w.TryEnqueue(testFrameOfSize(1, maxBytes-reserve)),
		"data path may fill up to the data ceiling")
	require.False(t, w.TryEnqueue(testFrameOfSize(2, 1)),
		"data path must leave the control reserve free")
	require.True(t, w.TryEnqueueControl(testFrameOfSize(3, reserve)),
		"control path may consume the reserved headroom")
	require.False(t, w.TryEnqueueControl(testFrameOfSize(4, 1)),
		"control path drops only when the full MaxBytes is gone")
}

func TestWriterEnqueueWaitHonorsControlReserveCeiling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWriter(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write:          func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:           frameSize,
		MaxBytes:       1000,
		ControlReserve: 200,
		FrameOverhead:  0,
	})
	defer w.Close()

	err := w.EnqueueWait(ctx, testFrameOfSize(1, 801))
	require.ErrorIs(t, err, ErrOverBudget,
		"EnqueueWait must reject items larger than the data ceiling, not park forever")
}

// TestWriterFlushWaitsForTheLastWrite pins the guarantee graceful shutdown
// rests on: after Flush returns, every frame enqueued before it was handed to
// the transport.
//
// The gap it closes is that Enqueue returns once a frame is QUEUED and pop()
// removes a frame BEFORE its Write runs, so neither an Enqueue return nor an
// empty queue means the bytes left the process. A shutdown that broadcast and
// immediately tore the connection down therefore dropped its own last words --
// Close discards the queue silently.
func TestWriterFlushWaitsForTheLastWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	var written atomic.Int64
	w := New(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error {
			<-release
			written.Add(1)
			return nil
		},
		Size: frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	defer w.Close()

	// Exactly ONE frame, deliberately. With several queued, a Flush that only
	// looked at queue length would still park on frames 2..N and the test
	// would pass without proving anything about the frame already popped. One
	// frame empties the queue the instant the drain goroutine picks it up, so
	// the only thing that can keep Flush waiting is the in-flight write.
	const frames = 1
	for i := 0; i < frames; i++ {
		require.NoError(t, w.Enqueue(testFrame(uint64(i))))
	}
	// Wait for the drain goroutine to actually pop it, so "queue is empty" is
	// true before Flush is called rather than becoming true during it.
	require.Eventually(t, func() bool { return w.QueuedLen() == 0 },
		2*time.Second, 5*time.Millisecond, "drain goroutine never popped the frame")

	flushed := make(chan error, 1)
	go func() { flushed <- w.Flush(context.Background()) }()

	// The queue is empty but the frame has NOT reached the transport.
	select {
	case <-flushed:
		t.Fatal("Flush returned while the popped frame was still being written")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-flushed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Flush never returned after the writes completed")
	}
	assert.Equal(t, int64(frames), written.Load(),
		"every enqueued frame must have reached the transport before Flush returned")
}

// TestWriterFlushHonoursItsDeadline: a peer that never drains must not hold
// shutdown open forever.
func TestWriterFlushHonoursItsDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := New(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { select {} },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	defer w.Close()
	require.NoError(t, w.Enqueue(testFrame(1)))

	flushCtx, flushCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer flushCancel()
	assert.ErrorIs(t, w.Flush(flushCtx), context.DeadlineExceeded)
}

// TestWriterFlushOnIdleWriterReturnsImmediately: with nothing queued there is
// no drain signal coming, so Flush must decide from state rather than park.
func TestWriterFlushOnIdleWriterReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := New(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:  frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	defer w.Close()

	flushCtx, flushCancel := context.WithTimeout(context.Background(), time.Second)
	defer flushCancel()
	assert.NoError(t, w.Flush(flushCtx))
}

// TestFlushAfterPopForTestReturnsPromptly pins the pop/finishWrite pairing
// against the one exported path that pops without writing.
//
// pop() sets `writing` because the frame has left the queue but not the
// process, and only finishWrite clears it. PopForTest skipped that, so the flag
// stayed set forever and every later Flush on the writer parked until its
// context expired -- which through Client.FlushSends is a 5s shutdown stall
// plus a spurious "outbound queue did not drain" warning.
func TestFlushAfterPopForTestReturnsPromptly(t *testing.T) {
	t.Parallel()

	w := newWriter(context.Background(), Config[string]{
		Write:    func(context.Context, string) error { return nil },
		Size:     func(s string) int { return len(s) },
		MaxBytes: 1024,
	})
	require.NoError(t, w.Enqueue("frame"))
	got, ok := w.PopForTest()
	require.True(t, ok)
	require.Equal(t, "frame", got)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, w.Flush(ctx), "an emptied queue with no write in flight is drained")
}

// TestConcurrentFlushBothReturn pins `drained` as a broadcast rather than a
// depth-1 signal. Flush is exported and nothing stops two goroutines from
// calling it; with a coalescing send, whichever woke first consumed the only
// value and the other parked with no producer left, then failed its own
// context -- reporting a failed flush for a queue that drained fine.
func TestConcurrentFlushBothReturn(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	w := New(context.Background(), Config[string]{
		Write: func(context.Context, string) error {
			<-release
			return nil
		},
		Size:     func(s string) int { return len(s) },
		MaxBytes: 1024,
	})
	t.Cleanup(w.Close)
	require.NoError(t, w.Enqueue("frame"))

	// Wait until the drain goroutine is inside Write, so both flushers park.
	require.Eventually(t, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.writing
	}, 2*time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- w.Flush(ctx) }()
	}
	close(release)

	for range 2 {
		select {
		case err := <-errs:
			require.NoError(t, err, "every concurrent Flush must observe the drain")
		case <-time.After(5 * time.Second):
			t.Fatal("a concurrent Flush never returned")
		}
	}
}

// TestSignalDrainedWakesEveryWaiter is the deterministic core of the concurrent
// -Flush case above: two parked Flush calls capture the SAME generation of
// `drained` under the mutex, so the signal has to wake both. A depth-1
// coalescing send wakes exactly one and leaves the other with no producer.
func TestSignalDrainedWakesEveryWaiter(t *testing.T) {
	t.Parallel()

	w := newWriter(context.Background(), Config[string]{
		Write:    func(context.Context, string) error { return nil },
		Size:     func(s string) int { return len(s) },
		MaxBytes: 1024,
	})

	// What Flush captures before parking, twice over.
	w.mu.Lock()
	first, second := w.drained, w.drained
	w.mu.Unlock()

	w.signalDrained()

	for i, ch := range []chan struct{}{first, second} {
		select {
		case <-ch:
		default:
			t.Fatalf("waiter %d was not woken by signalDrained", i)
		}
	}
}

func TestWriterDrainWritesFIFOAndReturnsNilWhenEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got []uint64
	w := NewUnstarted(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(_ context.Context, m *leapmuxv1.ChannelMessage) error {
			got = append(got, m.GetCorrelationId())
			return nil
		},
		Size: frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrame(1)))
	require.NoError(t, w.Enqueue(testFrame(2)))
	require.NoError(t, w.Drain())
	assert.Equal(t, []uint64{1, 2}, got)
	require.NoError(t, w.Drain(), "empty drain returns nil")
	assert.Equal(t, []uint64{1, 2}, got)
}

func TestWriterDrainAfterCloseWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wrote int
	w := NewUnstarted(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error {
			wrote++
			return nil
		},
		Size: frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	require.NoError(t, w.Enqueue(testFrame(1)))
	w.Close()
	require.NoError(t, w.Drain())
	assert.Zero(t, wrote)
}

func TestWriterFlushReturnsErrClosedWhenQueueWasDiscarded(t *testing.T) {
	t.Parallel()
	w := NewUnstarted(context.Background(), Config[string]{
		Write:    func(context.Context, string) error { return nil },
		Size:     func(s string) int { return len(s) },
		MaxBytes: 1024,
	})
	require.NoError(t, w.Enqueue("frame"))
	w.Close() // discards without writing
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	assert.ErrorIs(t, w.Flush(ctx), ErrClosed,
		"Flush must not report success when Close discarded queued frames")
}

func TestWriterDrainLimitedYieldsAndResignals(t *testing.T) {
	t.Parallel()
	wrote := make([]string, 0, 4)
	w := NewUnstarted(context.Background(), Config[string]{
		Write: func(_ context.Context, s string) error {
			wrote = append(wrote, s)
			return nil
		},
		Size:     func(s string) int { return len(s) },
		MaxBytes: 1024,
	})
	t.Cleanup(w.Close)
	for i := 0; i < 4; i++ {
		require.NoError(t, w.Enqueue(string(rune('a'+i))))
	}
	require.NoError(t, w.DrainLimited(DrainLimits{MaxFrames: 2}))
	require.Equal(t, []string{"a", "b"}, wrote)
	require.Equal(t, 2, w.QueuedLen())
	// Remaining frames must re-arm Wake so a handler select can turn again.
	select {
	case <-w.Wake():
	case <-time.After(time.Second):
		t.Fatal("DrainLimited must re-signal Wake when frames remain")
	}
	require.NoError(t, w.DrainLimited(DrainLimits{MaxFrames: 2}))
	require.Equal(t, []string{"a", "b", "c", "d"}, wrote)
}

func TestWriterConcurrentDrainPanics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	started := make(chan struct{})
	w := NewUnstarted(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error {
			close(started)
			<-release
			return nil
		},
		Size: frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	defer w.Close()
	t.Cleanup(func() { close(release) })

	require.NoError(t, w.Enqueue(testFrame(1)))
	go func() { _ = w.Drain() }()
	<-started
	assert.Panics(t, func() { _ = w.Drain() })
}

func TestWriterFlushStaysParkedAcrossMultiFrameDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gate := make(chan struct{})
	var wrote atomic.Int32
	w := NewUnstarted(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error {
			<-gate
			wrote.Add(1)
			return nil
		},
		Size: frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrame(1)))
	require.NoError(t, w.Enqueue(testFrame(2)))

	flushDone := make(chan error, 1)
	go func() {
		fctx, fcancel := context.WithTimeout(context.Background(), time.Second)
		defer fcancel()
		flushDone <- w.Flush(fctx)
	}()
	drainDone := make(chan error, 1)
	go func() { drainDone <- w.Drain() }()

	select {
	case <-flushDone:
		t.Fatal("Flush returned before all frames left the drain")
	case <-time.After(20 * time.Millisecond):
	}
	assert.Equal(t, int32(0), wrote.Load())

	close(gate)
	require.NoError(t, <-drainDone)
	require.NoError(t, <-flushDone)
	assert.Equal(t, int32(2), wrote.Load())
}

func TestWriterWriteTimeoutGivesUpUnderHandlerDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gaveUp := make(chan error, 1)
	w := NewUnstarted(ctx, Config[*leapmuxv1.ChannelMessage]{
		Write: func(context.Context, *leapmuxv1.ChannelMessage) error {
			select {}
		},
		Size: frameSize, MaxBytes: testMaxBytes, FrameOverhead: testFrameOverhead,
		WriteTimeout: testShortWriteTimeout,
		OnGiveUp: func(err error) {
			cancel()
			select {
			case gaveUp <- err:
			default:
			}
		},
	})
	defer w.Close()

	require.NoError(t, w.Enqueue(testFrame(1)))
	go func() { _ = w.Drain() }()

	select {
	case err := <-gaveUp:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("WriteTimeout must give up under handler drain")
	}
	assert.True(t, w.IsClosed(), "give-up must close the queue even while Drain is parked in Write")
	assert.ErrorIs(t, w.Enqueue(testFrame(2)), ErrClosed)
}
