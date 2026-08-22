package revocationwatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/util/ctxutil"
	"github.com/leapmux/leapmux/util/errwrap"
)

// leaseReleaseTimeout limits a best-effort Hub runtime lease release when
// the caller's own context is already exhausted: after a failed SeedCursor
// acquisition, and during Close when the loop drain has overrun the shutdown
// budget. The release is decoupled from the caller's deadline so the DELETE
// still runs, but limited so a broken store cannot hang shutdown forever.
const leaseReleaseTimeout = 5 * time.Second

// runtimeLease is the Hub singleton lease plus the revocation-stream cursor.
// Watcher holds one and runs publish/consume against it. This type owns lease
// liveness, recovery after a lapse, and the proof that lastSeq is still
// replayable. Watcher must not grow a second copy of that protocol.
//
// watcher is the owning Watcher. It supplies the store, the closed flag, and
// the fatal-error channel. Those stay on Watcher because Close, SeedCursor,
// and the owned loop already read them; duplicating them here would create a
// second source of truth.
//
// mu guards exactly the four cursor/liveness fields, so what the lock protects
// is explicit rather than mixed into the watcher's immutable config and
// loop-lifecycle fields. The sweep holds this lock across its store
// round-trips (releasing it per runStoreUnlocked so the heartbeat can renew).
type runtimeLease struct {
	watcher *Watcher
	mu      ctxutil.Mutex

	lastSeq int64
	// leaseExpiresAt is the granted deadline, and it must always come from
	// time.Now (directly or via Add) so it carries BOTH a monotonic and a wall
	// clock reading. remaining reads both, because the two can disagree by an
	// unbounded amount and only one of them matches the database.
	leaseExpiresAt time.Time
	seeded         bool
	// cursorUnverified is set when recovery reacquired the lease but has not yet
	// proven that lastSeq is still replayable. No event may be applied while it
	// is set; ensure clears it (see verifyCursor).
	cursorUnverified bool

	holderID string
	duration time.Duration
}

func (l *runtimeLease) events() store.RevocationEventStore {
	return l.watcher.store.RevocationEvents()
}

func (l *runtimeLease) isClosed() bool {
	return l.watcher != nil && l.watcher.closed.Load()
}

func (l *runtimeLease) signalFatal(err error) {
	select {
	case l.watcher.errors <- err:
	default:
	}
}

// lock acquires l.mu, limited by ctx so Close can wait out an in-flight sweep,
// and treats a concurrent Close as ErrClosed. label qualifies the
// acquisition-failure error. On success the caller owns l.mu and must unlock
// it. releaseSeededLease does not use it: it runs during Close, when closed is
// true by design and must not short-circuit the release.
func (l *runtimeLease) lock(ctx context.Context, label string) error {
	if err := l.mu.Lock(ctx); err != nil {
		if l.isClosed() {
			return fmt.Errorf("%s: %w", label, ErrClosed)
		}
		return fmt.Errorf("%s: acquire lease state: %w", label, err)
	}
	if l.isClosed() {
		l.mu.Unlock()
		return fmt.Errorf("%s: %w", label, ErrClosed)
	}
	return nil
}

// runStoreUnlocked runs a store round-trip with l.mu RELEASED, limited by the
// lease deadline captured before release, then re-acquires l.mu. Releasing the
// lock is what lets renewalLoop keep the runtime lease alive during a merely-slow
// (not down) store call, so a single slow page cannot self-fence the sole Hub: if
// the lease is validly renewed meanwhile, the caller's post-call
// handleStoreError re-check sees the extended deadline and classifies a
// deadline abort as transient rather than lease-fatal. If the lease is genuinely
// LOST, renewalLoop cancels loopCtx (the parent of ctx here), which aborts the
// in-flight call, so a fenced-out Hub still stops promptly. The store calls touch
// no mu-guarded lease state, so releasing the lock across them is safe (the
// same reasoning that lets applyEvent run unlocked). Caller holds l.mu; it is
// held again on return.
func (l *runtimeLease) runStoreUnlocked(parentCtx context.Context, fn func(context.Context) error) error {
	deadline := l.deadline()
	l.mu.Unlock()
	// Re-lock on the way out even if fn panics. runOnce holds l.mu under a
	// `defer l.mu.Unlock()`, so returning from this unlocked window without
	// re-locking -- which a panic through the bare Unlock/Lock pair would do --
	// makes that defer unlock an already-unlocked mutex, a second panic that masks
	// the real cause and corrupts lock state for any recover-based supervisor.
	// The re-lock is deliberately unbounded (and so cannot fail): the caller's
	// "held on entry, held on return" invariant must hold no matter what parentCtx
	// did while the lock was released.
	defer func() { _ = l.mu.Lock(context.Background()) }()
	ctx, cancel := context.WithDeadline(parentCtx, deadline)
	defer cancel()
	return fn(ctx)
}

