package qoder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const qoderChildStarted = `{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"local_agent","description":"Ask for one word","prompt":"Reply with PONG."}`
const qoderChildPrompt = `{"type":"user","parent_tool_use_id":"spawn-1","message":{"role":"user","content":[{"type":"text","text":"Reply with PONG."}]}}`
const qoderChildAnswer = `{"type":"assistant","parent_tool_use_id":"spawn-1","message":{"role":"assistant","content":[{"type":"text","text":"PONG"}]}}`
const qoderChildFinished = `{"type":"system","subtype":"task_notification","task_id":"task-1","tool_use_id":"spawn-1","status":"completed"}`

const qoderWorkflowLaunch = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"run-workflow","content":"Workflow launched in background."}]},"tool_use_result":{"summary":"Ask two children.","payload":"{\"status\":\"async_launched\",\"taskId\":\"wf-1\",\"runId\":\"wf_1\"}"}}`

func qoderWorkflowUserNotification(t *testing.T, sessionID, taskID, callID, outputFile, status string) []byte {
	t.Helper()
	text := fmt.Sprintf("<task-notification>\n<task-id>%s</task-id>\n<tool-use-id>%s</tool-use-id>\n<output-file>%s</output-file>\n<status>%s</status>\n<summary>Compute one local value.</summary>\n<result>NATIVEWORKFLOW42</result>\n</task-notification>", taskID, callID, outputFile, status)
	raw, err := json.Marshal(map[string]any{
		"type": "user", "session_id": sessionID, "parent_tool_use_id": nil, "isReplay": true,
		"message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": text}}},
	})
	require.NoError(t, err)
	return raw
}

func TestQoderWorkflowUserCompletionClosesExactZeroChildRun(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			fixture := newQoderWorkflowFixture(t)
			writeQoderWorkflowFixtureFile(t, fixture.outputFile, fmt.Sprintf(`{"runId":"wf_1","taskId":"wf-1","workflowName":"native-code","status":%q,"result":"NATIVEWORKFLOW42","agentCount":0}`, status))
			require.NoError(t, os.Remove(fixture.journal))
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
			a.HandleOutput([]byte(qoderWorkflowLaunch))
			before := len(sink.Messages())
			notification := qoderWorkflowUserNotification(t, "session-1", "wf-1", "run-workflow", fixture.outputFile, status)
			a.HandleOutput(notification)
			workflow, exists := sink.BackgroundTask("workflow:session-1:run-workflow")
			require.True(t, exists)
			expected := bgtask.StatusCompleted
			if status == "failed" {
				expected = bgtask.StatusFailed
			}
			assert.Equal(t, expected, workflow.Status)
			assert.Empty(t, workflow.ChildAgentID)
			assert.Len(t, sink.BackgroundTasks(), 1)
			require.Len(t, sink.Messages(), before+1)
			assert.JSONEq(t, string(notification), string(sink.Messages()[before].Content))
			assert.Empty(t, a.workflows)
		})
	}
}

func TestQoderWorkflowUserCompletionRejectsForeignIdentity(t *testing.T) {
	t.Parallel()
	for _, wrong := range []string{"session", "task", "call", "path"} {
		t.Run(wrong, func(t *testing.T) {
			t.Parallel()
			fixture := newQoderWorkflowFixture(t)
			writeQoderWorkflowFixtureFile(t, fixture.outputFile, `{"runId":"wf_1","taskId":"wf-1","workflowName":"native-code","status":"completed","result":"NATIVEWORKFLOW42","agentCount":0}`)
			writeQoderWorkflowFixtureFile(t, fixture.journal, "")
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
			a.HandleOutput([]byte(qoderWorkflowLaunch))
			sessionID, taskID, callID, path := "session-1", "wf-1", "run-workflow", fixture.outputFile
			switch wrong {
			case "session":
				sessionID = "session-2"
			case "task":
				taskID = "wf-other"
			case "call":
				callID = "other-call"
			case "path":
				path = filepath.Join(fixture.workingDir, "output.json")
			}
			a.HandleOutput(qoderWorkflowUserNotification(t, sessionID, taskID, callID, path, "completed"))
			workflow, exists := sink.BackgroundTask("workflow:session-1:run-workflow")
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, workflow.Status)
		})
	}
}

func TestQoderWorkflowUserCompletionRejectsMalformedOrUnrelatedText(t *testing.T) {
	t.Parallel()
	for _, wrong := range []string{"missing-status", "repeated-task", "running", "ordinary-prefix", "assistant", "child", "not-replay", "nested-task", "padded-task"} {
		t.Run(wrong, func(t *testing.T) {
			t.Parallel()
			fixture := newQoderWorkflowFixture(t)
			writeQoderWorkflowFixtureFile(t, fixture.outputFile, `{"runId":"wf_1","taskId":"wf-1","workflowName":"native-code","status":"completed","result":"NATIVEWORKFLOW42","agentCount":0}`)
			writeQoderWorkflowFixtureFile(t, fixture.journal, "")
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
			a.HandleOutput([]byte(qoderWorkflowLaunch))
			raw := qoderWorkflowUserNotification(t, "session-1", "wf-1", "run-workflow", fixture.outputFile, "completed")
			var envelope map[string]any
			require.NoError(t, json.Unmarshal(raw, &envelope))
			message := envelope["message"].(map[string]any)
			block := message["content"].([]any)[0].(map[string]any)
			text := block["text"].(string)
			switch wrong {
			case "missing-status":
				text = strings.ReplaceAll(text, "<status>completed</status>\n", "")
			case "repeated-task":
				text = strings.ReplaceAll(text, "<task-id>wf-1</task-id>", "<task-id>wf-1</task-id><task-id>wf-1</task-id>")
			case "running":
				text = strings.ReplaceAll(text, "<status>completed</status>", "<status>running</status>")
			case "ordinary-prefix":
				text = "Explain this example XML.\n" + text
			case "assistant":
				envelope["type"] = "assistant"
				message["role"] = "assistant"
			case "child":
				envelope["parent_tool_use_id"] = "other-spawn"
			case "not-replay":
				envelope["isReplay"] = false
			case "nested-task":
				text = strings.ReplaceAll(text, "<task-id>wf-1</task-id>", "<task-id>wf-1<extra>ignored</extra></task-id>")
			case "padded-task":
				text = strings.ReplaceAll(text, "<task-id>wf-1</task-id>", "<task-id> wf-1 </task-id>")
			}
			block["text"] = text
			raw, err := json.Marshal(envelope)
			require.NoError(t, err)
			a.HandleOutput(raw)
			workflow, exists := sink.BackgroundTask("workflow:session-1:run-workflow")
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, workflow.Status)
		})
	}
}

func TestQoderWorkflowUserCompletionReplayKeepsClosedRowAndOriginalBytes(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	writeQoderWorkflowFixtureFile(t, fixture.outputFile, `{"runId":"wf_1","taskId":"wf-1","workflowName":"native-code","status":"completed","result":"NATIVEWORKFLOW42","agentCount":0}`)
	writeQoderWorkflowFixtureFile(t, fixture.journal, "")
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	a.HandleOutput([]byte(qoderWorkflowLaunch))
	notification := qoderWorkflowUserNotification(t, "session-1", "wf-1", "run-workflow", fixture.outputFile, "completed")
	a.HandleOutput(notification)
	a.HandleOutput(notification)
	workflow, exists := sink.BackgroundTask("workflow:session-1:run-workflow")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusCompleted, workflow.Status)
	assert.Len(t, sink.BackgroundTasks(), 1)
	assert.Empty(t, a.workflows)
	rows := sink.Messages()
	require.GreaterOrEqual(t, len(rows), 2)
	assert.JSONEq(t, string(notification), string(rows[len(rows)-2].Content))
	assert.JSONEq(t, string(notification), string(rows[len(rows)-1].Content))
}

func TestQoderWorkflowUserCompletionKeepsMissingCanonicalArchivePending(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	require.NoError(t, os.Remove(fixture.outputFile))
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	a.HandleOutput([]byte(qoderWorkflowLaunch))
	a.HandleOutput(qoderWorkflowUserNotification(t, "session-1", "wf-1", "run-workflow", fixture.outputFile, "completed"))
	workflow, exists := sink.BackgroundTask("workflow:session-1:run-workflow")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusRunning, workflow.Status)
	assert.Len(t, a.workflows, 1)
}

func TestQoderWorkflowUserCompletionRequiresJournalForDeclaredOrKnownChildren(t *testing.T) {
	t.Parallel()
	for _, knownChild := range []bool{false, true} {
		t.Run(fmt.Sprintf("known-child-%t", knownChild), func(t *testing.T) {
			t.Parallel()
			fixture := newQoderWorkflowFixture(t)
			count := 1
			if knownChild {
				count = 0
			}
			writeQoderWorkflowFixtureFile(t, fixture.outputFile, fmt.Sprintf(`{"runId":"wf_1","taskId":"wf-1","workflowName":"native-code","status":"completed","agentCount":%d}`, count))
			require.NoError(t, os.Remove(fixture.journal))
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
			a.HandleOutput([]byte(qoderWorkflowLaunch))
			if knownChild {
				a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"child-task-1","tool_use_id":"run-workflow","task_type":"local_agent","subagent_type":"workflow-subagent","description":"First child","prompt":"Reply with FIRST."}`))
			}
			a.HandleOutput(qoderWorkflowUserNotification(t, "session-1", "wf-1", "run-workflow", fixture.outputFile, "completed"))
			workflow, exists := sink.BackgroundTask("workflow:session-1:run-workflow")
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, workflow.Status)
		})
	}
}

func TestQoderWorkflowUserCompletionRequiresCanonicalFinalStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"running", "failed", "", "unknown"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			fixture := newQoderWorkflowFixture(t)
			writeQoderWorkflowFixtureFile(t, fixture.outputFile, fmt.Sprintf(`{"runId":"wf_1","taskId":"wf-1","workflowName":"native-code","status":%q,"agentCount":0}`, status))
			writeQoderWorkflowFixtureFile(t, fixture.journal, "")
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
			a.HandleOutput([]byte(qoderWorkflowLaunch))
			a.HandleOutput(qoderWorkflowUserNotification(t, "session-1", "wf-1", "run-workflow", fixture.outputFile, "completed"))
			workflow, exists := sink.BackgroundTask("workflow:session-1:run-workflow")
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, workflow.Status)
		})
	}
}

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
