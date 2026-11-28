package cmd

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

type childCloseWorker struct {
	mu                   sync.Mutex
	agents               []*leapmuxv1.AgentInfo
	closedTabs           []string
	closeReqs            []*leapmuxv1.CloseTabRequest
	listDescendantsCalls int
	inspectResp          *leapmuxv1.InspectLastTabCloseResponse
	inspectBatchResp     *leapmuxv1.InspectTabsCloseResponse
	inspectBatchReqs     []*leapmuxv1.InspectTabsCloseRequest
	closeResp            *leapmuxv1.CloseTabResponse
}

func (w *childCloseWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		var in leapmuxv1.ListAgentsRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.mu.Lock()
		var out []*leapmuxv1.AgentInfo
		if in.GetIncludeDescendants() {
			w.listDescendantsCalls++
			out = append(out, w.agents...)
		} else {
			want := make(map[string]struct{}, len(in.GetTabIds()))
			for _, id := range in.GetTabIds() {
				want[id] = struct{}{}
			}
			for _, a := range w.agents {
				if len(want) == 0 {
					out = append(out, a)
					continue
				}
				if _, ok := want[a.GetId()]; ok {
					out = append(out, a)
				}
			}
		}
		w.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: out})
	case leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure:
		w.mu.Lock()
		resp := w.inspectResp
		w.mu.Unlock()
		if resp == nil {
			resp = &leapmuxv1.InspectLastTabCloseResponse{}
		}
		writeProtoResponse(rw, resp)
	case leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure:
		var in leapmuxv1.InspectTabsCloseRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.mu.Lock()
		w.inspectBatchReqs = append(w.inspectBatchReqs, &in)
		resp := w.inspectBatchResp
		w.mu.Unlock()
		if resp == nil {
			resp = &leapmuxv1.InspectTabsCloseResponse{}
		}
		writeProtoResponse(rw, resp)
	case leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure:
		writeProtoResponse(rw, &leapmuxv1.InspectTerminalProcessesResponse{})
	case leapmuxv1connect.WorkerTabServiceCloseTabProcedure:
		var in leapmuxv1.CloseTabRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.mu.Lock()
		w.closedTabs = append(w.closedTabs, in.GetTab().GetTabId())
		w.closeReqs = append(w.closeReqs, &in)
		resp := w.closeResp
		w.mu.Unlock()
		if resp == nil {
			resp = &leapmuxv1.CloseTabResponse{}
		}
		writeProtoResponse(rw, resp)
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func (w *childCloseWorker) closed() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.closedTabs...)
}

func (w *childCloseWorker) recordedCloseReqs() []*leapmuxv1.CloseTabRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*leapmuxv1.CloseTabRequest(nil), w.closeReqs...)
}

func (w *childCloseWorker) descendantsCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.listDescendantsCalls
}

