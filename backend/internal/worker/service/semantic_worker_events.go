package service

import (
	"context"

	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/eventjournal"
)

func (svc *Service) watchWorkerSemanticEvents(ctx context.Context, caller channel.Caller, req *leapmuxv1.WatchWorkerSemanticEventsRequest, sender channel.ResponseWriter) {
	tabType := req.GetTabType()
	if !eventjournal.SupportsTabType(tabType) {
		sendSemanticWatchError(sender, codes.InvalidArgument, "select the agent or terminal event history")
		return
	}
	scope, grant := semanticHistoryReadGrant(tabType)
	if !caller.Allows(scope) {
		sendSemanticWatchError(sender, codes.PermissionDenied, "event history requires "+grant)
		return
	}
	journal := svc.semanticEvents.journal.Load()
	if journal == nil {
		sendSemanticWatchError(sender, codes.Unavailable, eventjournal.ErrUnavailable.Error())
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	release, ok := sender.BindStream(semanticWatchControl{cancel: cancel})
	if !ok {
		sendSemanticWatchError(sender, codes.FailedPrecondition, "stream binding unavailable")
		return
	}
	defer release()

	checkOwner := func() bool {
		if !callerIsWorkerOwner(svc, caller.UserID) || !journal.OwnedBy(caller.UserID) {
			sendSemanticWatchError(sender, codes.PermissionDenied, workerOwnerDenial)
			return false
		}
		return true
	}
	if !checkOwner() {
		return
	}
	reader, ready, err := journal.OpenFeed(ctx, tabType, req.GetAfterCursor())
	if err != nil {
		reportSemanticWatchError(ctx, sender, err)
		return
	}
	if !checkOwner() {
		return
	}
	readyFrame := &leapmuxv1.WatchWorkerSemanticEventsResponse{Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Ready{Ready: ready}}
	if !sendSemanticWatchFrame(sender, readyFrame, false) {
		return
	}
	after := ready.GetAfterSequence()
	for ctx.Err() == nil && checkOwner() {
		frames, changed, err := reader.Read(ctx, after)
		if err != nil {
			reportSemanticWatchError(ctx, sender, err)
			return
		}
		for _, frame := range frames {
			if ctx.Err() != nil || !checkOwner() {
				return
			}
			record := frame.GetRecord()
			if request := record.GetFact().GetAgentRequest(); request != nil {
				handle, err := svc.currentSemanticRequestHandle(ctx, journal, record.GetTab().GetTabId(), request)
				if err != nil {
					reportSemanticWatchError(ctx, sender, err)
					return
				}
				record.CurrentRequestHandle = handle
			}
			if !checkOwner() || !sendSemanticWatchFrame(sender, frame, false) {
				return
			}
			if gap := frame.GetGap(); gap != nil {
				after = gap.GetResumeAfterSequence()
			} else {
				after = record.GetSequence()
			}
		}
		if len(frames) == 0 {
			select {
			case <-ctx.Done():
			case <-changed:
			}
		}
	}
}
