package cmd

import (
	"context"
	"errors"
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
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// interceptHub wraps a controlipc.HubClient so a test can override specific
// Hub RPCs without modifying shared test scaffolding.
type interceptHub struct {
	base    controlipc.HubClient
	handler func(ctx context.Context, uid userid.UserID, procedure string, payload []byte) ([]byte, error, bool)
}

func (h *interceptHub) CallInner(ctx context.Context, uid userid.UserID, procedure string, payload []byte) ([]byte, error) {
	if h.handler != nil {
		if resp, err, handled := h.handler(ctx, uid, procedure, payload); handled {
			return resp, err
		}
	}
	return h.base.CallInner(ctx, uid, procedure, payload)
}

type treeAgentDispatcher struct {
	mu      sync.Mutex
	agents  []*leapmuxv1.AgentInfo
	listErr string
	calls   []*leapmuxv1.ListAgentsRequest
}

func (d *treeAgentDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerAgentServiceListAgentsProcedure {
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	var in leapmuxv1.ListAgentsRequest
	_ = proto.Unmarshal(req.GetPayload(), &in)
	d.mu.Lock()
	d.calls = append(d.calls, &in)
	listErr := d.listErr
	agents := d.agents
	d.mu.Unlock()
	if listErr != "" {
		_ = rw.SendError(int32(codes.Unavailable), listErr)
		return
	}
	writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: agents})
}

