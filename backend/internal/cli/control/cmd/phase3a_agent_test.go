package cmd

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

type phase3aWorker struct {
	mu              sync.Mutex
	calls           []string
	agents          []*leapmuxv1.AgentInfo
	messages        []*leapmuxv1.AgentChatMessage
	goal            *leapmuxv1.AgentGoalSnapshot
	todos           *leapmuxv1.AgentTodoSnapshot
	tasks           *leapmuxv1.AgentBackgroundTaskSnapshot
	sessionInfoJSON string
	marks           []*leapmuxv1.MessageMark
	queue           *leapmuxv1.AgentInputQueueSnapshot
	recoveries      []*leapmuxv1.AgentInputRecoveryTarget
}

func (w *phase3aWorker) record(method string) {
	w.mu.Lock()
	w.calls = append(w.calls, method)
	w.mu.Unlock()
}

func (w *phase3aWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	m := req.GetMethod()
	w.record(m)
	switch m {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: w.agents})
	case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentMessagesResponse{
			Messages:        w.messages,
			Goal:            w.goal,
			Todos:           w.todos,
			BackgroundTasks: w.tasks,
		})
	case leapmuxv1connect.WorkerAgentServiceGetAgentSnapshotProcedure:
		writeProtoResponse(rw, &leapmuxv1.GetAgentSnapshotResponse{
			Goal:            w.goal,
			Todos:           w.todos,
			BackgroundTasks: w.tasks,
		})
	case leapmuxv1connect.WorkerAgentServiceUpdateAgentGoalProcedure:
		writeProtoResponse(rw, &leapmuxv1.UpdateAgentGoalResponse{})
	case leapmuxv1connect.WorkerAgentServiceGetAgentSessionInfoProcedure:
		writeProtoResponse(rw, &leapmuxv1.GetAgentSessionInfoResponse{SessionInfoJson: w.sessionInfoJSON})
	case leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure:
		writeProtoResponse(rw, &leapmuxv1.RespondAgentStartupPromptResponse{})
	case leapmuxv1connect.WorkerAgentServiceRetryAgentTurnProcedure:
		writeProtoResponse(rw, &leapmuxv1.RetryAgentTurnResponse{})
	case leapmuxv1connect.WorkerAgentServiceApproveAutoReviewDenialProcedure:
		writeProtoResponse(rw, &leapmuxv1.ApproveAutoReviewDenialResponse{})
	case leapmuxv1connect.WorkerAgentServiceGetAgentMessageProcedure:
		var msg *leapmuxv1.AgentChatMessage
		if len(w.messages) > 0 {
			msg = w.messages[0]
		}
		writeProtoResponse(rw, &leapmuxv1.GetAgentMessageResponse{Message: msg})
	case leapmuxv1connect.WorkerAgentServiceGetAgentSpanMessagesProcedure:
		writeProtoResponse(rw, &leapmuxv1.GetAgentSpanMessagesResponse{Messages: w.messages})
	case leapmuxv1connect.WorkerAgentServiceListMessageMarksProcedure:
		minSeq, maxSeq := int64(1), int64(5)
		writeProtoResponse(rw, &leapmuxv1.ListMessageMarksResponse{Marks: w.marks, MinSeq: &minSeq, MaxSeq: &maxSeq})
	case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentInputQueueResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServiceBeginQueuedAgentInputEditProcedure:
		writeProtoResponse(rw, &leapmuxv1.BeginQueuedAgentInputEditResponse{Snapshot: w.queue, Text: "full queued text"})
	case leapmuxv1connect.WorkerAgentInputServiceUpdateQueuedAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.UpdateQueuedAgentInputResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServiceCancelQueuedAgentInputEditProcedure:
		writeProtoResponse(rw, &leapmuxv1.CancelQueuedAgentInputEditResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServiceSteerQueuedAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.SteerQueuedAgentInputResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServicePreemptQueuedAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.PreemptQueuedAgentInputResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServiceAdvanceAgentInputQueueProcedure:
		writeProtoResponse(rw, &leapmuxv1.AdvanceAgentInputQueueResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServiceGetQueuedAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.GetQueuedAgentInputResponse{Text: "full queued text"})
	case leapmuxv1connect.WorkerAgentInputServiceRetryQueuedAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.RetryQueuedAgentInputResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServiceDeleteQueuedAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.DeleteQueuedAgentInputResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServiceMoveQueuedAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.MoveQueuedAgentInputResponse{Snapshot: w.queue})
	case leapmuxv1connect.WorkerAgentInputServiceListAgentInputRecoveriesProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentInputRecoveriesResponse{Targets: w.recoveries})
	case leapmuxv1connect.WorkerAgentInputServiceRecoverAgentInputProcessProcedure:
		writeProtoResponse(rw, &leapmuxv1.RecoverAgentInputProcessResponse{Snapshot: w.queue})
	default:
		_ = rw.SendError(12, "unimplemented: "+m)
	}
}

