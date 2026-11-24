package cmd

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hubrpc"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// fakeUserEvents serves the hub's user events to the router of the control
// socket, as the Worker's HubEventStreamer does: each subscription announces
// itself on sessions, then sends the steps that the test gives it.
type fakeUserEvents struct {
	sessions chan *fakeHubSession
	// workspaceIDs receives the filter of each subscription.
	workspaceIDs chan []string
}

func newFakeUserEvents() *fakeUserEvents {
	return &fakeUserEvents{sessions: make(chan *fakeHubSession, 8), workspaceIDs: make(chan []string, 8)}
}

func (f *fakeUserEvents) StreamHub(ctx context.Context, _ userid.UserID, procedure string, payload []byte, onPayload func([]byte) error) error {
	if procedure != hubrpc.WatchUserStream {
		return fmt.Errorf("unexpected hub stream %s", procedure)
	}
	var req leapmuxv1.WatchUserRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return err
	}
	f.workspaceIDs <- req.GetWorkspaceIds()
	s := &fakeHubSession{steps: make(chan hubStep, 32)}
	f.sessions <- s
	for {
		select {
		case step := <-s.steps:
			if step.end {
				return step.err
			}
			raw, err := proto.Marshal(step.event)
			if err != nil {
				return err
			}
			if err := onPayload(raw); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// fakeWatch is one WatchEvents stream that an eventsWorker serves. Its
// handler goroutine sends every frame: the IPC server closes the stream
// when the handler returns, and nothing orders a send from another
// goroutine before that close.
type fakeWatch struct {
	// requests carries the opening request and each revision to the
	// handler, which acknowledges each one.
	requests chan *leapmuxv1.WatchEventsRequest
	// seen receives each request after its acknowledgement went out.
	seen   chan *leapmuxv1.WatchEventsRequest
	frames chan *leapmuxv1.WatchEventsResponse
	// fail ends the stream with an in-band error.
	fail chan *leapmuxv1.RpcError
}

// watchRevisions is the controller that hands each revision of the client
// to the handler goroutine.
type watchRevisions struct{ watch *fakeWatch }

func (c watchRevisions) OnClientFrame(payload []byte) {
	var req leapmuxv1.WatchEventsRequest
	if proto.Unmarshal(payload, &req) == nil {
		c.watch.requests <- &req
	}
}

func (watchRevisions) OnCancel() {}

// eventsWorker is a fake Worker for `events`: it serves notify watches and
// answers ListAgents from its rows. mu guards agents.
type eventsWorker struct {
	mu      sync.Mutex
	agents  map[string]*leapmuxv1.AgentInfo
	watches chan *fakeWatch
}

func newEventsWorker(rows ...*leapmuxv1.AgentInfo) *eventsWorker {
	w := &eventsWorker{agents: map[string]*leapmuxv1.AgentInfo{}, watches: make(chan *fakeWatch, 8)}
	for _, row := range rows {
		w.agents[row.GetId()] = row
	}
	return w
}

func (w *eventsWorker) setAgent(row *leapmuxv1.AgentInfo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.agents[row.GetId()] = row
}

// listAgents answers ListAgents with the rows of the agents that it asks
// for.
func (w *eventsWorker) listAgents(payload []byte) *leapmuxv1.ListAgentsResponse {
	var in leapmuxv1.ListAgentsRequest
	_ = proto.Unmarshal(payload, &in)
	resp := &leapmuxv1.ListAgentsResponse{}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range in.GetTabIds() {
		if row, ok := w.agents[id]; ok {
			resp.Agents = append(resp.Agents, proto.Clone(row).(*leapmuxv1.AgentInfo))
		}
	}
	return resp
}

// serveWatch serves one WatchEvents stream until ctx ends or the test fails
// it. bind installs the controller of the revisions.
func (w *eventsWorker) serveWatch(ctx context.Context, payload []byte,
	send func(*leapmuxv1.InnerStreamMessage) error,
	bind func(channel.StreamController) (func(), bool),
) {
	var opening leapmuxv1.WatchEventsRequest
	if err := proto.Unmarshal(payload, &opening); err != nil {
		_ = send(channelwire.NewStreamError(int32(codes.InvalidArgument), err.Error()))
		return
	}
	watch := &fakeWatch{
		requests: make(chan *leapmuxv1.WatchEventsRequest, 16),
		seen:     make(chan *leapmuxv1.WatchEventsRequest, 16),
		frames:   make(chan *leapmuxv1.WatchEventsResponse, 32),
		fail:     make(chan *leapmuxv1.RpcError, 1),
	}
	watch.requests <- &opening
	release, _ := bind(watchRevisions{watch: watch})
	defer release()
	w.watches <- watch
	frame := func(resp *leapmuxv1.WatchEventsResponse) error {
		raw, err := proto.Marshal(resp)
		if err != nil {
			return err
		}
		return send(&leapmuxv1.InnerStreamMessage{Payload: raw})
	}
	for {
		select {
		case req := <-watch.requests:
			if frame(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{
				UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: req.GetUpdateId()},
			}}) != nil {
				return
			}
			watch.seen <- req
		case resp := <-watch.frames:
			if frame(resp) != nil {
				return
			}
		case rpcErr := <-watch.fail:
			_ = send(channelwire.NewStreamError(rpcErr.GetCode(), rpcErr.GetMessage()))
			return
		case <-ctx.Done():
			return
		}
	}
}