func TestAgentTreeOverIPC_RendersOpenAndUnopenedDescendants(t *testing.T) {
	rootID := fullTestID("ag-root")
	childOpenID := fullTestID("ag-open")
	childClosedID := fullTestID("ag-shut")

	hub := &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: rootID},
			TileId:      "tile-1",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-1",
		},
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: rootID}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: childOpenID}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
	}
	disp := &treeAgentDispatcher{
		agents: []*leapmuxv1.AgentInfo{
			{Id: rootID, Title: "Root", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
			{Id: childOpenID, Title: "Opened Child", ParentAgentId: rootID, RootAgentId: rootID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			{Id: childClosedID, Title: "Unopened Child", ParentAgentId: rootID, RootAgentId: rootID, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
	}
	startSpawnIPC(t, hub, disp)

	data := runControl(t, "agent", "tree", rootID).Data(t)
	root := data["root"].(map[string]any)
	assert.Equal(t, rootID, root["id"])
	assert.Equal(t, true, root["open_in_tab"])
	children := root["children"].([]any)
	require.Len(t, children, 2)

	byID := map[string]map[string]any{}
	for _, c := range children {
		m := c.(map[string]any)
		byID[m["id"].(string)] = m
	}
	assert.Equal(t, true, byID[childOpenID]["open_in_tab"])
	assert.Equal(t, false, byID[childClosedID]["open_in_tab"])
	assert.Equal(t, "subagent", byID[childOpenID]["relation"])
	assert.Equal(t, "subagent", byID[childClosedID]["relation"])
}

func TestAgentTreeOverIPC_DiscoversSpawnedAgentsAndRelations(t *testing.T) {
	rootID := fullTestID("ag-root")
	subID := fullTestID("ag-sub")
	spawnedID := fullTestID("ag-spawn1")
	grandSpawnedID := fullTestID("ag-spawn2")

	hub := &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: rootID},
			TileId:      "tile-1",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-1",
		},
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: rootID}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: spawnedID}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: grandSpawnedID}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
	}
	allAgents := map[string]*leapmuxv1.AgentInfo{
		rootID: {
			Id:            rootID,
			Title:         "Coordinator",
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		},
		subID: {
			Id:            subID,
			Title:         "Virtual Sub",
			ParentAgentId: rootID,
			RootAgentId:   rootID,
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		},
		spawnedID: {
			Id:                spawnedID,
			Title:             "Spawned Peer",
			SpawnedByAgentId:  rootID,
			ForkedFromAgentId: rootID,
			AgentProvider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			ActivityState:     leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		},
		grandSpawnedID: {
			Id:               grandSpawnedID,
			Title:            "Nested Spawned",
			SpawnedByAgentId: spawnedID,
			AgentProvider:    leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			ActivityState:    leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		},
	}
	disp := &codedWorkerDispatcher{fn: func(method string, body []byte) (proto.Message, error) {
		if method != leapmuxv1connect.WorkerAgentServiceListAgentsProcedure {
			return nil, connect.NewError(connect.CodeUnimplemented, "unexpected method: "+method)
		}
		var req leapmuxv1.ListAgentsRequest
		require.NoError(t, proto.Unmarshal(body, &req))
		var out []*leapmuxv1.AgentInfo
		for _, id := range req.GetTabIds() {
			if a, ok := allAgents[id]; ok {
				out = append(out, a)
			}
			if req.GetIncludeDescendants() {
				for _, cand := range allAgents {
					if cand.GetParentAgentId() == id {
						out = append(out, cand)
					}
				}
			}
		}
		return &leapmuxv1.ListAgentsResponse{Agents: out}, nil
	}}
	startSpawnIPC(t, hub, disp)

	data := runControl(t, "agent", "tree", rootID).Data(t)
	root := data["root"].(map[string]any)
	assert.Equal(t, rootID, root["id"])
	_, hasRootRelation := root["relation"]
	assert.False(t, hasRootRelation)

	children := root["children"].([]any)
	require.Len(t, children, 2)
	byID := map[string]map[string]any{}
	for _, c := range children {
		m := c.(map[string]any)
		byID[m["id"].(string)] = m
	}

	require.Contains(t, byID, subID)
	assert.Equal(t, "subagent", byID[subID]["relation"])
	assert.Equal(t, false, byID[subID]["open_in_tab"])

	require.Contains(t, byID, spawnedID)
	assert.Equal(t, "spawned", byID[spawnedID]["relation"])
	assert.Equal(t, rootID, byID[spawnedID]["spawned_by_agent_id"])
	assert.Equal(t, rootID, byID[spawnedID]["forked_from_agent_id"])
	assert.Equal(t, true, byID[spawnedID]["open_in_tab"])

	grandChildren := byID[spawnedID]["children"].([]any)
	require.Len(t, grandChildren, 1)
	gc := grandChildren[0].(map[string]any)
	assert.Equal(t, grandSpawnedID, gc["id"])
	assert.Equal(t, "spawned", gc["relation"])
	assert.Equal(t, spawnedID, gc["spawned_by_agent_id"])

	text := runControlText(t, "agent", "tree", rootID)
	require.Equal(t, cli.ExitOK, text.Code, text.Stderr)
	assert.Contains(t, text.Stdout, "[subagent]")
	assert.Contains(t, text.Stdout, "[spawned]")
	assert.Contains(t, text.Stdout, "(unopened)")
}

func TestAgentTreeOverIPC_PropagatesListTabsError(t *testing.T) {
	rootID := fullTestID("ag-root")
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: rootID},
			TileId:      "tile-1",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-1",
		},
	}
	disp := &treeAgentDispatcher{
		agents: []*leapmuxv1.AgentInfo{
			{Id: rootID, Title: "Root", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		},
	}
	startSpawnIPC(t, hub, disp, func(r *controlipc.Router) {
		base := r.Hub
		r.Hub = &interceptHub{
			base: base,
			handler: func(_ context.Context, _ userid.UserID, procedure string, _ []byte) ([]byte, error, bool) {
				if procedure == leapmuxv1connect.WorkspaceServiceListTabsProcedure {
					return nil, connect.NewError(connect.CodeUnavailable, "hub tab store unavailable"), true
				}
				return nil, nil, false
			},
		}
	})

	f := runControl(t, "agent", "tree", rootID).requireFailure(t, cli.CodeUnavailable)
	assert.Contains(t, f.Message, "hub tab store unavailable")
}

