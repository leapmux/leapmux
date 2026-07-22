package remoteipc

// Internal test: streamCollector and newStreamCollector are unexported,
// and the rest of this package's tests live in remoteipc_test.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestStreamCollector_TerminalFrameFinishesTheStream pins that a
// stream-shaped ending actually ends the stream.
//
// Only SendResponse and SendError used to finish the collector, which was
// enough while every handler answered unary. Streaming handlers now report
// a rejected request, and a panic, as InnerStreamMessage frames -- so
// without this a `leapmux remote --follow` whose subscription the worker
// had already ended sat in wait() until its own context expired, with the
// reason never surfacing.
func TestStreamCollector_TerminalFrameFinishesTheStream(t *testing.T) {
	t.Run("an error frame finishes and records why", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.StreamInnerEnvelope) error { return nil })

		require.NoError(t, c.SendStream(&leapmuxv1.InnerStreamMessage{
			IsError:      true,
			ErrorCode:    5,
			ErrorMessage: "not found",
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

	t.Run("an end frame finishes without an error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.StreamInnerEnvelope) error { return nil })

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
		c := newStreamCollector(ctx, "stream-1", func(*leapmuxv1.StreamInnerEnvelope) error { return nil })

		require.NoError(t, c.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("event")}))

		select {
		case <-c.done:
			t.Fatal("an ordinary data frame must leave the subscription open")
		default:
		}
	})
}