// DispatchWith serves the Worker that runs the socket: worker-A.
func (w *eventsWorker) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure:
		w.serveWatch(ctx, req.GetPayload(), rw.SendStream, rw.BindStream)
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		writeProtoResponse(rw, w.listAgents(req.GetPayload()))
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

// siblingWorkers reaches the other Workers through the router, as the
// Worker's cross-worker client does over the hub's relay.
type siblingWorkers struct {
	workers map[string]*eventsWorker
}

func (s siblingWorkers) CallInner(_ context.Context, target string, _ userid.UserID, req *leapmuxv1.InnerRpcRequest) ([]byte, error) {
	w := s.workers[target]
	if w == nil || req.GetMethod() != leapmuxv1connect.WorkerAgentServiceListAgentsProcedure {
		return nil, fmt.Errorf("unexpected call %s on %s", req.GetMethod(), target)
	}
	return proto.Marshal(w.listAgents(req.GetPayload()))
}

func (s siblingWorkers) StreamInner(ctx context.Context, target string, _ userid.UserID, req *leapmuxv1.InnerRpcRequest,
	onMsg func(*leapmuxv1.InnerStreamMessage), bindCtrl func(channel.StreamController),
) error {
	w := s.workers[target]
	if w == nil || req.GetMethod() != leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure {
		return fmt.Errorf("unexpected stream %s on %s", req.GetMethod(), target)
	}
	w.serveWatch(ctx, req.GetPayload(),
		func(m *leapmuxv1.InnerStreamMessage) error {
			onMsg(m)
			return nil
		},
		func(ctrl channel.StreamController) (func(), bool) {
			bindCtrl(ctrl)
			return func() {}, true
		})
	return nil
}

// liveRun is one `leapmux control …` run whose output a test reads line by
// line while it runs.
type liveRun struct {
	out    *lineSink
	errOut *lineSink
	cancel context.CancelFunc
	code   chan int
}

// runControlLive starts `leapmux control <args...>` through cli.Execute, the
// path that the binary takes. Cancelling the run stops it as Ctrl-C does.
func runControlLive(t *testing.T, args ...string) *liveRun {
	t.Helper()
	root := NewCommand()
	ctx, cancel := context.WithCancel(context.Background())
	run := &liveRun{out: newLineSink(), errOut: newLineSink(), cancel: cancel, code: make(chan int, 1)}
	prevErr := control.Err
	control.Err = run.errOut
	finished := make(chan struct{})
	go func() {
		run.code <- cli.Execute(ctx, root, args, strings.NewReader(""), run.out, run.errOut)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("the command did not stop")
		}
		control.Err = prevErr
	})
	return run
}

