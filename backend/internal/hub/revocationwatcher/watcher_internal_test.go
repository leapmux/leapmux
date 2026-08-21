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
	_ = w.lease.mu.Lock(context.Background())
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
		_ = w.lease.mu.Lock(context.Background())
		_, _, err := w.consumePageLocked(context.Background(), 10)
		w.lease.mu.Unlock()
		done <- err
	}()

	<-rev.entered // ListPublishedAfter is now blocking mid-read.

	// With w.lease.mu released during the read, a heartbeat goroutine can acquire it.
	acquired := make(chan struct{})
	go func() {
		_ = w.lease.mu.Lock(context.Background())
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
	_ = w.lease.mu.Lock(context.Background())
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
	_ = w.lease.mu.Lock(context.Background())
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
	// reacquireErr is what the recovery attempt an expired lease triggers gets
	// back: nil for a lapse this process may repair, ErrHubAlreadyRunning for a
	// rival holder it must fence on.
	reacquireErr   error
	reacquireCalls int
}

func (*errPublishEvents) PublishPending(context.Context, int32) (int64, error) {
	return 0, errors.New("store unavailable")
}

func (s *errPublishEvents) ReacquireHubRuntimeLease(context.Context, store.ReacquireHubRuntimeLeaseParams) error {
	s.reacquireCalls++
	return s.reacquireErr
}

// MaxPublishedSeq answers the cursor verification that follows a successful
// reacquisition: nothing was published past seq 0, so the cursor is trivially
// replayable and the test stays focused on the store-error classification.
func (*errPublishEvents) MaxPublishedSeq(context.Context) (int64, error) { return 0, nil }

// A publish store error on a LAPSED lease is fatal only when the lease cannot be
// taken back. A rival holder means that Hub may have consumed and compacted past
// this cursor, so the watcher signals the server to fence.
func TestPublishErrorOnLeaseLostToRivalIsFatalAndSignaled(t *testing.T) {
	rev := &errPublishEvents{reacquireErr: store.ErrHubAlreadyRunning}
	w := &Watcher{
		store:         fakeRevStore{rev: rev},
		leaseDuration: time.Hour,
		holderID:      "holder",
		errors:        make(chan error, 1),
	}
	_ = w.lease.mu.Lock(context.Background())
	w.lease.leaseExpiresAt = time.Now().Add(-time.Second) // already lapsed
	err := w.publishPendingLocked(context.Background(), 100, 1000)
	w.lease.mu.Unlock()

	require.Error(t, err)
	assert.True(t, errorsIsLeaseFatal(err), "a lease held by a rival must be lease-fatal")
	assert.Len(t, w.errors, 1, "the fatal must be signaled to the server so it fences")
}

// The same lapse with no rival must NOT fence: recovery takes the lease back and
// the publish failure is reported as the ordinary transient store error it is.
// This is the suspend path -- a laptop that slept past its lease keeps serving.
func TestPublishErrorOnLapsedLeaseRecoversAndReportsStoreError(t *testing.T) {
	rev := &errPublishEvents{}
	w := &Watcher{
		store:         fakeRevStore{rev: rev},
		leaseDuration: time.Hour,
		holderID:      "holder",
		errors:        make(chan error, 1),
	}
	_ = w.lease.mu.Lock(context.Background())
	w.lease.leaseExpiresAt = time.Now().Add(-time.Second) // already lapsed
	err := w.publishPendingLocked(context.Background(), 100, 1000)
	remaining := w.leaseRemainingLocked()
	w.lease.mu.Unlock()

	require.Error(t, err)
	assert.False(t, errorsIsLeaseFatal(err), "a recovered lapse must not be lease-fatal")
	assert.Empty(t, w.errors, "a recovered lapse must not fence the server")
	assert.Equal(t, 1, rev.reacquireCalls, "the lapsed lease must be reacquired")
	assert.Greater(t, remaining, time.Duration(0), "recovery must leave a live lease behind")
	// A lapse that only the WALL clock can see -- the state a suspend actually
	// leaves -- has no test of its own: that time.Time cannot be built in Go (see
	// leaseRemainingLocked). TestWatcher_ReacquiresLeaseLapsedDuringSuspend
	// covers the same recovery from the database side.
}

// recoverEvents drives the recovery paths a lapsed lease reaches. Every field is
// what one of those paths needs the store to do, so one fake covers them instead
// of four near-identical ones.
type recoverEvents struct {
	store.RevocationEventStore
	reacquireErr    error
	reacquireDelay  time.Duration
	beforeReacquire func()
	maxSeqErr       error
	maxSeq          int64

	reacquireCalls int
	released       []string
}

func (*recoverEvents) PublishPending(context.Context, int32) (int64, error) { return 0, nil }

func (s *recoverEvents) ReacquireHubRuntimeLease(context.Context, store.ReacquireHubRuntimeLeaseParams) error {
	s.reacquireCalls++
	if s.beforeReacquire != nil {
		s.beforeReacquire()
	}
	time.Sleep(s.reacquireDelay)
	return s.reacquireErr
}