func TestAgentCloseChildren_ClosesDirectChildrenAndDeduplicates(t *testing.T) {
	parentID := fullTestID("parent")
	child1 := fullTestID("child1")
	child2 := fullTestID("child2")
	grandchild := fullTestID("grand1")

	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "m").
		tab(parentID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(child1, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(child2, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(grandchild, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	hub := &recordingHub{
		workers:      workerRows("worker-A"),
		materialized: state,
		onSubmitOps:  tabCloseHub().onSubmitOps,
	}
	w := &childCloseWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
			{Id: child1, ParentAgentId: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: child2, ParentAgentId: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: grandchild, ParentAgentId: child1, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
	}
	startSpawnIPC(t, hub, w)
	t.Setenv(controlenv.TabIDVariable, parentID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	// Passing child1 explicitly alongside "children" closes each direct child once in first-seen order.
	data := runControl(t, "agent", "close", child1, "children").Data(t)
	closedRows, ok := data["closed"].([]any)
	require.True(t, ok)
	require.Len(t, closedRows, 2)
	assert.Equal(t, []string{child1, child2}, w.closed())
}

func TestAgentCloseChildren_ClosesSingleChildAndPreservesBusyGuardForRootAgents(t *testing.T) {
	parentID := fullTestID("parent")
	child1 := fullTestID("child1")
	busyRoot := fullTestID("busy01")

	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "m").
		tab(parentID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(child1, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(busyRoot, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	hub := &recordingHub{
		workers:      workerRows("worker-A"),
		materialized: state,
		onSubmitOps:  tabCloseHub().onSubmitOps,
	}
	w := &childCloseWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: child1, ParentAgentId: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
			{Id: busyRoot, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
		},
	}
	startSpawnIPC(t, hub, w)
	t.Setenv(controlenv.TabIDVariable, parentID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	// Closing a single direct child returns the single-tab payload shape.
	data := runControl(t, "agent", "close", "children").Data(t)
	assert.Equal(t, child1, data["id"])
	assert.Equal(t, []string{child1}, w.closed())

	// A busy root agent still trips the busy guard until --allow-busy is passed.
	runControl(t, "agent", "close", busyRoot).requireFailure(t, cli.CodeTabBusyRefused)
	assert.Equal(t, []string{child1}, w.closed(), "refused busy agent must not close without --allow-busy")

	data = runControl(t, "agent", "close", busyRoot, "--allow-busy").Data(t)
	assert.Equal(t, busyRoot, data["id"])
	assert.Equal(t, true, data["busy_override"])
	assert.Equal(t, []string{child1, busyRoot}, w.closed())
}

func TestAgentCloseChildren_ErrorsOutsideAgentTabOrWithNoChildren(t *testing.T) {
	parentID := fullTestID("parent")
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		materialized: newStateBuilder().
			workspace("ws-1", "root-1").
			leafNode("root-1", "", "m").
			tab(parentID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st,
		onSubmitOps: tabCloseHub().onSubmitOps,
	}
	w := &childCloseWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
	}
	startSpawnIPC(t, hub, w)

	t.Setenv(controlenv.TabIDVariable, "term-1")
	t.Setenv(controlenv.TabTypeVariable, "terminal")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	runControl(t, "agent", "close", "children").requireFailure(t, cli.CodeUsage)

	t.Setenv(controlenv.TabIDVariable, parentID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")
	runControl(t, "agent", "close", "children").requireFailure(t, cli.CodeNotFound)
}

func TestResolveVariadicAgentTargets_DeduplicatesPreservingOrder(t *testing.T) {
	parentID := fullTestID("parent")
	child1 := fullTestID("child1")
	child2 := fullTestID("child2")
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: child1}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: child2}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
	}
	w := &childCloseWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: child1, ParentAgentId: parentID, Title: "Child One"},
			{Id: child2, ParentAgentId: parentID, Title: "Child Two"},
		},
	}
	startSpawnIPC(t, hub, w)
	t.Setenv(controlenv.TabIDVariable, parentID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	root := NewCommand()
	waitCmd, _, err := root.Find([]string{"agent", "wait"})
	require.NoError(t, err)
	s, err := openSession(&cli.Invocation{Context: testutil.DeadlineContext(t), Command: waitCmd})
	require.NoError(t, err)
	defer s.Close()

	got, err := s.resolveVariadicTabs(resolve.KindAgent, []string{"title:Child Two", "CHILDREN", "title:Child One", "children"}, resolve.Need{WorkerID: true})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, child2, got[0].ID)
	assert.Equal(t, child1, got[1].ID)

	listTabsCalls := 0
	for _, proc := range hub.called() {
		if proc == leapmuxv1connect.WorkspaceServiceListTabsProcedure {
			listTabsCalls++
		}
	}
	assert.Equal(t, 1, listTabsCalls, "resolveVariadicAgentTargets must memoize ListTabs across inputs")
	assert.Equal(t, 1, w.descendantsCalls(), "resolveVariadicAgentTargets must memoize children resolution across repeated selectors")
}

func TestResolveVariadicAgentTargets_PropagatesMissingTargetError(t *testing.T) {
	parentID := fullTestID("parent-err")
	child1 := fullTestID("child-ok")
	missingID := fullTestID("child-missing")

	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "m").
		tab(parentID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(child1, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	hub := &recordingHub{
		workers:      workerRows("worker-A"),
		materialized: state,
		onSubmitOps:  tabCloseHub().onSubmitOps,
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: child1}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
	}
	w := &childCloseWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: child1, ParentAgentId: parentID, Title: "Child One", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
	}
	startSpawnIPC(t, hub, w)
	t.Setenv(controlenv.TabIDVariable, parentID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	root := NewCommand()
	waitCmd, _, err := root.Find([]string{"agent", "wait"})
	require.NoError(t, err)
	s, err := openSession(&cli.Invocation{Context: testutil.DeadlineContext(t), Command: waitCmd})
	require.NoError(t, err)
	defer s.Close()

	_, err = s.resolveVariadicTabs(resolve.KindAgent, []string{"title:Child One", "title:Does Not Exist"}, resolve.Need{WorkerID: true})
	require.Error(t, err)
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	// `agent close` resolves all targets before closing any, so a missing second target leaves child1 untouched.
	// Using "title:Child One" ensures the first target resolves cleanly from ListTabs while missingID fails LocateTab.
	hub.unlocatableTab = true
	runControl(t, "agent", "close", "title:Child One", missingID).requireFailure(t, cli.CodeNotFound)
	assert.Empty(t, w.closed(), "missing second target must abort before closing the first target")
}

