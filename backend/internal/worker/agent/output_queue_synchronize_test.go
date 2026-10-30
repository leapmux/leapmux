package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func waitOutputResult(t *testing.T, ctx context.Context, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		t.Fatal("output synchronization did not finish")
		return ctx.Err()
	}
}

func startOutputSynchronization(t *testing.T, queue *OutputQueue, ctx context.Context, pending int) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- queue.Synchronize(ctx) }()
	require.Eventually(t, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.pending) == pending
	}, 30*time.Second, time.Millisecond, "the synchronization marker was not admitted")
	return result
}

func waitOutputDrainer(t *testing.T, queue *OutputQueue) error {
	t.Helper()
	queue.mu.Lock()
	state := queue.flushState
	queue.mu.Unlock()
	if state == nil {
		return nil
	}
	ctx := testutil.DeadlineContext(t)
	select {
	case <-state.done:
		return state.err
	case <-ctx.Done():
		t.Fatal("the output drainer did not finish")
		return ctx.Err()
	}
}

func TestOutputQueueSynchronizeWaitsThroughItsOwnMarker(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		label := "preceding write"
		if empty {
			label = "empty"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			var queue OutputQueue
			var writes atomic.Int32
			if !empty {
				queue.Append(OutputWrite{Label: "preceding row", Run: func() error { writes.Add(1); return nil }})
			}
			require.NoError(t, queue.Synchronize(testutil.DeadlineContext(t)))
			if empty {
				assert.Zero(t, writes.Load())
			} else {
				assert.Equal(t, int32(1), writes.Load())
			}
			require.NoError(t, waitOutputDrainer(t, &queue))
			assert.False(t, queue.HasPending())
		})
	}
}

func TestOutputQueueSynchronizeRejectsMissingOrCancelledContextBeforeAdmission(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	var writes atomic.Int32
	queue.Append(OutputWrite{Label: "retained row", Run: func() error { writes.Add(1); return nil }})
	assert.ErrorContains(t, queue.Synchronize(nil), "requires a context") //nolint:staticcheck // This negative test verifies explicit nil-context rejection.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, queue.Synchronize(ctx), context.Canceled)
	assert.Zero(t, writes.Load())
	queue.mu.Lock()
	assert.Len(t, queue.pending, 1)
	queue.mu.Unlock()
}

func TestOutputQueueSynchronizeReportsEarlierFailureAndKeepsTheWrite(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	owner := NewOutputOwner()
	var attempts atomic.Int32
	ticket := queue.Append(OutputWrite{Owner: owner, Label: "failed row", Run: func() error { attempts.Add(1); return assert.AnError }})[0]
	assert.ErrorIs(t, queue.Synchronize(testutil.DeadlineContext(t)), assert.AnError)
	assert.Equal(t, int32(1), attempts.Load())
	assert.True(t, queue.HasOwner(owner))
	assert.False(t, ticket.Done())
	queue.mu.Lock()
	assert.Equal(t, []*OutputTicket{ticket}, queue.pending, "the failed row remains and only the marker leaves")
	queue.mu.Unlock()
}

func TestOutputQueueSynchronizeObservesAnExistingDrainersResult(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		label := "success"
		if failure {
			label = "failure"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			var queue OutputQueue
			entered, release := make(chan struct{}), make(chan struct{})
			resume := sync.OnceFunc(func() { close(release) })
			t.Cleanup(resume)
			var attempts atomic.Int32
			queue.Append(OutputWrite{Label: "held row", Run: func() error {
				attempts.Add(1)
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				if failure {
					return assert.AnError
				}
				return nil
			}})
			flushed := make(chan error, 1)
			go func() { flushed <- queue.Flush() }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("the existing drainer did not enter its callback")
			}
			result := startOutputSynchronization(t, &queue, ctx, 2)
			resume()
			if failure {
				assert.ErrorIs(t, waitOutputResult(t, ctx, result), assert.AnError)
				assert.ErrorIs(t, waitOutputResult(t, ctx, flushed), assert.AnError)
			} else {
				assert.NoError(t, waitOutputResult(t, ctx, result))
				assert.NoError(t, waitOutputResult(t, ctx, flushed))
			}
			assert.Equal(t, int32(1), attempts.Load())
		})
	}
}

func TestOutputQueueSynchronizeWaitsForActualCaptureReadiness(t *testing.T) {
	t.Parallel()
	for _, prepareBeforeExit := range []bool{false, true} {
		label := "after drainer exit"
		if prepareBeforeExit {
			label = "before drainer exit"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			var queue OutputQueue
			paused := pauseCapturedBatchBeforeUnreadyReturn(t, &queue)
			result := startOutputSynchronization(t, &queue, ctx, 2)
			if prepareBeforeExit {
				require.ErrorIs(t, paused.batch.Prepare(func() []CapturedTranscriptOperation { return nil }), ErrOutputInFlight)
				paused.resume()
				require.NoError(t, paused.result(t))
			} else {
				paused.resume()
				require.ErrorIs(t, paused.result(t), ErrOutputInFlight)
				assert.Equal(t, int32(1), paused.attempts.Load(), "a marker cannot invent capture readiness")
				err := paused.batch.Prepare(func() []CapturedTranscriptOperation { return nil })
				require.True(t, err == nil || errors.Is(err, ErrOutputInFlight), "%v", err)
			}
			require.NoError(t, waitOutputResult(t, ctx, result))
			assert.True(t, paused.batch.ticket.Done())
			assert.Equal(t, int32(2), paused.attempts.Load())
		})
	}
}

