package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityChildKeyOpensAndOwnsTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.streamCancel()
	a.root.childKeys["child"] = &childKey{role: "Researcher"}
	a.ensureChildren(a.root)
	row, ok := sink.BackgroundTask("child")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "Researcher", row.Title)
	child := a.cascades["child"]
	require.NotNil(t, child)
	child.parent = a.root
	a.beginTurn(child)
	a.endTurn(child, agent.MessageCompletionComplete)
	row, _ = sink.BackgroundTask("child")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestAntigravityInvokeResultsLinkSpawnWithoutSecondRow(t *testing.T) {
	a, sink := testAgent(t)
	a.streamCancel()
	a.root.childKeys["child"] = &childKey{role: "Researcher"}
	a.ensureChildren(a.root)
	var step antigravityStep
	step.Type = contracts.AntigravityStepTypeInvokeSubagent
	step.Metadata.ToolCall.ID = "spawn-span"
	step.Metadata.ToolCall.ArgumentsJSON = `{"Prompt":"original child prompt"}`
	step.InvokeSubagent.Subagents = append(step.InvokeSubagent.Subagents, struct {
		TypeName string `json:"typeName"`
		Role     string `json:"role"`
	}{TypeName: "research", Role: "Researcher"})
	step.InvokeSubagent.Results = append(step.InvokeSubagent.Results, struct {
		ConversationID string `json:"conversationId"`
	}{ConversationID: "child"})
	a.linkSubagentResults(a.root, 0, step)
	a.linkPendingChildSpans(a.root)
	assert.Equal(t, "spawn-span", a.root.childKeys["child"].spawnSpanID)
	assert.Len(t, sink.BackgroundTasks(), 1)
	childSink := sink.Child(a.cascades["child"].childAgentID)
	messages := transcriptMessages(childSink)
	require.Len(t, messages, 1)
	assert.Contains(t, string(messages[0].Content), "original child prompt")
	a.linkSubagentResults(a.root, 0, step)
	require.Len(t, transcriptMessages(childSink), 1)
}

func TestAntigravityNestedChildUsesSameReducer(t *testing.T) {
	a, sink := testAgent(t)
	a.streamCancel()
	a.root.childKeys["child"] = &childKey{role: "Parent child"}
	a.ensureChildren(a.root)
	child := a.cascades["child"]
	child.childKeys["grandchild"] = &childKey{role: "Nested child"}
	a.ensureChildren(child)
	row, ok := sink.BackgroundTask("grandchild")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.NotNil(t, a.cascades["grandchild"].parent)
}

func TestAntigravityBackgroundShellStatus(t *testing.T) {
	a, sink := testAgent(t)
	a.applyBackgroundTasks(a.root, &backgroundTasksPatch{
		Indices: []int{0}, BackgroundTasks: []backgroundTaskSnapshot{{TaskSnapshot: struct {
			TaskID      string `json:"taskId"`
			ToolName    string `json:"toolName"`
			ToolSummary string `json:"toolSummary"`
			Description string `json:"description"`
			StepIndex   int    `json:"stepIndex"`
			LogURI      string `json:"logUri"`
		}{TaskID: "shell", Description: "sleep 1", StepIndex: 2, LogURI: "file:///tmp/shell.log"}}}, TotalLength: 1,
	}, true)
	row, ok := sink.BackgroundTask("shell")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "/tmp/shell.log", row.Description)
	step := antigravityStep{Type: contracts.AntigravityStepTypeRunCommand, Status: contracts.AntigravityStepStatusDone}
	step.RunCommand.ExitCode = 1
	a.root.steps[2] = &stepState{}
	a.applyStep(a.root, 2, step, json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE","runCommand":{"exitCode":1}}`))
	row, _ = sink.BackgroundTask("shell")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestAntigravityBackgroundPatchPreservesOtherTasks(t *testing.T) {
	a, _ := testAgent(t)
	a.root.background[2] = backgroundTask{rowKey: "old", stepIndex: 2, slot: 0}
	a.root.background[4] = backgroundTask{rowKey: "other", stepIndex: 4, slot: 1}
	a.applyBackgroundTasks(a.root, &backgroundTasksPatch{TotalLength: 2}, false)
	assert.Contains(t, a.root.background, 2)
	assert.Contains(t, a.root.background, 4)
	a.applyBackgroundTasks(a.root, nil, true)
	assert.Empty(t, a.root.background)
}

func TestAntigravityInvokeResultPersistsPromptWhenChildOpensLater(t *testing.T) {
	a, sink := testAgent(t)
	a.streamCancel()
	a.root.childKeys["child"] = &childKey{role: "Researcher"}
	var step antigravityStep
	step.Type = contracts.AntigravityStepTypeInvokeSubagent
	step.Metadata.ToolCall.ID = "spawn-span"
	step.Metadata.ToolCall.ArgumentsJSON = `{"Prompt":"original child prompt"}`
	step.InvokeSubagent.Results = append(step.InvokeSubagent.Results, struct {
		ConversationID string `json:"conversationId"`
	}{ConversationID: "child"})
	a.linkSubagentResults(a.root, 0, step)
	a.ensureChildren(a.root)
	messages := transcriptMessages(sink.Child(a.cascades["child"].childAgentID))
	require.Len(t, messages, 1)
	assert.Contains(t, string(messages[0].Content), "original child prompt")
}

func TestAntigravitySendChildRevivesRow(t *testing.T) {
	a, sink := testAgent(t)
	rpc, _ := fakeAntigravityRPC(t)
	a.rpc = rpc
	child := newCascadeState("child", sink.ChildSink("child-agent"), 0, false)
	child.parent = a.root
	child.childAgentID = "child-agent"
	child.rowOpen = true
	close(child.connected)
	a.cascades["child"] = child
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "child", ChildAgentID: "child-agent", Kind: bgtask.KindSubagent, Status: bgtask.StatusSucceeded}))
	require.NoError(t, a.SendChildInput("child", "continue", nil))
	row, ok := sink.BackgroundTask("child")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}