func TestAgentCloseChildren_WorktreeAndAllowBusyEdgeCases(t *testing.T) {
	t.Run("worktree prompt refuses without --worktree and forwards --worktree keep", func(t *testing.T) {
		parentID := fullTestID("wt-parent")
		child1 := fullTestID("wt-child1")

		state := newStateBuilder().
			workspace("ws-1", "root-1").
			leafNode("root-1", "", "m").
			tab(parentID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			tab(child1, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st
		hub := &recordingHub{
			workers:      workerRows("worker-A"),
			materialized: state,
			onSubmitOps:  tabCloseHub().onSubmitOps,
		}
		w := &childCloseWorker{
			agents: []*leapmuxv1.AgentInfo{
				{Id: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
				{Id: child1, ParentAgentId: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			},
			inspectResp: &leapmuxv1.InspectLastTabCloseResponse{
				ShouldPrompt: true,
				Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
				WorktreePath: "/repo/.worktrees/child-branch",
			},
		}
		startSpawnIPC(t, hub, w)
		t.Setenv(controlenv.TabIDVariable, parentID)
		t.Setenv(controlenv.TabTypeVariable, "agent")
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		f := runControl(t, "agent", "close", "children").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "/repo/.worktrees/child-branch")
		assert.Empty(t, w.closed(), "missing --worktree must refuse before closing child tab")

		data := runControl(t, "agent", "close", "children", "--worktree", "keep").Data(t)
		assert.Equal(t, child1, data["id"])
		assert.Equal(t, "keep", data["worktree"])
		reqs := w.recordedCloseReqs()
		require.Len(t, reqs, 1)
		assert.Equal(t, leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP, reqs[0].GetWorktreeAction())
	})

	t.Run("worktree discard failure exits non-zero while reporting partial failure", func(t *testing.T) {
		parentID := fullTestID("wt-disc-p")
		child1 := fullTestID("wt-disc-c")

		state := newStateBuilder().
			workspace("ws-1", "root-1").
			leafNode("root-1", "", "m").
			tab(parentID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			tab(child1, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st
		hub := &recordingHub{
			workers:      workerRows("worker-A"),
			materialized: state,
			onSubmitOps:  tabCloseHub().onSubmitOps,
		}
		w := &childCloseWorker{
			agents: []*leapmuxv1.AgentInfo{
				{Id: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
				{Id: child1, ParentAgentId: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			},
			inspectResp: &leapmuxv1.InspectLastTabCloseResponse{
				ShouldPrompt: true,
				Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
				WorktreePath: "/repo/.worktrees/child-discard",
			},
			closeResp: &leapmuxv1.CloseTabResponse{
				WorktreeRemoval: leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED,
				FailureMessage:  "git worktree remove failed",
			},
		}
		startSpawnIPC(t, hub, w)
		t.Setenv(controlenv.TabIDVariable, parentID)
		t.Setenv(controlenv.TabTypeVariable, "agent")
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		out := runControl(t, "agent", "close", "children", "--worktree", "discard")
		assert.Equal(t, cli.ExitFailure, out.Code)
		var env struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Stdout), &env))
		assert.Equal(t, "failed", env.Data["worktree_removal"])
		assert.Equal(t, "partial_failure", env.Data["status"])
		reqs := w.recordedCloseReqs()
		require.Len(t, reqs, 1)
		assert.Equal(t, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, reqs[0].GetWorktreeAction())
	})

	t.Run("combining busy root agent with children honors --allow-busy", func(t *testing.T) {
		parentID := fullTestID("busy-par")
		child1 := fullTestID("busy-ch1")
		busyRoot := fullTestID("busy-rt1")

		state := newStateBuilder().
			workspace("ws-1", "root-1").
			leafNode("root-1", "", "m").
			tab(parentID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			tab(child1, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			tab(busyRoot, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st
		hub := &recordingHub{
			workers:      workerRows("worker-A"),
			materialized: state,
			onSubmitOps:  tabCloseHub().onSubmitOps,
		}
		w := &childCloseWorker{
			agents: []*leapmuxv1.AgentInfo{
				{Id: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
				{Id: child1, ParentAgentId: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
				{Id: busyRoot, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
			},
		}
		startSpawnIPC(t, hub, w)
		t.Setenv(controlenv.TabIDVariable, parentID)
		t.Setenv(controlenv.TabTypeVariable, "agent")
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		runControl(t, "agent", "close", busyRoot, "children").requireFailure(t, cli.CodeTabBusyRefused)
		assert.Empty(t, w.closed(), "busy first target must abort before closing children")

		data := runControl(t, "agent", "close", busyRoot, "children", "--allow-busy").Data(t)
		closedRows, ok := data["closed"].([]any)
		require.True(t, ok)
		require.Len(t, closedRows, 2)
		assert.Equal(t, busyRoot, closedRows[0].(map[string]any)["id"])
		assert.Equal(t, true, closedRows[0].(map[string]any)["busy_override"])
		assert.Equal(t, child1, closedRows[1].(map[string]any)["id"])
		assert.Equal(t, []string{busyRoot, child1}, w.closed())
	})
}

// SCAN-20: non-agent close verbs (`tab close`, `terminal close`) deduplicate
// repeated targets instead of closing the same tab twice.
func TestRunTabClose_DeduplicatesNonAgentTargets(t *testing.T) {
	termID := fullTestID("term-dup")
	termRow := &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: termID},
		TileId:      "root-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "m").
		tab(termID, "root-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	hub := &recordingHub{
		workers:      workerRows("worker-A"),
		materialized: state,
		onSubmitOps:  tabCloseHub().onSubmitOps,
		locateTab:    termRow,
		tabs:         []*leapmuxv1.WorkspaceTab{termRow},
	}
	w := &childCloseWorker{}
	startSpawnIPC(t, hub, w)

	data := runControl(t, "tab", "close", termID, termID).Data(t)
	assert.Equal(t, termID, data["id"])
	assert.Equal(t, []string{termID}, w.closed())
}

// SWEEP-9: guardTabClose must reject a self target before closing any earlier
// target in the same variadic `tab close` batch.
func TestRunTabClose_SelfTargetRefusesBeforeClosingOtherTabs(t *testing.T) {
	selfID := fullTestID("self-tab")
	otherID := fullTestID("other-tab")
	selfRow := &leapmuxv1.WorkspaceTab{
		Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: selfID}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1",
	}
	otherRow := &leapmuxv1.WorkspaceTab{
		Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: otherID}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1",
	}
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "m").
		tab(selfID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(otherID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	var (
		mu          sync.Mutex
		submitCount int
	)
	w := &childCloseWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: selfID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: otherID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
	}
	startSpawnIPC(t, &recordingHub{}, w, func(r *controlipc.Router) {
		r.Hub = &phase3bHub{fn: func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				var req leapmuxv1.LocateTabRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				switch req.GetTabId() {
				case selfID:
					return &leapmuxv1.LocateTabResponse{Tab: selfRow}, nil
				case otherID:
					return &leapmuxv1.LocateTabResponse{Tab: otherRow}, nil
				default:
					return nil, connect.NewError(connect.CodeNotFound, "tab not found")
				}
			case leapmuxv1connect.UserCRDTGetMaterializedProcedure:
				return &leapmuxv1.GetMaterializedResponse{State: state}, nil
			case leapmuxv1connect.UserCRDTSubmitOpsProcedure:
				mu.Lock()
				submitCount++
				mu.Unlock()
				return &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}}, nil
			default:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{selfRow, otherRow}}, nil
			}
		}}
	})
	t.Setenv(controlenv.TabIDVariable, selfID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	runControl(t, "tab", "close", otherID, "self").requireFailure(t, cli.CodeSelfTargetRefused)
	assert.Empty(t, w.closed(), "self-target refusal must abort before closing earlier tabs")
	mu.Lock()
	assert.Zero(t, submitCount, "self-target refusal must abort before submitting any CRDT op")
	mu.Unlock()
}

// RIPPLE-10: expanding "children" in `agent close children` skips child
// subagents that were never opened as tabs in the Hub CRDT instead of failing
// the whole command.
func TestAgentCloseChildren_SkipsUnopenedSubagents(t *testing.T) {
	parentID := fullTestID("par-skip")
	openedChild := fullTestID("ch-open1")
	unopenedChild := fullTestID("ch-unop1")

	parentRow := &leapmuxv1.WorkspaceTab{
		Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1",
	}
	openedRow := &leapmuxv1.WorkspaceTab{
		Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: openedChild}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1",
	}
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "m").
		tab(parentID, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(openedChild, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	w := &childCloseWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: openedChild, ParentAgentId: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: unopenedChild, ParentAgentId: parentID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
	}
	startSpawnIPC(t, &recordingHub{}, w, func(r *controlipc.Router) {
		r.Hub = &phase3bHub{fn: func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				var req leapmuxv1.LocateTabRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				switch req.GetTabId() {
				case parentID:
					return &leapmuxv1.LocateTabResponse{Tab: parentRow}, nil
				case openedChild:
					return &leapmuxv1.LocateTabResponse{Tab: openedRow}, nil
				default:
					return nil, connect.NewError(connect.CodeNotFound, "tab not found")
				}
			case leapmuxv1connect.UserCRDTGetMaterializedProcedure:
				return &leapmuxv1.GetMaterializedResponse{State: state}, nil
			case leapmuxv1connect.UserCRDTSubmitOpsProcedure:
				return &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}}, nil
			default:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{parentRow, openedRow}}, nil
			}
		}}
	})
	t.Setenv(controlenv.TabIDVariable, parentID)
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	data := runControl(t, "agent", "close", "children").Data(t)
	assert.Equal(t, openedChild, data["id"])
	assert.Equal(t, []string{openedChild}, w.closed())
}

