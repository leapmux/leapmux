package postgres

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type workspaceSectionStore struct {
	conn *pgConn
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
	owner, ok := userid.OwnerFilter(userID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListWorkspaceSectionsByUserID(ctx, owner)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, func(sec gendb.WorkspaceSection) store.WorkspaceSection { return *fromDBWorkspaceSection(sec) }), nil
}

func (s *workspaceSectionStore) Rename(ctx context.Context, p store.RenameWorkspaceSectionParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	return rowsAffected(s.conn.q.RenameWorkspaceSection(ctx, gendb.RenameWorkspaceSectionParams{
		Name:   p.Name,
		ID:     p.ID,
		UserID: owner,
		// A built-in section has a fixed name, so the query matches custom
		// sections only. The type is BOUND from the enum rather than spelled as
		// a literal in the SQL: a renumber then propagates instead of silently
		// changing which sections this matches.
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
	}))
}

func (s *workspaceSectionStore) UpdatePosition(ctx context.Context, p store.UpdateWorkspaceSectionPositionParams) error {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// userid.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.UpdateWorkspaceSectionPosition(ctx, gendb.UpdateWorkspaceSectionPositionParams{
		Position: p.Position,
		ID:       p.ID,
		UserID:   owner,
	}))
}

func (s *workspaceSectionStore) UpdateSidebarPosition(ctx context.Context, p store.UpdateWorkspaceSectionSidebarPositionParams) error {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// userid.OwnerFilter.
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
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	return rowsAffected(s.conn.q.DeleteWorkspaceSection(ctx, gendb.DeleteWorkspaceSectionParams{
		ID:     p.ID,
		UserID: owner,
		// Custom sections only, bound from the enum. This is the dangerous one:
		// were the literal to drift onto a built-in type, delete would start
		// matching the very sections it exists to protect.
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
	}))
}

func (s *workspaceSectionStore) HasDefaultForUser(ctx context.Context, userID userid.UserID) (bool, error) {
	owner, ok := userid.OwnerFilter(userID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return false, nil
	}
	exists, err := s.conn.q.HasDefaultSectionsForUser(ctx, gendb.HasDefaultSectionsForUserParams{
		UserID: owner,
		// "Has any NON-custom section", i.e. has the defaults been seeded.
		// Bound from the enum for the same reason as the two above.
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
	})
	return exists, mapErr(err)
}
