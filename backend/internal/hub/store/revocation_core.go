package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// HubRuntimeLeaseMillis validates and converts a Hub runtime lease duration
// to the millisecond unit shared by all SQL backends.
func HubRuntimeLeaseMillis(duration time.Duration) (int64, error) {
	millis := duration.Milliseconds()
	if duration <= 0 || millis <= 0 {
		return 0, fmt.Errorf("hub runtime lease duration must be at least 1ms")
	}
	return millis, nil
}

// RevocationLease is the dialect-neutral input to lease statements.
type RevocationLease struct {
	HolderID    string
	CursorSeq   int64
	LeaseMillis int64
}

// RevocationCoreOps keeps SQL execution and error mapping in each dialect
// while making sequencing and lease transaction boundaries shared behavior.
type RevocationCoreOps[C any] struct {
	InTransaction func(context.Context, func(C) error) error
	// HasPending is a cheap, transaction-free probe for unpublished events. It
	// lets PublishPending skip the write transaction on an idle Hub.
	HasPending         func(context.Context, C) (bool, error)
	LockSequence       func(context.Context, C) (int64, error)
	PublishRows        func(context.Context, C, int32, int64) (int64, error)
	SetSequence        func(context.Context, C, int64) error
	DeleteExpiredLease func(context.Context, C) error
	// DeleteOwnExpiredLease removes an expired lease row that belongs to the
	// given holder, and leaves any other holder's row in place.
	DeleteOwnExpiredLease func(context.Context, C, string) error
	CompactPublished      func(context.Context, C, time.Time) (int64, error)
	InsertLease           func(context.Context, C, RevocationLease) error
	RenewLease            func(context.Context, C, RevocationLease) (int64, error)
	ReleaseLease          func(context.Context, C, string) (int64, error)
}

// RevocationCore coordinates gapless event publication and the singleton Hub
// runtime lease for a backend-specific, statically typed connection.
type RevocationCore[C any] struct {
	conn C
	ops  RevocationCoreOps[C]
}

func NewRevocationCore[C any](conn C, ops RevocationCoreOps[C]) RevocationCore[C] {
	return RevocationCore[C]{conn: conn, ops: ops}
}

func (c RevocationCore[C]) PublishPending(ctx context.Context, limit int32) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	// Probe for unpublished events with a cheap read before opening the publish
	// write transaction, so an idle Hub never takes the writer lock. Only one
	// Hub runs the watcher (fenced by the runtime lease), so an event inserted
	// just after this probe is simply published on the next sweep.
	pending, err := c.ops.HasPending(ctx, c.conn)
	if err != nil {
		return 0, err
	}
	if !pending {
		return 0, nil
	}
	var published int64
	err = c.ops.InTransaction(ctx, func(conn C) error {
		var err error
		published, _, err = c.publishPending(ctx, conn, limit)
		return err
	})
	return published, err
}

func (c RevocationCore[C]) publishPending(ctx context.Context, conn C, limit int32) (int64, int64, error) {
	lastSeq, err := c.ops.LockSequence(ctx, conn)
	if err != nil {
		return 0, 0, err
	}
	if limit <= 0 {
		return 0, lastSeq, nil
	}
	published, err := c.ops.PublishRows(ctx, conn, limit, lastSeq)
	if err != nil {
		return 0, 0, err
	}
	nextSeq := lastSeq + published
	if published == 0 {
		return 0, nextSeq, nil
	}
	if err := c.ops.SetSequence(ctx, conn, nextSeq); err != nil {
		return 0, 0, err
	}
	return published, nextSeq, nil
}

func (c RevocationCore[C]) CompactPublished(ctx context.Context, cutoff time.Time) (int64, error) {
	var deleted int64
	err := c.ops.InTransaction(ctx, func(conn C) error {
		if err := c.ops.DeleteExpiredLease(ctx, conn); err != nil {
			return err
		}
		var err error
		deleted, err = c.ops.CompactPublished(ctx, conn, cutoff.UTC())
		return err
	})
	return deleted, err
}

func (c RevocationCore[C]) AcquireHubRuntimeLease(
	ctx context.Context,
	p AcquireHubRuntimeLeaseParams,
) (int64, error) {
	if p.HolderID == "" {
		return 0, fmt.Errorf("hub runtime lease holder ID is required")
	}
	leaseMillis, err := HubRuntimeLeaseMillis(p.LeaseDuration)
	if err != nil {
		return 0, err
	}
	var fence int64
	err = c.ops.InTransaction(ctx, func(conn C) error {
		_, nextSeq, err := c.publishPending(ctx, conn, p.PublishLimit)
		if err != nil {
			return err
		}
		fence = nextSeq
		if err := c.ops.DeleteExpiredLease(ctx, conn); err != nil {
			return err
		}
		err = c.ops.InsertLease(ctx, conn, RevocationLease{
			HolderID:    p.HolderID,
			CursorSeq:   fence,
			LeaseMillis: leaseMillis,
		})
		if errors.Is(err, ErrConflict) {
			return ErrHubAlreadyRunning
		}
		return err
	})
	return fence, err
}

// ReacquireHubRuntimeLease re-takes a lapsed lease for its previous holder
// without moving the cursor.
//
// It differs from AcquireHubRuntimeLease in the two ways that make it safe to
// call from a Hub that is already serving. It preserves the caller's cursor
// rather than fencing to the head of the stream, so every revocation published
// during the stall is still replayed -- a startup fence is only correct because
// a fresh process holds no cached credential to evict. And it removes ONLY the
// caller's own expired row, so any other holder's row -- live or expired --
// makes the INSERT conflict and reports ErrHubAlreadyRunning. That second Hub
// may have consumed and compacted past this cursor, which no reacquisition can
// undo, so the caller must fence itself instead.
//
// The cursor cannot move backwards: the caller's in-memory cursor only
// advances, and every renewal wrote it, so the value inserted here is at least
// the value the removed row held.
func (c RevocationCore[C]) ReacquireHubRuntimeLease(
	ctx context.Context,
	p ReacquireHubRuntimeLeaseParams,
) error {
	if p.HolderID == "" {
		return fmt.Errorf("hub runtime lease holder ID is required")
	}
	if p.CursorSeq < 0 {
		return fmt.Errorf("hub runtime lease cursor must not be negative")
	}
	leaseMillis, err := HubRuntimeLeaseMillis(p.LeaseDuration)
	if err != nil {
		return err
	}
	return c.ops.InTransaction(ctx, func(conn C) error {
		if err := c.ops.DeleteOwnExpiredLease(ctx, conn, p.HolderID); err != nil {
			return err
		}
		err := c.ops.InsertLease(ctx, conn, RevocationLease{
			HolderID:    p.HolderID,
			CursorSeq:   p.CursorSeq,
			LeaseMillis: leaseMillis,
		})
		if errors.Is(err, ErrConflict) {
			return ErrHubAlreadyRunning
		}
		return err
	})
}

func (c RevocationCore[C]) RenewHubRuntimeLease(
	ctx context.Context,
	p RenewHubRuntimeLeaseParams,
) (bool, error) {
	leaseMillis, err := HubRuntimeLeaseMillis(p.LeaseDuration)
	if err != nil {
		return false, err
	}
	n, err := c.ops.RenewLease(ctx, c.conn, RevocationLease{
		HolderID:    p.HolderID,
		CursorSeq:   p.CursorSeq,
		LeaseMillis: leaseMillis,
	})
	return n == 1, err
}

func (c RevocationCore[C]) ReleaseHubRuntimeLease(ctx context.Context, holderID string) (int64, error) {
	return c.ops.ReleaseLease(ctx, c.conn, holderID)
}