func (s *recoverEvents) MaxPublishedSeq(context.Context) (int64, error) {
	return s.maxSeq, s.maxSeqErr
}

func (s *recoverEvents) ReleaseHubRuntimeLease(_ context.Context, holderID string) (int64, error) {
	s.released = append(s.released, holderID)
	return 1, nil
}

func lapsedWatcher(rev store.RevocationEventStore, leaseDuration time.Duration) *Watcher {
	w := &Watcher{
		store:         fakeRevStore{rev: rev},
		leaseDuration: leaseDuration,
		holderID:      "holder",
		errors:        make(chan error, 1),
	}
	w.lease.seeded = true
	w.lease.leaseExpiresAt = time.Now().Add(-time.Second)
	return w
}

// A store that cannot answer the reacquisition must NOT fence the Hub. The
// lease merely lapsed, so the right response is to try again on the next tick --
// killing the process over a SQLITE_BUSY would reintroduce the outage this
// recovery path exists to remove.
func TestRecoverRetriesAfterATransientStoreFailure(t *testing.T) {
	rev := &recoverEvents{reacquireErr: errors.New("database is locked")}
	w := lapsedWatcher(rev, time.Hour)

	_ = w.lease.mu.Lock(context.Background())
	defer w.lease.mu.Unlock()

	err := w.renewLeaseIfStaleLocked(context.Background())
	require.Error(t, err)
	assert.False(t, errorsIsLeaseFatal(err), "a store blip during recovery must not be lease-fatal")
	assert.NotErrorIs(t, err, ErrLeaseLost,
		"the lapse cause must not be wrapped in, or callers stop the watcher for a store blip")
	assert.Empty(t, w.errors, "a retryable failure must not fence the server")

	// The lease is still lapsed, so the next attempt must try again rather than
	// latch. A recovery that gave up once would leave the Hub running without a
	// lease for the rest of the process.
	_ = w.renewLeaseIfStaleLocked(context.Background())
	assert.Equal(t, 2, rev.reacquireCalls, "recovery must retry while the lease is still lapsed")
}

// A reacquisition that outlasts the whole lease duration hands back a lease that
// may already have expired at the database. Trusting it would let the Hub write
// on a lease it does not hold, so this is one of the two genuinely fatal
// outcomes.
func TestRecoverRejectsALeaseThatOutlivedItsOwnBudget(t *testing.T) {
	const leaseDuration = 20 * time.Millisecond
	rev := &recoverEvents{reacquireDelay: 3 * leaseDuration}
	w := lapsedWatcher(rev, leaseDuration)

	_ = w.lease.mu.Lock(context.Background())
	defer w.lease.mu.Unlock()

	err := w.renewLeaseIfStaleLocked(context.Background())
	require.ErrorIs(t, err, ErrLeaseLost)
	assert.ErrorContains(t, err, "exceeded local lease budget")
	assert.Len(t, w.errors, 1, "a lease that cannot be trusted must fence the server")
}

// Close can set `closed` while a reacquisition is already in flight. The row
// that lands afterwards would outlive the Watcher and fence the next launch
// until its TTL, so recovery must remove what it just took.
func TestRecoverReleasesALeaseTakenWhileCloseWasRacing(t *testing.T) {
	rev := &recoverEvents{}
	w := lapsedWatcher(rev, time.Hour)
	// Close sets `closed` before it releases; model that landing mid-flight.
	rev.beforeReacquire = func() { w.closed.Store(true) }

	_ = w.lease.mu.Lock(context.Background())
	defer w.lease.mu.Unlock()

	err := w.renewLeaseIfStaleLocked(context.Background())
	require.ErrorIs(t, err, ErrClosed)
	assert.Equal(t, []string{"holder"}, rev.released,
		"a lease taken during a concurrent Close must be released, not orphaned for its TTL")
	assert.Empty(t, w.errors, "losing a race with Close is not a fatal lease loss")
}

// Recovery keeps the cursor, so the cursor must be proven replayable before any
// event is applied. When that proof cannot be READ, the watcher must hold the
// unverified flag and re-prove later -- not assume the cursor is fine and start
// applying events from it.
func TestRecoverHoldsTheCursorUnverifiedUntilItCanBeProven(t *testing.T) {
	rev := &recoverEvents{maxSeqErr: errors.New("store unavailable")}
	w := lapsedWatcher(rev, time.Hour)

	_ = w.lease.mu.Lock(context.Background())
	defer w.lease.mu.Unlock()

	err := w.renewLeaseIfStaleLocked(context.Background())
	require.Error(t, err)
	assert.False(t, errorsIsLeaseFatal(err), "an unreadable cursor proof is not a lost lease")
	assert.True(t, w.lease.cursorUnverified, "the cursor must stay unverified until it is proven")
	assert.Empty(t, w.errors)

	// Consumption stays blocked while the flag is set, so no event is applied
	// from a cursor that was never proven.
	require.Error(t, w.ensureLeaseLocked(context.Background()))

	// Once the store answers, the proof completes and the gate opens. maxSeq at
	// the cursor means nothing was published past it, so there is nothing to
	// replay and no gap to find.
	rev.maxSeqErr = nil
	require.NoError(t, w.ensureLeaseLocked(context.Background()))
	assert.False(t, w.lease.cursorUnverified, "a completed proof must clear the flag")
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

type blockingCloser struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingCloser) CloseChannelsBySession(string) int {
	close(c.started)
	<-c.release
	return 1
}

