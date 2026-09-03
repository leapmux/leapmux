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

	// ListSections is a pure READ. CreateUser seeds the default sections in the
	// same transaction as the user row, so every user already has them.
	//
	// Seeding them here instead was a read-modify-write with no transaction and
	// no uniqueness constraint behind it: two concurrent calls for one user both
	// saw an empty list, and both wrote a full set. The sidebar then rendered
	// two of every section, and the duplicates were indistinguishable.

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

	// The same rule MoveSection applies, so a section cannot be created into a
	// sidebar it could not then be moved to.
	sidebar := req.Msg.GetSidebar()
	if sidebar != leapmuxv1.Sidebar_SIDEBAR_LEFT && sidebar != leapmuxv1.Sidebar_SIDEBAR_RIGHT {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("sidebar must be LEFT or RIGHT"))
	}

	// Find the position between the last custom section and "Archived".
	sections, err := s.store.WorkspaceSections().ListByUserID(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// The scan follows the REQUESTED sidebar, not always the left one. The
	// right sidebar holds no Archived anchor and no custom section, so a
	// left-only scan left both ends empty there and every new section fell to
	// lexorank.First() -- always "n", which collides with the first built-in
	// section already on that side.
	var lastCustomPos, archivedPos, lastPos string
	for _, sec := range sections {
		if sec.Sidebar != sidebar {
			continue
		}
		if sec.SectionType == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM {
			lastCustomPos = sec.Position
		}
		if sec.SectionType == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED {
			archivedPos = sec.Position
		}
		// ListByUserID orders by (sidebar, position), so the last row of this
		// sidebar carries its highest position.
		lastPos = sec.Position
	}

	var position string
	switch {
	case lastCustomPos != "" && archivedPos != "":
		position = lexorank.Mid(lastCustomPos, archivedPos)
	case archivedPos != "":
		position = lexorank.Mid("", archivedPos)
	case lastPos != "":
		// No Archived anchor on this sidebar: append past its last section.
		position = lexorank.After(lastPos)
	default:
		position = lexorank.First()
	}

	sectionID := id.Generate()
	if err := s.store.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
		ID:          sectionID,
		UserID:      user.ID,
		Name:        name,
		Position:    position,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
		Sidebar:     sidebar,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// The whole row, because the SERVER computed the position. A client that
	// got only the id would have to re-derive the lexorank rule to place the
	// section in its own list, which is a second source of truth for the order.
	return connect.NewResponse(&leapmuxv1.CreateSectionResponse{
		Section: &leapmuxv1.Section{
			Id:          sectionID,
			Name:        name,
			Position:    position,
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     sidebar,
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

	name, err := validate.SanitizeName(req.Msg.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name: %w", err))
	}

	// Diagnose the two refusals apart BEFORE the write. The query carries its
	// own `section_type = custom` filter, so a built-in came back as rows == 0
	// and the handler could only answer "not found or not a custom section" --
	// a message that admits it cannot tell the caller which. There is no
	// transaction here (only DeleteSection has one), so this is a read that
	// precedes the update rather than one inside it; a section deleted in the
	// window between them still lands on the rows == 0 branch below.
	if _, err := s.requireCustomSection(ctx, user.ID, req.Msg.GetSectionId(), "rename"); err != nil {
		return nil, err
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
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found"))
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

	// Same pre-flight as RenameSection: separate "gone" from "built-in" while
	// the answer is still available. Inside the transaction the delete's own
	// `section_type = custom` filter collapses both into rows == 0.
	if _, err := s.requireCustomSection(ctx, user.ID, sectionID, "delete"); err != nil {
		return nil, err
	}

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
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("section not found"))
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

// requireCustomSection loads an owned section and refuses a built-in one.
//
// Rename and Delete apply to CUSTOM sections only, and both store queries carry
// that rule as their own `section_type = custom` filter -- which is what keeps
// the invariant true for any caller. What the filter cannot do is SAY which
// rule refused: a built-in and a missing row both come back as zero rows, so
// the handler answered NotFound for both and its own message admitted it
// ("not found or not a custom section").
//
// FailedPrecondition for the built-in, because the section exists, the caller
// owns it, and its STATE forbids the operation -- the same code the sibling
// archived-workspace guard uses, so two near-identical state rules answer with
// one code rather than two.
//
// `verb` names the refused operation in the message, so a client that logs the
// error alone still knows what was refused.
func (s *SectionService) requireCustomSection(
	ctx context.Context, userID userid.UserID, sectionID, verb string,
) (*store.WorkspaceSection, error) {
	section, err := s.requireOwnedSection(ctx, userID, sectionID)
	if err != nil {
		return nil, err
	}
	if section.SectionType != leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM {
		return nil, connect.NewError(
			connect.CodeFailedPrecondition,
			fmt.Errorf("cannot %s a built-in section", verb),
		)
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
