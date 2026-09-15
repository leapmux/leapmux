package postgres

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type lifecycleOutboxStore struct {
	conn *pgConn
}

var _ store.LifecycleOutboxStore = (*lifecycleOutboxStore)(nil)

func (s *lifecycleOutboxStore) Insert(ctx context.Context, p store.InsertLifecycleOutboxParams) error {
	return mapErr(s.conn.q.InsertLifecycleOutbox(ctx, gendb.InsertLifecycleOutboxParams{
		UserID:  p.UserID.String(),
		OpType:  p.OpType,
		Payload: p.Payload,
	}))
}

func (s *lifecycleOutboxStore) ListPending(ctx context.Context, p store.ListPendingLifecycleOutboxParams) ([]store.LifecycleOutboxRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListPendingLifecycleOutbox(ctx, gendb.ListPendingLifecycleOutboxParams{
		UserID: owner,
		Limit:  p.Limit,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.LifecycleOutboxRow, len(rows))
	for i, r := range rows {
		out[i] = store.LifecycleOutboxRow{
			ID:         r.ID,
			UserID:     r.UserID,
			OpType:     r.OpType,
			Payload:    r.Payload,
			EnqueuedAt: r.EnqueuedAt.Time,
			ConsumedAt: r.ConsumedAt.Ptr(),
		}
	}
	return out, nil
}

func (s *lifecycleOutboxStore) MarkConsumed(ctx context.Context, p store.MarkLifecycleOutboxConsumedParams) error {
	return mapErr(s.conn.q.MarkLifecycleOutboxConsumed(ctx, gendb.MarkLifecycleOutboxConsumedParams{
		ID:         p.ID,
		ConsumedAt: pgtime.NullOf(p.ConsumedAt),
	}))
}

func (s *lifecycleOutboxStore) DeleteConsumedBefore(ctx context.Context, before time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteConsumedLifecycleOutboxBefore(ctx, pgtime.NullOf(before)))
}