func TestOutputQueueSynchronizeWakesWhenAnUnreadyOwnerLeaves(t *testing.T) {
	t.Parallel()
	for _, transfer := range []bool{false, true} {
		label := "clear"
		if transfer {
			label = "transfer"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			var queue, destination OutputQueue
			paused := pauseCapturedBatchBeforeUnreadyReturn(t, &queue)
			result := startOutputSynchronization(t, &queue, ctx, 2)
			paused.resume()
			require.ErrorIs(t, paused.result(t), ErrOutputInFlight)
			if transfer {
				queue.MoveOwnerTo(paused.batch.owner, &destination, NewOutputOwner())
			} else {
				queue.ClearOwner(paused.batch.owner)
			}
			require.NoError(t, waitOutputResult(t, ctx, result))
			assert.False(t, paused.batch.ticket.Done())
			if transfer {
				assert.Same(t, &destination, paused.batch.ticket.Queue())
				err := paused.batch.Prepare(func() []CapturedTranscriptOperation { return nil })
				require.True(t, err == nil || errors.Is(err, ErrOutputInFlight), "%v", err)
				// The captured batch retains its original writer. Transfer changes
				// ordering ownership, so the receiver flushes its own queue.
				require.NoError(t, destination.FlushAfterReady(paused.batch.ticket))
				assert.True(t, paused.batch.ticket.Done())
			}
		})
	}
}

func TestOutputQueueSynchronizeDoesNotWaitForLaterWrites(t *testing.T) {
	t.Parallel()
	for _, laterFailure := range []bool{false, true} {
		label := "later blocking callback"
		if laterFailure {
			label = "later failure"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			var queue OutputQueue
			firstEntered, firstRelease := make(chan struct{}), make(chan struct{})
			laterEntered, laterRelease := make(chan struct{}), make(chan struct{})
			resumeFirst := sync.OnceFunc(func() { close(firstRelease) })
			resumeLater := sync.OnceFunc(func() { close(laterRelease) })
			t.Cleanup(resumeFirst)
			t.Cleanup(resumeLater)
			queue.Append(OutputWrite{Label: "earlier row", Run: func() error { close(firstEntered); <-firstRelease; return nil }})
			result := startOutputSynchronization(t, &queue, ctx, 2)
			select {
			case <-firstEntered:
			case <-ctx.Done():
				t.Fatal("the synchronization drainer did not start")
			}
			queue.Append(OutputWrite{Label: "later row", Run: func() error {
				close(laterEntered)
				if laterFailure {
					return assert.AnError
				}
				<-laterRelease
				return nil
			}})
			resumeFirst()
			select {
			case <-laterEntered:
			case <-ctx.Done():
				t.Fatal("the later callback did not start")
			}
			require.NoError(t, waitOutputResult(t, ctx, result), "a later write cannot change the completed marker")
			resumeLater()
			err := waitOutputDrainer(t, &queue)
			if laterFailure {
				assert.ErrorIs(t, err, assert.AnError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOutputQueueSynchronizeCancellationDoesNotWaitForABlockedCallback(t *testing.T) {
	t.Parallel()
	waitCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(waitCtx)
	defer cancel()
	var queue OutputQueue
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	owner := NewOutputOwner()
	ticket := queue.Append(OutputWrite{Owner: owner, Label: "blocked row", Run: func() error { close(entered); <-release; return nil }})[0]
	result := startOutputSynchronization(t, &queue, ctx, 2)
	select {
	case <-entered:
	case <-waitCtx.Done():
		t.Fatal("the output callback did not start")
	}
	cancel()
	assert.ErrorIs(t, waitOutputResult(t, waitCtx, result), context.Canceled)
	assert.True(t, queue.HasOwner(owner))
	assert.False(t, ticket.Done())
	queue.mu.Lock()
	assert.Equal(t, []*OutputTicket{ticket}, queue.pending)
	queue.mu.Unlock()
	resume()
	require.NoError(t, waitOutputDrainer(t, &queue))
	assert.True(t, ticket.Done())
}

func TestOutputQueueSynchronizeConcurrentMarkersSurviveOtherOwnerCleanup(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	var queue OutputQueue
	paused := pauseCapturedBatchBeforeUnreadyReturn(t, &queue)
	firstCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	first := startOutputSynchronization(t, &queue, firstCtx, 2)
	second := startOutputSynchronization(t, &queue, ctx, 3)
	cancel()
	assert.ErrorIs(t, waitOutputResult(t, ctx, first), context.Canceled)
	queue.ClearOwner(paused.batch.owner)
	paused.resume()
	require.NoError(t, paused.result(t))
	require.NoError(t, waitOutputResult(t, ctx, second))
	assert.False(t, paused.batch.ticket.Done())
}

func TestOutputQueueSynchronizeConcurrentEmptyHandoffsLoseNoMarker(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	var queue OutputQueue
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() { assert.NoError(t, queue.Synchronize(ctx)) })
	}
	group.Wait()
	require.NoError(t, waitOutputDrainer(t, &queue))
	assert.False(t, queue.HasPending())
}

func TestOutputQueueSynchronizeUnreadyCaptureDoesNotBusyLoop(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	var queue OutputQueue
	var attempts atomic.Int32
	observed := make(chan struct{})
	queue.Append(OutputWrite{Label: "unready capture", Run: func() error {
		if attempts.Add(1) == 1 {
			close(observed)
			return ErrOutputInFlight
		}
		return errors.New("the queue retried without a readiness transition")
	}})
	result := startOutputSynchronization(t, &queue, ctx, 2)
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("the unready capture did not run")
	}
	require.ErrorIs(t, waitOutputDrainer(t, &queue), ErrOutputInFlight)
	var waiting, premature bool
	var prematureError error
	require.Eventually(t, func() bool {
		queue.mu.Lock()
		waiting = queue.readinessChanged != nil
		queue.mu.Unlock()
		select {
		case prematureError = <-result:
			premature = true
			return true
		default:
		}
		return waiting
	}, 30*time.Second, time.Millisecond)
	require.False(t, premature, "synchronization returned before readiness: %v", prematureError)
	require.True(t, waiting)
	assert.Equal(t, int32(1), attempts.Load())
	queue.ClearOwner(nil)
	require.NoError(t, waitOutputResult(t, ctx, result))
	assert.Equal(t, int32(1), attempts.Load())
}

// heldDoneContext pauses the waiter after it admits its marker. Queue callbacks
// use the underlying context, so external drains can establish both outcomes.
type heldDoneContext struct {
	context.Context
	evaluated chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (ctx *heldDoneContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.evaluated) })
	<-ctx.release
	return ctx.Context.Done()
}