// next waits for the next JSON event of the run.
func (r *liveRun) next(t *testing.T, ctx context.Context) streamEvent {
	t.Helper()
	return decodeEventLine(t, receive(t, ctx, r.out.lines))
}

// startEventsIPC serves worker-A on the control socket, worker-B behind it,
// and the hub's user events, as a tab of worker-A sees them.
func startEventsIPC(t *testing.T, workerA, workerB *eventsWorker) *fakeUserEvents {
	t.Helper()
	userEvents := newFakeUserEvents()
	startSpawnIPC(t, &recordingHub{}, workerA, func(r *controlipc.Router) {
		r.HubStreams = userEvents
		r.CrossWorker = siblingWorkers{workers: map[string]*eventsWorker{"worker-B": workerB}}
	})
	return userEvents
}

// The whole stream over the socket of a tab: the merge of two Workers, a tab
// that joins and one that leaves, and a Worker stream that drops and comes
// back with its gap. Each step waits for the line of the step before it, so
// the order of the lines is the order of the steps.
func TestEventsOverIPC_PrintsTheMergedStream(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	workerA := newEventsWorker(agentLevelRow("ag-1", statusActive, idle))
	workerB := newEventsWorker()
	userEvents := startEventsIPC(t, workerA, workerB)

	run := runControlLive(t, "events", "--json")
	assert.Empty(t, receive(t, ctx, userEvents.workspaceIDs), "the stream watches every workspace")
	hub := receive(t, ctx, userEvents.sessions)
	hub.send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A"), terminalTabRecord("tm-1", "ws-1", "worker-B")))

	var watchA, watchB *fakeWatch
	for watchA == nil || watchB == nil {
		select {
		case watchA = <-workerA.watches:
			assert.Equal(t, []string{"ag-1"}, watchedAgents(t, receive(t, ctx, watchA.seen)))
		case watchB = <-workerB.watches:
			assert.Equal(t, []string{"tm-1"}, watchedTerminals(t, receive(t, ctx, watchB.seen)))
		case <-ctx.Done():
			t.Fatal("the stream did not watch both Workers")
		}
	}
	ready := run.next(t, ctx)
	require.Equal(t, kindStreamReady, ready.Kind)
	assert.Equal(t, &scopeCounts{Workspaces: 1, Tabs: 2, Workers: 2, WorkersLive: 2, HubLive: true}, ready.Scope)

	watchA.frames <- turnEndFrame("ag-1")
	turnEnd := run.next(t, ctx)
	assert.Equal(t, []string{kindAgentTurnEnd, "ag-1", "worker-A", "ws-1"}, []string{turnEnd.Kind, turnEnd.TabID, turnEnd.WorkerID, turnEnd.WorkspaceID})

	watchB.frames <- bellFrame("tm-1")
	bell := run.next(t, ctx)
	assert.Equal(t, []string{kindTerminalBell, "tm-1", "worker-B"}, []string{bell.Kind, bell.TabID, bell.WorkerID},
		"a Worker behind the socket's own reaches the stream through the router")

	workerA.setAgent(agentLevelRow("ag-2", statusActive, working))
	hub.send(materializedTab(agentTabRecord("ag-2", "ws-1", "worker-A")))
	hub.send(batchEndEvent())
	opened := run.next(t, ctx)
	assert.Equal(t, []string{kindTabOpened, "ag-2"}, []string{opened.Kind, opened.TabID})
	assert.Equal(t, []string{"ag-1", "ag-2"}, watchedAgents(t, receive(t, ctx, watchA.seen)), "the revision rides the open stream")
	status, activity := run.next(t, ctx), run.next(t, ctx)
	assert.Equal(t, []string{kindAgentStatus, "ag-2", "active"}, []string{status.Kind, status.TabID, status.Status})
	assert.Equal(t, []string{kindAgentActivity, "ag-2", "working"}, []string{activity.Kind, activity.TabID, activity.State})

	hub.send(batchEvent(closeTabOp(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "tm-1", 20)))
	hub.send(batchEndEvent())
	closed := run.next(t, ctx)
	assert.Equal(t, []string{kindTabClosed, "tm-1"}, []string{closed.Kind, closed.TabID})

	workerA.setAgent(agentLevelRow("ag-1", statusActive, waiting))
	watchA.fail <- &leapmuxv1.RpcError{Code: int32(codes.Unavailable), Message: "the worker restarts"}
	down := run.next(t, ctx)
	assert.Equal(t, kindStreamUnavailable, down.Kind)
	assert.Equal(t, "worker-A", down.WorkerID)
	assert.Equal(t, string(cli.CodeUnavailable), down.Code)
	assert.Equal(t, "the worker restarts", down.Message)

	again := receive(t, ctx, workerA.watches)
	assert.Equal(t, []string{"ag-1", "ag-2"}, watchedAgents(t, receive(t, ctx, again.seen)), "the new stream states the whole interest")
	gap := run.next(t, ctx)
	assert.Equal(t, kindStreamGap, gap.Kind)
	assert.Equal(t, "worker-A", gap.WorkerID)
	assert.Equal(t, []string{"ag-1", "ag-2"}, gap.TabIDs)
	assert.Equal(t, lostKinds(sourceWorker, false), gap.Lost)
	reconciled := run.next(t, ctx)
	assert.Equal(t, []string{kindAgentActivity, "ag-1", "waiting_for_user"}, []string{reconciled.Kind, reconciled.TabID, reconciled.State})
	assert.True(t, reconciled.Reconciled)

	again.frames <- turnEndFrame("ag-2")
	assert.Equal(t, kindAgentTurnEnd, run.next(t, ctx).Kind, "the stream delivers again")

	run.cancel()
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code), "the end of the run is no failure")
}

