package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const (
	layoutEventPageLimit = 128
	layoutEventWaitLimit = 15 * time.Second
	layoutEventRecheck   = 2 * time.Second
	layoutCursorVersion  = 1
	layoutPositionSize   = 1 + 8
)

func (s *WorkspaceService) ReadLayoutEvents(ctx context.Context, req *leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	workspaceID, cursor := req.GetWorkspaceId(), req.GetAfterCursor()
	if len(cursor) == 0 && workspaceID != "" {
		if _, err := loadWorkspaceForRead(ctx, s.store, workspaceID, user); err != nil {
			return nil, err
		}
	}
	events := s.store.LayoutEvents()
	if len(cursor) == 0 {
		page, err := events.ReadPage(ctx, user.ID, workspaceID, math.MaxInt64, 1)
		if errors.Is(err, store.ErrNotFound) {
			if err := ensureLayoutEventHead(ctx, events, user.ID); err != nil {
				return nil, wrappedConnectError(connect.CodeUnavailable, err)
			}
			page, err = events.ReadPage(ctx, user.ID, workspaceID, math.MaxInt64, 1)
		}
		if err != nil {
			return nil, wrappedConnectError(connect.CodeUnavailable, err)
		}
		if workspaceID != "" && !page.WorkspaceLive {
			return nil, connect.NewError(connect.CodeNotFound, "workspace not found")
		}
		return layoutPageStart(page.Head, user.ID, workspaceID, page.Head.HighSequence), nil
	}
	after, ok := layoutCursorPosition(cursor)
	if !ok {
		return nil, invalidLayoutCursor()
	}
	wait := min(time.Duration(req.GetWaitMillis())*time.Millisecond, layoutEventWaitLimit)
	expiry := time.NewTimer(wait)
	defer expiry.Stop()
	recheck := time.NewTicker(layoutEventRecheck)
	defer recheck.Stop()
	var manager *crdt.Manager
	var lifecycleErr error
	drainTried := s.registry == nil
	for {
		var changed <-chan struct{}
		if manager != nil {
			changed = manager.LayoutEventWake()
		}
		response, err := readLayoutEventPage(ctx, events, user.ID, workspaceID, cursor, after)
		if err != nil || response.GetEnd() != nil {
			return response, err
		}
		progress := response.GetGap() != nil || response.GetScannedSequence() > after
		if !drainTried {
			drainTried = true
			drained := s.startLayoutLifecycleDrain(ctx, user.ID)
			// Committed pages can return while lifecycle recovery runs. A
			// busy account must still recover pending lifecycle mutations.
			if progress {
				return response, nil
			}
			select {
			case <-drained.done:
				manager, lifecycleErr = drained.manager, drained.err
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
		case <-expiry.C:
			return response, nil
		case <-changed:
		case <-recheck.C:
		}
	}
}

type layoutLifecycleDrain struct {
	done    chan struct{}
	manager *crdt.Manager
	err     error
}

func (s *WorkspaceService) startLayoutLifecycleDrain(ctx context.Context, owner userid.UserID) *layoutLifecycleDrain {
	s.layoutDrainsMu.Lock()
	defer s.layoutDrainsMu.Unlock()
	userID := owner.String()
	if drain := s.layoutDrains[userID]; drain != nil {
		return drain
	}
	if s.layoutDrains == nil {
		s.layoutDrains = make(map[string]*layoutLifecycleDrain)
	}
	drain := &layoutLifecycleDrain{done: make(chan struct{})}
	s.layoutDrains[userID] = drain
	go func() {
		// A replay request can return its page while this shared drain runs.
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), layoutEventWaitLimit)
		defer cancel()
		drain.manager, drain.err = s.registry.Get(work, userID)
		if drain.err == nil {
			drain.err = drain.manager.SubmitLifecycle(work, lifecycleOutboxAdapter{store: s.store})
		}
		s.layoutDrainsMu.Lock()
		delete(s.layoutDrains, userID)
		close(drain.done)
		s.layoutDrainsMu.Unlock()
	}()
	return drain
}

