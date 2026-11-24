package controlipc

// Internal test: streamCollector and newStreamCollector are unexported,
// and the rest of this package's tests live in controlipc_test.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// requireSettled fails the test unless c has settled its outcome. The deadline
// is generous, because nothing here should take more than a moment.
func requireSettled(t *testing.T, c *streamCollector) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the collector never settled")
	}
}

// TestStreamCollector_TerminalFrameFinishesTheStream pins that a
// stream-shaped ending actually ends the stream.
//
// Only SendResponse and SendError used to finish the collector, which was
// enough while every handler answered unary. Streaming handlers now report
// a rejected request, and a panic, as InnerStreamMessage frames -- so
// without this a `leapmux control --follow` whose subscription the worker
// had already ended sat in wait() until its own context expired, with the
// reason never surfacing.
func TestStreamCollector_TerminalFrameFinishesTheStream(t *testing.T) {
	t.Run("an error frame finishes and records why", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error { return nil })

		require.NoError(t, c.SendStream(&leapmuxv1.InnerStreamMessage{
			Error: &leapmuxv1.RpcError{Code: 5, Message: "not found"},
		}))

		// wait() must return without the context being cancelled; if the
		// frame did not finish the collector this blocks until the deadline.
		waited := make(chan struct{})
		go func() { defer close(waited); c.wait() }()
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
			t.Fatal("an error frame did not terminate the stream")
		}
		require.Error(t, c.err)
		assert.Contains(t, c.err.Error(), "not found", "the reason must reach the caller")
	})

	t.Run("a clean end frame reports its delivery failure, like SendResponse does", func(t *testing.T) {
		// The asymmetry this pins: SendResponse assigned onMsg's error straight to
		// its final outcome, while SendStream looked only at the frame's error
		// -- so a clean End frame whose delivery FAILED settled as success and the caller
		// was told the stream completed. Which of two equivalent reply shapes the
		// handler happened to use decided the outcome.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sendErr := errors.New("connect stream send failed")
		c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error { return sendErr })

		// The handler still learns of it via the return value...
		require.Error(t, c.SendStream(&leapmuxv1.InnerStreamMessage{End: true}))

		waited := make(chan struct{})
		go func() { defer close(waited); c.wait() }()
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
			t.Fatal("a clean end frame did not terminate the stream")
		}
		// ...and so does the CALLER, which is what was missing.
		require.Error(t, c.err, "a delivery failure on a clean End frame must not settle as success")
		assert.Contains(t, c.err.Error(), "connect stream send failed")
	})

	t.Run("an end frame finishes without an error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error { return nil })

		require.NoError(t, c.SendStream(&leapmuxv1.InnerStreamMessage{End: true}))

		waited := make(chan struct{})
		go func() { defer close(waited); c.wait() }()
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
			t.Fatal("an end frame did not terminate the stream")
		}
		assert.NoError(t, c.err, "a clean end is not an error")
	})

	t.Run("a data frame does not finish the stream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error { return nil })

		require.NoError(t, c.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("event")}))

		select {
		case <-c.done:
			t.Fatal("an ordinary data frame must leave the subscription open")
		default:
		}
	})

	t.Run("an error frame reaches the caller unchanged", func(t *testing.T) {
		// The caller reads the code and the message from the frame itself, so
		// the collector forwards the frame rather than a copy that could drop a
		// field.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var forwarded *leapmuxv1.InnerStreamMessage
		c := newStreamCollector(ctx, "stream-1", func(msg *leapmuxv1.InnerStreamMessage) error {
			forwarded = msg
			return nil
		})

		frame := channelwire.NewStreamError(7, "denied")
		require.NoError(t, c.SendStream(frame))

		assert.Same(t, frame, forwarded)
		requireSettled(t, c)
		var rpcErr *channelwire.RPCError
		require.ErrorAs(t, c.outcome(), &rpcErr, "the outcome keeps the frame's code")
		assert.Equal(t, int32(7), rpcErr.Code)
		assert.EqualError(t, c.outcome(), "rpc error (code 7): denied")
	})

	t.Run("an error frame reports its own error over a delivery failure", func(t *testing.T) {
		// The handler's error states why the stream ended. A delivery failure
		// is a second, lesser fact, and it must not replace the first.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sendErr := errors.New("connect stream send failed")
		c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error { return sendErr })

		require.ErrorIs(t, c.SendStream(channelwire.NewStreamError(5, "not found")), sendErr,
			"the handler still learns that the frame was not delivered")

		requireSettled(t, c)
		require.Error(t, c.outcome())
		assert.Contains(t, c.outcome().Error(), "not found")
		assert.NotErrorIs(t, c.outcome(), sendErr)
	})
}