func TestAgentOpenTabOverIPC_FindsSubagentOutsideDestinationWorkspaceAndSetsOriginClient(t *testing.T) {
	parentID := fullTestID("ag-parent")
	subagentID := fullTestID("5e9a01bc-sub")

	state := newStateBuilder().
		workspace("ws-dest", "split-1").
		splitNode("split-1", "", "0|a:").
		tombstonedNode("tile-dead", "split-1").
		leafNode("tile-second", "split-1", "0|b:").
		leafNode("tile-first", "split-1", "0|a:").
		st

	hub := &recordingHub{
		workers: workerRows("worker-A"),
		workspaces: []*leapmuxv1.Workspace{
			{Id: "ws-src", Title: "Source"},
			{Id: "ws-dest", Title: "Dest"},
		},
		tabs: []*leapmuxv1.WorkspaceTab{
			{
				Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID},
				TileId:      "tile-src",
				WorkerId:    "worker-A",
				WorkspaceId: "ws-src",
			},
		},
		materialized: state,
	}
	disp := &treeAgentDispatcher{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID, Title: "Parent", WorkerId: "worker-A"},
			{Id: subagentID, Title: "Subagent", ParentAgentId: parentID, WorkerId: "worker-A"},
		},
	}

	var (
		mu             sync.Mutex
		listTabsReqs   []*leapmuxv1.ListTabsRequest
		submittedBatch *leapmuxv1.OpBatch
	)
	startSpawnIPC(t, hub, disp, func(r *controlipc.Router) {
		base := r.Hub
		r.Hub = &interceptHub{
			base: base,
			handler: func(ctx context.Context, uid userid.UserID, procedure string, payload []byte) ([]byte, error, bool) {
				switch procedure {
				case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
					var req leapmuxv1.ListTabsRequest
					require.NoError(t, proto.Unmarshal(payload, &req))
					mu.Lock()
					listTabsReqs = append(listTabsReqs, &req)
					mu.Unlock()
					// Respect WorkspaceIds filter so a bug that passes ws-dest would return nothing.
					if len(req.GetWorkspaceIds()) > 0 {
						var filtered []*leapmuxv1.WorkspaceTab
						for _, tab := range hub.tabs {
							for _, wid := range req.GetWorkspaceIds() {
								if tab.GetWorkspaceId() == wid {
									filtered = append(filtered, tab)
								}
							}
						}
						b, err := proto.Marshal(&leapmuxv1.ListTabsResponse{Tabs: filtered})
						return b, err, true
					}
					return nil, nil, false
				case leapmuxv1connect.UserCRDTSubmitOpsProcedure:
					var req leapmuxv1.SubmitOpsRequest
					require.NoError(t, proto.Unmarshal(payload, &req))
					require.Len(t, req.GetBatches(), 1)
					mu.Lock()
					submittedBatch = req.GetBatches()[0]
					mu.Unlock()
					b, err := proto.Marshal(&leapmuxv1.SubmitOpsResponse{
						Results: []*leapmuxv1.BatchResult{{BatchId: req.GetBatches()[0].GetBatchId()}},
					})
					return b, err, true
				default:
					return nil, nil, false
				}
			},
		}
	})

	data := runControl(t, "agent", "open-tab", "5e9a01bc", "--workspace", "ws-dest").Data(t)
	assert.Equal(t, subagentID, data["id"])
	assert.Equal(t, "ws-dest", data["workspace_id"])
	assert.Equal(t, "tile-first", data["tile_id"], "firstLiveLeaf selects the first live leaf in display order")
	assert.Equal(t, "worker-A", data["worker_id"])

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, listTabsReqs)
	assert.Empty(t, listTabsReqs[0].GetWorkspaceIds(), "subagent discovery must search across all workspaces, not the destination --workspace")
	require.NotNil(t, submittedBatch)
	require.Len(t, submittedBatch.GetOps(), 4)
	for i, op := range submittedBatch.GetOps() {
		assert.NotEmpty(t, op.GetOpId(), "op %d must carry an OpId", i)
		assert.NotEmpty(t, op.GetOriginClientId(), "op %d must carry OriginClientId from envelope()", i)
		assert.NotNil(t, op.GetClientHlc(), "op %d must carry ClientHlc", i)
	}
	assert.NotNil(t, submittedBatch.GetOps()[0].GetReviveTab())
	assert.Equal(t, "tile-first", submittedBatch.GetOps()[1].GetSetTabRegister().GetTileId())
	assert.Equal(t, "worker-A", submittedBatch.GetOps()[2].GetSetTabRegister().GetWorkerId())
	assert.NotEmpty(t, submittedBatch.GetOps()[3].GetSetTabRegister().GetPosition())
}

