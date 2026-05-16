package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// setupTodoTest provisions a worker service with one Claude-code agent and
// returns the sink, the agent_id, and a row-listing helper bound to that
// agent. Used by the to-do persistence/broadcast tests.
func setupTodoTest(t *testing.T) (agent.OutputSink, string, func() []db.AgentTodo) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	listRows := func() []db.AgentTodo {
		t.Helper()
		rows, err := svc.Queries.ListAgentTodos(ctx, db.ListAgentTodosParams{AgentID: "agent-1", Limit: 1000})
		require.NoError(t, err)
		return rows
	}
	return sink, "agent-1", listRows
}

func marshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestOutputTodos_TodoWriteSnapshotPersists(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	body := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TodoWrite",
					"input": map[string]any{
						"todos": []any{
							map[string]any{"content": "A", "status": "pending", "activeForm": "Doing A"},
							map[string]any{"content": "B", "status": "in_progress", "activeForm": "Doing B"},
						},
					},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, body, agent.SpanInfo{
		SpanID: "span-todowrite", SpanType: "TodoWrite",
	}))
	rows := listRows()
	require.Len(t, rows, 2)
	assert.Equal(t, "A", rows[0].Content)
	assert.Equal(t, "pending", rows[0].Status)
	assert.Equal(t, "Doing A", rows[0].ActiveForm)
	assert.Equal(t, "B", rows[1].Content)
	assert.Equal(t, "in_progress", rows[1].Status)
}

func TestOutputTodos_TaskCreateInsertsRowAfterResult(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	// 1. Persist the tool_use side first (so the result-side lookup
	//    finds it via GetAgentMessageBySpanIDAndSource).
	use := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TaskCreate",
					"input": map[string]any{
						"subject": "Add proto messages", "description": "Edit proto", "activeForm": "Adding proto",
					},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, use, agent.SpanInfo{
		SpanID: "span-tc1", SpanType: "TaskCreate",
	}))
	// Nothing yet — the tool_use has no id.
	assert.Empty(t, listRows())

	// 2. Persist the tool_result with the assigned id.
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "1", "subject": "Add proto messages"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, result, agent.SpanInfo{
		SpanID: "span-tc1", SpanType: "TaskCreate",
	}))

	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "1", rows[0].TaskID)
	assert.Equal(t, "Add proto messages", rows[0].Content)
	assert.Equal(t, "Adding proto", rows[0].ActiveForm)
	assert.Equal(t, "Edit proto", rows[0].Description)
	assert.Equal(t, "pending", rows[0].Status)
}

func TestOutputTodos_TaskUpdateStatusOnlyPreservesActiveForm(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	// Seed via TaskCreate (tool_use + tool_result).
	use := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TaskCreate",
					"input": map[string]any{"subject": "Run tests", "activeForm": "Running tests"},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, use, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "1", "subject": "Run tests"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, result, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))

	// TaskUpdate: only status changes; activeForm must survive.
	useU := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TaskUpdate",
					"input": map[string]any{"taskId": "1", "status": "in_progress"},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, useU, agent.SpanInfo{
		SpanID: "span-u", SpanType: "TaskUpdate",
	}))
	resultU := marshalJSON(t, map[string]any{
		"type":    "user",
		"message": map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{
			"success": true, "taskId": "1", "updatedFields": []any{"status"},
			"statusChange": map[string]any{"from": "pending", "to": "in_progress"},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, resultU, agent.SpanInfo{
		SpanID: "span-u", SpanType: "TaskUpdate",
	}))

	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "in_progress", rows[0].Status)
	assert.Equal(t, "Running tests", rows[0].ActiveForm, "activeForm must survive a status-only patch")
}

func TestOutputTodos_TaskUpdateDeletedRemovesRow(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	// Seed.
	use := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TaskCreate",
					"input": map[string]any{"subject": "tmp"},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, use, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "7", "subject": "tmp"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, result, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))
	require.Len(t, listRows(), 1)

	// Delete.
	resultD := marshalJSON(t, map[string]any{
		"type":    "user",
		"message": map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{
			"success": true, "taskId": "7", "updatedFields": []any{"status"},
			"statusChange": map[string]any{"from": "completed", "to": "deleted"},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, resultD, agent.SpanInfo{
		SpanID: "span-d", SpanType: "TaskUpdate",
	}))
	assert.Empty(t, listRows())
}

func TestOutputTodos_TodoWriteReplacesPriorTaskList(t *testing.T) {
	// Most-recent-wins: a snapshot must wipe rows accumulated by Task*.
	sink, _, listRows := setupTodoTest(t)
	use := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TaskCreate",
					"input": map[string]any{"subject": "keep me"},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, use, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "1", "subject": "keep me"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, result, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))
	require.Len(t, listRows(), 1)

	// Now a TodoWrite snapshot arrives.
	snap := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TodoWrite",
					"input": map[string]any{
						"todos": []any{
							map[string]any{"content": "fresh", "status": "pending", "activeForm": ""},
						},
					},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, snap, agent.SpanInfo{
		SpanID: "span-tw", SpanType: "TodoWrite",
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "fresh", rows[0].Content)
	assert.Empty(t, rows[0].TaskID, "snapshot rows have no task_id")
}

