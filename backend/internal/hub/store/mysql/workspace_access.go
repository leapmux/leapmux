package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

type workspaceAccessStore struct {
	conn *mysqlConn
}

var _ store.WorkspaceAccessStore = (*workspaceAccessStore)(nil)

func (s *workspaceAccessStore) Grant(ctx context.Context, p store.GrantWorkspaceAccessParams) error {
	return mapErr(s.conn.q.GrantWorkspaceAccess(ctx, gendb.GrantWorkspaceAccessParams{
		WorkspaceID: p.WorkspaceID,
		UserID:      p.UserID,
	}))
}

func (s *workspaceAccessStore) BulkGrant(ctx context.Context, params []store.GrantWorkspaceAccessParams) error {
	for _, p := range params {
		if err := s.Grant(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func (s *workspaceAccessStore) Revoke(ctx context.Context, p store.RevokeWorkspaceAccessParams) error {
	return mapErr(s.conn.q.RevokeWorkspaceAccess(ctx, gendb.RevokeWorkspaceAccessParams{
		WorkspaceID: p.WorkspaceID,
		UserID:      p.UserID,
	}))
}

func (s *workspaceAccessStore) ListByWorkspaceID(ctx context.Context, workspaceID string) ([]store.WorkspaceAccess, error) {
	rows, err := s.conn.q.ListWorkspaceAccessByWorkspaceID(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	result := make([]store.WorkspaceAccess, len(rows))
	for i, r := range rows {
		result[i] = store.WorkspaceAccess{
			WorkspaceID: r.WorkspaceID,
			UserID:      r.UserID,
			CreatedAt:   r.CreatedAt,
		}
	}
	return result, nil
}

func (s *workspaceAccessStore) HasAccess(ctx context.Context, p store.HasWorkspaceAccessParams) (bool, error) {
	ok, err := s.conn.q.HasWorkspaceAccess(ctx, gendb.HasWorkspaceAccessParams{
		WorkspaceID: p.WorkspaceID,
		UserID:      p.UserID,
	})
	return ok, mapErr(err)
}

func (s *workspaceAccessStore) Clear(ctx context.Context, workspaceID string) error {
	return mapErr(s.conn.q.ClearWorkspaceAccess(ctx, workspaceID))
}
