package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// setupTodoTest provisions a worker service with one Claude-code agent and
// returns the sink, the agent_id, and a row-listing helper bound to that
// agent. Used by the to-do persistence/broadcast tests.
func setupTodoTest(t *testing.T) (agent.ProviderServices, string, func() []db.AgentTodo) {
	return setupTodoTestForProvider(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
}

// setupTodoTestForProvider is setupTodoTest for an agent of a stated provider. The
// provider decides which extractor reads the message, so a test that feeds one
// provider's shape has to create an agent of THAT provider.
func setupTodoTestForProvider(t *testing.T, provider leapmuxv1.AgentProvider) (agent.ProviderServices, string, func() []db.AgentTodo) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: provider,
	}))
	sink := svc.Output.NewSink("agent-1", provider)
	// The seed query returns NEWEST first (it feeds a capped cache, which must
	// keep the newest rows). Reverse here so assertions read the persisted rows
	// in ascending seq order, which is the order the cache exposes them in.
	listRows := func() []db.AgentTodo {
		t.Helper()
		rows, err := svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{AgentID: "agent-1", Limit: 1000})
		require.NoError(t, err)
		slices.Reverse(rows)
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

func TestReduceTodoUpdateBuildsThePostUpdateMutation(t *testing.T) {
	t.Parallel()

	status := todoevents.StatusInProgress
	rows := []cachedTodo{{
		item:   todoevents.Item{ID: "1", Content: "Run tests", Status: todoevents.StatusPending},
		rowKey: "1",
	}}
	mutation, ok := reduceTodoUpdate(rows, todoevents.Event{
		Kind:  todoevents.KindUpdate,
		ID:    "1",
		Patch: todoevents.Patch{Status: &status},
	})

	require.True(t, ok)
	assert.Equal(t, 0, mutation.index)
	assert.Equal(t, todoevents.StatusInProgress, mutation.item.Status)
	assert.Equal(t, todoevents.StatusPending, rows[0].item.Status, "preparing the mutation must not update the canonical list")
}

func TestOutputTodos_TodoWriteSnapshotPersists(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: body}, agent.SpanInfo{
		SpanID: "span-todowrite", SpanType: "TodoWrite",
	}))
	rows := listRows()
	require.Len(t, rows, 2)
	assert.Equal(t, "A", rows[0].Content)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusPending), rows[0].Status)
	assert.Equal(t, "Doing A", rows[0].ActiveForm)
	assert.Equal(t, "B", rows[1].Content)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusInProgress), rows[1].Status)
}

func TestOutputTodos_OpenCodeFamilyNativeResults(t *testing.T) {
	t.Parallel()
	for _, provider := range []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO} {
		t.Run(provider.String(), func(t *testing.T) {
			t.Parallel()
			sink, _, listRows := setupTodoTestForProvider(t, provider)
			persist := func(status string, todos any) {
				t.Helper()
				body := marshalJSON(t, map[string]any{
					"sessionUpdate": "tool_call_update", "toolCallId": "todos", "status": status,
					"rawOutput": map[string]any{"metadata": map[string]any{"todos": todos}},
				})
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: body}, agent.SpanInfo{SpanID: "todos", SpanType: "other"}))
			}
			todos := []map[string]string{{"content": "Inspect sample", "status": "in_progress"}, {"content": "Cancelled task", "status": "cancelled"}}
			persist("pending", todos)
			assert.Empty(t, listRows())
			persist("completed", todos)
			rows := listRows()
			require.Len(t, rows, 2)
			assert.Equal(t, "Inspect sample", rows[0].Content)
			assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusInProgress), rows[0].Status)
			assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusDeleted), rows[1].Status)
			persist("failed", []any{})
			assert.Len(t, listRows(), 2)
			persist("completed", []any{})
			assert.Empty(t, listRows())
		})
	}
}

