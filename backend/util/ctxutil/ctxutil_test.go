package ctxutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithLinkedCancelCancelsWhenOtherEnds(t *testing.T) {
	other, cancelOther := context.WithCancel(context.Background())
	ctx, cancel := WithLinkedCancel(context.Background(), other)
	defer cancel()

	require.NoError(t, ctx.Err(), "derived context is live while both parents are")
	cancelOther()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("derived context was not cancelled when other ended")
	}
}

func TestWithLinkedCancelCancelsWhenParentEnds(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	other := context.Background()
	ctx, cancel := WithLinkedCancel(parent, other)
	defer cancel()

	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("derived context was not cancelled when parent ended")
	}
}

func TestWithLinkedCancelEagerlyCancelsForAlreadyDoneOther(t *testing.T) {
	other, cancelOther := context.WithCancel(context.Background())
	cancelOther() // already done before linking
	ctx, cancel := WithLinkedCancel(context.Background(), other)
	defer cancel()

	assert.Error(t, ctx.Err(), "an already-cancelled other must cancel the derived context immediately")
}

func TestWithLinkedCancelNilOtherIsPlainCancel(t *testing.T) {
	ctx, cancel := WithLinkedCancel(context.Background(), nil)
	require.NoError(t, ctx.Err())
	cancel()
	assert.Error(t, ctx.Err(), "cancel still cancels the derived context when other is nil")
}

func TestWithLinkedCancelNilParentDefaultsToBackground(t *testing.T) {
	var nilCtx context.Context // deliberately nil to exercise the parent==nil guard
	require.NotPanics(t, func() {
		ctx, cancel := WithLinkedCancel(nilCtx, nilCtx) //nolint:staticcheck // testing the nil-parent default
		defer cancel()
		assert.NoError(t, ctx.Err())
	})
}

// The returned cancel must detach the AfterFunc link so a short-lived operation
// context does not retain the (long-lived) other context after it completes.
func TestWithLinkedCancelStopDetachesFromOther(t *testing.T) {
	other, cancelOther := context.WithCancel(context.Background())
	defer cancelOther()
	ctx, cancel := WithLinkedCancel(context.Background(), other)
	cancel() // operation done: detach + cancel

	// Cancelling other afterwards must not panic or otherwise misbehave; ctx is
	// already cancelled by our own cancel().
	cancelOther()
	assert.Error(t, ctx.Err())
}

// The zero value must be a usable unlocked mutex (matching sync.Mutex), so a
// struct embedding one needs no constructor to be safe.
func TestMutexZeroValueIsUsable(t *testing.T) {
	var mu Mutex
	require.NoError(t, mu.Lock(context.Background()), "an uncontended zero-value Lock succeeds")
	mu.Unlock()
	assert.True(t, mu.TryLock(), "the mutex is free again after Unlock")
	mu.Unlock()
}

// An uncontended Lock must succeed even under an already-cancelled context: the
// caller is not waiting on anything, so there is nothing for ctx to abort.
func TestMutexLockSucceedsUncontendedWithCancelledContext(t *testing.T) {
	var mu Mutex
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, mu.Lock(ctx), "a free mutex is acquired without consulting ctx")
	mu.Unlock()
}

// A contended Lock must give up with ctx's error when ctx ends -- the whole
// point of the type, and what bounds sendRemoteClose's wait for writeMu.
func TestMutexLockReturnsContextErrorWhenContended(t *testing.T) {
	var mu Mutex
	require.NoError(t, mu.Lock(context.Background()))
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := mu.Lock(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded, "a contended Lock unwinds with ctx's error")
}

// A Lock that gave up must NOT be holding the mutex: the goroutine-trampoline
// idiom this replaces left an abandoned waiter to acquire the lock after its
// caller was gone, handing it to nobody. Assert the lock is genuinely free once
// the original holder releases it.
func TestMutexCancelledWaiterDoesNotStealLock(t *testing.T) {
	var mu Mutex
	require.NoError(t, mu.Lock(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.Error(t, mu.Lock(ctx), "the waiter gives up")

	mu.Unlock() // the original holder releases

	// Poll rather than TryLock once: a stranded waiter would race to grab the
	// slot the Unlock just freed, and this must fail if one ever does.
	acquired := make(chan struct{})
	go func() {
		_ = mu.Lock(context.Background())
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("a cancelled waiter stole the lock: a later Lock never acquired it")
	}
	mu.Unlock()
}

// Unlock of an unlocked mutex is a caller bug and must die loudly rather than
// silently corrupting the semaphore's accounting.
func TestMutexUnlockOfUnlockedPanics(t *testing.T) {
	var mu Mutex
	assert.PanicsWithValue(t, "ctxutil: unlock of unlocked Mutex", func() { mu.Unlock() })
}

// Lock must serialize: a second acquire cannot proceed while the first holds it.
func TestMutexExcludesConcurrentHolders(t *testing.T) {
	var mu Mutex
	require.NoError(t, mu.Lock(context.Background()))

	entered := make(chan struct{})
	go func() {
		_ = mu.Lock(context.Background())
		close(entered)
	}()
	select {
	case <-entered:
		t.Fatal("a second Lock acquired the mutex while it was held")
	case <-time.After(50 * time.Millisecond):
	}
	mu.Unlock()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("the waiting Lock did not acquire the mutex after Unlock")
	}
	mu.Unlock()
}
