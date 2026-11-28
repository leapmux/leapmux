package cmd

import (
	"context"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// startCustomSpawnIPC mirrors startSpawnIPC for tests that supply a custom
// controlipc.HubClient wrapper (for example opCaptureHub or strictWorkspaceHub).
func startCustomSpawnIPC(t *testing.T, hub controlipc.HubClient, disp controlipc.LocalDispatcher) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}
	clearRemoteEnv(t)

	router := &controlipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("u-spawn"),
		Hub:             hub,
		LocalDispatcher: disp,
		Sources:         spawnTabSources{},
	}
	sockURL := shortIPCSocket(t)
	rawToken := controlipc.MintToken()
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: controlipc.TokenInfo{
			UserID:   userid.MustNew("u-spawn"),
			WorkerID: "worker-A",
			TabID:    "agent-1",
			TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
		},
		Router: router,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
	t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)
}

func floatingWindowFixture() *leapmuxv1.UserMaterialized {
	state := newStateBuilder().
		workspace("ws-1", "ws-root").
		leafNode("ws-root", "", "").
		splitNode("fw-root", "", "").
		leafNode("fw-a", "fw-root", "a").
		leafNode("fw-b", "fw-root", "b").
		tabOnWorker("tab-1", "fw-a", "worker-A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tabOnWorker("tab-2", "fw-b", "worker-A", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabOnWorker("tab-3", "fw-b", "worker-A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tombstonedTab("tab-dead", "fw-a", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	state.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{
		"win-1234": {
			WindowId:    "win-1234",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
			RootNodeId:  "fw-root",
			X:           &leapmuxv1.LWWDouble{Value: 100},
			Y:           &leapmuxv1.LWWDouble{Value: 120},
			Width:       &leapmuxv1.LWWDouble{Value: 640},
			Height:      &leapmuxv1.LWWDouble{Value: 420},
			Opacity:     &leapmuxv1.LWWDouble{Value: 1.0},
		},
	}
	return state
}

// SCAN-4: window close must tombstone the floating window's root node exactly
// once, tombstone descendants leaves-first, tombstone all live tabs once, and
// pass crdt.ValidateBatch. It must also protect the calling tab unless
// --allow-self is passed.
func TestRunWindowClose_TombstonesRootOnceAndProtectsCallingTab(t *testing.T) {
	t.Run("tombstones root once and passes validator", func(t *testing.T) {
		state := floatingWindowFixture()
		hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
		startCustomSpawnIPC(t, hub, &closeDispatcher{})

		data := runControl(t, "window", "close", "win-1234", "--allow-busy").Data(t)
		assert.Equal(t, "win-1234", data["id"])
		assert.Equal(t, true, data["closed"])
		assert.Equal(t, float64(3), data["tabs_closed"])

		require.Len(t, hub.submitted, 1)
		ops := hub.submitted[0]
		rootTombstones := 0
		windowTombstones := 0
		tabTombstones := map[string]int{}
		for _, op := range ops {
			switch b := op.GetBody().(type) {
			case *leapmuxv1.CrdtOp_TombstoneNode:
				if b.TombstoneNode.GetNodeId() == "fw-root" {
					rootTombstones++
				}
			case *leapmuxv1.CrdtOp_TombstoneFloatingWindow:
				if b.TombstoneFloatingWindow.GetWindowId() == "win-1234" {
					windowTombstones++
				}
			case *leapmuxv1.CrdtOp_TombstoneTab:
				tabTombstones[b.TombstoneTab.GetTab().GetTabId()]++
			}
		}
		assert.Equal(t, 1, rootTombstones, "floating window root node must be tombstoned once")
		assert.Equal(t, 1, windowTombstones, "floating window must be tombstoned once")
		assert.Equal(t, map[string]int{"tab-1": 1, "tab-2": 1, "tab-3": 1}, tabTombstones)

		next := validateOpsAgainstMaterialized(t, state, ops)
		assert.False(t, crdt.HLCIsZero(next.FloatingWindows["win-1234"].GetTombstoneAt()))
		assert.False(t, crdt.HLCIsZero(next.Nodes["fw-root"].GetTombstoneAt()))
	})

	t.Run("refuses when calling tab lives inside window", func(t *testing.T) {
		state := floatingWindowFixture()
		hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
		startCustomSpawnIPC(t, hub, &closeDispatcher{})
		t.Setenv("LEAPMUX_CONTROL_TAB_ID", "tab-2")

		f := runControl(t, "window", "close", "win-1234").requireFailure(t, cli.CodeSelfTargetRefused)
		assert.Contains(t, f.Message, "win-1234")
		assert.Empty(t, hub.submitted)

		data := runControl(t, "window", "close", "win-1234", "--allow-self", "--allow-busy").Data(t)
		assert.Equal(t, true, data["closed"])
		assert.Len(t, hub.submitted, 1)
	})
}

// SCAN-15: window move, window resize, and window set-opacity must report the
// actual count of live tabs in the floating window's subtree, not 0.
func TestRunWindowMutations_ReportActualLiveTabCount(t *testing.T) {
	state := floatingWindowFixture()
	hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
	startCustomSpawnIPC(t, hub, nil)

	moved := runControl(t, "window", "move", "win-1234", "--x", "250").Data(t)
	assert.Equal(t, float64(3), moved["tabs"])
	assert.Equal(t, float64(250), moved["x"])

	resized := runControl(t, "window", "resize", "win-1234", "--width", "800").Data(t)
	assert.Equal(t, float64(3), resized["tabs"])
	assert.Equal(t, float64(800), resized["width"])

	opacity := runControl(t, "window", "set-opacity", "win-1234", "0.75").Data(t)
	assert.Equal(t, float64(3), opacity["tabs"])
	assert.Equal(t, float64(0.75), opacity["opacity"])
}

// SWEEP-10: window list, window create, and window move --workspace must
// validate explicit workspace IDs and fail with CodeNotFound when absent.
func TestRunWindowCommands_RejectMissingExplicitWorkspace(t *testing.T) {
	hub := &strictWorkspaceHub{
		recordingHub:       &recordingHub{materialized: floatingWindowFixture()},
		existingWorkspaces: map[string]bool{"ws-1": true},
	}
	startCustomSpawnIPC(t, hub, nil)

	runControl(t, "window", "list", "id:missing-ws").requireFailure(t, cli.CodeNotFound)
	runControl(t, "window", "create", "id:missing-ws").requireFailure(t, cli.CodeNotFound)
	runControl(t, "window", "move", "win-1234", "--workspace", "id:missing-ws").requireFailure(t, cli.CodeNotFound)
	assert.Empty(t, hub.submitted, "missing workspace must prevent CRDT submission")
}

// TRAPS-9 + DESIGN-7: resolveFloatingWindow normalizes id handles and returns
// deterministically sorted ambiguous candidates.
func TestResolveFloatingWindow_DeterministicOrderAndHandleNormalization(t *testing.T) {
	state := &leapmuxv1.UserMaterialized{
		FloatingWindows: map[string]*leapmuxv1.FloatingWindowRecord{
			"wxyz-c": {WindowId: "wxyz-c"},
			"wxyz-a": {WindowId: "wxyz-a"},
			"wxyz-b": {WindowId: "wxyz-b"},
			"uniq-1": {WindowId: "uniq-1"},
			"w1":     {WindowId: "w1"},
			"dead-1": {WindowId: "dead-1", TombstoneAt: &leapmuxv1.HLC{Physical: 1}},
		},
	}

	_, err := resolveFloatingWindow(state, "")
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)

	_, err = resolveFloatingWindow(state, "id:   ")
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)

	fw, err := resolveFloatingWindow(state, "id:uniq-1")
	require.NoError(t, err)
	assert.Equal(t, "uniq-1", fw.GetWindowId())

	fw, err = resolveFloatingWindow(state, "id:w1")
	require.NoError(t, err)
	assert.Equal(t, "w1", fw.GetWindowId())

	fw, err = resolveFloatingWindow(state, "uniq")
	require.NoError(t, err)
	assert.Equal(t, "uniq-1", fw.GetWindowId())

	// "id:" allows unique prefix matching (>= 4 chars) and reports ambiguity.
	fw, err = resolveFloatingWindow(state, "id:uniq")
	require.NoError(t, err)
	assert.Equal(t, "uniq-1", fw.GetWindowId())

	_, err = resolveFloatingWindow(state, "id:uni")
	require.Error(t, err)
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	_, err = resolveFloatingWindow(state, "id:wxyz")
	require.Error(t, err)
	ambig := cli.FromError(err)
	assert.Equal(t, cli.CodeAmbiguousTarget, ambig.Code)
	require.Len(t, ambig.Candidates, 3)
	assert.Equal(t, []string{"wxyz-a", "wxyz-b", "wxyz-c"}, []string{
		ambig.Candidates[0].ID,
		ambig.Candidates[1].ID,
		ambig.Candidates[2].ID,
	})

	// Dead window is not matched.
	_, err = resolveFloatingWindow(state, "dead-1")
	require.Error(t, err)
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	// Ambiguous candidates must always come back sorted by window ID.
	for range 10 {
		_, err := resolveFloatingWindow(state, "wxyz")
		require.Error(t, err)
		cliErr := cli.FromError(err)
		assert.Equal(t, cli.CodeAmbiguousTarget, cliErr.Code)
		require.Len(t, cliErr.Candidates, 3)
		assert.Equal(t, []string{"wxyz-a", "wxyz-b", "wxyz-c"}, []string{
			cliErr.Candidates[0].ID,
			cliErr.Candidates[1].ID,
			cliErr.Candidates[2].ID,
		})
	}
}

