package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
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
	worker := &accountIPCWorker{cursors: make(chan []byte, 4)}
	userEvents := newFakeUserEvents()
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: revision.Load,
	}, worker, func(r *controlipc.Router) { r.HubStreams = userEvents })
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
	// Continuous uncontended streaming delivers the first event on the SAME
	// stream without tearing down and reopening between Ready and the record!
	assert.Empty(t, worker.cursors, "uncontended worker must not reopen between Ready and the first event")

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
	userEvents := newFakeUserEvents()
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: func() int64 { return 1 },
		readLayoutEvents: func(*leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
			return nil, connect.NewError(connect.CodePermissionDenied, "workspace history refused")
		},
	}, worker, func(r *controlipc.Router) { r.HubStreams = userEvents })
	path := filepath.Join(t.TempDir(), "account.json")
	run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream,terminal.bell,tab.opened", "--limit", "1", "--json")
	unavailable := run.next(t, ctx)
	assert.Equal(t, kindStreamUnavailable, unavailable.Kind)
	assert.Equal(t, "hub", unavailable.Source)
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.Equal(t, kindTerminalBell, run.next(t, ctx).Kind)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
}

func TestAccountCheckpointSharesTheLimitBetweenHubAndWorker(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	worker := &accountIPCWorker{cursors: make(chan []byte, 4)}
	userEvents := newFakeUserEvents()
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: func() int64 { return 1 },
		readLayoutEvents: func(req *leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
			return &leapmuxv1.ReadLayoutEventsResponse{JournalId: "hub", Cursor: []byte("h0"), ScannedCursor: []byte("h0")}, nil
		},
	}, worker, func(r *controlipc.Router) { r.HubStreams = userEvents })
	path := filepath.Join(t.TempDir(), "account.json")
	run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream.ready,workspace.renamed,terminal.bell", "--limit", "2", "--json")
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)

	// One WebSocket multiplexes BOTH workers and layout_journal topics!
	req := receive(t, ctx, userEvents.requests)
	assert.ElementsMatch(t, []leapmuxv1.UserEventTopic{
		leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS,
		leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL,
	}, req.GetTopics())
	assert.Equal(t, []byte("h0"), req.GetLayoutAfterCursor())
	wsSession := receive(t, ctx, userEvents.sessions)
	wsSession.send(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_LayoutEvents{
			LayoutEvents: &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "hub", Cursor: []byte("h0"), HighWaterSequence: 1, ScannedSequence: 1, ScannedCursor: []byte("h1"),
				Records: []*leapmuxv1.LayoutEventRecord{{
					Sequence: 1, Cursor: []byte("h1"),
					Fact: &leapmuxv1.LayoutEventFact{Event: &leapmuxv1.LayoutEventFact_Workspace{Workspace: &leapmuxv1.LayoutWorkspaceChange{
						WorkspaceId: "ws-1", Operation: leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME, Title: "Renamed",
					}}},
				}},
			},
		},
	})

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

func TestAccountCheckpointReconcilesWorkersOnWorkersChangedEvent(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	var mu sync.Mutex
	invRev := int64(1)
	invWorkers := []string{"worker-A"}
	userEvents := newFakeUserEvents()

	workerA := &accountIPCWorker{cursors: make(chan []byte, 4)}
	sibling := &replayWorkerB{bellReady: make(chan struct{}, 1)}
	startSpawnIPC(t, &recordingHub{
		workerInventory: func() *leapmuxv1.GetWorkerInventoryResponse {
			mu.Lock()
			defer mu.Unlock()
			return &leapmuxv1.GetWorkerInventoryResponse{
				OwnerUserId: "u-spawn",
				Revision:    invRev,
				WorkerIds:   append([]string(nil), invWorkers...),
			}
		},
	}, workerA, func(r *controlipc.Router) {
		r.HubStreams = userEvents
		r.CrossWorker = sibling
	})

	path := filepath.Join(t.TempDir(), "account-enroll.json")
	run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream,terminal.bell", "--limit", "2", "--json")
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	assert.Equal(t, kindTerminalBell, run.next(t, ctx).Kind)

	// Verify /ws/userevents opened with workers-only topic (no layout_journal).
	req := receive(t, ctx, userEvents.requests)
	assert.Equal(t, []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS}, req.GetTopics())
	wsSession := receive(t, ctx, userEvents.sessions)

	// Enroll worker-B and push WorkersChanged over WebSocket (no 2s polling!).
	mu.Lock()
	invRev = 2
	invWorkers = []string{"worker-A", "worker-B"}
	mu.Unlock()
	wsSession.send(workersChangedEvent())

	// Account replay reports membership gap, starts worker-B, and receives its bell.
	gap := run.next(t, ctx)
	assert.Equal(t, kindStreamGap, gap.Kind)
	assert.Equal(t, "worker_membership", gap.GapReason)
	secondGap := run.next(t, ctx)
	assert.Equal(t, kindStreamGap, secondGap.Kind)
	assert.Equal(t, "source_unenrolled", secondGap.GapReason)
	bellB := run.next(t, ctx)
	assert.Equal(t, kindTerminalBell, bellB.Kind)
	assert.Equal(t, "worker-B", bellB.WorkerID)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
}