func TestAgentOpenTabOverIPC_RejectsMismatchedTileAndWorkspace(t *testing.T) {
	parentID := fullTestID("ag-parent")
	subagentID := fullTestID("5e9a01bc-sub")

	state := newStateBuilder().
		workspace("ws-1", "tile-ws1").
		leafNode("tile-ws1", "", "0|a:").
		workspace("ws-2", "tile-ws2").
		leafNode("tile-ws2", "", "0|a:").
		st

	hub := &recordingHub{
		workers: workerRows("worker-A"),
		workspaces: []*leapmuxv1.Workspace{
			{Id: "ws-1", Title: "One"},
			{Id: "ws-2", Title: "Two"},
		},
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID}, TileId: "tile-ws1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
		materialized: state,
	}
	disp := &treeAgentDispatcher{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID, Title: "Parent", WorkerId: "worker-A"},
			{Id: subagentID, Title: "Subagent", ParentAgentId: parentID, WorkerId: "worker-A"},
		},
	}

	startSpawnIPC(t, hub, disp, func(r *controlipc.Router) {
		base := r.Hub
		r.Hub = &interceptHub{
			base: base,
			handler: func(_ context.Context, _ userid.UserID, procedure string, payload []byte) ([]byte, error, bool) {
				switch procedure {
				case leapmuxv1connect.WorkspaceServiceLocateTileProcedure:
					var req leapmuxv1.LocateTileRequest
					require.NoError(t, proto.Unmarshal(payload, &req))
					ws := "ws-1"
					if req.GetTileId() == "tile-ws2" {
						ws = "ws-2"
					}
					b, err := proto.Marshal(&leapmuxv1.LocateTileResponse{WorkspaceId: ws})
					return b, err, true
				case leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:
					var req leapmuxv1.GetWorkspaceRequest
					require.NoError(t, proto.Unmarshal(payload, &req))
					b, err := proto.Marshal(&leapmuxv1.GetWorkspaceResponse{Workspace: &leapmuxv1.Workspace{Id: req.GetWorkspaceId()}})
					return b, err, true
				default:
					return nil, nil, false
				}
			},
		}
	})

	f := runControl(t, "agent", "open-tab", subagentID, "--tile", "id:tile-ws1", "--workspace", "id:ws-2").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "conflicting inputs")
}

