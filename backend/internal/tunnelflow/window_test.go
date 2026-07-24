package tunnelflow

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowAcquireGrantAccounting(t *testing.T) {
	w := NewWindow(4)
	assert.Equal(t, 4, w.Available())
	assert.Equal(t, 0, w.InUse())

	require.NoError(t, w.Acquire(context.Background()))
	assert.Equal(t, 3, w.Available())
	assert.Equal(t, 1, w.InUse())

	w.Grant(1)
	assert.Equal(t, 4, w.Available())
	assert.Equal(t, 0, w.InUse())
}

func TestWindowGrantAboveCapacityDiscardsSurplus(t *testing.T) {
	w := NewWindow(4)
	require.NoError(t, w.Acquire(context.Background()))
	require.NoError(t, w.Acquire(context.Background()))
	assert.Equal(t, 2, w.Available())

	w.Grant(100)
	assert.Equal(t, 4, w.Available(), "surplus beyond capacity is discarded")
}

func TestWindowGrantMaxUint64Terminates(t *testing.T) {
	const maxWindow = 64
	cases := []struct {
		name    string
		drain   int
		grant   uint64
		wantAvl int
	}{
		{"zero grant is a no-op", 10, 0, maxWindow - 10},
		{"in-budget grant adds exactly", 10, 4, maxWindow - 6},
		{"grant to exactly max", 10, 10, maxWindow},
		{"grant past max clamps", 10, 30, maxWindow},
		{"MaxUint64 clamps, never spins", 10, math.MaxUint64, maxWindow},
		{"grant in [2^63,2^64) clamps", 10, 1 << 63, maxWindow},
		{"MaxInt64 grant clamps", 10, math.MaxInt64, maxWindow},
		{"overflow from a full window clamps", 0, math.MaxUint64, maxWindow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWindow(maxWindow)
			for i := 0; i < tc.drain; i++ {
				require.NoError(t, w.Acquire(context.Background()), "seeded window must admit drain %d", i)
			}
			w.Grant(tc.grant)
			got := w.Available()
			assert.Equal(t, tc.wantAvl, got, "clamped credit")
			assert.GreaterOrEqual(t, got, 0, "credit must never go negative")
			assert.LessOrEqual(t, got, maxWindow, "credit must never exceed the window")
		})
	}
}

func TestWindowCloseWakesAcquire(t *testing.T) {
	w := NewWindow(1)
	require.NoError(t, w.Acquire(context.Background()))

	result := make(chan error, 1)
	go func() { result <- w.Acquire(context.Background()) }()
	select {
	case <-result:
		t.Fatal("acquire returned with no credit available")
	case <-time.After(50 * time.Millisecond):
	}

	w.Close()
	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrWindowClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("close did not release the parked acquire")
	}
}

func TestWindowCloseWakesReadyWaiter(t *testing.T) {
	w := NewWindow(1)
	<-w.Ready() // drain the sole token

	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-w.Ready():
			t.Error("Ready must not hand out a token after Close")
		case <-w.Closed():
		}
	}()

	w.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake a Ready waiter watching Closed")
	}
}

// Acquire on an already-closed, drained window must return ErrWindowClosed
// immediately rather than block: the worker read loop calls it in a hot loop and
// must exit promptly once the conn tears down, not park on a token that will
// never come.
func TestWindowAcquireOnClosedEmptyReturnsImmediately(t *testing.T) {
	w := NewWindow(1)
	require.NoError(t, w.Acquire(context.Background())) // drain the sole token
	w.Close()

	done := make(chan error, 1)
	go func() { done <- w.Acquire(context.Background()) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrWindowClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire on a closed, drained window blocked instead of returning")
	}
}

func TestWindowGrantWakesAcquire(t *testing.T) {
	w := NewWindow(1)
	require.NoError(t, w.Acquire(context.Background()))

	result := make(chan error, 1)
	go func() { result <- w.Acquire(context.Background()) }()
	w.Grant(1)
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("a grant did not wake the parked acquire")
	}
}

func TestWindowAvailableInUseAgree(t *testing.T) {
	w := NewWindow(8)
	for i := 0; i < 5; i++ {
		require.NoError(t, w.Acquire(context.Background()))
		assert.Equal(t, 8, w.Available()+w.InUse())
	}
	w.Grant(3)
	assert.Equal(t, 8, w.Available()+w.InUse())
	assert.Equal(t, 6, w.Available())
	assert.Equal(t, 2, w.InUse())
}

func TestWindowAcquireRespectsContext(t *testing.T) {
	w := NewWindow(1)
	require.NoError(t, w.Acquire(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := w.Acquire(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