// --workspace resolves its input before the stream starts. The hub streams the
// selected workspace alone, the stream watches only its tabs, and the deletion
// of the workspace ends the run with exit status 0.
func TestEventsOverIPC_WatchesTheWorkspaceThatItSelects(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	workerA := newEventsWorker(agentLevelRow("ag-1", statusActive, idle), agentLevelRow("ag-9", statusActive, idle))
	userEvents := startEventsIPC(t, workerA, newEventsWorker())

	run := runControlLive(t, "events", "--workspace", "title:First", "--json")
	assert.Equal(t, []string{"ws-1"}, receive(t, ctx, userEvents.workspaceIDs), "the hub streams the resolved workspace alone")
	hub := receive(t, ctx, userEvents.sessions)
	hub.send(snapshotEvent([]string{"ws-1", "ws-2"}, agentTabRecord("ag-1", "ws-1", "worker-A"), agentTabRecord("ag-9", "ws-2", "worker-A")))
	watch := receive(t, ctx, workerA.watches)
	assert.Equal(t, []string{"ag-1"}, watchedAgents(t, receive(t, ctx, watch.seen)), "a tab of another workspace is not watched")
	ready := run.next(t, ctx)
	require.Equal(t, kindStreamReady, ready.Kind)
	assert.Equal(t, &scopeCounts{Workspaces: 1, Tabs: 1, Workers: 1, WorkersLive: 1, HubLive: true}, ready.Scope)

	hub.send(workspaceDeletedEvent("ws-1"))
	deleted := run.next(t, ctx)
	assert.Equal(t, []string{kindWorkspaceDeleted, "ws-1"}, []string{deleted.Kind, deleted.WorkspaceID})
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code), "nothing can happen in a deleted workspace")
}

