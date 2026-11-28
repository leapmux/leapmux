package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

func TestPhase4_AgentSearchAndFileSearchGlob(t *testing.T) {
	agID := fullTestID("ag-s1")
	startPhase3BIPC(t,
		func(method string, _ []byte) (proto.Message, error) {
			switch method {
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
			case leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure:
				var req leapmuxv1.SearchAgentMessagesRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				assert.Equal(t, "timeout", req.GetQuery())
				assert.True(t, req.GetRegex())
				return &leapmuxv1.SearchAgentMessagesResponse{
					Hits: []*leapmuxv1.AgentMessageSearchHit{{
						AgentId:    agID,
						AgentTitle: "Fixer",
						Provider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
						Seq:        42,
						Source:     leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
						Snippet:    "increased timeout to 30s",
						CreatedAt:  "2026-11-23T12:00:00Z",
					}},
				}, nil
			case leapmuxv1connect.WorkerFileServiceSearchFilesProcedure:
				var req leapmuxv1.SearchFilesRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				assert.Equal(t, "NewLeaf", req.GetPattern())
				assert.True(t, req.GetIgnoreCase())
				assert.Equal(t, "**/*.go", req.GetGlob())
				return &leapmuxv1.SearchFilesResponse{
					Matches: []*leapmuxv1.FileSearchMatch{{Path: "cmd/root.go", Line: 18, Text: "cli.NewLeaf(...)"}},
				}, nil
			case leapmuxv1connect.WorkerFileServiceGlobFilesProcedure:
				var req leapmuxv1.GlobFilesRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				assert.Equal(t, "**/*.proto", req.GetPattern())
				return &leapmuxv1.GlobFilesResponse{Paths: []string{"leapmux/v1/agent.proto", "leapmux/v1/git.proto"}}, nil
			default:
				return nil, assert.AnError
			}
		},
	)

	// agent search
	res := runControl(t, "agent", "search", "timeout", "--regex", "--since", "24h").Data(t)
	hits := res["hits"].([]any)
	require.Len(t, hits, 1)
	assert.Equal(t, agID, hits[0].(map[string]any)["agent_id"])
	assert.Equal(t, float64(42), hits[0].(map[string]any)["seq"])

	quietSearch := runControlText(t, "agent", "search", "timeout", "--regex", "-q")
	require.Equal(t, 0, quietSearch.Code)
	assert.Equal(t, agID+":42\n", quietSearch.Stdout)

	// file search (via grep alias)
	fres := runControl(t, "file", "grep", "NewLeaf", "/repo", "-i", "--glob", "**/*.go").Data(t)
	matches := fres["matches"].([]any)
	require.Len(t, matches, 1)
	assert.Equal(t, "cmd/root.go", matches[0].(map[string]any)["path"])

	// file glob (via find alias)
	gres := runControl(t, "file", "find", "**/*.proto", "/repo").Data(t)
	paths := gres["paths"].([]any)
	assert.Equal(t, []any{"leapmux/v1/agent.proto", "leapmux/v1/git.proto"}, paths)
}

