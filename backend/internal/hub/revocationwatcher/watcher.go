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
	// leaseReleaseTimeout bounds a best-effort Hub runtime lease release when
	// the caller's own context is already exhausted: after a failed SeedCursor
	// acquisition, and during Close when the loop drain has overrun the shutdown
	// budget. The release is decoupled from the caller's deadline so the DELETE
	// still runs, but bounded so a broken store cannot hang shutdown forever.
	leaseReleaseTimeout = 5 * time.Second
)

// leaseState is the runtime lease + cursor position the watcher advances as it
// drains the revocation stream. Its own mutex guards exactly these three fields,
// so what the lock protects is explicit rather than mixed into the watcher's
// immutable config and loop-lifecycle fields. The sweep holds this lock across
// its store round-trips (releasing it per runStoreUnlocked so the heartbeat can
// renew), which is why the sweep helpers operate on w.lease.
type leaseState struct {
	mu      ctxutil.Mutex
	lastSeq int64
	// leaseExpiresAt is the granted deadline, and it must always come from
	// time.Now (directly or via Add) so it carries BOTH a monotonic and a wall
	// clock reading. leaseRemainingLocked reads both, because the two can
	// disagree by an unbounded amount and only one of them matches the database.
	leaseExpiresAt time.Time
	seeded         bool
	// cursorUnverified is set when recovery reacquired the lease but has not yet
	// proven that lastSeq is still replayable. No event may be applied while it
	// is set; ensureLeaseLocked clears it (see verifyCursorLocked).
	cursorUnverified bool
}

// Watcher is constructed once at hub bootstrap and started via StartLoop.
type Watcher struct {
	// Immutable configuration: set at construction and never mutated, so it
	// needs no lock.
	store           store.Store
	lifecycle       *auth.CredentialLifecycleEffects
	interval        time.Duration
	leaseDuration   time.Duration
	pageSize        int32
	maxEventsPerRun int32
	holderID        string
	// operationsCtx owns every seed and sweep operation, including public
	// RunOnce calls whose caller context may otherwise outlive this Watcher.
	// Close cancels it before waiting for the active operation to drain, which
	// prevents a store mutation from continuing after Watcher teardown.
	operationsCtx    context.Context
	cancelOperations context.CancelFunc

	// lease is the mutex-guarded runtime lease + cursor state (its own lock).
	lease leaseState
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
	return func(w *Watcher) { w.leaseDuration = duration }
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
		leaseDuration:    DefaultLeaseDuration,
		pageSize:         DefaultPageSize,
		maxEventsPerRun:  DefaultMaxEventsPerRun,
		holderID:         id.Generate(),
		operationsCtx:    operationsCtx,
		cancelOperations: cancelOperations,
		errors:           make(chan error, 1),
	}
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
	if err := w.lockLeaseState(ctx, "seed revocation event cursor"); err != nil {
		return err
	}
	defer w.lease.mu.Unlock()
	if w.lease.seeded {
		return fmt.Errorf("seed revocation event cursor: already seeded")
	}
	leaseDuration := w.leaseDuration
	leaseStartedAt := time.Now()
	maxSeq, err := w.store.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
		HolderID:      w.holderID,
		PublishLimit:  w.maxEventsPerRun,
		LeaseDuration: leaseDuration,
	})
	if err != nil {
		return fmt.Errorf("seed revocation event cursor: %w", err)
	}
	if w.closed.Load() {
		releaseErr := w.releaseHubRuntimeLease(ctx)
		return errors.Join(
			fmt.Errorf("seed revocation event cursor: %w", ErrClosed),
			errwrap.Wrap(releaseErr, "release Hub runtime lease after concurrent close"),
		)
	}
	leaseExpiresAt, exceeded := leaseBudgetExpiry(leaseStartedAt, leaseDuration)
	if exceeded {
		budgetErr := fmt.Errorf("seed revocation event cursor: %w: acquisition exceeded local lease budget", ErrLeaseLost)
		releaseErr := w.releaseHubRuntimeLease(ctx)
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
		"holder", w.holderID, "cursor", maxSeq, "lease_duration", leaseDuration)
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
		w.signalFatalLocked(ErrNotSeeded)
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
	ticker := time.NewTicker(max(w.leaseDuration/4, time.Millisecond))
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
			// loopCtx, not a lease-bounded context: renewLocked bounds its own
			// round-trip by the lease, and the recovery it falls back to runs
			// precisely when that deadline has passed.
			err := w.renewLeaseIfStaleLocked(loopCtx)
			w.lease.mu.Unlock()
			// Only a lease-fatal error (a rival holder, or a local-budget expiry)
			// stops the watcher; renewLocked and recoverLeaseLocked have already
			// signaled it on w.errors so the server fences. A transient store
			// error (SQLITE_BUSY, a network blip) leaves the still-valid lease
			// intact -- log and retry on the next tick instead of silently killing
			// the watcher (which would leave the Hub serving with revocations no
			// longer applied). A lease that merely LAPSED is likewise not fatal:
			// recoverLeaseLocked re-takes it, which is what carries a suspended
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
	if err := w.lockLeaseState(ctx, "revocation sweep"); err != nil {
		return false, err
	}
	defer w.lease.mu.Unlock()
	if !w.lease.seeded {
		return false, ErrNotSeeded
	}
	if err := w.ensureLeaseLocked(ctx); err != nil {
		return false, err
	}
	// Each store round-trip runs with w.lease.mu released (see runStoreUnlocked),
	// bounded by the lease deadline captured before release, so a fenced-out Hub
	// stops mutating past its lease while renewalLoop can still renew during a
	// slow page -- a merely-slow (not down) store cannot self-fence the sole Hub.
	// Each phase re-derives its deadline from the current leaseExpiresAt, which
	// renewLocked/renewalLoop extend, so a multi-page drain is not aborted at the
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

