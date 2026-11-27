package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

type codedWorkerDispatcher struct {
	fn func(method string, body []byte) (proto.Message, error)
}

func (w *codedWorkerDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	msg, err := w.fn(req.GetMethod(), req.GetPayload())
	if err != nil {
		code := int32(connect.CodeOf(err))
		if code == 0 {
			code = int32(connect.CodeInternal)
		}
		var ce *connect.Error
		if connectErr, ok := err.(*connect.Error); ok {
			ce = connectErr
		}
		text := err.Error()
		if ce != nil {
			text = ce.Message()
		}
		_ = rw.SendError(code, text)
		return
	}
	writeProtoResponse(rw, msg)
}

func startForkIPC(t *testing.T, hubFn func(string, []byte) (proto.Message, error), workerFn func(string, []byte) (proto.Message, error)) {
	t.Helper()
	startSpawnIPC(t, &recordingHub{}, &codedWorkerDispatcher{fn: workerFn}, func(r *controlipc.Router) {
		r.Hub = &phase3bHub{fn: hubFn}
	})
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")
}

func TestAgentFork_UseWorktreeDirectoryValidation(t *testing.T) {
	srcID := fullTestID("ag-fksrc")
	wsID := fullTestID("ws-fkwt")
	tileID := fullTestID("tl-fkwt")

	makeEnv := func(t *testing.T, captured *[]*leapmuxv1.OpenAgentRequest) {
		t.Helper()
		startForkIPC(t,
			func(method string, _ []byte) (proto.Message, error) {
				tab := &leapmuxv1.WorkspaceTab{
					Tab:         &leapmuxv1.TabIdent{TabId: srcID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
					WorkspaceId: wsID,
					TileId:      tileID,
					WorkerId:    "worker-A",
				}
				switch method {
				case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
					return &leapmuxv1.LocateTabResponse{Tab: tab}, nil
				case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
					return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{tab}}, nil
				case leapmuxv1connect.WorkerManagementServiceListWorkersProcedure:
					return &leapmuxv1.ListWorkersResponse{
						Workers: []*leapmuxv1.Worker{
							{Id: "worker-A", Online: true},
							{Id: "worker-B", Online: true},
						},
					}, nil
				default:
					return &leapmuxv1.ListWorkspacesResponse{}, nil
				}
			},
			func(method string, body []byte) (proto.Message, error) {
				switch method {
				case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
					return &leapmuxv1.ListAgentsResponse{
						Agents: []*leapmuxv1.AgentInfo{{
							Id:            srcID,
							Title:         "Source",
							WorkerId:      "worker-A",
							WorkingDir:    "/repo/source",
							AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
						}},
					}, nil
				case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
					var req leapmuxv1.OpenAgentRequest
					require.NoError(t, proto.Unmarshal(body, &req))
					*captured = append(*captured, &req)
					return &leapmuxv1.OpenAgentResponse{
						Agent: &leapmuxv1.AgentInfo{Id: req.GetCreation().GetTabId(), WorkerId: "worker-A", Title: req.GetTitle()},
						Publication: &leapmuxv1.PublishedTab{
							Tab:         &leapmuxv1.TabIdent{TabId: req.GetCreation().GetTabId(), TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
							WorkspaceId: wsID,
							TileId:      tileID,
							Position:    "a1",
						},
					}, nil
				default:
					return nil, assert.AnError
				}
			},
		)
	}

	t.Run("use-worktree path clears inherited source WorkingDir", func(t *testing.T) {
		var captured []*leapmuxv1.OpenAgentRequest
		makeEnv(t, &captured)
		_ = runControl(t, "agent", "fork", srcID, "--use-worktree", "path:/wt/existing", "--no-focus").Data(t)
		require.Len(t, captured, 1)
		assert.Equal(t, "", captured[0].GetWorkingDir())
		assert.Equal(t, "/wt/existing", captured[0].GetGitSetup().GetUseWorktree().GetPath())
	})

	t.Run("use-worktree branch on same worker defaults to source WorkingDir", func(t *testing.T) {
		var captured []*leapmuxv1.OpenAgentRequest
		makeEnv(t, &captured)
		_ = runControl(t, "agent", "fork", srcID, "--use-worktree", "branch:feat-1", "--no-focus").Data(t)
		require.Len(t, captured, 1)
		assert.Equal(t, "/repo/source", captured[0].GetWorkingDir())
		assert.Equal(t, "feat-1", captured[0].GetGitSetup().GetUseWorktree().GetBranch())
	})

	t.Run("use-worktree with whitespace-only --dir fails with usage error", func(t *testing.T) {
		var captured []*leapmuxv1.OpenAgentRequest
		makeEnv(t, &captured)
		f := runControl(t, "agent", "fork", srcID, "--use-worktree", "branch:feat-1", "--dir", "   ", "--no-focus").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--dir is empty")
		assert.Empty(t, captured)
	})

	t.Run("use-worktree branch across workers without --dir fails with usage error", func(t *testing.T) {
		var captured []*leapmuxv1.OpenAgentRequest
		makeEnv(t, &captured)
		f := runControl(t, "agent", "fork", srcID, "--worker", "worker-B", "--use-worktree", "branch:feat-1", "--no-focus").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "branch selection requires --dir on the selected worker")
		assert.Empty(t, captured)
	})
}

