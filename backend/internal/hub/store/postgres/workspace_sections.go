package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type workspaceSectionStore struct {
	q *gendb.Queries
}

var _ store.WorkspaceSectionStore = (*workspaceSectionStore)(nil)

func fromDBWorkspaceSection(s gendb.WorkspaceSection) *store.WorkspaceSection {
	return &store.WorkspaceSection{
		ID:          s.ID,
		UserID:      s.UserID,
		Name:        s.Name,
		Position:    s.Position,
		SectionType: s.SectionType,
		Sidebar:     s.Sidebar,
		CreatedAt:   tsToTime(s.CreatedAt),
	}
}

func (s *workspaceSectionStore) Create(ctx context.Context, p store.CreateWorkspaceSectionParams) error {
	return mapErr(s.q.CreateWorkspaceSection(ctx, gendb.CreateWorkspaceSectionParams{
		ID:          p.ID,
		UserID:      p.UserID,
		Name:        p.Name,
		Position:    p.Position,
		SectionType: p.SectionType,
		Sidebar:     p.Sidebar,
	}))
}

func (s *workspaceSectionStore) GetByID(ctx context.Context, id string) (*store.WorkspaceSection, error) {
	sec, err := s.q.GetWorkspaceSectionByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkspaceSection(sec), nil
}

func (s *workspaceSectionStore) ListByUserID(ctx context.Context, userID string) ([]store.WorkspaceSection, error) {
	rows, err := s.q.ListWorkspaceSectionsByUserID(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, func(sec gendb.WorkspaceSection) store.WorkspaceSection { return *fromDBWorkspaceSection(sec) }), nil
}

func (s *workspaceSectionStore) Rename(ctx context.Context, p store.RenameWorkspaceSectionParams) (int64, error) {
	return rowsAffected(s.q.RenameWorkspaceSection(ctx, gendb.RenameWorkspaceSectionParams{
		Name:   p.Name,
		ID:     p.ID,
		UserID: p.UserID,
	}))
}

func (s *workspaceSectionStore) UpdatePosition(ctx context.Context, p store.UpdateWorkspaceSectionPositionParams) error {
	return mapErr(s.q.UpdateWorkspaceSectionPosition(ctx, gendb.UpdateWorkspaceSectionPositionParams{
		Position: p.Position,
		ID:       p.ID,
		UserID:   p.UserID,
	}))
}

func (s *workspaceSectionStore) UpdateSidebarPosition(ctx context.Context, p store.UpdateWorkspaceSectionSidebarPositionParams) error {
	return mapErr(s.q.UpdateWorkspaceSectionSidebarPosition(ctx, gendb.UpdateWorkspaceSectionSidebarPositionParams{
		Sidebar:  p.Sidebar,
		Position: p.Position,
		ID:       p.ID,
		UserID:   p.UserID,
	}))
}

func (s *workspaceSectionStore) Delete(ctx context.Context, p store.DeleteWorkspaceSectionParams) (int64, error) {
	return rowsAffected(s.q.DeleteWorkspaceSection(ctx, gendb.DeleteWorkspaceSectionParams{
		ID:     p.ID,
		UserID: p.UserID,
	}))
}

func (s *workspaceSectionStore) HasDefaultForUser(ctx context.Context, userID string) (bool, error) {
	exists, err := s.q.HasDefaultSectionsForUser(ctx, userID)
	return exists, mapErr(err)
}