func TestPhase3A_AgentGoalTodosTasksUsageAndOps(t *testing.T) {
	agID := fullTestID("ag-root")
	childID := fullTestID("ag-sub")
	reply := "All tests passed."
	w := &phase3aWorker{
		agents: []*leapmuxv1.AgentInfo{
			{
				Id:               agID,
				Title:            "Root",
				AgentProvider:    leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
				ActivityState:    leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
				AcceptsMessages:  true,
				AcceptsInterrupt: true,
				StartupPrompt:    &leapmuxv1.AgentStartupPrompt{PromptId: "prompt-kc", Title: "Keychain access"},
			},
			{
				Id:            childID,
				Title:         "Subagent",
				ParentAgentId: agID,
				RootAgentId:   agID,
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			},
		},
		messages: []*leapmuxv1.AgentChatMessage{
			{Id: "m-1", Seq: 1, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, ReplyText: &reply, Content: []byte(`{"text":"All tests passed."}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE},
		},
		goal: &leapmuxv1.AgentGoalSnapshot{
			Goal:             &leapmuxv1.AgentGoal{Objective: "Ship v2 CLI", Status: leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE},
			SupportedActions: []leapmuxv1.AgentGoalAction{leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_PAUSE, leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_CLEAR},
		},
		todos: &leapmuxv1.AgentTodoSnapshot{
			Todos: []*leapmuxv1.TodoItem{{Id: "t1", Content: "Run lint", Status: leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS}},
		},
		tasks: &leapmuxv1.AgentBackgroundTaskSnapshot{
			Tasks: []*leapmuxv1.BackgroundTaskItem{
				{Id: "bg-1", Kind: leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING, Title: "Research", ChildAgentId: childID},
				{Id: "bg-2", Kind: leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, Title: "go test"},
			},
		},
		sessionInfoJSON: `{"total_cost_usd":0.42,"context_usage":{"usage_percent":33.5,"context_tokens":67000,"context_window":200000},"running_tool":{"span_id":"span-1","elapsed_seconds":12},"rate_limits":{"values":[{"rate_limit_type":"five_hour","status":"allowed","utilization":0.25}]},"turn_retry_offer":{"turn_id":"turn-9","model":"fast-1"}}`,
		marks:           []*leapmuxv1.MessageMark{{Seq: 1, Type: leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE}},
		queue: &leapmuxv1.AgentInputQueueSnapshot{
			Revision:            7,
			ActiveTurn:          true,
			ActiveTurnSteerable: true,
			ProcessRecovery:     &leapmuxv1.AgentInputProcessRecovery{OutputGeneration: 2, ProcessInstanceId: "proc-1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE},
			Items: []*leapmuxv1.QueuedAgentInput{
				{Id: "in-11111111", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, Text: "first", ReorderSegment: 1, CanSteer: true, CanPreempt: true, Version: 1},
				{Id: "in-22222222", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED, Text: "second", Error: "broken pipe", ReorderSegment: 1, Version: 2},
			},
		},
		recoveries: []*leapmuxv1.AgentInputRecoveryTarget{
			{AgentId: agID, Title: "Root", Recovery: &leapmuxv1.AgentInputProcessRecovery{OutputGeneration: 2, ProcessInstanceId: "proc-1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}},
		},
	}
	startAgentWorker(t, agID, w)

	// Surfaced AgentInfo fields on `agent get`.
	agData := runControl(t, "agent", "get", agID).Data(t)
	assert.Equal(t, true, agData["accepts_messages"])
	assert.Equal(t, true, agData["accepts_interrupt"])
	require.NotNil(t, agData["startup_prompt"])

	// Goal get / set / pause / resume / clear.
	goalData := runControl(t, "agent", "goal", "get", agID).Data(t)
	assert.Equal(t, "Ship v2 CLI", goalData["objective"])
	assert.Equal(t, "active", goalData["status"])
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "goal", "set", agID, "New objective").Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "goal", "pause", agID).Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "goal", "resume", agID).Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "goal", "clear", agID).Code)

	// Todos & Tasks (--active filtering).
	todos := runControl(t, "agent", "todos", agID).Data(t)["todos"].([]any)
	require.Len(t, todos, 1)
	tasksAll := runControl(t, "agent", "tasks", agID).Data(t)["tasks"].([]any)
	require.Len(t, tasksAll, 2)
	tasksActive := runControl(t, "agent", "tasks", agID, "--active").Data(t)["tasks"].([]any)
	require.Len(t, tasksActive, 1)

	w.mu.Lock()
	snapshotCalls := append([]string(nil), w.calls...)
	w.mu.Unlock()
	assert.Contains(t, snapshotCalls, leapmuxv1connect.WorkerAgentServiceGetAgentSnapshotProcedure)
	assert.NotContains(t, snapshotCalls, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure)

	// Usage (GetAgentSessionInfo).
	usageOut := runControlText(t, "agent", "usage", agID)
	require.Equal(t, cli.ExitOK, usageOut.Code, usageOut.Stderr)
	assert.Contains(t, usageOut.Stdout, "$0.4200")
	assert.Contains(t, usageOut.Stdout, "33.5%")
	assert.Contains(t, usageOut.Stdout, "span-1")
	assert.Contains(t, usageOut.Stdout, "five_hour")

	// Queue superpowers: get, edit, steer, preempt, advance, retry, clear, move --after, send --steer/--preempt.
	qGet := runControl(t, "agent", "queue", "get", agID, "in-1111").Data(t)
	assert.Equal(t, "full queued text", qGet["text"])
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "queue", "edit", agID, "in-1111", "updated prompt").Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "queue", "steer", agID).Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "queue", "preempt", agID).Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "queue", "advance", agID).Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "queue", "retry", agID).Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "queue", "move", agID, "in-1111", "--after", "in-2222").Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "queue", "clear", agID).Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "send", agID, "steer now", "--steer").Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "send", agID, "preempt now", "--preempt").Code)

	// Recoveries & recover.
	recList := runControl(t, "agent", "recoveries", "--worker", "worker-A").Data(t)["targets"].([]any)
	require.Len(t, recList, 1)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "recover", agID).Code)

	// Startup approve/cancel, retry-turn (auto-reads offer), approve-review.
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "startup", "approve", agID).Code)
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "startup", "cancel", agID).Code)
	retryData := runControl(t, "agent", "retry-turn", agID).Data(t)
	assert.Equal(t, "turn-9", retryData["turn_id"])
	assert.Equal(t, "fast-1", retryData["model"])
	require.Equal(t, cli.ExitOK, runControl(t, "agent", "approve-review", agID, "--message-id", "m-1", "--review-id", "r-1").Code)

	// Message, span, marks, export.
	msgOut := runControl(t, "agent", "message", agID, "1").Data(t)
	assert.Equal(t, "m-1", msgOut["id"])
	spanOut := runControl(t, "agent", "span", agID, "toolu_1").Data(t)["messages"].([]any)
	require.Len(t, spanOut, 1)
	marksOut := runControl(t, "agent", "marks", agID).Data(t)["marks"].([]any)
	require.Len(t, marksOut, 1)
	exportPath := filepath.Join(t.TempDir(), "transcript.md")
	expOut := runControl(t, "agent", "export", agID, "--style", "markdown", "--output", exportPath)
	require.Equal(t, cli.ExitOK, expOut.Code, expOut.Stderr)
	exported, err := os.ReadFile(exportPath)
	require.NoError(t, err)
	assert.Contains(t, string(exported), "All tests passed.")

	// Agent tree shows unopened subagent.
	treeOut := runControlText(t, "agent", "tree", agID)
	require.Equal(t, cli.ExitOK, treeOut.Code, treeOut.Stderr)
	assert.Contains(t, treeOut.Stdout, cli.ShortID(childID))
	assert.Contains(t, treeOut.Stdout, "(unopened)")
}

func TestPhase3A_AgentOpenTabSubmitsReviveTabOp(t *testing.T) {
	wsID := fullTestID("ws-open")
	tileID := fullTestID("tile-1")
	subID := fullTestID("ag-child")
	hub := &rpcHub{
		answers: map[string]proto.Message{
			leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure: &leapmuxv1.ListWorkspacesResponse{
				Workspaces: []*leapmuxv1.Workspace{{Id: wsID, Title: "Main"}},
			},
			leapmuxv1connect.UserCRDTGetMaterializedProcedure: &leapmuxv1.GetMaterializedResponse{
				State: &leapmuxv1.UserMaterialized{
					Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{wsID: {RootNodeId: tileID}},
					Nodes: map[string]*leapmuxv1.NodeRecord{
						tileID: {
							NodeId: tileID,
							Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF},
						},
					},
				},
			},
			leapmuxv1connect.UserCRDTSubmitOpsProcedure: &leapmuxv1.SubmitOpsResponse{
				Results: []*leapmuxv1.BatchResult{{}},
			},
		},
	}
	worker := &phase3aWorker{
		agents: []*leapmuxv1.AgentInfo{{Id: subID}},
	}
	startRPCIPC(t, hub, worker)
	out := runControl(t, "agent", "open-tab", subID, "--workspace", wsID)
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	calls := hub.callsOf(leapmuxv1connect.UserCRDTSubmitOpsProcedure)
	require.Len(t, calls, 1)
	var req leapmuxv1.SubmitOpsRequest
	require.NoError(t, proto.Unmarshal(calls[0].payload, &req))
	require.Len(t, req.GetBatches(), 1)
	ops := req.GetBatches()[0].GetOps()
	require.Len(t, ops, 4)
	assert.Equal(t, subID, ops[0].GetReviveTab().GetTab().GetTabId())
	assert.Equal(t, tileID, ops[1].GetSetTabRegister().GetTileId())
	assert.NotEmpty(t, ops[2].GetSetTabRegister().GetWorkerId())
	assert.NotEmpty(t, ops[3].GetSetTabRegister().GetPosition())
}

func TestPhase3A_AgentOpenTabRefusesNonexistentSubagent(t *testing.T) {
	wsID := fullTestID("ws-open")
	tileID := fullTestID("tile-1")
	ghostID := fullTestID("ag-ghost")
	hub := &rpcHub{
		answers: map[string]proto.Message{
			leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure: &leapmuxv1.ListWorkspacesResponse{
				Workspaces: []*leapmuxv1.Workspace{{Id: wsID, Title: "Main"}},
			},
			leapmuxv1connect.UserCRDTGetMaterializedProcedure: &leapmuxv1.GetMaterializedResponse{
				State: &leapmuxv1.UserMaterialized{
					Workspaces: map[string]*leapmuxv1.WorkspaceContentsRecord{wsID: {RootNodeId: tileID}},
					Nodes: map[string]*leapmuxv1.NodeRecord{
						tileID: {
							NodeId: tileID,
							Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF},
						},
					},
				},
			},
		},
	}
	worker := &phase3aWorker{}
	startRPCIPC(t, hub, worker)
	out := runControl(t, "agent", "open-tab", ghostID, "--workspace", wsID)
	require.Equal(t, cli.ExitNotFound, out.Code, out.Stderr)
	assert.Empty(t, hub.callsOf(leapmuxv1connect.UserCRDTSubmitOpsProcedure), "nonexistent subagent must not submit ReviveTabOp")
}
