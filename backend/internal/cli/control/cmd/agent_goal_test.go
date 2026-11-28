package cmd

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func TestRunAgentUsage_PropagatesMalformedSessionInfoJSON(t *testing.T) {
	agID := fullTestID("ag-usage")

	t.Run("malformed JSON returns internal error", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{sessionInfoJSON: "{broken"})
		f := runControl(t, "agent", "usage", agID).requireFailure(t, cli.CodeInternal)
		assert.Contains(t, f.Message, "parse agent session info")
	})

	t.Run("non-object JSON returns internal error", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{sessionInfoJSON: "[1, 2, 3]"})
		f := runControl(t, "agent", "usage", agID).requireFailure(t, cli.CodeInternal)
		assert.Contains(t, f.Message, "parse agent session info")
	})

	t.Run("empty session info succeeds with empty metrics", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{sessionInfoJSON: "   "})
		out := runControlText(t, "agent", "usage", agID)
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Contains(t, out.Stdout, "no live usage metrics reported yet")
	})

	t.Run("valid JSON populates usage fields and renders retry offer", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{
			sessionInfoJSON: `{"total_cost_usd": 1.25, "turn_retry_offer": {"turn_id": " turn-3 ", "model": " gpt-fast "}}`,
		})
		data := runControl(t, "agent", "usage", agID).Data(t)
		assert.Equal(t, agID, data["agent_id"])
		assert.Equal(t, 1.25, data["total_cost_usd"])

		textOut := runControlText(t, "agent", "usage", agID)
		require.Equal(t, cli.ExitOK, textOut.Code, textOut.Stderr)
		assert.Contains(t, textOut.Stdout, "retry:    turn=turn-3 model=gpt-fast")
	})
}

func TestParseAgentSessionInfoAndExtractTurnRetryOffer(t *testing.T) {
	t.Run("parseAgentSessionInfo handles empty, null, valid, non-object, and invalid JSON", func(t *testing.T) {
		empty, err := parseAgentSessionInfo("")
		require.NoError(t, err)
		assert.Empty(t, empty)

		whitespace, err := parseAgentSessionInfo("  \t\n ")
		require.NoError(t, err)
		assert.Empty(t, whitespace)

		nullInfo, err := parseAgentSessionInfo("null")
		require.NoError(t, err)
		assert.Empty(t, nullInfo)

		valid, err := parseAgentSessionInfo(`{"total_cost_usd":0.5}`)
		require.NoError(t, err)
		assert.Equal(t, 0.5, valid["total_cost_usd"])

		for _, raw := range []string{"{bad", "[]", "[1,2]", `"str"`, "123"} {
			_, err = parseAgentSessionInfo(raw)
			require.Error(t, err, "input=%s", raw)
			assert.Equal(t, cli.CodeInternal, cli.FromError(err).Code, "input=%s", raw)
		}
	})

	t.Run("extractTurnRetryOffer handles absent, malformed, and valid offers", func(t *testing.T) {
		turnID, model := extractTurnRetryOffer(nil)
		assert.Empty(t, turnID)
		assert.Empty(t, model)

		turnID, model = extractTurnRetryOffer(map[string]any{"turn_retry_offer": "not-a-map"})
		assert.Empty(t, turnID)
		assert.Empty(t, model)

		turnID, model = extractTurnRetryOffer(map[string]any{
			"turn_retry_offer": map[string]any{"turn_id": " t-1 ", "model": " fast-m "},
		})
		assert.Equal(t, "t-1", turnID)
		assert.Equal(t, "fast-m", model)
	})

	t.Run("RenderText renders standalone RetryOffer when SessionInfo is empty", func(t *testing.T) {
		var buf bytes.Buffer
		view := agentUsageView{
			AgentID:    "ag-1",
			RetryOffer: map[string]any{"turn_id": "t-9", "model": "fast-9"},
		}
		require.NoError(t, view.RenderText(&buf))
		assert.Contains(t, buf.String(), "retry:    turn=t-9 model=fast-9")
	})
}

type snapshotOnlyWorker struct {
	mu    sync.Mutex
	calls []string
	resp  *leapmuxv1.GetAgentSnapshotResponse
}

func (w *snapshotOnlyWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	method := req.GetMethod()
	w.mu.Lock()
	w.calls = append(w.calls, method)
	w.mu.Unlock()
	if method == leapmuxv1connect.WorkerAgentServiceGetAgentSnapshotProcedure {
		writeProtoResponse(rw, w.resp)
		return
	}
	_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+method)
}

func (w *snapshotOnlyWorker) recordedCalls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}

