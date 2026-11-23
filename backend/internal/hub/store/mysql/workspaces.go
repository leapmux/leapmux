package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type workspaceStore struct {
	conn *mysqlConn
}

var _ store.WorkspaceStore = (*workspaceStore)(nil)

func (c *mysqlConn) fromDBWorkspace(w gendb.Workspace) (store.Workspace, error) {
	title, err := c.ks.OpenString(w.Title, keystore.WorkspaceTitleAAD(w.ID))
	if err != nil {
		return store.Workspace{}, err
	}
	return store.Workspace{
		ID:          w.ID,
		OwnerUserID: w.OwnerUserID,
		Title:       title,
		CreatedAt:   w.CreatedAt.Time,
		DeletedAt:   w.DeletedAt.Ptr(),
	}, nil
}

func (s *workspaceStore) Create(ctx context.Context, p store.CreateWorkspaceParams) error {
	title, err := s.conn.ks.SealString(p.Title, keystore.WorkspaceTitleAAD(p.ID))
	if err != nil {
		return err
	}
	return s.conn.queryError(s.conn.q.CreateWorkspace(ctx, gendb.CreateWorkspaceParams{
		ID:          p.ID,
		OwnerUserID: p.OwnerUserID.String(),
		Title:       title,
	}))
}

func (s *workspaceStore) GetByID(ctx context.Context, id string) (*store.Workspace, error) {
	w, err := s.conn.q.GetWorkspaceByID(ctx, id)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out, err := s.conn.fromDBWorkspace(w)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *workspaceStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Workspace, error) {
	w, err := s.conn.q.GetWorkspaceByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out, err := s.conn.fromDBWorkspace(w)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *workspaceStore) ListByIDs(ctx context.Context, ids []string) ([]store.Workspace, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.conn.q.ListWorkspacesByIDs(ctx, ids)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return store.MapSliceErr(rows, s.conn.fromDBWorkspace)
}

func (s *workspaceStore) ListAccessible(ctx context.Context, p store.ListAccessibleWorkspacesParams) ([]store.Workspace, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListAccessibleWorkspaces(ctx, owner)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return store.MapSliceErr(rows, s.conn.fromDBWorkspace)
}

func (s *workspaceStore) Rename(ctx context.Context, p store.RenameWorkspaceParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.OwnerUserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	title, err := s.conn.ks.SealString(p.Title, keystore.WorkspaceTitleAAD(p.ID))
	if err != nil {
		return 0, err
	}
	return s.conn.rowsAffected(s.conn.q.RenameWorkspace(ctx, gendb.RenameWorkspaceParams{
		Title:       title,
		ID:          p.ID,
		OwnerUserID: owner,
	}))
}

func (s *workspaceStore) SoftDelete(ctx context.Context, p store.SoftDeleteWorkspaceParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.OwnerUserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	return s.conn.rowsAffected(s.conn.q.SoftDeleteWorkspace(ctx, gendb.SoftDeleteWorkspaceParams{
		ID:          p.ID,
		OwnerUserID: owner,
	}))
}

func (s *workspaceStore) SoftDeleteAllByUser(ctx context.Context, ownerUserID userid.UserID) error {
	owner, ok := userid.OwnerFilter(ownerUserID)
	if !ok {
		// An unminted caller names no user, so a bulk mutation must refuse
		// rather than address every blank-owner row -- or report success
		// having changed nothing. See userid.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return s.conn.queryError(s.conn.q.SoftDeleteAllWorkspacesByUser(ctx, owner))
}