type replayWorkerB struct {
	bellReady chan struct{}
}

func (*replayWorkerB) CallInner(context.Context, string, userid.UserID, *leapmuxv1.InnerRpcRequest) ([]byte, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, "unexpected unary")
}

func (w *replayWorkerB) StreamInner(ctx context.Context, workerID string, _ userid.UserID, req *leapmuxv1.InnerRpcRequest,
	send func(*leapmuxv1.InnerStreamMessage) error, _ func(channel.StreamController),
) error {
	for _, frame := range []*leapmuxv1.WatchWorkerSemanticEventsResponse{
		{Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Ready{Ready: &leapmuxv1.SemanticWorkerWatchReady{
			WorkerId: workerID, TabType: checkpointTerminalType, JournalId: "j-b",
			AfterSequence: 0, HighWaterSequence: 0, Cursor: []byte("b0"),
		}}},
		{Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Record{Record: &leapmuxv1.SemanticWorkerWatchRecord{
			Sequence: 1, Cursor: []byte("b1"), Tab: &leapmuxv1.TabIdent{TabType: checkpointTerminalType, TabId: "term-b"},
			Fact: &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_TerminalBell{TerminalBell: &leapmuxv1.TerminalBell{}}},
		}}},
	} {
		raw, err := proto.Marshal(frame)
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
			userEvents := newFakeUserEvents()
			startSpawnIPC(t, &recordingHub{
				workers: workerRows("worker-B", "worker-C", "worker-D"), workerEnrollment: func() int64 { return 3 },
			}, nil, func(r *controlipc.Router) {
				r.CrossWorker = workers
				r.HubStreams = userEvents
			})
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

func TestAccountCheckpointSnapshotWaitsForBothHubAndWorkerBacklog(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	worker := &accountIPCWorker{cursors: make(chan []byte, 4)}
	path := filepath.Join(t.TempDir(), "account-snap.json")
	file, err := openEventCheckpoint(path)
	require.NoError(t, err)
	require.NoError(t, file.save(&eventCheckpoint{
		Version: eventCheckpointVersion, Scope: accountCheckpointScope,
		Filter: []string{kindWorkspaceRenamed, kindTerminalBell},
		Account: &accountEventCheckpoint{
			OwnerID: "u-spawn", EnrollmentRevision: 1,
			Hub: &eventHistoryPosition{JournalID: "hub", Sequence: 0, Cursor: []byte("h0")},
			Workers: map[string]accountWorkerPositions{
				"worker-A": {"terminal": {JournalID: "worker-journal", Sequence: 0, Cursor: []byte("initial")}},
			},
		},
	}))
	file.close()

	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: func() int64 { return 1 },
		readLayoutEvents: func(req *leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
			return &leapmuxv1.ReadLayoutEventsResponse{
				JournalId: "hub", Cursor: []byte("h0"), AfterSequence: 0, HighWaterSequence: 0,
				ScannedSequence: 0, ScannedCursor: []byte("h0"),
			}, nil
		},
	}, worker)

	run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream.ready,workspace.renamed,terminal.bell", "--snapshot", "--json")
	assert.Equal(t, kindStreamReady, run.next(t, ctx).Kind)
	bell := run.next(t, ctx)
	assert.Equal(t, kindTerminalBell, bell.Kind)
	assert.Empty(t, bell.Source, "normal Worker records must not set Source")
	require.NotNil(t, bell.Replayed)
	assert.True(t, *bell.Replayed)
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
}

