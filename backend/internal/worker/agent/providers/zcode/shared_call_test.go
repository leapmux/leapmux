package zcode

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeSharedCalls_ConcurrentCallersShareOneCall(t *testing.T) {
	t.Parallel()

	var calls zcodeSharedCalls[string, int]
	var runs atomic.Int32
	release := make(chan struct{})
	results := make(chan int, 2)
	for range 2 {
		go func() {
			value, err := calls.do(context.Background(), "key", time.Hour, func(context.Context) (int, error) {
				<-release
				return int(runs.Add(1)), nil
			})
			assert.NoError(t, err)
			results <- value
		}()
	}
	require.Eventually(t, func() bool { return calls.waiting("key") == 2 }, 30*time.Second, time.Millisecond)
	close(release)
	assert.Equal(t, 1, <-results)
	assert.Equal(t, 1, <-results)

	value, err := calls.do(context.Background(), "key", time.Hour, func(context.Context) (int, error) {
		return int(runs.Add(1)), nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, value, "no result outlives its call")
}

func TestZCodeSharedCalls_TheLastCallerToLeaveEndsTheCall(t *testing.T) {
	t.Parallel()

	var calls zcodeSharedCalls[string, int]
	ended := make(chan error, 1)
	first, cancelFirst := context.WithCancel(context.Background())
	second, cancelSecond := context.WithCancel(context.Background())
	errs := make(chan error, 2)
	for _, ctx := range []context.Context{first, second} {
		go func() {
			_, err := calls.do(ctx, "key", time.Hour, func(callCtx context.Context) (int, error) {
				<-callCtx.Done()
				ended <- callCtx.Err()
				return 0, callCtx.Err()
			})
			errs <- err
		}()
	}
	require.Eventually(t, func() bool { return calls.waiting("key") == 2 }, 30*time.Second, time.Millisecond)

	cancelFirst()
	require.ErrorIs(t, <-errs, context.Canceled)
	require.Eventually(t, func() bool { return calls.waiting("key") == 1 }, 30*time.Second, time.Millisecond)
	select {
	case <-ended:
		t.Fatal("one caller that leaves must not end the call for the other")
	default:
	}

	cancelSecond()
	require.ErrorIs(t, <-errs, context.Canceled)
	require.ErrorIs(t, <-ended, context.Canceled, "the last caller that leaves ends the call")
	assert.Zero(t, calls.waitingInAll())
}

func TestZCodeSharedCalls_TheTimeoutLimitsTheCall(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var calls zcodeSharedCalls[string, int]
		_, err := calls.do(context.Background(), "key", time.Hour, func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		})
		require.True(t, errors.Is(err, context.DeadlineExceeded), "the error is %v", err)
	})
}

func TestZCodeSharedCalls_DoesNotStartACancelledCall(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var calls zcodeSharedCalls[string, int]
		var runs atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := calls.do(ctx, "key", time.Hour, func(context.Context) (int, error) {
			return int(runs.Add(1)), nil
		})
		require.ErrorIs(t, err, context.Canceled)
		synctest.Wait()
		assert.Zero(t, runs.Load())
	})
}

func TestZCodeSharedCalls_RejectsPublicationAfterItsDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var calls zcodeSharedCalls[string, int]
		var publications atomic.Int32
		finished := make(chan struct{})
		value, err := calls.do(t.Context(), "key", time.Hour, func(ctx context.Context) (int, error) {
			defer close(finished)
			<-ctx.Done()
			calls.publish(ctx, "key", func() { publications.Add(1) })
			return 42, nil
		})
		<-finished
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Zero(t, value)
		assert.Zero(t, publications.Load())
	})
}

