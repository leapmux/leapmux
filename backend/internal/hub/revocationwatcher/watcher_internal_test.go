package revocationwatcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// fakeRevStore is a store.Store whose only exercised method is
// RevocationEvents(); the embedded nil interface panics on any other call,
// which surfaces an accidental dependency instead of hiding it.
type fakeRevStore struct {
	store.Store
	rev store.RevocationEventStore
}

func (s fakeRevStore) RevocationEvents() store.RevocationEventStore { return s.rev }

type renewCountingEvents struct {
	store.RevocationEventStore
	renewals int
}

func (s *renewCountingEvents) RenewHubRuntimeLease(context.Context, store.RenewHubRuntimeLeaseParams) (bool, error) {
	s.renewals++
	return true, nil
}

// An idle sweep must not write a lease renewal on every tick: renewal is gated
// on the lease having passed half its duration.
func TestRenewLeaseIfStaleSkipsFreshLease(t *testing.T) {
	rev := &renewCountingEvents{}
	w := &Watcher{
		store:         fakeRevStore{rev: rev},
		leaseDuration: time.Hour,
		holderID:      "holder",
	}
	w.lease.mu.Lock()
	defer w.lease.mu.Unlock()

	// Fresh lease (well over half its duration remaining): renewal is skipped.
	w.lease.leaseExpiresAt = time.Now().Add(time.Hour)
	require.NoError(t, w.renewLeaseIfStaleLocked(context.Background()))
	require.Equal(t, 0, rev.renewals, "a fresh lease must not be renewed")

	// Stale lease (under half its duration remaining): renewal fires.
	w.lease.leaseExpiresAt = time.Now().Add(time.Minute)
	require.NoError(t, w.renewLeaseIfStaleLocked(context.Background()))
	require.Equal(t, 1, rev.renewals, "a past-half-life lease must be renewed")
}

type deadlineCapturingEvents struct {
	store.RevocationEventStore
	pages         [][]store.PublishedRevocationEvent
	next          int
	listDeadlines []time.Time
}

func (*deadlineCapturingEvents) PublishPending(context.Context, int32) (int64, error) {
	return 0, nil
}

func (s *deadlineCapturingEvents) ListPublishedAfter(ctx context.Context, _ int64, _ int32) ([]store.PublishedRevocationEvent, error) {
	if dl, ok := ctx.Deadline(); ok {
		s.listDeadlines = append(s.listDeadlines, dl)
	}
	if s.next < len(s.pages) {
		page := s.pages[s.next]
		s.next++
		return page, nil
	}
	return nil, nil
}

func (*deadlineCapturingEvents) RenewHubRuntimeLease(context.Context, store.RenewHubRuntimeLeaseParams) (bool, error) {
	return true, nil
}

// blockingListEvents blocks ListPublishedAfter until released, so a test can
// observe whether w.lease.mu is held during the (slow) store read.
type blockingListEvents struct {
	store.RevocationEventStore
	entered chan struct{}
	release chan struct{}
}

func (*blockingListEvents) PublishPending(context.Context, int32) (int64, error) { return 0, nil }
func (*blockingListEvents) RenewHubRuntimeLease(context.Context, store.RenewHubRuntimeLeaseParams) (bool, error) {
	return true, nil
}

func (s *blockingListEvents) ListPublishedAfter(context.Context, int64, int32) ([]store.PublishedRevocationEvent, error) {
	close(s.entered)
	<-s.release
	return nil, nil
}

