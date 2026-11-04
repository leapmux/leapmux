package service

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertLockHeld fails the test when mu is free. A TryLock that succeeds holds the
// lock, so this releases it. Otherwise the code under test would deadlock on it,
// and the regression would show as a hang instead of a failure.
func assertLockHeld(t *testing.T, mu *sync.Mutex, msg string) {
	t.Helper()
	if mu.TryLock() {
		mu.Unlock()
		assert.Fail(t, msg)
	}
}

func TestPublicationQueues_DeliversInOrderAndReleasesTheEmptyQueue(t *testing.T) {
	t.Parallel()

	var queues publicationQueues[int]
	for i := range 3 {
		queues.enqueue("entity", func() int { return i })
	}
	require.Len(t, queues.queues, 1, "a queue exists while it holds undelivered items")

	var delivered []int
	queues.drain("entity", func(item int) { delivered = append(delivered, item) })

	assert.Equal(t, []int{0, 1, 2}, delivered)
	assert.Empty(t, queues.queues, "the drain must release the empty queue")
}

// build must run inside the lock, or a change that build did not see could enter
// the queue ahead of an item that states the older value.
func TestPublicationQueues_BuildRunsInsideTheLock(t *testing.T) {
	t.Parallel()

	var queues publicationQueues[int]
	queues.enqueue("entity", func() int {
		assertLockHeld(t, &queues.mu, "build must hold the queue's lock")
		return 1
	})
}

func TestPublicationQueues_DrainOfAnEntityWithNoQueueDeliversNothing(t *testing.T) {
	t.Parallel()

	var queues publicationQueues[int]
	queues.drain("absent", func(int) { assert.Fail(t, "nothing was queued") })
	assert.Empty(t, queues.queues)
}

// A delivery that publishes again must not recurse or overtake. Its item waits
// for the current delivery, and the current drainer delivers it.
func TestPublicationQueues_AReentrantDrainLeavesItsItemToTheCurrentDrainer(t *testing.T) {
	t.Parallel()

	var queues publicationQueues[int]
	var delivered []int
	var deliver func(item int)
	deliver = func(item int) {
		if item == 1 {
			queues.enqueue("entity", func() int { return 3 })
			queues.drain("entity", deliver)
		}
		delivered = append(delivered, item)
	}
	queues.enqueue("entity", func() int { return 1 })
	queues.enqueue("entity", func() int { return 2 })

	queues.drain("entity", deliver)

	assert.Equal(t, []int{1, 2, 3}, delivered)
	assert.Empty(t, queues.queues)
}

// Items of one entity reach the drainer that holds it, and the other entity has a
// drainer of its own.
func TestPublicationQueues_EntitiesDrainIndependently(t *testing.T) {
	t.Parallel()

	var queues publicationQueues[int]
	parked := make(chan struct{})
	release := make(chan struct{})
	queues.enqueue("stalled", func() int { return 1 })
	var drainer sync.WaitGroup
	drainer.Go(func() {
		queues.drain("stalled", func(int) {
			close(parked)
			<-release
		})
	})
	<-parked

	queues.enqueue("other", func() int { return 2 })
	var delivered []int
	queues.drain("other", func(item int) { delivered = append(delivered, item) })

	assert.Equal(t, []int{2}, delivered)
	close(release)
	drainer.Wait()
	assert.Empty(t, queues.queues)
}
