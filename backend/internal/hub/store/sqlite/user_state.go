package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type userStateStore struct {
	conn *sqliteConn
}

var _ store.UserStateStore = (*userStateStore)(nil)

func (s *userStateStore) Get(ctx context.Context, userID userid.UserID) (*store.UserStateRow, error) {
	owner, ok := userid.OwnerFilter(userID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.GetUserState(ctx, owner)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return &store.UserStateRow{
		UserID:         row.UserID,
		StatePayload:   row.StatePayload,
		CurrentEpoch:   row.CurrentEpoch,
		EpochStartedAt: row.EpochStartedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
	}, nil
}

func (s *userStateStore) Upsert(ctx context.Context, p store.UpsertUserStateParams) error {
	return mapErr(s.conn.q.UpsertUserState(ctx, gendb.UpsertUserStateParams{
		UserID:               p.UserID.String(),
		StatePayload:         p.StatePayload,
		CompactionPhysicalMs: p.CompactionPhysicalMs,
		CurrentEpoch:         p.CurrentEpoch,
		EpochStartedAt:       sqltime.NewSQLiteTime(p.EpochStartedAt),
		UpdatedAt:            sqltime.NewSQLiteTime(p.UpdatedAt),
	}))
}

func (s *userStateStore) AdvanceEpoch(ctx context.Context, p store.AdvanceUserEpochParams) error {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil
	}
	return mapErr(s.conn.q.AdvanceUserEpoch(ctx, gendb.AdvanceUserEpochParams{
		UserID:         owner,
		Epoch:          p.Epoch,
		EpochStartedAt: sqltime.NewSQLiteTime(p.EpochStartedAt),
		UpdatedAt:      sqltime.NewSQLiteTime(p.UpdatedAt),
	}))
}
