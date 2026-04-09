package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type workspaceSectionItemStore struct {
	conn *pgConn
}

var _ store.WorkspaceSectionItemStore = (*workspaceSectionItemStore)(nil)

func (s *workspaceSectionItemStore) Set(ctx context.Context, p store.SetWorkspaceSectionItemParams) error {
	return mapErr(s.conn.q.SetWorkspaceSectionItem(ctx, gendb.SetWorkspaceSectionItemParams{
		UserID:      p.UserID,
		WorkspaceID: p.WorkspaceID,
		SectionID:   p.SectionID,
		Position:    p.Position,
	}))
}

func (s *workspaceSectionItemStore) Get(ctx context.Context, p store.GetWorkspaceSectionItemParams) (*store.WorkspaceSectionItem, error) {
	item, err := s.conn.q.GetWorkspaceSectionItem(ctx, gendb.GetWorkspaceSectionItemParams{
		UserID:      p.UserID,
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

func (s *workspaceSectionItemStore) ListByUser(ctx context.Context, userID string) ([]store.WorkspaceSectionItem, error) {
	rows, err := s.conn.q.ListWorkspaceSectionItemsByUser(ctx, userID)
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
	return mapErr(s.conn.q.DeleteWorkspaceSectionItem(ctx, gendb.DeleteWorkspaceSectionItemParams{
		UserID:      p.UserID,
		WorkspaceID: p.WorkspaceID,
	}))
}

func (s *workspaceSectionItemStore) DeleteBySection(ctx context.Context, sectionID string) error {
	return mapErr(s.conn.q.DeleteWorkspaceSectionItemsBySection(ctx, sectionID))
}

func (s *workspaceSectionItemStore) MoveToSection(ctx context.Context, p store.MoveWorkspaceSectionItemsToSectionParams) error {
	return mapErr(s.conn.q.MoveWorkspaceSectionItemsToSection(ctx, gendb.MoveWorkspaceSectionItemsToSectionParams{
		SectionID:   p.ToSectionID,
		SectionID_2: p.FromSectionID,
	}))
}

func (s *workspaceSectionItemStore) HasItemsBySection(ctx context.Context, sectionID string) (bool, error) {
	ok, err := s.conn.q.HasWorkspaceSectionItemsBySection(ctx, sectionID)
	return ok, mapErr(err)
}

func (s *workspaceSectionItemStore) IsInArchivedSection(ctx context.Context, p store.IsWorkspaceInArchivedSectionParams) (bool, error) {
	ok, err := s.conn.q.IsWorkspaceInArchivedSection(ctx, gendb.IsWorkspaceInArchivedSectionParams{
		UserID:      p.UserID,
		WorkspaceID: p.WorkspaceID,
	})
	return ok, mapErr(err)
}