// renewIfStale renews the runtime lease only once it has passed roughly half
// its duration. In steady state (no events to consume) the sweep runs every
// interval, so renewing unconditionally would write a lease row on every tick
// -- ~duration/interval redundant writes that contend the single SQLite writer.
// Renewing at the half-life keeps ample liveness margin -- a missed renewal is
// still caught, because once the lease actually expires the check guards in
// renew and ensure route to recovery -- while cutting idle renewal writes to
// ~2 per lease duration. During active consumption the caller renews every
// page instead, to persist cursor progress.
//
// Staleness comes from remaining, so the half-life is measured by whichever
// clock says less is left. A suspend that freezes the monotonic clock
// therefore still reports the lease stale on the first tick after the wake.
func (l *runtimeLease) renewIfStale(ctx context.Context) error {
	if l.remaining() > l.duration/2 {
		return nil
	}
	return l.renew(ctx)
}

// renew renews the durable lease, or recovers it when it has already lapsed.
//
// ctx must not carry the lease deadline. Recovery runs precisely when
// that deadline has passed, so a context derived from it would be dead on
// arrival and no recovery could ever complete. renew applies the lease
// deadline to the renewal round-trip itself, which is the call that limit
// belongs to: a fenced-out Hub must stop writing at its deadline, while a
// merely-slow store is kept alive by renewalLoop meanwhile.
func (l *runtimeLease) renew(ctx context.Context) error {
	if l.isClosed() {
		// Close has begun teardown and is about to release (or has released) the
		// durable lease; a renewal now would race that DELETE and could re-create
		// the row after release, orphaning it for its TTL. Close sets `closed`
		// before calling releaseSeededLease, so this check lets an in-flight sweep
		// unwind without ever re-acquiring the lease.
		return ErrClosed
	}
	if err := l.check(); err != nil {
		// The lease already lapsed, so the renewal statement -- which requires a
		// live row -- cannot match. Recovery re-takes the lease and writes this
		// same cursor, which is exactly what the renewal would have done.
		return l.recover(ctx, err)
	}
	leaseDuration := l.duration
	leaseStartedAt := time.Now()
	renewCtx, cancelRenew := context.WithDeadline(ctx, l.deadline())
	advanced, err := l.events().RenewHubRuntimeLease(renewCtx, store.RenewHubRuntimeLeaseParams{
		HolderID:      l.holderID,
		CursorSeq:     l.lastSeq,
		LeaseDuration: leaseDuration,
	})
	cancelRenew()
	if err != nil {
		return l.handleStoreError(ctx, err)
	}
	if !advanced {
		// The durable row is gone or no longer live even though the local
		// deadline says otherwise -- the divergence a suspend produces, and the
		// one the wall-clock guard in remaining cannot catch when the
		// database's clock is simply ahead of ours. Recovery decides whether this
		// is a lapse to repair or a takeover to fence on.
		return l.recover(ctx, fmt.Errorf("holder %s was removed, replaced, or expired", l.holderID))
	}
	leaseExpiresAt, exceeded := leaseBudgetExpiry(leaseStartedAt, leaseDuration)
	if exceeded {
		err := fmt.Errorf("%w: holder %s renewal exceeded local lease budget", ErrLeaseLost, l.holderID)
		l.signalFatal(err)
		return err
	}
	l.leaseExpiresAt = leaseExpiresAt
	return nil
}