func TestEventsOverIPC_WatchesOnlyTheSelectedTabAcrossAMove(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	worker := newEventsWorker()
	userEvents := startEventsIPC(t, worker, newEventsWorker())
	run := runControlLive(t, "events", "--tab", "id:tm-1", "--json")
	var hub *fakeHubSession
	select {
	case hub = <-userEvents.sessions:
	case code := <-run.code:
		t.Fatalf("events exited with code %d before opening the stream", code)
	case <-ctx.Done():
		t.Fatal("events did not open the Hub stream")
	}
	assert.Empty(t, receive(t, ctx, userEvents.workspaceIDs), "the Hub subscription follows the tab across workspaces")
	hub.send(snapshotEvent([]string{"ws-1", "ws-2"},
		terminalTabRecord("tm-1", "ws-1", "worker-A"), terminalTabRecord("tm-2", "ws-1", "worker-A")))
	watch := receive(t, ctx, worker.watches)
	assert.Equal(t, []string{"tm-1"}, watchedTerminals(t, receive(t, ctx, watch.seen)))
	ready := run.next(t, ctx)
	require.Equal(t, kindStreamReady, ready.Kind)
	assert.Equal(t, &scopeCounts{Workspaces: 1, Tabs: 1, Workers: 1, WorkersLive: 1, HubLive: true}, ready.Scope)

	watch.frames <- bellFrame("tm-2")
	watch.frames <- bellFrame("tm-1")
	bell := run.next(t, ctx)
	assert.Equal(t, []string{kindTerminalBell, "tm-1", "ws-1"}, []string{bell.Kind, bell.TabID, bell.WorkspaceID})

	hub.send(workspaceRenamedEvent("ws-2", "Unrelated change"))
	hub.send(batchEvent(moveTabOp(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "tm-1", "ws-2", 20)))
	hub.send(batchEndEvent())
	moved := run.next(t, ctx)
	assert.Equal(t, []string{kindTabMoved, "tm-1", "ws-2"}, []string{moved.Kind, moved.TabID, moved.WorkspaceID})
	watch.frames <- bellFrame("tm-1")
	assert.Equal(t, "ws-2", run.next(t, ctx).WorkspaceID)

	hub.send(batchEvent(closeTabOp(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "tm-1", 21)))
	hub.send(batchEndEvent())
	closed := run.next(t, ctx)
	assert.Equal(t, []string{kindTabClosed, "tm-1"}, []string{closed.Kind, closed.TabID})
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
}

func TestEventsOverIPC_RefusesATabOutsideTheSelectedWorkspace(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	worker := newEventsWorker()
	userEvents := startEventsIPC(t, worker, newEventsWorker())
	run := runControlLive(t, "events", "--tab", "id:tm-1", "--workspace", "title:First", "--json")
	var hub *fakeHubSession
	select {
	case hub = <-userEvents.sessions:
	case code := <-run.code:
		t.Fatalf("events exited with code %d before receiving the workspace snapshot", code)
	case <-ctx.Done():
		t.Fatal("events did not open the Hub stream")
	}
	assert.Equal(t, []string{"ws-1"}, receive(t, ctx, userEvents.workspaceIDs))
	hub.send(snapshotEvent([]string{"ws-1", "ws-2"}, terminalTabRecord("tm-1", "ws-2", "worker-A")))
	assert.Equal(t, cli.ExitNotFound, receive(t, ctx, run.code))
	assert.Contains(t, receive(t, ctx, run.out.lines), `"code":"not_found"`)
	assert.Empty(t, worker.watches, "an out-of-scope tab cannot start a Worker watch")
}

func TestEventsOverIPC_ClosesTheSelectedTabWhenItsWorkspaceIsDeleted(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	userEvents := startEventsIPC(t, newEventsWorker(), newEventsWorker())
	run := runControlLive(t, "events", "--tab", "id:tm-1", "--json")
	hub := receive(t, ctx, userEvents.sessions)
	hub.send(snapshotEvent([]string{"ws-1"}, terminalTabRecord("tm-1", "ws-1", "worker-A")))
	require.Equal(t, kindStreamReady, run.next(t, ctx).Kind)

	hub.send(workspaceDeletedEvent("ws-1"))
	closed := run.next(t, ctx)
	assert.Equal(t, []string{kindTabClosed, "tm-1", "ws-1"}, []string{closed.Kind, closed.TabID, closed.WorkspaceID})
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
}

// --workspace refuses an input that selects no workspace before the stream
// starts, so the run fails at once instead of watching nothing.
func TestEventsOverIPC_RefusesAWorkspaceThatDoesNotExist(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	userEvents := startEventsIPC(t, newEventsWorker(), newEventsWorker())

	run := runControlLive(t, "events", "--workspace", "title:Missing", "--json")
	assert.Equal(t, cli.ExitNotFound, receive(t, ctx, run.code))
	failure := receive(t, ctx, run.out.lines)
	assert.Contains(t, failure, `"code":"not_found"`)
	select {
	case ids := <-userEvents.workspaceIDs:
		t.Fatalf("the stream subscribed to the hub with %v", ids)
	default:
	}
}