func TestAgentFork_RejectsMismatchedWorkspaceAndTile(t *testing.T) {
	srcID := fullTestID("ag-fkpl")
	ws1 := fullTestID("ws-pl1")
	ws2 := fullTestID("ws-pl2")
	tileInWS1 := fullTestID("tl-pl1")
	openCalled := false

	startForkIPC(t,
		func(method string, _ []byte) (proto.Message, error) {
			tab := &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabId: srcID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
				WorkspaceId: ws1,
				TileId:      tileInWS1,
				WorkerId:    "worker-A",
			}
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				return &leapmuxv1.LocateTabResponse{Tab: tab}, nil
			case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{tab}}, nil
			case leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:
				return &leapmuxv1.ListWorkspacesResponse{
					Workspaces: []*leapmuxv1.Workspace{
						{Id: ws1, Title: "WS 1"},
						{Id: ws2, Title: "WS 2"},
					},
				}, nil
			case leapmuxv1connect.WorkspaceServiceLocateTileProcedure:
				return &leapmuxv1.LocateTileResponse{WorkspaceId: ws1}, nil
			default:
				return &leapmuxv1.ListWorkspacesResponse{}, nil
			}
		},
		func(method string, _ []byte) (proto.Message, error) {
			if method == leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure {
				openCalled = true
			}
			return &leapmuxv1.ListAgentsResponse{}, nil
		},
	)

	f := runControl(t, "agent", "fork", srcID, "--workspace", ws2, "--tile", tileInWS1, "--no-focus").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "conflicting inputs")
	assert.False(t, openCalled)
}

