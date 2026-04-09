package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

type workspaceLayoutStore struct {
	conn *mysqlConn
}

var _ store.WorkspaceLayoutStore = (*workspaceLayoutStore)(nil)

func (s *workspaceLayoutStore) Get(ctx context.Context, workspaceID string) (*store.WorkspaceLayout, error) {
	l, err := s.conn.q.GetWorkspaceLayout(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.WorkspaceLayout{
		WorkspaceID: l.WorkspaceID,
		LayoutJSON:  l.LayoutJson,
		UpdatedAt:   l.UpdatedAt,
	}, nil
}

func (s *workspaceLayoutStore) Upsert(ctx context.Context, p store.UpsertWorkspaceLayoutParams) error {
	return mapErr(s.conn.q.UpsertWorkspaceLayout(ctx, gendb.UpsertWorkspaceLayoutParams{
		WorkspaceID: p.WorkspaceID,
		LayoutJson:  p.LayoutJSON,
	}))
}

func (s *workspaceLayoutStore) Delete(ctx context.Context, workspaceID string) error {
	return mapErr(s.conn.q.DeleteWorkspaceLayout(ctx, workspaceID))
}