func TestAccountCheckpointNoReconnectExitsWhenAllSourcesFailRetryably(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	worker := &accountIPCWorker{cursors: make(chan []byte, 4)}
	worker.notFoundOnOpen.Store(true)
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-A"), workerEnrollment: func() int64 { return 1 },
		readLayoutEvents: func(*leapmuxv1.ReadLayoutEventsRequest) (*leapmuxv1.ReadLayoutEventsResponse, error) {
			return nil, connect.NewError(connect.CodeUnavailable, "hub temporarily unavailable")
		},
	}, worker)

	path := filepath.Join(t.TempDir(), "account-noreconnect.json")
	run := runControlLive(t, "events", "--checkpoint", path, "--kind", "stream,tab.opened,terminal.bell", "--reconnect=false", "--json")
	first := run.next(t, ctx)
	second := run.next(t, ctx)
	assert.Equal(t, kindStreamUnavailable, first.Kind)
	assert.Equal(t, kindStreamUnavailable, second.Kind)
	require.NotNil(t, first.Retrying)
	require.NotNil(t, second.Retrying)
	assert.False(t, *first.Retrying)
	assert.False(t, *second.Retrying)
	assert.NotEqual(t, cli.ExitOK, receive(t, ctx, run.code))
}

func TestAccountReplayAllSourcesStoppedIsDeterministic(t *testing.T) {
	t.Parallel()
	errA := cli.Newf(cli.CodeUnavailable, "worker-A agent down")
	errB := cli.Newf(cli.CodeUnavailable, "worker-B terminal down")
	r := &accountReplay{
		workers: map[workerHistoryKey]*accountWorkerWatch{
			{id: "worker-B", tabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL}: {
				replaySourceState: replaySourceState{stopped: errB},
			},
			{id: "worker-A", tabType: leapmuxv1.TabType_TAB_TYPE_AGENT}: {
				replaySourceState: replaySourceState{stopped: errA},
			},
		},
	}
	for range 32 {
		assert.Equal(t, errA, r.allSourcesStopped())
	}
}

func TestReadAccountInventoryUsesSingleAtomicRPC(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	hub := &recordingHub{
		workers: workerRows("worker-B", "worker-A"), workerEnrollment: func() int64 { return 7 },
	}
	startSpawnIPC(t, hub, nil)
	cmd := newEventsCommand()
	inv := &cli.Invocation{Context: ctx, Command: cmd, Printer: &cli.Printer{}}
	s, err := openStreamSession(inv)
	require.NoError(t, err)
	defer s.Close()

	inventory, err := readAccountInventory(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, "u-spawn", inventory.owner)
	assert.Equal(t, int64(7), inventory.revision)
	assert.Equal(t, []string{"worker-A", "worker-B"}, inventory.workerIDs)
	assert.Equal(t, []string{
		leapmuxv1connect.WorkerManagementServiceGetWorkerInventoryProcedure,
	}, hub.called())
}

