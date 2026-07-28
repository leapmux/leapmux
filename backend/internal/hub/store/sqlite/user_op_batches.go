package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type userOpBatchesStore struct {
	conn *sqliteConn
}

var _ store.UserOpBatchesStore = (*userOpBatchesStore)(nil)

func (s *userOpBatchesStore) Insert(ctx context.Context, p store.InsertUserOpBatchParams) error {
	return mapErr(s.conn.q.InsertUserOpBatch(ctx, gendb.InsertUserOpBatchParams{
		UserID:       p.UserID.String(),
		PhysicalMs:   p.PhysicalMs,
		Logical:      p.Logical,
		LastLogical:  p.LastLogical,
		OriginClient: p.OriginClient,
		PrincipalID:  p.PrincipalID,
		BatchID:      p.BatchID,
		BodyHash:     p.BodyHash,
		BatchPayload: p.BatchPayload,
		OpCount:      p.OpCount,
		Epoch:        p.Epoch,
	}))
}

func (s *userOpBatchesStore) ListAfter(ctx context.Context, p store.ListUserOpBatchesAfterParams) ([]store.UserOpBatchRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListUserOpBatchesAfter(ctx, gendb.ListUserOpBatchesAfterParams{
		UserID:            owner,
		AfterPhysicalMs:   p.AfterPhysicalMs,
		AfterLogical:      p.AfterLogical,
		AfterOriginClient: p.AfterOriginClient,
		RowLimit:          int64(p.Limit),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.UserOpBatchRow, len(rows))
	for i, r := range rows {
		out[i] = toUserOpBatchRow(r)
	}
	return out, nil
}

func toUserOpBatchRow(r gendb.UserOpBatch) store.UserOpBatchRow {
	return store.UserOpBatchRow{
		UserID:       r.UserID,
		PhysicalMs:   r.PhysicalMs,
		Logical:      r.Logical,
		LastLogical:  r.LastLogical,
		OriginClient: r.OriginClient,
		PrincipalID:  r.PrincipalID,
		BatchID:      r.BatchID,
		BodyHash:     r.BodyHash,
		BatchPayload: r.BatchPayload,
		OpCount:      r.OpCount,
		Epoch:        r.Epoch,
		CommittedAt:  r.CommittedAt.Time,
	}
}

func (s *userOpBatchesStore) DeleteThrough(ctx context.Context, p store.DeleteUserOpBatchesThroughParams) error {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil
	}
	return mapErr(s.conn.q.DeleteUserOpBatchesThrough(ctx, gendb.DeleteUserOpBatchesThroughParams{
		UserID:              owner,
		ThroughPhysicalMs:   p.ThroughPhysicalMs,
		ThroughLogical:      p.ThroughLogical,
		ThroughOriginClient: p.ThroughOriginClient,
	}))
}

func (s *userOpBatchesStore) Count(ctx context.Context, userID userid.UserID) (int64, error) {
	owner, ok := userid.OwnerFilter(userID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	n, err := s.conn.q.CountUserOpBatches(ctx, owner)
	return n, mapErr(err)
}
