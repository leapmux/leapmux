package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
)

type workspaceLayoutStore struct {
	q *gendb.Queries
}

var _ store.WorkspaceLayoutStore = (*workspaceLayoutStore)(nil)

func (s *workspaceLayoutStore) Get(ctx context.Context, workspaceID string) (*store.WorkspaceLayout, error) {
	l, err := s.q.GetWorkspaceLayout(ctx, workspaceID)
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
	return mapErr(s.q.UpsertWorkspaceLayout(ctx, gendb.UpsertWorkspaceLayoutParams{
		WorkspaceID: p.WorkspaceID,
		LayoutJson:  p.LayoutJSON,
	}))
}

func (s *workspaceLayoutStore) Delete(ctx context.Context, workspaceID string) error {
	return mapErr(s.q.DeleteWorkspaceLayout(ctx, workspaceID))
}
