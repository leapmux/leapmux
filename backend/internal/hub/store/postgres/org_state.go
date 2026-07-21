package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
)

type orgStateStore struct {
	conn *pgConn
}

var _ store.OrgStateStore = (*orgStateStore)(nil)

func (s *orgStateStore) Get(ctx context.Context, orgID string) (*store.OrgStateRow, error) {
	row, err := s.conn.q.GetOrgState(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return &store.OrgStateRow{
		OrgID:          row.OrgID,
		StatePayload:   row.StatePayload,
		CurrentEpoch:   row.CurrentEpoch,
		EpochStartedAt: row.EpochStartedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
	}, nil
}

func (s *orgStateStore) Upsert(ctx context.Context, p store.UpsertOrgStateParams) error {
	return mapErr(s.conn.q.UpsertOrgState(ctx, gendb.UpsertOrgStateParams{
		OrgID:          p.OrgID,
		StatePayload:   p.StatePayload,
		CurrentEpoch:   p.CurrentEpoch,
		EpochStartedAt: pgtime.New(p.EpochStartedAt),
		UpdatedAt:      pgtime.New(p.UpdatedAt),
	}))
}

func (s *orgStateStore) AdvanceEpoch(ctx context.Context, p store.AdvanceOrgEpochParams) error {
	return mapErr(s.conn.q.AdvanceOrgEpoch(ctx, gendb.AdvanceOrgEpochParams{
		OrgID:          p.OrgID,
		Epoch:          p.Epoch,
		EpochStartedAt: pgtime.New(p.EpochStartedAt),
		UpdatedAt:      pgtime.New(p.UpdatedAt),
	}))
}
