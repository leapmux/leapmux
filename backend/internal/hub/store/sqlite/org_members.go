package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// orgMemberStore implements store.OrgMemberStore backed by SQLite.
type orgMemberStore struct{ q *gendb.Queries }

var _ store.OrgMemberStore = (*orgMemberStore)(nil)

func (s *orgMemberStore) Create(ctx context.Context, p store.CreateOrgMemberParams) error {
	return mapErr(s.q.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  p.OrgID,
		UserID: p.UserID,
		Role:   p.Role,
	}))
}

func (s *orgMemberStore) GetByOrgAndUser(ctx context.Context, orgID, userID string) (*store.OrgMember, error) {
	row, err := s.q.GetOrgMember(ctx, gendb.GetOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOrgMember(row), nil
}

func (s *orgMemberStore) ListByOrgID(ctx context.Context, orgID string) ([]store.OrgMemberWithUser, error) {
	rows, err := s.q.ListOrgMembersByOrgID(ctx, orgID)
	if err != nil {
		return nil, mapErr(err)
	}
	result := make([]store.OrgMemberWithUser, len(rows))
	for i, r := range rows {
		result[i] = store.OrgMemberWithUser{
			OrgMember: store.OrgMember{
				OrgID:    r.OrgID,
				UserID:   r.UserID,
				Role:     r.Role,
				JoinedAt: r.JoinedAt,
			},
			Username:    r.Username,
			DisplayName: r.DisplayName,
			Email:       r.Email,
		}
	}
	return result, nil
}

func (s *orgMemberStore) ListOrgsByUserID(ctx context.Context, userID string) ([]store.Org, error) {
	rows, err := s.q.ListOrgsByUserID(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return sqlutil.MapSlice(rows, fromDBOrg), nil
}

func (s *orgMemberStore) UpdateRole(ctx context.Context, p store.UpdateOrgMemberRoleParams) error {
	return mapErr(s.q.UpdateOrgMemberRole(ctx, gendb.UpdateOrgMemberRoleParams{
		Role:   p.Role,
		OrgID:  p.OrgID,
		UserID: p.UserID,
	}))
}

func (s *orgMemberStore) Delete(ctx context.Context, p store.DeleteOrgMemberParams) error {
	return mapErr(s.q.DeleteOrgMember(ctx, gendb.DeleteOrgMemberParams{
		OrgID:  p.OrgID,
		UserID: p.UserID,
	}))
}

func (s *orgMemberStore) CountByRole(ctx context.Context, p store.CountOrgMembersByRoleParams) (int64, error) {
	n, err := s.q.CountOrgMembersByRole(ctx, gendb.CountOrgMembersByRoleParams{
		OrgID: p.OrgID,
		Role:  p.Role,
	})
	return n, mapErr(err)
}

func (s *orgMemberStore) IsMember(ctx context.Context, p store.IsOrgMemberParams) (bool, error) {
	ok, err := s.q.IsOrgMember(ctx, gendb.IsOrgMemberParams{
		OrgID:  p.OrgID,
		UserID: p.UserID,
	})
	return ok, mapErr(err)
}

func fromDBOrgMember(m gendb.OrgMember) *store.OrgMember {
	return &store.OrgMember{
		OrgID:    m.OrgID,
		UserID:   m.UserID,
		Role:     m.Role,
		JoinedAt: m.JoinedAt,
	}
}
