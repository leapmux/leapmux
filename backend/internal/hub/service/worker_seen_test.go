package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

type seenRecorderStore struct {
	store.Store
	update func(context.Context, string) error
}

func (s seenRecorderStore) Workers() store.WorkerStore {
	return seenRecorderWorkers{update: s.update}
}

type seenRecorderWorkers struct {
	store.WorkerStore
	update func(context.Context, string) error
}

func (w seenRecorderWorkers) UpdateLastSeen(ctx context.Context, workerID string) error {
	return w.update(ctx, workerID)
}

func newSeenRecorderService(t *testing.T, update func(context.Context, string) error) (*WorkerConnectorService, *quartz.Mock) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	return &WorkerConnectorService{store: seenRecorderStore{update: update}, clock: clock}, clock
}

func seenRecorderContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestWorkerSeenRecorderCoalescesPendingObservations(t *testing.T) {
	t.Parallel()
	ctx := seenRecorderContext(t)
	var writes atomic.Int32
	first := make(chan struct{})
	second := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWrite := func() { releaseOnce.Do(func() { close(release) }) }
	svc, _ := newSeenRecorderService(t, func(writeCtx context.Context, workerID string) error {
		assert.Equal(t, "worker-1", workerID)
		switch writes.Add(1) {
		case 1:
			close(first)
			select {
			case <-release:
			case <-writeCtx.Done():
				return writeCtx.Err()
			}
		case 2:
			close(second)
		}
		return nil
	})
	record, stop := svc.startWorkerSeenRecorder(ctx, "worker-1")
	t.Cleanup(stop)
	t.Cleanup(releaseWrite)
	record()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("the recorder did not start its first write")
	}
	for range 100 {
		record()
	}
	releaseWrite()
	select {
	case <-second:
	case <-ctx.Done():
		t.Fatal("the recorder discarded its pending observation")
	}
	stop()
	record()
	stop()
	assert.Equal(t, int32(2), writes.Load())
}

func TestWorkerSeenWriteEndsAtItsDeadline(t *testing.T) {
	t.Parallel()
	ctx := seenRecorderContext(t)
	entered := make(chan context.Context, 1)
	finished := make(chan error, 1)
	svc, clock := newSeenRecorderService(t, func(writeCtx context.Context, _ string) error {
		entered <- writeCtx
		<-writeCtx.Done()
		finished <- context.Cause(writeCtx)
		return writeCtx.Err()
	})
	record, stop := svc.startWorkerSeenRecorder(ctx, "worker-1")
	t.Cleanup(stop)
	timer := clock.Trap().AfterFunc(workerSeenWriteTimerTag)
	t.Cleanup(timer.Close)
	record()
	call := timer.MustWait(ctx)
	assert.Equal(t, 5*time.Second, call.Duration)
	call.MustRelease(ctx)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the recorder did not start its timed write")
	}
	clock.Advance(workerSeenWriteTimeout).MustWait(ctx)
	select {
	case cause := <-finished:
		assert.ErrorIs(t, cause, context.DeadlineExceeded)
	case <-ctx.Done():
		t.Fatal("the store write ignored its deadline")
	}
	stop()
	assert.NoError(t, ctx.Err(), "the write deadline must not cancel its connection")
}

func TestWorkerSeenRecorderJoinsItsWriteOnCancellation(t *testing.T) {
	t.Parallel()
	for _, cancelParent := range []bool{false, true} {
		name := "recorder stop"
		if cancelParent {
			name = "parent cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ctx := seenRecorderContext(t)
			parent, cancel := context.WithCancel(ctx)
			t.Cleanup(cancel)
			entered := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseWrite := func() { releaseOnce.Do(func() { close(release) }) }
			var exited atomic.Bool
			svc, _ := newSeenRecorderService(t, func(writeCtx context.Context, _ string) error {
				close(entered)
				<-writeCtx.Done()
				close(canceled)
				<-release
				exited.Store(true)
				return writeCtx.Err()
			})
			record, stop := svc.startWorkerSeenRecorder(parent, "worker-1")
			t.Cleanup(stop)
			t.Cleanup(releaseWrite)
			record()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("the recorder did not start its write")
			}
			if cancelParent {
				cancel()
			}
			joined := make(chan bool, 1)
			go func() { stop(); joined <- exited.Load() }()
			select {
			case <-canceled:
			case <-ctx.Done():
				t.Fatal("cancellation did not reach the store write")
			}
			releaseWrite()
			select {
			case finished := <-joined:
				require.True(t, finished, "stop returned before the store write finished")
			case <-ctx.Done():
				t.Fatal("stop did not join the store write")
			}
		})
	}
}

func TestWorkerSeenRecorderContinuesAfterAStoreFailure(t *testing.T) {
	t.Parallel()
	ctx := seenRecorderContext(t)
	writes := make(chan struct{}, 1)
	svc, _ := newSeenRecorderService(t, func(context.Context, string) error {
		writes <- struct{}{}
		return errors.New("store unavailable")
	})
	record, stop := svc.startWorkerSeenRecorder(ctx, "worker-1")
	t.Cleanup(stop)
	for range 2 {
		record()
		select {
		case <-writes:
		case <-ctx.Done():
			t.Fatal("a store failure stopped subsequent observations")
		}
	}
	stop()
	assert.NoError(t, ctx.Err())
}
