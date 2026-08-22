// Package revocationwatcher consumes the durable credential lifecycle stream
// and drives matching cache eviction and revocation teardown.
//
// Admin tools and other hub processes can only mutate the database. Every
// credential mutation therefore writes a durable pending event in the same
// transaction as the row change. This watcher publishes pending events into
// a gapless seq stream, then consumes published events by seq. The cursor is
// not a timestamp, so late commits and same-clock ties cannot be skipped.
//
// In-process callers still drive direct close paths for zero-latency local
// teardown. Watcher delivery is the cross-process, idempotent safety net.
package revocationwatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/util/ctxutil"
	"github.com/leapmux/leapmux/util/errwrap"
)

// DefaultInterval is how often the watcher publishes and drains the DB
// revocation stream.
const DefaultInterval = 2 * time.Second

// DefaultLeaseDuration is deliberately much longer than the normal sweep
// interval so transient store failures can recover without fencing the Hub.
const DefaultLeaseDuration = 30 * time.Second

var (
	ErrLeaseLost = errors.New("revocation watcher lease lost")
	ErrNotSeeded = errors.New("revocation watcher is not seeded")
	ErrClosed    = errors.New("revocation watcher is closed")
)

const (
	DefaultPageSize        int32 = 1000
	DefaultMaxEventsPerRun int32 = 10000
	saturatedRetryDelay          = 10 * time.Millisecond
)

// Watcher is constructed once at hub bootstrap and started via StartLoop.
type Watcher struct {
	// Immutable configuration: set at construction and never mutated, so it
	// needs no lock.
	store           store.Store
	lifecycle       *auth.CredentialLifecycleEffects
	interval        time.Duration
	pageSize        int32
	maxEventsPerRun int32
	// operationsCtx owns every seed and sweep operation, including public
	// RunOnce calls whose caller context may otherwise outlive this Watcher.
	// Close cancels it before waiting for the active operation to drain, which
	// prevents a store mutation from continuing after Watcher teardown.
	operationsCtx    context.Context
	cancelOperations context.CancelFunc

	// lease owns the Hub runtime lease, the stream cursor, and the recover /
	// prove protocol. Watcher publishes and consumes against it.
	lease runtimeLease
	// runMu serializes complete sweeps and lets Close wait through the periods
	// where a sweep deliberately drops lease.mu for store I/O or event effects.
	runMu ctxutil.Mutex

	// Loop lifecycle. lifecycleMu guards the once-only start handshake (started +
	// loopDone); the lease lock and this one are never held nested, so there is
	// no ordering hazard between them.
	lifecycleMu sync.Mutex
	started     bool
	loopDone    chan struct{}

	// errors is created once in New and only ever read / sent-on (never
	// reassigned), so it needs no lock.
	errors chan error
	// loopCancel cancels the owned loop's context. It is held atomically (not
	// under any lock) so Close can cancel an in-flight runOnce -- which holds
	// w.lease.mu across its store round-trips -- without first blocking on a lock.
	loopCancel atomic.Pointer[context.CancelFunc]
	closed     atomic.Bool
}

// Option configures a Watcher before it starts. Options validate their input;
// runtime mutation is intentionally unsupported because the loop snapshots
// some timings while reading other limits on each pass.
type Option func(*Watcher)

func WithInterval(interval time.Duration) Option {
	if interval <= 0 {
		panic("revocation watcher interval must be positive")
	}
	return func(w *Watcher) { w.interval = interval }
}

func WithLeaseDuration(duration time.Duration) Option {
	if duration < time.Millisecond {
		panic("revocation watcher lease duration must be at least 1ms")
	}
	return func(w *Watcher) { w.lease.duration = duration }
}

func WithPageSize(pageSize int32) Option {
	if pageSize <= 0 {
		panic("revocation watcher page size must be positive")
	}
	return func(w *Watcher) { w.pageSize = pageSize }
}

func WithMaxEventsPerRun(maxEvents int32) Option {
	if maxEvents <= 0 {
		panic("revocation watcher event limit must be positive")
	}
	return func(w *Watcher) { w.maxEventsPerRun = maxEvents }
}

