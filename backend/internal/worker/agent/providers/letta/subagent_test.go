package letta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	lettaTestChildID     = "subagent-native-1"
	lettaTestChildPrompt = "Count the files and report the number."
)

func newLettaChildTestAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()
	return a, sink
}

func lettaChildStateFrame(t *testing.T, status string) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"type":    "update_subagent_state",
		"runtime": map[string]any{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"},
		"subagents": []any{map[string]any{
			"subagent_id": lettaTestChildID, "subagent_type": "explore",
			"description": "Count the files", "prompt": lettaTestChildPrompt,
			"status": status, "is_background": true, "silent": false,
			"tool_call_id": "call-agent-1", "parent_agent_id": "agent-local-1",
			"parent_conversation_id": "local-conv-1",
		}},
	})
	require.NoError(t, err)
	return frame
}

func lettaChildDeltaFrame(t *testing.T, delta map[string]any) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"type": "stream_delta", "subagent_id": lettaTestChildID,
		"runtime": map[string]any{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"},
		"delta":   delta,
	})
	require.NoError(t, err)
	return frame
}

func TestLettaChildSnapshotOpensOneRegistryRowAndPrompt(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	frame := lettaChildStateFrame(t, "running")
	a.HandleOutput(frame)
	a.HandleOutput(frame)

	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok, "the native child snapshot must open a Worker registry row")
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	require.NotEmpty(t, row.ChildAgentID)
	assert.Contains(t, row.Title, "Count the files")
	childRows := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, childRows, 1, "a repeated snapshot must not repeat the child prompt")
	assert.Contains(t, string(childRows[0].Content), lettaTestChildPrompt)
}

func TestLettaChildDeltasStayInTheirChildTranscript(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "letta-tool-call-agent-1", ProviderChildKey: lettaTestChildID, Title: "Count the files"})
	require.NoError(t, err)
	require.NoError(t, sink.PersistChildPrompt(childID, lettaTestChildPrompt))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: lettaTestChildID, Kind: bgtask.KindSubagent, ChildAgentID: childID,
		Title: "Count the files", Status: bgtask.StatusRunning,
	}))
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"message_type": "assistant_message", "content": []any{map[string]string{"type": "text", "text": "LETTA_CHILD_EARLY"}},
	}))
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"message_type": "client_tool_start", "tool_call_id": "call-read-1",
		"tool_name": "Read", "tool_input": map[string]string{"file_path": "note.txt"},
	}))
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"message_type": "tool_return_message", "tool_call_id": "call-read-1",
		"tool_return": "LETTA_CHILD_RESULT",
	}))
	a.HandleOutput(lettaChildStateFrame(t, "completed"))

	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	var childContents []string
	for _, message := range sink.Child(childID).Messages() {
		childContents = append(childContents, string(message.Content))
	}
	childText := strings.Join(childContents, "\n")
	assert.Contains(t, childText, "LETTA_CHILD_EARLY")
	assert.Contains(t, childText, "call-read-1")
	assert.Contains(t, childText, "LETTA_CHILD_RESULT")
	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "LETTA_CHILD_EARLY")
		assert.NotContains(t, string(message.Content), "call-read-1")
		assert.NotContains(t, string(message.Content), "LETTA_CHILD_RESULT")
	}
}

func TestLettaNativeChildToolCallMessageOpensTheReadSpan(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"type": "message", "message_type": "tool_call_message",
		"tool_calls": []any{map[string]any{
			"tool_call_id": "call-read-native", "name": "Read",
			"arguments": `{"file_path":"note.txt"}`,
		}},
	}))
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"type": "message", "message_type": "tool_return_message",
		"tool_call_id": "call-read-native", "tool_return": "NATIVE_READ_RESULT",
	}))
	var contents []string
	for _, message := range sink.Child(row.ChildAgentID).Messages() {
		contents = append(contents, string(message.Content))
	}
	require.GreaterOrEqual(t, len(contents), 3, "the prompt, Read request, and result need separate child rows")
	assert.Contains(t, contents[1], "call-read-native")
	assert.Contains(t, contents[2], "NATIVE_READ_RESULT")
	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "call-read-native")
	}
}