type recordingBusyWorker struct {
	mu            sync.Mutex
	queriedAgents []string
}

func (w *recordingBusyWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		var in leapmuxv1.ListAgentsRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		w.mu.Lock()
		w.queriedAgents = append(w.queriedAgents, in.GetTabIds()...)
		w.mu.Unlock()
		agents := make([]*leapmuxv1.AgentInfo, 0, len(in.GetTabIds()))
		for _, id := range in.GetTabIds() {
			agents = append(agents, &leapmuxv1.AgentInfo{
				Id:            id,
				ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
			})
		}
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: agents})
		return
	case leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure:
		writeProtoResponse(rw, &leapmuxv1.InspectTabsCloseResponse{})
		return
	}
	_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
}

// TRAPS-10: window close sorts live subtree tabs deterministically by TabID
// before invoking guardBulkCloseTabs.
func TestRunWindowClose_DeterministicTabOrderForBusyGuard(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "ws-root").
		leafNode("ws-root", "", "").
		leafNode("fw-root", "", "").
		tabOnWorker("ag-c", "fw-root", "worker-A", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabOnWorker("ag-a", "fw-root", "worker-A", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabOnWorker("ag-b", "fw-root", "worker-A", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	state.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{
		"win-busy": {
			WindowId:    "win-busy",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
			RootNodeId:  "fw-root",
		},
	}

	for range 5 {
		w := &recordingBusyWorker{}
		hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
		startCustomSpawnIPC(t, hub, w)

		runControl(t, "window", "close", "win-busy").requireFailure(t, cli.CodeTabBusyRefused)
		assert.Equal(t, []string{"ag-a", "ag-b", "ag-c"}, w.queriedAgents)
		assert.Empty(t, hub.submitted)
	}
}

// ALTITUDE-3: window close uses guardBulkCloseTabs to refuse closing tabs with
// dirty git worktrees unless --allow-busy is passed.
func TestRunWindowClose_DirtyWorktreeGuard(t *testing.T) {
	state := floatingWindowFixture()
	hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
	disp := &deleteGuardDispatcher{dirtyWorktree: true}
	startCustomSpawnIPC(t, hub, disp)

	f := runControl(t, "window", "close", "win-1234").requireFailure(t, cli.CodeTabBusyRefused)
	assert.Contains(t, f.Message, "uncommitted changes")
	assert.Empty(t, hub.submitted)

	data := runControl(t, "window", "close", "win-1234", "--allow-busy").Data(t)
	assert.Equal(t, true, data["closed"])
	assert.Len(t, hub.submitted, 1)
}

// EFFICIENCY-21: window list aggregates subtree tab counts in one pass and
// filters by workspace when an argument is supplied.
func TestRunWindowList_CountsSubtreeTabsAndFiltersWorkspace(t *testing.T) {
	state := floatingWindowFixture()
	state.Workspaces["ws-2"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "ws-2", RootNodeId: "ws2-root"}
	state.Nodes["ws2-root"] = &leapmuxv1.NodeRecord{NodeId: "ws2-root", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF}}
	state.Nodes["fw2-root"] = &leapmuxv1.NodeRecord{NodeId: "fw2-root", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF}}
	state.FloatingWindows["win-2222"] = &leapmuxv1.FloatingWindowRecord{
		WindowId:    "win-2222",
		WorkspaceId: &leapmuxv1.LWWString{Value: "ws-2"},
		RootNodeId:  "fw2-root",
	}
	hub := &strictWorkspaceHub{
		recordingHub:       &recordingHub{materialized: state},
		existingWorkspaces: map[string]bool{"ws-1": true, "ws-2": true},
	}
	startCustomSpawnIPC(t, hub, nil)

	all := runControl(t, "window", "list").Data(t)
	allWins := all["windows"].([]any)
	require.Len(t, allWins, 2)
	assert.Equal(t, "win-1234", allWins[0].(map[string]any)["id"])
	assert.Equal(t, float64(3), allWins[0].(map[string]any)["tabs"])
	assert.Equal(t, "win-2222", allWins[1].(map[string]any)["id"])
	assert.Equal(t, float64(0), allWins[1].(map[string]any)["tabs"])

	scoped := runControl(t, "window", "list", "id:ws-1").Data(t)
	scopedWins := scoped["windows"].([]any)
	require.Len(t, scopedWins, 1)
	assert.Equal(t, "win-1234", scopedWins[0].(map[string]any)["id"])
}

// DUPLICATION-15: appendCreateFloatingWindowOps emits only the 7 floating
// window register ops so callers control rootNodeID's shape ops.
func TestAppendCreateFloatingWindowOps(t *testing.T) {
	bs := testBootstrap(&leapmuxv1.UserMaterialized{})
	ops := appendCreateFloatingWindowOps(nil, bs, "win-1", "ws-1", "root-1", 10, 20, 640, 480, 0.9)
	require.Len(t, ops, 7)
	for _, op := range ops {
		fwOp := op.GetSetFloatingWindowRegister()
		require.NotNil(t, fwOp, "expected only SetFloatingWindowRegister ops")
		assert.Equal(t, "win-1", fwOp.GetWindowId())
	}
}