// New returns a watcher with production defaults.
func New(st store.Store, lifecycle *auth.CredentialLifecycleEffects, opts ...Option) *Watcher {
	if lifecycle == nil {
		panic("revocation watcher requires credential lifecycle effects")
	}
	operationsCtx, cancelOperations := context.WithCancel(context.Background())
	w := &Watcher{
		store:            st,
		lifecycle:        lifecycle,
		interval:         DefaultInterval,
		pageSize:         DefaultPageSize,
		maxEventsPerRun:  DefaultMaxEventsPerRun,
		operationsCtx:    operationsCtx,
		cancelOperations: cancelOperations,
		errors:           make(chan error, 1),
		lease: runtimeLease{
			holderID: id.Generate(),
			duration: DefaultLeaseDuration,
		},
	}
	w.lease.watcher = w
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// SeedCursor publishes at most one bounded startup batch and advances this
// watcher's cursor to the sequence fence returned by that same locked
// transaction. Pending events beyond the batch are intentionally replayed by
// RunOnce; this bounds startup without skipping concurrently published events.
func (w *Watcher) SeedCursor(ctx context.Context) error {
	if w.closed.Load() {
		return fmt.Errorf("seed revocation event cursor: %w", ErrClosed)
	}
	ctx, cancel := w.operationContext(ctx)
	defer cancel()
	if err := w.lease.lock(ctx, "seed revocation event cursor"); err != nil {
		return err
	}
	defer w.lease.mu.Unlock()
	if w.lease.seeded {
		return fmt.Errorf("seed revocation event cursor: already seeded")
	}
	leaseDuration := w.lease.duration
	leaseStartedAt := time.Now()
	maxSeq, err := w.store.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
		HolderID:      w.lease.holderID,
		PublishLimit:  w.maxEventsPerRun,
		LeaseDuration: leaseDuration,
	})
	if err != nil {
		return fmt.Errorf("seed revocation event cursor: %w", err)
	}
	if w.closed.Load() {
		return w.lease.releaseAfterConcurrentClose(ctx, fmt.Errorf("seed revocation event cursor: %w", ErrClosed))
	}
	leaseExpiresAt, exceeded := leaseBudgetExpiry(leaseStartedAt, leaseDuration)
	if exceeded {
		budgetErr := fmt.Errorf("seed revocation event cursor: %w: acquisition exceeded local lease budget", ErrLeaseLost)
		releaseErr := w.lease.release(ctx)
		return errors.Join(budgetErr, errwrap.Wrap(releaseErr, "release Hub runtime lease after failed seed"))
	}
	w.lease.lastSeq = maxSeq
	w.lease.leaseExpiresAt = leaseExpiresAt
	w.lease.seeded = true
	// The holder ID is the only identifier every later lease message carries,
	// including the fatal ones. Without this line it appears for the first time
	// in the error that stops the Hub, anchored to nothing -- the operator cannot
	// tell when the lease was taken, at what cursor, or whether it is even this
	// process's own.
	slog.Info("revocation watcher: acquired the Hub runtime lease",
		"holder", w.lease.holderID, "cursor", maxSeq, "lease_duration", leaseDuration)
	return nil
}