func TestOutputTodos_ZCodeStreamedInputReachesTheProjection(t *testing.T) {
	t.Parallel()
	sink, _, listRows := setupTodoTestForProvider(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	original := []byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"todo","toolName":"TodoWrite","inputOmitted":true,"inputRef":"model_stream"}}`)
	supplemental := []byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"todo","input":{"todos":[{"content":"Recovered task","status":"pending"}]}}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original, Supplemental: supplemental}, agent.SpanInfo{SpanID: "todo", SpanType: "TodoWrite"}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "Recovered task", rows[0].Content)
}

// ZCode spells its to-do tool exactly as Claude Code does and takes the same input,
// but it carries it in its OWN `tool.updated` envelope -- so the list only reaches
// the sidebar because the extraction is dispatched through the provider plugin.
func TestOutputTodos_ZCodeToolUpdatedSnapshotPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-z",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}))
	sink := svc.Output.NewSink("agent-z", leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)

	body := marshalJSON(t, map[string]any{
		"type": "tool.updated",
		"payload": map[string]any{
			"kind": "scheduled", "toolCallId": "call-1", "toolName": "TodoWrite",
			"input": map[string]any{
				"todos": []any{
					map[string]any{"content": "A", "status": "in_progress", "activeForm": "Doing A"},
					map[string]any{"content": "B", "status": "pending"},
				},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: body}, agent.SpanInfo{
		SpanID: "call-1", SpanType: "TodoWrite",
	}))

	rows, err := svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{AgentID: "agent-z", Limit: 1000})
	require.NoError(t, err)
	slices.Reverse(rows)
	require.Len(t, rows, 2)
	assert.Equal(t, "A", rows[0].Content)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusInProgress), rows[0].Status)
	assert.Equal(t, "Doing A", rows[0].ActiveForm)
	assert.Equal(t, "B", rows[1].Content)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusPending), rows[1].Status)

	// The result half repeats the tool name with no input. Reading it as an empty
	// snapshot would clear the list the opener just set.
	result := marshalJSON(t, map[string]any{
		"type": "tool.updated",
		"payload": map[string]any{
			"kind": "result", "toolCallId": "call-1", "toolName": "TodoWrite",
			"result": map[string]any{"success": true},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: result}, agent.SpanInfo{
		SpanID: "call-1", SpanType: "TodoWrite", Closing: true,
	}))
	rows, err = svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{AgentID: "agent-z", Limit: 1000})
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

// One provider's to-do shape must never populate another provider's list. The dispatch
// through the plugin is the whole mechanism, and this is what proves it: before it, a
// shared extractor tried every shape on every message, so the reader was decided by the
// message body rather than by the agent that produced it. ZCode makes that reachable --
// it gives its tool the same name Claude Code does, `TodoWrite` -- and this pins the
// ownership: each shape feeds its own provider's list and no other.
func TestOutputTodos_OneProvidersShapeNeverFeedsAnother(t *testing.T) {
	t.Parallel()

	shapes := map[string]struct {
		owner    leapmuxv1.AgentProvider
		spanType string
		body     string
	}{
		"claude TodoWrite": {leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "TodoWrite",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TodoWrite",
			  "input":{"todos":[{"content":"A","status":"pending"}]}}]}}`},
		"codex plan": {leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "",
			`{"method":"turn/plan/updated","params":{"plan":[{"step":"A","status":"pending"}]}}`},
		"acp plan": {leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "",
			`{"sessionUpdate":"plan","entries":[{"content":"A","status":"pending"}]}`},
		"zcode tool.updated": {leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, "TodoWrite",
			`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"c1","toolName":"TodoWrite",
			  "input":{"todos":[{"content":"A","status":"pending"}]}}}`},
	}
	readers := []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
	}

	for name, shape := range shapes {
		for _, reader := range readers {
			t.Run(name+" read by "+reader.String(), func(t *testing.T) {
				t.Parallel()
				sink, _, listRows := setupTodoTestForProvider(t, reader)
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					agent.MessageContent{Original: []byte(shape.body)}, agent.SpanInfo{SpanID: "span-1", SpanType: shape.spanType}))

				rows := listRows()
				if reader == shape.owner {
					require.Len(t, rows, 1, "the owning provider reads its own shape")
					assert.Equal(t, "A", rows[0].Content)
					return
				}
				assert.Empty(t, rows, "no other provider may read it")
			})
		}
	}
}

func TestOutputTodos_TaskCreateInsertsRowAfterResult(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: result}, agent.SpanInfo{
		SpanID: "span-tc1", SpanType: "TaskCreate",
	}))

	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "1", rows[0].TaskID)
	assert.Equal(t, "Add proto messages", rows[0].Content)
	assert.Equal(t, "Adding proto", rows[0].ActiveForm)
	assert.Equal(t, "Edit proto", rows[0].Description)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusPending), rows[0].Status)
}

// TestOutputTodos_ListAgentTodosNewestFirstOrdersBySeqNumeric drives 12 sequential
// TaskCreate events and asserts that ListAgentTodosNewestFirst returns rows in
// numeric seq order (seq=2 before seq=10), not a lexicographic order
// where "10" would precede "2". This is the source-of-truth ordering
// the sidebar and TaskList cards consume — `cache.snapshot()` walks
// `cache.rows`, which is seeded from this query and appended-at-tail
// for incremental inserts.
func TestOutputTodos_ListAgentTodosNewestFirstOrdersBySeqNumeric(t *testing.T) {
	t.Parallel()

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
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
			SpanID: spanID, SpanType: "TaskCreate",
		}))
		res := marshalJSON(t, map[string]any{
			"type":            "user",
			"message":         map[string]any{"content": []any{}},
			"tool_use_result": map[string]any{"task": map[string]any{"id": taskID, "subject": fmt.Sprintf("task %d", i)}},
		})
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: res}, agent.SpanInfo{
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
	t.Parallel()

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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "1", "subject": "Run tests"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: result}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: useU}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: resultU}, agent.SpanInfo{
		SpanID: "span-u", SpanType: "TaskUpdate",
	}))

	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusInProgress), rows[0].Status)
	assert.Equal(t, "Running tests", rows[0].ActiveForm, "activeForm must survive a status-only patch")
}

