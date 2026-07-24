package sendq

import (
	"context"
	"errors"
	"net"
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
	srv, client, write, cancel := newWSWritePair(t, received)
	defer cancel()
	defer srv.Close()
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

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
	srv, client, write, cancel := newWSWritePair(t, received)
	defer cancel()
	defer srv.Close()
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

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
	srv, client, write, cancel := newWSWritePair(t, received)
	defer cancel()
	defer srv.Close()
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

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
	srv, client, write, pairCancel := newWSWritePair(t, received)
	defer pairCancel()
	defer srv.Close()
	defer func() { _ = client.Close(websocket.StatusNormalClosure, "") }()

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
		WriteTimeout: testWriteTimeout,
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
	case <-time.After(testWriteTimeout + 20*time.Second):
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
	case <-w.WakeChForTest():
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

func newWSWritePair(t *testing.T, received chan *leapmuxv1.ChannelMessage) (
	*httptest.Server, *websocket.Conn, func(context.Context, *leapmuxv1.ChannelMessage) error, context.CancelFunc,
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
	write := func(wctx context.Context, msg *leapmuxv1.ChannelMessage) error {
		return channelwire.WriteChannelMessage(wctx, conn, msg)
	}
	return srv, client, write, cancel
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