// StartLoop starts the owned watcher goroutine. Lease loss is sent to Errors
// and permanently stops the loop; callers must treat it as process-fatal.
func (w *Watcher) StartLoop(ctx context.Context) {
	if w.closed.Load() {
		return
	}
	// Unbounded acquire: StartLoop has no caller budget to honor, and a
	// context.Background acquire cannot fail, so the error is discarded.
	_ = w.lease.mu.Lock(context.Background())
	if !w.lease.seeded {
		w.lease.signalFatal(ErrNotSeeded)
		w.lease.mu.Unlock()
		return
	}
	w.lease.mu.Unlock()

	// The started/loopDone start handshake lives under its own lock, released
	// before any lease-lock acquisition so the two are never nested.
	w.lifecycleMu.Lock()
	if w.started || w.closed.Load() {
		w.lifecycleMu.Unlock()
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	w.loopCancel.Store(&cancel)
	w.loopDone = make(chan struct{})
	w.started = true
	done := w.loopDone
	w.lifecycleMu.Unlock()

	// The lease heartbeat runs on its OWN goroutine, independent of event
	// processing. runOnce releases w.lease.mu across the (potentially slow) channel
	// teardown in applyEvent, so the heartbeat can renew the lease even while a
	// wedged worker/frontend blocks a teardown -- otherwise a hung peer could
	// stall the single processing goroutine past the lease deadline and
	// self-fence the whole Hub. Either goroutine cancels loopCtx on a fatal
	// error, stopping the other; loopDone closes once both have exited.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w.processingLoop(loopCtx) }()
	go func() { defer wg.Done(); w.renewalLoop(loopCtx) }()
	go func() { wg.Wait(); close(done) }()
}

// processingLoop publishes and consumes revocation events on a fixed interval.
// It does NOT own lease liveness during active teardown -- renewalLoop does --
// but runOnce still renews per page to persist cursor progress and renews when
// idle-stale.
func (w *Watcher) processingLoop(loopCtx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		saturated, err := w.runOnce(loopCtx)
		if errorsIsLeaseFatal(err) {
			w.cancelLoop()
			return
		}
		if saturated {
			retryTimer := time.NewTimer(saturatedRetryDelay)
			select {
			case <-loopCtx.Done():
				stopTimer(retryTimer)
				return
			case <-retryTimer.C:
				continue
			}
		}
		select {
		case <-loopCtx.Done():
			return
		case <-ticker.C:
		}
	}
}

