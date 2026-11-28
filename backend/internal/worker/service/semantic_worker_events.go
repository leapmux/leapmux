package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

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
	streamSemanticEvents(svc, ctx, caller, tabType, sender, semanticFrameOps[*leapmuxv1.WatchWorkerSemanticEventsResponse]{
		open: func(ctx context.Context, journal *eventjournal.Journal) (func(context.Context, int64) ([]*leapmuxv1.WatchWorkerSemanticEventsResponse, <-chan struct{}, error), proto.Message, int64, error) {
			reader, ready, err := journal.OpenFeed(ctx, tabType, req.GetAfterCursor())
			if err != nil {
				return nil, nil, 0, err
			}
			return reader.Read, &leapmuxv1.WatchWorkerSemanticEventsResponse{Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Ready{Ready: ready}}, ready.GetAfterSequence(), nil
		},
		requestBinding: func(frame *leapmuxv1.WatchWorkerSemanticEventsResponse) (semanticRequestHandleTarget, bool) {
			record := frame.GetRecord()
			request := record.GetFact().GetAgentRequest()
			if request == nil {
				return semanticRequestHandleTarget{}, false
			}
			return semanticRequestHandleTarget{
				agentID:   record.GetTab().GetTabId(),
				request:   request,
				setHandle: func(handle string) { record.CurrentRequestHandle = handle },
			}, true
		},
		isFinished: func(*leapmuxv1.WatchWorkerSemanticEventsResponse) bool {
			return false
		},
		nextAfter: func(frame *leapmuxv1.WatchWorkerSemanticEventsResponse) int64 {
			if gap := frame.GetGap(); gap != nil {
				return gap.GetResumeAfterSequence()
			}
			return frame.GetRecord().GetSequence()
		},
	})
}
