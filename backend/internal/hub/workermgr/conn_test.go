package workermgr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/sendq"
)

// testHello is the WorkerHello every Conn in this package's tests is built
// with: a classic-mode worker with a fixed 32-byte X25519 key. workermgrtest
// has its own copy, because it imports this package and this package's tests
// cannot import it back.
func testHello() *leapmuxv1.WorkerHello {
	return &leapmuxv1.WorkerHello{
		Keys:           &leapmuxv1.WorkerPublicKeys{X25519: bytes.Repeat([]byte{0x42}, 32)},
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}
}

// testPool gives one Conn a private byte budget the size a production Hub
// grants a single connection, so a test that fills its queue does not change
// what any other test's Conn is admitted. Tests that mean to exercise SHARED
// pressure build one pool and hand it to several NewConn calls.
func TestConnSendReturnsWhileWriteParked(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	conn, pump := NewConn(context.Background(), func() {}, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return nil
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	require.NoError(t, conn.Send(channelMsg(1)))
	drainErr := make(chan error, 1)
	go func() { drainErr <- pump.Drain() }()
	<-started

	sendDone := make(chan error, 1)
	go func() { sendDone <- conn.Send(channelMsg(2)) }()
	select {
	case err := <-sendDone:
		require.NoError(t, err, "Send must return while a drain write is parked")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Send blocked behind a parked drain write")
	}
}

func TestConnOverBudgetFencesAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, _ := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		select {} // never drain — fill the queue
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	const chunk = 1 << 20
	var err error
	for i := 0; i < int(sendq.DefaultMaxBytes/int64(chunk))+4; i++ {
		err = conn.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_ChannelMessage{
				ChannelMessage: &leapmuxv1.ChannelMessage{
					Ciphertext: make([]byte, chunk),
				},
			},
		})
		if err != nil {
			break
		}
	}
	require.ErrorIs(t, err, ErrConnectionClosed)
	select {
	case <-conn.Done():
	case <-time.After(time.Second):
		t.Fatal("over-budget give-up must cancel the conn")
	}
	assert.ErrorIs(t, conn.Send(channelMsg(1)), ErrConnectionClosed)
}

func TestConnSendControlSucceedsWhenDataCeilingFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, _ := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		select {}
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	ceiling := sendq.DefaultMaxBytes - sendq.DefaultControlReserve
	payload := sizedCiphertext(t, ceiling)
	require.NoError(t, conn.Send(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ChannelMessage{
			ChannelMessage: &leapmuxv1.ChannelMessage{Ciphertext: payload},
		},
	}))
	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}), "control reserve must remain usable when the data ceiling is full")

	// A further data-path enqueue must give up (tear down), not merely refuse.
	assert.ErrorIs(t, conn.Send(channelMsg(99)), ErrConnectionClosed)
	select {
	case <-conn.Done():
	default:
		t.Fatal("over-budget data Send must fence via give-up")
	}
}

func TestConnSendControlFencesWhenFullySaturated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, _ := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		select {}
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	// Fill via the control path until TryEnqueueControl refuses. Control
	// saturating the full MaxBytes budget Fences — same recovery policy as
	// the worker's TrySendOrReset.
	hb := &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}
	refused := false
	for i := 0; i < 1<<20; i++ {
		if err := conn.SendControl(hb); err != nil {
			require.ErrorIs(t, err, ErrControlSaturated)
			refused = true
			break
		}
	}
	require.True(t, refused, "control path must eventually hit MaxBytes")
	select {
	case <-conn.Done():
	case <-time.After(time.Second):
		t.Fatal("SendControl must fence when the control budget is exhausted")
	}
}

func TestConnFlushReturnsImmediatelyWhenIdle(t *testing.T) {
	conn, _ := NewConn(context.Background(), func() {}, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		return nil
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.NoError(t, conn.Flush(ctx))
}

func TestConnDrainWriteErrorFences(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, pump := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		return errors.New("stream reset")
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))
	require.Error(t, pump.Drain())
	select {
	case <-conn.Done():
	case <-time.After(time.Second):
		t.Fatal("write failure must give up and fence")
	}
	assert.ErrorIs(t, conn.Send(channelMsg(1)), ErrConnectionClosed)
}

func TestConnFenceIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cancels atomic.Int32
	conn, _ := NewConn(ctx, func() {
		cancels.Add(1)
		cancel()
	}, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error { return nil }, nil, testHello())

	conn.Fence()
	conn.Fence()
	assert.Equal(t, int32(1), cancels.Load(), "cancel runs at most once")
	assert.ErrorIs(t, conn.Send(channelMsg(1)), ErrConnectionClosed)
}

// sizedCiphertext returns a ciphertext whose ConnectResponse charges exactly
// targetBytes against the sendq budget (proto.Size + sendq.DefaultFrameOverhead).
func sizedCiphertext(t *testing.T, targetBytes int64) []byte {
	t.Helper()
	lo, hi := int64(0), targetBytes
	best := int64(-1)
	for lo <= hi {
		mid := (lo + hi) / 2
		msg := &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_ChannelMessage{
				ChannelMessage: &leapmuxv1.ChannelMessage{Ciphertext: make([]byte, mid)},
			},
		}
		charged := int64(proto.Size(msg)) + sendq.DefaultFrameOverhead
		if charged <= targetBytes {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	require.GreaterOrEqual(t, best, int64(0))
	return make([]byte, best)
}

func TestConnDoneClosesOnFence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, _ := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error { return nil }, nil, testHello())
	conn.Fence()
	select {
	case <-conn.Done():
	default:
		t.Fatal("Fence must close Done")
	}
}

func TestConnDrainAfterFenceWritesNothing(t *testing.T) {
	var wrote int
	conn, pump := NewConn(context.Background(), func() {}, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		wrote++
		return nil
	}, nil, testHello())
	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{}))
	conn.Fence()
	require.NoError(t, pump.Drain())
	assert.Zero(t, wrote, "Drain after Fence must not write discarded frames")
}

func TestConnFlushWaitsForEveryQueuedFrame(t *testing.T) {
	var mu sync.Mutex
	var wrote []int
	gate := make(chan struct{})

	conn, pump := NewConn(context.Background(), func() {}, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(msg *leapmuxv1.ConnectResponse) error {
		<-gate
		mu.Lock()
		wrote = append(wrote, int(msg.GetChannelMessage().GetCorrelationId()))
		mu.Unlock()
		return nil
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	require.NoError(t, conn.Send(channelMsg(1)))
	require.NoError(t, conn.Send(channelMsg(2)))

	flushDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		flushDone <- conn.Flush(ctx)
	}()

	drainDone := make(chan error, 1)
	go func() { drainDone <- pump.Drain() }()

	select {
	case <-flushDone:
		t.Fatal("Flush returned before any frame reached the transport")
	case <-time.After(20 * time.Millisecond):
	}

	close(gate)
	require.NoError(t, <-drainDone)
	require.NoError(t, <-flushDone)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int{1, 2}, wrote)
}

// The handshake a Conn reports is a snapshot of the hello it was built with:
// neither the caller's hello nor a returned response is shared with it, so no
// later write can make two answers for one connection disagree.
func TestConnHandshakeIsAnImmutableSnapshot(t *testing.T) {
	hello := &leapmuxv1.WorkerHello{
		Keys: &leapmuxv1.WorkerPublicKeys{
			X25519: bytes.Repeat([]byte{0x01}, 32),
			Mlkem:  bytes.Repeat([]byte{0x02}, 1568),
			Slhdsa: bytes.Repeat([]byte{0x03}, 64),
		},
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	}
	conn, _ := NewConn(context.Background(), func() {}, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		return nil
	}, nil, hello)
	t.Cleanup(conn.Fence)

	// Change the caller's hello after construction.
	hello.EncryptionMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC
	hello.Keys.X25519[0] = 0xff
	hello.Keys.Mlkem = nil

	first := conn.Handshake()
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, first.GetEncryptionMode())
	assert.Equal(t, bytes.Repeat([]byte{0x01}, 32), first.GetKeys().GetX25519())
	assert.Len(t, first.GetKeys().GetMlkem(), 1568)

	// Change a returned response.
	first.EncryptionMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC
	first.Keys.X25519[0] = 0xee
	first.Keys.Slhdsa = nil

	second := conn.Handshake()
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, second.GetEncryptionMode())
	assert.Equal(t, bytes.Repeat([]byte{0x01}, 32), second.GetKeys().GetX25519())
	assert.Len(t, second.GetKeys().GetSlhdsa(), 64)
}

