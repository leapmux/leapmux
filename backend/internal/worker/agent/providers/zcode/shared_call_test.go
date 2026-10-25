package zcode

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
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

	var calls zcodeSharedCalls[string, int]
	_, err := calls.do(context.Background(), "key", time.Millisecond, func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	require.True(t, errors.Is(err, context.DeadlineExceeded), "the error is %v", err)
}