func TestLettaNativeChildToolCallMessageSplitsTwoCalls(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"type": "message", "message_type": "tool_call_message",
		"tool_calls": []any{
			map[string]any{"tool_call_id": "read-a", "name": "Read", "arguments": `{"file_path":"a.txt"}`},
			map[string]any{"tool_call_id": "read-b", "name": "Read", "arguments": `{"file_path":"b.txt"}`},
		},
	}))
	for _, callID := range []string{"read-a", "read-b"} {
		a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
			"message_type": "tool_return_message", "tool_call_id": callID,
			"tool_return": "result for " + callID,
		}))
	}
	var requests []string
	for _, message := range sink.Child(row.ChildAgentID).Messages() {
		if strings.Contains(string(message.Content), "tool_call_message") {
			requests = append(requests, string(message.Content))
		}
	}
	require.Len(t, requests, 2)
	assert.Contains(t, requests[0], "read-a")
	assert.NotContains(t, requests[0], "read-b")
	assert.Contains(t, requests[1], "read-b")
	assert.NotContains(t, requests[1], "read-a")
	assert.Len(t, sink.Child(row.ChildAgentID).Messages(), 5)
}

func TestLettaChildSnapshotRejectsAnotherParentAndDuplicateID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		parentID   string
		duplicates bool
	}{
		{name: "wrong parent", parentID: "agent-other"},
		{name: "duplicate child id", parentID: "agent-local-1", duplicates: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newLettaChildTestAgent(t)
			child := map[string]any{
				"subagent_id": lettaTestChildID, "subagent_type": "general-purpose",
				"description": "Count the files", "prompt": lettaTestChildPrompt,
				"status": "running", "tool_call_id": "call-agent-1",
				"parent_agent_id": tc.parentID, "parent_conversation_id": "local-conv-1",
			}
			children := []any{child}
			if tc.duplicates {
				children = append(children, child)
			}
			frame, err := json.Marshal(map[string]any{"type": "update_subagent_state", "subagents": children})
			require.NoError(t, err)
			a.HandleOutput(frame)
			assert.Empty(t, sink.BackgroundTasks())
			assert.Empty(t, sink.ChildAgentIDs())
		})
	}
}

func lettaRootDeltaFrame(t *testing.T, delta map[string]any) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"type":    "stream_delta",
		"runtime": map[string]any{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"},
		"delta":   delta,
	})
	require.NoError(t, err)
	return frame
}

func lettaChildTaskReceipt(t *testing.T) []byte {
	return lettaChildTaskReceiptWithOutput(t, "")
}

func lettaChildTaskReceiptWithOutput(t *testing.T, path string) []byte {
	t.Helper()
	output := ""
	if path != "" {
		output = "Output file: " + path + "\n"
	}
	return lettaRootDeltaFrame(t, map[string]any{
		"message_type": "tool_return_message", "tool_call_id": "call-agent-1",
		"tool_return": "Task running in background with task ID: task_1\nAgent ID: agent-child\nConversation ID: default\n" + output,
	})
}

func lettaChildTaskNotification(t *testing.T, taskID, answer string) []byte {
	t.Helper()
	result := "subagent_type=general-purpose subagent_id=" + lettaTestChildID +
		" subagent_status=success agent_id=agent-child conversation_id=default\n\n" + answer
	return lettaRootDeltaFrame(t, map[string]any{
		"message_type": "user_message",
		"content":      "<task-notification>\n<task-id>" + taskID + "</task-id>\n<status>completed</status>\n<summary>Child completed</summary>\n<result>" + result + "</result>\n</task-notification>",
	})
}

func TestLettaChildCompletionFlushesItsBufferedAnswer(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	a.HandleOutput(lettaChildTaskReceipt(t))
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"message_type": "assistant_message", "content": []any{map[string]string{"type": "text", "text": "LETTA_FINAL_IN_STREAM"}},
	}))
	a.HandleOutput(lettaChildTaskNotification(t, "task_1", "LETTA_FINAL_IN_STREAM"))

	completed, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, completed.Status)
	var matches int
	for _, message := range sink.Child(row.ChildAgentID).Messages() {
		if strings.Contains(string(message.Content), "LETTA_FINAL_IN_STREAM") {
			matches++
		}
	}
	assert.Equal(t, 1, matches, "the native stream and task report must create one final child answer")
}

func TestLettaChildCompletionUsesTheReportWhenTheStreamHasNoFinalMessage(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	a.HandleOutput(lettaChildTaskReceipt(t))
	a.HandleOutput(lettaChildTaskNotification(t, "task_1", "LETTA_FINAL_ONLY_IN_REPORT"))

	completed, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, completed.Status)
	var childText []string
	for _, message := range sink.Child(row.ChildAgentID).Messages() {
		childText = append(childText, string(message.Content))
	}
	assert.Contains(t, strings.Join(childText, "\n"), "LETTA_FINAL_ONLY_IN_REPORT")
}

