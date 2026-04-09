package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type workspaceStore struct {
	conn *sqliteConn
}

var _ store.WorkspaceStore = (*workspaceStore)(nil)

func fromDBWorkspace(w gendb.Workspace) *store.Workspace {
	return &store.Workspace{
		ID:          w.ID,
		OrgID:       w.OrgID,
		OwnerUserID: w.OwnerUserID,
		Title:       w.Title,
		IsDeleted:   ptrconv.Int64ToBool(w.IsDeleted),
		CreatedAt:   w.CreatedAt,
		DeletedAt:   ptrconv.NullTimeToPtr(w.DeletedAt),
	}
}

func (s *workspaceStore) Create(ctx context.Context, p store.CreateWorkspaceParams) error {
	return mapErr(s.conn.q.CreateWorkspace(ctx, gendb.CreateWorkspaceParams{
		ID:          p.ID,
		OrgID:       p.OrgID,
		OwnerUserID: p.OwnerUserID,
		Title:       p.Title,
	}))
}

func (s *workspaceStore) GetByID(ctx context.Context, id string) (*store.Workspace, error) {
	w, err := s.conn.q.GetWorkspaceByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkspace(w), nil
}

func (s *workspaceStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Workspace, error) {
	w, err := s.conn.q.GetWorkspaceByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkspace(w), nil
}

func (s *workspaceStore) ListAccessible(ctx context.Context, p store.ListAccessibleWorkspacesParams) ([]store.Workspace, error) {
	rows, err := s.conn.q.ListAccessibleWorkspaces(ctx, gendb.ListAccessibleWorkspacesParams{
		UserID: p.UserID,
		OrgID:  p.OrgID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, func(w gendb.Workspace) store.Workspace { return *fromDBWorkspace(w) }), nil
}

func (s *workspaceStore) Rename(ctx context.Context, p store.RenameWorkspaceParams) (int64, error) {
	return rowsAffected(s.conn.q.RenameWorkspace(ctx, gendb.RenameWorkspaceParams{
		Title:       p.Title,
		ID:          p.ID,
		OwnerUserID: p.OwnerUserID,
	}))
}

func (s *workspaceStore) SoftDelete(ctx context.Context, p store.SoftDeleteWorkspaceParams) (int64, error) {
	return rowsAffected(s.conn.q.SoftDeleteWorkspace(ctx, gendb.SoftDeleteWorkspaceParams{
		ID:          p.ID,
		OwnerUserID: p.OwnerUserID,
	}))
}

func (s *workspaceStore) SoftDeleteAllByUser(ctx context.Context, ownerUserID string) error {
	return mapErr(s.conn.q.SoftDeleteAllWorkspacesByUser(ctx, ownerUserID))
}
