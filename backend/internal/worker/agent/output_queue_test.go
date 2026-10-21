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

func TestOutputQueueKeepsTheFailedWriteBeforeLaterWritesAndEffects(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	var order []string
	fail := true
	first := queue.Append(OutputWrite{Label: "first", Run: func() error {
		if fail {
			return assert.AnError
		}
		order = append(order, "first")
		return nil
	}})[0]
	require.ErrorIs(t, queue.Flush(), assert.AnError)
	later := queue.Append(OutputWrite{Label: "later", Run: func() error { order = append(order, "later"); return nil }})[0]
	queue.Append(OutputWrite{Label: "release", Run: func() error { order = append(order, "release"); return nil }})
	require.ErrorIs(t, queue.Flush(), assert.AnError)
	assert.False(t, first.Done())
	assert.False(t, later.Done())
	assert.Empty(t, order)
	fail = false
	require.NoError(t, queue.Flush())
	assert.Equal(t, []string{"first", "later", "release"}, order)
	assert.True(t, first.Done())
	assert.True(t, later.Done())
	require.NoError(t, queue.Flush())
	assert.Len(t, order, 3)
}

func TestOutputQueueAllowsReentrantSubmissionWithoutRepeatingTheCurrentWrite(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	var order []string
	queue.Append(OutputWrite{Label: "outer", Run: func() error {
		order = append(order, "outer")
		queue.Append(OutputWrite{Label: "inner", Run: func() error { order = append(order, "inner"); return nil }})
		assert.ErrorIs(t, queue.Flush(), ErrOutputInFlight)
		return nil
	}})
	require.NoError(t, queue.Flush())
	assert.Equal(t, []string{"outer", "inner"}, order)
}

func TestOutputQueueConcurrentFlushWritesEachEntryOnce(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	var group sync.WaitGroup
	var mu sync.Mutex
	var stored []int
	for i := range 100 {
		queue.Append(OutputWrite{Label: "row", Run: func() error { mu.Lock(); stored = append(stored, i); mu.Unlock(); return nil }})
	}
	for range 8 {
		group.Go(func() {
			err := queue.Flush()
			assert.True(t, err == nil || errors.Is(err, ErrOutputInFlight), "%v", err)
		})
	}
	group.Wait()
	require.Len(t, stored, 100)
	for i, value := range stored {
		assert.Equal(t, i, value)
	}
}

func TestOutputQueueClearOwnerKeepsOtherWrites(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	owner := NewOutputOwner()
	var stored []string
	queue.Append(OutputWrite{Owner: owner, ScopeID: "scope", RetainedBytes: 12, Label: "generation", Run: func() error { t.Error("the discarded write ran"); return nil }},
		OutputWrite{Label: "native", Run: func() error { stored = append(stored, "native"); return nil }})
	assert.Equal(t, 12, queue.RetainedBytes(owner, "scope"))
	queue.ClearOwner(owner)
	require.NoError(t, queue.Flush())
	assert.Equal(t, []string{"native"}, stored)
}

func TestOutputQueueMoveOwnerKeepsTheOriginalCallback(t *testing.T) {
	t.Parallel()
	var source, destination OutputQueue
	owner, targetOwner := NewOutputOwner(), NewOutputOwner()
	var stored []string
	source.Append(OutputWrite{Owner: owner, Label: "old destination", Run: func() error { stored = append(stored, "source"); return nil }})
	destination.Append(OutputWrite{Label: "destination", Run: func() error { stored = append(stored, "destination"); return nil }})
	source.MoveOwnerTo(owner, &destination, targetOwner)
	assert.False(t, source.HasOwner(owner))
	assert.True(t, destination.HasOwner(targetOwner))
	require.NoError(t, destination.Flush())
	assert.Equal(t, []string{"destination", "source"}, stored)
}

func TestOutputQueueCancellationStopsRetriesAndKeepsPendingWrites(t *testing.T) {
	t.Parallel()
	var queue OutputQueue
	clock := testutil.NewQuartzMock(t)
	ctx, cancel := context.WithCancel(t.Context())
	trap := clock.Trap().NewTimer(OutputRetryTimerTag)
	t.Cleanup(func() { trap.Close(); cancel(); queue.StopRetries() })
	queue.ConfigureRetry(ctx, clock, nil)
	var attempts atomic.Int32
	queue.Append(OutputWrite{Label: "unavailable", Run: func() error { attempts.Add(1); return assert.AnError }})
	require.ErrorIs(t, queue.Flush(), assert.AnError)
	waitCtx := testutil.DeadlineContext(t)
	assert.Equal(t, outputRetryDelay, testutil.WaitForTimer(t, waitCtx, trap))
	cancel()
	queue.StopRetries()
	clock.Advance(time.Hour).MustWait(waitCtx)
	assert.EqualValues(t, 1, attempts.Load())
	assert.True(t, queue.HasPending())
}