// The text form prints one line per event: the local time, the kind, the
// short ids, and the summary. --limit ends the run with exit status 0.
func TestEventsOverIPC_PrintsTextLines(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	workerA := newEventsWorker(agentLevelRow("ag-1", statusActive, idle))
	userEvents := startEventsIPC(t, workerA, newEventsWorker())

	run := runControlLive(t, "events", "--kind", "agent", "--limit", "2")
	hub := receive(t, ctx, userEvents.sessions)
	hub.send(snapshotEvent([]string{"workspace-1"}, agentTabRecord("agent-tab-1", "workspace-1", "worker-A")))
	watch := receive(t, ctx, workerA.watches)
	receive(t, ctx, watch.seen)
	assert.Contains(t, receive(t, ctx, run.errOut.lines), " stream.ready watching 1 workspace and 1 tab on 1 worker",
		"--kind does not select stream.ready, so it prints on stderr")

	three := int32(3)
	watch.frames <- activityChangedFrame("agent-tab-1", working)
	watch.frames <- agentFrame("agent-tab-1", &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ActivityChanged{
		ActivityChanged: &leapmuxv1.AgentActivityChanged{State: idle, NumToolUses: &three},
	}})
	stamp := `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}(Z|[+-]\d{2}:\d{2})`
	assert.Regexp(t, regexp.MustCompile(`^`+stamp+` agent\.activity workspace=workspac tab=agent-ta worker=worker-A working$`), receive(t, ctx, run.out.lines))
	assert.Regexp(t, regexp.MustCompile(`^`+stamp+` agent\.activity workspace=workspac tab=agent-ta worker=worker-A idle \(3 tool calls\)$`), receive(t, ctx, run.out.lines))
	assert.Equal(t, cli.ExitOK, receive(t, ctx, run.code))
	select {
	case line := <-run.out.lines:
		t.Fatalf("a line after the limit: %s", line)
	default:
	}
}

// Flags that cannot run fail with usage before the command reaches the hub.
func TestEvents_RefusesFlagsThatCannotRun(t *testing.T) {
	clearRemoteEnv(t)
	for _, tc := range []struct {
		args []string
		text string
	}{
		{[]string{"--kind", "agent.nope"}, `unknown event kind "agent.nope"`},
		{[]string{"--limit", "-1"}, "--limit"},
		{[]string{"--heartbeat", "-1s"}, "--heartbeat"},
		{[]string{"--heartbeat", "10ms"}, "at least 1s"},
		{[]string{"--quiet"}, "--quiet"},
		{[]string{"--workspace", " "}, "--workspace is empty"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			o := runControl(t, append([]string{"events", "--hub", "https://stub.invalid"}, tc.args...)...)
			f := o.requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.text)
		})
	}
	o := runControl(t, "events", "--kind", "nope", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, cli.CodeUsage)
	require.Len(t, f.Hints, 1)
	assert.Contains(t, f.Hints[0], "agent.request_ended", "the hint lists the kinds")
}

// A hub stream that fails before its first event fails the command at once,
// with the hub's error and its exit status, rather than a stream that waits
// for ever.
func TestEventsOverIPC_FailsWhenTheHubStreamFailsFirst(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	userEvents := startEventsIPC(t, newEventsWorker(), newEventsWorker())
	run := runControlLive(t, "events", "--json")
	receive(t, ctx, userEvents.sessions).end(connect.NewError(connect.CodeUnavailable, "the hub is unreachable"))
	assert.Equal(t, cli.ExitUnavailable, receive(t, ctx, run.code))
	failure := receive(t, ctx, run.out.lines)
	assert.Contains(t, failure, `"code":"unavailable"`)
	assert.Contains(t, failure, "the hub is unreachable")
}
