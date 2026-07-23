package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type workspaceSectionItemStore struct {
	conn *mysqlConn
}

var _ store.WorkspaceSectionItemStore = (*workspaceSectionItemStore)(nil)

func (s *workspaceSectionItemStore) Set(ctx context.Context, p store.SetWorkspaceSectionItemParams) error {
	return mapErr(s.conn.q.SetWorkspaceSectionItem(ctx, gendb.SetWorkspaceSectionItemParams{
		UserID:      p.UserID.String(),
		WorkspaceID: p.WorkspaceID,
		SectionID:   p.SectionID,
		Position:    p.Position,
	}))
}

func (s *workspaceSectionItemStore) Get(ctx context.Context, p store.GetWorkspaceSectionItemParams) (*store.WorkspaceSectionItem, error) {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return nil, store.ErrNotFound
	}
	item, err := s.conn.q.GetWorkspaceSectionItem(ctx, gendb.GetWorkspaceSectionItemParams{
		UserID:      owner,
		WorkspaceID: p.WorkspaceID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.WorkspaceSectionItem{
		UserID:      item.UserID,
		WorkspaceID: item.WorkspaceID,
		SectionID:   item.SectionID,
		Position:    item.Position,
	}, nil
}

func (s *workspaceSectionItemStore) ListByUser(ctx context.Context, userID userid.UserID) ([]store.WorkspaceSectionItem, error) {
	owner, ok := store.OwnerFilter(userID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListWorkspaceSectionItemsByUser(ctx, owner)
	if err != nil {
		return nil, mapErr(err)
	}
	result := make([]store.WorkspaceSectionItem, len(rows))
	for i, item := range rows {
		result[i] = store.WorkspaceSectionItem{
			UserID:      item.UserID,
			WorkspaceID: item.WorkspaceID,
			SectionID:   item.SectionID,
			Position:    item.Position,
		}
	}
	return result, nil
}

func (s *workspaceSectionItemStore) Delete(ctx context.Context, p store.DeleteWorkspaceSectionItemParams) error {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// store.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteWorkspaceSectionItem(ctx, gendb.DeleteWorkspaceSectionItemParams{
		UserID:      owner,
		WorkspaceID: p.WorkspaceID,
	}))
}

func (s *workspaceSectionItemStore) DeleteBySection(ctx context.Context, sectionID string) error {
	return mapErr(s.conn.q.DeleteWorkspaceSectionItemsBySection(ctx, sectionID))
}

func (s *workspaceSectionItemStore) HasItemsBySection(ctx context.Context, sectionID string) (bool, error) {
	ok, err := s.conn.q.HasWorkspaceSectionItemsBySection(ctx, sectionID)
	return ok, mapErr(err)
}

func (s *workspaceSectionItemStore) IsInArchivedSection(ctx context.Context, p store.IsWorkspaceInArchivedSectionParams) (bool, error) {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return false, nil
	}
	ok, err := s.conn.q.IsWorkspaceInArchivedSection(ctx, gendb.IsWorkspaceInArchivedSectionParams{
		UserID:      owner,
		WorkspaceID: p.WorkspaceID,
	})
	return ok, mapErr(err)
}
