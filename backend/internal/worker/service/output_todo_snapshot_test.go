package service

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapturedTodoSnapshotFreezesInputAndClearsWithoutTranscript(t *testing.T) {
	t.Parallel()
	h, queries, services, _ := newCapturedPlanTest(t)
	items := []todoevents.Item{{Content: "0"}, {Content: "Finished", Status: todoevents.StatusCompleted}}
	captured := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
	prepared, err := services.PrepareTodoSnapshot(captured, items)
	require.NoError(t, err)
	items[0].Content = "mutated"
	items[1].Status = 999
	applied, err := prepared.Persist()
	require.NoError(t, err)
	require.True(t, applied)
	stored, err := h.LoadTodos(t.Context(), "plan-owner")
	require.NoError(t, err)
	assert.Equal(t, []todoevents.Item{{Content: "0", Status: todoevents.StatusPending}, {Content: "Finished", Status: todoevents.StatusCompleted}}, stored)
	prepared, err = services.PrepareTodoSnapshot(captured, nil)
	require.NoError(t, err)
	applied, err = prepared.Persist()
	require.NoError(t, err)
	require.True(t, applied)
	stored, err = h.LoadTodos(t.Context(), "plan-owner")
	require.NoError(t, err)
	assert.Empty(t, stored)
	rows, err := queries.ListMessagesByAgentID(t.Context(), db.ListMessagesByAgentIDParams{AgentID: "plan-owner", Limit: 100})
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestCapturedTodoSnapshotPreservesStateAcrossFailedTransaction(t *testing.T) {
	t.Parallel()
	h, queries, services, _ := newCapturedPlanTest(t)
	captured := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
	baseline := []todoevents.Item{{Content: "Existing", Status: todoevents.StatusPending}}
	prepared, err := services.PrepareTodoSnapshot(captured, baseline)
	require.NoError(t, err)
	applied, err := prepared.Persist()
	require.NoError(t, err)
	require.True(t, applied)
	_, err = h.db.ExecContext(t.Context(), `CREATE TRIGGER reject_todo_snapshot BEFORE INSERT ON agent_todos WHEN NEW.content = 'reject' BEGIN SELECT RAISE(ABORT, 'todo insert held'); END`)
	require.NoError(t, err)
	items := []todoevents.Item{{Content: "First", Status: todoevents.StatusInProgress}, {Content: "reject", Status: todoevents.StatusCompleted}}
	prepared, err = services.PrepareTodoSnapshot(captured, items)
	require.NoError(t, err)
	applied, err = prepared.Persist()
	require.ErrorContains(t, err, "todo insert held")
	assert.False(t, applied)
	warm, err := h.LoadTodos(t.Context(), "plan-owner")
	require.NoError(t, err)
	assert.Equal(t, baseline, warm)
	rows, err := queries.ListAgentTodosNewestFirst(t.Context(), db.ListAgentTodosNewestFirstParams{AgentID: "plan-owner", Limit: 100})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "Existing", rows[0].Content)
	_, err = h.db.ExecContext(t.Context(), "DROP TRIGGER reject_todo_snapshot")
	require.NoError(t, err)
	applied, err = prepared.Persist()
	require.NoError(t, err)
	require.True(t, applied)
	warm, err = h.LoadTodos(t.Context(), "plan-owner")
	require.NoError(t, err)
	assert.Equal(t, items, warm)
}

