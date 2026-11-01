package controlipc

// Internal test: streamCollector and newStreamCollector are unexported,
// and the rest of this package's tests live in controlipc_test.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
		// its terminal outcome, while SendStream looked only at the frame's error
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
		require.Error(t, c.outcome())
		assert.Equal(t, "rpc error 7: denied", c.outcome().Error())
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
// collector settles with that error and forwards nothing: the response's
// payload, if any, belongs to a failed call.
func TestStreamCollector_SendResponseWithAnErrorFinishesWithIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	forwarded := 0
	c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.InnerStreamMessage) error {
		forwarded++
		return nil
	})

	resp := channelwire.NewErrorResponse(9, "refused")
	resp.Payload = []byte("must not be forwarded")
	require.NoError(t, c.SendResponse(resp))

	requireSettled(t, c)
	require.Error(t, c.outcome())
	assert.Equal(t, "rpc error: refused", c.outcome().Error())
	assert.Zero(t, forwarded, "an error response forwards no frame")

	// The collector settles once. A later frame neither forwards nor changes
	// the outcome.
	require.NoError(t, c.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: []byte("late")}))
	assert.Zero(t, forwarded)
	assert.Equal(t, "rpc error: refused", c.outcome().Error())
}
