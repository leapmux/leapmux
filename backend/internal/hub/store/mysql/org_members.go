package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

// orgMemberStore implements store.OrgMemberStore backed by MySQL.
type orgMemberStore struct{ conn *mysqlConn }

var _ store.OrgMemberStore = (*orgMemberStore)(nil)

func (s *orgMemberStore) Create(ctx context.Context, p store.CreateOrgMemberParams) error {
	return mapErr(s.conn.q.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  p.OrgID,
		UserID: p.UserID,
		Role:   p.Role,
	}))
}

func (s *orgMemberStore) GetByOrgAndUser(ctx context.Context, orgID, userID string) (*store.OrgMember, error) {
	row, err := s.conn.q.GetOrgMember(ctx, gendb.GetOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOrgMember(row), nil
}

func (s *orgMemberStore) ListByOrgID(ctx context.Context, orgID string) ([]store.OrgMemberWithUser, error) {
	rows, err := s.conn.q.ListOrgMembersByOrgID(ctx, orgID)
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
	rows, err := s.conn.q.ListOrgsByUserID(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, func(r gendb.ListOrgsByUserIDRow) store.Org {
		return store.Org{
			ID:         r.ID,
			Name:       r.Name,
			IsPersonal: r.IsPersonal,
			CreatedAt:  r.CreatedAt,
			DeletedAt:  ptrconv.NullTimeToPtr(r.DeletedAt),
		}
	}), nil
}

func (s *orgMemberStore) UpdateRole(ctx context.Context, p store.UpdateOrgMemberRoleParams) error {
	return mapErr(s.conn.q.UpdateOrgMemberRole(ctx, gendb.UpdateOrgMemberRoleParams{
		Role:   p.Role,
		OrgID:  p.OrgID,
		UserID: p.UserID,
	}))
}

func (s *orgMemberStore) Delete(ctx context.Context, p store.DeleteOrgMemberParams) error {
	return mapErr(s.conn.q.DeleteOrgMember(ctx, gendb.DeleteOrgMemberParams{
		OrgID:  p.OrgID,
		UserID: p.UserID,
	}))
}

func (s *orgMemberStore) CountByRole(ctx context.Context, p store.CountOrgMembersByRoleParams) (int64, error) {
	n, err := s.conn.q.CountOrgMembersByRole(ctx, gendb.CountOrgMembersByRoleParams{
		OrgID: p.OrgID,
		Role:  p.Role,
	})
	return n, mapErr(err)
}

func (s *orgMemberStore) IsMember(ctx context.Context, p store.IsOrgMemberParams) (bool, error) {
	ok, err := s.conn.q.IsOrgMember(ctx, gendb.IsOrgMemberParams{
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