func TestLettaChildCompletionRejectsAnotherTaskID(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	a.HandleOutput(lettaChildTaskReceipt(t))
	a.HandleOutput(lettaChildTaskNotification(t, "task_other", "WRONG_TASK_REPORT"))

	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	for _, message := range sink.Child(row.ChildAgentID).Messages() {
		assert.NotContains(t, string(message.Content), "WRONG_TASK_REPORT")
	}
}

func TestLettaChildCompletionPreservesTheReportTextAndIgnoresReplay(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	a.HandleOutput(lettaChildTaskReceipt(t))
	const report = "  indented child report\nsecond line  "
	notification := lettaChildTaskNotification(t, "task_1", report)
	a.HandleOutput(notification)
	a.HandleOutput(notification)

	var answers []string
	for _, message := range sink.Child(row.ChildAgentID).Messages() {
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &content) == nil && content.Type == "assembled_message" {
			answers = append(answers, content.Text)
		}
	}
	assert.Equal(t, []string{report}, answers)
}

func TestLettaChildCompletionReadsTheFullNativeTaskReport(t *testing.T) {
	t.Parallel()
	directory, err := os.MkdirTemp("", "letta-background-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	path := filepath.Join(directory, "task_1.log")
	const fullReport = "FULL_LETTA_CHILD_REPORT_AFTER_NATIVE_TRUNCATION"
	log := "[Task started: Count the files]\n[subagent_type: general-purpose]\n\n" +
		"subagent_type=general-purpose subagent_id=" + lettaTestChildID +
		" subagent_status=success agent_id=agent-child conversation_id=default\n\n" +
		fullReport + "\n\n[Task completed]\n"
	require.NoError(t, os.WriteFile(path, []byte(log), 0o600))
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	a.HandleOutput(lettaChildTaskReceiptWithOutput(t, path))
	a.HandleOutput(lettaChildTaskNotification(t, "task_1", "CLIPPED_LETTA_CHILD_REPORT"))

	var content []string
	for _, message := range sink.Child(row.ChildAgentID).Messages() {
		content = append(content, string(message.Content))
	}
	assert.Contains(t, strings.Join(content, "\n"), fullReport)
	assert.NotContains(t, strings.Join(content, "\n"), "CLIPPED_LETTA_CHILD_REPORT")
}

func lettaOutputChildState(t *testing.T, childID, status string) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"type": "update_subagent_state",
		"subagents": []any{map[string]any{
			"subagent_id": childID, "subagent_type": "explore", "description": "Native output child",
			"prompt": "Read the native output " + childID, "status": status, "tool_call_id": "spawn-" + childID,
			"parent_agent_id": "agent-local-1", "parent_conversation_id": "local-conv-1",
		}},
	})
	require.NoError(t, err)
	return frame
}

