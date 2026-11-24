package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

type replayIPCWorker struct {
	requests chan *leapmuxv1.WatchSemanticEventsRequest
	attempt  atomic.Int32
}

func (*replayIPCWorker) CallInner(context.Context, string, userid.UserID, *leapmuxv1.InnerRpcRequest) ([]byte, error) {
	return nil, fmt.Errorf("unexpected unary Worker request")
}

func (w *replayIPCWorker) StreamInner(ctx context.Context, target string, _ userid.UserID, req *leapmuxv1.InnerRpcRequest,
	send func(*leapmuxv1.InnerStreamMessage), _ func(channel.StreamController),
) error {
	if target != "worker-B" || req.GetMethod() != semanticWatchProcedure {
		return fmt.Errorf("unexpected stream %s on %s", req.GetMethod(), target)
	}
	request := new(leapmuxv1.WatchSemanticEventsRequest)
	if err := proto.Unmarshal(req.GetPayload(), request); err != nil {
		return err
	}
	select {
	case w.requests <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	attempt := w.attempt.Add(1)
	if attempt > 2 {
		return connect.NewError(connect.CodeFailedPrecondition, "unexpected retry")
	}
	frame := func(value *leapmuxv1.WatchSemanticEventsResponse) error {
		raw, err := proto.Marshal(value)
		if err != nil {
			return err
		}
		send(&leapmuxv1.InnerStreamMessage{Payload: raw})
		return nil
	}
	if err := frame(&leapmuxv1.WatchSemanticEventsResponse{Event: &leapmuxv1.WatchSemanticEventsResponse_Ready{
		Ready: &leapmuxv1.SemanticWatchReady{
			Tab: request.GetTab(), WorkerId: target, JournalId: "journal", TabStreamId: "tab-stream",
			HighWaterSequence: int64(attempt - 1), Cursor: []byte("initial position"),
		},
	}}); err != nil {
		return err
	}
	if attempt == 1 {
		return connect.NewError(connect.CodeCanceled, "sibling Worker channel closed")
	}
	return frame(checkpointBell(1))
}

func TestCheckpointedEventsOverIPCResumesAfterTheSiblingWorkerDisconnects(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	terminalID := fullTestID("terminal")
	worker := &replayIPCWorker{requests: make(chan *leapmuxv1.WatchSemanticEventsRequest, 3)}
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A", "worker-B"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:    &leapmuxv1.TabIdent{TabType: checkpointTerminalType, TabId: terminalID},
			TileId: "root-1", WorkerId: "worker-B", WorkspaceId: "ws-1",
		},
	}, newEventsWorker(), func(r *controlipc.Router) { r.CrossWorker = worker })
	path := filepath.Join(t.TempDir(), "position.json")
	run := runControlLive(t, "events", "--tab", terminalID, "--checkpoint", path, "--limit", "1", "--json")
	assert.Empty(t, receive(t, ctx, worker.requests).GetAfterCursor())
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.Equal(t, kindStreamUnavailable, run.next(t, ctx).Kind)
	assert.Equal(t, []byte("initial position"), receive(t, ctx, worker.requests).GetAfterCursor())
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	bell := run.next(t, ctx)
	assert.Equal(t, kindTerminalBell, bell.Kind)
	require.NotNil(t, bell.Replayed)
	assert.True(t, *bell.Replayed)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
	checkpoint, err := openEventCheckpoint(path)
	require.NoError(t, err)
	defer checkpoint.close()
	saved, err := checkpoint.read()
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Equal(t, int64(1), saved.Sequence)
	assert.Equal(t, []byte("next signed position"), saved.Cursor)
}

type localEventHistoryWorker struct {
	requests chan *leapmuxv1.WatchSemanticEventsRequest
}

