package qoder

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const qoderChildStarted = `{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"local_agent","description":"Ask for one word","prompt":"Reply with PONG."}`
const qoderChildPrompt = `{"type":"user","parent_tool_use_id":"spawn-1","message":{"role":"user","content":[{"type":"text","text":"Reply with PONG."}]}}`
const qoderChildAnswer = `{"type":"assistant","parent_tool_use_id":"spawn-1","message":{"role":"assistant","content":[{"type":"text","text":"PONG"}]}}`
const qoderChildFinished = `{"type":"system","subtype":"task_notification","task_id":"task-1","tool_use_id":"spawn-1","status":"completed"}`

const qoderWorkflowLaunch = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"run-workflow","content":"Workflow launched in background."}]},"tool_use_result":{"summary":"Ask two children.","payload":"{\"status\":\"async_launched\",\"taskId\":\"wf-1\",\"runId\":\"wf_1\"}"}}`

func TestQoderWorkflowKeepsDistinctChildrenUnderOneGroup(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(qoderWorkflowLaunch))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"child-task-1","tool_use_id":"run-workflow","task_type":"local_agent","subagent_type":"workflow-subagent","description":"First child","prompt":"Reply with FIRST."}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"child-task-2","tool_use_id":"run-workflow","task_type":"local_agent","subagent_type":"workflow-subagent","description":"Second child","prompt":"Reply with SECOND."}`))

	workflow, exists := sink.BackgroundTask("workflow:session-1:run-workflow")
	require.True(t, exists, "the native launch must open a workflow row")
	assert.Equal(t, bgtask.KindWorkflow, workflow.Kind)
	assert.Equal(t, bgtask.StatusRunning, workflow.Status)
	assert.Equal(t, "Ask two children.", workflow.GroupLabel)
	first, exists := sink.BackgroundTask("child-task-1")
	require.True(t, exists, "the first task ID must keep its own row")
	second, exists := sink.BackgroundTask("child-task-2")
	require.True(t, exists, "the second task ID must keep its own row")
	assert.Equal(t, "workflow:session-1:run-workflow", first.GroupKey)
	assert.Equal(t, first.GroupKey, second.GroupKey)
	assert.Equal(t, workflow.GroupLabel, first.GroupLabel)
	assert.Equal(t, workflow.GroupLabel, second.GroupLabel)
	assert.NotEqual(t, first.ChildAgentID, second.ChildAgentID)
	assert.NotEmpty(t, first.ChildAgentID)
	assert.NotEmpty(t, second.ChildAgentID)
	assert.Len(t, sink.Child(first.ChildAgentID).Messages(), 1)
	assert.Len(t, sink.Child(second.ChildAgentID).Messages(), 1)
}

func TestQoderChildTaskLifecycleRoutesItsTranscript(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(qoderChildStarted))

	row, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok, "the native task start must create a child registry row")
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "Ask for one word", row.Title)
	require.NotEmpty(t, row.ChildAgentID)
	child := sink.Child(row.ChildAgentID)
	require.Len(t, child.Messages(), 1)
	var prompt map[string]string
	require.NoError(t, json.Unmarshal(child.Messages()[0].Content, &prompt))
	assert.Equal(t, "Reply with PONG.", prompt["content"])

	a.HandleOutput([]byte(qoderChildPrompt))
	a.HandleOutput([]byte(qoderChildAnswer))
	assert.Empty(t, sink.Messages(), "child frames must not enter the root transcript")
	childMessages := child.Messages()
	require.Len(t, childMessages, 3)
	assert.Contains(t, string(childMessages[2].Content), "PONG")

	a.HandleOutput([]byte(qoderChildFinished))
	row, ok = sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.Empty(t, sink.Messages(), "task bookends belong to the registry")
}

func TestQoderChildTaskReplayDoesNotDuplicateItsPrompt(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(qoderChildStarted))
	a.HandleOutput([]byte(qoderChildStarted))

	row, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	assert.Len(t, sink.BackgroundTasks(), 1)
	assert.Len(t, sink.Child(row.ChildAgentID).Messages(), 1)
}

