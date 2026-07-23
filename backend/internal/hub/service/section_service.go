package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

// SectionService implements the SectionServiceHandler interface.
type SectionService struct {
	store store.Store
}

// NewSectionService creates a new SectionService.
func NewSectionService(st store.Store) *SectionService {
	return &SectionService{store: st}
}

func (s *SectionService) ListSections(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListSectionsRequest],
) (*connect.Response[leapmuxv1.ListSectionsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	sections, err := s.store.WorkspaceSections().ListByUserID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Auto-initialize default sections if needed.
	if len(sections) == 0 {
		if err := s.initDefaultSections(ctx, user.ID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("init sections: %w", err))
		}
		sections, err = s.store.WorkspaceSections().ListByUserID(ctx, user.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	// Ensure Workers section exists for users created before it became a server-side section.
	hasWorkers := false
	for _, sec := range sections {
		if sec.SectionType == leapmuxv1.SectionType_SECTION_TYPE_WORKERS {
			hasWorkers = true
			break
		}
	}
	if !hasWorkers {
		workersSec, err := s.createWorkersSection(ctx, user.ID, sections)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create workers section: %w", err))
		}
		sections = append(sections, *workersSec)
	}

	items, err := s.store.WorkspaceSectionItems().ListByUser(ctx, user.ID)
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

	name, err := validate.SanitizeName(req.Msg.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name: %w", err))
	}

	// Find the position between the last custom section and "Archived".
	sections, err := s.store.WorkspaceSections().ListByUserID(ctx, user.ID)
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
	if err := s.store.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
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
		SectionId: sectionID,
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

	name, err := validate.SanitizeName(req.Msg.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name: %w", err))
	}

	rows, err := s.store.WorkspaceSections().Rename(ctx, store.RenameWorkspaceSectionParams{
		Name:   name,
		ID:     req.Msg.GetSectionId(),
		UserID: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
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

	// The whole move-then-delete sequence runs in one transaction: the
	// previous code looped N `Set` calls and then a `Delete` outside
	// any transaction, so a mid-loop failure (ctx cancel, DB blip)
	// left items split between the doomed section and "In progress"
	// with the row itself still around. The single bulk UPDATE this
	// loop replaced was implicitly atomic at SQL level; rebuilding
	// atomicity at the application boundary restores the same
	// invariant while keeping the per-item lexorank stamping that
	// avoids position collisions.
	var notFound bool
	if err := s.store.RunInTransaction(ctx, func(tx store.Store) error {
		// Find the "In progress" section to move orphaned workspaces there.
		sections, err := tx.WorkspaceSections().ListByUserID(ctx, user.ID)
		if err != nil {
			return err
		}

		var inProgressID string
		for _, sec := range sections {
			if sec.SectionType == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS {
				inProgressID = sec.ID
				break
			}
		}

		if inProgressID == "" {
			return fmt.Errorf("in_progress section not found")
		}

		// Move items from the deleted section into "In progress",
		// reassigning positions so the relocated items APPEND past
		// the existing in_progress items. A blind bulk UPDATE
		// preserving each item's old position would collide with
		// in_progress items at the same lexorank value
		// (lexorank.First() always returns "n", so any two
		// "first into a section" items both hold position "n"). The
		// collision then bubbles up as the sidebar shuffling
		// workspaces on every refresh -- items in tie come back in
		// planner-defined order. Iterating in stable order and
		// stamping fresh `After(lastPos)` ranks keeps the relative
		// ordering of the moved items intact while guaranteeing
		// uniqueness against the destination's existing ranks.
		allItems, err := tx.WorkspaceSectionItems().ListByUser(ctx, user.ID)
		if err != nil {
			return err
		}
		// Find the highest position currently in in_progress so we
		// can extend past it. ListByUser already orders by
		// (ws.position, wsi.position, wsi.workspace_id), so the last
		// in-progress entry in iteration order is the one to append
		// after.
		lastInProgressPos := ""
		for _, item := range allItems {
			if item.SectionID == inProgressID {
				lastInProgressPos = item.Position
			}
		}
		// Walk source items in the same sort order so the relocated
		// block keeps its original relative order in the destination.
		for _, item := range allItems {
			if item.SectionID != sectionID {
				continue
			}
			newPos := lexorank.After(lastInProgressPos)
			if err := tx.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
				UserID:      user.ID,
				WorkspaceID: item.WorkspaceID,
				SectionID:   inProgressID,
				Position:    newPos,
			}); err != nil {
				return err
			}
			lastInProgressPos = newPos
		}

		// Verify the section is empty after moving items (race
		// protection). HasItemsBySection runs inside the same tx so
		// a sibling SetWorkspaceSectionItem committed between the
		// loop and this check can't slip past the guard.
		hasItems, err := tx.WorkspaceSectionItems().HasItemsBySection(ctx, sectionID)
		if err != nil {
			return err
		}
		if hasItems {
			return store.ErrSectionNotEmpty
		}

		rows, err := tx.WorkspaceSections().Delete(ctx, store.DeleteWorkspaceSectionParams{
			ID:     sectionID,
			UserID: user.ID,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			notFound = true
			// Roll back so we don't commit the orphan moves against
			// a phantom delete. The outer handler maps notFound to
			// CodeNotFound.
			return errSectionDeleteRollback
		}
		return nil
	}); err != nil {
		if notFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found or not a custom section"))
		}
		if errors.Is(err, store.ErrSectionNotEmpty) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, store.ErrSectionNotEmpty)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.DeleteSectionResponse{}), nil
}

