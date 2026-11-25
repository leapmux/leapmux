package cmd

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

type accountIPCWorker struct {
	cursors         chan []byte
	offlineOnResume atomic.Bool
	notFoundOnOpen  atomic.Bool
}

func (w *accountIPCWorker) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	request := new(leapmuxv1.WatchWorkerSemanticEventsRequest)
	if req.GetMethod() != leapmuxv1connect.WorkerWatchServiceWatchWorkerSemanticEventsProcedure ||
		proto.Unmarshal(req.GetPayload(), request) != nil || request.GetTabType() != checkpointTerminalType {
		_ = writer.SendError(int32(codes.InvalidArgument), "unexpected Worker request")
		return
	}
	cursors := request.GetAfterCursor()
	select {
	case w.cursors <- cursors:
	case <-ctx.Done():
		return
	}
	if len(cursors) == 0 && w.notFoundOnOpen.Swap(false) {
		_ = writer.SendStream(channelwire.NewStreamError(int32(codes.NotFound), "Worker absent at admission"))
		return
	}
	if string(cursors) == "one" && w.offlineOnResume.Swap(false) {
		_ = writer.SendStream(channelwire.NewStreamError(int32(codes.Unavailable), "Worker disconnected"))
		return
	}
	var after, high int64
	cursor, next := []byte("initial"), []byte("one")
	if string(cursors) == "initial" {
		high = 1
	}
	if string(cursors) == "one" {
		after, high = 1, 2
		cursor, next = []byte("one"), []byte("two")
	}
	frames := []*leapmuxv1.WatchWorkerSemanticEventsResponse{
		{Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Ready{Ready: &leapmuxv1.SemanticWorkerWatchReady{
			WorkerId: "worker-A", TabType: checkpointTerminalType, JournalId: "worker-journal",
			AfterSequence: after, HighWaterSequence: high, Cursor: cursor,
		}}},
		{Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Record{Record: &leapmuxv1.SemanticWorkerWatchRecord{
			Sequence: after + 1, Cursor: next, Tab: &leapmuxv1.TabIdent{TabType: checkpointTerminalType, TabId: "terminal"},
			Fact: &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_TerminalBell{TerminalBell: &leapmuxv1.TerminalBell{}}},
		}}},
	}
	for _, frame := range frames {
		raw, err := proto.Marshal(frame)
		if err != nil || writer.SendStream(&leapmuxv1.InnerStreamMessage{Payload: raw}) != nil {
			return
		}
	}
	<-ctx.Done()
}

func TestAccountCheckpointOverIPCContinuesAfterAnUnseenEnrollment(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var revision atomic.Int64
	revision.Store(1)
	worker := &accountIPCWorker{cursors: make(chan []byte, 2)}
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: revision.Load,
	}, worker)
	path := filepath.Join(t.TempDir(), "account.json")
	args := []string{"events", "--checkpoint", path, "--kind", "stream,terminal.bell", "--limit", "1", "--json"}
	first := runControlLive(t, args...)
	assert.Empty(t, receive(t, ctx, worker.cursors))
	assert.Equal(t, kindStreamReady, first.next(t, ctx).Kind)
	initial := first.next(t, ctx)
	assert.Equal(t, kindTerminalBell, initial.Kind)
	require.NotNil(t, initial.Replayed)
	assert.False(t, *initial.Replayed)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, first.code))
	assert.Equal(t, []byte("initial"), receive(t, ctx, worker.cursors))

	revision.Store(2)
	worker.offlineOnResume.Store(true)
	second := runControlLive(t, args...)
	gap := second.next(t, ctx)
	assert.Equal(t, kindStreamGap, gap.Kind)
	assert.Equal(t, "worker_membership", gap.GapReason)
	assert.Equal(t, []byte("one"), receive(t, ctx, worker.cursors))
	assert.Equal(t, kindStreamUnavailable, second.next(t, ctx).Kind)
	assert.Equal(t, kindStreamReady, second.next(t, ctx).Kind)
	assert.Equal(t, []byte("one"), receive(t, ctx, worker.cursors))
	resumed := second.next(t, ctx)
	assert.Equal(t, kindTerminalBell, resumed.Kind)
	require.NotNil(t, resumed.Replayed)
	assert.True(t, *resumed.Replayed)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, second.code))
	file, err := openEventCheckpoint(path)
	require.NoError(t, err)
	defer file.close()
	checkpoint, err := file.read()
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	assert.Equal(t, int64(2), checkpoint.Account.EnrollmentRevision)
	position := checkpoint.Account.Workers["worker-A"]["terminal"]
	require.NotNil(t, position)
	assert.Equal(t, []byte("two"), position.Cursor)
	assert.Equal(t, int64(2), position.Sequence)
}

