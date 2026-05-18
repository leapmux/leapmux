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

// TestOutputTodos_ListAgentTodosOrdersBySeqNumeric drives 12 sequential
// TaskCreate events and asserts that ListAgentTodos returns rows in
// numeric seq order (seq=2 before seq=10), not a lexicographic order
// where "10" would precede "2". This is the source-of-truth ordering
// the sidebar and TaskList cards consume — `cache.snapshot()` walks
// `cache.rows`, which is seeded from this query and appended-at-tail
// for incremental inserts.
func TestOutputTodos_ListAgentTodosOrdersBySeqNumeric(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	const total = 12
	for i := 1; i <= total; i++ {
		spanID := fmt.Sprintf("span-tc-%d", i)
		taskID := fmt.Sprintf("t%d", i)
		use := marshalJSON(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []any{
					map[string]any{
						"type": "tool_use", "name": "TaskCreate",
						"input": map[string]any{"subject": fmt.Sprintf("task %d", i)},
					},
				},
			},
		})
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, use, agent.SpanInfo{
			SpanID: spanID, SpanType: "TaskCreate",
		}))
		res := marshalJSON(t, map[string]any{
			"type":            "user",
			"message":         map[string]any{"content": []any{}},
			"tool_use_result": map[string]any{"task": map[string]any{"id": taskID, "subject": fmt.Sprintf("task %d", i)}},
		})
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, res, agent.SpanInfo{
			SpanID: spanID, SpanType: "TaskCreate",
		}))
	}

	rows := listRows()
	require.Len(t, rows, total)

	// Numeric seq order: 1,2,3,...,12. A lexicographic regression would
	// surface as 1,10,11,12,2,3,...,9 — the row at index 1 would be t10.
	taskIDs := make([]string, len(rows))
	seqs := make([]int64, len(rows))
	for i, r := range rows {
		taskIDs[i] = r.TaskID
		seqs[i] = r.Seq
	}
	expected := make([]string, total)
	for i := range expected {
		expected[i] = fmt.Sprintf("t%d", i+1)
	}
	assert.Equal(t, expected, taskIDs,
		"rows must come back in numeric seq order — seq=10 must NOT precede seq=2")

	// Belt-and-braces: assert the seq sequence is strictly ascending and
	// dense 1..total.
	for i, s := range seqs {
		assert.Equal(t, int64(i+1), s, "row %d expected seq=%d, got %d", i, i+1, s)
	}
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

func TestOutputTodos_TaskUpdateDeletedSoftDeletesRow(t *testing.T) {
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

	// Delete — the row stays as a "deleted" tombstone.
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
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "deleted", rows[0].Status, "delete should mark the row as deleted, not remove it")
	assert.Equal(t, "tmp", rows[0].Content, "content survives the soft-delete so the UI can still render the row")
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

// TestOutputTodos_TaskCreateAfterEvictionAcrossRestart guards the
// `nextSeq` re-seed against the post-eviction sparse-seq case. After
// eviction physically removes the oldest terminal row, the surviving
// rows hold a contiguous-from-2 seq range (2..N). A fresh
// OutputHandler reading those rows must derive nextSeq from the max
// existing seq (N+1), not `len(rows)+1` (which would collide with
// seq=N and violate `UNIQUE(agent_id, seq)`).
func TestOutputTodos_TaskCreateAfterEvictionAcrossRestart(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	listRows := func() []db.AgentTodo {
		t.Helper()
		rows, err := svc.Queries.ListAgentTodos(ctx, db.ListAgentTodosParams{AgentID: "agent-1", Limit: 1000})
		require.NoError(t, err)
		return rows
	}

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	// Seed MaxTodos rows via TaskList snapshot, oldest marked completed
	// so eviction has a target.
	tasks := make([]any, todoevents.MaxTodos)
	for i := range tasks {
		status := "in_progress"
		if i == 0 {
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

	// Trigger eviction by creating a fresh task while at cap. t1
	// (completed) is the oldest terminal row and gets removed.
	use := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TaskCreate",
					"input": map[string]any{"subject": "evict-trigger"},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, use, agent.SpanInfo{
		SpanID: "span-evict", SpanType: "TaskCreate",
	}))
	resBody := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "evict-trigger", "subject": "evict-trigger"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, resBody, agent.SpanInfo{
		SpanID: "span-evict", SpanType: "TaskCreate",
	}))

	// Confirm the post-eviction DB state: MaxTodos rows with sparse seqs.
	rowsBefore := listRows()
	require.Len(t, rowsBefore, todoevents.MaxTodos)
	seqsBefore := make(map[int64]struct{}, len(rowsBefore))
	var maxSeqBefore int64
	for _, r := range rowsBefore {
		seqsBefore[r.Seq] = struct{}{}
		if r.Seq > maxSeqBefore {
			maxSeqBefore = r.Seq
		}
	}
	// Sparse: max(seq) is strictly greater than the row count because
	// seq=1 was evicted.
	assert.Greater(t, maxSeqBefore, int64(len(rowsBefore)),
		"post-eviction seqs should be sparse — max > count")

	// Simulate a worker restart: build a fresh OutputHandler against
	// the same DB and re-bind the sink. The new handler's todo cache
	// starts empty and is re-seeded from the persisted rows on next
	// touch.
	svc.Output = NewOutputHandler(svc.DB, svc.Queries, svc.Watchers, svc.Agents, nil)
	sink2 := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// First TaskCreate after the restart. With the seed bug, nextSeq
	// would be `len(rows)+1` = an existing seq, colliding with the
	// UNIQUE(agent_id, seq) constraint and surfacing as a write error.
	// Mark an existing row deleted first to make room (cap is still full).
	delRes := marshalJSON(t, map[string]any{
		"type":    "user",
		"message": map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{
			"success": true, "taskId": "t2", "updatedFields": []any{"status"},
			"statusChange": map[string]any{"from": "in_progress", "to": "deleted"},
		},
	})
	require.NoError(t, sink2.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, delRes, agent.SpanInfo{
		SpanID: "span-del", SpanType: "TaskUpdate",
	}))
	createUse := marshalJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use", "name": "TaskCreate",
					"input": map[string]any{"subject": "post-restart"},
				},
			},
		},
	})
	require.NoError(t, sink2.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, createUse, agent.SpanInfo{
		SpanID: "span-post", SpanType: "TaskCreate",
	}))
	createRes := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "post-restart", "subject": "post-restart"}},
	})
	require.NoError(t, sink2.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, createRes, agent.SpanInfo{
		SpanID: "span-post", SpanType: "TaskCreate",
	}))

	// The new row must land with a strictly-greater seq than every
	// surviving seed row, so listing by seq yields a stable
	// "post-restart row is newest" order.
	rowsAfter := listRows()
	var postRow *db.AgentTodo
	for i := range rowsAfter {
		if rowsAfter[i].TaskID == "post-restart" {
			postRow = &rowsAfter[i]
			break
		}
	}
	require.NotNil(t, postRow, "post-restart row should be persisted")
	assert.Greater(t, postRow.Seq, maxSeqBefore,
		"new row's seq must exceed the pre-restart max to avoid UNIQUE collision")
}

