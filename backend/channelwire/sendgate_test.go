package channelwire

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingSender records every chunk under a mutex so concurrent senders'
// interleaving is observable.
type recordingSender struct {
	mu     sync.Mutex
	chunks []chunkRec
	// holdFirst parks the first chunk's sendChunk until released, so a test
	// can start a multi-chunk send and then race a single-chunk one past it.
	holdFirst chan struct{}
	started   chan struct{}
}

type chunkRec struct {
	id    int
	flags leapmuxv1.ChannelMessageFlags
	n     int
}

func (r *recordingSender) send(id int) func([]byte, leapmuxv1.ChannelMessageFlags) error {
	return func(chunk []byte, flags leapmuxv1.ChannelMessageFlags) error {
		if r.holdFirst != nil {
			r.mu.Lock()
			first := len(r.chunks) == 0
			r.mu.Unlock()
			if first {
				close(r.started)
				<-r.holdFirst
			}
		}
		r.mu.Lock()
		r.chunks = append(r.chunks, chunkRec{id: id, flags: flags, n: len(chunk)})
		r.mu.Unlock()
		return nil
	}
}

func (r *recordingSender) snapshot() []chunkRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]chunkRec, len(r.chunks))
	copy(out, r.chunks)
	return out
}

func TestSendGateSingleChunkOvertakesMultiChunk(t *testing.T) {
	var gate SendGate
	rec := &recordingSender{
		holdFirst: make(chan struct{}),
		started:   make(chan struct{}),
	}
	big := make([]byte, MaxPlaintextPerChunk+100)
	small := []byte("hi")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, gate.Send(context.Background(), nil, big, rec.send(1)))
	}()

	select {
	case <-rec.started:
	case <-time.After(2 * time.Second):
		t.Fatal("multi-chunk send never started")
	}

	// Park the small send on the frame permit WHILE the big send still holds it
	// inside its first sendChunk. Releasing holdFirst then lets the small win the
	// between-chunk acquire — calling Send synchronously here would deadlock.
	smallDone := make(chan error, 1)
	go func() {
		smallDone <- gate.Send(context.Background(), nil, small, rec.send(2))
	}()
	time.Sleep(20 * time.Millisecond) // let the small Send park on acquire
	close(rec.holdFirst)

	select {
	case err := <-smallDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("small send did not complete after the first chunk released the frame")
	}
	wg.Wait()

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 3)
	// First chunk of the big message, then the small message, then the rest.
	assert.Equal(t, 1, got[0].id)
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE, got[0].flags)
	assert.Equal(t, 2, got[1].id)
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, got[1].flags)
	assert.Equal(t, 1, got[2].id)
}

func TestSendGateConcurrentMultiChunkNeverOverlapMORERuns(t *testing.T) {
	var gate SendGate
	var mu sync.Mutex
	var active int
	var overlap atomic.Bool

	send := func([]byte, leapmuxv1.ChannelMessageFlags) error {
		mu.Lock()
		active++
		if active > 1 {
			overlap.Store(true)
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}

	big := make([]byte, 2*MaxPlaintextPerChunk+1)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, gate.Send(context.Background(), nil, big, send))
		}()
	}
	wg.Wait()
	assert.False(t, overlap.Load(), "two multi-chunk MORE runs must not overlap")
}

func TestSendGateSerializesSendChunkInvocations(t *testing.T) {
	var gate SendGate
	var concurrent atomic.Int32
	var max atomic.Int32

	send := func([]byte, leapmuxv1.ChannelMessageFlags) error {
		n := concurrent.Add(1)
		for {
			cur := max.Load()
			if n <= cur || max.CompareAndSwap(cur, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		concurrent.Add(-1)
		return nil
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := []byte{byte(i)}
			require.NoError(t, gate.Send(context.Background(), nil, payload, send))
		}(i)
	}
	wg.Wait()
	assert.Equal(t, int32(1), max.Load(), "sendChunk invocations are globally serialized")
}

func TestSendGateCtxCancelBeforeFirstChunkEmitsNothing(t *testing.T) {
	var gate SendGate
	// Hold the frame permit so the second Send parks on acquire.
	gate.init()
	gate.frame <- struct{}{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls atomic.Int32
	err := gate.Send(ctx, nil, []byte("x"), func([]byte, leapmuxv1.ChannelMessageFlags) error {
		calls.Add(1)
		return nil
	})
	require.ErrorIs(t, err, ErrSendAborted)
	assert.Zero(t, calls.Load())

	<-gate.frame // release so the gate is usable again
}

func TestSendGateCtxCancelAfterFirstChunkDoesNotAbandon(t *testing.T) {
	var gate SendGate
	ctx, cancel := context.WithCancel(context.Background())
	big := make([]byte, MaxPlaintextPerChunk+10)
	var calls atomic.Int32

	err := gate.Send(ctx, nil, big, func([]byte, leapmuxv1.ChannelMessageFlags) error {
		n := calls.Add(1)
		if n == 1 {
			cancel() // cancel after the first chunk is committed
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "committed message must finish despite entry ctx cancel")
}

func TestSendGateLifetimeUnwedgesParkedSender(t *testing.T) {
	var gate SendGate
	gate.init()
	gate.frame <- struct{}{} // occupy the frame permit

	life, endLife := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- gate.Send(context.Background(), life, []byte("x"),
			func([]byte, leapmuxv1.ChannelMessageFlags) error { return nil })
	}()

	select {
	case <-done:
		t.Fatal("send returned while the frame permit was held")
	case <-time.After(50 * time.Millisecond):
	}

	endLife()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrSendAborted)
	case <-time.After(2 * time.Second):
		t.Fatal("lifetime end did not unwedge the parked sender")
	}
}

func TestSendGateZeroValueAndNilLifetime(t *testing.T) {
	var gate SendGate
	require.NoError(t, gate.Send(context.Background(), nil, []byte("ok"),
		func([]byte, leapmuxv1.ChannelMessageFlags) error { return nil }))
}

// A sendChunk failure MID multi-chunk message must release BOTH permits (frame
// and chunked), or the next send on this channel wedges forever behind a permit
// nothing will ever hand back. Drive a failing multi-chunk send, then prove a
// second multi-chunk send completes without parking.
func TestSendGateSendChunkErrorReleasesBothPermits(t *testing.T) {
	var gate SendGate
	multiChunk := make([]byte, 2*MaxPlaintextPerChunk) // forces the chunked permit + >1 frame

	boom := errors.New("write failed")
	err := gate.Send(context.Background(), nil, multiChunk,
		func([]byte, leapmuxv1.ChannelMessageFlags) error { return boom })
	require.ErrorIs(t, err, boom, "the sendChunk failure must surface")

	// If either permit leaked, this second send parks forever rather than
	// completing -- so bound it and fail loudly on a wedge.
	done := make(chan error, 1)
	go func() {
		done <- gate.Send(context.Background(), nil, multiChunk,
			func([]byte, leapmuxv1.ChannelMessageFlags) error { return nil })
	}()
	select {
	case err := <-done:
		require.NoError(t, err, "a send after a failed one must succeed once both permits are freed")
	case <-time.After(2 * time.Second):
		t.Fatal("a permit leaked on the sendChunk error path: the next send wedged")
	}
}