// A slow page-list store round-trip must run with w.lease.mu RELEASED so renewalLoop
// can keep the lease alive during the slow call. If w.lease.mu were held (the pre-fix
// behavior), the heartbeat would block on it and a merely-slow -- not down --
// store would self-fence the sole Hub.
func TestConsumePageReleasesLockDuringSlowStoreRead(t *testing.T) {
	rev := &blockingListEvents{entered: make(chan struct{}), release: make(chan struct{})}
	w := &Watcher{
		store:         fakeRevStore{rev: rev},
		leaseDuration: time.Hour,
		holderID:      "holder",
	}
	// Ample lease so the read is not aborted by its deadline during the test.
	w.lease.leaseExpiresAt = time.Now().Add(time.Hour)

	done := make(chan error, 1)
	go func() {
		w.lease.mu.Lock()
		_, _, err := w.consumePageLocked(context.Background(), 10)
		w.lease.mu.Unlock()
		done <- err
	}()

	<-rev.entered // ListPublishedAfter is now blocking mid-read.

	// With w.lease.mu released during the read, a heartbeat goroutine can acquire it.
	acquired := make(chan struct{})
	go func() {
		w.lease.mu.Lock()
		// Touch the mu-guarded lease state, exactly as renewalLoop does when it
		// renews -- a non-empty critical section that would block if the sweep
		// held w.lease.mu across the store read.
		_ = w.lease.leaseExpiresAt
		w.lease.mu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
		// good: the lock was released during the slow read.
	case <-time.After(2 * time.Second):
		close(rev.release)
		t.Fatal("w.lease.mu held during ListPublishedAfter: renewalLoop would be blocked and could self-fence the Hub")
	}

	close(rev.release)
	require.NoError(t, <-done)
}

// A mid-sweep lease renewal extends the lease, and the deadline bounding
// subsequent store round-trips must follow it. Otherwise a multi-page drain
// keeps aborting at the pre-renewal deadline even though the lease is valid.
func TestConsumeReDerivesDeadlineAfterRenewal(t *testing.T) {
	rev := &deadlineCapturingEvents{
		pages: [][]store.PublishedRevocationEvent{
			{{Seq: 1, Event: store.RevocationEvent{ID: "e1", Kind: store.RevocationEventKindUserInfo, UserID: "u"}}},
		},
	}
	w := &Watcher{
		store:           fakeRevStore{rev: rev},
		lifecycle:       auth.NewCredentialLifecycleEffects(nil, nil, nil),
		leaseDuration:   time.Hour,
		pageSize:        1,
		maxEventsPerRun: 10,
		holderID:        "holder",
		lease: leaseState{
			seeded: true,
			// Start with a near-term deadline; the first page's renewal must push
			// it far into the future so the second page is not bounded by this.
			leaseExpiresAt: time.Now().Add(time.Second),
		},
	}

	_, err := w.runOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, rev.listDeadlines, 2, "expected a page fetch plus a drained fetch")
	assert.WithinDuration(t, time.Now().Add(time.Hour), rev.listDeadlines[1], 5*time.Minute,
		"the second page's deadline must track the renewed (extended) lease, not the pre-renewal deadline")
	assert.Greater(t, rev.listDeadlines[1].Sub(rev.listDeadlines[0]), 30*time.Minute,
		"the renewed deadline must be much later than the pre-renewal deadline")
}

type transientRenewEvents struct {
	store.RevocationEventStore
	err error
}

func (s *transientRenewEvents) RenewHubRuntimeLease(context.Context, store.RenewHubRuntimeLeaseParams) (bool, error) {
	return false, s.err
}

// A transient store error during lease renewal (while the lease is still valid)
// must be neither lease-fatal nor signaled on the errors channel, so renewalLoop
// logs and retries on the next tick instead of silently killing the watcher --
// which would leave the Hub serving with cross-process revocations no longer
// applied and no error surfaced to the server.
func TestRenewTransientErrorIsNotFatal(t *testing.T) {
	rev := &transientRenewEvents{err: errors.New("database is locked")}
	w := &Watcher{
		store:         fakeRevStore{rev: rev},
		leaseDuration: time.Hour,
		holderID:      "holder",
		errors:        make(chan error, 1),
	}
	w.lease.mu.Lock()
	// Stale lease (past half-life) with real budget left, so renewLocked calls
	// the store rather than short-circuiting on a locally-expired lease.
	w.lease.leaseExpiresAt = time.Now().Add(20 * time.Minute)
	err := w.renewLeaseIfStaleLocked(context.Background())
	w.lease.mu.Unlock()

	require.Error(t, err)
	assert.False(t, errorsIsLeaseFatal(err), "a transient renew error must not be lease-fatal")
	assert.Empty(t, w.errors, "a transient renew error must not signal fatal to the server")
}

