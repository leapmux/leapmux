package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func getAgentSnapshot(t *testing.T, d testRPCDispatcher, agentID string) (*leapmuxv1.GetAgentSnapshotResponse, *testResponseWriter) {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceGetAgentSnapshotProcedure, &leapmuxv1.GetAgentSnapshotRequest{
		AgentId: agentID,
	}, w)
	if len(w.errors) > 0 {
		return nil, w
	}
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.GetAgentSnapshotResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	return &resp, w
}

func TestGetAgentSnapshot_RootChildClosedAndArchived(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, _ := setupTestService(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	const rootID = "snap-root"
	const childID = "snap-child"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            rootID,
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            childID,
		ParentAgentID: sql.NullString{String: rootID, Valid: true},
		WorkingDir:    t.TempDir(),
		Title:         "Child",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Seed root todos, background tasks, and goal.
	require.NoError(t, svc.Queries.InsertAgentTodo(ctx, db.InsertAgentTodoParams{
		AgentID: rootID,
		Seq:     1,
		TaskID:  "t-1",
		Content: "Run tests",
		Status:  leapmuxv1.TodoStatus(todoevents.StatusInProgress),
	}))
	writeBackgroundTaskFixture(t, svc.Queries, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: rootID,
		RowKey:       "bg-1",
		Seq:          1,
		Kind:         leapmuxv1.BackgroundTaskKind(bgtask.KindShell),
		Role:         leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_TASK_OUTCOME,
		Title:        "go test ./...",
		Status:       leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
		CreatedAt:    sqltime.NewSQLiteTime(now),
		UpdatedAt:    sqltime.NewSQLiteTime(now),
	})
	require.NoError(t, svc.Queries.UpdateAgentGoal(ctx, db.UpdateAgentGoalParams{
		GoalObjective:    "Ship clean snapshots",
		GoalStatus:       leapmuxv1.AgentGoalStatus(agent.GoalStatusActive),
		GoalStatusDetail: "active",
		GoalCreatedAt:    sqltime.SQLiteNullTimeOf(now),
		GoalUpdatedAt:    sqltime.SQLiteNullTimeOf(now),
		ID:               rootID,
	}))

	// Seed a child-only todo.
	require.NoError(t, svc.Queries.InsertAgentTodo(ctx, db.InsertAgentTodoParams{
		AgentID: childID,
		Seq:     1,
		TaskID:  "c-1",
		Content: "Inspect logs",
		Status:  leapmuxv1.TodoStatus(todoevents.StatusPending),
	}))

	// Root returns todos, background tasks, and goal.
	rootSnap, w := getAgentSnapshot(t, d, rootID)
	require.Empty(t, w.errors)
	require.NotNil(t, rootSnap.GetTodos())
	require.Len(t, rootSnap.GetTodos().GetTodos(), 1)
	assert.Equal(t, "Run tests", rootSnap.GetTodos().GetTodos()[0].GetContent())
	require.NotNil(t, rootSnap.GetBackgroundTasks())
	require.Len(t, rootSnap.GetBackgroundTasks().GetTasks(), 1)
	assert.Equal(t, "go test ./...", rootSnap.GetBackgroundTasks().GetTasks()[0].GetTitle())
	require.NotNil(t, rootSnap.GetGoal())
	require.NotNil(t, rootSnap.GetGoal().GetGoal())
	assert.Equal(t, "Ship clean snapshots", rootSnap.GetGoal().GetGoal().GetObjective())

	// Child returns its own todos plus present empty background tasks and empty goal.
	childSnap, w := getAgentSnapshot(t, d, childID)
	require.Empty(t, w.errors)
	require.NotNil(t, childSnap.GetTodos())
	require.Len(t, childSnap.GetTodos().GetTodos(), 1)
	assert.Equal(t, "Inspect logs", childSnap.GetTodos().GetTodos()[0].GetContent())
	require.NotNil(t, childSnap.GetBackgroundTasks())
	assert.Empty(t, childSnap.GetBackgroundTasks().GetTasks())
	require.NotNil(t, childSnap.GetGoal())
	assert.Nil(t, childSnap.GetGoal().GetGoal())

	// Archived agent stays readable.
	applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		&leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: rootID})
	archivedSnap, w := getAgentSnapshot(t, d, rootID)
	require.Empty(t, w.errors)
	require.NotNil(t, archivedSnap.GetTodos())
	assert.Len(t, archivedSnap.GetTodos().GetTodos(), 1)
	require.NotNil(t, archivedSnap.GetBackgroundTasks())
	assert.Len(t, archivedSnap.GetBackgroundTasks().GetTasks(), 1)
	require.NotNil(t, archivedSnap.GetGoal())
	require.NotNil(t, archivedSnap.GetGoal().GetGoal())
	assert.Equal(t, "Ship clean snapshots", archivedSnap.GetGoal().GetGoal().GetObjective())

	// Closed agent returns an empty response.
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, rootID)))
	closedSnap, w := getAgentSnapshot(t, d, rootID)
	require.Empty(t, w.errors)
	assert.Nil(t, closedSnap.GetTodos())
	assert.Nil(t, closedSnap.GetBackgroundTasks())
	assert.Nil(t, closedSnap.GetGoal())
}

func TestGetAgentSnapshot_RejectsEmptyAndUnknownAgentID(t *testing.T) {
	t.Parallel()

	_, d, _ := setupTestService(t)

	t.Run("empty agent_id returns InvalidArgument", func(t *testing.T) {
		resp, w := getAgentSnapshot(t, d, "")
		assert.Nil(t, resp)
		assert.Empty(t, w.responses)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)
	})

	t.Run("nonexistent agent_id returns NotFound", func(t *testing.T) {
		resp, w := getAgentSnapshot(t, d, "nonexistent-agent")
		assert.Nil(t, resp)
		assert.Empty(t, w.responses)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.NotFound), w.errors[0].code)
	})
}