func TestAgentSnapshotCommands_UseGetAgentSnapshotNotListMessages(t *testing.T) {
	agID := fullTestID("ag-snap")
	w := &snapshotOnlyWorker{
		resp: &leapmuxv1.GetAgentSnapshotResponse{
			Goal: &leapmuxv1.AgentGoalSnapshot{
				Goal:             &leapmuxv1.AgentGoal{Objective: "Keep snapshot fast", Status: leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_ACTIVE},
				SupportedActions: []leapmuxv1.AgentGoalAction{leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_PAUSE},
			},
			Todos: &leapmuxv1.AgentTodoSnapshot{
				Todos: []*leapmuxv1.TodoItem{{Id: "td-1", Content: "Verify snapshot RPC", Status: leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS}},
			},
			BackgroundTasks: &leapmuxv1.AgentBackgroundTaskSnapshot{
				Tasks: []*leapmuxv1.BackgroundTaskItem{
					{Id: "bg-1", Kind: leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING, Title: "lint"},
					{Id: "bg-2", Kind: leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, Title: "build"},
				},
			},
		},
	}
	startAgentWorker(t, agID, w)

	goalData := runControl(t, "agent", "goal", "get", agID).Data(t)
	assert.Equal(t, "Keep snapshot fast", goalData["objective"])
	assert.Equal(t, "active", goalData["status"])

	todosData := runControl(t, "agent", "todos", agID).Data(t)
	todos, ok := todosData["todos"].([]any)
	require.True(t, ok)
	require.Len(t, todos, 1)

	tasksData := runControl(t, "agent", "tasks", agID).Data(t)
	tasks, ok := tasksData["tasks"].([]any)
	require.True(t, ok)
	require.Len(t, tasks, 2)

	activeTasksData := runControl(t, "agent", "tasks", agID, "--active").Data(t)
	activeTasks, ok := activeTasksData["tasks"].([]any)
	require.True(t, ok)
	require.Len(t, activeTasks, 1)

	calls := w.recordedCalls()
	assert.Equal(t, []string{
		leapmuxv1connect.WorkerAgentServiceGetAgentSnapshotProcedure,
		leapmuxv1connect.WorkerAgentServiceGetAgentSnapshotProcedure,
		leapmuxv1connect.WorkerAgentServiceGetAgentSnapshotProcedure,
		leapmuxv1connect.WorkerAgentServiceGetAgentSnapshotProcedure,
	}, calls)
	assert.NotContains(t, calls, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure)
}

func TestAgentSnapshotCommands_EmptySnapshotsAndValidation(t *testing.T) {
	agID := fullTestID("ag-empty-snap")

	t.Run("empty snapshot returns empty lists and minimal goal view", func(t *testing.T) {
		startAgentWorker(t, agID, &snapshotOnlyWorker{
			resp: &leapmuxv1.GetAgentSnapshotResponse{},
		})

		goalData := runControl(t, "agent", "goal", "get", agID).Data(t)
		assert.Equal(t, map[string]any{"agent_id": agID}, goalData)

		todosData := runControl(t, "agent", "todos", agID).Data(t)
		assert.Equal(t, []any{}, todosData["todos"], "empty todos must serialize as an empty array")
		todosQuiet := runControlText(t, "agent", "todos", agID, "-q")
		require.Equal(t, cli.ExitOK, todosQuiet.Code, todosQuiet.Stderr)
		assert.Empty(t, todosQuiet.Stdout)

		tasksData := runControl(t, "agent", "tasks", agID).Data(t)
		assert.Equal(t, []any{}, tasksData["tasks"], "empty tasks must serialize as an empty array")
		activeTasksData := runControl(t, "agent", "tasks", agID, "--active").Data(t)
		assert.Equal(t, []any{}, activeTasksData["tasks"])
		tasksQuiet := runControlText(t, "agent", "tasks", agID, "-q")
		require.Equal(t, cli.ExitOK, tasksQuiet.Code, tasksQuiet.Stderr)
		assert.Empty(t, tasksQuiet.Stdout)
	})

	t.Run("goal snapshot without active goal preserves supported actions", func(t *testing.T) {
		startAgentWorker(t, agID, &snapshotOnlyWorker{
			resp: &leapmuxv1.GetAgentSnapshotResponse{
				Goal: &leapmuxv1.AgentGoalSnapshot{
					SupportedActions: []leapmuxv1.AgentGoalAction{
						leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET,
					},
				},
			},
		})

		goalData := runControl(t, "agent", "goal", "get", agID).Data(t)
		assert.Equal(t, agID, goalData["agent_id"])
		assert.Nil(t, goalData["objective"])
		assert.Nil(t, goalData["status"])
		assert.Equal(t, []any{"set"}, goalData["supported_actions"])
	})

	t.Run("goal set rejects whitespace objective before dialing", func(t *testing.T) {
		clearRemoteEnv(t)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())

		f := runControl(t, "agent", "goal", "set", agID, "   ", "--hub", "https://stub.invalid").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "the objective is empty")
	})
}