// renewalLoop keeps the runtime lease alive on a heartbeat that is decoupled
// from event processing, so a slow channel teardown cannot delay renewal past
// the lease deadline. It ticks at a quarter of the lease duration and renews
// once the lease has passed its half-life, guaranteeing a renewal well before
// expiry. A fatal lease loss cancels loopCtx so the processing goroutine stops.
func (w *Watcher) renewalLoop(loopCtx context.Context) {
	ticker := time.NewTicker(max(w.lease.duration/4, time.Millisecond))
	defer ticker.Stop()
	for {
		select {
		case <-loopCtx.Done():
			return
		case <-ticker.C:
			// Unbounded acquire (cannot fail): the heartbeat must renew whenever
			// the sweep lets go, and loopCtx cancellation is observed on the next
			// select rather than by abandoning a renewal it is entitled to make.
			_ = w.lease.mu.Lock(context.Background())
			// loopCtx, not a lease-limited context: renew limits its own
			// round-trip by the lease, and the recovery it falls back to runs
			// precisely when that deadline has passed.
			err := w.lease.renewIfStale(loopCtx)
			w.lease.mu.Unlock()
			// Only a lease-fatal error (a rival holder, or a local-budget expiry)
			// stops the watcher; renew and recover have already
			// signaled it on w.errors so the server fences. A transient store
			// error (SQLITE_BUSY, a network blip) leaves the still-valid lease
			// intact -- log and retry on the next tick instead of silently killing
			// the watcher (which would leave the Hub serving with revocations no
			// longer applied). A lease that merely LAPSED is likewise not fatal:
			// recover re-takes it, which is what carries a suspended
			// laptop across its sleep.
			if errorsIsLeaseFatal(err) {
				w.cancelLoop()
				return
			}
			if err != nil && !errors.Is(err, ErrClosed) {
				slog.Warn("revocation watcher: lease renewal failed, will retry", "error", err)
			}
		}
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

// RunOnce publishes pending revocation events and consumes published events in
// bounded pages. Store errors are logged and retried by the next sweep.
func (w *Watcher) RunOnce(ctx context.Context) error {
	_, err := w.runOnce(ctx)
	return err
}

// runOnce also reports whether it consumed the per-run cap, allowing the
// owned loop to drain a backlog promptly without making one pass unbounded.
func (w *Watcher) runOnce(ctx context.Context) (bool, error) {
	if w.closed.Load() {
		return false, ErrClosed
	}
	ctx, cancel := w.operationContext(ctx)
	defer cancel()
	if err := w.runMu.Lock(ctx); err != nil {
		if w.closed.Load() {
			return false, ErrClosed
		}
		return false, err
	}
	defer w.runMu.Unlock()
	if err := w.lease.lock(ctx, "revocation sweep"); err != nil {
		return false, err
	}
	defer w.lease.mu.Unlock()
	if !w.lease.seeded {
		return false, ErrNotSeeded
	}
	if err := w.lease.ensure(ctx); err != nil {
		return false, err
	}
	// Each store round-trip runs with w.lease.mu released (see runtimeLease.runStoreUnlocked),
	// limited by the lease deadline captured before release, so a fenced-out Hub
	// stops mutating past its lease while renewalLoop can still renew during a
	// slow page -- a merely-slow (not down) store cannot self-fence the sole Hub.
	// Each phase re-derives its deadline from the current leaseExpiresAt, which
	// renew/renewalLoop extend, so a multi-page drain is not aborted at the
	// pre-renewal deadline even though the lease was validly renewed.
	pageSize := w.pageSize
	maxEvents := w.maxEventsPerRun
	// A lease-fatal publish error (the lease expired mid-drain) aborts the
	// sweep -- publishPendingLocked has already signaled it. A transient publish
	// error is logged there and left for the next sweep, so fall through and
	// consume whatever is already published.
	if err := w.publishPendingLocked(ctx, pageSize, maxEvents); errorsIsLeaseFatal(err) {
		return false, err
	}

	var processed int32
	for processed < maxEvents {
		limit := min(pageSize, maxEvents-processed)
		n, drained, err := w.consumePageLocked(ctx, limit)
		if err != nil {
			return false, err
		}
		if drained {
			return false, nil
		}
		processed += n
	}
	return processed == maxEvents, nil
}

// publishPendingLocked publishes pending revocation events into the gapless seq
// stream in bounded pages. Each page's store round-trip runs with w.lease.mu released
// (see runtimeLease.runStoreUnlocked) so renewalLoop can keep the lease alive during a slow
// publish, and it also renews between pages to persist lease liveness across a
// large backlog drain. Transient store errors are logged and returned for the
// caller to treat as non-fatal; a lease-fatal error (already signaled) is
// returned so the sweep aborts. Caller holds w.lease.mu.
func (w *Watcher) publishPendingLocked(parentCtx context.Context, pageSize, maxEvents int32) error {
	var published int32
	for published < maxEvents {
		limit := min(pageSize, maxEvents-published)
		var n int64
		err := w.lease.runStoreUnlocked(parentCtx, func(ctx context.Context) error {
			var e error
			n, e = w.store.RevocationEvents().PublishPending(ctx, limit)
			return e
		})
		if err != nil {
			slog.Warn("revocation watcher: publish pending failed", "error", err)
			return w.lease.handleStoreError(parentCtx, err)
		}
		if n == 0 {
			return nil
		}
		published += int32(n)
		if n < int64(limit) {
			return nil
		}
		// parentCtx, not a lease-limited context: renew applies the lease
		// deadline to its own round-trip, and its recovery path needs a context
		// that outlives the deadline it repairs.
		if err := w.lease.renewIfStale(parentCtx); err != nil {
			// Log before returning: runOnce treats a non-fatal return here as
			// transient and discards it (falling through to consume), so without
			// this the only store error on the publish path that leaves no
			// breadcrumb is the inter-page renew. Consistent with the publish/list
			// warnings above; a lease-fatal err is already signaled via
			// signalFatal, so logging unconditionally is harmless.
			slog.Warn("revocation watcher: inter-page lease renewal failed", "error", err)
			return err
		}
	}
	return nil
}

// consumePageLocked lists and applies one page of published events, advances the
// cursor, and renews the lease. The list round-trip runs with w.lease.mu released (see
// runtimeLease.runStoreUnlocked) so renewalLoop can keep the lease alive during a slow page;
// the cursor is snapshotted before release (only this single sweep goroutine
// mutates it, so it is stable across the gap). Returns the number of events
// applied and whether the stream is drained. Caller holds w.lease.mu.
func (w *Watcher) consumePageLocked(parentCtx context.Context, limit int32) (int32, bool, error) {
	lastSeq := w.lease.lastSeq
	var events []store.PublishedRevocationEvent
	err := w.lease.runStoreUnlocked(parentCtx, func(ctx context.Context) error {
		var e error
		events, e = w.store.RevocationEvents().ListPublishedAfter(ctx, lastSeq, limit)
		return e
	})
	if err != nil {
		slog.Warn("revocation watcher: list published failed", "error", err)
		return 0, false, w.lease.handleStoreError(parentCtx, err)
	}
	if len(events) == 0 {
		return 0, true, w.lease.renewIfStale(parentCtx)
	}
	for _, event := range events {
		if err := parentCtx.Err(); err != nil {
			return 0, false, err
		}
		if w.closed.Load() || !w.lease.seeded {
			return 0, false, ErrClosed
		}
		if err := w.lease.ensure(parentCtx); err != nil {
			return 0, false, err
		}
		if event.Seq != w.lease.lastSeq+1 {
			fatal := fmt.Errorf(
				"%w: holder %s cannot replay from seq %d; the next published event is seq %d",
				ErrLeaseLost, w.lease.holderID, w.lease.lastSeq, event.Seq)
			w.lease.signalFatal(fatal)
			return 0, false, fatal
		}
		// Apply the event's teardown WITHOUT w.lease.mu so renewalLoop can keep the
		// lease alive across a slow channel teardown -- a wedged worker or
		// back-pressured frontend can block a channel close for seconds, and
		// holding w.lease.mu across that would stall renewal and self-fence the Hub.
		// applyEvent touches only the auth registry and channel manager (their
		// own locks), never w.lease.mu-guarded watcher state. It cannot fail: every
		// event kind applies an in-process effect and an unknown kind is logged and
		// skipped, never fenced (see applyEvent), so there is no fatal path here.
		w.applyEventUnlocked(event)
		// Record progress immediately: applyEventUnlocked has already applied the
		// event's in-process effect, so the cursor must advance with it. Inserting
		// the parentCtx/closed checks between apply and advance would let a
		// concurrent cancel/close leave the event applied but the cursor stale,
		// re-applying it on the next sweep (harmless today only because every
		// apply is idempotent, but the atomicity guarantee would be gone).
		w.lease.lastSeq = event.Seq
		if err := parentCtx.Err(); err != nil {
			return 0, false, err
		}
		if w.closed.Load() || !w.lease.seeded {
			return 0, false, ErrClosed
		}
	}
	// parentCtx, not a lease-limited context. renew reads leaseExpiresAt
	// itself when it limits its round-trip, so it always uses the deadline
	// renewalLoop left there rather than one snapshotted before the teardown
	// above -- and its recovery path still runs when that deadline has passed.
	if err := w.lease.renew(parentCtx); err != nil {
		return 0, false, err
	}
	// A short page (fewer than limit) means no more events exist at this seq
	// right now, so report drained and skip the trailing empty ListPublishedAfter
	// the caller would otherwise issue just to learn the backlog is exhausted --
	// mirroring publishPendingLocked's `n < limit` short-circuit. Concurrently
	// published events are picked up on the next tick, as the publish path already
	// assumes.
	return int32(len(events)), int32(len(events)) < limit, nil
}

func errorsIsLeaseFatal(err error) bool {
	return err != nil && (errors.Is(err, ErrLeaseLost) ||
		errors.Is(err, ErrNotSeeded))
}

// Errors reports fatal lifecycle errors. The channel is intentionally not
// closed because the Watcher has a single lifetime and emits at most one error.
func (w *Watcher) Errors() <-chan error { return w.errors }

// cancelLoop cancels the owned loop's context if one is running. Safe to call
// before StartLoop (no-op) and repeatedly (context cancel is idempotent).
func (w *Watcher) cancelLoop() {
	if cp := w.loopCancel.Load(); cp != nil {
		(*cp)()
	}
}

// operationContext preserves the caller's deadline and values while linking
// cancellation to the Watcher's single owned lifetime. It delegates to the
// shared ctxutil.WithLinkedCancel (context.AfterFunc based, no per-operation
// bridge goroutine, stopped when the operation exits normally). A nil
// operationsCtx -- production Watchers always receive one from New; the zero
// value stays usable for focused package-internal tests and defensive teardown
// of partially constructed values -- is simply not linked.
func (w *Watcher) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return ctxutil.WithLinkedCancel(parent, w.operationsCtx)
}

// Close stops the owned loop, waits for it, and releases the runtime lease.
// Lease release is attempted even when the loop does not drain before ctx is
// exhausted; in that case the drain error is returned after the release.
func (w *Watcher) Close(ctx context.Context) error {
	w.closed.Store(true)
	if w.cancelOperations != nil {
		w.cancelOperations()
	}
	drainErr := w.stopLoop(ctx)
	releaseErr := w.releaseSeededLease(ctx)
	return errors.Join(drainErr, releaseErr)
}

func (w *Watcher) stopLoop(ctx context.Context) error {
	// Cancel the loop context BEFORE taking any lock: an in-flight runOnce holds
	// w.lease.mu across its store round-trips, so locking first could block Close
	// well past its ctx budget. Cancelling aborts those round-trips (the store
	// honors ctx), letting runOnce release the lease lock promptly. The repeat
	// under lifecycleMu -- where loopDone is published -- covers a StartLoop that
	// stored loopCancel just after this pre-lock cancel.
	w.cancelLoop()
	w.lifecycleMu.Lock()
	w.cancelLoop()
	done := w.loopDone
	w.lifecycleMu.Unlock()

	// Wait for the processing/renewal goroutines to exit so no straggler
	// touches the lease row. A slow drain is reported to Close, which still
	// attempts release so the next Hub launch is not fenced until the lease TTL.
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			slog.Warn("revocation watcher: loop drain timed out during close; releasing lease anyway")
			return ctx.Err()
		}
	}
	return nil
}

