package mysql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type userStateStore struct {
	conn *mysqlConn
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
		return nil, s.conn.queryError(err)
	}
	payload, err := s.conn.ks.OpenBytes(row.StatePayload, sqlutil.UserStatePayloadAAD(row.UserID))
	if err != nil {
		return nil, err
	}
	return &store.UserStateRow{
		UserID:       row.UserID,
		StatePayload: payload,
	}, nil
}

func (s *userStateStore) Upsert(ctx context.Context, p store.UpsertUserStateParams) error {
	owner, ok := userid.OwnerFilter(p.UserID())
	if !ok {
		return store.ErrInvalidArgument
	}
	payload, err := s.conn.ks.SealBytes(p.StatePayload(), sqlutil.UserStatePayloadAAD(owner))
	if err != nil {
		return err
	}
	return s.conn.queryError(s.conn.q.UpsertUserState(ctx, gendb.UpsertUserStateParams{
		UserID:               owner,
		StatePayload:         payload,
		CompactionPhysicalMs: p.CompactionPhysicalMs(),
	}))
}