func TestAgentOpenTabOverIPC_RejectsNonAgentExistingTab(t *testing.T) {
	termID := fullTestID("tm-existing")
	state := newStateBuilder().
		workspace("ws-1", "tile-1").
		leafNode("tile-1", "", "0|a:").
		tab(termID, "tile-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st

	hub := &recordingHub{
		workers:      workerRows("worker-A"),
		workspaces:   []*leapmuxv1.Workspace{{Id: "ws-1", Title: "One"}},
		tabs:         []*leapmuxv1.WorkspaceTab{},
		materialized: state,
	}
	var submitted bool
	startSpawnIPC(t, hub, &treeAgentDispatcher{}, func(r *controlipc.Router) {
		base := r.Hub
		r.Hub = &interceptHub{
			base: base,
			handler: func(_ context.Context, _ userid.UserID, procedure string, _ []byte) ([]byte, error, bool) {
				if procedure == leapmuxv1connect.UserCRDTSubmitOpsProcedure {
					submitted = true
				}
				return nil, nil, false
			},
		}
	})

	f := runControl(t, "agent", "open-tab", "id:"+termID, "--workspace", "id:ws-1", "--worker", "worker-A").requireFailure(t, cli.CodeInvalidArgument)
	assert.Contains(t, f.Message, "has type terminal; expected agent")
	assert.False(t, submitted, "a non-agent tab must never be revived as an agent tab")
}

func TestAgentOpenTabOverIPC_RejectsLongNonUUIDMissAndPropagatesListError(t *testing.T) {
	const longNonUUID = "not-a-uuid-prefix-36-chars-long-1234"
	require.Len(t, longNonUUID, 36)

	t.Run("36-char non-UUID miss returns not_found instead of falling through as a full ID", func(t *testing.T) {
		hub := &recordingHub{
			workers:    workerRows("worker-A"),
			workspaces: []*leapmuxv1.Workspace{{Id: "ws-1", Title: "One"}},
			tabs:       []*leapmuxv1.WorkspaceTab{},
		}
		startSpawnIPC(t, hub, &treeAgentDispatcher{})

		f := runControl(t, "agent", "open-tab", longNonUUID, "--workspace", "id:ws-1", "--worker", "worker-A").
			requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "no subagent matches")
	})

	t.Run("ListTabs Unavailable error propagates for 36-char non-UUID", func(t *testing.T) {
		hub := &recordingHub{
			workers:    workerRows("worker-A"),
			workspaces: []*leapmuxv1.Workspace{{Id: "ws-1", Title: "One"}},
		}
		startSpawnIPC(t, hub, &treeAgentDispatcher{}, func(r *controlipc.Router) {
			base := r.Hub
			r.Hub = &interceptHub{
				base: base,
				handler: func(_ context.Context, _ userid.UserID, procedure string, _ []byte) ([]byte, error, bool) {
					if procedure == leapmuxv1connect.WorkspaceServiceListTabsProcedure {
						return nil, connect.NewError(connect.CodeUnavailable, "hub tab store unavailable"), true
					}
					return nil, nil, false
				},
			}
		})

		f := runControl(t, "agent", "open-tab", longNonUUID, "--workspace", "id:ws-1", "--worker", "worker-A").
			requireFailure(t, cli.CodeUnavailable)
		assert.Contains(t, f.Message, "hub tab store unavailable")
	})
}