func TestOutputTodos_CodexPlanSnapshotPopulates(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	body := marshalJSON(t, map[string]any{
		"method": "turn/plan/updated",
		"params": map[string]any{
			"plan": []any{
				map[string]any{"step": "Investigate", "status": "in_progress"},
				map[string]any{"step": "Fix", "status": "pending"},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, body, agent.SpanInfo{}))
	rows := listRows()
	require.Len(t, rows, 2)
	assert.Equal(t, "Investigate", rows[0].Content)
	assert.Equal(t, "in_progress", rows[0].Status)
	assert.Equal(t, "Fix", rows[1].Content)
}

func TestOutputTodos_AcpPlanSnapshotPopulates(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	body := marshalJSON(t, map[string]any{
		"sessionUpdate": "plan",
		"entries": []any{
			map[string]any{"content": "one", "status": "pending"},
			map[string]any{"content": "two", "status": "completed"},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, body, agent.SpanInfo{}))
	rows := listRows()
	require.Len(t, rows, 2)
	assert.Equal(t, "one", rows[0].Content)
	assert.Equal(t, "two", rows[1].Content)
	assert.Equal(t, "completed", rows[1].Status)
}

// Note: AgentTodosChanged broadcast is a one-line call adjacent to the
// already-tested broadcast for AgentMessage; covering it would require a
// second-level test harness around channel.Sender. The above persistence
// tests prove the upstream extract/apply pipeline; the broadcast is a
// trivial mechanical fan-out via WatcherManager.BroadcastAgentEvent.

// TestOutputTodos_TaskCreateAtCapEvictsOldestCompleted seeds the
// agent's to-do list at the MaxTodos cap (first five rows completed,
// the rest in_progress), then fires a TaskCreate. The oldest
// completed row should be evicted and the new task inserted at the
// tail.
func TestOutputTodos_TaskCreateAtCapEvictsOldestCompleted(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	// Seed MaxTodos rows via TaskList snapshot. Mark the first five
	// completed, rest in_progress, so eviction has a target.
	const completedPrefix = 5
	tasks := make([]any, todoevents.MaxTodos)
	for i := range tasks {
		status := "in_progress"
		if i < completedPrefix {
			status = "completed"
		}
		tasks[i] = map[string]any{
			"id":      fmt.Sprintf("t%d", i+1),
			"subject": fmt.Sprintf("task %d", i+1),
			"status":  status,
		}
	}
	listBody := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"tasks": tasks},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, listBody, agent.SpanInfo{
		SpanID: "span-list", SpanType: "TaskList",
	}))
	require.Len(t, listRows(), todoevents.MaxTodos)

	// Fire a TaskCreate: cap is reached and t1 is the oldest completed
	// row, so it should be evicted before the new row is appended.
	use := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TaskCreate",
					"input": map[string]any{"subject": "fresh"},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, use, agent.SpanInfo{
		SpanID: "span-new", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "new", "subject": "fresh"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, result, agent.SpanInfo{
		SpanID: "span-new", SpanType: "TaskCreate",
	}))

	rows := listRows()
	require.Len(t, rows, todoevents.MaxTodos)
	for _, r := range rows {
		assert.NotEqual(t, "t1", r.TaskID, "oldest completed row should have been evicted")
	}
	// Other completed rows should remain (only one eviction per insert).
	taskIDs := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		taskIDs[r.TaskID] = struct{}{}
	}
	assert.Contains(t, taskIDs, "t2")
	assert.Contains(t, taskIDs, "new")
}

// TestOutputTodos_TaskCreateAtCapNoCompletedDrops verifies that when
// the cap is reached and no completed rows exist to evict, the new
// task is dropped silently (with a warn log) and the list stays
// unchanged.
func TestOutputTodos_TaskCreateAtCapNoCompletedDrops(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	// Seed MaxTodos in_progress rows — nothing for eviction to take.
	tasks := make([]any, todoevents.MaxTodos)
	for i := range tasks {
		tasks[i] = map[string]any{
			"id":      fmt.Sprintf("t%d", i+1),
			"subject": fmt.Sprintf("task %d", i+1),
			"status":  "in_progress",
		}
	}
	listBody := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"tasks": tasks},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, listBody, agent.SpanInfo{
		SpanID: "span-list", SpanType: "TaskList",
	}))
	require.Len(t, listRows(), todoevents.MaxTodos)

	use := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TaskCreate",
					"input": map[string]any{"subject": "dropped"},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, use, agent.SpanInfo{
		SpanID: "span-drop", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "dropme", "subject": "dropped"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, result, agent.SpanInfo{
		SpanID: "span-drop", SpanType: "TaskCreate",
	}))

	rows := listRows()
	require.Len(t, rows, todoevents.MaxTodos)
	for _, r := range rows {
		assert.NotEqual(t, "dropme", r.TaskID, "new task should have been dropped — no completed row to evict")
	}
}