func TestOutputQueueSynchronizeKeepsEarlierFailureWhenRecoveryReachesItsMarker(t *testing.T) {
	t.Parallel()
	for _, initiallyUnready := range []bool{false, true} {
		label := "initial failure"
		if initiallyUnready {
			label = "failure after unready drain"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			waitCtx := testutil.DeadlineContext(t)
			ctx := &heldDoneContext{Context: waitCtx, evaluated: make(chan struct{}), release: make(chan struct{})}
			resumeWaiter := sync.OnceFunc(func() { close(ctx.release) })
			t.Cleanup(resumeWaiter)
			var queue OutputQueue
			var attempts atomic.Int32
			queue.Append(OutputWrite{Label: "retained row", Run: func() error {
				attempt := attempts.Add(1)
				if initiallyUnready && attempt == 1 {
					return ErrOutputInFlight
				}
				failureAttempt := int32(1)
				if initiallyUnready {
					failureAttempt = 2
				}
				if attempt == failureAttempt {
					return assert.AnError
				}
				return nil
			}})
			result := make(chan error, 1)
			go func() { result <- queue.Synchronize(ctx) }()
			select {
			case <-ctx.evaluated:
			case <-waitCtx.Done():
				t.Fatal("the synchronization waiter did not admit its marker")
			}
			firstError := waitOutputDrainer(t, &queue)
			if initiallyUnready {
				require.ErrorIs(t, firstError, ErrOutputInFlight)
				require.ErrorIs(t, queue.Flush(), assert.AnError)
			} else {
				require.ErrorIs(t, firstError, assert.AnError)
			}
			require.NoError(t, queue.Flush(), "external recovery reaches the already admitted marker")
			resumeWaiter()
			assert.ErrorIs(t, waitOutputResult(t, waitCtx, result), assert.AnError, "later success cannot replace an earlier failure")
		})
	}
}

func TestOutputQueueDropsCompletedFlushStateWhenOwnerCleanupEmptiesTheQueue(t *testing.T) {
	t.Parallel()
	for _, transfer := range []bool{false, true} {
		label := "clear"
		if transfer {
			label = "transfer"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			var queue, destination OutputQueue
			owner := NewOutputOwner()
			queue.Append(OutputWrite{Owner: owner, Label: "retained row", Run: func() error { return assert.AnError }})
			require.ErrorIs(t, queue.Flush(), assert.AnError)
			if transfer {
				queue.MoveOwnerTo(owner, &destination, NewOutputOwner())
			} else {
				queue.ClearOwner(owner)
			}
			assert.False(t, queue.HasPending())
			queue.mu.Lock()
			assert.Nil(t, queue.flushState, "an empty queue must not retain an old failure")
			queue.mu.Unlock()
		})
	}
}