// runStoreUnlocked runs a store round-trip with w.lease.mu RELEASED, bounded by the
// lease deadline captured before release, then re-acquires w.lease.mu. Releasing the
// lock is what lets renewalLoop keep the runtime lease alive during a merely-slow
// (not down) store call, so a single slow page cannot self-fence the sole Hub: if
// the lease is validly renewed meanwhile, the caller's post-call
// handleStoreErrorLocked re-check sees the extended deadline and classifies a
// deadline abort as transient rather than lease-fatal. If the lease is genuinely
// LOST, renewalLoop cancels loopCtx (the parent of ctx here), which aborts the
// in-flight call, so a fenced-out Hub still stops promptly. The store calls touch
// no w.lease.mu-guarded watcher state, so releasing the lock across them is safe (the
// same reasoning that lets applyEvent run unlocked). Caller holds w.lease.mu; it is
// held again on return.
func (w *Watcher) runStoreUnlocked(parentCtx context.Context, fn func(context.Context) error) error {
	deadline := w.lease.leaseExpiresAt
	w.lease.mu.Unlock()
	// Re-lock on the way out even if fn panics. runOnce holds w.lease.mu under a
	// `defer w.lease.mu.Unlock()`, so returning from this unlocked window without
	// re-locking -- which a panic through the bare Unlock/Lock pair would do --
	// makes that defer unlock an already-unlocked mutex, a second panic that masks
	// the real cause and corrupts lock state for any recover-based supervisor.
	// The re-lock is deliberately unbounded (and so cannot fail): the caller's
	// "held on entry, held on return" invariant must hold no matter what parentCtx
	// did while the lock was released.
	defer func() { _ = w.lease.mu.Lock(context.Background()) }()
	ctx, cancel := context.WithDeadline(parentCtx, deadline)
	defer cancel()
	return fn(ctx)
}

