package service

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/seqcursor"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const layoutEventPageLimit = 128

func (s *WorkspaceService) ReadLayoutEvents(ctx context.Context, req *leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	workspaceID, cursor := req.GetWorkspaceId(), req.GetAfterCursor()
	if len(cursor) == 0 {
		return readInitialLayoutCursor(ctx, s.store, user, workspaceID)
	}
	after, ok := layoutCursorPosition(cursor)
	if !ok {
		return nil, invalidLayoutCursor()
	}
	return readLayoutEventPageWithDrain(ctx, s.store.LayoutEvents(), s.registry, user.ID, workspaceID, cursor, after)
}

func readLayoutEventPageWithDrain(
	ctx context.Context,
	events store.LayoutEventStore,
	registry *crdt.Registry,
	owner userid.UserID,
	workspaceID string,
	cursor []byte,
	after int64,
) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	response, ownerLifecyclePending, err := readLayoutEventPage(ctx, events, owner, workspaceID, cursor, after)
	if err != nil || response.GetEnd() != nil {
		return response, err
	}
	progress := response.GetGap() != nil || response.GetScannedSequence() > after
	if registry == nil || !ownerLifecyclePending {
		return response, nil
	}
	drained := registry.StartLifecycleDrain(owner.String())
	// Committed pages can return while lifecycle recovery runs. A busy account
	// must still recover pending lifecycle mutations.
	if progress {
		return response, nil
	}
	select {
	case <-drained.Done():
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	lifecycleErr := drained.Err()
	// A failed outbox entry cannot hide an already committed page. Read again
	// because the drain can commit before it reports a fault.
	response, _, err = readLayoutEventPage(ctx, events, owner, workspaceID, cursor, after)
	if err != nil || response.GetEnd() != nil {
		return response, err
	}
	if response.GetGap() != nil || response.GetScannedSequence() > after {
		return response, nil
	}
	if lifecycleErr != nil {
		return nil, wrappedConnectError(connect.CodeUnavailable, lifecycleErr)
	}
	return response, nil
}

func readInitialLayoutCursor(ctx context.Context, st store.Store, user *auth.UserInfo, workspaceID string) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	if workspaceID != "" {
		if _, err := loadWorkspaceForRead(ctx, st, workspaceID, user); err != nil {
			return nil, err
		}
	}
	events := st.LayoutEvents()
	state, err := events.ReadHead(ctx, user.ID, workspaceID)
	if errors.Is(err, store.ErrNotFound) {
		if err := ensureLayoutEventHead(ctx, events, user.ID); err != nil {
			return nil, wrappedConnectError(connect.CodeUnavailable, err)
		}
		state, err = events.ReadHead(ctx, user.ID, workspaceID)
	}
	if err != nil {
		return nil, wrappedConnectError(connect.CodeUnavailable, err)
	}
	if workspaceID != "" && !state.WorkspaceLive {
		return nil, connect.NewError(connect.CodeNotFound, "workspace not found")
	}
	return layoutPageStart(state.Head, user.ID, workspaceID, state.Head.HighSequence), nil
}