// recover re-takes a lease that lapsed while this process could not renew it,
// and returns nil once the lease is held again and its cursor is proven
// replayable.
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
func (l *runtimeLease) recover(ctx context.Context, cause error) error {
	if l.isClosed() {
		return ErrClosed
	}
	leaseDuration := l.duration
	// Read both clocks BEFORE the reacquisition overwrites the deadline: these
	// two numbers are what let a reader tell a suspend from a takeover, and they
	// are gone once the new deadline lands.
	monotonicLeft, wallLeft := l.clockReadings()
	leaseStartedAt := time.Now()
	// Limited by one lease duration, and NOT by the lapsed deadline: a lease
	// that cannot be re-taken within its own duration would be expired on
	// arrival anyway. ctx still cancels it, so Close (which cancels the loop and
	// the operation context before waiting) is not delayed by a slow store.
	reacquireCtx, cancelReacquire := context.WithTimeout(ctx, leaseDuration)
	err := l.events().ReacquireHubRuntimeLease(reacquireCtx, store.ReacquireHubRuntimeLeaseParams{
		HolderID:      l.holderID,
		CursorSeq:     l.lastSeq,
		LeaseDuration: leaseDuration,
	})
	cancelReacquire()
	if errors.Is(err, store.ErrHubAlreadyRunning) {
		fatal := fmt.Errorf("%w: holder %s cannot reacquire the lease, another Hub holds it: %w", ErrLeaseLost, l.holderID, cause)
		// Logged here as well as signaled: the server folds this into an aggregate
		// error that surfaces only after the whole teardown, so without this line
		// the log shows the Hub stopping with no cause at the moment it decided to.
		slog.Error("revocation watcher: another Hub holds the runtime lease; this Hub must stop",
			"holder", l.holderID, "cursor", l.lastSeq,
			"monotonic_remaining", monotonicLeft, "wall_remaining", wallLeft, "cause", cause)
		l.signalFatal(fatal)
		return fatal
	}
	if err != nil {
		// Transient: the store refused this attempt, but the local deadline still
		// says lapsed, so the next sweep or heartbeat tries again. Deliberately
		// does NOT wrap cause -- an ErrLeaseLost in the returned chain would make
		// errorsIsLeaseFatal stop the watcher for a store blip.
		slog.Warn("revocation watcher: could not reacquire a lapsed lease, will retry",
			"error", err, "cause", cause, "holder", l.holderID)
		return fmt.Errorf("reacquire Hub runtime lease: %w", err)
	}
	if l.isClosed() {
		// Close raced this reacquisition and has already run (or is running) its
		// release, so the row just written would outlive the Watcher. Remove it,
		// exactly as SeedCursor does for the same race.
		return l.releaseAfterConcurrentClose(ctx, ErrClosed)
	}
	leaseExpiresAt, exceeded := leaseBudgetExpiry(leaseStartedAt, leaseDuration)
	if exceeded {
		fatal := fmt.Errorf("%w: holder %s reacquisition exceeded local lease budget", ErrLeaseLost, l.holderID)
		l.signalFatal(fatal)
		return fatal
	}
	l.leaseExpiresAt = leaseExpiresAt
	// The reacquisition kept the cursor, so nothing published during the lapse is
	// skipped -- but only a rival that ran DURING the lapse could have compacted
	// past it, and the reacquisition cannot see one that already released. Prove
	// the cursor before any event is applied.
	l.cursorUnverified = true
	// Both clock readings, not just the minimum: monotonic_remaining still
	// positive beside a deeply negative wall_remaining is the signature of a
	// suspended process, and their difference is roughly how long it was frozen.
	slog.Warn("revocation watcher: reacquired a lease that lapsed while this process could not renew",
		"holder", l.holderID, "cursor", l.lastSeq,
		"monotonic_remaining", monotonicLeft, "wall_remaining", wallLeft, "cause", cause)
	return l.verifyCursor(ctx)
}

// verifyCursor proves that every revocation this watcher has not yet applied is
// still in the stream, and clears cursorUnverified when it is.
//
// Sequence numbers are gapless (publishPending assigns them contiguously), and
// compaction deletes from the oldest, so the first surviving event above the
// cursor is cursor+1 unless something deleted the events in between. A hole
// there means a second Hub consumed and compacted this stream while the lease
// was lapsed, so this process would silently never apply those revocations --
// the one outcome worse than fencing. Fence.
//
// This runs after the lease is held again, and a live lease's cursor limits
// compaction to rows at or below it, so no rival can open a hole between the
// two reads. Caller holds l.mu.
func (l *runtimeLease) verifyCursor(ctx context.Context) error {
	cursor := l.lastSeq
	var maxSeq int64
	err := l.runStoreUnlocked(ctx, func(ctx context.Context) error {
		var e error
		maxSeq, e = l.events().MaxPublishedSeq(ctx)
		return e
	})
	if err != nil {
		return fmt.Errorf("read the revocation sequence to verify cursor %d: %w", cursor, err)
	}
	if maxSeq <= cursor {
		// Nothing was published past the cursor, so there is nothing to replay.
		l.cursorUnverified = false
		return nil
	}
	var events []store.PublishedRevocationEvent
	err = l.runStoreUnlocked(ctx, func(ctx context.Context) error {
		var e error
		events, e = l.events().ListPublishedAfter(ctx, cursor, 1)
		return e
	})
	if err != nil {
		return fmt.Errorf("read the revocation stream to verify cursor %d: %w", cursor, err)
	}
	if len(events) == 0 || events[0].Seq != cursor+1 {
		fatal := fmt.Errorf(
			"%w: holder %s cannot replay from seq %d; another Hub consumed and compacted this stream",
			ErrLeaseLost, l.holderID, cursor)
		l.signalFatal(fatal)
		return fatal
	}
	l.cursorUnverified = false
	return nil
}