func readLayoutEventPage(ctx context.Context, events store.LayoutEventStore, owner userid.UserID, workspaceID string, cursor []byte, after int64) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	page, err := events.ReadPage(ctx, owner, workspaceID, after, layoutEventPageLimit)
	if errors.Is(err, store.ErrNotFound) {
		return nil, invalidLayoutCursor()
	}
	if err != nil {
		return nil, wrappedConnectError(connect.CodeUnavailable, err)
	}
	head := page.Head
	if final := page.WorkspaceFinalSequence; final != nil {
		head.HighSequence = min(head.HighSequence, *final)
	}
	if after > head.HighSequence || !hmac.Equal(cursor, signLayoutCursor(head, owner, workspaceID, after)) {
		return nil, invalidLayoutCursor()
	}
	response := layoutPageStart(head, owner, workspaceID, after)
	if final := page.WorkspaceFinalSequence; final != nil && after == *final {
		response.End = &leapmuxv1.LayoutEventEnd{FinalSequence: *final}
		return response, nil
	}
	if after == head.HighSequence && workspaceID != "" && !page.WorkspaceLive && !page.OwnerLifecyclePending {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the Hub cannot establish the final layout position for the deleted workspace")
	}
	if after < head.FloorSequence {
		resume := min(head.FloorSequence, head.HighSequence)
		response.Gap = &leapmuxv1.LayoutEventGap{
			ResumeAfterSequence: resume, Cursor: signLayoutCursor(head, owner, workspaceID, resume),
		}
		return response, nil
	}
	for _, row := range page.Rows {
		var fact leapmuxv1.LayoutEventFact
		if row.Sequence-1 != response.ScannedSequence ||
			proto.Unmarshal(row.Fact, &fact) != nil || fact.GetEvent() == nil {
			return nil, connect.NewError(connect.CodeInternal, "the Hub stored an inconsistent layout event page")
		}
		response.ScannedSequence = row.Sequence
		if scoped := layoutFactForWorkspace(&fact, workspaceID); scoped != nil {
			response.Records = append(response.Records, &leapmuxv1.LayoutEventRecord{
				Sequence: row.Sequence, ObservedUnixMillis: row.ObservedUnixMillis, Fact: scoped,
				Cursor: signLayoutCursor(head, owner, workspaceID, row.Sequence),
			})
		}
	}
	if len(page.Rows) == 0 && after < head.HighSequence {
		return nil, connect.NewError(connect.CodeInternal, "the Hub lost a committed layout event")
	}
	response.ScannedCursor = signLayoutCursor(head, owner, workspaceID, response.ScannedSequence)
	return response, nil
}

func layoutPageStart(head store.LayoutEventHead, owner userid.UserID, workspaceID string, after int64) *leapmuxv1.ReadLayoutEventsResponse {
	cursor := signLayoutCursor(head, owner, workspaceID, after)
	return &leapmuxv1.ReadLayoutEventsResponse{
		JournalId: head.JournalID, AfterSequence: after, HighWaterSequence: head.HighSequence, Cursor: cursor,
		ScannedSequence: after, ScannedCursor: cursor,
	}
}

func layoutFactForWorkspace(fact *leapmuxv1.LayoutEventFact, workspaceID string) *leapmuxv1.LayoutEventFact {
	if workspaceID == "" || fact.GetLoss() != nil {
		return fact
	}
	if tab := fact.GetTab(); tab != nil {
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
	}
	if fact.GetWorkspace().GetWorkspaceId() == workspaceID {
		return fact
	}
	return nil
}

func signLayoutCursor(head store.LayoutEventHead, owner userid.UserID, workspaceID string, after int64) []byte {
	token := make([]byte, layoutPositionSize, layoutPositionSize+sha256.Size)
	token[0] = layoutCursorVersion
	binary.BigEndian.PutUint64(token[1:], uint64(after))
	digest := hmac.New(sha256.New, head.CursorKey)
	for _, field := range []string{"leapmux layout event position", owner.String(), head.JournalID, workspaceID, string(token)} {
		var lenBytes [8]byte
		binary.BigEndian.PutUint64(lenBytes[:], uint64(len(field)))
		_, _ = digest.Write(lenBytes[:])
		_, _ = io.WriteString(digest, field)
	}
	return append(token, digest.Sum(nil)...)
}

func layoutCursorPosition(cursor []byte) (int64, bool) {
	if len(cursor) != layoutPositionSize+sha256.Size || cursor[0] != layoutCursorVersion {
		return 0, false
	}
	raw := binary.BigEndian.Uint64(cursor[1:layoutPositionSize])
	if raw > math.MaxInt64 {
		return 0, false
	}
	return int64(raw), true
}

func invalidLayoutCursor() error {
	return connect.NewError(connect.CodeFailedPrecondition, "the event checkpoint does not match this layout history and scope")
}
