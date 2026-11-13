package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/util/ctxutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminMembershipWaitCancellationDoesNotRunAnotherTransaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
	t.Cleanup(cancel)
	var mutex ctxutil.Mutex
	locked, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	holder := make(chan error, 1)
	go func() {
		holder <- RunAdminMembershipTransaction(ctx, &mutex, func(held bool) error {
			if err := RequireAdminMembershipLock(held); err != nil {
				return err
			}
			close(locked)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	t.Cleanup(func() {
		releaseOnce()
		require.NoError(t, <-holder)
	})
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	requested := make(chan struct{})
	result := make(chan error, 1)
	var began atomic.Bool
	go func() {
		result <- RunAdminMembershipTransaction(waitCtx, &mutex, func(held bool) error {
			if !held {
				close(requested)
				return RequireAdminMembershipLock(false)
			}
			began.Store(true)
			return nil
		})
	}()
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelWait()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assert.False(t, began.Load(), "a cancelled waiter must not begin a transaction under the mutex")
}
