package store

import (
	"context"
	"math/rand/v2"
	"time"
)

// A conflict abort is a NORMAL answer from a distributed SQL backend, and the
// client is the part that must handle it.
//
// LeapMux runs the postgres dialect against PostgreSQL, YugabyteDB and
// CockroachDB, and the mysql dialect against MySQL and TiDB. A distributed
// backend resolves a write-write conflict by aborting one of the transactions
// with a retryable error, and every one of them documents that the
// application retries it -- there is no server-side wait that makes the error
// go away. PostgreSQL itself raises the same two SQLSTATEs: 40001 under
// REPEATABLE READ or SERIALIZABLE, and 40P01 whenever two transactions take
// row locks in opposite orders.
//
// Without a retry the store returned that abort to the caller as a failure,
// so two people who changed the same row at the same time got an error one of
// them did not earn.
//
// The POLICY lives here, in the cross-dialect package, and only the
// per-driver question "is this error one of ours" stays in each dialect. The
// two dialects held byte-identical copies of the loop, the backoff and both
// constants, so tuning one left the other on the old numbers with nothing to
// notice -- and a third dialect would have written a third copy.
const (
	// ConflictRetryLimit is the total number of attempts, the first included.
	// Five covers a contended row; a backend that keeps aborting past that is
	// congested rather than merely contended, and a caller that waits longer
	// serves nobody.
	ConflictRetryLimit = 5
	// ConflictRetryBaseDelay doubles per attempt and carries full jitter, so
	// a burst of aborted writers spreads out instead of re-colliding in step.
	// The worst wait before the last attempt is about 80 ms.
	//
	// Exported because each dialect's own wrapper test asserts that a
	// cancelled request returns in less than one backoff step, and a copy of
	// the number in a test is the same drift the policy moved here to stop.
	ConflictRetryBaseDelay = 5 * time.Millisecond
)

// RetryOnConflict runs the statement again while isRetryable keeps reporting
// that the backend aborted it for a conflict it expects the client to retry.
//
// isRetryable is the dialect's own: the two SQLSTATEs for pgx, the two server
// error numbers for MySQL. It is a parameter rather than an interface for the
// same reason RunCredentialMutation takes a withTransaction function value --
// the shared part is the policy, and the driver-specific part is one
// predicate.
//
// It returns the DATABASE error rather than the context error when the wait
// is cut short, because the database error is what the caller has to act on:
// a cancelled retry sleep means the request went away, and reporting
// "context canceled" would hide which statement conflicted.
func RetryOnConflict(ctx context.Context, isRetryable func(error) bool, run func() error) error {
	var err error
	for attempt := range ConflictRetryLimit {
		err = run()
		if err == nil || !isRetryable(err) || attempt == ConflictRetryLimit-1 {
			return err
		}
		if !sleepBeforeConflictRetry(ctx, attempt) {
			return err
		}
	}
	return err
}

// sleepBeforeConflictRetry waits out one backoff step and reports whether it
// finished. Full jitter over an exponential window: two writers that collide
// must not wake together and collide again.
func sleepBeforeConflictRetry(ctx context.Context, attempt int) bool {
	window := ConflictRetryBaseDelay << attempt
	timer := time.NewTimer(time.Duration(rand.Int64N(int64(window)) + 1))
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
