package postgres

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type lifecycleOutboxStore struct {
	conn *pgConn
}

var _ store.LifecycleOutboxStore = (*lifecycleOutboxStore)(nil)

func (s *lifecycleOutboxStore) Insert(ctx context.Context, p store.InsertLifecycleOutboxParams) error {
	return mapErr(s.conn.q.InsertLifecycleOutbox(ctx, gendb.InsertLifecycleOutboxParams{
		OrgID:   p.OrgID,
		OpType:  p.OpType,
		Payload: p.Payload,
	}))
}

func (s *lifecycleOutboxStore) ListPending(ctx context.Context, p store.ListPendingLifecycleOutboxParams) ([]store.LifecycleOutboxRow, error) {
	rows, err := s.conn.q.ListPendingLifecycleOutbox(ctx, gendb.ListPendingLifecycleOutboxParams{
		OrgID: p.OrgID,
		Limit: p.Limit,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.LifecycleOutboxRow, len(rows))
	for i, r := range rows {
		out[i] = store.LifecycleOutboxRow{
			ID:         r.ID,
			OrgID:      r.OrgID,
			OpType:     r.OpType,
			Payload:    r.Payload,
			EnqueuedAt: tsToTime(r.EnqueuedAt),
			ConsumedAt: tsToTimePtr(r.ConsumedAt),
		}
	}
	return out, nil
}

func (s *lifecycleOutboxStore) MarkConsumed(ctx context.Context, p store.MarkLifecycleOutboxConsumedParams) error {
	return mapErr(s.conn.q.MarkLifecycleOutboxConsumed(ctx, gendb.MarkLifecycleOutboxConsumedParams{
		ID:         p.ID,
		ConsumedAt: timeToTs(p.ConsumedAt),
	}))
}

func (s *lifecycleOutboxStore) DeleteConsumedBefore(ctx context.Context, before time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteConsumedLifecycleOutboxBefore(ctx, timeToTs(before)))
}