// TestAccountCheckpointStaysConnectedWithoutReopeningWhenUncontended verifies
// both EFFICIENCY-1 (zero re-opens during steady-state multi-event delivery
// when slots are uncontended) and per-Worker E2EE channel multiplexing (ONE
// channel carries BOTH agent and terminal WatchWorkerSemanticEvents streams).
func TestAccountCheckpointStaysConnectedWithoutReopeningWhenUncontended(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()

	var streamOpens atomic.Int32
	agentPush := make(chan int64, 4)
	termPush := make(chan int64, 4)
	hub, client := startTestEventsChannelHub(t, func(d *channel.Dispatcher) {
		d.Handle(leapmuxv1connect.WorkerWatchServiceWatchWorkerSemanticEventsProcedure, channel.Registration{},
			func(sctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
				streamOpens.Add(1)
				var in leapmuxv1.WatchWorkerSemanticEventsRequest
				if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
					_ = w.SendError(int32(codes.InvalidArgument), err.Error())
					return
				}
				send := func(frame *leapmuxv1.WatchWorkerSemanticEventsResponse) error {
					raw, err := proto.Marshal(frame)
					if err != nil {
						return err
					}
					return w.SendStream(&leapmuxv1.InnerStreamMessage{Payload: raw})
				}
				if err := send(&leapmuxv1.WatchWorkerSemanticEventsResponse{
					Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Ready{
						Ready: &leapmuxv1.SemanticWorkerWatchReady{
							WorkerId: "worker-A", TabType: in.GetTabType(), JournalId: "j-" + tabTypeName(in.GetTabType()),
							AfterSequence: 0, HighWaterSequence: 0, Cursor: []byte("c0"),
						},
					},
				}); err != nil {
					return
				}
				pushCh := termPush
				if in.GetTabType() == leapmuxv1.TabType_TAB_TYPE_AGENT {
					pushCh = agentPush
				}
				for {
					select {
					case seq := <-pushCh:
						var fact *leapmuxv1.SemanticTabFact
						if in.GetTabType() == leapmuxv1.TabType_TAB_TYPE_AGENT {
							fact = &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_AgentTurnEnd{AgentTurnEnd: &leapmuxv1.AgentTurnEnd{}}}
						} else {
							fact = &leapmuxv1.SemanticTabFact{Event: &leapmuxv1.SemanticTabFact_TerminalBell{TerminalBell: &leapmuxv1.TerminalBell{}}}
						}
						if err := send(&leapmuxv1.WatchWorkerSemanticEventsResponse{
							Event: &leapmuxv1.WatchWorkerSemanticEventsResponse_Record{
								Record: &leapmuxv1.SemanticWorkerWatchRecord{
									Sequence: seq, Cursor: []byte{byte('0' + seq)},
									Tab:  &leapmuxv1.TabIdent{TabType: in.GetTabType(), TabId: "tab-" + tabTypeName(in.GetTabType())},
									Fact: fact,
								},
							},
						}); err != nil {
							return
						}
					case <-sctx.Done():
						return
					}
				}
			})
	})

	path := filepath.Join(t.TempDir(), "steady.json")
	file, err := openEventCheckpoint(path)
	require.NoError(t, err)
	defer file.close()

	filter, err := parseKindFilter([]string{kindStreamReady, kindAgentTurnEnd, kindTerminalBell})
	require.NoError(t, err)
	out := newLineSink()
	r := &accountReplay{
		session:        &session{c: client},
		ctx:            ctx,
		opts:           eventsOptions{filter: filter, limit: 4},
		printer:        newEventPrinter(&cli.Printer{Out: out, Err: newLineSink(), Format: cli.FormatJSON}, filter, 4),
		clock:          testutil.NewQuartzMock(t),
		file:           file,
		state:          &eventCheckpoint{Version: eventCheckpointVersion, Scope: accountCheckpointScope, Filter: []string{kindAgentTurnEnd, kindTerminalBell}, Account: &accountEventCheckpoint{OwnerID: hubChannelUser, Workers: make(map[string]accountWorkerPositions)}},
		kinds:          []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL},
		workers:        make(map[workerHistoryKey]*accountWorkerWatch),
		workerChannels: newSharedWorkerChannels(client),
		delivery:       make(chan accountDelivery),
		readySignal:    make(chan struct{}),
		workerSlots:    make(chan struct{}, detailFanoutLimit),
		slotBudget:     detailFanoutLimit,
		rotationMode:   make(chan struct{}),
	}
	require.NoError(t, r.applyInventory(accountInventory{owner: hubChannelUser, revision: 1, workerIDs: []string{"worker-A"}}, true))

	done := make(chan error, 1)
	go func() {
		done <- r.run()
	}()
	defer func() {
		cancel()
		r.sources.Wait()
	}()

	ready := decodeEventLine(t, receive(t, ctx, out.lines))
	require.Equal(t, kindStreamReady, ready.Kind)

	// Deliver 2 agent events and 2 terminal events across the steady-state streams.
	agentPush <- 1
	termPush <- 1
	agentPush <- 2
	termPush <- 2

	var gotKinds []string
	for range 4 {
		gotKinds = append(gotKinds, decodeEventLine(t, receive(t, ctx, out.lines)).Kind)
	}
	require.NoError(t, receive(t, ctx, done))
	assert.ElementsMatch(t, []string{kindAgentTurnEnd, kindAgentTurnEnd, kindTerminalBell, kindTerminalBell}, gotKinds)
	assert.Equal(t, int32(1), hub.opens.Load(), "both agent and terminal streams must multiplex on ONE E2EE channel with zero re-opens")
	assert.Equal(t, int32(2), streamOpens.Load(), "each tab family opens exactly one continuous stream with zero re-opens")
}

