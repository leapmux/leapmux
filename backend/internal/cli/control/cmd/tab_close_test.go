package cmd

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

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
)

type childCloseWorker struct {
	mu                   sync.Mutex
	agents               []*leapmuxv1.AgentInfo
	closedTabs           []string
	closeReqs            []*leapmuxv1.CloseTabRequest
	listDescendantsCalls int
	inspectResp          *leapmuxv1.InspectLastTabCloseResponse
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

	t.Setenv(controlenv.TabIDVariable, "")
	t.Setenv(controlenv.TabTypeVariable, "")
	t.Setenv(controlenv.WorkerIDVariable, "")
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

	got, err := s.resolveVariadicAgentTargets([]string{"title:Child Two", "CHILDREN", "title:Child One", "children"}, resolve.Need{})
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

	_, err = s.resolveVariadicAgentTargets([]string{"title:Child One", "title:Does Not Exist"}, resolve.Need{})
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
