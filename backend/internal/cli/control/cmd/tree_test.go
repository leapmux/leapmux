package cmd

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

type treeErrorHub struct {
	*recordingHub
	listSectionsErr error
}

func (h *treeErrorHub) CallInner(ctx context.Context, userID userid.UserID, procedure string, payload []byte) ([]byte, error) {
	if procedure == leapmuxv1connect.SectionServiceListSectionsProcedure && h.listSectionsErr != nil {
		return nil, h.listSectionsErr
	}
	return h.recordingHub.CallInner(ctx, userID, procedure, payload)
}

func TestAssembleTreeTabs_ThreeLevelSubagentChainsAndFlatTypes(t *testing.T) {
	agRoot := fullTestID("ag-root")
	agUnopenedMid := fullTestID("ag-mid")
	agUnopenedLeaf := fullTestID("ag-leaf")
	agOpenedGrandchild := fullTestID("ag-grand")
	agOpenedChild := fullTestID("ag-child")
	agCrossWSGrandchild := fullTestID("ag-cross")

	tabs := []*leapmuxv1.WorkspaceTab{
		{WorkspaceId: "ws-1", TileId: "tile-1", WorkerId: "w-1", Tab: &leapmuxv1.TabIdent{TabId: agRoot, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
		{WorkspaceId: "ws-1", TileId: "tile-1", WorkerId: "w-1", Tab: &leapmuxv1.TabIdent{TabId: agOpenedChild, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
		{WorkspaceId: "ws-1", TileId: "tile-1", WorkerId: "w-1", Tab: &leapmuxv1.TabIdent{TabId: agOpenedGrandchild, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
		{WorkspaceId: "ws-2", TileId: "tile-2", WorkerId: "w-1", Tab: &leapmuxv1.TabIdent{TabId: agCrossWSGrandchild, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
	}
	wdata := workerTabData{
		details: map[string]tabDetail{
			agRoot: {
				Title: "Root", State: "working",
				Agent: &leapmuxv1.AgentInfo{Id: agRoot, Title: "Root", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE},
			},
			agOpenedChild: {
				Title: "Opened Child", State: "idle",
				Agent: &leapmuxv1.AgentInfo{Id: agOpenedChild, Title: "Opened Child", ParentAgentId: agRoot, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE},
			},
			agUnopenedMid: {
				Title: "Unopened Mid", State: "working",
				Agent: &leapmuxv1.AgentInfo{Id: agUnopenedMid, Title: "Unopened Mid", ParentAgentId: agRoot, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX},
			},
			agUnopenedLeaf: {
				Title: "Unopened Leaf", State: "idle",
				Agent: &leapmuxv1.AgentInfo{Id: agUnopenedLeaf, Title: "Unopened Leaf", ParentAgentId: agUnopenedMid, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX},
			},
			agOpenedGrandchild: {
				Title: "Opened Grandchild", State: "idle",
				Agent: &leapmuxv1.AgentInfo{Id: agOpenedGrandchild, Title: "Opened Grandchild", ParentAgentId: agUnopenedMid, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX},
			},
			agCrossWSGrandchild: {
				Title: "Cross Workspace", State: "idle",
				Agent: &leapmuxv1.AgentInfo{Id: agCrossWSGrandchild, Title: "Cross Workspace", ParentAgentId: agUnopenedMid, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX},
			},
		},
		workerNames: map[string]string{"w-1": "devbox"},
		agentWorker: map[string]string{
			agRoot:              "w-1",
			agOpenedChild:       "w-1",
			agUnopenedMid:       "w-1",
			agUnopenedLeaf:      "w-1",
			agOpenedGrandchild:  "w-1",
			agCrossWSGrandchild: "w-1",
		},
	}

	got := assembleTreeTabs(tabs, wdata, agOpenedGrandchild)

	// In ws-1/tile-1, only agRoot is top-level; opened child and opened
	// grandchild under the unopened middle subagent both nest underneath it.
	tile1Tabs := got.tabsByTile[treeTileKey{wsID: "ws-1", tileID: "tile-1"}]
	require.Len(t, tile1Tabs, 1)
	root := tile1Tabs[0]
	assert.Equal(t, agRoot, root.ID)
	assert.Equal(t, "agent", root.Type)
	require.Len(t, root.Children, 2)

	var midNode *treeTab
	for i := range root.Children {
		if root.Children[i].ID == agUnopenedMid {
			midNode = &root.Children[i]
		}
	}
	require.NotNil(t, midNode, "unopened middle subagent must nest under root")
	assert.Equal(t, "ws-1", midNode.WorkspaceID)
	assert.Equal(t, "subagent", midNode.Type)
	require.Len(t, midNode.Children, 2, "both unopened leaf and opened grandchild must nest under unopened middle")

	childByID := map[string]treeTab{}
	for _, c := range midNode.Children {
		childByID[c.ID] = c
	}
	assert.Equal(t, "ws-1", childByID[agUnopenedLeaf].WorkspaceID, "3rd-level unopened subagent must inherit workspace transitively")
	assert.Equal(t, "subagent", childByID[agUnopenedLeaf].Type)
	assert.Equal(t, "ws-1", childByID[agOpenedGrandchild].WorkspaceID)
	assert.Equal(t, "subagent", childByID[agOpenedGrandchild].Type)
	assert.True(t, childByID[agOpenedGrandchild].Here)

	// Cross-workspace grandchild stays top-level in ws-2 as an ordinary agent.
	tile2Tabs := got.tabsByTile[treeTileKey{wsID: "ws-2", tileID: "tile-2"}]
	require.Len(t, tile2Tabs, 1)
	assert.Equal(t, agCrossWSGrandchild, tile2Tabs[0].ID)
	assert.Equal(t, "agent", tile2Tabs[0].Type)

	// Flat rows must also label opened subagents as "subagent" and carry
	// transitively inherited workspace IDs on unopened descendants.
	flatByID := map[string]treeTab{}
	for _, row := range got.flatRows {
		flatByID[row.ID] = row
	}
	assert.Equal(t, "agent", flatByID[agRoot].Type)
	assert.Equal(t, "subagent", flatByID[agOpenedChild].Type)
	assert.Equal(t, "subagent", flatByID[agOpenedGrandchild].Type)
	assert.Equal(t, "subagent", flatByID[agUnopenedMid].Type)
	assert.Equal(t, "ws-1", flatByID[agUnopenedMid].WorkspaceID)
	assert.Equal(t, "subagent", flatByID[agUnopenedLeaf].Type)
	assert.Equal(t, "ws-1", flatByID[agUnopenedLeaf].WorkspaceID)
	assert.Equal(t, "agent", flatByID[agCrossWSGrandchild].Type)
}

func TestTreeCommand_ListSectionsErrorsAndPermissionTolerance(t *testing.T) {
	wsID := fullTestID("ws-sec")

	t.Run("propagates non-permission errors", func(t *testing.T) {
		hub := &recordingHub{
			workspaces: []*leapmuxv1.Workspace{{Id: wsID, Title: "Main"}},
		}
		startSpawnIPC(t, hub, &phase2bWorkerDispatcher{}, func(r *controlipc.Router) {
			r.Hub = &treeErrorHub{
				recordingHub:    hub,
				listSectionsErr: connect.NewError(connect.CodeUnavailable, "sections store down"),
			}
		})

		f := runControl(t, "tree").requireFailure(t, cli.CodeUnavailable)
		assert.Contains(t, f.Message, "sections store down")
	})

	t.Run("tolerates PermissionDenied from a narrow delegation bearer", func(t *testing.T) {
		// recordingHub returns CodePermissionDenied for ListSections when sections is nil.
		hub := &recordingHub{
			workspaces: []*leapmuxv1.Workspace{{Id: wsID, Title: "Main"}},
			tabs:       []*leapmuxv1.WorkspaceTab{},
		}
		startSpawnIPC(t, hub, &phase2bWorkerDispatcher{})

		data := runControl(t, "tree").Data(t)
		sections, ok := data["sections"].([]any)
		require.True(t, ok)
		require.Len(t, sections, 1)
	})
}

func TestTreeCommand_FlatSkipsGetMaterializedAndLabelsSubagents(t *testing.T) {
	wsID := fullTestID("ws-flat")
	agParent := fullTestID("ag-p1")
	agChild := fullTestID("ag-c1")
	agUnopened := fullTestID("ag-u1")
	agGrand := fullTestID("ag-g1")

	state := newStateBuilder().
		workspace(wsID, "tile-1").
		leafNode("tile-1", "", "n").
		st
	hub := &recordingHub{
		workspaces: []*leapmuxv1.Workspace{{Id: wsID, Title: "Flat WS"}},
		sections:   &leapmuxv1.ListSectionsResponse{},
		tabs: []*leapmuxv1.WorkspaceTab{
			{WorkspaceId: wsID, TileId: "tile-1", WorkerId: "worker-A", Tab: &leapmuxv1.TabIdent{TabId: agParent, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
			{WorkspaceId: wsID, TileId: "tile-1", WorkerId: "worker-A", Tab: &leapmuxv1.TabIdent{TabId: agChild, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
		},
		materialized: state,
	}
	worker := &phase2bWorkerDispatcher{
		workerName: "devbox",
		agents: []*leapmuxv1.AgentInfo{
			{Id: agParent, Title: "Lead", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
			{Id: agChild, Title: "Opened Sub", ParentAgentId: agParent, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: agUnopened, Title: "Unopened Sub", ParentAgentId: agParent, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: agGrand, Title: "Unopened Grand", ParentAgentId: agUnopened, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
	}
	startSpawnIPC(t, hub, worker)

	data := runControl(t, "tree", "--flat").Data(t)
	assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTGetMaterializedProcedure, "--flat must skip CRDT bootstrap")

	rawTabs, ok := data["tabs"].([]any)
	require.True(t, ok)
	byID := map[string]map[string]any{}
	for _, item := range rawTabs {
		row := item.(map[string]any)
		byID[row["id"].(string)] = row
	}
	assert.Equal(t, "agent", byID[agParent]["type"])
	assert.Equal(t, "subagent", byID[agChild]["type"], "opened subagent must be labeled subagent in --flat")
	assert.Equal(t, wsID, byID[agGrand]["workspace_id"], "unopened grandchild must inherit workspace_id in --flat")
}

func TestTreeCommand_WorkspaceFilterListsWorkspacesOnce(t *testing.T) {
	wsOne := fullTestID("ws-one")
	wsTwo := fullTestID("ws-two")
	hub := &recordingHub{
		workspaces: []*leapmuxv1.Workspace{
			{Id: wsOne, Title: "Alpha"},
			{Id: wsTwo, Title: "Beta"},
		},
		sections: &leapmuxv1.ListSectionsResponse{},
		tabs:     []*leapmuxv1.WorkspaceTab{},
	}
	startSpawnIPC(t, hub, &phase2bWorkerDispatcher{})

	data := runControl(t, "tree", "--workspace", "title:Beta", "--flat").Data(t)
	require.Contains(t, data, "tabs")

	listCount := 0
	for _, proc := range hub.called() {
		if proc == leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure {
			listCount++
		}
	}
	assert.Equal(t, 1, listCount, "--workspace must resolve against the already-fetched workspace list")
}

func TestCollectFloatingTiles_WalksNestedSubtreeOnce(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "main-root").
		leafNode("main-root", "", "n").
		splitNode("fw-split", "", "n").
		leafNode("fw-leaf-a", "fw-split", "b").
		leafNode("fw-leaf-b", "fw-split", "n").
		st
	state.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{
		"fw-1": {
			WindowId:    "fw-1",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
			RootNodeId:  "fw-split",
		},
		"fw-dead": {
			WindowId:    "fw-dead",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
			RootNodeId:  "main-root",
			TombstoneAt: &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "x"},
		},
	}

	got := collectFloatingTiles(state)
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	assert.Equal(t, []string{"fw-leaf-a", "fw-leaf-b", "fw-split"}, keys)
}
