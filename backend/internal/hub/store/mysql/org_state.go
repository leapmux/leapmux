package mysql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type orgStateStore struct {
	conn *mysqlConn
}

var _ store.OrgStateStore = (*orgStateStore)(nil)

func (s *orgStateStore) Get(ctx context.Context, orgID string) (*store.OrgStateRow, error) {
	row, err := s.conn.q.GetOrgState(ctx, orgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
		EpochStartedAt: sqltime.NewMySQLTime(p.EpochStartedAt),
		UpdatedAt:      sqltime.NewMySQLTime(p.UpdatedAt),
	}))
}

func (s *orgStateStore) AdvanceEpoch(ctx context.Context, p store.AdvanceOrgEpochParams) error {
	return mapErr(s.conn.q.AdvanceOrgEpoch(ctx, gendb.AdvanceOrgEpochParams{
		OrgID:          p.OrgID,
		Epoch:          p.Epoch,
		EpochStartedAt: sqltime.NewMySQLTime(p.EpochStartedAt),
		UpdatedAt:      sqltime.NewMySQLTime(p.UpdatedAt),
	}))
}