func TestNewConnRequiresAHello(t *testing.T) {
	assert.PanicsWithValue(t, "workermgr.NewConn: hello is required", func() {
		NewConn(context.Background(), func() {}, "w1", "u1", sendq.NewMaxBytesPoolForTest(),
			func(*leapmuxv1.ConnectResponse) error { return nil }, nil, nil)
	})
}

// A hello that carries no keys still answers with its mode, and with no keys
// rather than an empty key message, so a caller sees the same absence the
// worker sent.
func TestConnHandshakeKeepsAnAbsentKeyMessageAbsent(t *testing.T) {
	hello := &leapmuxv1.WorkerHello{EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC}
	conn, _ := NewConn(context.Background(), func() {}, "w1", "u1", sendq.NewMaxBytesPoolForTest(),
		func(*leapmuxv1.ConnectResponse) error { return nil }, nil, hello)
	t.Cleanup(conn.Fence)

	handshake := conn.Handshake()
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, handshake.GetEncryptionMode())
	assert.Nil(t, handshake.GetKeys())
}

// Concurrent Handshake calls share nothing they could race on: each answer is
// its own message, built from a hello no one writes after construction.
func TestConnHandshakeIsSafeForConcurrentUse(t *testing.T) {
	conn, _ := NewConn(context.Background(), func() {}, "w1", "u1", sendq.NewMaxBytesPoolForTest(),
		func(*leapmuxv1.ConnectResponse) error { return nil }, nil, testHello())
	t.Cleanup(conn.Fence)

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			handshake := conn.Handshake()
			// Writing the answer must not reach the Conn or another caller.
			handshake.Keys.X25519[0] = 0xff
		})
	}
	wg.Wait()
	assert.Equal(t, testHello().GetKeys().GetX25519(), conn.Handshake().GetKeys().GetX25519())
}

func TestSendPumpDrain_WritePanicDoesNotEscape(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, pump := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		panic("worker stream already finished")
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))

	var drainErr error
	assert.NotPanics(t, func() {
		drainErr = pump.Drain()
	}, "a panicking write must not escape SendPump.Drain")
	require.Error(t, drainErr)
	select {
	case <-conn.Done():
	default:
		t.Fatal("panicking write must fence the conn")
	}
	assert.ErrorIs(t, conn.SendControl(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed)
}

func TestConnSendWaitEnqueuesAndHonoursDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wrote atomic.Int32
	conn, pump := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		wrote.Add(1)
		return nil
	}, nil, testHello())
	t.Cleanup(conn.Fence)
	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-conn.Done():
				return
			}
		}
	}()

	require.NoError(t, conn.SendWait(context.Background(), channelMsg(1)))
	require.Eventually(t, func() bool { return wrote.Load() == 1 }, time.Second, 5*time.Millisecond)

	// Leave a never-drained conn one frame short of the data ceiling, then
	// park SendWait on a frame that cannot fit until budget frees.
	fillCtx, fillCancel := context.WithCancel(context.Background())
	defer fillCancel()
	fill, _ := NewConn(fillCtx, fillCancel, "w2", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		select {}
	}, nil, testHello())
	t.Cleanup(fill.Fence)
	ceiling := sendq.DefaultMaxBytes - sendq.DefaultControlReserve
	pad := sizedCiphertext(t, ceiling-512)
	require.NoError(t, fill.Send(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ChannelMessage{
			ChannelMessage: &leapmuxv1.ChannelMessage{Ciphertext: pad},
		},
	}))
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer waitCancel()
	blocked := &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ChannelMessage{
			ChannelMessage: &leapmuxv1.ChannelMessage{Ciphertext: make([]byte, 1024)},
		},
	}
	require.ErrorIs(t, fill.SendWait(waitCtx, blocked), context.DeadlineExceeded)
}

