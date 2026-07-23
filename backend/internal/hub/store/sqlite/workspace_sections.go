package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type workspaceSectionStore struct {
	conn *sqliteConn
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
		CreatedAt:   s.CreatedAt.Time,
	}
}

func (s *workspaceSectionStore) Create(ctx context.Context, p store.CreateWorkspaceSectionParams) error {
	return mapErr(s.conn.q.CreateWorkspaceSection(ctx, gendb.CreateWorkspaceSectionParams{
		ID:          p.ID,
		UserID:      p.UserID.String(),
		Name:        p.Name,
		Position:    p.Position,
		SectionType: p.SectionType,
		Sidebar:     p.Sidebar,
	}))
}

func (s *workspaceSectionStore) GetByID(ctx context.Context, id string) (*store.WorkspaceSection, error) {
	sec, err := s.conn.q.GetWorkspaceSectionByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkspaceSection(sec), nil
}

func (s *workspaceSectionStore) ListByUserID(ctx context.Context, userID userid.UserID) ([]store.WorkspaceSection, error) {
	owner, ok := store.OwnerFilter(userID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListWorkspaceSectionsByUserID(ctx, owner)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, func(sec gendb.WorkspaceSection) store.WorkspaceSection { return *fromDBWorkspaceSection(sec) }), nil
}

func (s *workspaceSectionStore) Rename(ctx context.Context, p store.RenameWorkspaceSectionParams) (int64, error) {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return 0, nil
	}
	return rowsAffected(s.conn.q.RenameWorkspaceSection(ctx, gendb.RenameWorkspaceSectionParams{
		Name:   p.Name,
		ID:     p.ID,
		UserID: owner,
	}))
}

func (s *workspaceSectionStore) UpdatePosition(ctx context.Context, p store.UpdateWorkspaceSectionPositionParams) error {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// store.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.UpdateWorkspaceSectionPosition(ctx, gendb.UpdateWorkspaceSectionPositionParams{
		Position: p.Position,
		ID:       p.ID,
		UserID:   owner,
	}))
}

func (s *workspaceSectionStore) UpdateSidebarPosition(ctx context.Context, p store.UpdateWorkspaceSectionSidebarPositionParams) error {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// store.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.UpdateWorkspaceSectionSidebarPosition(ctx, gendb.UpdateWorkspaceSectionSidebarPositionParams{
		Sidebar:  p.Sidebar,
		Position: p.Position,
		ID:       p.ID,
		UserID:   owner,
	}))
}

func (s *workspaceSectionStore) Delete(ctx context.Context, p store.DeleteWorkspaceSectionParams) (int64, error) {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return 0, nil
	}
	return rowsAffected(s.conn.q.DeleteWorkspaceSection(ctx, gendb.DeleteWorkspaceSectionParams{
		ID:     p.ID,
		UserID: owner,
	}))
}

func (s *workspaceSectionStore) HasDefaultForUser(ctx context.Context, userID userid.UserID) (bool, error) {
	owner, ok := store.OwnerFilter(userID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return false, nil
	}
	n, err := s.conn.q.HasDefaultSectionsForUser(ctx, owner)
	return n, mapErr(err)
}
