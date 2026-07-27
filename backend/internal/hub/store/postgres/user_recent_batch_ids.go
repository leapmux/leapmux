package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type userRecentBatchIDStore struct {
	conn *pgConn
}

var _ store.UserRecentBatchIDStore = (*userRecentBatchIDStore)(nil)

func (s *userRecentBatchIDStore) Get(ctx context.Context, userID userid.UserID, batchID string) (*store.UserRecentBatchIDRow, error) {
	owner, ok := userid.OwnerFilter(userID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.GetRecentBatchID(ctx, gendb.GetRecentBatchIDParams{UserID: owner, BatchID: batchID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return &store.UserRecentBatchIDRow{
		UserID:              row.UserID,
		BatchID:             row.BatchID,
		BodyHash:            row.BodyHash,
		PrincipalID:         row.PrincipalID,
		CanonicalPhysicalMs: row.CanonicalPhysicalMs,
		CanonicalLogical:    row.CanonicalLogical,
		CanonicalClient:     row.CanonicalClient,
		OpCount:             int64(row.OpCount),
		Epoch:               row.Epoch,
		ExpiresAt:           row.ExpiresAt.Time,
	}, nil
}

func (s *userRecentBatchIDStore) Insert(ctx context.Context, p store.InsertUserRecentBatchIDParams) error {
	return mapErr(s.conn.q.InsertRecentBatchID(ctx, gendb.InsertRecentBatchIDParams{
		UserID:              p.UserID.String(),
		BatchID:             p.BatchID,
		BodyHash:            p.BodyHash,
		PrincipalID:         p.PrincipalID,
		CanonicalPhysicalMs: p.CanonicalPhysicalMs,
		CanonicalLogical:    p.CanonicalLogical,
		CanonicalClient:     p.CanonicalClient,
		OpCount:             int32(p.OpCount),
		Epoch:               p.Epoch,
		ExpiresAt:           pgtime.New(p.ExpiresAt),
	}))
}

func (s *userRecentBatchIDStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredRecentBatchIDs(ctx, pgtime.New(before)))
}
