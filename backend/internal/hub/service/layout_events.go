package service

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/quartz"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/seqcursor"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const (
	layoutEventPageLimit      = 128
	layoutEventWaitLimit      = 15 * time.Second
	layoutEventDeadlineMargin = 250 * time.Millisecond
	layoutEventRecheck        = 2 * time.Second
)

func (s *WorkspaceService) ReadLayoutEvents(ctx context.Context, req *leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	workspaceID, cursor := req.GetWorkspaceId(), req.GetAfterCursor()
	events := s.store.LayoutEvents()
	if len(cursor) == 0 {
		return s.readInitialLayoutCursor(ctx, events, user, workspaceID)
	}
	after, ok := layoutCursorPosition(cursor)
	if !ok {
		return nil, invalidLayoutCursor()
	}
	clk := s.clk()
	wait := capLayoutEventWait(ctx, clk, time.Duration(req.GetWaitMillis())*time.Millisecond)
	var expiryCh <-chan time.Time
	var recheckCh <-chan time.Time
	if wait > 0 {
		expiry := clk.NewTimer(wait)
		defer expiry.Stop()
		expiryCh = expiry.C
		// Production runs a single Hub instance whose writers all signal
		// s.registry.SignalLayoutEvents. Keep the periodic fallback ticker only
		// when no registry is attached (store-only tests).
		if s.registry == nil {
			recheck := clk.NewTicker(layoutEventRecheck)
			defer recheck.Stop()
			recheckCh = recheck.C
		}
	}
	var lifecycleErr error
	drainTried := s.registry == nil
	for {
		var changed <-chan struct{}
		if s.registry != nil {
			changed = s.registry.LayoutEventWake(user.ID.String())
		}
		response, ownerLifecyclePending, err := readLayoutEventPage(ctx, events, user.ID, workspaceID, cursor, after)
		if err != nil || response.GetEnd() != nil {
			return response, err
		}
		progress := response.GetGap() != nil || response.GetScannedSequence() > after
		if !drainTried && ownerLifecyclePending {
			drainTried = true
			drained := s.registry.StartLifecycleDrain(user.ID.String())
			// Committed pages can return while lifecycle recovery runs. A
			// busy account must still recover pending lifecycle mutations.
			if progress {
				return response, nil
			}
			select {
			case <-drained.Done():
				lifecycleErr = drained.Err()
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// A failed outbox entry cannot hide an already committed page.
			// Read again because the drain can commit before it reports a fault.
			continue
		}
		if progress {
			return response, nil
		}
		if lifecycleErr != nil {
			return nil, wrappedConnectError(connect.CodeUnavailable, lifecycleErr)
		}
		if wait == 0 {
			return response, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-expiryCh:
			return response, nil
		case <-changed:
			drainTried = false
		case <-recheckCh:
		}
	}
}

func (s *WorkspaceService) readInitialLayoutCursor(ctx context.Context, events store.LayoutEventStore, user *auth.UserInfo, workspaceID string) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	if workspaceID != "" {
		if _, err := loadWorkspaceForRead(ctx, s.store, workspaceID, user); err != nil {
			return nil, err
		}
	}
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

// capLayoutEventWait limits long-poll waiting to layoutEventWaitLimit and leaves
// a margin before the request's context deadline (including the unary timeout
// interceptor's default) so an idle poll returns an empty page instead of
// failing with DeadlineExceeded.
func capLayoutEventWait(ctx context.Context, clk quartz.Clock, requested time.Duration) time.Duration {
	if requested <= 0 {
		return 0
	}
	wait := min(requested, layoutEventWaitLimit)
	if dl, ok := ctx.Deadline(); ok {
		remaining := dl.Sub(clk.Now()) - layoutEventDeadlineMargin
		if remaining <= 0 {
			return 0
		}
		wait = min(wait, remaining)
	}
	return wait
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