// publishPendingLocked publishes pending revocation events into the gapless seq
// stream in bounded pages. Each page's store round-trip runs with w.lease.mu released
// (see runStoreUnlocked) so renewalLoop can keep the lease alive during a slow
// publish, and it also renews between pages to persist lease liveness across a
// large backlog drain. Transient store errors are logged and returned for the
// caller to treat as non-fatal; a lease-fatal error (already signaled) is
// returned so the sweep aborts. Caller holds w.lease.mu.
func (w *Watcher) publishPendingLocked(parentCtx context.Context, pageSize, maxEvents int32) error {
	var published int32
	for published < maxEvents {
		limit := min(pageSize, maxEvents-published)
		var n int64
		err := w.runStoreUnlocked(parentCtx, func(ctx context.Context) error {
			var e error
			n, e = w.store.RevocationEvents().PublishPending(ctx, limit)
			return e
		})
		if err != nil {
			slog.Warn("revocation watcher: publish pending failed", "error", err)
			return w.handleStoreErrorLocked(parentCtx, err)
		}
		if n == 0 {
			return nil
		}
		published += int32(n)
		if n < int64(limit) {
			return nil
		}
		// parentCtx, not a lease-bounded context: renewLocked applies the lease
		// deadline to its own round-trip, and its recovery path needs a context
		// that outlives the deadline it repairs.
		if err := w.renewLeaseIfStaleLocked(parentCtx); err != nil {
			// Log before returning: runOnce treats a non-fatal return here as
			// transient and discards it (falling through to consume), so without
			// this the only store error on the publish path that leaves no
			// breadcrumb is the inter-page renew. Consistent with the publish/list
			// warnings above; a lease-fatal err is already signaled via
			// signalFatalLocked, so logging unconditionally is harmless.
			slog.Warn("revocation watcher: inter-page lease renewal failed", "error", err)
			return err
		}
	}
	return nil
}