func TestIsSlotContentionError(t *testing.T) {
	t.Parallel()
	assert.False(t, isSlotContentionError(nil))
	assert.False(t, isSlotContentionError(errors.New("network reset")))
	assert.False(t, isSlotContentionError(websocket.CloseError{Code: websocket.StatusNormalClosure, Reason: "normal"}))
	assert.True(t, isSlotContentionError(websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: contracts.CloseReasonTooManyConnections}))
	assert.True(t, isSlotContentionError(fmt.Errorf("dial relay: %w", websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: contracts.CloseReasonTooManyConnections})))
	assert.True(t, isSlotContentionError(errors.New("the Worker connection slot is busy")))
}

func TestAccountWorkerFailure(t *testing.T) {
	t.Parallel()

	nilErr := accountWorkerFailure(nil)
	require.NotNil(t, nilErr)
	assert.ErrorIs(t, nilErr, io.EOF)
	assert.Equal(t, cli.CodeInternal, nilErr.Code)

	ctxDeadline := accountWorkerFailure(context.DeadlineExceeded)
	assert.Equal(t, cli.CodeUnavailable, ctxDeadline.Code)
	assert.Contains(t, ctxDeadline.Message, "timed out")

	rpcDeadlineErr := accountWorkerFailure(connect.NewError(connect.CodeDeadlineExceeded, "rpc slow"))
	assert.Equal(t, cli.CodeUnavailable, rpcDeadlineErr.Code)
	assert.Contains(t, rpcDeadlineErr.Message, "timed out")

	notFound := accountWorkerFailure(connect.NewError(connect.CodeNotFound, "worker missing"))
	assert.Equal(t, cli.CodeUnavailable, notFound.Code)
	assert.Contains(t, notFound.Message, "worker missing")
}

func TestAccountCheckpointMismatchGuards(t *testing.T) {
	t.Run("startHub returns CodeConflict when Hub kinds deselected with saved Hub position", func(t *testing.T) {
		filter, err := parseKindFilter([]string{kindTerminalBell})
		require.NoError(t, err)
		r := &accountReplay{
			opts: eventsOptions{filter: filter},
			state: &eventCheckpoint{
				Version: eventCheckpointVersion,
				Scope:   accountCheckpointScope,
				Filter:  []string{kindTerminalBell},
				Account: &accountEventCheckpoint{
					OwnerID: "u-spawn",
					Hub:     &eventHistoryPosition{JournalID: "hub", Sequence: 1, Cursor: []byte("h1")},
					Workers: map[string]accountWorkerPositions{},
				},
			},
		}
		err = r.startHub(false)
		require.Error(t, err)
		assert.Equal(t, cli.CodeConflict, cli.FromError(err).Code)
		assert.Contains(t, err.Error(), "Hub position")
	})

	t.Run("runAccountCheckpointedEvents returns CodeConflict on owner mismatch", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "account-owner-mismatch.json")
		file, err := openEventCheckpoint(path)
		require.NoError(t, err)
		require.NoError(t, file.save(&eventCheckpoint{
			Version: eventCheckpointVersion,
			Scope:   accountCheckpointScope,
			Filter:  []string{kindTerminalBell},
			Account: &accountEventCheckpoint{
				OwnerID:            "other-user",
				EnrollmentRevision: 1,
				Workers:            map[string]accountWorkerPositions{},
			},
		}))
		file.close()

		startSpawnIPC(t, &recordingHub{
			workers:          workerRows("worker-A"),
			workerEnrollment: func() int64 { return 1 },
		}, nil)

		f := runControl(t, "events", "--checkpoint", path, "--kind", "stream,terminal.bell").requireFailure(t, cli.CodeConflict)
		assert.Contains(t, f.Message, "different account")
	})
}
