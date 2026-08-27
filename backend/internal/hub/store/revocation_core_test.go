package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubRuntimeLeaseMillis(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     int64
		wantErr  string
	}{
		{name: "one millisecond", duration: time.Millisecond, want: 1},
		{name: "truncates sub-millisecond remainder", duration: 1500 * time.Microsecond, want: 1},
		{name: "zero", wantErr: "hub runtime lease duration must be at least 1ms"},
		{name: "positive but below one millisecond", duration: time.Microsecond, wantErr: "hub runtime lease duration must be at least 1ms"},
		{name: "negative", duration: -time.Second, wantErr: "hub runtime lease duration must be at least 1ms"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HubRuntimeLeaseMillis(tc.duration)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

type revocationCoreTestConn struct {
	sequence     int64
	pending      int64
	lease        RevocationLease
	leasePresent bool
	transactions int
	sequenceSets int
	leaseDeletes int
	compactions  int
}

func newRevocationCoreTestSubject(conn *revocationCoreTestConn) RevocationCore[*revocationCoreTestConn] {
	return NewRevocationCore(conn, RevocationCoreOps[*revocationCoreTestConn]{
		InTransaction: func(_ context.Context, fn func(*revocationCoreTestConn) error) error {
			conn.transactions++
			before := *conn
			if err := fn(conn); err != nil {
				*conn = before
				return err
			}
			return nil
		},
		HasPending:   func(_ context.Context, c *revocationCoreTestConn) (bool, error) { return c.pending > 0, nil },
		LockSequence: func(_ context.Context, c *revocationCoreTestConn) (int64, error) { return c.sequence, nil },
		PublishRows: func(_ context.Context, c *revocationCoreTestConn, limit int32, _ int64) (int64, error) {
			published := min(c.pending, int64(limit))
			c.pending -= published
			return published, nil
		},
		SetSequence: func(_ context.Context, c *revocationCoreTestConn, sequence int64) error {
			c.sequenceSets++
			c.sequence = sequence
			return nil
		},
		DeleteExpiredLease: func(_ context.Context, c *revocationCoreTestConn) error {
			c.leaseDeletes++
			return nil
		},
		CompactPublished: func(_ context.Context, c *revocationCoreTestConn, _ time.Time) (int64, error) {
			c.compactions++
			return 3, nil
		},
		InsertLease: func(_ context.Context, c *revocationCoreTestConn, lease RevocationLease) error {
			if c.leasePresent {
				return ErrConflict
			}
			c.lease = lease
			c.leasePresent = true
			return nil
		},
		RenewLease: func(_ context.Context, c *revocationCoreTestConn, lease RevocationLease) (int64, error) {
			if !c.leasePresent || c.lease.HolderID != lease.HolderID {
				return 0, nil
			}
			c.lease = lease
			return 1, nil
		},
		ReleaseLease: func(_ context.Context, c *revocationCoreTestConn, holderID string) (int64, error) {
			if !c.leasePresent || c.lease.HolderID != holderID {
				return 0, nil
			}
			c.leasePresent = false
			return 1, nil
		},
	})
}

func TestRevocationCoreSkipsWriteTransactionWhenNothingPending(t *testing.T) {
	conn := &revocationCoreTestConn{sequence: 9}
	core := newRevocationCoreTestSubject(conn)

	published, err := core.PublishPending(context.Background(), 10)
	require.NoError(t, err)
	assert.Zero(t, published)
	assert.Zero(t, conn.sequenceSets)
	// The cheap HasPending probe must keep an idle Hub off the writer lock: with
	// nothing pending, PublishPending opens no transaction at all.
	assert.Zero(t, conn.transactions, "idle publish must not open a write transaction")
}

func TestRevocationCorePublishesInsideOneTransactionWhenPending(t *testing.T) {
	conn := &revocationCoreTestConn{sequence: 9, pending: 3}
	core := newRevocationCoreTestSubject(conn)

	published, err := core.PublishPending(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, int64(3), published)
	assert.Equal(t, 1, conn.transactions)
	assert.Equal(t, int64(12), conn.sequence)
}

func TestRevocationCoreCompactsInOneTransaction(t *testing.T) {
	conn := &revocationCoreTestConn{}
	core := newRevocationCoreTestSubject(conn)

	deleted, err := core.CompactPublished(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)
	assert.Equal(t, 1, conn.transactions)
	assert.Equal(t, 1, conn.leaseDeletes)
	assert.Equal(t, 1, conn.compactions)
}

func TestRevocationCorePublishesAndFencesLeaseInOneTransaction(t *testing.T) {
	conn := &revocationCoreTestConn{sequence: 3, pending: 4}
	core := newRevocationCoreTestSubject(conn)

	fence, err := core.AcquireHubRuntimeLease(context.Background(), AcquireHubRuntimeLeaseParams{
		HolderID:      "holder",
		PublishLimit:  2,
		LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5), fence)
	assert.Equal(t, int64(5), conn.sequence)
	assert.Equal(t, RevocationLease{HolderID: "holder", CursorSeq: 5, LeaseMillis: 1000}, conn.lease)

	_, err = core.AcquireHubRuntimeLease(context.Background(), AcquireHubRuntimeLeaseParams{
		HolderID: "other", PublishLimit: 1, LeaseDuration: time.Second,
	})
	assert.True(t, errors.Is(err, ErrHubAlreadyRunning))
	assert.Equal(t, int64(5), conn.sequence, "lease conflict must roll back sequence allocation")
	assert.Equal(t, int64(2), conn.pending, "lease conflict must roll back event publication")

	renewed, err := core.RenewHubRuntimeLease(context.Background(), RenewHubRuntimeLeaseParams{
		HolderID: "holder", CursorSeq: 5, LeaseDuration: 2 * time.Second,
	})
	require.NoError(t, err)
	assert.True(t, renewed)
	assert.Equal(t, int64(2000), conn.lease.LeaseMillis)

	released, err := core.ReleaseHubRuntimeLease(context.Background(), "other")
	require.NoError(t, err)
	assert.Zero(t, released)
	released, err = core.ReleaseHubRuntimeLease(context.Background(), "holder")
	require.NoError(t, err)
	assert.Equal(t, int64(1), released)
	assert.False(t, conn.leasePresent)
}

func TestRevocationCoreReacquireReplacesOwnLiveRow(t *testing.T) {
	conn := &revocationCoreTestConn{leasePresent: true, lease: RevocationLease{HolderID: "holder", CursorSeq: 3, LeaseMillis: 1000}}
	core := newRevocationCoreTestSubject(conn)

	require.NoError(t, core.ReacquireHubRuntimeLease(context.Background(), ReacquireHubRuntimeLeaseParams{
		HolderID: "holder", CursorSeq: 4, LeaseDuration: 2 * time.Second,
	}))
	assert.True(t, conn.leasePresent, "own-row reacquire must put a live row back")
	assert.Equal(t, RevocationLease{HolderID: "holder", CursorSeq: 4, LeaseMillis: 2000}, conn.lease)

	err := core.ReacquireHubRuntimeLease(context.Background(), ReacquireHubRuntimeLeaseParams{
		HolderID: "other", CursorSeq: 0, LeaseDuration: time.Second,
	})
	assert.True(t, errors.Is(err, ErrHubAlreadyRunning))
	assert.True(t, conn.leasePresent, "a rival reacquire must not delete the live row")
	assert.Equal(t, "holder", conn.lease.HolderID, "a rival reacquire must leave the live row in place")
}

func TestRevocationCoreRejectsInvalidLeaseBeforeDatabaseWork(t *testing.T) {
	conn := &revocationCoreTestConn{pending: 1}
	core := newRevocationCoreTestSubject(conn)

	_, err := core.AcquireHubRuntimeLease(context.Background(), AcquireHubRuntimeLeaseParams{LeaseDuration: time.Second})
	require.EqualError(t, err, "hub runtime lease holder ID is required")
	assert.Equal(t, int64(1), conn.pending)

	ok, err := core.RenewHubRuntimeLease(context.Background(), RenewHubRuntimeLeaseParams{
		HolderID: "holder", LeaseDuration: time.Microsecond,
	})
	assert.False(t, ok)
	require.EqualError(t, err, "hub runtime lease duration must be at least 1ms")

	err = core.ReacquireHubRuntimeLease(context.Background(), ReacquireHubRuntimeLeaseParams{
		HolderID: "holder", CursorSeq: -1, LeaseDuration: time.Second,
	})
	require.EqualError(t, err, "hub runtime lease cursor must not be negative")
	assert.Equal(t, int64(1), conn.pending, "invalid reacquire must not open a write transaction")
	assert.Zero(t, conn.transactions)
}

// retryingOps wraps a RevocationCoreOps with an InTransaction that runs the
// callback TWICE, exactly as the postgres and mysql dialects do when the
// backend aborts the first attempt for a retryable conflict. The first attempt
// runs to the end and then "loses its commit"; the second one fails early, so
// any value only the first attempt assigned is still in the captured variable.
func retryingOps(ops RevocationCoreOps[*revocationCoreTestConn], conn *revocationCoreTestConn, attempts *int) RevocationCoreOps[*revocationCoreTestConn] {
	ops.InTransaction = func(_ context.Context, fn func(*revocationCoreTestConn) error) error {
		*attempts++
		before := *conn
		if err := fn(conn); err != nil {
			*conn = before
			return err
		}
		// The first attempt committed nothing: roll it back and run again.
		*conn = before
		*attempts++
		if err := fn(conn); err != nil {
			*conn = before
			return err
		}
		return nil
	}
	return ops
}

// TestRevocationCoreCompactReportsNothingWhenTheRetryFails pins the retry
// contract on CompactPublished: a callback that may run twice must OVERWRITE
// the count it reports, so a failed second attempt cannot report the rows
// the first, rolled-back attempt deleted.
func TestRevocationCoreCompactReportsNothingWhenTheRetryFails(t *testing.T) {
	conn := &revocationCoreTestConn{}
	attempts := 0
	boom := errors.New("lease delete aborted")
	ops := RevocationCoreOps[*revocationCoreTestConn]{
		DeleteExpiredLease: func(_ context.Context, c *revocationCoreTestConn) error {
			c.leaseDeletes++
			// The attempt counter lives outside the conn, which the harness
			// rolls back between attempts, so only the SECOND attempt fails.
			if attempts > 1 {
				return boom
			}
			return nil
		},
		CompactPublished: func(_ context.Context, c *revocationCoreTestConn, _ time.Time) (int64, error) {
			c.compactions++
			return 7, nil
		},
	}
	core := NewRevocationCore(conn, retryingOps(ops, conn, &attempts))

	deleted, err := core.CompactPublished(context.Background(), time.Now())
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 2, attempts, "the harness must have run the callback twice")
	assert.Zero(t, deleted,
		"a failed retry must report 0, not the rows the rolled-back attempt deleted")
}

// TestRevocationCoreAcquireReportsNoFenceWhenTheRetryFails is the same rule on
// the lease cursor. The fence decides which revocation events a fresh Hub
// replays, so a value from a rolled-back attempt would skip events.
func TestRevocationCoreAcquireReportsNoFenceWhenTheRetryFails(t *testing.T) {
	conn := &revocationCoreTestConn{sequence: 12}
	attempts := 0
	boom := errors.New("sequence lock aborted")
	ops := RevocationCoreOps[*revocationCoreTestConn]{
		LockSequence: func(_ context.Context, c *revocationCoreTestConn) (int64, error) {
			// Only the SECOND attempt fails; see the note in the compact twin.
			if attempts > 1 {
				return 0, boom
			}
			return c.sequence, nil
		},
		PublishRows:        func(context.Context, *revocationCoreTestConn, int32, int64) (int64, error) { return 0, nil },
		SetSequence:        func(context.Context, *revocationCoreTestConn, int64) error { return nil },
		DeleteExpiredLease: func(context.Context, *revocationCoreTestConn) error { return nil },
		InsertLease: func(_ context.Context, c *revocationCoreTestConn, lease RevocationLease) error {
			c.lease = lease
			c.leasePresent = true
			return nil
		},
	}
	core := NewRevocationCore(conn, retryingOps(ops, conn, &attempts))

	fence, err := core.AcquireHubRuntimeLease(context.Background(), AcquireHubRuntimeLeaseParams{
		HolderID:      "hub-1",
		PublishLimit:  10,
		LeaseDuration: time.Minute,
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 2, attempts, "the harness must have run the callback twice")
	assert.Zero(t, fence,
		"a failed retry must report no fence, not the cursor the rolled-back attempt computed")
}