func readLayoutEventPage(ctx context.Context, events store.LayoutEventStore, owner userid.UserID, workspaceID string, cursor []byte, after int64) (*leapmuxv1.ReadLayoutEventsResponse, bool, error) {
	page, err := events.ReadPage(ctx, owner, workspaceID, after, layoutEventPageLimit)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, invalidLayoutCursor()
	}
	if err != nil {
		return nil, false, wrappedConnectError(connect.CodeUnavailable, err)
	}
	head := page.Head
	if final := page.WorkspaceFinalSequence; final != nil {
		head.HighSequence = min(head.HighSequence, *final)
	}
	signer := newLayoutCursorSigner(head, owner, workspaceID)
	verifiedAfter, ok := signer.Verify(cursor)
	if !ok || verifiedAfter != after || after > head.HighSequence {
		return nil, false, invalidLayoutCursor()
	}
	response := layoutPageStartWithCursor(head, after, cursor)
	if final := page.WorkspaceFinalSequence; final != nil && after == *final {
		response.End = &leapmuxv1.LayoutEventEnd{FinalSequence: *final}
		return response, page.OwnerLifecyclePending, nil
	}
	if after == head.HighSequence && workspaceID != "" && !page.WorkspaceLive && !page.OwnerLifecyclePending {
		return nil, false, connect.NewError(connect.CodeFailedPrecondition, "the Hub cannot establish the final layout position for the deleted workspace")
	}
	if after < head.FloorSequence {
		resume := min(head.FloorSequence, head.HighSequence)
		response.Gap = &leapmuxv1.LayoutEventGap{
			ResumeAfterSequence: resume, Cursor: signer.Sign(resume),
		}
		return response, page.OwnerLifecyclePending, nil
	}
	for _, row := range page.Rows {
		var fact leapmuxv1.LayoutEventFact
		if row.Sequence-1 != response.ScannedSequence ||
			proto.Unmarshal(row.Fact, &fact) != nil || fact.GetEvent() == nil {
			return nil, false, connect.NewError(connect.CodeInternal, "the Hub stored an inconsistent layout event page")
		}
		response.ScannedSequence = row.Sequence
		if scoped := layoutFactForWorkspace(&fact, workspaceID); scoped != nil {
			response.Records = append(response.Records, &leapmuxv1.LayoutEventRecord{
				Sequence: row.Sequence, ObservedUnixMillis: row.ObservedUnixMillis, Fact: scoped,
				Cursor: signer.Sign(row.Sequence),
			})
		}
	}
	if len(page.Rows) == 0 && after < head.HighSequence {
		return nil, false, connect.NewError(connect.CodeInternal, "the Hub lost a committed layout event")
	}
	switch {
	case response.ScannedSequence == after:
		response.ScannedCursor = cursor
	case len(response.Records) > 0 && response.Records[len(response.Records)-1].Sequence == response.ScannedSequence:
		response.ScannedCursor = response.Records[len(response.Records)-1].Cursor
	default:
		response.ScannedCursor = signer.Sign(response.ScannedSequence)
	}
	return response, page.OwnerLifecyclePending, nil
}

func layoutPageStart(head store.LayoutEventHead, owner userid.UserID, workspaceID string, after int64) *leapmuxv1.ReadLayoutEventsResponse {
	signer := newLayoutCursorSigner(head, owner, workspaceID)
	return layoutPageStartWithCursor(head, after, signer.Sign(after))
}

func layoutPageStartWithCursor(head store.LayoutEventHead, after int64, cursor []byte) *leapmuxv1.ReadLayoutEventsResponse {
	return &leapmuxv1.ReadLayoutEventsResponse{
		JournalId: head.JournalID, AfterSequence: after, HighWaterSequence: head.HighSequence, Cursor: cursor,
		ScannedSequence: after, ScannedCursor: cursor,
	}
}

func layoutFactForWorkspace(fact *leapmuxv1.LayoutEventFact, workspaceID string) *leapmuxv1.LayoutEventFact {
	if workspaceID == "" {
		return fact
	}
	switch event := fact.GetEvent().(type) {
	case *leapmuxv1.LayoutEventFact_Loss:
		return fact
	case *leapmuxv1.LayoutEventFact_Tab:
		tab := event.Tab
		before := tab.GetBefore().GetWorkspaceId() == workspaceID
		after := tab.GetAfter().GetWorkspaceId() == workspaceID
		if !before && !after {
			return nil
		}
		if before && after {
			return fact
		}
		scoped := &leapmuxv1.LayoutTabChange{Tab: tab.GetTab(), Kind: tab.GetKind()}
		if before {
			scoped.Before = tab.GetBefore()
		}
		if after {
			scoped.After = tab.GetAfter()
			if scoped.Kind == leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_MOVED {
				scoped.Kind = leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_OPENED
			}
		}
		return &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Tab{Tab: scoped}}
	case *leapmuxv1.LayoutEventFact_Workspace:
		if event.Workspace.GetWorkspaceId() == workspaceID {
			return fact
		}
		return nil
	default:
		return nil
	}
}

func newLayoutCursorSigner(head store.LayoutEventHead, owner userid.UserID, workspaceID string) seqcursor.Signer {
	return seqcursor.NewSigner(head.CursorKey, "leapmux layout event position", owner.String(), head.JournalID, workspaceID)
}

func layoutCursorPosition(cursor []byte) (int64, bool) {
	return seqcursor.DecodePosition(cursor)
}

func invalidLayoutCursor() error {
	return connect.NewError(connect.CodeFailedPrecondition, "the event checkpoint does not match this layout history and scope")
}