func TestPhase4_GitDiffLogCommitAndWorktreeOps(t *testing.T) {
	startPhase3BIPC(t,
		func(string, []byte) (proto.Message, error) { return &leapmuxv1.ListWorkspacesResponse{}, nil },
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure:
				var req leapmuxv1.GetGitDiffRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				assert.True(t, req.GetStaged())
				assert.Equal(t, "main", req.GetBaseRef())
				assert.Equal(t, []string{"README.md"}, req.GetFilePaths())
				return &leapmuxv1.GetGitDiffResponse{Diff: "+hello\n", TotalSize: 7}, nil
			case leapmuxv1connect.WorkerGitServiceGetGitLogProcedure:
				var req leapmuxv1.GetGitLogRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				assert.Equal(t, int32(5), req.GetLimit())
				return &leapmuxv1.GetGitLogResponse{
					Commits: []*leapmuxv1.GitCommitEntry{{
						Hash:       "abcdef1234567890",
						ShortHash:  "abcdef1",
						AuthorName: "Alice",
						AuthoredAt: "2026-11-23T10:00:00Z",
						Subject:    "Add git log",
					}},
				}, nil
			case leapmuxv1connect.WorkerGitServiceGitCommitProcedure:
				var req leapmuxv1.GitCommitRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				assert.Equal(t, "Ship it", req.GetMessage())
				assert.True(t, req.GetStageAll())
				return &leapmuxv1.GitCommitResponse{Hash: "1234567890abcdef", Subject: "Ship it"}, nil
			case leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure:
				var req leapmuxv1.CreateGitWorktreeRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				assert.Equal(t, "feat-x", req.GetBranch())
				return &leapmuxv1.CreateGitWorktreeResponse{WorktreePath: "/wt/feat-x", Branch: "feat-x", RepoRoot: "/repo"}, nil
			case leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure:
				var req leapmuxv1.RemoveGitWorktreeRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				assert.Equal(t, "/wt/feat-x", req.GetPath())
				assert.True(t, req.GetForce())
				return &leapmuxv1.RemoveGitWorktreeResponse{}, nil
			case leapmuxv1connect.WorkerGitServiceInspectWorktreeRemovalProcedure:
				return &leapmuxv1.InspectWorktreeRemovalResponse{BlockedReason: "worktree is locked"}, nil
			case leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure:
				return &leapmuxv1.InspectBranchDeletionResponse{
					IsWorktree:   true,
					WorktreePath: "/wt/feat-x",
					BranchName:   "feat-x",
					GitState:     &leapmuxv1.BranchGitState{HasUncommittedChanges: true, UnpushedCommitCount: 2},
				}, nil
			default:
				return nil, assert.AnError
			}
		},
	)

	diffOut := runControl(t, "git", "diff", "--staged", "--base", "main", "--dir", "/repo", "README.md").Data(t)
	assert.Equal(t, "+hello\n", diffOut["diff"])

	logOut := runControl(t, "git", "log", "-n", "5", "--dir", "/repo").Data(t)
	commits := logOut["commits"].([]any)
	require.Len(t, commits, 1)
	assert.Equal(t, "abcdef1", commits[0].(map[string]any)["short_hash"])

	commitOut := runControl(t, "git", "commit", "-a", "-m", "Ship it", "--dir", "/repo").Data(t)
	assert.Equal(t, "1234567890abcdef", commitOut["hash"])

	wtCreate := runControl(t, "worktree", "create", "feat-x", "--dir", "/repo").Data(t)
	assert.Equal(t, "/wt/feat-x", wtCreate["path"])

	wtInspect := runControl(t, "worktree", "inspect", "/wt/feat-x").Data(t)
	assert.Equal(t, "worktree is locked", wtInspect["removal_blocked_reason"])
	assert.Equal(t, float64(2), wtInspect["unpushed_commits"])

	wtRemove := runControl(t, "worktree", "remove", "/wt/feat-x", "--force").Data(t)
	assert.Equal(t, true, wtRemove["removed"])
}