func TestAgentFork_PostOpenEnqueueFailureReportsInProgressWithCreatedAgentHint(t *testing.T) {
	srcID := fullTestID("ag-fkpo")
	wsID := fullTestID("ws-fkpo")
	tileID := fullTestID("tl-fkpo")
	attachPath := filepath.Join(t.TempDir(), "note.txt")
	require.NoError(t, os.WriteFile(attachPath, []byte("hello"), 0o600))

	var openedID string
	var rpcOrder []string

	startForkIPC(t,
		func(method string, _ []byte) (proto.Message, error) {
			tab := &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabId: srcID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
				WorkspaceId: wsID,
				TileId:      tileID,
				WorkerId:    "worker-A",
			}
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				return &leapmuxv1.LocateTabResponse{Tab: tab}, nil
			case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{tab}}, nil
			default:
				return &leapmuxv1.ListWorkspacesResponse{}, nil
			}
		},
		func(method string, body []byte) (proto.Message, error) {
			rpcOrder = append(rpcOrder, method)
			switch method {
			case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
				return &leapmuxv1.ListAgentsResponse{
					Agents: []*leapmuxv1.AgentInfo{{
						Id:            srcID,
						Title:         "Source",
						WorkerId:      "worker-A",
						WorkingDir:    "/repo",
						AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
					}},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
				var req leapmuxv1.OpenAgentRequest
				require.NoError(t, proto.Unmarshal(body, &req))
				openedID = req.GetCreation().GetTabId()
				return &leapmuxv1.OpenAgentResponse{
					Agent: &leapmuxv1.AgentInfo{Id: openedID, WorkerId: "worker-A", Title: req.GetTitle()},
					Publication: &leapmuxv1.PublishedTab{
						Tab:         &leapmuxv1.TabIdent{TabId: openedID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
						WorkspaceId: wsID,
						TileId:      tileID,
						Position:    "a1",
					},
				}, nil
			case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
				return nil, connect.NewError(connect.CodeInvalidArgument, "unsupported attachment type")
			default:
				return nil, assert.AnError
			}
		},
	)

	outcome := runControl(t, "agent", "fork", srcID, "--message", "check this", "--attach", attachPath, "--request-id", "fork-req-1", "--no-focus")
	require.NotEqual(t, cli.ExitOK, outcome.Code)
	lines := outcome.lines()
	require.NotEmpty(t, lines)

	var env struct {
		Error struct {
			Code    cli.Code          `json:"code"`
			Message string            `json:"message"`
			Hints   []string          `json:"hints"`
			Receipt *cli.RetryReceipt `json:"receipt"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &env), "stdout: %s", outcome.Stdout)
	assert.Equal(t, cli.CodeInvalidArgument, env.Error.Code)
	require.NotNil(t, env.Error.Receipt)
	assert.Equal(t, dispositionInProgress, env.Error.Receipt.Disposition, "must not report not_sent after the tab was already created")
	assert.Equal(t, "fork-req-1", env.Error.Receipt.RequestID)
	assert.Contains(t, env.Error.Receipt.QueryCommand, openedID)
	assert.Contains(t, env.Error.Receipt.RetryCommand, "--request-id fork-req-1")
	assert.Contains(t, env.Error.Hints, "agent "+openedID+" was already created")
	assert.Equal(t, []string{
		leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
		leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure,
		leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure,
	}, rpcOrder)
}

func TestAgentFork_DerivesDistinctRequestScopeFromAgentOpen(t *testing.T) {
	const reqID = "shared-req-1"
	srcID := fullTestID("ag-fkscope")
	wsID := fullTestID("ws-fkscope")
	tileID := fullTestID("tl-fkscope")
	openTabID := requestScopedID(reqID, requestScopeAgentOpen, "worker-A")
	forkTabID := requestScopedID(reqID, requestScopeAgentFork, "worker-A")
	require.NotEqual(t, openTabID, forkTabID)

	startEnv := func(t *testing.T, extraTabs []*leapmuxv1.WorkspaceTab, extraAgents []*leapmuxv1.AgentInfo, captured *[]*leapmuxv1.OpenAgentRequest) {
		t.Helper()
		srcTab := &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabId: srcID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			WorkspaceId: wsID,
			TileId:      tileID,
			WorkerId:    "worker-A",
		}
		allTabs := append([]*leapmuxv1.WorkspaceTab{srcTab}, extraTabs...)
		allAgents := append([]*leapmuxv1.AgentInfo{{
			Id:            srcID,
			Title:         "Source",
			WorkerId:      "worker-A",
			WorkingDir:    "/repo",
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		}}, extraAgents...)
		startForkIPC(t,
			func(method string, _ []byte) (proto.Message, error) {
				switch method {
				case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
					return &leapmuxv1.LocateTabResponse{Tab: srcTab}, nil
				case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
					return &leapmuxv1.ListTabsResponse{Tabs: allTabs}, nil
				case leapmuxv1connect.WorkerManagementServiceListWorkersProcedure:
					return &leapmuxv1.ListWorkersResponse{
						Workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}},
					}, nil
				default:
					return &leapmuxv1.ListWorkspacesResponse{}, nil
				}
			},
			func(method string, body []byte) (proto.Message, error) {
				switch method {
				case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
					return &leapmuxv1.ListAgentsResponse{Agents: allAgents}, nil
				case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
					var req leapmuxv1.OpenAgentRequest
					require.NoError(t, proto.Unmarshal(body, &req))
					*captured = append(*captured, &req)
					return &leapmuxv1.OpenAgentResponse{
						Agent: &leapmuxv1.AgentInfo{Id: req.GetCreation().GetTabId(), WorkerId: "worker-A", Title: req.GetTitle()},
						Publication: &leapmuxv1.PublishedTab{
							Tab:         &leapmuxv1.TabIdent{TabId: req.GetCreation().GetTabId(), TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
							WorkspaceId: wsID,
							TileId:      tileID,
							Position:    "a1",
						},
					}, nil
				default:
					return nil, assert.AnError
				}
			},
		)
	}

	t.Run("derives tab ID under agent fork scope instead of agent open scope", func(t *testing.T) {
		var captured []*leapmuxv1.OpenAgentRequest
		startEnv(t, nil, nil, &captured)

		data := runControl(t, "agent", "fork", srcID, "--request-id", reqID, "--no-focus").Data(t)
		require.Len(t, captured, 1)
		assert.Equal(t, forkTabID, captured[0].GetCreation().GetTabId())
		assert.NotEqual(t, openTabID, captured[0].GetCreation().GetTabId())
		assert.Equal(t, forkTabID, data["id"])
	})

	t.Run("slug check conflicts with an agent open tab sharing the same request ID", func(t *testing.T) {
		var captured []*leapmuxv1.OpenAgentRequest
		openTab := &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabId: openTabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			WorkspaceId: wsID,
			TileId:      tileID,
			WorkerId:    "worker-A",
		}
		openAgent := &leapmuxv1.AgentInfo{Id: openTabID, Title: "reviewer", WorkerId: "worker-A"}
		startEnv(t, []*leapmuxv1.WorkspaceTab{openTab}, []*leapmuxv1.AgentInfo{openAgent}, &captured)

		f := runControl(t, "agent", "fork", srcID, "--request-id", reqID, "--name", "reviewer", "--no-focus").requireFailure(t, cli.CodeConflict)
		assert.Contains(t, f.Message, `agent name "reviewer" is already in use`)
		assert.Empty(t, captured)
	})

	t.Run("slug check allows retry matching the agent fork scope tab ID", func(t *testing.T) {
		var captured []*leapmuxv1.OpenAgentRequest
		forkTab := &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabId: forkTabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			WorkspaceId: wsID,
			TileId:      tileID,
			WorkerId:    "worker-A",
		}
		forkAgent := &leapmuxv1.AgentInfo{Id: forkTabID, Title: "reviewer", WorkerId: "worker-A"}
		startEnv(t, []*leapmuxv1.WorkspaceTab{forkTab}, []*leapmuxv1.AgentInfo{forkAgent}, &captured)

		data := runControl(t, "agent", "fork", srcID, "--request-id", reqID, "--name", "reviewer", "--no-focus").Data(t)
		require.Len(t, captured, 1)
		assert.Equal(t, forkTabID, captured[0].GetCreation().GetTabId())
		assert.Equal(t, forkTabID, data["id"])
	})
}

func TestAgentFork_SetsDeterministicInitialMessageID(t *testing.T) {
	srcID := fullTestID("ag-fkinit")
	wsID := fullTestID("ws-fkinit")
	tileID := fullTestID("tl-fkinit")

	startEnv := func(t *testing.T, captured *[]*leapmuxv1.OpenAgentRequest) {
		t.Helper()
		srcTab := &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabId: srcID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			WorkspaceId: wsID,
			TileId:      tileID,
			WorkerId:    "worker-A",
		}
		startForkIPC(t,
			func(method string, _ []byte) (proto.Message, error) {
				switch method {
				case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
					return &leapmuxv1.LocateTabResponse{Tab: srcTab}, nil
				case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
					return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{srcTab}}, nil
				default:
					return &leapmuxv1.ListWorkspacesResponse{}, nil
				}
			},
			func(method string, body []byte) (proto.Message, error) {
				switch method {
				case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
					return &leapmuxv1.ListAgentsResponse{
						Agents: []*leapmuxv1.AgentInfo{{
							Id:            srcID,
							Title:         "Source",
							WorkerId:      "worker-A",
							WorkingDir:    "/repo",
							AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
						}},
					}, nil
				case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
					var req leapmuxv1.OpenAgentRequest
					require.NoError(t, proto.Unmarshal(body, &req))
					*captured = append(*captured, &req)
					return &leapmuxv1.OpenAgentResponse{
						Agent: &leapmuxv1.AgentInfo{Id: req.GetCreation().GetTabId(), WorkerId: "worker-A", Title: req.GetTitle()},
						Publication: &leapmuxv1.PublishedTab{
							Tab:         &leapmuxv1.TabIdent{TabId: req.GetCreation().GetTabId(), TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
							WorkspaceId: wsID,
							TileId:      tileID,
							Position:    "a1",
						},
					}, nil
				default:
					return nil, assert.AnError
				}
			},
		)
	}

	t.Run("with initial message and request-id sets deterministic initial_message_id", func(t *testing.T) {
		var captured []*leapmuxv1.OpenAgentRequest
		startEnv(t, &captured)

		const reqID = "fork-msg-1"
		data := runControl(t, "agent", "fork", srcID, "--message", "Try alternative", "--request-id", reqID, "--no-focus").Data(t)

		wantTab := requestScopedID(reqID, requestScopeAgentFork, "worker-A")
		assert.Equal(t, wantTab, data["id"])
		require.Len(t, captured, 1)
		require.NotNil(t, captured[0].InitialMessage)
		assert.Equal(t, "Try alternative", captured[0].GetInitialMessage())
		require.NotNil(t, captured[0].InitialMessageId)
		assert.Equal(t, requestScopedID(reqID, requestScopeAgentSend, wantTab), captured[0].GetInitialMessageId())
	})

	t.Run("without initial message leaves initial_message_id nil even when request-id is set", func(t *testing.T) {
		var captured []*leapmuxv1.OpenAgentRequest
		startEnv(t, &captured)

		const reqID = "fork-nomsg-1"
		data := runControl(t, "agent", "fork", srcID, "--request-id", reqID, "--no-focus").Data(t)

		wantTab := requestScopedID(reqID, requestScopeAgentFork, "worker-A")
		assert.Equal(t, wantTab, data["id"])
		require.Len(t, captured, 1)
		assert.Nil(t, captured[0].InitialMessage)
		assert.Nil(t, captured[0].InitialMessageId)
	})
}

func TestAgentFork_SameWorkerWithAttachReusesSingleChannel(t *testing.T) {
	srcID := fullTestID("ag-fkch")
	wsID := fullTestID("ws-fkch")
	tileID := fullTestID("tl-fkch")
	attachPath := filepath.Join(t.TempDir(), "spec.txt")
	require.NoError(t, os.WriteFile(attachPath, []byte("attachment body"), 0o600))

	var (
		mu         sync.Mutex
		rpcOrder   []string
		openReq    *leapmuxv1.OpenAgentRequest
		enqueueReq *leapmuxv1.EnqueueAgentInputRequest
	)
	disp := &codedWorkerDispatcher{fn: func(method string, body []byte) (proto.Message, error) {
		mu.Lock()
		rpcOrder = append(rpcOrder, method)
		mu.Unlock()
		switch method {
		case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
			return &leapmuxv1.ListAgentsResponse{
				Agents: []*leapmuxv1.AgentInfo{{
					Id:            srcID,
					Title:         "Source",
					WorkerId:      "worker-A",
					WorkingDir:    "/repo",
					AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				}},
			}, nil
		case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
			var req leapmuxv1.OpenAgentRequest
			require.NoError(t, proto.Unmarshal(body, &req))
			mu.Lock()
			openReq = &req
			mu.Unlock()
			tabID := req.GetCreation().GetTabId()
			return &leapmuxv1.OpenAgentResponse{
				Agent: &leapmuxv1.AgentInfo{Id: tabID, WorkerId: "worker-A", Title: req.GetTitle()},
				Publication: &leapmuxv1.PublishedTab{
					Tab:         &leapmuxv1.TabIdent{TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
					WorkspaceId: wsID,
					TileId:      tileID,
					Position:    "a1",
				},
			}, nil
		case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
			var req leapmuxv1.EnqueueAgentInputRequest
			require.NoError(t, proto.Unmarshal(body, &req))
			mu.Lock()
			enqueueReq = &req
			mu.Unlock()
			return &leapmuxv1.EnqueueAgentInputResponse{}, nil
		default:
			return nil, assert.AnError
		}
	}}

	hub := &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabId: srcID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			WorkspaceId: wsID,
			TileId:      tileID,
			WorkerId:    "worker-A",
		},
	}
	h := startHubChannelHarness(t, hub, disp)

	const reqID = "fork-attach-ch-1"
	data := runControl(t, "agent", "fork", srcID, "--message", "check attachment", "--attach", attachPath, "--request-id", reqID, "--no-focus", "--hub", h.url).Data(t)

	wantTabID := requestScopedID(reqID, requestScopeAgentFork, "worker-A")
	assert.Equal(t, wantTabID, data["id"])
	assert.Equal(t, 1, h.channelOpens(), "same-worker agent fork with --attach must reuse one E2EE channel")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{
		leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
		leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure,
		leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure,
	}, rpcOrder)
	require.NotNil(t, openReq)
	assert.Nil(t, openReq.InitialMessage, "message with attachment travels via EnqueueAgentInput instead of OpenAgent")
	require.NotNil(t, enqueueReq)
	assert.Equal(t, wantTabID, enqueueReq.GetAgentId())
	assert.Equal(t, requestScopedID(reqID, requestScopeAgentSend, wantTabID), enqueueReq.GetInputId())
	assert.Equal(t, "check attachment", enqueueReq.GetText())
	require.Len(t, enqueueReq.GetAttachments(), 1)
	assert.Equal(t, "spec.txt", enqueueReq.GetAttachments()[0].GetFilename())
	assert.Equal(t, []byte("attachment body"), enqueueReq.GetAttachments()[0].GetData())
}
