package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// The park/flush/overflow policy had NO direct coverage before this type
// existed: it was four locals inside a ~250-line HTTP handler closure, so
// reaching it meant httptest.NewServer plus a real websocket.Dial, and the only
// handler test in the suite does neither. Every case here is one the closure
// form could not express.
func evt(id string) *crdt.MarshaledEvent {
	return crdt.NewMarshaledEvent(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Batch{Batch: &leapmuxv1.OpBatch{BatchId: id}},
	})
}

func TestSubscriberQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("parks before release and flushes in order", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		require.NoError(t, q.Send(ctx, evt("a")))
		require.NoError(t, q.Send(ctx, evt("b")))

		// Nothing reaches the live queue while parked.
		assert.Empty(t, q.Live())

		flushed := q.Release()
		require.Len(t, flushed, 2)
		assert.Equal(t, "a", flushed[0].Event.GetBatch().GetBatchId())
		assert.Equal(t, "b", flushed[1].Event.GetBatch().GetBatchId())
	})

	t.Run("streams to the live queue after release", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		assert.Empty(t, q.Release())

		require.NoError(t, q.Send(ctx, evt("live")))
		select {
		case got := <-q.Live():
			assert.Equal(t, "live", got.Event.GetBatch().GetBatchId())
		default:
			t.Fatal("a post-release frame must reach the live queue")
		}
	})

	// The release is what makes the flushed slice COMPLETE: a frame accepted
	// after it must go to the live queue, never into a buffer nothing will read.
	t.Run("release is one-way, so a later frame is never parked again", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		q.Release()
		require.NoError(t, q.Send(ctx, evt("after")))
		assert.Empty(t, q.Release(), "nothing may park once released")
		assert.Len(t, q.Live(), 1)
	})

	t.Run("reports ErrSubscriberSlow when the park buffer overflows", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		for i := range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")), "frame %d is within the cap", i)
		}
		assert.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
		// The cap holds: overflow must not grow the buffer past it.
		assert.Len(t, q.Release(), parkedFrameCap)
	})

	t.Run("reports ErrSubscriberSlow when the live queue is full", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		q.Release()
		for i := range liveQueueDepth {
			require.NoError(t, q.Send(ctx, evt("x")), "frame %d is within the depth", i)
		}
		assert.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
	})

	// A resume that gives up takes a snapshot at a LATER point than anything
	// parked during the scan, so replaying those frames over it would reinstate
	// stale entity records permanently -- the client applies materialized /
	// removed wholesale, with no HLC compare.
	t.Run("rebaseline discards parked frames without releasing", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		require.NoError(t, q.Send(ctx, evt("superseded")))
		q.Rebaseline()

		require.NoError(t, q.Send(ctx, evt("kept")))
		flushed := q.Release()
		require.Len(t, flushed, 1, "only frames after the rebaseline survive")
		assert.Equal(t, "kept", flushed[0].Event.GetBatch().GetBatchId())
	})

	// A single three-way select with a ready send AND a ready ctx.Done() picks
	// between them at RANDOM, so a cancelled context could preempt a send the
	// queue had room for -- nondeterministically. Room wins; ctx is consulted
	// only when there is none.
	t.Run("a cancelled context does not preempt a send the queue can take", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		q.Release()
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		for i := range liveQueueDepth {
			require.NoError(t, q.Send(cancelled, evt("x")), "frame %d fits, so ctx must not win", i)
		}
		// Now there is no room, so the cancelled context is what surfaces.
		assert.ErrorIs(t, q.Send(cancelled, evt("overflow")), context.Canceled)
	})

	// The park-phase overflow is what buildResumeDelta consults to convert a
	// dropped frame into ONE snapshot instead of a torn-down connection.
	t.Run("records a park-phase overflow for the resume path", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		assert.False(t, q.Overflowed())
		for range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")))
		}
		require.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
		assert.True(t, q.Overflowed(), "the resume scan must be able to see the drop")
	})
}
