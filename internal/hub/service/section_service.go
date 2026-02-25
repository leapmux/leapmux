package service

import (
	"context"
	"database/sql"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/lexorank"
)

// SectionService implements the SectionServiceHandler interface.
type SectionService struct {
	queries *db.Queries
}

// NewSectionService creates a new SectionService.
func NewSectionService(q *db.Queries) *SectionService {
	return &SectionService{queries: q}
}

func (s *SectionService) ListSections(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListSectionsRequest],
) (*connect.Response[leapmuxv1.ListSectionsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	// Auto-initialize default sections if needed.
	count, err := s.queries.CountDefaultSectionsForUser(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if count == 0 {
		if err := s.initDefaultSections(ctx, user.ID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("init sections: %w", err))
		}
	}

	sections, err := s.queries.ListWorkspaceSectionsByUserID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	items, err := s.queries.ListWorkspaceSectionItemsByUser(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoSections := make([]*leapmuxv1.Section, len(sections))
	for i, sec := range sections {
		protoSections[i] = &leapmuxv1.Section{
			Id:          sec.ID,
			Name:        sec.Name,
			Position:    sec.Position,
			SectionType: sec.SectionType,
			Sidebar:     sec.Sidebar,
		}
	}

	protoItems := make([]*leapmuxv1.SectionItem, len(items))
	for i, item := range items {
		protoItems[i] = &leapmuxv1.SectionItem{
			WorkspaceId: item.WorkspaceID,
			SectionId:   item.SectionID,
			Position:    item.Position,
		}
	}

	return connect.NewResponse(&leapmuxv1.ListSectionsResponse{
		Sections: protoSections,
		Items:    protoItems,
	}), nil
}

func (s *SectionService) CreateSection(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CreateSectionRequest],
) (*connect.Response[leapmuxv1.CreateSectionResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}

	// Find the position between the last custom section and "Archived".
	sections, err := s.queries.ListWorkspaceSectionsByUserID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var lastCustomPos, archivedPos string
	for _, sec := range sections {
		if sec.Sidebar != leapmuxv1.Sidebar_SIDEBAR_LEFT {
			continue
		}
		if sec.SectionType == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM {
			lastCustomPos = sec.Position
		}
		if sec.SectionType == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED {
			archivedPos = sec.Position
		}
	}

	var position string
	if lastCustomPos != "" && archivedPos != "" {
		position = lexorank.Mid(lastCustomPos, archivedPos)
	} else if archivedPos != "" {
		position = lexorank.Mid("", archivedPos)
	} else {
		position = lexorank.First()
	}

	sectionID := id.Generate()
	if err := s.queries.CreateWorkspaceSection(ctx, db.CreateWorkspaceSectionParams{
		ID:          sectionID,
		UserID:      user.ID,
		Name:        name,
		Position:    position,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.CreateSectionResponse{
		Section: &leapmuxv1.Section{
			Id:          sectionID,
			Name:        name,
			Position:    position,
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		},
	}), nil
}

func (s *SectionService) RenameSection(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RenameSectionRequest],
) (*connect.Response[leapmuxv1.RenameSectionResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}

	result, err := s.queries.RenameWorkspaceSection(ctx, db.RenameWorkspaceSectionParams{
		Name:   name,
		ID:     req.Msg.GetSectionId(),
		UserID: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found or not a custom section"))
	}

	return connect.NewResponse(&leapmuxv1.RenameSectionResponse{}), nil
}

func (s *SectionService) DeleteSection(
	ctx context.Context,
	req *connect.Request[leapmuxv1.DeleteSectionRequest],
) (*connect.Response[leapmuxv1.DeleteSectionResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	sectionID := req.Msg.GetSectionId()

	// Find the "In progress" section to move orphaned workspaces there.
	sections, err := s.queries.ListWorkspaceSectionsByUserID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var inProgressID string
	for _, sec := range sections {
		if sec.SectionType == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS {
			inProgressID = sec.ID
			break
		}
	}

	if inProgressID == "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("in_progress section not found"))
	}

	// Move items from the deleted section to "In progress".
	if err := s.queries.MoveWorkspaceSectionItemsToSection(ctx, db.MoveWorkspaceSectionItemsToSectionParams{
		SectionID:   inProgressID,
		SectionID_2: sectionID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	result, err := s.queries.DeleteWorkspaceSection(ctx, db.DeleteWorkspaceSectionParams{
		ID:     sectionID,
		UserID: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found or not a custom section"))
	}

	return connect.NewResponse(&leapmuxv1.DeleteSectionResponse{}), nil
}

func (s *SectionService) MoveSection(
	ctx context.Context,
	req *connect.Request[leapmuxv1.MoveSectionRequest],
) (*connect.Response[leapmuxv1.MoveSectionResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	sidebar := req.Msg.GetSidebar()
	if sidebar != leapmuxv1.Sidebar_SIDEBAR_LEFT && sidebar != leapmuxv1.Sidebar_SIDEBAR_RIGHT {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("sidebar must be LEFT or RIGHT"))
	}

	// Verify the section exists and belongs to the user.
	section, err := s.queries.GetWorkspaceSectionByID(ctx, req.Msg.GetSectionId())
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if section.UserID != user.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found"))
	}

	if err := s.queries.UpdateWorkspaceSectionSidebarPosition(ctx, db.UpdateWorkspaceSectionSidebarPositionParams{
		Sidebar:  sidebar,
		Position: req.Msg.GetPosition(),
		ID:       req.Msg.GetSectionId(),
		UserID:   user.ID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.MoveSectionResponse{}), nil
}

func (s *SectionService) MoveWorkspace(
	ctx context.Context,
	req *connect.Request[leapmuxv1.MoveWorkspaceRequest],
) (*connect.Response[leapmuxv1.MoveWorkspaceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	// Verify the target section exists and belongs to the user.
	section, err := s.queries.GetWorkspaceSectionByID(ctx, req.Msg.GetSectionId())
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if section.UserID != user.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found"))
	}

	if err := s.queries.SetWorkspaceSectionItem(ctx, db.SetWorkspaceSectionItemParams{
		UserID:      user.ID,
		WorkspaceID: req.Msg.GetWorkspaceId(),
		SectionID:   req.Msg.GetSectionId(),
		Position:    req.Msg.GetPosition(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.MoveWorkspaceResponse{}), nil
}

// initDefaultSections creates the default sections for a user.
func (s *SectionService) initDefaultSections(ctx context.Context, userID string) error {
	// Left sidebar sections
	inProgressPos := lexorank.First()
	sharedPos := lexorank.After(inProgressPos)
	archivedPos := lexorank.After(sharedPos)

	// Right sidebar sections
	filesPos := lexorank.First()
	todosPos := lexorank.After(filesPos)

	if err := s.queries.CreateWorkspaceSection(ctx, db.CreateWorkspaceSectionParams{
		ID:          id.Generate(),
		UserID:      userID,
		Name:        "In progress",
		Position:    inProgressPos,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}); err != nil {
		return err
	}

	if err := s.queries.CreateWorkspaceSection(ctx, db.CreateWorkspaceSectionParams{
		ID:          id.Generate(),
		UserID:      userID,
		Name:        "Shared",
		Position:    sharedPos,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_SHARED,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}); err != nil {
		return err
	}

	if err := s.queries.CreateWorkspaceSection(ctx, db.CreateWorkspaceSectionParams{
		ID:          id.Generate(),
		UserID:      userID,
		Name:        "Archived",
		Position:    archivedPos,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}); err != nil {
		return err
	}

	if err := s.queries.CreateWorkspaceSection(ctx, db.CreateWorkspaceSectionParams{
		ID:          id.Generate(),
		UserID:      userID,
		Name:        "Files",
		Position:    filesPos,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_FILES,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_RIGHT,
	}); err != nil {
		return err
	}

	if err := s.queries.CreateWorkspaceSection(ctx, db.CreateWorkspaceSectionParams{
		ID:          id.Generate(),
		UserID:      userID,
		Name:        "To-dos",
		Position:    todosPos,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_TODOS,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_RIGHT,
	}); err != nil {
		return err
	}

	return nil
}