// TestOutputTodos_TaskCreateAtCapNoTerminalDrops verifies that when
// the cap is reached and no completed/deleted rows exist to evict,
// the new task is dropped silently (with a warn log) and the list
// stays unchanged.
func TestOutputTodos_TaskCreateAtCapNoTerminalDrops(t *testing.T) {
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
		assert.NotEqual(t, "dropme", r.TaskID, "new task should have been dropped — no terminal row to evict")
	}
}

// TestOutputTodos_TaskUpdateDeletedIsIdempotent verifies that
// reissuing the delete sentinel on an already-deleted task is a
// no-op: no second broadcast, no DB churn, status stays "deleted".
func TestOutputTodos_TaskUpdateDeletedIsIdempotent(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	// Seed one task.
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
		"tool_use_result": map[string]any{"task": map[string]any{"id": "9", "subject": "tmp"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, result, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))

	// First delete.
	resultD := marshalJSON(t, map[string]any{
		"type":    "user",
		"message": map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{
			"success": true, "taskId": "9", "updatedFields": []any{"status"},
			"statusChange": map[string]any{"from": "completed", "to": "deleted"},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, resultD, agent.SpanInfo{
		SpanID: "span-d1", SpanType: "TaskUpdate",
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "deleted", rows[0].Status)
	updatedAfterFirst := rows[0].UpdatedAt

	// Second delete on the same task — should leave the row untouched
	// (idempotent guard returns the existing snapshot without a DB write).
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, resultD, agent.SpanInfo{
		SpanID: "span-d2", SpanType: "TaskUpdate",
	}))
	rows = listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "deleted", rows[0].Status)
	assert.Equal(t, updatedAfterFirst, rows[0].UpdatedAt,
		"second delete must not rewrite the DB row (updated_at would change)")
}

// TestOutputTodos_TaskCreateAtCapMixedTerminalEvictsOldest seeds the
// cap with a mix of completed and deleted rows scattered across the
// list and verifies the eviction pool treats them as a single oldest-
// first pool. Whichever terminal row has the lower seq is the one
// that gets evicted.
func TestOutputTodos_TaskCreateAtCapMixedTerminalEvictsOldest(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	// Seed MaxTodos rows. Layout:
	//   index 0  → completed (oldest terminal — should be evicted)
	//   index 1  → deleted   (younger terminal)
	//   index 2  → completed
	//   index 3+ → in_progress
	tasks := make([]any, todoevents.MaxTodos)
	for i := range tasks {
		var status string
		switch i {
		case 0, 2:
			status = "completed"
		case 1:
			status = "deleted"
		default:
			status = "in_progress"
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

	// Fire a TaskCreate at the cap. Oldest terminal is t1 (completed),
	// not t2 (deleted), because t1 has a lower seq.
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
	taskIDs := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		taskIDs[r.TaskID] = struct{}{}
	}
	assert.NotContains(t, taskIDs, "t1", "oldest terminal (completed t1) should have been evicted")
	assert.Contains(t, taskIDs, "t2", "younger deleted row (t2) should still be there")
	assert.Contains(t, taskIDs, "t3", "other completed row (t3) should still be there")
	assert.Contains(t, taskIDs, "new")
}

// TestOutputTodos_TaskCreateAtCapEvictsOldestDeleted verifies that
// the cap-eviction pool also includes deleted (tombstoned) rows. If
// the oldest terminal row is a deleted one, it's the row that's
// evicted to make room for the new task.
func TestOutputTodos_TaskCreateAtCapEvictsOldestDeleted(t *testing.T) {
	sink, _, listRows := setupTodoTest(t)
	// Seed MaxTodos in_progress rows except the first, which is
	// "deleted" (the oldest tombstone). The eviction predicate must
	// pick t1 even though no row carries status "completed".
	tasks := make([]any, todoevents.MaxTodos)
	for i := range tasks {
		status := "in_progress"
		if i == 0 {
			status = "deleted"
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
		assert.NotEqual(t, "t1", r.TaskID, "oldest deleted row should have been evicted")
	}
	taskIDs := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		taskIDs[r.TaskID] = struct{}{}
	}
	assert.Contains(t, taskIDs, "t2")
	assert.Contains(t, taskIDs, "new")
}