func TestOutputTodos_TaskUpdatePersistsItsPostUpdateSnapshotAtomically(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-snapshot", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-snapshot", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	createUse := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskCreate","input":{"subject":"Run tests","activeForm":"Running tests","description":"Full suite"}}]}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: createUse}, agent.SpanInfo{SpanID: "create", SpanType: "TaskCreate"}))
	createResult := []byte(`{"type":"user","message":{"content":[]},"tool_use_result":{"task":{"id":"1","subject":"Run tests"}}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: createResult}, agent.SpanInfo{SpanID: "create", SpanType: "TaskCreate"}))

	updateUse := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskUpdate","input":{"taskId":"1","status":"in_progress"}}]}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: updateUse}, agent.SpanInfo{SpanID: "update", SpanType: "TaskUpdate"}))
	updateResult := []byte(`{"type":"user","message":{"content":[]},"tool_use_result":{"success":true,"taskId":"1","updatedFields":["status"],"statusChange":{"from":"pending","to":"in_progress"}}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: updateResult}, agent.SpanInfo{SpanID: "update", SpanType: "TaskUpdate"}))

	messages, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-snapshot", Seq: 0, Limit: 100})
	require.NoError(t, err)
	var update db.Message
	for _, message := range messages {
		if message.SpanID == "update" && message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_USER {
			update = message
			break
		}
	}
	require.NotEmpty(t, update.ID)
	supplement, err := msgcodec.Decompress(update.SupplementalContent, update.SupplementalContentCompression)
	require.NoError(t, err)
	decoded, err := agent.DecodeMessageSupplement(updateResult, supplement)
	require.NoError(t, err)
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(decoded.Metadata, &metadata))
	require.Contains(t, metadata, "todo_snapshot")
	assert.JSONEq(t, `{"id":"1","content":"Run tests","status":"TODO_STATUS_IN_PROGRESS","activeForm":"Running tests","description":"Full suite"}`, string(metadata["todo_snapshot"]))
}