func TestAccountCheckpointAdmitsWorkerWhenHubLayoutRefuses(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	worker := &accountIPCWorker{cursors: make(chan []byte, 4)}
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: func() int64 { return 1 },
		readLayoutEvents: func(*leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
			return nil, connect.NewError(connect.CodePermissionDenied, "workspace history refused")
		},
	}, worker)
	path := filepath.Join(t.TempDir(), "account.json")
	run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream,terminal.bell,tab.opened", "--limit", "1", "--json")
	unavailable := run.next(t, ctx)
	assert.Equal(t, kindStreamUnavailable, unavailable.Kind)
	assert.Equal(t, "hub", unavailable.Source)
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.Equal(t, kindTerminalBell, run.next(t, ctx).Kind)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
}

func TestAccountCheckpointRechecksInventoryDuringConcurrentEnrollment(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var reads atomic.Int64
	worker := &accountIPCWorker{cursors: make(chan []byte, 4)}
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: func() int64 { return min(reads.Add(1), 2) },
	}, worker)
	path := filepath.Join(t.TempDir(), "account.json")
	run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream.ready,terminal.bell", "--limit", "1", "--json")
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.Equal(t, kindTerminalBell, run.next(t, ctx).Kind)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
	assert.GreaterOrEqual(t, reads.Load(), int64(4))
}

func TestAccountCheckpointSharesTheLimitBetweenHubAndWorker(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	worker := &accountIPCWorker{cursors: make(chan []byte, 4)}
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: func() int64 { return 1 },
		readLayoutEvents: func(req *leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
			switch string(req.GetAfterCursor()) {
			case "":
				return &leapmuxv1.ReadLayoutEventsResponse{JournalId: "hub", Cursor: []byte("h0"), ScannedCursor: []byte("h0")}, nil
			case "h0":
				return &leapmuxv1.ReadLayoutEventsResponse{
					JournalId: "hub", Cursor: []byte("h0"), HighWaterSequence: 1, ScannedSequence: 1, ScannedCursor: []byte("h1"),
					Records: []*leapmuxv1.LayoutEventRecord{{
						Sequence: 1, Cursor: []byte("h1"),
						Fact: &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Workspace{Workspace: &leapmuxv1.LayoutWorkspaceChange{
							WorkspaceId: "ws-1", Operation: leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME, Title: "Renamed",
						}}},
					}},
				}, nil
			default:
				return &leapmuxv1.ReadLayoutEventsResponse{
					JournalId: "hub", Cursor: []byte("h1"), HighWaterSequence: 1, AfterSequence: 1,
					ScannedSequence: 1, ScannedCursor: []byte("h1"),
				}, nil
			}
		},
	}, worker)
	path := filepath.Join(t.TempDir(), "account.json")
	run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream.ready,workspace.renamed,terminal.bell", "--limit", "2", "--json")
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.ElementsMatch(t, []string{kindWorkspaceRenamed, kindTerminalBell}, []string{run.next(t, ctx).Kind, run.next(t, ctx).Kind})
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
	file, err := openEventCheckpoint(path)
	require.NoError(t, err)
	defer file.close()
	checkpoint, err := file.read()
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	assert.Equal(t, int64(1), checkpoint.Account.Hub.Sequence)
	assert.Equal(t, int64(1), checkpoint.Account.Workers["worker-A"]["terminal"].Sequence)
}

func TestAccountCheckpointRechecksInventoryDuringConcurrentRemoval(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var reads atomic.Int64
	worker := &accountIPCWorker{cursors: make(chan []byte, 4)}
	worker.notFoundOnOpen.Store(true)
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: func() int64 { return 2 },
		workerActiveCount: func() int64 {
			if reads.Add(1) == 1 {
				return 2
			}
			return 1
		},
	}, worker)
	path := filepath.Join(t.TempDir(), "account.json")
	run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream.ready,terminal.bell", "--limit", "1", "--json")
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.Equal(t, kindTerminalBell, run.next(t, ctx).Kind)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
	assert.GreaterOrEqual(t, reads.Load(), int64(4))
	assert.Empty(t, receive(t, ctx, worker.cursors))
	assert.Empty(t, receive(t, ctx, worker.cursors), "retry admission before acquiring a cursor")
}