func TestResolveSubagentAcrossWorkers_ConcurrentAndDeterministic(t *testing.T) {
	t.Run("fans out concurrently across workers", func(t *testing.T) {
		entered := make(chan string, 2)
		release := make(chan struct{})
		byWorker := map[string][]string{
			"worker-b": {"root-b"},
			"worker-a": {"root-a"},
		}
		go func() {
			<-entered
			<-entered
			close(release)
		}()
		id, wk, verified, err := resolveSubagentAcrossWorkers(
			context.Background(),
			"sub-unique",
			byWorker,
			func(_ context.Context, workerID string, _ []string) ([]*leapmuxv1.AgentInfo, error) {
				entered <- workerID
				<-release
				if workerID == "worker-b" {
					return []*leapmuxv1.AgentInfo{{Id: "sub-unique-1234", Title: "Child"}}, nil
				}
				return nil, nil
			},
		)
		require.NoError(t, err)
		assert.True(t, verified)
		assert.Equal(t, "sub-unique-1234", id)
		assert.Equal(t, "worker-b", wk)
	})

	t.Run("exact match picks deterministically by worker ID and ignores failing sibling worker", func(t *testing.T) {
		byWorker := map[string][]string{
			"worker-c": {"root-c"},
			"worker-a": {"root-a"},
			"worker-b": {"root-b"},
		}
		id, wk, verified, err := resolveSubagentAcrossWorkers(
			context.Background(),
			"sub-exact",
			byWorker,
			func(_ context.Context, workerID string, _ []string) ([]*leapmuxv1.AgentInfo, error) {
				if workerID == "worker-a" {
					return nil, errors.New("worker offline")
				}
				return []*leapmuxv1.AgentInfo{{Id: "sub-exact", Title: "Dup on " + workerID}}, nil
			},
		)
		require.NoError(t, err)
		assert.True(t, verified)
		assert.Equal(t, "sub-exact", id)
		assert.Equal(t, "worker-b", wk, "lowest worker ID wins deterministically")
	})

	t.Run("ambiguous prefix returns sorted candidates deterministically", func(t *testing.T) {
		byWorker := map[string][]string{
			"worker-b": {"root-b"},
			"worker-a": {"root-a"},
		}
		_, _, verified, err := resolveSubagentAcrossWorkers(
			context.Background(),
			"sub-",
			byWorker,
			func(_ context.Context, workerID string, _ []string) ([]*leapmuxv1.AgentInfo, error) {
				if workerID == "worker-b" {
					return []*leapmuxv1.AgentInfo{{Id: "sub-zzz", Title: "Z"}, {Id: "sub-aaa", Title: "A"}}, nil
				}
				return []*leapmuxv1.AgentInfo{{Id: "sub-mmm", Title: "M"}}, nil
			},
		)
		assert.False(t, verified)
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeAmbiguousTarget, e.Code)
		require.Len(t, e.Candidates, 3)
		assert.Equal(t, []string{"sub-aaa", "sub-mmm", "sub-zzz"}, []string{
			e.Candidates[0].ID, e.Candidates[1].ID, e.Candidates[2].ID,
		})
	})

	t.Run("all workers failing returns no match and no error", func(t *testing.T) {
		byWorker := map[string][]string{
			"worker-a": {"root-a"},
			"worker-b": {"root-b"},
		}
		id, wk, verified, err := resolveSubagentAcrossWorkers(
			context.Background(),
			"sub-unique",
			byWorker,
			func(_ context.Context, workerID string, _ []string) ([]*leapmuxv1.AgentInfo, error) {
				return nil, errors.New(workerID + " offline")
			},
		)
		require.NoError(t, err)
		assert.False(t, verified)
		assert.Empty(t, id)
		assert.Empty(t, wk)
	})

	t.Run("empty worker map returns no match and no error", func(t *testing.T) {
		id, wk, verified, err := resolveSubagentAcrossWorkers(
			context.Background(),
			"sub-unique",
			nil,
			func(_ context.Context, _ string, _ []string) ([]*leapmuxv1.AgentInfo, error) {
				t.Fatal("callback must not run for an empty worker map")
				return nil, nil
			},
		)
		require.NoError(t, err)
		assert.False(t, verified)
		assert.Empty(t, id)
		assert.Empty(t, wk)
	})

	t.Run("exact short id matches while short non-exact prefix does not", func(t *testing.T) {
		byWorker := map[string][]string{"worker-a": {"root-a"}}
		list := func(_ context.Context, _ string, _ []string) ([]*leapmuxv1.AgentInfo, error) {
			return []*leapmuxv1.AgentInfo{
				{Id: "sub-unique-1234", Title: "Child"},
				{Id: "s1", Title: "Short"},
			}, nil
		}

		id, wk, verified, err := resolveSubagentAcrossWorkers(context.Background(), "s1", byWorker, list)
		require.NoError(t, err)
		assert.True(t, verified)
		assert.Equal(t, "s1", id)
		assert.Equal(t, "worker-a", wk)

		id, wk, verified, err = resolveSubagentAcrossWorkers(context.Background(), "sub", byWorker, list)
		require.NoError(t, err)
		assert.False(t, verified)
		assert.Empty(t, id)
		assert.Empty(t, wk)
	})
}