func (*blockingCloser) CloseChannelsByBearer(auth.BearerRef) int        { return 0 }
func (*blockingCloser) CloseChannelsByUserRevocation(string, int64) int { return 0 }
func (*blockingCloser) RestampSessionGeneration(string, int64)          {}

type singleSessionEventStore struct {
	store.RevocationEventStore
	listed bool
}

func (*singleSessionEventStore) PublishPending(context.Context, int32) (int64, error) {
	return 0, nil
}

func (s *singleSessionEventStore) ListPublishedAfter(context.Context, int64, int32) ([]store.PublishedRevocationEvent, error) {
	if s.listed {
		return nil, nil
	}
	s.listed = true
	return []store.PublishedRevocationEvent{{
		Seq: 1,
		Event: store.RevocationEvent{
			ID: "event", Kind: store.RevocationEventKindSession, SubjectID: "session",
		},
	}}, nil
}

func (*singleSessionEventStore) RenewHubRuntimeLease(context.Context, store.RenewHubRuntimeLeaseParams) (bool, error) {
	return true, nil
}

func (*singleSessionEventStore) ReleaseHubRuntimeLease(context.Context, string) (int64, error) {
	return 1, nil
}

// Close releases the runtime lease WITHOUT waiting for an in-flight RunOnce
// event effect to finish. applyEvent runs in a lock-free window
// (applyEventUnlocked releases lease.mu) and can block for seconds on a
// back-pressured frontend; the lease release acquires only lease.mu (which that
// window frees), so Close can return while the effect is still running. This is
// safe because the effect is in-process and idempotent -- it mutates only the
// closing Hub's own auth/channel state, never the durable lease row or the
// shared DB, so it cannot corrupt a Hub that has since acquired the released
// lease. In production the owned loop is still drained via stopLoop before the
// store closes; only a directly-invoked RunOnce (no production caller) is left
// to unwind on its own. renewLocked is gated on `closed`, so the unwind cannot
// re-acquire the lease; it observes `closed`/`!seeded` and returns ErrClosed.
func TestCloseReleasesLeaseWithoutWaitingForInFlightApply(t *testing.T) {
	closer := &blockingCloser{started: make(chan struct{}), release: make(chan struct{})}
	w := &Watcher{
		store:           fakeRevStore{rev: &singleSessionEventStore{}},
		lifecycle:       auth.NewCredentialLifecycleEffects(nil, closer, nil),
		leaseDuration:   time.Hour,
		pageSize:        10,
		maxEventsPerRun: 10,
		holderID:        "holder",
	}
	w.lease.seeded = true
	w.lease.leaseExpiresAt = time.Now().Add(time.Hour)

	runDone := make(chan error, 1)
	go func() { runDone <- w.RunOnce(context.Background()) }()
	<-closer.started

	// Close returns promptly: it does not block on the in-flight, uncancellable
	// applyEvent (which holds only runMu, not lease.mu).
	closeDone := make(chan error, 1)
	go func() { closeDone <- w.Close(context.Background()) }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close waited for the in-flight applyEvent instead of releasing the lease")
	}

	// Let the in-flight effect finish; RunOnce then observes `closed`/`!seeded`
	// and unwinds without re-acquiring the lease.
	close(closer.release)
	require.ErrorIs(t, <-runDone, ErrClosed)
}

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
	_ = w.lease.mu.Lock(context.Background())

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
	_ = w.lease.mu.Lock(context.Background())

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

	_ = w.lease.mu.Lock(context.Background()) // simulate runOnce holding the lock across a store call
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

// elapsedDeadlineCtx models the race window leaseReleaseContext must survive:
// a context whose Deadline() has already passed on the wall clock while Err()
// still reads nil (the deadline elapsing between the two reads). Feeding the
// negative remainder into the timeout min would hand back an already-expired
// context -- the stillborn release the function's doc promises cannot happen.
type elapsedDeadlineCtx struct{ context.Context }

func (elapsedDeadlineCtx) Deadline() (time.Time, bool) { return time.Now().Add(-time.Second), true }
func (elapsedDeadlineCtx) Err() error                  { return nil }

func TestLeaseReleaseContextSurvivesDeadlineElapsingMidCheck(t *testing.T) {
	ctx, cancel := leaseReleaseContext(elapsedDeadlineCtx{context.Background()})
	defer cancel()

	require.NoError(t, ctx.Err(), "the release context must not be born expired")
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	remaining := time.Until(deadline)
	assert.Greater(t, remaining, leaseReleaseTimeout/2,
		"an elapsed caller deadline is excluded from the cap; the release gets the full budget")
}
