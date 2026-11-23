package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type userOpBatchesStore struct {
	conn *pgConn
}

var _ store.UserOpBatchesStore = (*userOpBatchesStore)(nil)

func (s *userOpBatchesStore) Insert(ctx context.Context, p store.InsertUserOpBatchParams) error {
	userID := p.UserID.String()
	batchPayload, err := s.conn.ks.SealBytes(p.BatchPayload, sqlutil.UserOpBatchPayloadAAD(userID, p.PhysicalMs, p.Logical))
	if err != nil {
		return err
	}
	transitionsPayload, err := s.conn.ks.SealBytes(p.TransitionsPayload, sqlutil.UserOpBatchTransitionsAAD(userID, p.PhysicalMs, p.Logical))
	if err != nil {
		return err
	}
	return mapErr(s.conn.q.InsertUserOpBatch(ctx, gendb.InsertUserOpBatchParams{
		UserID:             userID,
		PhysicalMs:         p.PhysicalMs,
		Logical:            p.Logical,
		BatchID:            p.BatchID,
		BatchPayload:       batchPayload,
		TransitionsPayload: transitionsPayload,
		OpCount:            int32(p.OpCount),
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
		UserID:          owner,
		AfterPhysicalMs: p.AfterPhysicalMs,
		AfterLogical:    p.AfterLogical,
		RowLimit:        p.Limit,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSliceErr(rows, s.conn.toUserOpBatchRow)
}

func (c *pgConn) toUserOpBatchRow(r gendb.UserOpBatch) (store.UserOpBatchRow, error) {
	batchPayload, err := c.ks.OpenBytes(r.BatchPayload, sqlutil.UserOpBatchPayloadAAD(r.UserID, r.PhysicalMs, r.Logical))
	if err != nil {
		return store.UserOpBatchRow{}, err
	}
	transitionsPayload, err := c.ks.OpenBytes(r.TransitionsPayload, sqlutil.UserOpBatchTransitionsAAD(r.UserID, r.PhysicalMs, r.Logical))
	if err != nil {
		return store.UserOpBatchRow{}, err
	}
	return store.UserOpBatchRow{
		UserID:             r.UserID,
		PhysicalMs:         r.PhysicalMs,
		Logical:            r.Logical,
		BatchID:            r.BatchID,
		BatchPayload:       batchPayload,
		TransitionsPayload: transitionsPayload,
		OpCount:            int64(r.OpCount),
	}, nil
}

func (s *userOpBatchesStore) DeleteThrough(ctx context.Context, p store.DeleteUserOpBatchesThroughParams) error {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil
	}
	return mapErr(s.conn.q.DeleteUserOpBatchesThrough(ctx, gendb.DeleteUserOpBatchesThroughParams{
		UserID:            owner,
		ThroughPhysicalMs: p.ThroughPhysicalMs,
		ThroughLogical:    p.ThroughLogical,
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
