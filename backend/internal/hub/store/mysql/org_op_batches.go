package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

type orgOpBatchesStore struct {
	conn *mysqlConn
}

var _ store.OrgOpBatchesStore = (*orgOpBatchesStore)(nil)

func (s *orgOpBatchesStore) Insert(ctx context.Context, p store.InsertOrgOpBatchParams) error {
	return mapErr(s.conn.q.InsertOrgOpBatch(ctx, gendb.InsertOrgOpBatchParams{
		OrgID:        p.OrgID,
		PhysicalMs:   p.PhysicalMs,
		Logical:      p.Logical,
		LastLogical:  p.LastLogical,
		OriginClient: p.OriginClient,
		PrincipalID:  p.PrincipalID,
		BatchID:      p.BatchID,
		BodyHash:     p.BodyHash,
		BatchPayload: p.BatchPayload,
		OpCount:      int32(p.OpCount),
		Epoch:        p.Epoch,
	}))
}

func (s *orgOpBatchesStore) ListAfter(ctx context.Context, p store.ListOrgOpBatchesAfterParams) ([]store.OrgOpBatchRow, error) {
	rows, err := s.conn.q.ListOrgOpBatchesAfter(ctx, gendb.ListOrgOpBatchesAfterParams{
		OrgID:             p.OrgID,
		AfterPhysicalMs:   p.AfterPhysicalMs,
		AfterLogical:      p.AfterLogical,
		AfterOriginClient: p.AfterOriginClient,
		Limit:             p.Limit,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.OrgOpBatchRow, len(rows))
	for i, r := range rows {
		out[i] = toOrgOpBatchRow(r)
	}
	return out, nil
}

func toOrgOpBatchRow(r gendb.OrgOpBatch) store.OrgOpBatchRow {
	return store.OrgOpBatchRow{
		OrgID:        r.OrgID,
		PhysicalMs:   r.PhysicalMs,
		Logical:      r.Logical,
		LastLogical:  r.LastLogical,
		OriginClient: r.OriginClient,
		PrincipalID:  r.PrincipalID,
		BatchID:      r.BatchID,
		BodyHash:     r.BodyHash,
		BatchPayload: r.BatchPayload,
		OpCount:      int64(r.OpCount),
		Epoch:        r.Epoch,
		CommittedAt:  r.CommittedAt,
	}
}

func (s *orgOpBatchesStore) DeleteThrough(ctx context.Context, p store.DeleteOrgOpBatchesThroughParams) error {
	return mapErr(s.conn.q.DeleteOrgOpBatchesThrough(ctx, gendb.DeleteOrgOpBatchesThroughParams{
		OrgID:               p.OrgID,
		ThroughPhysicalMs:   p.ThroughPhysicalMs,
		ThroughLogical:      p.ThroughLogical,
		ThroughOriginClient: p.ThroughOriginClient,
	}))
}

func (s *orgOpBatchesStore) Count(ctx context.Context, orgID string) (int64, error) {
	n, err := s.conn.q.CountOrgOpBatches(ctx, orgID)
	return n, mapErr(err)
}
