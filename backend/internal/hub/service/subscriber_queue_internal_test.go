package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
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

		flushed, _ := q.Release()
		require.Len(t, flushed, 2)
		assert.Equal(t, "a", flushed[0].Event.GetBatch().GetBatchId())
		assert.Equal(t, "b", flushed[1].Event.GetBatch().GetBatchId())
	})

	t.Run("streams to the live queue after release", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		flushedEmpty, _ := q.Release()
		assert.Empty(t, flushedEmpty)

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
		_, _ = q.Release()
		require.NoError(t, q.Send(ctx, evt("after")))
		afterRelease, _ := q.Release()
		assert.Empty(t, afterRelease, "nothing may park once released")
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
		capped, _ := q.Release()
		assert.Len(t, capped, parkedFrameCap)
	})

	// The parking window stays open across the BOOTSTRAP WRITE, which is the
	// stretch the manager's own Overflowed() check cannot see -- it runs before
	// the frame is even built. Release is therefore the last place a drop in
	// that window can be noticed, and liveCatchUpSink discards the send error,
	// so without this verdict the flush is simply short and the subscriber
	// streams on missing whatever went away.
	t.Run("release reports a drop that happened while parking", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		for range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")))
		}
		require.ErrorIs(t, q.Send(ctx, evt("dropped")), crdt.ErrSubscriberSlow)

		flushed, dropped := q.Release()
		assert.True(t, dropped, "a frame was lost during parking and Release must say so")
		assert.Len(t, flushed, parkedFrameCap, "the survivors are still returned")
	})

	t.Run("release reports no drop when every parked frame fit", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		require.NoError(t, q.Send(ctx, evt("a")))
		require.NoError(t, q.Send(ctx, evt("b")))

		flushed, dropped := q.Release()
		assert.False(t, dropped)
		assert.Len(t, flushed, 2)
	})

	t.Run("reports ErrSubscriberSlow when the live queue is full", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		_, _ = q.Release()
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
		flushed, _ := q.Release()
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
		_, _ = q.Release()
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

	// Rebaseline discards the parked frames AND the overflow they caused. The
	// flag means "a frame that mattered was dropped", and every frame it could
	// refer to has just been superseded by a newer baseline.
	//
	// Leaving it set is not cosmetic: fallbackOutcome's ladder would read a
	// resume scan's stale overflow as an overflow of the baseline it is about
	// to build, burn a retry, and on the last rung escalate to the locked path
	// for a hole that no longer exists.
	t.Run("rebaseline clears the parked frames and the overflow they caused", func(t *testing.T) {
		t.Parallel()
		q := newSubscriberQueue()
		for range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")))
		}
		require.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
		require.True(t, q.Overflowed())

		q.Rebaseline()

		assert.False(t, q.Overflowed(), "the dropped frames are superseded, so the drop is too")
		// And there is room again, so the next baseline's live traffic parks
		// normally rather than immediately re-overflowing.
		require.NoError(t, q.Send(ctx, evt("after")))
		flushed, _ := q.Release()
		require.Len(t, flushed, 1, "the superseded frames must be gone, leaving only the new one")
		assert.Equal(t, "after", flushed[0].Event.GetBatch().GetBatchId())
	})
}

// The OVERFLOW POLICY -- which of two very different answers a dropped frame
// gets -- is what newUserEventsSubscriber was extracted for.
//
// Inline it was an anonymous closure inside a struct literal inside a 280-line
// HTTP handler, so exercising it meant httptest.NewServer plus a real
// websocket.Dial plus a burst large enough to fill 256 slots. As a named unit
// the two arms are three lines each to assert, and the difference between them
// -- whether the connection is torn down -- is directly observable.
func TestNewUserEventsSubscriber_OverflowPolicyDiffersByPhase(t *testing.T) {
	user := &auth.UserInfo{ID: userid.MustNew(id.Generate())}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	sub, queue := newUserEventsSubscriber(ctx, cancel, user, "client-1", nil)
	require.Equal(t, "client-1", sub.ClientID)

	// PRE-BOOTSTRAP: fill the park buffer, then overflow it.
	for i := range parkedFrameCap {
		require.NoError(t, sub.Send(evt(fmt.Sprintf("parked-%d", i))))
	}
	require.ErrorIs(t, sub.Send(evt("overflows")), crdt.ErrSubscriberSlow)
	// Flagged for the manager's ladder, and the connection is left ALIVE -- a
	// teardown here would send the client back with the same cursor to redo the
	// work, under the load that caused the overflow.
	assert.True(t, sub.Overflowed(), "a park overflow must be visible to the fallback ladder")
	require.NoError(t, ctx.Err(), "the pre-bootstrap arm must NOT tear the connection down")

	// A rebaseline supersedes those frames and clears the flag, which is what
	// lets the ladder retry rather than escalate for a hole that is gone.
	sub.OnRebaseline()
	assert.False(t, sub.Overflowed(), "Rebaseline must clear the flag alongside the frames")

	// STEADY STATE: past Release there is no scan to fall back from.
	_, dropped := queue.Release()
	require.False(t, dropped, "the rebaseline cleared the earlier drop")
	for range liveQueueDepth {
		require.NoError(t, sub.Send(evt("live")))
	}
	require.ErrorIs(t, sub.Send(evt("over-live")), crdt.ErrSubscriberSlow)
	assert.Error(t, ctx.Err(), "the steady-state arm must cancel, dropping the subscriber")
}
