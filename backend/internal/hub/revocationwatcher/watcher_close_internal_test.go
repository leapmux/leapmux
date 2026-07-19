package revocationwatcher

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// releaseRecordingEvents records ReleaseHubRuntimeLease calls. It embeds the
// store interface (nil) so any other method call panics -- surfacing an
// accidental dependency rather than hiding it, matching the other fakes here.
type releaseRecordingEvents struct {
	store.RevocationEventStore
	mu              sync.Mutex
	released        []string
	releaseCtxErr   error
	releaseDeadline time.Time
}

func (s *releaseRecordingEvents) ReleaseHubRuntimeLease(ctx context.Context, holderID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, holderID)
	s.releaseCtxErr = ctx.Err()
	s.releaseDeadline, _ = ctx.Deadline()
	return 1, nil
}

func (s *releaseRecordingEvents) snapshot() (released []string, deadline time.Time, ctxErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.released...), s.releaseDeadline, s.releaseCtxErr
}

// Close must release the Hub runtime lease even when the loop drain overruns
// the caller's shutdown budget. Previously Close returned ctx.Err() on a drain
// timeout and skipped the release, orphaning the lease row and fencing the next
// Hub launch until its TTL. Here loopDone stays open (the loop never exits) and
// ctx is already cancelled, forcing the drain-timeout path; the release must
// still fire while Close reports that its owned loop did not drain.
func TestCloseReleasesLeaseEvenWhenDrainTimesOut(t *testing.T) {
	rev := &releaseRecordingEvents{}
	w := &Watcher{
		store:    fakeRevStore{rev: rev},
		holderID: "holder",
		loopDone: make(chan struct{}), // open: the processing/renewal goroutines never exit
	}
	w.lease.seeded = true

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate an already-exhausted shutdown budget

	err := w.Close(ctx)
	require.ErrorIs(t, err, context.Canceled,
		"Close must report that its owned loop did not drain even after releasing the lease")
	released, releaseDeadline, releaseCtxErr := rev.snapshot()
	require.Equal(t, []string{"holder"}, released,
		"Close must release the lease despite the drain timeout")
	require.NoError(t, releaseCtxErr,
		"lease release must not inherit the exhausted caller context")
	require.WithinDuration(t, time.Now().Add(leaseReleaseTimeout), releaseDeadline, time.Second,
		"lease release must use its own bounded timeout")

	// And the seeded flag is cleared so a subsequent Close is a no-op.
	_ = w.lease.mu.Lock(context.Background())
	seeded := w.lease.seeded
	w.lease.mu.Unlock()
	require.False(t, seeded, "Close must clear the seeded flag after releasing")
}

// The lease-release phase of Close must remain a no-op for an unseeded watcher,
// even when the loop drain times out -- there is no lease row to release.
func TestCloseUnseededSkipsReleaseEvenOnDrainTimeout(t *testing.T) {
	rev := &releaseRecordingEvents{}
	w := &Watcher{
		store:    fakeRevStore{rev: rev},
		holderID: "holder",
		loopDone: make(chan struct{}), // open
	}
	// w.lease.seeded stays false.

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, w.Close(ctx), context.Canceled)
	released, _, _ := rev.snapshot()
	require.Empty(t, released, "unseeded Close must not call ReleaseHubRuntimeLease")
}

func TestCloseBoundsLeaseLockAcquisitionByCallerDeadline(t *testing.T) {
	rev := &releaseRecordingEvents{}
	w := &Watcher{store: fakeRevStore{rev: rev}, holderID: "holder"}
	w.lease.seeded = true
	_ = w.lease.mu.Lock(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Close(ctx) }()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		w.lease.mu.Unlock()
		t.Fatal("Close ignored the caller deadline while acquiring the lease lock")
	}
	w.lease.mu.Unlock()
}

// Close must release the Hub runtime lease even when an in-flight sweep holds
// runMu for longer than the shutdown budget. runOnce holds runMu across
// applyEventUnlocked -> applyEvent, whose channel teardown can block for
// seconds on a back-pressured frontend and is not cancellable via the watcher's
// contexts. releaseSeededLease therefore acquires ONLY the lease-state lock
// (which applyEventUnlocked releases during each event) and must not wait on
// runMu; a prior version waited on runMu, timed out, and orphaned the lease
// for its 30s TTL. renewLocked is gated on `closed` so the stuck sweep cannot
// re-acquire the lease after the release.
func TestCloseReleasesLeaseEvenWhenSweepHoldsRunMu(t *testing.T) {
	rev := &releaseRecordingEvents{}
	w := &Watcher{
		store:    fakeRevStore{rev: rev},
		holderID: "holder",
		loopDone: make(chan struct{}), // open: the loop goroutines never exit -> drain times out
	}
	w.lease.seeded = true
	// Simulate a sweep stuck in an uncancellable applyEvent: runOnce holds runMu
	// across applyEventUnlocked and will not release it within any budget.
	_ = w.runMu.Lock(context.Background())
	t.Cleanup(w.runMu.Unlock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-exhausted shutdown budget

	err := w.Close(ctx)
	require.ErrorIs(t, err, context.Canceled,
		"Close must report the loop drain timeout even after releasing the lease")
	released, _, releaseCtxErr := rev.snapshot()
	require.Equal(t, []string{"holder"}, released,
		"Close must release the lease even while a sweep holds runMu")
	require.NoError(t, releaseCtxErr,
		"lease release must not inherit the exhausted caller context")
}