func (w *localEventHistoryWorker) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	request := new(leapmuxv1.WatchSemanticEventsRequest)
	if req.GetMethod() != semanticWatchProcedure || proto.Unmarshal(req.GetPayload(), request) != nil {
		_ = writer.SendError(3, "unexpected event history request")
		return
	}
	select {
	case w.requests <- request:
	case <-ctx.Done():
		return
	}
	send := func(frame *leapmuxv1.WatchSemanticEventsResponse, end bool) {
		raw, err := proto.Marshal(frame)
		if err != nil {
			_ = writer.SendError(13, err.Error())
			return
		}
		_ = writer.SendStream(&leapmuxv1.InnerStreamMessage{Payload: raw, End: end})
	}
	send(&leapmuxv1.WatchSemanticEventsResponse{Event: &leapmuxv1.WatchSemanticEventsResponse_Ready{
		Ready: &leapmuxv1.SemanticWatchReady{
			Tab: request.GetTab(), WorkerId: "worker-A", JournalId: "journal", TabStreamId: "tab-stream",
			Cursor: []byte("initial position"),
		},
	}}, false)
	send(&leapmuxv1.WatchSemanticEventsResponse{Event: &leapmuxv1.WatchSemanticEventsResponse_Record{
		Record: &leapmuxv1.SemanticWatchRecord{
			Sequence: 1, Cursor: []byte("next position"),
			Fact: &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_AgentTurnEnd{AgentTurnEnd: &leapmuxv1.AgentTurnEnd{}}},
		},
	}}, true)
}

func TestCheckpointedEventsOverIPCResolvesSelfWithoutTheHub(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	worker := &localEventHistoryWorker{requests: make(chan *leapmuxv1.WatchSemanticEventsRequest, 1)}
	startSpawnIPC(t, &recordingHub{}, worker)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "agent-1")
	t.Setenv("LEAPMUX_CONTROL_TAB_TYPE", "agent")
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")
	run := runControlLive(t, "events", "--tab", "self", "--checkpoint", filepath.Join(t.TempDir(), "position.json"), "--limit", "1", "--json")
	require.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.Equal(t, kindAgentTurnEnd, run.next(t, ctx).Kind)
	request := receive(t, ctx, worker.requests)
	assert.Equal(t, "agent-1", request.GetTab().GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, request.GetTab().GetTabType())
}

type retiredHistoryIPCWorker struct {
	requests chan *leapmuxv1.WatchSemanticEventsRequest
	end      bool
}

func (*retiredHistoryIPCWorker) CallInner(context.Context, string, userid.UserID, *leapmuxv1.InnerRpcRequest) ([]byte, error) {
	return nil, fmt.Errorf("unexpected unary Worker request")
}

func (w *retiredHistoryIPCWorker) StreamInner(ctx context.Context, target string, _ userid.UserID, req *leapmuxv1.InnerRpcRequest,
	send func(*leapmuxv1.InnerStreamMessage), _ func(channel.StreamController),
) error {
	if target != "worker-B" || req.GetMethod() != semanticWatchProcedure {
		return fmt.Errorf("unexpected stream %s on %s", req.GetMethod(), target)
	}
	request := new(leapmuxv1.WatchSemanticEventsRequest)
	if err := proto.Unmarshal(req.GetPayload(), request); err != nil {
		return err
	}
	select {
	case w.requests <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	saved := validCheckpoint()
	frames := []*leapmuxv1.WatchSemanticEventsResponse{
		{Event: &leapmuxv1.WatchSemanticEventsResponse_Ready{Ready: &leapmuxv1.SemanticWatchReady{
			Tab: request.GetTab(), WorkerId: target, JournalId: saved.JournalID, TabStreamId: saved.TabStreamID,
			AfterSequence: saved.Sequence, HighWaterSequence: saved.Sequence, Cursor: request.GetAfterCursor(),
		}}},
		{Event: &leapmuxv1.WatchSemanticEventsResponse_Retired{Retired: &leapmuxv1.SemanticWatchRetired{FinalSequence: saved.Sequence}}},
	}
	for _, frame := range frames {
		raw, err := proto.Marshal(frame)
		if err != nil {
			return err
		}
		send(&leapmuxv1.InnerStreamMessage{Payload: raw, End: frame.GetRetired() != nil && w.end})
	}
	return nil
}

func TestCheckpointedClosedTerminalOverIPCUsesItsSavedSourceAndRequiresEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  bool
		code int
	}{
		{"final retirement", true, cli.ExitOK},
		{"retirement without end", false, cli.ExitFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testutil.DeadlineContext(t)
			worker := &retiredHistoryIPCWorker{
				requests: make(chan *leapmuxv1.WatchSemanticEventsRequest, 1), end: tc.end,
			}
			startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A", "worker-B")}, newEventsWorker(), func(r *controlipc.Router) { r.CrossWorker = worker })
			saved := validCheckpoint()
			saved.TabID, saved.WorkerID = fullTestID("terminal"), "worker-B"
			path := filepath.Join(t.TempDir(), "closed-terminal.json")
			checkpoint, err := openEventCheckpoint(path)
			require.NoError(t, err)
			require.NoError(t, checkpoint.save(saved))
			checkpoint.close()

			run := runControlLive(t, "events", "--tab", saved.TabID, "--checkpoint", path, "--kind", "stream,terminal.bell", "--json")
			assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
			request := receive(t, ctx, worker.requests)
			assert.Equal(t, saved.TabID, request.GetTab().GetTabId())
			assert.Equal(t, checkpointTerminalType, request.GetTab().GetTabType())
			assert.Equal(t, saved.Cursor, request.GetAfterCursor())
			if tc.end {
				last := run.next(t, ctx)
				assert.Equal(t, kindStreamRetired, last.Kind)
				require.NotNil(t, last.Sequence)
				assert.Equal(t, saved.Sequence, *last.Sequence)
			}
			assert.Equal(t, tc.code, receive(t, ctx, run.code))
		})
	}
}