func TestLettaLiveOutputKeepsRootAndTwoChildOwnersSeparate(t *testing.T) {
	t.Parallel()
	a, root := newLettaChildTestAgent(t)
	children := map[string]*agenttest.Sink{}
	for _, scope := range []string{"child-one", "child-two"} {
		a.HandleOutput(lettaOutputChildState(t, scope, "running"))
		row, found := root.BackgroundTask(scope)
		require.True(t, found)
		children[scope] = root.Child(row.ChildAgentID)
	}
	for _, scope := range []string{"", "child-one", "child-two"} {
		start, _ := lettaOutputFrame(t, scope, lettaOutputStart("shared-call", "run-"+scope))
		a.HandleOutput(start)
		window, _ := lettaOutputFrame(t, scope, lettaOutputWindow("shared-call", "run-"+scope, "current window for "+scope))
		a.HandleOutput(window)
	}
	assert.Equal(t, []agent.ProgressUpdate{agent.OutputTailProgress("letta-tool-shared-call", "current window for ", false)}, root.ProgressUpdates())
	for scope, sink := range children {
		assert.Equal(t, []agent.ProgressUpdate{agent.OutputTailProgress("letta-tool-shared-call", "current window for "+scope, false)}, sink.ProgressUpdates())
	}
	unknown, _ := lettaOutputFrame(t, "unknown-child", lettaOutputWindow("shared-call", "run-", "unowned window"))
	a.HandleOutput(unknown)
	assert.Len(t, root.Messages(), 2)
	a.HandleOutput(lettaOutputChildState(t, "child-one", "completed"))
	assert.NotContains(t, a.tools, lettaToolKey("child-one", "shared-call"))
	assert.Contains(t, a.tools, lettaToolKey("child-two", "shared-call"))
	assert.Contains(t, a.tools, lettaToolKey("", "shared-call"))
	assert.Empty(t, root.ClosedSpans())
	assert.Empty(t, children["child-two"].ClosedSpans())
	assert.Equal(t, []string{"letta-tool-shared-call"}, children["child-one"].ClosedSpans())
	before := len(children["child-one"].Messages())
	late, native := lettaOutputFrame(t, "child-one", map[string]any{"message_type": "tool_return_message", "id": "synthetic-tool-return-late-native-final", "tool_call_id": "shared-call", "run_id": "run-child-one", "status": "success", "tool_return": "late native child result"})
	a.HandleOutput(late)
	rows := children["child-one"].Messages()
	require.Len(t, rows, before+1)
	assert.Equal(t, native, rows[len(rows)-1].Content)
	assert.True(t, rows[len(rows)-1].Closing)
	assert.Contains(t, a.tools, lettaToolKey("child-two", "shared-call"))
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	assert.NotContains(t, a.tools, lettaToolKey("", "shared-call"))
	assert.Contains(t, a.tools, lettaToolKey("child-two", "shared-call"))
	assert.Equal(t, []int{1}, agenttest.TurnToolUseCounts(t, root.Messages()))
	assert.Equal(t, []string{"letta-tool-shared-call"}, root.ClosedSpans())
	a.HandleOutput(lettaOutputChildState(t, "child-two", "error"))
	assert.Empty(t, a.tools)
}

func TestLettaScopeCompletionClearsAnErrorEndWithoutInventingAResult(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	start, _ := lettaOutputFrame(t, "", lettaOutputStart("call", "run"))
	window, _ := lettaOutputFrame(t, "", lettaOutputWindow("call", "run", "actual live output"))
	end, native := lettaOutputFrame(t, "", map[string]any{"message_type": "client_tool_end", "tool_call_id": "call", "run_id": "run", "status": "error"})
	a.HandleOutput(start)
	a.HandleOutput(window)
	a.HandleOutput(end)
	assert.Empty(t, sink.ClosedSpans())
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	rows := sink.Messages()
	require.Len(t, rows, 4)
	assert.Equal(t, native, rows[2].Content)
	for _, row := range rows {
		assert.False(t, row.Closing, "native scope completion must create no result payload")
	}
	assert.True(t, rows[3].TurnEnd)
	assert.Empty(t, a.tools)
	assert.Equal(t, []string{"letta-tool-call"}, sink.ClosedSpans())
}

func TestLettaTaskNotificationClearsLiveCallsBeforeItsLateNativeFinal(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, found := sink.BackgroundTask(lettaTestChildID)
	require.True(t, found)
	child := sink.Child(row.ChildAgentID)
	a.HandleOutput(lettaChildTaskReceipt(t))
	start, _ := lettaOutputFrame(t, lettaTestChildID, lettaOutputStart("native-child-call", "native-child-run"))
	window, _ := lettaOutputFrame(t, lettaTestChildID, lettaOutputWindow("native-child-call", "native-child-run", "actual child output"))
	a.HandleOutput(start)
	a.HandleOutput(window)
	a.HandleOutput(lettaChildTaskNotification(t, "task_1", "The actual native child report."))
	assert.True(t, a.children[lettaTestChildID].closed)
	assert.NotContains(t, a.tools, lettaToolKey(lettaTestChildID, "native-child-call"))
	assert.Contains(t, child.ProgressUpdates(), agent.CompleteOutputProgress("letta-tool-native-child-call"))
	before := len(child.ProgressUpdates())
	a.HandleOutput(window)
	assert.Len(t, child.ProgressUpdates(), before, "a late native window must not revive the completed child")
	final, native := lettaOutputFrame(t, lettaTestChildID, map[string]any{
		"message_type": "tool_return_message", "id": "synthetic-tool-return-late-child-final",
		"run_id": "native-child-run", "tool_call_id": "native-child-call",
		"status": "success", "tool_return": "The actual late native tool return.",
	})
	a.HandleOutput(final)
	rows := child.Messages()
	assert.Equal(t, native, rows[len(rows)-1].Content)
	assert.True(t, rows[len(rows)-1].Closing)
}