func TestQoderChildStreamBlocksBecomeRenderableRows(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(qoderChildStarted))
	for _, frame := range []string{
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"message_start","message":{"id":"child-answer","role":"assistant","content":[]}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Check the task."}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"PONG"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_stop","index":1}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"message_stop"}}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	row, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	childRows := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, childRows, 3, "the prompt, thought, and answer are the only visible child rows")
	assert.Empty(t, sink.Messages())
	var thought, answer struct {
		Type    string `json:"type"`
		Message struct {
			Content []map[string]any `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(childRows[1].Content, &thought))
	require.NoError(t, json.Unmarshal(childRows[2].Content, &answer))
	require.Len(t, thought.Message.Content, 1)
	require.Len(t, answer.Message.Content, 1)
	assert.Equal(t, "assistant", thought.Type)
	assert.Equal(t, "Check the task.", thought.Message.Content[0]["thinking"])
	assert.Equal(t, "assistant", answer.Type)
	assert.Equal(t, "PONG", answer.Message.Content[0]["text"])
}

func TestQoderChildToolArgumentsUseCompletedJSON(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(qoderChildStarted))
	for _, frame := range []string{
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"read-1","name":"Read","input":{}}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"note.txt\"}"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_stop","index":0}}`,
	} {
		a.HandleOutput([]byte(frame))
	}
	row, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	childRows := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, childRows, 2)
	var envelope struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type  string         `json:"type"`
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(childRows[1].Content, &envelope))
	require.Len(t, envelope.Message.Content, 1)
	assert.Equal(t, "assistant", envelope.Type)
	assert.Equal(t, "tool_use", envelope.Message.Content[0].Type)
	assert.Equal(t, "read-1", envelope.Message.Content[0].ID)
	assert.Equal(t, "Read", envelope.Message.Content[0].Name)
	assert.Equal(t, "note.txt", envelope.Message.Content[0].Input["file_path"])
}

func TestQoderChildStreamCapsLargeTextWithoutLosingTheRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(qoderChildStarted))
	a.HandleOutput([]byte(`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`))
	frame, err := json.Marshal(map[string]any{
		"type": "stream_event", "parent_tool_use_id": "spawn-1",
		"event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{
			"type": "text_delta", "text": strings.Repeat("A", qoderChildBlockByteLimit+32),
		}},
	})
	require.NoError(t, err)
	a.HandleOutput(frame)
	a.HandleOutput([]byte(`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_stop","index":0}}`))
	row, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	childRows := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, childRows, 2)
	assert.Contains(t, string(childRows[1].Content), "[Child output truncated]")
	assert.Less(t, len(childRows[1].Content), qoderChildBlockByteLimit+512)
}

func TestQoderChildStreamsKeepConcurrentChildrenSeparate(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(qoderChildStarted))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-2","tool_use_id":"spawn-2","task_type":"local_agent","description":"Second child","prompt":"Reply with SECOND."}`))
	for _, frame := range []string{
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-2","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-2","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"SECOND"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"FIRST"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-2","event":{"type":"content_block_stop","index":0}}`,
	} {
		a.HandleOutput([]byte(frame))
	}
	first, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	second, ok := sink.BackgroundTask("spawn-2")
	require.True(t, ok)
	firstRows := sink.Child(first.ChildAgentID).Messages()
	secondRows := sink.Child(second.ChildAgentID).Messages()
	require.Len(t, firstRows, 2)
	require.Len(t, secondRows, 2)
	assert.Contains(t, string(firstRows[1].Content), "FIRST")
	assert.NotContains(t, string(firstRows[1].Content), "SECOND")
	assert.Contains(t, string(secondRows[1].Content), "SECOND")
	assert.NotContains(t, string(secondRows[1].Content), "FIRST")
}

func TestQoderChildMalformedToolInputShowsAReadableNotice(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(qoderChildStarted))
	for _, frame := range []string{
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"read-1","name":"Read","input":{}}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{bad"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"spawn-1","event":{"type":"content_block_stop","index":0}}`,
	} {
		a.HandleOutput([]byte(frame))
	}
	row, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	childRows := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, childRows, 2)
	assert.Contains(t, string(childRows[1].Content), "Child tool input could not be decoded.")
	assert.True(t, json.Valid(childRows[1].Content))
}

func TestQoderChildTaskFinalStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status bgtask.Status
	}{
		{name: "completed", status: bgtask.StatusCompleted},
		{name: "failed", status: bgtask.StatusFailed},
		{name: "stopped", status: bgtask.StatusStopped},
		{name: "cancelled", status: bgtask.StatusStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput([]byte(qoderChildStarted))
			a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-1","tool_use_id":"spawn-1","status":"` + tc.name + `"}`))
			row, ok := sink.BackgroundTask("spawn-1")
			require.True(t, ok)
			assert.Equal(t, tc.status, row.Status)
		})
	}
}

func TestQoderChildTaskIgnoresMissingIdentity(t *testing.T) {
	t.Parallel()
	for _, frame := range []string{
		`{"type":"system","subtype":"task_started","task_id":"task-1","task_type":"local_agent","prompt":"work"}`,
		`{"type":"system","subtype":"task_started","tool_use_id":"spawn-1","task_type":"local_agent","prompt":"work"}`,
		`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"unknown","prompt":"work"}`,
	} {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(frame))
		assert.Empty(t, sink.BackgroundTasks(), frame)
	}
}

func TestQoderUnknownChildFrameDoesNotEnterTheRoot(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"unknown","message":{"role":"assistant","content":[{"type":"text","text":"stray child"}]}}`))
	assert.Empty(t, sink.Messages(), "an unknown child frame must not become a root answer")
}