// A streaming handler can also end its stream with a unary error response. The
// collector settles with that error, as a *channelwire.RPCError that keeps its
// code, and forwards nothing.
func TestStreamCollector_SendResponseWithAnErrorFinishesWithIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	forwarded := 0
	c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error {
		forwarded++
		return nil
	})

	require.NoError(t, c.SendResponse(channelwire.NewErrorResponse(9, "refused")))

	requireSettled(t, c)
	var rpcErr *channelwire.RPCError
	require.ErrorAs(t, c.outcome(), &rpcErr)
	assert.Equal(t, int32(9), rpcErr.Code, "the outcome keeps the handler's code")
	assert.EqualError(t, c.outcome(), "rpc error (code 9): refused")
	assert.Zero(t, forwarded, "an error response forwards no frame")

	// The collector settles once. A later frame neither forwards nor changes
	// the outcome.
	require.NoError(t, c.SendResponse(channelwire.NewPayloadResponse([]byte("late"))))
	assert.Zero(t, forwarded)
	assert.EqualError(t, c.outcome(), "rpc error (code 9): refused")
}

// relayError keeps the code of a failure and carries its text once. The text of
// a Connect error already starts with its code, and connect.NewError adds the
// code again, so the local-IPC output read "not_found: not_found: worker not
// found". A wrapper's context stays, because it says which step of the relay
// failed. A plain error has no code, so it becomes Internal.
func TestRelayError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		code    connect.Code
		message string
	}{
		{
			name:    "connect error",
			err:     connect.NewError(connect.CodeNotFound, "worker not found"),
			code:    connect.CodeNotFound,
			message: "worker not found",
		},
		{
			name:    "wrapped connect error",
			err:     fmt.Errorf("get worker handshake params: %w", connect.NewError(connect.CodeUnavailable, "worker is offline")),
			code:    connect.CodeUnavailable,
			message: "get worker handshake params: worker is offline",
		},
		{
			name: "connect error with an empty message",
			err:  connect.NewError(connect.CodePermissionDenied, ""),
			code: connect.CodePermissionDenied,
		},
		{
			name:    "connect error with the unknown code",
			err:     connect.NewError(connect.CodeUnknown, "no code to keep"),
			code:    connect.CodeInternal,
			message: "no code to keep",
		},
		{
			name:    "plain error",
			err:     errors.New("boom"),
			code:    connect.CodeInternal,
			message: "boom",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relayed := relayError(tc.err)

			var connectErr *connect.Error
			require.ErrorAs(t, relayed, &connectErr)
			assert.Equal(t, tc.code, connectErr.Code())
			assert.Equal(t, tc.message, connectErr.Message())
			assert.ErrorIs(t, relayed, tc.err, "the original error stays the cause")
		})
	}
}

func TestRelayError_PrintsTheCodeOnce(t *testing.T) {
	relayed := relayError(connect.NewError(connect.CodeNotFound, "worker not found"))
	assert.Equal(t, "not_found: worker not found", relayed.Error())
}