func TestLayoutCheckpointOverIPCRequiresTheHubEndAfterADeletion(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	workspace := fullTestID("workspace")
	path := filepath.Join(t.TempDir(), "layout.json")
	checkpoint, err := openEventCheckpoint(path)
	require.NoError(t, err)
	require.NoError(t, checkpoint.save(&eventCheckpoint{
		Version: eventCheckpointVersion, Scope: layoutCheckpointScope, WorkspaceID: workspace,
		JournalID: "layout", Filter: []string{kindWorkspaceDeleted}, Sequence: 1, Cursor: []byte("one"),
	}))
	checkpoint.close()
	hub := &recordingHub{readLayoutEvents: func(request *leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
		if request.GetWorkspaceId() != workspace {
			return nil, connect.NewError(connect.CodeInvalidArgument, "unexpected workspace")
		}
		switch string(request.GetAfterCursor()) {
		case "one":
			return &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "layout", Cursor: []byte("one"), HighWaterSequence: 2, AfterSequence: 1,
				ScannedSequence: 2, ScannedCursor: []byte("two"),
				Records: []*leapmuxv1.LayoutEventRecord{{
					Sequence: 2, Cursor: []byte("two"),
					Fact: &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Workspace{Workspace: &leapmuxv1.LayoutWorkspaceChange{
						WorkspaceId: workspace, Operation: leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_DELETE,
					}}},
				}},
			}, nil
		case "two":
			return &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "layout", Cursor: []byte("two"), HighWaterSequence: 2, AfterSequence: 2,
				ScannedSequence: 2, ScannedCursor: []byte("two"),
				End: &leapmuxv1.LayoutEventEnd{FinalSequence: 3},
			}, nil
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, "unexpected cursor")
		}
	}}
	startSpawnIPC(t, hub, nil)
	run := runControlLive(t, "events", "--workspace", workspace, "--checkpoint", path,
		"--kind", "stream.ready,workspace.deleted", "--json")
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.Equal(t, kindWorkspaceDeleted, run.next(t, ctx).Kind)
	assert.NotEqual(t, cli.ExitOK, receive(t, ctx, run.code), "the invalid Hub end must fail")
	assert.Equal(t, []string{
		leapmuxv1connect.WorkspaceServiceReadLayoutEventsProcedure,
		leapmuxv1connect.WorkspaceServiceReadLayoutEventsProcedure,
	}, hub.called())
	checkpoint, err = openEventCheckpoint(path)
	require.NoError(t, err)
	defer checkpoint.close()
	saved, err := checkpoint.read()
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Equal(t, int64(2), saved.Sequence)
	assert.Equal(t, []byte("two"), saved.Cursor)
}