func TestAgentOpenTabOverIPC_NestedSplitLayoutAndEmptyWorkspace(t *testing.T) {
	parentID := fullTestID("ag-parent")
	subagentID := fullTestID("5e9a01bc-sub")

	t.Run("nested split skips dead branch and selects first live leaf in depth-first order", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-nested", "root-split").
			splitNode("root-split", "", "0|a:").
			splitNode("left-split", "root-split", "0|a:").
			tombstonedNode("dead-left", "left-split").
			splitNode("right-split", "root-split", "0|b:").
			splitNode("inner-split", "right-split", "0|a:").
			leafNode("leaf-far-right", "right-split", "0|b:").
			leafNode("leaf-inner-b", "inner-split", "0|b:").
			leafNode("leaf-inner-a", "inner-split", "0|a:").
			st

		hub := &recordingHub{
			workers: workerRows("worker-A"),
			workspaces: []*leapmuxv1.Workspace{
				{Id: "ws-nested", Title: "Nested"},
			},
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID}, TileId: "leaf-far-right", WorkerId: "worker-A", WorkspaceId: "ws-nested"},
			},
			materialized: state,
		}
		disp := &treeAgentDispatcher{
			agents: []*leapmuxv1.AgentInfo{
				{Id: parentID, Title: "Parent", WorkerId: "worker-A"},
				{Id: subagentID, Title: "Subagent", ParentAgentId: parentID, WorkerId: "worker-A"},
			},
		}
		startSpawnIPC(t, hub, disp, func(r *controlipc.Router) {
			base := r.Hub
			r.Hub = &interceptHub{
				base: base,
				handler: func(_ context.Context, _ userid.UserID, procedure string, payload []byte) ([]byte, error, bool) {
					if procedure == leapmuxv1connect.UserCRDTSubmitOpsProcedure {
						var req leapmuxv1.SubmitOpsRequest
						require.NoError(t, proto.Unmarshal(payload, &req))
						b, err := proto.Marshal(&leapmuxv1.SubmitOpsResponse{
							Results: []*leapmuxv1.BatchResult{{BatchId: req.GetBatches()[0].GetBatchId()}},
						})
						return b, err, true
					}
					return nil, nil, false
				},
			}
		})

		data := runControl(t, "agent", "open-tab", subagentID, "--workspace", "ws-nested").Data(t)
		assert.Equal(t, subagentID, data["id"])
		assert.Equal(t, "leaf-inner-a", data["tile_id"], "firstLiveLeaf must descend past dead branches into nested splits")
	})

	t.Run("workspace with no live leaf fails with not_found", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-dead", "split-dead").
			splitNode("split-dead", "", "0|a:").
			tombstonedNode("leaf-dead", "split-dead").
			st

		hub := &recordingHub{
			workers:      workerRows("worker-A"),
			workspaces:   []*leapmuxv1.Workspace{{Id: "ws-dead", Title: "Dead"}},
			tabs:         []*leapmuxv1.WorkspaceTab{{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID}, TileId: "t1", WorkerId: "worker-A", WorkspaceId: "ws-src"}},
			materialized: state,
		}
		disp := &treeAgentDispatcher{
			agents: []*leapmuxv1.AgentInfo{
				{Id: parentID, Title: "Parent", WorkerId: "worker-A"},
				{Id: subagentID, Title: "Subagent", ParentAgentId: parentID, WorkerId: "worker-A"},
			},
		}
		startSpawnIPC(t, hub, disp)

		f := runControl(t, "agent", "open-tab", subagentID, "--workspace", "ws-dead").requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "workspace ws-dead has no leaf tile")
	})
}