// A Worker broadcasts each event from its own goroutine, so several can reach
// one stream at once. A connect stream takes one Send at a time, so the
// collector must forward one frame at a time.
func TestStreamCollector_ForwardsOneFrameAtATime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var inFlight, overlaps atomic.Int32
	c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error {
		if inFlight.Add(1) > 1 {
			overlaps.Add(1)
		}
		runtime.Gosched()
		inFlight.Add(-1)
		return nil
	})
	var senders sync.WaitGroup
	for range 8 {
		senders.Go(func() {
			for range 200 {
				_ = c.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("x")})
			}
		})
	}
	senders.Wait()
	assert.Zero(t, overlaps.Load(), "two frames reached the stream at once")
}

// A frame in flight holds the send lock for the whole of its send. close takes
// the same lock, so it waits for that frame, and the HTTP handler never closes
// a stream that a broadcaster still writes.
func TestStreamCollector_HoldsTheSendLockWhileItForwardsAFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error {
		close(entered)
		<-release
		return nil
	})
	sent := make(chan error, 1)
	go func() { sent <- c.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("late")}) }()
	<-entered

	locked := c.sendMu.TryLock()
	if locked {
		c.sendMu.Unlock()
	}
	assert.False(t, locked, "a frame in flight must hold the send lock")
	close(release)
	require.NoError(t, <-sent)
}

// retainingDispatcher ends the stream at once and keeps its writer, as a
// broadcaster that still holds a subscriber's writer after the watch ended.
type retainingDispatcher struct {
	writers chan channel.ResponseWriter
}

func (d retainingDispatcher) DispatchWith(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.writers <- w
	_ = w.SendStream(&leapmuxv1.InnerStreamMessage{End: true})
}

// The HTTP handler closes the stream once streamLocal returns, so streamLocal
// closes its collector first. The stream's context ends only when StreamInner
// returns, after that close, so the close is what refuses a broadcast in the
// time between the end of the wait and the return. Once StreamInner returns,
// the collector that a broadcaster holds is closed, and a late broadcast never
// reaches the stream.
func TestRouter_StreamLocalClosesTheCollectorBeforeItReturns(t *testing.T) {
	d := retainingDispatcher{writers: make(chan channel.ResponseWriter, 1)}
	r := &Router{LocalDispatcher: d, WorkerID: "A"}
	var afterReturn atomic.Bool
	var lateFrames atomic.Int32

	require.NoError(t, r.StreamInner(context.Background(), TokenInfo{}, "worker.Watch", []byte("{}"), "A", "",
		func(m *leapmuxv1.InnerStreamMessage) error {
			if afterReturn.Load() {
				lateFrames.Add(1)
			}
			return nil
		}))
	afterReturn.Store(true)

	collector, ok := (<-d.writers).(*streamCollector)
	require.True(t, ok, "the dispatcher writes to the stream's collector")
	collector.sendMu.Lock()
	closed := collector.closed
	collector.sendMu.Unlock()
	assert.True(t, closed, "streamLocal closes the collector before it returns")
	require.Error(t, collector.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("late")}))
	assert.Zero(t, lateFrames.Load(), "no frame reaches a stream after streamLocal returned")
}

// After close, no frame reaches the stream: a late broadcast fails, and a
// final payload of SendResponse does too.
func TestStreamCollector_RefusesEveryFrameAfterClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := 0
	c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error {
		sent++
		return nil
	})
	require.NoError(t, c.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("before")}))
	c.close()

	require.ErrorIs(t, c.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("after")}), errStreamClosed)
	require.NoError(t, c.SendResponse(&leapmuxv1.InnerRpcResponse{Result: &leapmuxv1.InnerRpcResponse_Payload{Payload: []byte("final")}}))
	requireSettled(t, c)
	require.ErrorIs(t, c.outcome(), errStreamClosed, "a final payload that could not reach the stream is the outcome")
	assert.Equal(t, 1, sent, "only the frame before close reached the stream")
}