// consumePageLocked lists and applies one page of published events, advances the
// cursor, and renews the lease. The list round-trip runs with w.lease.mu released (see
// runStoreUnlocked) so renewalLoop can keep the lease alive during a slow page;
// the cursor is snapshotted before release (only this single sweep goroutine
// mutates it, so it is stable across the gap). Returns the number of events
// applied and whether the stream is drained. Caller holds w.lease.mu.
func (w *Watcher) consumePageLocked(parentCtx context.Context, limit int32) (int32, bool, error) {
	lastSeq := w.lease.lastSeq
	var events []store.PublishedRevocationEvent
	err := w.runStoreUnlocked(parentCtx, func(ctx context.Context) error {
		var e error
		events, e = w.store.RevocationEvents().ListPublishedAfter(ctx, lastSeq, limit)
		return e
	})
	if err != nil {
		slog.Warn("revocation watcher: list published failed", "error", err)
		return 0, false, w.handleStoreErrorLocked(parentCtx, err)
	}
	if len(events) == 0 {
		return 0, true, w.renewLeaseIfStaleLocked(parentCtx)
	}
	for _, event := range events {
		if err := parentCtx.Err(); err != nil {
			return 0, false, err
		}
		if w.closed.Load() || !w.lease.seeded {
			return 0, false, ErrClosed
		}
		if err := w.ensureLeaseLocked(parentCtx); err != nil {
			return 0, false, err
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
	// parentCtx, not a lease-bounded context. renewLocked reads leaseExpiresAt
	// itself when it bounds its round-trip, so it always uses the deadline
	// renewalLoop left there rather than one snapshotted before the teardown
	// above -- and its recovery path still runs when that deadline has passed.
	if err := w.renewLocked(parentCtx); err != nil {
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

// renewLeaseIfStaleLocked renews the runtime lease only once it has passed
// roughly half its duration. In steady state (no events to consume) the sweep
// runs every interval, so renewing unconditionally would write a lease row on
// every tick -- ~leaseDuration/interval redundant writes that contend the
// single SQLite writer. Renewing at the half-life keeps ample liveness margin --
// a missed renewal is still caught, because once the lease actually expires the
// checkLeaseLocked guards in renewLocked and ensureLeaseLocked route to
// recovery -- while cutting idle renewal writes to ~2 per lease duration.
// During active consumption the caller renews every page instead, to persist
// cursor progress.
//
// Staleness comes from leaseRemainingLocked, so the half-life is measured by
// whichever clock says less is left. A suspend that freezes the monotonic clock
// therefore still reports the lease stale on the first tick after the wake.
func (w *Watcher) renewLeaseIfStaleLocked(ctx context.Context) error {
	if w.leaseRemainingLocked() > w.leaseDuration/2 {
		return nil
	}
	return w.renewLocked(ctx)
}

// renewLocked renews the durable lease, or recovers it when it has already
// lapsed.
//
// ctx must NOT be bounded by the lease deadline. Recovery runs precisely when
// that deadline has passed, so a context derived from it would be dead on
// arrival and no recovery could ever complete. renewLocked applies the lease
// deadline to the renewal round-trip itself, which is the call that bound
// belongs to: a fenced-out Hub must stop writing at its deadline, while a
// merely-slow store is kept alive by renewalLoop meanwhile.
func (w *Watcher) renewLocked(ctx context.Context) error {
	if w.closed.Load() {
		// Close has begun teardown and is about to release (or has released) the
		// durable lease; a renewal now would race that DELETE and could re-create
		// the row after release, orphaning it for its TTL. Close sets `closed`
		// before calling releaseSeededLease, so gating here lets an in-flight sweep
		// unwind without ever re-acquiring the lease.
		return ErrClosed
	}
	if err := w.checkLeaseLocked(); err != nil {
		// The lease already lapsed, so the renewal statement -- which requires a
		// live row -- cannot match. Recovery re-takes the lease and writes this
		// same cursor, which is exactly what the renewal would have done.
		return w.recoverLeaseLocked(ctx, err)
	}
	leaseDuration := w.leaseDuration
	leaseStartedAt := time.Now()
	renewCtx, cancelRenew := context.WithDeadline(ctx, w.lease.leaseExpiresAt)
	advanced, err := w.store.RevocationEvents().RenewHubRuntimeLease(renewCtx, store.RenewHubRuntimeLeaseParams{
		HolderID:      w.holderID,
		CursorSeq:     w.lease.lastSeq,
		LeaseDuration: leaseDuration,
	})
	cancelRenew()
	if err != nil {
		return w.handleStoreErrorLocked(ctx, err)
	}
	if !advanced {
		// The durable row is gone or no longer live even though the local
		// deadline says otherwise -- the divergence a suspend produces, and the
		// one the wall-clock guard in leaseRemainingLocked cannot catch when the
		// database's clock is simply ahead of ours. Recovery decides whether this
		// is a lapse to repair or a takeover to fence on.
		return w.recoverLeaseLocked(ctx, fmt.Errorf("holder %s was removed, replaced, or expired", w.holderID))
	}
	leaseExpiresAt, exceeded := leaseBudgetExpiry(leaseStartedAt, leaseDuration)
	if exceeded {
		err := fmt.Errorf("%w: holder %s renewal exceeded local lease budget", ErrLeaseLost, w.holderID)
		w.signalFatalLocked(err)
		return err
	}
	w.lease.leaseExpiresAt = leaseExpiresAt
	return nil
}

// recoverLeaseLocked re-takes a lease that lapsed while this process could not
// renew it, and returns nil once the lease is held again and its cursor is
// proven replayable.
//
// A lapse is NOT a takeover. A suspended laptop, a paused VM, or a stall long
// enough to miss every heartbeat all end with the durable row expired and
// nobody else holding it; the Hub that wakes up is still the only Hub. Fencing
// it there stopped the process for a condition that repairs itself, which is
// what killed the desktop app's local Hub after every sleep. Only a rival
// holder's row is fatal, because that Hub may have consumed and compacted past
// this cursor -- damage no reacquisition can undo.
//
// cause is the lapse this recovery answers. It is reported, not returned: a
// successful recovery must not hand back an ErrLeaseLost that callers treat as
// process-fatal.
func (w *Watcher) recoverLeaseLocked(ctx context.Context, cause error) error {
	if w.closed.Load() {
		return ErrClosed
	}
	leaseDuration := w.leaseDuration
	// Read both clocks BEFORE the reacquisition overwrites the deadline: these
	// two numbers are what let a reader tell a suspend from a takeover, and they
	// are gone once the new deadline lands.
	monotonicLeft, wallLeft := w.leaseClockReadingsLocked()
	leaseStartedAt := time.Now()
	// Bounded by one lease duration, and NOT by the lapsed deadline: a lease
	// that cannot be re-taken within its own duration would be expired on
	// arrival anyway. ctx still cancels it, so Close (which cancels the loop and
	// the operation context before waiting) is not delayed by a slow store.
	reacquireCtx, cancelReacquire := context.WithTimeout(ctx, leaseDuration)
	err := w.store.RevocationEvents().ReacquireHubRuntimeLease(reacquireCtx, store.ReacquireHubRuntimeLeaseParams{
		HolderID:      w.holderID,
		CursorSeq:     w.lease.lastSeq,
		LeaseDuration: leaseDuration,
	})
	cancelReacquire()
	if errors.Is(err, store.ErrHubAlreadyRunning) {
		fatal := fmt.Errorf("%w: holder %s cannot reacquire the lease, another Hub holds it: %w", ErrLeaseLost, w.holderID, cause)
		// Logged here as well as signaled: the server folds this into an aggregate
		// error that surfaces only after the whole teardown, so without this line
		// the log shows the Hub stopping with no cause at the moment it decided to.
		slog.Error("revocation watcher: another Hub holds the runtime lease; this Hub must stop",
			"holder", w.holderID, "cursor", w.lease.lastSeq,
			"monotonic_remaining", monotonicLeft, "wall_remaining", wallLeft, "cause", cause)
		w.signalFatalLocked(fatal)
		return fatal
	}
	if err != nil {
		// Transient: the store refused this attempt, but the local deadline still
		// says lapsed, so the next sweep or heartbeat tries again. Deliberately
		// does NOT wrap cause -- an ErrLeaseLost in the returned chain would make
		// errorsIsLeaseFatal stop the watcher for a store blip.
		slog.Warn("revocation watcher: could not reacquire a lapsed lease, will retry",
			"error", err, "cause", cause, "holder", w.holderID)
		return fmt.Errorf("reacquire Hub runtime lease: %w", err)
	}
	if w.closed.Load() {
		// Close raced this reacquisition and has already run (or is running) its
		// release, so the row just written would outlive the Watcher. Remove it,
		// exactly as SeedCursor does for the same race.
		releaseErr := w.releaseHubRuntimeLease(ctx)
		return errors.Join(ErrClosed, errwrap.Wrap(releaseErr, "release Hub runtime lease after concurrent close"))
	}
	leaseExpiresAt, exceeded := leaseBudgetExpiry(leaseStartedAt, leaseDuration)
	if exceeded {
		fatal := fmt.Errorf("%w: holder %s reacquisition exceeded local lease budget", ErrLeaseLost, w.holderID)
		w.signalFatalLocked(fatal)
		return fatal
	}
	w.lease.leaseExpiresAt = leaseExpiresAt
	// The reacquisition kept the cursor, so nothing published during the lapse is
	// skipped -- but only a rival that ran DURING the lapse could have compacted
	// past it, and the reacquisition cannot see one that already released. Prove
	// the cursor before any event is applied.
	w.lease.cursorUnverified = true
	// Both clock readings, not just the minimum: monotonic_remaining still
	// positive beside a deeply negative wall_remaining is the signature of a
	// suspended process, and their difference is roughly how long it was frozen.
	slog.Warn("revocation watcher: reacquired a lease that lapsed while this process could not renew",
		"holder", w.holderID, "cursor", w.lease.lastSeq,
		"monotonic_remaining", monotonicLeft, "wall_remaining", wallLeft, "cause", cause)
	return w.verifyCursorLocked(ctx)
}

// verifyCursorLocked proves that every revocation this watcher has not yet
// applied is still in the stream, and clears cursorUnverified when it is.
//
// Sequence numbers are gapless (publishPending assigns them contiguously), and
// compaction deletes from the oldest, so the first surviving event above the
// cursor is cursor+1 unless something deleted the events in between. A hole
// there means a second Hub consumed and compacted this stream while the lease
// was lapsed, so this process would silently never apply those revocations --
// the one outcome worse than fencing. Fence.
//
// This runs after the lease is held again, and a live lease's cursor bounds
// compaction to rows at or below it, so no rival can open a hole between the
// two reads. Caller holds w.lease.mu.
func (w *Watcher) verifyCursorLocked(ctx context.Context) error {
	cursor := w.lease.lastSeq
	maxSeq, err := w.store.RevocationEvents().MaxPublishedSeq(ctx)
	if err != nil {
		return fmt.Errorf("read the revocation sequence to verify cursor %d: %w", cursor, err)
	}
	if maxSeq <= cursor {
		// Nothing was published past the cursor, so there is nothing to replay.
		w.lease.cursorUnverified = false
		return nil
	}
	events, err := w.store.RevocationEvents().ListPublishedAfter(ctx, cursor, 1)
	if err != nil {
		return fmt.Errorf("read the revocation stream to verify cursor %d: %w", cursor, err)
	}
	if len(events) == 0 || events[0].Seq != cursor+1 {
		fatal := fmt.Errorf(
			"%w: holder %s cannot replay from seq %d; another Hub consumed and compacted this stream",
			ErrLeaseLost, w.holderID, cursor)
		w.signalFatalLocked(fatal)
		return fatal
	}
	w.lease.cursorUnverified = false
	return nil
}

// ensureLeaseLocked returns nil while this watcher may safely apply events: it
// holds a live lease, and the cursor it holds is still replayable. It is the
// single gate every consume path passes, so neither check can be forgotten at a
// call site. Caller holds w.lease.mu.
func (w *Watcher) ensureLeaseLocked(ctx context.Context) error {
	if err := w.checkLeaseLocked(); err != nil {
		return w.recoverLeaseLocked(ctx, err)
	}
	if w.lease.cursorUnverified {
		return w.verifyCursorLocked(ctx)
	}
	return nil
}

// leaseRemainingLocked is how much of the lease is left by the SOONER of the two
// clocks that judge it.
//
// The wall-clock reading is what makes a suspend visible. Go's monotonic clock
// STOPS while the machine is asleep (darwin's mach_absolute_time, Linux's
// CLOCK_MONOTONIC), and so does every timer built on it, while the database
// compares lease_expires_at against its own wall clock, which does not stop. A
// laptop that sleeps for an hour therefore wakes holding a monotonic deadline
// that says the lease is live and a row that expired 59 minutes ago. Reading
// only the monotonic side let the watcher keep consuming on a lease it no longer
// held, until a renewal was refused and fenced the whole Hub.
//
// A time.Time from time.Now carries both readings, so both come from the one
// leaseExpiresAt field and cannot drift apart: Sub uses the monotonic reading
// when both operands have one, and Round(0) strips it so the second subtraction
// uses the wall reading. A wall clock that an NTP correction pushes forward only
// makes this report a lapse the database was about to report anyway.
//
// The divergence itself cannot be reproduced in a unit test. Only time.Now
// produces a monotonic reading, Add moves both readings together, and every
// operation that could separate them (AddDate, Round, Truncate, UTC) strips the
// monotonic one instead. The recovery this drives is covered end to end by
// TestWatcher_ReacquiresLeaseLapsedDuringSuspend, which produces the equivalent
// split from the database side.
//
// util/clockjump reports the same pauses process-wide, and this check does NOT
// defer to it: that detector samples on an interval, so its report arrives after
// a write this lease must already have refused. It explains the pause; this
// gates the write.
// Caller holds w.lease.mu.
func (w *Watcher) leaseRemainingLocked() time.Duration {
	monotonic, wall := w.leaseClockReadingsLocked()
	return min(monotonic, wall)
}

// leaseClockReadingsLocked reports the lease remainder as each clock sees it.
//
// The PAIR is the suspend signature, and it is the reason both readings are
// logged rather than only the minimum: a monotonic remainder that is still
// comfortably positive beside a wall remainder deep in the negative means the
// process did not run for the difference between them. No other condition
// produces that shape, so one log line separates "this laptop slept" from "a
// second Hub took the lease" without the reader having to infer it.
// Caller holds w.lease.mu.
func (w *Watcher) leaseClockReadingsLocked() (monotonic, wall time.Duration) {
	now := time.Now()
	return w.lease.leaseExpiresAt.Sub(now), w.lease.leaseExpiresAt.Round(0).Sub(now.Round(0))
}

func (w *Watcher) checkLeaseLocked() error {
	if remaining := w.leaseRemainingLocked(); remaining <= 0 {
		// Round(0) drops the monotonic suffix, so the printed deadline is the
		// wall time the database also judged this lease by.
		return fmt.Errorf("%w: holder %s expired at %s, %s ago",
			ErrLeaseLost, w.holderID, w.lease.leaseExpiresAt.Round(0), -remaining)
	}
	return nil
}

// leaseBudgetExpiry returns the deadline a lease granted at startedAt for dur is
// valid until, and whether the granting store round trip already outlasted that
// whole budget -- in which case the lease may be expired the instant it was
// granted and must not be trusted. The seed and renew paths share this identical
// budget check but layer their own release / fatal-signal handling and error
// context on the result, so it stays a single named home for the guard rather
// than two inline copies that could drift.
func leaseBudgetExpiry(startedAt time.Time, dur time.Duration) (expiresAt time.Time, exceeded bool) {
	expiresAt = startedAt.Add(dur)
	return expiresAt, !time.Now().Before(expiresAt)
}

// handleStoreErrorLocked reports the real cause of a failed store round-trip. A
// call that failed while the lease had lapsed usually failed BECAUSE of the
// lapse (its context carried the lease deadline), so repair the lease first and
// report the store error only if the lease is sound. Caller holds w.lease.mu.
func (w *Watcher) handleStoreErrorLocked(ctx context.Context, err error) error {
	if leaseErr := w.ensureLeaseLocked(ctx); leaseErr != nil {
		return leaseErr
	}
	return err
}

func (w *Watcher) signalFatalLocked(err error) {
	select {
	case w.errors <- err:
	default:
	}
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
	// lease afterwards: Close has already set `closed` (gating renewLocked) and
	// cancelled operationsCtx, so the sweep aborts at its next event boundary
	// and any in-flight renewal unwinds through its cancelled context.
	// A bounded acquire, not a TryLock spin: ctxutil.Mutex serves waiters FIFO, so
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
	// DELETE runs under a fresh bounded context decoupled from the caller's
	// (possibly exhausted) deadline -- the same policy releaseHubRuntimeLease
	// applies, so route through it rather than reconstructing the context here.
	if err := w.releaseHubRuntimeLease(ctx); err != nil {
		return fmt.Errorf("release Hub runtime lease: %w", err)
	}
	w.lease.seeded = false
	// Pairs with the acquisition log, so a lease that was NEVER released is
	// visible as a missing line. That is the difference between a Hub that shut
	// down cleanly and one whose row is orphaned until the TTL expires -- which
	// is what fences the next launch.
	slog.Info("revocation watcher: released the Hub runtime lease",
		"holder", w.holderID, "cursor", w.lease.lastSeq)
	return nil
}

// releaseHubRuntimeLease deliberately outlives an exhausted caller context,
// while retaining a fixed upper bound so a broken store cannot hang shutdown.
func (w *Watcher) releaseHubRuntimeLease(ctx context.Context) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseReleaseTimeout)
	defer cancel()
	_, err := w.store.RevocationEvents().ReleaseHubRuntimeLease(releaseCtx, w.holderID)
	return err
}

// leaseReleaseContext bounds releaseSeededLease's wait for the lease-state lock.
// Like releaseHubRuntimeLease it is detached from the caller's cancellation --
// the release must still run when Close's ctx is already cancelled, which is
// precisely when it is needed -- but unlike it, a still-live caller deadline caps
// the wait, so acquiring the lock cannot consume more than the caller budgeted.
// An ALREADY-expired ctx (whose remaining time would be negative) is excluded
// from the cap and gets the full leaseReleaseTimeout, so the release is never
// stillborn.
func leaseReleaseContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := leaseReleaseTimeout
	if deadline, ok := ctx.Deadline(); ok && ctx.Err() == nil {
		// Re-check the remaining time rather than trusting Err() alone: the
		// deadline can elapse between the Err() read and here, and min-ing in a
		// zero/negative remainder would hand back an already-expired context --
		// the stillborn release the exclusion above promises cannot happen.
		if remaining := time.Until(deadline); remaining > 0 {
			timeout = min(timeout, remaining)
		}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// lockLeaseState acquires w.lease.mu, bounded by ctx so Close can wait out an
// in-flight sweep, and treats a concurrent Close as ErrClosed. label qualifies
// the acquisition-failure error. On success the caller owns w.lease.mu and must
// unlock it. It centralizes the lock-and-close handling that SeedCursor and
// runOnce each repeated, so the close checks cannot drift between them.
// releaseSeededLease does not use it: it runs during Close, when closed is true
// by design and must not short-circuit the release.
func (w *Watcher) lockLeaseState(ctx context.Context, label string) error {
	if err := w.lease.mu.Lock(ctx); err != nil {
		if w.closed.Load() {
			return fmt.Errorf("%s: %w", label, ErrClosed)
		}
		return fmt.Errorf("%s: acquire lease state: %w", label, err)
	}
	if w.closed.Load() {
		w.lease.mu.Unlock()
		return fmt.Errorf("%s: %w", label, ErrClosed)
	}
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
	// Unbounded (and therefore infallible) re-lock: see runStoreUnlocked.
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