func (w *Watcher) releaseSeededLease(ctx context.Context) error {
	waitCtx, cancelWait := leaseReleaseContext(ctx)
	defer cancelWait()
	// Acquire ONLY the lease-state lock, not runMu. runOnce holds runMu across
	// applyEventUnlocked -> applyEvent, whose channel teardown can block for
	// seconds on a back-pressured frontend and is not cancellable via the
	// watcher's contexts; waiting on runMu could exhaust the release budget and
	// orphan the lease for its 30s TTL. applyEventUnlocked releases lease.mu
	// during each event's lifecycle effect, so this acquisition succeeds even
	// while a sweep is stuck in that teardown. The sweep cannot re-acquire the
	// lease afterwards: Close has already set `closed` (renew checks that flag)
	// and cancelled operationsCtx, so the sweep aborts at its next event
	// boundary and any in-flight renewal unwinds through its cancelled context.
	// A limited acquire, not a TryLock spin: ctxutil.Mutex serves waiters FIFO, so
	// this release earns the lock behind the sweep's next Unlock instead of losing
	// every race to it and burning the whole budget without ever acquiring a lock
	// it is entitled to.
	if err := w.lease.mu.Lock(waitCtx); err != nil {
		return fmt.Errorf("acquire lease state for release: %w", err)
	}
	defer w.lease.mu.Unlock()
	if !w.lease.seeded {
		return nil
	}
	// Waiting for a sweep/renewal to drain must not consume the database
	// cleanup budget. Once the lease state is exclusively owned, the durable
	// DELETE runs under a fresh limited context decoupled from the caller's
	// (possibly exhausted) deadline -- the same policy runtimeLease.release
	// applies, so route through it rather than reconstructing the context here.
	if err := w.lease.release(ctx); err != nil {
		return fmt.Errorf("release Hub runtime lease: %w", err)
	}
	w.lease.seeded = false
	// Pairs with the acquisition log, so a lease that was NEVER released is
	// visible as a missing line. That is the difference between a Hub that shut
	// down cleanly and one whose row is orphaned until the TTL expires -- which
	// is what fences the next launch.
	slog.Info("revocation watcher: released the Hub runtime lease",
		"holder", w.lease.holderID, "cursor", w.lease.lastSeq)
	return nil
}