func TestCapturedTodoSnapshotKeepsOriginalNativeAuthority(t *testing.T) {
	for _, knownChild := range []bool{false, true} {
		for _, lateFactory := range []bool{false, true} {
			for _, change := range []string{"ordinary turns", "same session update", "transcript lease renewal", "state lease renewal", "root fresh same", "root A B A", "child session", "child cleanup", "root physical"} {
				t.Run(fmt.Sprintf("knownChild=%v/lateFactory=%v/%s", knownChild, lateFactory, change), func(t *testing.T) {
					t.Parallel()
					h, _, root, _ := newCapturedPlanTest(t)
					id, err := root.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "todo-child", Title: "Child"})
					require.NoError(t, err)
					lease := root.AcquireChildStateSink(id)
					child := lease.Sink
					if knownChild {
						child.UpdateSessionID("child-native")
					}
					captured := agent.CaptureTranscript(child, agent.MessageContent{}, agent.SpanInfo{})
					var prepared agent.CapturedTodoSnapshot
					original := []todoevents.Item{{Content: "Original observation", Status: todoevents.StatusPending}}
					if !lateFactory {
						prepared, err = child.PrepareTodoSnapshot(captured, original)
						require.NoError(t, err)
					}
					currentRoot := root
					switch change {
					case "ordinary turns":
						root.SetTurnState(agent.TurnState{Active: true}, 1)
						root.SetTurnState(agent.TurnState{}, 2)
						child.SetTurnState(agent.TurnState{Active: true}, 1)
						child.SetTurnState(agent.TurnState{}, 2)
					case "same session update":
						root.UpdateSessionID("native")
					case "transcript lease renewal":
						root.AcquireChildSink(id)
					case "state lease renewal":
						root.AcquireChildStateSink(id)
					case "root fresh same":
						root.AdoptNativeSession("native")
					case "root A B A":
						root.UpdateSessionID("replacement")
						root.UpdateSessionID("native")
					case "child session":
						child.AdoptNativeSession("child-native")
					case "child cleanup":
						require.True(t, lease.Cleanup())
					case "root physical":
						currentRoot, err = h.NewRootSink("plan-owner", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
						require.NoError(t, err)
					}
					if lateFactory {
						prepared, err = child.PrepareTodoSnapshot(captured, original)
						require.NoError(t, err)
					}
					live := change == "ordinary turns" || change == "same session update" || change == "transcript lease renewal" || change == "state lease renewal"
					replacement := []todoevents.Item{{Content: "Replacement state", Status: todoevents.StatusCompleted}}
					if !live {
						currentChild := currentRoot.ChildSink(id)
						fresh, err := currentChild.PrepareTodoSnapshot(agent.CaptureTranscript(currentChild, agent.MessageContent{}, agent.SpanInfo{}), replacement)
						require.NoError(t, err)
						applied, err := fresh.Persist()
						require.NoError(t, err)
						require.True(t, applied)
					}
					applied, err := prepared.Persist()
					require.NoError(t, err)
					assert.Equal(t, live, applied)
					stored, err := h.LoadTodos(t.Context(), id)
					require.NoError(t, err)
					if live {
						assert.Equal(t, original, stored)
					} else {
						assert.Equal(t, replacement, stored)
					}
				})
			}
		}
	}
}

func TestCapturedTodoSnapshotConcurrentPersistenceKeepsCompleteLists(t *testing.T) {
	t.Parallel()
	h, queries, services, _ := newCapturedPlanTest(t)
	prepared := make([]agent.CapturedTodoSnapshot, 32)
	for i := range prepared {
		var err error
		prepared[i], err = services.PrepareTodoSnapshot(agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{}), []todoevents.Item{{Content: fmt.Sprintf("%d/first", i), Status: todoevents.StatusPending}, {Content: fmt.Sprintf("%d/second", i), Status: todoevents.StatusCompleted}})
		require.NoError(t, err)
	}
	var writers sync.WaitGroup
	errors := make(chan error, len(prepared))
	for _, snapshot := range prepared {
		writers.Go(func() {
			applied, err := snapshot.Persist()
			if err == nil && !applied {
				err = fmt.Errorf("a current native source was refused")
			}
			errors <- err
		})
	}
	writers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	warm, err := h.LoadTodos(t.Context(), "plan-owner")
	require.NoError(t, err)
	require.Len(t, warm, 2)
	assert.Equal(t, strings.TrimSuffix(warm[0].Content, "/first"), strings.TrimSuffix(warm[1].Content, "/second"))
	rows, err := queries.ListAgentTodosNewestFirst(t.Context(), db.ListAgentTodosNewestFirstParams{AgentID: "plan-owner", Limit: 100})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, warm[1].Content, rows[0].Content)
	assert.Equal(t, warm[0].Content, rows[1].Content)
}
