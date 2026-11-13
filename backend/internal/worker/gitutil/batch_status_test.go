package gitutil

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func batchDeadlineContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestBatchCancellationDoesNotWaitOrMutateResults(t *testing.T) {
	for _, count := range []int{1, 2, 3, 9} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			const capacity = 2
			started := make(chan string, capacity)
			finished := make(chan struct{}, capacity)
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseAll)
			executor := &gitStatusBatchExecutor{slots: make(chan struct{}, capacity)}
			executor.probe = func(_ context.Context, dir string) *leapmuxv1.GitRepoStatus {
				started <- dir
				<-release
				defer func() { finished <- struct{}{} }()
				return &leapmuxv1.GitRepoStatus{Branch: dir}
			}
			dirs := make([]string, count)
			for index := range dirs {
				dirs[index] = fmt.Sprint(index)
			}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			done := make(chan []*leapmuxv1.GitRepoStatus, 1)
			go func() { done <- executor.get(ctx, dirs) }()
			admitted := min(count, capacity)
			for range admitted {
				select {
				case <-started:
				case <-batchDeadlineContext(t).Done():
					t.Fatal("probe did not start")
				}
			}
			cancel()
			var results []*leapmuxv1.GitRepoStatus
			select {
			case results = <-done:
			case <-batchDeadlineContext(t).Done():
				t.Fatal("canceled batch waited for blocked probes")
			}
			for _, status := range results {
				require.Nil(t, status)
			}
			// All workers finish before the final slice read. A worker write to
			// returned storage would race this read and change its nil value.
			releaseAll()
			for range admitted {
				select {
				case <-finished:
				case <-batchDeadlineContext(t).Done():
					t.Fatal("probe did not finish")
				}
			}
			for range capacity {
				select {
				case executor.slots <- struct{}{}:
				case <-batchDeadlineContext(t).Done():
					t.Fatal("worker did not release capacity")
				}
			}
			for range capacity {
				<-executor.slots
			}
			for _, status := range results {
				assert.Nil(t, status)
			}
		})
	}
}

func TestBatchSharesCapacityAcrossCanceledCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const capacity = 2
		release := make(chan struct{})
		defer close(release)
		started := make(chan struct{}, 16)
		var count atomic.Int32
		executor := &gitStatusBatchExecutor{slots: make(chan struct{}, capacity), probe: func(context.Context, string) *leapmuxv1.GitRepoStatus {
			count.Add(1)
			started <- struct{}{}
			<-release
			return nil
		}}
		first, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan struct{}, 1)
		go func() { executor.get(first, []string{"a", "b", "c"}); result <- struct{}{} }()
		for range capacity {
			select {
			case <-started:
			case <-batchDeadlineContext(t).Done():
				t.Fatal("initial probes did not start")
			}
		}
		cancel()
		select {
		case <-result:
		case <-batchDeadlineContext(t).Done():
			t.Fatal("first cancellation blocked")
		}
		for range 4 {
			ctx, stop := context.WithCancel(t.Context())
			done := make(chan struct{}, 1)
			go func() { executor.get(ctx, []string{"later"}); done <- struct{}{} }()
			synctest.Wait()
			assert.Equal(t, int32(capacity), count.Load(), "later calls must wait for shared admission")
			stop()
			select {
			case <-done:
			case <-batchDeadlineContext(t).Done():
				t.Fatal("later cancellation blocked")
			}
		}
		assert.Equal(t, int32(capacity), count.Load())
	})
}

func TestBatchMapsDuplicatesAndNilWithoutExtraProbes(t *testing.T) {
	var mu sync.Mutex
	calls := make(map[string]int)
	executor := &gitStatusBatchExecutor{slots: make(chan struct{}, 2), probe: func(_ context.Context, dir string) *leapmuxv1.GitRepoStatus {
		mu.Lock()
		calls[dir]++
		mu.Unlock()
		if dir == "nil" {
			return nil
		}
		return &leapmuxv1.GitRepoStatus{Branch: dir}
	}}
	results := executor.get(batchDeadlineContext(t), []string{"a", "", "a", "b", "nil"})
	require.Len(t, results, 5)
	assert.Same(t, results[0], results[2])
	assert.Nil(t, results[1])
	assert.Nil(t, results[4])
	assert.Equal(t, "b", results[3].Branch)
	assert.Equal(t, map[string]int{"a": 1, "b": 1, "nil": 1}, calls)
	assert.Empty(t, executor.get(t.Context(), nil))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	assert.Equal(t, []*leapmuxv1.GitRepoStatus{nil}, executor.get(canceled, []string{"unadmitted"}))
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, calls["unadmitted"])
}
