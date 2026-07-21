package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type orgRecentBatchIDStore struct {
	conn *sqliteConn
}

var _ store.OrgRecentBatchIDStore = (*orgRecentBatchIDStore)(nil)

func (s *orgRecentBatchIDStore) Get(ctx context.Context, orgID, batchID string) (*store.OrgRecentBatchIDRow, error) {
	row, err := s.conn.q.GetRecentBatchID(ctx, gendb.GetRecentBatchIDParams{OrgID: orgID, BatchID: batchID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return &store.OrgRecentBatchIDRow{
		OrgID:               row.OrgID,
		BatchID:             row.BatchID,
		BodyHash:            row.BodyHash,
		PrincipalID:         row.PrincipalID,
		CanonicalPhysicalMs: row.CanonicalPhysicalMs,
		CanonicalLogical:    row.CanonicalLogical,
		CanonicalClient:     row.CanonicalClient,
		OpCount:             row.OpCount,
		Epoch:               row.Epoch,
		ExpiresAt:           row.ExpiresAt,
	}, nil
}

func (s *orgRecentBatchIDStore) Insert(ctx context.Context, p store.InsertOrgRecentBatchIDParams) error {
	return mapErr(s.conn.q.InsertRecentBatchID(ctx, gendb.InsertRecentBatchIDParams{
		OrgID:               p.OrgID,
		BatchID:             p.BatchID,
		BodyHash:            p.BodyHash,
		PrincipalID:         p.PrincipalID,
		CanonicalPhysicalMs: p.CanonicalPhysicalMs,
		CanonicalLogical:    p.CanonicalLogical,
		CanonicalClient:     p.CanonicalClient,
		OpCount:             p.OpCount,
		Epoch:               p.Epoch,
		ExpiresAt:           sqlutil.BindTime(p.ExpiresAt),
	}))
}

func (s *orgRecentBatchIDStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredRecentBatchIDs(ctx, formatSQLiteTime(before)))
}
