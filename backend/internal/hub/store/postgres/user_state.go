package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type userStateStore struct {
	conn *pgConn
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
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	payload, err := s.conn.ks.OpenBytes(row.StatePayload, keystore.UserStatePayloadAAD(row.UserID))
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
	payload, err := s.conn.ks.SealBytes(p.StatePayload(), keystore.UserStatePayloadAAD(owner))
	if err != nil {
		return err
	}
	return mapErr(s.conn.q.UpsertUserState(ctx, gendb.UpsertUserStateParams{
		UserID:               owner,
		StatePayload:         payload,
		CompactionPhysicalMs: p.CompactionPhysicalMs(),
	}))
}