type cappedAccountIPCWorkers struct {
	active        atomic.Bool
	mu            sync.Mutex
	first         string
	sendsBadReady bool
}

func (*cappedAccountIPCWorkers) CallInner(context.Context, string, userid.UserID, *leapmuxv1.InnerRpcRequest) ([]byte, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, "no unary Worker method")
}

func (w *cappedAccountIPCWorkers) StreamInner(ctx context.Context, workerID string, _ userid.UserID, req *leapmuxv1.InnerRpcRequest,
	send func(*leapmuxv1.InnerStreamMessage) error, _ func(channel.StreamController),
) error {
	if !w.active.CompareAndSwap(false, true) {
		return connect.NewError(connect.CodeUnavailable, "the Worker connection slot is busy")
	}
	defer w.active.Store(false)
	request := new(leapmuxv1.WatchWorkerSemanticEventsRequest)
	if req.GetMethod() != leapmuxv1connect.WorkerWatchServiceWatchWorkerSemanticEventsProcedure ||
		proto.Unmarshal(req.GetPayload(), request) != nil || request.GetTabType() != checkpointTerminalType {
		return connect.NewError(connect.CodeInvalidArgument, "unexpected Worker history request")
	}

	w.mu.Lock()
	if w.first == "" {
		w.first = workerID
	}
	isFirst := w.first == workerID
	w.mu.Unlock()
	journalWorker := workerID
	if isFirst && w.sendsBadReady {
		journalWorker = "unexpected-worker"
	}
	var high int64
	if !isFirst && len(request.GetAfterCursor()) != 0 {
		high = 1
	}
	frame := &leapmuxv1.WatchWorkerSemanticEventsResponse{Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Ready{
		Ready: &leapmuxv1.SemanticWorkerWatchReady{
			WorkerId: journalWorker, TabType: checkpointTerminalType, JournalId: "journal-" + workerID,
			HighWaterSequence: high, Cursor: []byte("tail"),
		},
	}}
	raw, err := proto.Marshal(frame)
	if err != nil {
		return err
	}
	if err := send(&leapmuxv1.InnerStreamMessage{Payload: raw}); err != nil {
		return err
	}
	if high > 0 {
		frame.Event = &leapmuxv1.WatchWorkerSemanticEventsResponse_Record{Record: &leapmuxv1.SemanticWorkerWatchRecord{
			Sequence: 1, Cursor: []byte("bell"), Tab: &leapmuxv1.TabIdent{TabType: checkpointTerminalType, TabId: "second-terminal"},
			Fact: &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_TerminalBell{TerminalBell: &leapmuxv1.TerminalBell{}}},
		}}
		raw, err = proto.Marshal(frame)
		if err != nil {
			return err
		}
		if err := send(&leapmuxv1.InnerStreamMessage{Payload: raw}); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestAccountCheckpointRotatesThroughAWorkerConnectionSlot(t *testing.T) {
	for _, tc := range []struct {
		title        string
		sendBadReady bool
	}{
		{title: "quiet Worker releases the slot"},
		{title: "malformed Worker does not stop another source", sendBadReady: true},
	} {
		t.Run(tc.title, func(t *testing.T) {
			ctx := testutil.DeadlineContext(t)
			workers := &cappedAccountIPCWorkers{sendsBadReady: tc.sendBadReady}
			startSpawnIPC(t, &recordingHub{
				workers: workerRows("worker-B", "worker-C", "worker-D"), workerEnrollment: func() int64 { return 3 },
			}, nil, func(r *controlipc.Router) { r.CrossWorker = workers })
			path := filepath.Join(t.TempDir(), "account.json")
			run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream.ready,terminal.bell", "--limit", "1", "--json")
			assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
			record := run.next(t, ctx)
			assert.Equal(t, kindTerminalBell, record.Kind)
			assert.Equal(t, "second-terminal", record.TabID)
			assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
			workers.mu.Lock()
			first := workers.first
			workers.mu.Unlock()
			assert.NotEqual(t, first, record.WorkerID)
		})
	}
}