// errSectionDeleteRollback is a sentinel used only inside DeleteSection
// to roll back the surrounding transaction when the target section row
// doesn't exist. The handler swallows it and re-maps to CodeNotFound;
// callers will never see this value.
var errSectionDeleteRollback = errors.New("section delete: roll back to surface NotFound")

// requireOwnedSection loads a workspace_sections row by id and verifies
// it belongs to the caller. Returns the section on success, or a
// pre-coded *connect.Error suitable for direct `return nil, err` from
// the RPC handler. Non-owner hits masquerade as CodeNotFound by design
// — disclosing "exists but not yours" would leak section ids to
// unrelated users. Both MoveSection and MoveWorkspace need the same
// existence + ownership gate, and an earlier duplicate-by-hand copy
// risked one side diverging on the auth contract (e.g. one branch
// switching to CodePermissionDenied without the other).
// It takes a userid.UserID rather than a string and compares through Matches --
// the same mechanism loadOwnedWorkspaceOr403 uses -- so this, the package's
// OTHER resource-ownership predicate, cannot fail open by matching a blank
// workspace_sections.user_id against a caller whose id never got populated.
func (s *SectionService) requireOwnedSection(ctx context.Context, userID userid.UserID, sectionID string) (*store.WorkspaceSection, error) {
	section, err := s.store.WorkspaceSections().GetByID(ctx, sectionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !userID.Matches(section.UserID) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found"))
	}
	return section, nil
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

	if _, err := s.requireOwnedSection(ctx, user.ID, req.Msg.GetSectionId()); err != nil {
		return nil, err
	}

	if err := s.store.WorkspaceSections().UpdateSidebarPosition(ctx, store.UpdateWorkspaceSectionSidebarPositionParams{
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

	workspaceID := req.Msg.GetWorkspaceId()

	if _, err := s.requireOwnedSection(ctx, user.ID, req.Msg.GetSectionId()); err != nil {
		return nil, err
	}

	if err := s.store.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
		UserID:      user.ID,
		WorkspaceID: workspaceID,
		SectionID:   req.Msg.GetSectionId(),
		Position:    req.Msg.GetPosition(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.MoveWorkspaceResponse{}), nil
}

// defaultSection is one of the sections every new user starts with. Position is
// omitted: it is derived below from the section's order within its sidebar, so
// the two cannot disagree.
type defaultSection struct {
	name        string
	sectionType leapmuxv1.SectionType
	sidebar     leapmuxv1.Sidebar
}

// defaultSections lists the starting sections in the order they appear, per
// sidebar. Adding one is a line here rather than a sixth copy of the Create
// block below -- which is what kept the five copies' UserID, error handling,
// and id generation identical by construction instead of by inspection.
var defaultSections = []defaultSection{
	{"In progress", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS, leapmuxv1.Sidebar_SIDEBAR_LEFT},
	{"Archived", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED, leapmuxv1.Sidebar_SIDEBAR_LEFT},
	{"Workers", leapmuxv1.SectionType_SECTION_TYPE_WORKERS, leapmuxv1.Sidebar_SIDEBAR_LEFT},
	{"Files", leapmuxv1.SectionType_SECTION_TYPE_FILES, leapmuxv1.Sidebar_SIDEBAR_RIGHT},
	{"To-dos", leapmuxv1.SectionType_SECTION_TYPE_TODOS, leapmuxv1.Sidebar_SIDEBAR_RIGHT},
}

// initDefaultSections creates the default sections for a user.
func (s *SectionService) initDefaultSections(ctx context.Context, userID userid.UserID) error {
	// Each sidebar is ranked independently, starting at First() and chaining
	// After() down the list -- the same left/right split the literals encoded
	// by hand, now a consequence of the table's order rather than of five
	// separately-computed variables that had to be paired with the right rows.
	lastPos := map[leapmuxv1.Sidebar]string{}
	for _, section := range defaultSections {
		position := lexorank.First()
		if prev, ok := lastPos[section.sidebar]; ok {
			position = lexorank.After(prev)
		}
		lastPos[section.sidebar] = position

		if err := s.store.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          id.Generate(),
			UserID:      userID,
			Name:        section.name,
			Position:    position,
			SectionType: section.sectionType,
			Sidebar:     section.sidebar,
		}); err != nil {
			return err
		}
	}
	return nil
}

// createWorkersSection creates a Workers section for an existing user.
// It is positioned after the last left-sidebar section.
func (s *SectionService) createWorkersSection(ctx context.Context, userID userid.UserID, sections []store.WorkspaceSection) (*store.WorkspaceSection, error) {
	var lastLeftPos string
	for _, sec := range sections {
		if sec.Sidebar == leapmuxv1.Sidebar_SIDEBAR_LEFT && sec.Position > lastLeftPos {
			lastLeftPos = sec.Position
		}
	}

	var position string
	if lastLeftPos != "" {
		position = lexorank.After(lastLeftPos)
	} else {
		position = lexorank.First()
	}

	sectionID := id.Generate()
	if err := s.store.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
		ID:          sectionID,
		UserID:      userID,
		Name:        "Workers",
		Position:    position,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKERS,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}); err != nil {
		return nil, err
	}

	return &store.WorkspaceSection{
		ID:          sectionID,
		UserID:      userID.String(),
		Name:        "Workers",
		Position:    position,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKERS,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}, nil
}