// applyEventUnlocked runs applyEvent with w.lease.mu released (so renewalLoop
// can renew across a slow channel teardown) and re-locks on the way out even if
// applyEvent panics. Its caller holds w.lease.mu under runOnce's defer Unlock,
// so returning from the unlocked window without re-locking -- as a panic through
// a bare Unlock/apply/Lock sequence would -- would double-panic on that defer.
// The deferred re-lock keeps the "held on entry, held on return" invariant.
func (w *Watcher) applyEventUnlocked(event store.PublishedRevocationEvent) {
	w.lease.mu.Unlock()
	// Unbounded (and therefore infallible) re-lock: see runtimeLease.runStoreUnlocked.
	defer func() { _ = w.lease.mu.Lock(context.Background()) }()
	w.applyEvent(event)
}

// applyEvent applies one revocation event's in-process effect. It has no
// failure path: every recognized kind dispatches to a void lifecycle effect,
// and an unrecognized kind is logged and skipped rather than fenced, so the
// watcher never treats event application as fatal.
func (w *Watcher) applyEvent(event store.PublishedRevocationEvent) {
	switch event.Event.Kind {
	case store.RevocationEventKindSession:
		w.applySessionEvent(event.Event)
	case store.RevocationEventKindAPIToken:
		w.applyTokenEvent(auth.BearerKindAPI, event.Event)
	case store.RevocationEventKindAPITokenRotation:
		w.applyAPITokenRotationEvent(event.Event)
	case store.RevocationEventKindDelegationToken:
		w.applyTokenEvent(auth.BearerKindDelegation, event.Event)
	case store.RevocationEventKindUserTokens:
		w.applyUserTokensEvent(event.Event)
	case store.RevocationEventKindUserInfo:
		w.applyUserInfoEvent(event.Event)
	default:
		// An unrecognized event kind (data corruption, or a forward-compat kind
		// written by a newer binary) is logged and SKIPPED, not treated as fatal.
		// Fencing the sole active Hub on one unprocessable row is a full outage,
		// and a restart seeds the cursor past the row anyway -- so skipping reaches
		// the same end-state without the downtime, while every OTHER revocation in
		// the stream keeps flowing. Surfaced loudly so an operator/alert can catch
		// a genuinely-unexpected kind.
		slog.Error("revocation watcher: skipping unknown event kind",
			"seq", event.Seq, "event", event.Event.ID, "kind", event.Event.Kind)
	}
}