func TestPhase4_WorkspaceExportAndApply(t *testing.T) {
	wsID := fullTestID("ws-exp")
	rootID := fullTestID("nd-root")
	leftID := fullTestID("nd-left")
	rightID := fullTestID("nd-right")
	agID := fullTestID("ag-exp")
	termID := fullTestID("tm-exp")
	fileID := fullTestID("fl-exp")
	newWSID := fullTestID("ws-new")
	newRootID := fullTestID("nd-newr")

	state := &leapmuxv1.UserMaterialized{
		Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{
			wsID:    {WorkspaceId: wsID, RootNodeId: rootID},
			newWSID: {WorkspaceId: newWSID, RootNodeId: newRootID},
		},
		Nodes: map[string]*leapmuxv1.NodeRecord{
			rootID: {
				NodeId:    rootID,
				Kind:      &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
				Direction: &leapmuxv1.LWWDirection{Value: leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL},
				Ratios:    &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.6, 0.4}}},
			},
			leftID: {
				NodeId:   leftID,
				Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF},
				ParentId: rootID,
				Position: &leapmuxv1.LWWString{Value: "a0"},
			},
			rightID: {
				NodeId:   rightID,
				Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF},
				ParentId: rootID,
				Position: &leapmuxv1.LWWString{Value: "a1"},
			},
			newRootID: {
				NodeId: newRootID,
				Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF},
			},
		},
		Tabs: map[string]*leapmuxv1.TabRecord{
			agID:   {TabId: agID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TileId: &leapmuxv1.LWWString{Value: leftID}, Position: &leapmuxv1.LWWString{Value: "a0"}, WorkerId: &leapmuxv1.LWWString{Value: "worker-A"}},
			termID: {TabId: termID, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TileId: &leapmuxv1.LWWString{Value: rightID}, Position: &leapmuxv1.LWWString{Value: "a0"}, WorkerId: &leapmuxv1.LWWString{Value: "worker-A"}},
			fileID: {TabId: fileID, TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TileId: &leapmuxv1.LWWString{Value: rightID}, Position: &leapmuxv1.LWWString{Value: "a1"}, WorkerId: &leapmuxv1.LWWString{Value: "worker-A"}},
		},
	}

	var openedAgents, openedTerms, openedFiles int
	startPhase3BIPC(t,
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:
				return &leapmuxv1.ListWorkspacesResponse{
					Workspaces: []*leapmuxv1.Workspace{{Id: wsID, Title: "Dev Template"}},
				}, nil
			case leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:
				return &leapmuxv1.GetWorkspaceResponse{
					Workspace: &leapmuxv1.Workspace{Id: wsID, Title: "Dev Template"},
				}, nil
			case leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure:
				return &leapmuxv1.CreateWorkspaceResponse{WorkspaceId: newWSID}, nil
			case leapmuxv1connect.UserCRDTGetMaterializedProcedure:
				return &leapmuxv1.GetMaterializedResponse{State: state}, nil
			case leapmuxv1connect.UserCRDTSubmitOpsProcedure:
				return &leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}}, nil
			case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
				return &leapmuxv1.ListTabsResponse{
					Tabs: []*leapmuxv1.WorkspaceTab{
						{Tab: &leapmuxv1.TabIdent{TabId: agID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkspaceId: wsID, WorkerId: "worker-A"},
						{Tab: &leapmuxv1.TabIdent{TabId: termID, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL}, WorkspaceId: wsID, WorkerId: "worker-A"},
						{Tab: &leapmuxv1.TabIdent{TabId: fileID, TabType: leapmuxv1.TabType_TAB_TYPE_FILE}, WorkspaceId: wsID, WorkerId: "worker-A"},
					},
				}, nil
			default:
				return &leapmuxv1.ListSectionsResponse{}, nil
			}
		},
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
				return &leapmuxv1.ListAgentsResponse{
					Agents: []*leapmuxv1.AgentInfo{{
						Id:            agID,
						Title:         "Coder",
						WorkerId:      "worker-A",
						WorkingDir:    "/repo",
						AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
						OptionGroups: []*leapmuxv1.AvailableOptionGroup{
							{Id: "model", CurrentValue: "opus", Mutable: true},
						},
					}},
				}, nil
			case leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure:
				return &leapmuxv1.ListTerminalsResponse{
					Terminals: []*leapmuxv1.TerminalInfo{{
						TerminalId: termID,
						Title:      "Shell",
						WorkingDir: "/repo",
					}},
				}, nil
			case leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure:
				return &leapmuxv1.GetTabPayloadResponse{
					Payload: &leapmuxv1.TabPayload{
						WorkingDir: "/repo",
						Kind:       &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: "/repo/README.md"}},
					},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
				var req leapmuxv1.OpenAgentRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				openedAgents++
				return &leapmuxv1.OpenAgentResponse{
					Agent: &leapmuxv1.AgentInfo{Id: req.GetCreation().GetTabId(), WorkerId: "worker-A", Title: req.GetTitle()},
					Publication: &leapmuxv1.PublishedTab{
						Tab:         &leapmuxv1.TabIdent{TabId: req.GetCreation().GetTabId(), TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
						WorkspaceId: newWSID,
						TileId:      req.GetCreation().GetPlacement().GetTileId(),
						Position:    "a0",
					},
				}, nil
			case leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure:
				var req leapmuxv1.OpenTerminalRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				openedTerms++
				return &leapmuxv1.OpenTerminalResponse{
					TerminalId: req.GetCreation().GetTabId(),
					Title:      req.GetTitle(),
					Publication: &leapmuxv1.PublishedTab{
						Tab:         &leapmuxv1.TabIdent{TabId: req.GetCreation().GetTabId(), TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
						WorkspaceId: newWSID,
						TileId:      req.GetCreation().GetPlacement().GetTileId(),
						Position:    "a0",
					},
				}, nil
			case leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure:
				var req leapmuxv1.RegisterTabPayloadRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				openedFiles++
				return &leapmuxv1.RegisterTabPayloadResponse{
					Publication: &leapmuxv1.PublishedTab{
						Tab:         &leapmuxv1.TabIdent{TabId: req.GetCreation().GetTabId(), TabType: leapmuxv1.TabType_TAB_TYPE_FILE},
						WorkspaceId: newWSID,
						TileId:      req.GetCreation().GetPlacement().GetTileId(),
						Position:    "a1",
					},
				}, nil
			default:
				return nil, assert.AnError
			}
		},
	)

	// Export to YAML file.
	outFile := filepath.Join(t.TempDir(), "dev.yaml")
	expRes := runControl(t, "workspace", "export", wsID, "--output", outFile).Data(t)
	assert.Equal(t, float64(3), expRes["tabs"])
	rawYAML, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Contains(t, string(rawYAML), "title: Dev Template")
	assert.Contains(t, string(rawYAML), "provider: claude-code")

	// Dry-run apply does not open any tabs.
	dry := runControl(t, "workspace", "apply", outFile, "--dry-run").Data(t)
	assert.Equal(t, true, dry["dry_run"])
	assert.Equal(t, float64(3), dry["planned_tabs"])
	assert.Equal(t, 0, openedAgents+openedTerms+openedFiles)

	// Real apply creates workspace + opens agent, terminal, and file tabs.
	applied := runControl(t, "workspace", "apply", outFile).Data(t)
	assert.Equal(t, newWSID, applied["workspace_id"])
	assert.Equal(t, true, applied["created"])
	assert.Equal(t, 1, openedAgents)
	assert.Equal(t, 1, openedTerms)
	assert.Equal(t, 1, openedFiles)
}