// ensure returns nil while this watcher may safely apply events: it holds a
// live lease, and the cursor it holds is still replayable. It is the single
// check every consume path passes, so neither condition can be forgotten at a
// call site. Caller holds l.mu.
func (l *runtimeLease) ensure(ctx context.Context) error {
	if err := l.check(); err != nil {
		if recErr := l.recover(ctx, err); recErr != nil {
			return recErr
		}
		return l.checkAfterProof(ctx)
	}
	if !l.cursorUnverified {
		return nil
	}
	if err := l.verifyCursor(ctx); err != nil {
		return err
	}
	return l.checkAfterProof(ctx)
}

// checkAfterProof re-reads the clocks after a store round-trip that released
// l.mu. A proof or reacquisition that outlasted the new lease must not open the
// consume path on a row this process no longer holds.
func (l *runtimeLease) checkAfterProof(ctx context.Context) error {
	if err := l.check(); err != nil {
		return l.recover(ctx, err)
	}
	return nil
}

// remaining is how much of the lease is left by the SOONER of the two clocks
// that judge it.
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
// controls the write.
// Caller holds l.mu.
func (l *runtimeLease) remaining() time.Duration {
	monotonic, wall := l.clockReadings()
	return min(monotonic, wall)
}

// deadline is the time.Time to hand context.WithDeadline so the wait matches
// remaining. WithDeadline uses the monotonic reading when the Time has one, so
// passing leaseExpiresAt itself ignores a wall lapse. now.Add(remaining)
// builds a deadline min(monotonic, wall) away.
func (l *runtimeLease) deadline() time.Time {
	left := l.remaining()
	now := time.Now()
	if left <= 0 {
		return now.Add(-time.Nanosecond)
	}
	return now.Add(left)
}

// clockReadings reports the lease remainder as each clock sees it.
//
// The PAIR is the suspend signature, and it is the reason both readings are
// logged rather than only the minimum: a monotonic remainder that is still
// comfortably positive beside a wall remainder deep in the negative means the
// process did not run for the difference between them. No other condition
// produces that shape, so one log line separates "this laptop slept" from "a
// second Hub took the lease" without the reader having to infer it.
// Caller holds l.mu.
func (l *runtimeLease) clockReadings() (monotonic, wall time.Duration) {
	now := time.Now()
	return l.leaseExpiresAt.Sub(now), l.leaseExpiresAt.Round(0).Sub(now.Round(0))
}

func (l *runtimeLease) check() error {
	if remaining := l.remaining(); remaining <= 0 {
		// Round(0) drops the monotonic suffix, so the printed deadline is the
		// wall time the database also judged this lease by.
		return fmt.Errorf("%w: holder %s expired at %s, %s ago",
			ErrLeaseLost, l.holderID, l.leaseExpiresAt.Round(0), -remaining)
	}
	return nil
}

// handleStoreError reports the real cause of a failed store round-trip. A
// call that failed while the lease had lapsed usually failed BECAUSE of the
// lapse (its context carried the lease deadline), so repair the lease first and
// report the store error only if the lease is sound. Caller holds l.mu.
func (l *runtimeLease) handleStoreError(ctx context.Context, err error) error {
	if leaseErr := l.ensure(ctx); leaseErr != nil {
		return leaseErr
	}
	return err
}

// releaseAfterConcurrentClose removes a lease row that landed after Close set
// `closed`, so the row cannot outlive the Watcher and fence the next launch.
func (l *runtimeLease) releaseAfterConcurrentClose(ctx context.Context, primary error) error {
	releaseErr := l.release(ctx)
	return errors.Join(primary, errwrap.Wrap(releaseErr, "release Hub runtime lease after concurrent close"))
}

// release deliberately outlives an exhausted caller context, while retaining a
// fixed timeout so a broken store cannot hang shutdown.
func (l *runtimeLease) release(ctx context.Context) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseReleaseTimeout)
	defer cancel()
	_, err := l.events().ReleaseHubRuntimeLease(releaseCtx, l.holderID)
	return err
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
	now := time.Now()
	left := min(expiresAt.Sub(now), expiresAt.Round(0).Sub(now.Round(0)))
	return expiresAt, left <= 0
}

// leaseReleaseContext limits releaseSeededLease's wait for the lease-state lock.
// Like release it is detached from the caller's cancellation -- the release
// must still run when Close's ctx is already cancelled, which is precisely when
// it is needed -- but unlike it, a still-live caller deadline caps the wait, so
// acquiring the lock cannot consume more than the caller budgeted. An
// ALREADY-expired ctx (whose remaining time would be negative) is excluded
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
