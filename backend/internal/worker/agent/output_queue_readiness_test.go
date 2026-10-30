package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pausedCapturedBatch holds the actual reservation callback after it observes
// an unready batch, before OutputQueue can process that callback's error.
type pausedCapturedBatch struct {
	batch    *CapturedTranscriptBatch
	attempts atomic.Int32
	observed chan error
	release  chan struct{}
	resumed  sync.Once
	done     chan struct{}
	err      error
	ctx      context.Context
}

func pauseCapturedBatchBeforeUnreadyReturn(t *testing.T, queue *OutputQueue) *pausedCapturedBatch {
	t.Helper()
	batch, err := reserveCapturedTranscriptBatch(queueReservationTranscript{}, queue)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	paused := &pausedCapturedBatch{
		batch: batch, observed: make(chan error, 1), release: make(chan struct{}),
		done: make(chan struct{}), ctx: ctx,
	}
	original := batch.ticket.write.Run
	batch.ticket.write.Run = func() error {
		attempt := paused.attempts.Add(1)
		// Stop an incorrect retry loop deterministically, rather than waiting
		// for a test timeout to detect repeated callback execution.
		if attempt > 2 {
			return errors.New("the queue repeated a callback without another readiness transition")
		}
		err := original()
		if attempt == 1 {
			paused.observed <- err
			<-paused.release
		}
		return err
	}
	go func() {
		paused.err = queue.Flush()
		close(paused.done)
	}()
	t.Cleanup(func() {
		paused.resume()
		select {
		case <-paused.done:
		case <-ctx.Done():
			t.Error("the output drainer did not finish after its callback was released")
		}
		cancel()
	})
	select {
	case err := <-paused.observed:
		require.ErrorIs(t, err, ErrOutputInFlight, "the real reserved batch must observe its unready state")
	case <-ctx.Done():
		t.Fatal("the reserved batch callback did not observe its unready state")
	}
	return paused
}

func (paused *pausedCapturedBatch) resume() {
	paused.resumed.Do(func() { close(paused.release) })
}

func (paused *pausedCapturedBatch) result(t *testing.T) error {
	t.Helper()
	select {
	case <-paused.done:
		return paused.err
	case <-paused.ctx.Done():
		t.Fatal("the output drainer did not finish after readiness changed")
		return nil
	}
}

func TestCapturedTranscriptBatchReadinessResumesTheCurrentDrainer(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		name := "eager"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var queue OutputQueue
			paused := pauseCapturedBatchBeforeUnreadyReturn(t, &queue)
			var order []string
			later := queue.Append(OutputWrite{Label: "record the following row", Run: func() error {
				assert.True(t, paused.batch.ticket.Done(), "the following row cannot overtake the reserved batch")
				order = append(order, "row")
				return nil
			}})[0]
			services := orderedTranscript{queue: &queue}
			require.ErrorIs(t, services.submit("release the following effect", func() error {
				order = append(order, "effect")
				return nil
			}), ErrOutputInFlight)
			var prepareErr error
			if stream {
				prepareErr = paused.batch.PrepareStream(func() (CapturedTranscriptOperation, bool, error) {
					return CapturedTranscriptOperation{}, true, nil
				})
			} else {
				prepareErr = paused.batch.Prepare(func() []CapturedTranscriptOperation { return nil })
			}
			require.ErrorIs(t, prepareErr, ErrOutputInFlight)
			assert.False(t, paused.batch.ticket.Done())
			assert.False(t, later.Done())
			assert.Nil(t, queue.retry, "the readiness handoff must not depend on configured retries")
			paused.resume()
			require.NoError(t, paused.result(t))
			assert.EqualValues(t, 2, paused.attempts.Load())
			assert.True(t, paused.batch.ticket.Done())
			assert.True(t, later.Done())
			assert.Equal(t, []string{"row", "effect"}, order)
			assert.False(t, queue.HasPending())
		})
	}
}

func TestCapturedTranscriptBatchDiscardResumesTheNextReadyBatch(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	paused := pauseCapturedBatchBeforeUnreadyReturn(t, &queue)
	next, err := reserveCapturedTranscriptBatch(queueReservationTranscript{}, &queue)
	require.NoError(t, err)
	var order []string
	require.ErrorIs(t, next.PrepareStream(func() (CapturedTranscriptOperation, bool, error) {
		order = append(order, "next batch")
		return CapturedTranscriptOperation{}, true, nil
	}), ErrOutputInFlight)
	later := queue.Append(OutputWrite{Label: "record the following row", Run: func() error {
		assert.True(t, next.ticket.Done(), "the next batch must complete before its following row")
		order = append(order, "row")
		return nil
	}})[0]
	require.True(t, paused.batch.Discard())
	assert.Nil(t, queue.retry)
	paused.resume()
	require.NoError(t, paused.result(t))
	assert.EqualValues(t, 1, paused.attempts.Load(), "the discarded batch must never run again")
	assert.False(t, paused.batch.ticket.Done(), "discarding a batch does not persist it")
	assert.True(t, next.ticket.Done())
	assert.True(t, later.Done())
	assert.Equal(t, []string{"next batch", "row"}, order)
	assert.False(t, queue.HasPending())
}