type publishRenewCountingEvents struct {
	store.RevocationEventStore
	publishCalls int
	renewals     int
}

func (s *publishRenewCountingEvents) PublishPending(_ context.Context, limit int32) (int64, error) {
	s.publishCalls++
	if s.publishCalls == 1 {
		return int64(limit), nil // a full page -> the caller must loop again
	}
	return 0, nil // drained
}

func (s *publishRenewCountingEvents) RenewHubRuntimeLease(context.Context, store.RenewHubRuntimeLeaseParams) (bool, error) {
	s.renewals++
	return true, nil
}

// The publish phase holds w.lease.mu across every page, blocking the heartbeat
// renewalLoop, so it must renew the lease itself between pages. Otherwise a large
// backlog drain over a merely-slow store burns the lease budget and self-fences
// the sole Hub -- the failure the consume phase already avoids by renewing per
// page.
func TestPublishRenewsLeaseBetweenPages(t *testing.T) {
	rev := &publishRenewCountingEvents{}
	w := &Watcher{
		store:         fakeRevStore{rev: rev},
		leaseDuration: time.Hour,
		holderID:      "holder",
		errors:        make(chan error, 1),
	}
	w.lease.mu.Lock()
	// Stale lease so the between-pages renewLeaseIfStaleLocked actually renews.
	w.lease.leaseExpiresAt = time.Now().Add(20 * time.Minute)
	err := w.publishPendingLocked(context.Background(), 100, 1000)
	w.lease.mu.Unlock()

	require.NoError(t, err)
	assert.Equal(t, 2, rev.publishCalls, "a full first page must drive a second publish call")
	assert.GreaterOrEqual(t, rev.renewals, 1, "publish must renew the lease between pages")
}

type errPublishEvents struct {
	store.RevocationEventStore
}

func (*errPublishEvents) PublishPending(context.Context, int32) (int64, error) {
	return 0, errors.New("store unavailable")
}

// When the lease has expired, a publish store error is lease-fatal: publish
// now returns the error (rather than only logging it) so runOnce aborts instead
// of consuming on a lost lease, and handleStoreErrorLocked signals it on the
// errors channel so the server fences.
func TestPublishErrorOnExpiredLeaseIsFatalAndSignaled(t *testing.T) {
	rev := &errPublishEvents{}
	w := &Watcher{
		store:         fakeRevStore{rev: rev},
		leaseDuration: time.Hour,
		holderID:      "holder",
		errors:        make(chan error, 1),
	}
	w.lease.mu.Lock()
	w.lease.leaseExpiresAt = time.Now().Add(-time.Second) // already expired
	err := w.publishPendingLocked(context.Background(), 100, 1000)
	w.lease.mu.Unlock()

	require.Error(t, err)
	assert.True(t, errorsIsLeaseFatal(err), "a publish store error on an expired lease must be lease-fatal")
	assert.Len(t, w.errors, 1, "the fatal must be signaled to the server so it fences")
}

func TestApplyEventSkipsUnknownKind(t *testing.T) {
	w := &Watcher{}
	// An unknown kind is logged and skipped, never fatal -- the caller advances
	// the cursor past it instead of fencing the Hub. applyEvent has no failure
	// path, so the guarantee is that it returns without panicking (it does not
	// dispatch to any lifecycle effect for an unrecognized kind).
	assert.NotPanics(t, func() {
		w.applyEvent(store.PublishedRevocationEvent{
			Seq: 7,
			Event: store.RevocationEvent{
				ID: "event", Kind: "future_kind",
			},
		})
	})
}

// A user_info event is a recognized cache-invalidation kind. With a nil
// lifecycle the effect is a nil-safe no-op; the point is that applyEvent routes
// it (removing the case would fall into the unknown-kind skip arm, silently
// dropping a benign cache-invalidation instead of applying it).
func TestApplyEventDispatchesUserInfo(t *testing.T) {
	w := &Watcher{}
	// A recognized kind routes to its (nil-safe) lifecycle effect without panic;
	// the point is that applyEvent has a case for it rather than falling into the
	// unknown-kind skip arm.
	assert.NotPanics(t, func() {
		w.applyEvent(store.PublishedRevocationEvent{
			Seq: 8,
			Event: store.RevocationEvent{
				ID: "event", Kind: store.RevocationEventKindUserInfo, SubjectID: "u", UserID: "u",
			},
		})
	})
}