func (w *Watcher) applyAPITokenRotationEvent(event store.RevocationEvent) {
	// Cross-process backstop: invalidate the cached secret only. The zero
	// expiry means "do not reschedule leases/channels" -- those live on the Hub
	// that performed the rotation, which already extended them in-process.
	w.lifecycle.BearerRotatedCacheOnly(auth.BearerKindAPI, event.SubjectID)
}

// eventUserID resolves the user a user-scoped event targets. user_tokens and
// user_info events carry the user in UserID; SubjectID is the fallback for
// events whose subject IS the user, so both consumers resolve it identically.
func eventUserID(event store.RevocationEvent) string {
	if event.UserID != "" {
		return event.UserID
	}
	return event.SubjectID
}

// applyUserInfoEvent drops cached profile data (e.g. IsAdmin) for the user
// without revoking credentials, so a non-credential change like an admin-role
// update propagates across Hub processes.
func (w *Watcher) applyUserInfoEvent(event store.RevocationEvent) {
	w.lifecycle.UserInfoInvalidated(eventUserID(event))
}

func (w *Watcher) applySessionEvent(event store.RevocationEvent) {
	w.lifecycle.SessionRevoked(event.SubjectID)
}

func (w *Watcher) applyTokenEvent(kind auth.BearerKind, event store.RevocationEvent) {
	w.lifecycle.BearerRevoked(kind, event.SubjectID)
}

func (w *Watcher) applyUserTokensEvent(event store.RevocationEvent) {
	w.lifecycle.UserRevoked(eventUserID(event), event.UserAuthGeneration)
}
