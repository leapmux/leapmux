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
}
