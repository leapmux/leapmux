package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

type workerSemanticTestWriter struct {
	*testResponseWriter
	frames []*leapmuxv1.WatchWorkerSemanticEventsResponse
	stop   func(*leapmuxv1.WatchWorkerSemanticEventsResponse)
}

func (w *workerSemanticTestWriter) SendStream(message *leapmuxv1.InnerStreamMessage) error {
	if err := w.testResponseWriter.SendStream(message); err != nil {
		return err
	}
	if len(message.GetPayload()) == 0 {
		return nil
	}
	frame := new(leapmuxv1.WatchWorkerSemanticEventsResponse)
	if err := proto.Unmarshal(message.GetPayload(), frame); err != nil {
		return err
	}
	w.frames = append(w.frames, frame)
	w.stop(frame)
	return nil
}

func TestWatchWorkerSemanticEventsReplaysAClosedTabFromAnEmptyWorker(t *testing.T) {
	t.Parallel()
	svc, dispatcher, opened := setupTestService(t)
	svc.EnableSemanticEvents()
	t.Cleanup(func() {
		svc.Shutdown()
		svc.FinalizeSemanticEvents(true)
	})
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)
	ctx := testutil.DeadlineContext(t)
	terminalKind := leapmuxv1.TabType_TAB_TYPE_TERMINAL
	caller := channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read terminal:read"))
	watch := func(cursor []byte, sendUpdate bool, stopAt func(*leapmuxv1.WatchWorkerSemanticEventsResponse) bool) []*leapmuxv1.WatchWorkerSemanticEventsResponse {
		watchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		writer := &workerSemanticTestWriter{testResponseWriter: newTestWriter()}
		writer.stop = func(frame *leapmuxv1.WatchWorkerSemanticEventsResponse) {
			if frame.GetReady() != nil && sendUpdate {
				writer.deliverStreamRequest([]byte("an unused update"), false)
			}
			if stopAt(frame) {
				writer.deliverStreamRequest(nil, true)
			}
		}
		payload, err := proto.Marshal(&leapmuxv1.WatchWorkerSemanticEventsRequest{TabType: terminalKind, AfterCursor: cursor})
		require.NoError(t, err)
		dispatchTestRequest(dispatcher, watchCtx, caller, &leapmuxv1.InnerRpcRequest{
			Method: leapmuxv1connect.WorkerWatchServiceWatchWorkerSemanticEventsProcedure, Payload: payload,
		}, writer)
		require.Empty(t, writer.errors)
		for _, frame := range writer.streamsSnapshot() {
			require.Nil(t, frame.GetError())
		}
		return writer.frames
	}
	initial := watch(nil, false, func(frame *leapmuxv1.WatchWorkerSemanticEventsResponse) bool { return frame.GetReady() != nil })
	require.Len(t, initial, 1)
	ready := initial[0].GetReady()
	require.NotNil(t, ready)
	require.Zero(t, ready.GetHighWaterSequence())
	require.NotEmpty(t, ready.GetCursor())

	tabID := openTerminalViaRPC(t, svc, dispatcher, opened, t.TempDir())
	broadcastBell := func(id string) {
		svc.Watchers.BroadcastTerminalEvent(id, &leapmuxv1.TerminalEvent{
			TerminalId: id,
			Event:      &leapmuxv1.TerminalEvent_Bell{Bell: &leapmuxv1.TerminalBell{}},
		})
	}
	broadcastBell(tabID)
	// A Worker feed admission waits for the queued publication.
	_, _, err := journal.OpenFeed(ctx, terminalKind, ready.GetCursor())
	require.NoError(t, err)
	closed := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab: terminalTabIdent(tabID), WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	}, closed)
	require.Empty(t, closed.errors)
	require.Len(t, closed.responses, 1)
	liveID := openTerminalViaRPC(t, svc, dispatcher, newTestWriter(), t.TempDir())
	var bells []*leapmuxv1.SemanticWorkerWatchRecord
	resumed := watch(ready.GetCursor(), true, func(frame *leapmuxv1.WatchWorkerSemanticEventsResponse) bool {
		record := frame.GetRecord()
		if record.GetFact().GetTerminalBell() == nil {
			return false
		}
		bells = append(bells, record)
		if record.GetTab().GetTabId() == tabID {
			broadcastBell(liveID)
		}
		return record.GetTab().GetTabId() == liveID
	})
	require.Len(t, bells, 2)
	high := resumed[0].GetReady().GetHighWaterSequence()
	assert.Equal(t, tabID, bells[0].GetTab().GetTabId())
	assert.Equal(t, terminalKind, bells[0].GetTab().GetTabType())
	assert.LessOrEqual(t, bells[0].GetSequence(), high)
	assert.Equal(t, liveID, bells[1].GetTab().GetTabId())
	assert.Greater(t, bells[1].GetSequence(), high)
}

func TestWatchWorkerSemanticEventsRequiresTheSelectedKindGrant(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	for _, input := range []struct {
		tabType leapmuxv1.TabType
		grants  string
		missing string
	}{
		{leapmuxv1.TabType_TAB_TYPE_TERMINAL, "worker:read agent:read", "terminal:read"},
		{leapmuxv1.TabType_TAB_TYPE_AGENT, "worker:read terminal:read", "agent:read"},
	} {
		payload, err := proto.Marshal(&leapmuxv1.WatchWorkerSemanticEventsRequest{TabType: input.tabType})
		require.NoError(t, err)
		writer := newTestWriter()
		dispatchTestRequest(dispatcher, t.Context(), channel.NewCaller(svc.RegisteredBy(), mustScopes(input.grants)),
			&leapmuxv1.InnerRpcRequest{Method: leapmuxv1connect.WorkerWatchServiceWatchWorkerSemanticEventsProcedure, Payload: payload}, writer)
		frames := writer.streamsSnapshot()
		require.Len(t, frames, 1)
		assert.True(t, frames[0].GetEnd())
		assert.Equal(t, int32(codes.PermissionDenied), frames[0].GetError().GetCode())
		assert.Contains(t, frames[0].GetError().GetMessage(), input.missing)
	}
}