func TestPhase4_AgentForkClonesSettingsAndOptions(t *testing.T) {
	srcID := fullTestID("ag-src")
	wsID := fullTestID("ws-fk")
	tileID := fullTestID("tl-fk")
	var capturedOpen *leapmuxv1.OpenAgentRequest

	startPhase3BIPC(t,
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
			switch method {
			case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
				return &leapmuxv1.ListAgentsResponse{
					Agents: []*leapmuxv1.AgentInfo{{
						Id:            srcID,
						Title:         "Architect",
						WorkerId:      "worker-A",
						WorkingDir:    "/src/leapmux",
						AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
						OptionGroups: []*leapmuxv1.AvailableOptionGroup{
							{Id: "model", CurrentValue: "gpt-5", Mutable: true},
							{Id: "effort", CurrentValue: "high", Mutable: true},
							{Id: "sandbox", CurrentValue: "workspace-write", Mutable: true},
						},
					}},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
				var req leapmuxv1.OpenAgentRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				capturedOpen = &req
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

	res := runControl(t, "agent", "fork", srcID, "--worktree", "alt-design", "--effort", "medium", "--no-focus").Data(t)
	require.NotNil(t, capturedOpen)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, capturedOpen.GetAgentProvider())
	assert.Equal(t, "/src/leapmux", capturedOpen.GetWorkingDir())
	assert.Equal(t, "Architect (fork)", capturedOpen.GetTitle())
	assert.Equal(t, "gpt-5", capturedOpen.GetOptions()["model"])
	assert.Equal(t, "medium", capturedOpen.GetOptions()["effort"])
	assert.Equal(t, "workspace-write", capturedOpen.GetOptions()["sandbox"])
	assert.Equal(t, "alt-design", capturedOpen.GetGitSetup().GetCreateWorktree().GetBranch())
	assert.NotEmpty(t, res["request_id"], "fork auto-generates request_id receipt")
}

func TestPhase4_RetryReceiptsAndAgentRequestStatus(t *testing.T) {
	agID := fullTestID("ag-rc")
	reqID := "deploy-check-99"
	inputID := requestScopedID(reqID, requestScopeAgentSend, agID)

	// Verify disposition classification and structured JSON receipt.
	wrapped := wrapRetryReceipt(
		cli.Newf(cli.CodeUnavailable, "channel dropped"),
		retryStageDuringSend,
		retryReceipt{
			RequestID:    reqID,
			QueryCommand: buildAgentRequestStatusCmd(agID, reqID),
			RetryCommand: buildAgentSendRetryCmd(nil, agID, "Run tests", true, reqID),
		},
	)
	ce := cli.FromError(wrapped)
	require.NotNil(t, ce.Receipt)
	assert.Equal(t, dispositionOutcomeUnknown, ce.Receipt.Disposition)
	assert.Equal(t, reqID, ce.Receipt.RequestID)
	assert.Contains(t, ce.Receipt.QueryCommand, "agent request-status")
	assert.Contains(t, ce.Receipt.RetryCommand, "--request-id "+reqID)

	usageWrapped := wrapRetryReceipt(cli.Usagef("bad arg"), retryStageDuringSend, retryReceipt{RequestID: reqID})
	assert.Equal(t, dispositionNotSent, cli.FromError(usageWrapped).Receipt.Disposition)

	// Verify agent request-status against Worker IPC.
	startPhase3BIPC(t,
		func(method string, _ []byte) (proto.Message, error) {
			tab := &leapmuxv1.WorkspaceTab{
				Tab:      &leapmuxv1.TabIdent{TabId: agID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
				WorkerId: "worker-A",
			}
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				return &leapmuxv1.LocateTabResponse{Tab: tab}, nil
			case leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure:
				return &leapmuxv1.GetWorkerResponse{Worker: &leapmuxv1.Worker{Id: "worker-A", Online: true}}, nil
			default:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{tab}}, nil
			}
		},
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
				var req leapmuxv1.ListAgentInputQueueRequest
				if err := proto.Unmarshal(body, &req); !assert.NoError(t, err) {
					return nil, err
				}
				assert.Equal(t, inputID, req.GetReplyInputId())
				return &leapmuxv1.ListAgentInputQueueResponse{
					Snapshot: &leapmuxv1.AgentInputQueueSnapshot{},
					InputReply: &leapmuxv1.AgentInputTurnReply{
						AcceptedSeq: 12,
						Outcome:     leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
					},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
				reply := "All 42 tests passed."
				return &leapmuxv1.ListAgentMessagesResponse{
					Messages: []*leapmuxv1.AgentChatMessage{{
						Seq:       13,
						Source:    leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
						ReplyText: &reply,
					}},
				}, nil
			default:
				return nil, assert.AnError
			}
		},
	)

	status := runControl(t, "agent", "request-status", agID, reqID).Data(t)
	assert.Equal(t, "completed", status["status"])
	assert.Equal(t, "completed", status["turn_outcome"])
	assert.Equal(t, inputID, status["input_id"])
	assert.Equal(t, []any{"All 42 tests passed."}, status["replies"])

	qStatus := runControlText(t, "agent", "request-status", agID, reqID, "-q")
	require.Equal(t, 0, qStatus.Code)
	assert.Equal(t, "completed\n", qStatus.Stdout)
}