func TestOutputTodos_TaskUpdateMessageFailureDoesNotMutateTodo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-message-failure", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-message-failure", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	createUse := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskCreate","input":{"subject":"Keep pending"}}]}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: createUse}, agent.SpanInfo{SpanID: "create", SpanType: "TaskCreate"}))
	createResult := []byte(`{"type":"user","message":{"content":[]},"tool_use_result":{"task":{"id":"1","subject":"Keep pending"}}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: createResult}, agent.SpanInfo{SpanID: "create", SpanType: "TaskCreate"}))
	updateUse := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskUpdate","input":{"taskId":"1","status":"in_progress"}}]}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: updateUse}, agent.SpanInfo{SpanID: "update", SpanType: "TaskUpdate"}))
	_, err := svc.DB.ExecContext(ctx, `CREATE TRIGGER fail_task_update_message BEFORE INSERT ON messages WHEN NEW.span_id = 'update' BEGIN SELECT RAISE(ABORT, 'message unavailable'); END`)
	require.NoError(t, err)

	updateResult := []byte(`{"type":"user","message":{"content":[]},"tool_use_result":{"success":true,"taskId":"1","statusChange":{"from":"pending","to":"in_progress"}}}`)
	err = sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: updateResult}, agent.SpanInfo{SpanID: "update", SpanType: "TaskUpdate"})
	require.ErrorContains(t, err, "message unavailable")
	rows, err := svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{AgentID: "agent-message-failure", Limit: 100})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusPending), rows[0].Status)
}

func TestOutputTodos_TaskUpdateRegistryFailureKeepsSnapshottedMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-registry-failure", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-registry-failure", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	createUse := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskCreate","input":{"subject":"Saved transcript"}}]}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: createUse}, agent.SpanInfo{SpanID: "create", SpanType: "TaskCreate"}))
	createResult := []byte(`{"type":"user","message":{"content":[]},"tool_use_result":{"task":{"id":"1","subject":"Saved transcript"}}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: createResult}, agent.SpanInfo{SpanID: "create", SpanType: "TaskCreate"}))
	updateUse := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskUpdate","input":{"taskId":"1","status":"in_progress"}}]}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: updateUse}, agent.SpanInfo{SpanID: "update", SpanType: "TaskUpdate"}))
	_, err := svc.DB.ExecContext(ctx, `CREATE TRIGGER fail_task_update_registry BEFORE UPDATE ON agent_todos BEGIN SELECT RAISE(ABORT, 'registry unavailable'); END`)
	require.NoError(t, err)

	updateResult := []byte(`{"type":"user","message":{"content":[]},"tool_use_result":{"success":true,"taskId":"1","statusChange":{"from":"pending","to":"in_progress"}}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: updateResult}, agent.SpanInfo{SpanID: "update", SpanType: "TaskUpdate"}))

	todos, err := svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{AgentID: "agent-registry-failure", Limit: 100})
	require.NoError(t, err)
	require.Len(t, todos, 1)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusPending), todos[0].Status)
	messages, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-registry-failure", Seq: 0, Limit: 100})
	require.NoError(t, err)
	var persisted db.Message
	for _, message := range messages {
		if message.SpanID == "update" && message.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_USER {
			persisted = message
			break
		}
	}
	require.NotEmpty(t, persisted.ID)
	supplement, err := msgcodec.Decompress(persisted.SupplementalContent, persisted.SupplementalContentCompression)
	require.NoError(t, err)
	decoded, err := agent.DecodeMessageSupplement(updateResult, supplement)
	require.NoError(t, err)
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(decoded.Metadata, &metadata))
	assert.JSONEq(t, `{"id":"1","content":"Saved transcript","status":"TODO_STATUS_IN_PROGRESS"}`, string(metadata["todo_snapshot"]))
}

func TestOutputTodos_TaskUpdateDeletedSoftDeletesRow(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "7", "subject": "tmp"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: result}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: resultD}, agent.SpanInfo{
		SpanID: "span-d", SpanType: "TaskUpdate",
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusDeleted), rows[0].Status, "delete should mark the row as deleted, not remove it")
	assert.Equal(t, "tmp", rows[0].Content, "content survives the soft-delete so the UI can still render the row")
}

func TestOutputTodos_TodoWriteReplacesPriorTaskList(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "1", "subject": "keep me"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: result}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: snap}, agent.SpanInfo{
		SpanID: "span-tw", SpanType: "TodoWrite",
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "fresh", rows[0].Content)
	assert.Empty(t, rows[0].TaskID, "snapshot rows have no task_id")
}

func TestOutputTodos_CodexPlanSnapshotPopulates(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupTodoTestForProvider(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	body := marshalJSON(t, map[string]any{
		"method": "turn/plan/updated",
		"params": map[string]any{
			"plan": []any{
				map[string]any{"step": "Investigate", "status": "in_progress"},
				map[string]any{"step": "Fix", "status": "pending"},
			},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: body}, agent.SpanInfo{}))
	rows := listRows()
	require.Len(t, rows, 2)
	assert.Equal(t, "Investigate", rows[0].Content)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusInProgress), rows[0].Status)
	assert.Equal(t, "Fix", rows[1].Content)
}

func TestOutputTodos_AcpPlanSnapshotPopulates(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupTodoTestForProvider(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE)
	body := marshalJSON(t, map[string]any{
		"sessionUpdate": "plan",
		"entries": []any{
			map[string]any{"content": "one", "status": "pending"},
			map[string]any{"content": "two", "status": "completed"},
		},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: body}, agent.SpanInfo{}))
	rows := listRows()
	require.Len(t, rows, 2)
	assert.Equal(t, "one", rows[0].Content)
	assert.Equal(t, "two", rows[1].Content)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusCompleted), rows[1].Status)
}

// cursorTodoRow persists the row that CLOSES one Cursor tool call and returns the
// bytes the enrichment must state back as the original content.
func cursorTodoRow(t *testing.T, sink agent.ProviderServices, toolCallID string) []byte {
	t.Helper()
	original := marshalJSON(t, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": toolCallID, "status": "completed",
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: toolCallID, Closing: true}))
	return original
}

// cursorTodoSupplement is what the Cursor worker stores for one cursor/update_todos
// frame: the method beside its params under the contract's own key, and the identity
// of the row it belongs to. A supplement that identifies no row reaches none, on either
// side of the language boundary.
func cursorTodoSupplement(t *testing.T, toolCallID, params string) []byte {
	t.Helper()
	return marshalJSON(t, map[string]any{
		contracts.ACPSupplementIdentitySessionUpdate: "tool_call_update",
		contracts.ACPSupplementIdentityToolCallID:    toolCallID,
		contracts.ACPSupplementIdentityStatus:        "completed",
		contracts.CursorSupplementExtension: map[string]any{
			"method": contracts.CursorMethodUpdateTodos,
			"params": json.RawMessage(params),
		},
	})
}

// Cursor's to-do list reaches the row as a SUPPLEMENT, one frame after the tool call
// it describes, because the `merge` flag travels on that frame alone. Nothing else
// applies a to-do event at an enrichment, so without this path the list stays empty.
func TestOutputTodos_CursorExtensionFrameFeedsTheStore(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupTodoTestForProvider(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	original := cursorTodoRow(t, sink, "call-1")
	replace := `{"toolCallId":"call-1","todos":[` +
		`{"id":"1","content":"one","status":"in_progress"},` +
		`{"id":"2","content":"two","status":"pending"}],"merge":false}`
	written, err := sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "call-1", OriginalContent: original, SupplementalContent: cursorTodoSupplement(t, "call-1", replace),
	})
	require.NoError(t, err)
	require.True(t, written)

	rows := listRows()
	require.Len(t, rows, 2)
	assert.Equal(t, "one", rows[0].Content)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusInProgress), rows[0].Status)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusPending), rows[1].Status)
}

// A merge frame states the rows that CHANGED. Reading it as a snapshot would delete
// every row it stayed silent about, which is every row after the first update.
func TestOutputTodos_CursorMergeFrameLeavesTheRowsItOmits(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupTodoTestForProvider(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	first := cursorTodoRow(t, sink, "call-1")
	replace := `{"toolCallId":"call-1","todos":[` +
		`{"id":"1","content":"one","status":"in_progress"},` +
		`{"id":"2","content":"two","status":"pending"},` +
		`{"id":"3","content":"three","status":"pending"}],"merge":false}`
	written, err := sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "call-1", OriginalContent: first, SupplementalContent: cursorTodoSupplement(t, "call-1", replace),
	})
	require.NoError(t, err)
	require.True(t, written)

	second := cursorTodoRow(t, sink, "call-2")
	merge := `{"toolCallId":"call-2","todos":[` +
		`{"id":"1","content":"one","status":"completed"},` +
		`{"id":"2","content":"two","status":"in_progress"}],"merge":true}`
	written, err = sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "call-2", OriginalContent: second, SupplementalContent: cursorTodoSupplement(t, "call-2", merge),
	})
	require.NoError(t, err)
	require.True(t, written)

	rows := listRows()
	require.Len(t, rows, 3, "the row the merge frame omitted stays")
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusCompleted), rows[0].Status)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusInProgress), rows[1].Status)
	assert.Equal(t, "three", rows[2].Content)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusPending), rows[2].Status)
}

// The SECOND enrichment of one row must not replay the frame the FIRST one carried.
//
// A supplement survives the next enrichment -- tooltranscript.MergeSupplements replaces only the
// keys that collide -- so the cursor/update_todos frame EnrichToolSpan wrote is still
// on the row when the turn-end store pass enriches it again with the tool record.
// Guarding on the ORIGINAL content alone found nothing both times, so the second pass
// re-applied the first frame's merge:false snapshot: every row the later merge frames
// added was deleted and every status they changed was reverted.
func TestOutputTodos_EnrichmentNeverReplaysTheFrameTheSupplementAlreadyCarried(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupTodoTestForProvider(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	first := cursorTodoRow(t, sink, "call-1")
	snapshot := `{"toolCallId":"call-1","todos":[` +
		`{"id":"1","content":"one","status":"in_progress"},` +
		`{"id":"2","content":"two","status":"pending"}],"merge":false}`
	written, err := sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "call-1", OriginalContent: first, SupplementalContent: cursorTodoSupplement(t, "call-1", snapshot),
	})
	require.NoError(t, err)
	require.True(t, written)

	second := cursorTodoRow(t, sink, "call-2")
	merge := `{"toolCallId":"call-2","todos":[` +
		`{"id":"3","content":"three","status":"pending"}],"merge":true}`
	written, err = sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "call-2", OriginalContent: second, SupplementalContent: cursorTodoSupplement(t, "call-2", merge),
	})
	require.NoError(t, err)
	require.True(t, written)
	require.Len(t, listRows(), 3, "the merge frame added its row")

	// The store pass enriches call-1 a SECOND time. Its supplement still carries the
	// snapshot -- tooltranscript.MergeSupplements keeps a key nothing collides with -- and the
	// tool record is the extra key beside it.
	combined := marshalJSON(t, map[string]any{
		contracts.CursorSupplementExtension: map[string]any{
			"method": contracts.CursorMethodUpdateTodos,
			"params": json.RawMessage(snapshot),
		},
		"rawOutput": map[string]any{"content": []any{}},
	})
	_, err = sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "call-1", OriginalContent: first, SupplementalContent: combined, PreviousRevision: 1,
	})
	require.NoError(t, err)

	rows := listRows()
	require.Len(t, rows, 3, "the re-enrichment must not delete the row the merge frame added")
	assert.Equal(t, "three", rows[2].Content)
}

// An enrichment may only introduce an event the original content does not yield. A
// row whose own bytes already carried the list was applied when it was persisted, and
// restating it later would overwrite a newer list with an older one -- the store pass
// enriches a row long after the rows that follow it land.
func TestOutputTodos_EnrichmentNeverReplaysTheListTheRowAlreadyCarried(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupTodoTestForProvider(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE)
	stale := marshalJSON(t, map[string]any{
		"sessionUpdate": "plan",
		"entries":       []any{map[string]any{"content": "stale", "status": "pending"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: stale}, agent.SpanInfo{SpanID: "call-1", Closing: true}))
	fresh := marshalJSON(t, map[string]any{
		"sessionUpdate": "plan",
		"entries":       []any{map[string]any{"content": "fresh", "status": "completed"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: fresh}, agent.SpanInfo{SpanID: "call-2", Closing: true}))
	require.Equal(t, "fresh", listRows()[0].Content)

	written, err := sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "call-1", OriginalContent: stale, SupplementalContent: []byte(`{"rawOutput":{"content":[]}}`),
	})
	require.NoError(t, err)
	require.True(t, written)

	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "fresh", rows[0].Content, "the enriched older row must not restore its own list")
}

// Note: AgentTodosChanged broadcast is a one-line call adjacent to the
// already-tested broadcast for AgentMessage; covering it would require a
// second-level test harness around channel.ResponseWriter. The above persistence
// tests prove the upstream extract/apply pipeline; the broadcast is a
// trivial mechanical fan-out via WatcherManager.BroadcastAgentEvent.

// TestOutputTodos_TaskCreateAtCapEvictsOldestCompleted seeds the
// agent's to-do list at the MaxTodos cap (first five rows completed,
// the rest in_progress), then fires a TaskCreate. The oldest
// completed row should be evicted and the new task inserted at the
// tail.
func TestOutputTodos_TaskCreateAtCapEvictsOldestCompleted(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: listBody}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
		SpanID: "span-new", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "new", "subject": "fresh"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: result}, agent.SpanInfo{
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
// eviction physically removes the oldest finished row, the surviving
// rows hold a contiguous-from-2 seq range (2..N). A fresh
// OutputHandler reading those rows must derive nextSeq from the max
// existing seq (N+1), not `len(rows)+1` (which would collide with
// seq=N and violate `UNIQUE(agent_id, seq)`).
func TestOutputTodos_TaskCreateAfterEvictionAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	// The seed query returns NEWEST first (it feeds a capped cache, which must
	// keep the newest rows). Reverse here so assertions read the persisted rows
	// in ascending seq order, which is the order the cache exposes them in.
	listRows := func() []db.AgentTodo {
		t.Helper()
		rows, err := svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{AgentID: "agent-1", Limit: 1000})
		require.NoError(t, err)
		slices.Reverse(rows)
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: listBody}, agent.SpanInfo{
		SpanID: "span-list", SpanType: "TaskList",
	}))
	require.Len(t, listRows(), todoevents.MaxTodos)

	// Trigger eviction by creating a fresh task while at cap. t1
	// (completed) is the oldest finished row and gets removed.
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
		SpanID: "span-evict", SpanType: "TaskCreate",
	}))
	resBody := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "evict-trigger", "subject": "evict-trigger"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: resBody}, agent.SpanInfo{
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
	require.NoError(t, sink2.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: delRes}, agent.SpanInfo{
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
	require.NoError(t, sink2.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: createUse}, agent.SpanInfo{
		SpanID: "span-post", SpanType: "TaskCreate",
	}))
	createRes := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "post-restart", "subject": "post-restart"}},
	})
	require.NoError(t, sink2.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: createRes}, agent.SpanInfo{
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

// TestOutputTodos_TaskCreateAtCapNoFinishedRowDrops verifies that when
// the cap is reached and no completed/deleted rows exist to evict,
// the new task is dropped silently (with a warn log) and the list
// stays unchanged.
func TestOutputTodos_TaskCreateAtCapNoFinishedRowDrops(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: listBody}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
		SpanID: "span-drop", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "dropme", "subject": "dropped"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: result}, agent.SpanInfo{
		SpanID: "span-drop", SpanType: "TaskCreate",
	}))

	rows := listRows()
	require.Len(t, rows, todoevents.MaxTodos)
	for _, r := range rows {
		assert.NotEqual(t, "dropme", r.TaskID, "new task should have been dropped — no finished row to evict")
	}
}

// TestOutputTodos_TaskUpdateDeletedIsIdempotent verifies that
// reissuing the delete sentinel on an already-deleted task is a
// no-op: no second broadcast, no DB churn, status stays "deleted".
func TestOutputTodos_TaskUpdateDeletedIsIdempotent(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
		SpanID: "span-c", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "9", "subject": "tmp"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: result}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: resultD}, agent.SpanInfo{
		SpanID: "span-d1", SpanType: "TaskUpdate",
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusDeleted), rows[0].Status)
	updatedAfterFirst := rows[0].UpdatedAt

	// Second delete on the same task — should leave the row untouched
	// (idempotent guard returns the existing snapshot without a DB write).
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: resultD}, agent.SpanInfo{
		SpanID: "span-d2", SpanType: "TaskUpdate",
	}))
	rows = listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.TodoStatus(todoevents.StatusDeleted), rows[0].Status)
	assert.Equal(t, updatedAfterFirst, rows[0].UpdatedAt,
		"second delete must not rewrite the DB row (updated_at would change)")
}

// TestOutputTodos_TaskCreateAtCapMixedFinishedEvictsOldest seeds the
// cap with a mix of completed and deleted rows scattered across the
// list and verifies the eviction pool treats them as a single oldest-
// first pool. Whichever terminal row has the lower seq is the one
// that gets evicted.
func TestOutputTodos_TaskCreateAtCapMixedFinishedEvictsOldest(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupTodoTest(t)
	// Seed MaxTodos rows. Layout:
	//   index 0  → completed (oldest finished — should be evicted)
	//   index 1  → deleted   (younger finished)
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: listBody}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
		SpanID: "span-new", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "new", "subject": "fresh"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: result}, agent.SpanInfo{
		SpanID: "span-new", SpanType: "TaskCreate",
	}))

	rows := listRows()
	require.Len(t, rows, todoevents.MaxTodos)
	taskIDs := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		taskIDs[r.TaskID] = struct{}{}
	}
	assert.NotContains(t, taskIDs, "t1", "oldest finished (completed t1) should have been evicted")
	assert.Contains(t, taskIDs, "t2", "younger deleted row (t2) should still be there")
	assert.Contains(t, taskIDs, "t3", "other completed row (t3) should still be there")
	assert.Contains(t, taskIDs, "new")
}

// TestOutputTodos_TaskCreateAtCapEvictsOldestDeleted verifies that
// the cap-eviction pool also includes deleted (tombstoned) rows. If
// the oldest finished row is a deleted one, it's the row that's
// evicted to make room for the new task.
func TestOutputTodos_TaskCreateAtCapEvictsOldestDeleted(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: listBody}, agent.SpanInfo{
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: use}, agent.SpanInfo{
		SpanID: "span-new", SpanType: "TaskCreate",
	}))
	result := marshalJSON(t, map[string]any{
		"type":            "user",
		"message":         map[string]any{"content": []any{}},
		"tool_use_result": map[string]any{"task": map[string]any{"id": "new", "subject": "fresh"}},
	})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: result}, agent.SpanInfo{
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

// --- the paired tool_use reader ---
//
// It is the one piece of SHARED code the to-do path still owns, because resolving
// the row is a database read that only the worker can do. Every branch of it
// answers nil, so a provider's extractor builds the less detailed row rather than
// none at all.

func TestPairedToolUseLookup_ReadsTheToolUseHalfOfTheSpan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	body := marshalJSON(t, map[string]any{"type": "assistant", "note": "the use half"})
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: body}, agent.SpanInfo{
		SpanID: "span-1", SpanType: "TaskCreate",
	}))

	read := svc.Output.pairedToolUseLookup("agent-1", agent.SpanInfo{SpanID: "span-1"})
	assert.JSONEq(t, string(body), string(read()))
	assert.JSONEq(t, string(body), string(read()), "a second read answers from the memo")
}

func TestPairedToolUseLookup_AnswersNilWhenThereIsNothingToRead(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)

	assert.Nil(t, svc.Output.pairedToolUseLookup("agent-1", agent.SpanInfo{})(),
		"a message with no span has no tool_use half")
	assert.Nil(t, svc.Output.pairedToolUseLookup("agent-1", agent.SpanInfo{SpanID: "never-persisted"})(),
		"a rolled-up tool_use, or one this result raced, is absent rather than an error")
}

// The miss is memoized too: a row that appears between two reads must not change
// the answer inside one message's extraction, which would let two parsers of the
// same message disagree about what the row said.
func TestPairedToolUseLookup_MemoizesTheMiss(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	read := svc.Output.pairedToolUseLookup("agent-1", agent.SpanInfo{SpanID: "span-1"})
	require.Nil(t, read())

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: marshalJSON(t, map[string]any{"type": "assistant"})}, agent.SpanInfo{SpanID: "span-1"}))
	assert.Nil(t, read(), "the answer is fixed for the message being extracted")
}

// A merge frame states the new truth for the rows it lists, and it lands whole or not
// at all. One statement per row was one COMMIT per row, so a frame that failed partway
// left the earlier rows in the table and the rest of the frame nowhere -- a list the
// agent never stated. The out-of-range status is how this test makes the write fail:
// agent_todos.status carries a CHECK of 1..4.
func TestOutputTodos_AMergeFrameLandsWholeOrNotAtAll(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	listRows := func() []db.AgentTodo {
		t.Helper()
		rows, err := svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{AgentID: "agent-1", Limit: 1000})
		require.NoError(t, err)
		return rows
	}

	_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{
		Kind:  todoevents.KindMerge,
		Items: []todoevents.Item{{ID: "t1", Content: "one", Status: todoevents.StatusPending}},
	})
	require.NoError(t, err)
	require.Len(t, listRows(), 1)

	_, _, err = svc.Output.applyTodoEvent("agent-1", todoevents.Event{
		Kind: todoevents.KindMerge,
		Items: []todoevents.Item{
			{ID: "t2", Content: "two", Status: todoevents.StatusPending},
			{ID: "t3", Content: "three", Status: todoevents.Status(9)},
		},
	})
	require.Error(t, err, "an out-of-range status must fail the column CHECK")

	rows := listRows()
	require.Len(t, rows, 1, "the frame rolled back, so neither of its rows landed")
	assert.Equal(t, "t1", rows[0].TaskID)

	// The cache mirrors the table, and it moved as the transaction did. A rollback
	// that left `t2` in memory would state a list nothing can rebuild.
	cache := svc.Output.todoCache("agent-1")
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	require.Len(t, cache.Rows, 1, "the mirror rolled back with the transaction")
	assert.Equal(t, "t1", cache.Rows[0].item.ID)
}

// The eviction the cap forces is part of the frame's transaction, which is the whole
// reason registryOps takes its query handle as a parameter. While the ops closed over
// the handler's own handle, a transaction opened here enclosed the upserts and not the
// DELETE -- so a frame that rolled back still destroyed the row it evicted.
func TestOutputTodos_AFailedMergeKeepsTheRowItsCapEvicted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	taskIDs := func() map[string]struct{} {
		t.Helper()
		rows, err := svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{AgentID: "agent-1", Limit: 1000})
		require.NoError(t, err)
		ids := make(map[string]struct{}, len(rows))
		for _, r := range rows {
			ids[r.TaskID] = struct{}{}
		}
		return ids
	}

	// A full pool of FINISHED rows, so the next insert must evict one.
	snapshot := make([]todoevents.Item, todoevents.MaxTodos)
	for i := range snapshot {
		snapshot[i] = todoevents.Item{ID: fmt.Sprintf("t%d", i+1), Content: fmt.Sprintf("task %d", i+1), Status: todoevents.StatusCompleted}
	}
	_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: snapshot})
	require.NoError(t, err)
	require.Len(t, taskIDs(), todoevents.MaxTodos)

	// The first row forces the eviction of `t1`; the second fails the CHECK.
	_, _, err = svc.Output.applyTodoEvent("agent-1", todoevents.Event{
		Kind: todoevents.KindMerge,
		Items: []todoevents.Item{
			{ID: "fresh", Content: "fresh", Status: todoevents.StatusPending},
			{ID: "bad", Content: "bad", Status: todoevents.Status(9)},
		},
	})
	require.Error(t, err)

	ids := taskIDs()
	assert.Contains(t, ids, "t1", "the eviction rolled back with the frame that caused it")
	assert.NotContains(t, ids, "fresh", "the row the eviction made room for rolled back too")
	assert.Len(t, ids, todoevents.MaxTodos)

	// And the mirror agrees with the table, including the row it had evicted.
	cache := svc.Output.todoCache("agent-1")
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	require.Len(t, cache.Rows, todoevents.MaxTodos)
	assert.Equal(t, "t1", cache.Rows[0].item.ID, "the evicted row is back at the head of the mirror")
}

// inTransactionLocked owns BOTH halves of a grouped write: the transaction and
// the mirror that a rollback restores. The pairing used to be the caller's, and
// only a comment said which callers owed the mirror -- so a caller that took the
// transaction and forgot the mirror left a rolled-back write in memory, which is
// a list nothing can rebuild until the next cold seed.
//
// This drives the helper itself rather than one caller that happens to use it,
// so the guarantee is pinned where it now lives. The body mutates the cache and
// then fails, which is the shape every grouped write has: each write reads what
// the one before it left.
func TestRegistryCache_AFailedTransactionBodyRestoresTheCacheAndWritesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{
		Kind: todoevents.KindCreate,
		Item: todoevents.Item{ID: "t1", Content: "one", Status: todoevents.StatusPending},
	})
	require.NoError(t, err)

	cache := svc.Output.todoCache("agent-1")
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	require.NotNil(t, svc.Output.db, "the transaction branch is the one under test")
	reg := cache.on(svc.Output.queries, "agent-1")
	before := slices.Clone(cache.Rows)
	beforeSeq := cache.nextSeq

	wantErr := errors.New("the body refused")
	err = reg.inTransactionLocked(ctx, svc.Output.db, func(tx agentTodoView) error {
		_, upsertErr := svc.Output.upsertTodoLocked(ctx, tx, todoevents.KindCreate,
			todoevents.Item{ID: "t2", Content: "two", Status: todoevents.StatusPending})
		require.NoError(t, upsertErr)
		require.Len(t, cache.Rows, 2, "the body moved the mirror before it failed")
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	assert.Equal(t, before, cache.Rows, "the mirror is back at its pre-transaction state")
	assert.Equal(t, beforeSeq, cache.nextSeq, "and so is the seq the next insert takes")

	rows, err := svc.Queries.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{AgentID: "agent-1", Limit: 1000})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the transaction rolled back, so the body's write never landed")
	assert.Equal(t, "t1", rows[0].TaskID)
}