func TestAgentOpenTabOverIPC_UnverifiedFullIDChecksWorkerAndValidatesInput(t *testing.T) {
	subagentID := fullTestID("unseen-sub")

	state := newStateBuilder().
		workspace("ws-1", "tile-1").
		leafNode("tile-1", "", "0|a:").
		st

	t.Run("absent full id fails verification on worker without submitting ops", func(t *testing.T) {
		hub := &recordingHub{
			workers:      workerRows("worker-A"),
			workspaces:   []*leapmuxv1.Workspace{{Id: "ws-1", Title: "One"}},
			tabs:         []*leapmuxv1.WorkspaceTab{},
			materialized: state,
		}
		var submitted bool
		startSpawnIPC(t, hub, &treeAgentDispatcher{}, func(r *controlipc.Router) {
			base := r.Hub
			r.Hub = &interceptHub{
				base: base,
				handler: func(_ context.Context, _ userid.UserID, procedure string, _ []byte) ([]byte, error, bool) {
					if procedure == leapmuxv1connect.UserCRDTSubmitOpsProcedure {
						submitted = true
					}
					return nil, nil, false
				},
			}
		})

		f := runControl(t, "agent", "open-tab", subagentID, "--workspace", "id:ws-1", "--worker", "worker-A").requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "no subagent "+subagentID+" exists on worker")
		assert.False(t, submitted)
	})

	t.Run("invalid empty id: and short prefix fail with usage", func(t *testing.T) {
		hub := &recordingHub{
			workers:    workerRows("worker-A"),
			workspaces: []*leapmuxv1.Workspace{{Id: "ws-1", Title: "One"}},
			tabs:       []*leapmuxv1.WorkspaceTab{},
		}
		startSpawnIPC(t, hub, &treeAgentDispatcher{})

		f := runControl(t, "agent", "open-tab", "id:   ", "--workspace", "id:ws-1").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, `"id:" needs an id after the colon`)

		f = runControl(t, "agent", "open-tab", "ab", "--workspace", "id:ws-1").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "too short for a subagent id prefix")

		f = runControl(t, "agent", "open-tab", "id:ab", "--workspace", "id:ws-1").requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "no subagent matches")
	})

	t.Run("id: prefix matches unique subagent prefix and never matches title", func(t *testing.T) {
		parentID := fullTestID("ag-parent")
		targetID := fullTestID("7f8e9d0c-sub")
		hub := &recordingHub{
			workers:    workerRows("worker-A"),
			workspaces: []*leapmuxv1.Workspace{{Id: "ws-1", Title: "One"}},
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID}, TileId: "tile-1", WorkerId: "worker-A", WorkspaceId: "ws-1"},
			},
			materialized: state,
		}
		disp := &treeAgentDispatcher{
			agents: []*leapmuxv1.AgentInfo{
				{Id: parentID, Title: "Parent", WorkerId: "worker-A"},
				{Id: targetID, Title: "SubTitle", ParentAgentId: parentID, WorkerId: "worker-A"},
			},
		}
		startSpawnIPC(t, hub, disp, func(r *controlipc.Router) {
			base := r.Hub
			r.Hub = &interceptHub{
				base: base,
				handler: func(_ context.Context, _ userid.UserID, procedure string, payload []byte) ([]byte, error, bool) {
					if procedure == leapmuxv1connect.UserCRDTSubmitOpsProcedure {
						var req leapmuxv1.SubmitOpsRequest
						require.NoError(t, proto.Unmarshal(payload, &req))
						b, err := proto.Marshal(&leapmuxv1.SubmitOpsResponse{
							Results: []*leapmuxv1.BatchResult{{BatchId: req.GetBatches()[0].GetBatchId()}},
						})
						return b, err, true
					}
					return nil, nil, false
				},
			}
		})

		data := runControl(t, "agent", "open-tab", "id:7f8e9d0c", "--workspace", "id:ws-1").Data(t)
		assert.Equal(t, targetID, data["id"])

		f := runControl(t, "agent", "open-tab", "id:SubTitle", "--workspace", "id:ws-1").requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "no subagent matches")
	})
}