func TestCapturedTranscriptBatchReadinessDoesNotRepeatARealCallbackFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "storage failure", err: assert.AnError},
		{name: "callback remains in flight", err: ErrOutputInFlight},
	} {
		t.Run(failure.name, func(t *testing.T) {
			t.Parallel()
			var queue OutputQueue
			paused := pauseCapturedBatchBeforeUnreadyReturn(t, &queue)
			var iteratorCalls atomic.Int32
			require.ErrorIs(t, paused.batch.PrepareStream(func() (CapturedTranscriptOperation, bool, error) {
				iteratorCalls.Add(1)
				return CapturedTranscriptOperation{}, false, failure.err
			}), ErrOutputInFlight)
			later := queue.Append(OutputWrite{Label: "record the following row", Run: func() error {
				t.Error("a following row ran after a failed batch")
				return nil
			}})[0]
			paused.resume()
			require.ErrorIs(t, paused.result(t), failure.err)
			assert.EqualValues(t, 2, paused.attempts.Load(), "one readiness transition permits one new attempt")
			assert.EqualValues(t, 1, iteratorCalls.Load())
			assert.False(t, paused.batch.ticket.Done())
			assert.False(t, later.Done())
			assert.True(t, queue.HasPending())
			assert.Nil(t, queue.retry)
		})
	}
}

func TestOutputQueueConcurrentFlushDoesNotInventReadiness(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	paused := pauseCapturedBatchBeforeUnreadyReturn(t, &queue)
	for range 3 {
		require.ErrorIs(t, queue.Flush(), ErrOutputInFlight)
	}
	paused.resume()
	require.ErrorIs(t, paused.result(t), ErrOutputInFlight)
	assert.EqualValues(t, 1, paused.attempts.Load())
	assert.False(t, paused.batch.ticket.Done())
	assert.True(t, queue.HasPending())
	assert.Nil(t, queue.retry)
}

func TestCapturedTranscriptBatchReadinessDoesNotWakeAnotherReservation(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	paused := pauseCapturedBatchBeforeUnreadyReturn(t, &queue)
	next, err := reserveCapturedTranscriptBatch(queueReservationTranscript{}, &queue)
	require.NoError(t, err)
	require.ErrorIs(t, next.Prepare(func() []CapturedTranscriptOperation { return nil }), ErrOutputInFlight)
	paused.resume()
	require.ErrorIs(t, paused.result(t), ErrOutputInFlight)
	assert.EqualValues(t, 1, paused.attempts.Load(), "preparing another reservation cannot make the head ready")
	assert.False(t, paused.batch.ticket.Done())
	assert.False(t, next.ticket.Done())
	assert.True(t, queue.HasPending())
	assert.Nil(t, queue.retry)
}

func TestOutputQueueReportsARealFailureAfterTheHeadWasRemoved(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	owner := NewOutputOwner()
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	first := queue.Append(OutputWrite{Owner: owner, Label: "write the removed row", Run: func() error {
		close(entered)
		<-release
		return assert.AnError
	}})[0]
	later := queue.Append(OutputWrite{Label: "record the following row", Run: func() error {
		t.Error("the queue ignored a genuine error from its removed head")
		return nil
	}})[0]
	done := make(chan error, 1)
	go func() { done <- queue.Flush() }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the output callback did not start")
	}
	queue.ClearOwner(owner)
	resume()
	select {
	case err := <-done:
		require.ErrorIs(t, err, assert.AnError)
	case <-ctx.Done():
		t.Fatal("the output drainer did not report its callback failure")
	}
	assert.False(t, first.Done())
	assert.False(t, later.Done())
	assert.True(t, queue.HasPending())
	assert.Nil(t, queue.retry)
}

type queueReservationTranscript struct{ TranscriptServices }

func (queueReservationTranscript) ReserveTranscriptOutput(queue *OutputQueue, writes ...OutputWrite) ([]*OutputTicket, error) {
	return queue.Append(writes...), nil
}

func (queueReservationTranscript) ValidateTranscriptOutput(ticket *OutputTicket) error {
	return ValidateTranscriptOutputTicket(ticket)
}