func TestConnFlushReturnsErrConnectionClosedAfterDiscard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, _ := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		select {}
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))
	conn.Fence() // discards the queued frame without writing

	flushCtx, flushCancel := context.WithTimeout(context.Background(), time.Second)
	defer flushCancel()
	assert.ErrorIs(t, conn.Flush(flushCtx), ErrConnectionClosed,
		"Flush must map a discarded queue to ErrConnectionClosed, not success")
}

func TestConnFlushReturnsErrConnectionClosedOnWriteError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var writeStarted sync.WaitGroup
	writeStarted.Add(1)
	conn, pump := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		writeStarted.Done()
		<-release
		return fmt.Errorf("connection reset")
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-conn.Done():
				return
			}
		}
	}()

	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))
	writeStarted.Wait()

	flushed := make(chan error, 1)
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer flushCancel()
	go func() { flushed <- conn.Flush(flushCtx) }()
	close(release)

	select {
	case err := <-flushed:
		require.ErrorIs(t, err, ErrConnectionClosed,
			"Flush must map a refused write to ErrConnectionClosed, not success")
	case <-time.After(3 * time.Second):
		t.Fatal("Flush never returned after the write failed")
	}
	assert.True(t, conn.GaveUp())
}

// Caller Flush deadline on a still-open queue must stay a deadline error.
// Mapping every cancel to ErrConnectionClosed would hide a live slow peer.
func TestConnFlushPreservesCallerDeadlineWhenQueueStillOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	defer close(release)
	var writeStarted sync.WaitGroup
	writeStarted.Add(1)
	conn, pump := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		writeStarted.Done()
		<-release
		return nil
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-conn.Done():
				return
			}
		}
	}()

	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))
	writeStarted.Wait()

	flushCtx, flushCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer flushCancel()
	err := conn.Flush(flushCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"Flush must not map a caller deadline to ErrConnectionClosed while the queue is open")
	assert.False(t, conn.GaveUp())
	assert.NotErrorIs(t, err, ErrConnectionClosed)
}

func TestSendPumpDrainTurnBoundsBatchAndResignals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wrote atomic.Int32
	conn, pump := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		wrote.Add(1)
		return nil
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	// Enqueue more frames than one DrainTurn admits (connDrainMaxFrames=32).
	const n = connDrainMaxFrames + 8
	for i := 0; i < n; i++ {
		require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
		}))
	}
	require.NoError(t, pump.DrainTurn())
	assert.Equal(t, int32(connDrainMaxFrames), wrote.Load(), "DrainTurn must stop at the frame budget")

	// Remaining frames re-signal Ready so the Connect select can turn again.
	select {
	case <-pump.Ready():
	case <-time.After(time.Second):
		t.Fatal("DrainTurn must re-signal Ready when frames remain")
	}
	require.NoError(t, pump.Drain())
	assert.Equal(t, int32(n), wrote.Load())
}

func TestSendPumpRePanicsConcurrentDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	conn, pump := NewConn(ctx, cancel, "w1", "u1", sendq.NewMaxBytesPoolForTest(), func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}, nil, testHello())
	t.Cleanup(conn.Fence)

	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))
	go func() { _ = pump.Drain() }()
	<-started

	assert.Panics(t, func() { _ = pump.Drain() },
		"concurrent Drain must re-panic as an ownership bug, not recover into an error")
}

func channelMsg(id uint64) *leapmuxv1.ConnectResponse {
	return &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ChannelMessage{
			ChannelMessage: &leapmuxv1.ChannelMessage{CorrelationId: id},
		},
	}
}

// TestNewConnRefusesToBuildWithoutABudget pins that a composition which forgot
// the worker pool fails at wiring time rather than running with an unbounded
// per-connection queue that nothing would flag until the Hub ran out of memory.
func TestNewConnRefusesToBuildWithoutABudget(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t, "workermgr.NewConn: pool is required", func() {
		NewConn(context.Background(), func() {}, "w1", "u1", nil,
			func(*leapmuxv1.ConnectResponse) error { return nil }, nil, testHello())
	})
}