func TestZCodeSharedCalls_TimeoutReleasesWaitersBeforeTheFunctionReturns(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var calls zcodeSharedCalls[string, int]
		ctx, cancel := context.WithCancel(t.Context())
		release, expired, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		t.Cleanup(func() {
			cancel()
			close(release)
			select {
			case <-finished:
			case <-time.After(30 * time.Second):
				t.Error("the call did not finish during cleanup")
			}
		})
		go func() {
			_, err := calls.do(ctx, "key", time.Hour, func(ctx context.Context) (int, error) {
				defer close(finished)
				<-ctx.Done()
				close(expired)
				<-release
				return 42, nil
			})
			result <- err
		}()
		<-expired
		select {
		case err := <-result:
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Zero(t, calls.waiting("key"))
		case <-time.After(30 * time.Second):
			t.Fatal("the shared-call deadline did not release its waiter")
		}
	})
}

func TestZCodeSharedCalls_CommittedResultWinsCancellation(t *testing.T) {
	t.Parallel()
	for _, outcome := range []error{nil, errors.New("completed failure")} {
		var calls zcodeSharedCalls[string, int]
		call := &zcodeSharedCall[int]{done: make(chan struct{}), value: 0, err: outcome}
		close(call.done)
		value, err := calls.finishWait("key", call, context.Canceled)
		assert.Zero(t, value)
		assert.ErrorIs(t, err, outcome)
	}
}

func TestZCodeSharedCalls_NonpositiveDeadlineStartsNoWork(t *testing.T) {
	t.Parallel()
	for _, timeout := range []time.Duration{0, -time.Hour} {
		var calls zcodeSharedCalls[string, int]
		value, err := calls.do(t.Context(), "", timeout, func(context.Context) (int, error) {
			t.Error("an expired deadline started work")
			return 42, nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Zero(t, value)
		assert.Zero(t, calls.waitingInAll())
	}
}

func TestZCodeSharedCalls_HandlesAnEmptyKeyAndZeroResult(t *testing.T) {
	t.Parallel()
	var calls zcodeSharedCalls[string, int]
	value, err := calls.do(t.Context(), "", time.Duration(1<<63-1), func(context.Context) (int, error) { return 0, nil })
	require.NoError(t, err)
	assert.Zero(t, value)
	assert.Zero(t, calls.waitingInAll())
}

// Cancelling one waiter must not cancel the call: the survivor's result may
// still be published.
func TestZCodeSharedCalls_ACancelledWaiterStillPublishesForTheSurvivor(t *testing.T) {
	t.Parallel()

	var calls zcodeSharedCalls[string, int]
	published := make(chan bool, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	fn := func(callCtx context.Context) (int, error) {
		<-release
		published <- calls.publish(callCtx, "key", func() {})
		return 42, nil
	}

	first, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstErr := make(chan error, 1)
	survivor := make(chan int, 1)
	go func() {
		_, err := calls.do(first, "key", time.Hour, fn)
		firstErr <- err
	}()
	go func() {
		value, err := calls.do(context.Background(), "key", time.Hour, fn)
		assert.NoError(t, err)
		survivor <- value
	}()
	require.Eventually(t, func() bool { return calls.waiting("key") == 2 }, 30*time.Second, time.Millisecond)

	cancelFirst()
	require.ErrorIs(t, <-firstErr, context.Canceled, "the cancelled waiter gets no result")
	require.Eventually(t, func() bool { return calls.waiting("key") == 1 }, 30*time.Second, time.Millisecond,
		"one cancelled waiter must leave the call for the survivor")

	unblock()
	assert.True(t, <-published, "the call of a survivor may still publish")
	assert.Equal(t, 42, <-survivor)
}

// The last waiter to leave cancels the call: the abandoned result cannot
// publish.
func TestZCodeSharedCalls_LastWaiterCancellationRefusesPublication(t *testing.T) {
	t.Parallel()

	var calls zcodeSharedCalls[string, int]
	published := make(chan bool, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := calls.do(ctx, "key", time.Hour, func(callCtx context.Context) (int, error) {
			close(started)
			<-release
			published <- calls.publish(callCtx, "key", func() {})
			return 42, nil
		})
		done <- err
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled, "the cancelled waiter gets no result")

	unblock()
	assert.False(t, <-published, "an abandoned call cannot publish")
	assert.Zero(t, calls.waitingInAll())
}