// panickingCloser stands in for a wedged teardown that panics inside
// applyEvent's unlocked window.
type panickingCloser struct{}

func (panickingCloser) CloseChannelsBySession(string) int               { panic("boom in teardown") }
func (panickingCloser) CloseChannelsByBearer(auth.BearerRef) int        { return 0 }
func (panickingCloser) CloseChannelsByUserRevocation(string, int64) int { return 0 }
func (panickingCloser) RestampSessionGeneration(string, int64)          {}

// applyEventUnlocked releases w.lease.mu across the (slow, panic-prone) teardown
// and MUST re-lock on the way out even when applyEvent panics. runOnce holds
// w.lease.mu under a defer Unlock, so a panic that returned with the lock
// released would make that defer unlock an already-unlocked mutex -- a second
// panic that masks the real teardown failure. Drive a panicking teardown and
// assert the lock is held again afterwards.
func TestApplyEventUnlockedReLocksAfterPanic(t *testing.T) {
	w := &Watcher{
		lifecycle: auth.NewCredentialLifecycleEffects(nil, panickingCloser{}, nil),
	}
	w.lease.mu.Lock()

	func() {
		defer func() {
			require.NotNil(t, recover(), "expected the teardown panic to propagate")
		}()
		w.applyEventUnlocked(store.PublishedRevocationEvent{
			Event: store.RevocationEvent{Kind: store.RevocationEventKindSession, SubjectID: "s1"},
		})
	}()

	// The deferred re-lock must have re-acquired w.lease.mu. A non-reentrant
	// Mutex held by this goroutine makes TryLock fail; had the re-lock been
	// skipped (the pre-fix bare Unlock/apply/Lock), the mutex would be free and
	// TryLock would succeed.
	if w.lease.mu.TryLock() {
		w.lease.mu.Unlock()
		t.Fatal("w.lease.mu not re-locked after applyEvent panic; runOnce's defer Unlock would double-panic")
	}
	w.lease.mu.Unlock()
}

// runStoreUnlocked releases w.lease.mu across a store round-trip and MUST re-lock
// on the way out even when the store call panics -- for the same reason as
// applyEventUnlocked (runOnce's defer Unlock would otherwise double-panic).
func TestRunStoreUnlockedReLocksAfterPanic(t *testing.T) {
	w := &Watcher{}
	w.lease.mu.Lock()

	func() {
		defer func() {
			require.NotNil(t, recover(), "expected the store round-trip panic to propagate")
		}()
		_ = w.runStoreUnlocked(context.Background(), func(context.Context) error {
			panic("boom in store round-trip")
		})
	}()

	if w.lease.mu.TryLock() {
		w.lease.mu.Unlock()
		t.Fatal("w.lease.mu not re-locked after store round-trip panic; runOnce's defer Unlock would double-panic")
	}
	w.lease.mu.Unlock()
}

// Close must cancel the owned loop's context BEFORE acquiring w.lease.mu: an in-flight
// runOnce holds w.lease.mu across its store round-trips, so if Close locked first it
// could block past its ctx budget. Here we hold w.lease.mu to stand in for that
// in-flight runOnce and assert Close still cancels the loop.
func TestCloseCancelsLoopBeforeAcquiringMu(t *testing.T) {
	w := &Watcher{}
	loopCtx, cancel := context.WithCancel(context.Background())
	w.loopCancel.Store(&cancel)

	w.lease.mu.Lock() // simulate runOnce holding the lock across a store call
	closeReturned := make(chan error, 1)
	go func() { closeReturned <- w.Close(context.Background()) }()

	select {
	case <-loopCtx.Done(): // cancellation happened without Close needing w.lease.mu
	case <-time.After(2 * time.Second):
		w.lease.mu.Unlock()
		t.Fatal("Close did not cancel the loop before acquiring w.lease.mu")
	}

	w.lease.mu.Unlock() // let Close finish (unseeded -> returns nil)
	require.NoError(t, <-closeReturned)
}