// ALTITUDE-3: closing 2+ sibling tabs on a dirty worktree without --worktree
// must preflight the full closing set via InspectTabsClose and refuse BEFORE
// tombstoning or closing tab 1.
func TestRunTabClose_MultiTabPreflightsDirtyWorktreeBeforeClosingFirstTab(t *testing.T) {
	tab1 := fullTestID("wt-sib-1")
	tab2 := fullTestID("wt-sib-2")
	row1 := &leapmuxv1.WorkspaceTab{
		Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tab1}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1",
	}
	row2 := &leapmuxv1.WorkspaceTab{
		Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tab2}, TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1",
	}
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "m").
		tab(tab1, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab(tab2, "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	var (
		mu          sync.Mutex
		submitCount int
	)
	w := &childCloseWorker{
		agents: []*leapmuxv1.AgentInfo{
			{Id: tab1, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: tab2, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
		// Single-tab InspectLastTabClose would see the sibling and return false,
		// so only the batch InspectTabsClose preflight catches the dirty worktree.
		inspectResp: &leapmuxv1.InspectLastTabCloseResponse{ShouldPrompt: false},
		inspectBatchResp: &leapmuxv1.InspectTabsCloseResponse{
			Items: []*leapmuxv1.TabCloseInspection{
				{
					Tab:          &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tab1},
					ShouldPrompt: true,
					IsWorktree:   true,
					WorktreePath: "/repo/.worktrees/shared-dirty",
					BranchName:   "feature/shared-dirty",
					GitState: &leapmuxv1.BranchGitState{
						HasUncommittedChanges: true,
						DiffAdded:             3,
						UnpushedCommitCount:   1,
					},
				},
			},
		},
	}
	startSpawnIPC(t, &recordingHub{}, w, func(r *controlipc.Router) {
		r.Hub = &phase3bHub{fn: func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				var req leapmuxv1.LocateTabRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				switch req.GetTabId() {
				case tab1:
					return &leapmuxv1.LocateTabResponse{Tab: row1}, nil
				case tab2:
					return &leapmuxv1.LocateTabResponse{Tab: row2}, nil
				default:
					return nil, connect.NewError(connect.CodeNotFound, "tab not found")
				}
			case leapmuxv1connect.UserCRDTGetMaterializedProcedure:
				return &leapmuxv1.GetMaterializedResponse{State: state}, nil
			case leapmuxv1connect.UserCRDTSubmitOpsProcedure:
				mu.Lock()
				submitCount++
				mu.Unlock()
				return &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}}, nil
			default:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{row1, row2}}, nil
			}
		}}
	})

	f := runControl(t, "tab", "close", tab1, tab2).requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "/repo/.worktrees/shared-dirty")
	assert.Contains(t, f.Hints, "pass --worktree=keep|push|discard to say what happens to it")
	assert.Empty(t, w.closed(), "dirty-worktree batch preflight must refuse before closing tab 1")
	mu.Lock()
	assert.Zero(t, submitCount, "dirty-worktree batch preflight must refuse before submitting any CRDT op")
	mu.Unlock()
	require.Len(t, w.inspectBatchReqs, 1)
	require.Len(t, w.inspectBatchReqs[0].GetTabs(), 2)
}
