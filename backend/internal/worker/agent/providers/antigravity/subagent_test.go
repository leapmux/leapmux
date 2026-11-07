package antigravity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
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
	assert.NoError(t, a.endTurn(child, agent.MessageCompletionComplete))
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
	require.Empty(t, transcriptMessages(childSink))
	native := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_SYSTEM_MESSAGE","status":"CORTEX_STEP_STATUS_DONE","systemMessage":{"agentMessage":{"id":"initial","sender":"parent","content":"original child prompt","sourceMetadata":{"tool":{"toolCall":{"name":"invoke_subagent"}}}}}}`)
	require.NoError(t, a.applyUpdate(a.cascades["child"], updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{native}), false))
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
	assert.NoError(t, a.applyBackgroundTasks(a.root, &backgroundTasksPatch{
		Indices: []int{0}, BackgroundTasks: []backgroundTaskSnapshot{{TaskSnapshot: struct {
			TaskID      string `json:"taskId"`
			ToolName    string `json:"toolName"`
			ToolSummary string `json:"toolSummary"`
			Description string `json:"description"`
			StepIndex   int    `json:"stepIndex"`
			LogURI      string `json:"logUri"`
		}{TaskID: "shell", Description: "sleep 1", StepIndex: 2, LogURI: "file:///tmp/shell.log"}}}, TotalLength: 1,
	}, true))
	row, ok := sink.BackgroundTask("shell")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "/tmp/shell.log", row.Description)
	step := antigravityStep{Type: contracts.AntigravityStepTypeRunCommand, Status: contracts.AntigravityStepStatusDone}
	step.RunCommand.ExitCode = 1
	a.root.steps[2] = &stepState{}
	assert.NoError(t, a.applyStep(a.root, 2, step, json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE","runCommand":{"exitCode":1}}`)))
	row, _ = sink.BackgroundTask("shell")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestAntigravityBackgroundPatchPreservesOtherTasks(t *testing.T) {
	a, _ := testAgent(t)
	a.root.background[2] = backgroundTask{rowKey: "old", stepIndex: 2, slot: 0}
	a.root.background[4] = backgroundTask{rowKey: "other", stepIndex: 4, slot: 1}
	assert.NoError(t, a.applyBackgroundTasks(a.root, &backgroundTasksPatch{TotalLength: 2}, false))
	assert.Contains(t, a.root.background, 2)
	assert.Contains(t, a.root.background, 4)
	assert.NoError(t, a.applyBackgroundTasks(a.root, nil, true))
	assert.Empty(t, a.root.background)
}

func TestAntigravityInvokeResultUsesNativePromptWhenChildOpensLater(t *testing.T) {
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
	child := a.cascades["child"]
	native := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_SYSTEM_MESSAGE","status":"CORTEX_STEP_STATUS_DONE","systemMessage":{"agentMessage":{"id":"initial","content":"original child prompt","sourceMetadata":{"tool":{"toolCall":{"name":"invoke_subagent"}}}}}}`)
	require.NoError(t, a.applyUpdate(child, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{native}), false))
	require.NoError(t, a.applyUpdate(child, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{native}), true))
	messages := transcriptMessages(sink.Child(child.childAgentID))
	require.Len(t, messages, 1)
	assert.Contains(t, string(messages[0].Content), "original child prompt")
}

func TestAntigravitySendChildRevivesRowOnNativeTurn(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))
	require.NoError(t, a.SendChildInput(child.id, "continue", nil))
	row, ok := sink.BackgroundTask(child.id)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
	row, _ = sink.BackgroundTask(child.id)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestAntigravityBackgroundSlotReplacementAndEmptyPatch(t *testing.T) {
	a, _ := testAgent(t)
	a.root.background[2] = backgroundTask{rowKey: "old", stepIndex: 2, slot: 0}
	a.root.background[6] = backgroundTask{rowKey: "other", stepIndex: 6, slot: 1}
	var patch backgroundTasksPatch
	require.NoError(t, json.Unmarshal([]byte(`{"indices":[0],"backgroundTasks":[{"taskSnapshot":{"taskId":"new","stepIndex":4}}],"totalLength":2}`), &patch))
	require.NoError(t, a.applyBackgroundTasks(a.root, &patch, false))
	assert.NotContains(t, a.root.background, 2)
	assert.Contains(t, a.root.background, 4)
	assert.Contains(t, a.root.background, 6)
	require.NoError(t, a.applyBackgroundTasks(a.root, nil, false))
	assert.Len(t, a.root.background, 2)
	require.NoError(t, a.applyBackgroundTasks(a.root, &backgroundTasksPatch{TotalLength: 0}, false))
	assert.Empty(t, a.root.background)
}
func TestAntigravityRemovedBackgroundOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, step string
		status     bgtask.Status
	}{
		{"success", `{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE","runCommand":{"exitCode":0}}`, bgtask.StatusSucceeded},
		{"exit failure", `{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE","runCommand":{"exitCode":1}}`, bgtask.StatusFailed},
		{"error", `{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_ERROR"}`, bgtask.StatusFailed},
		{"canceled", `{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_CANCELED"}`, bgtask.StatusStopped},
		{"malformed", `{"future":true}`, bgtask.StatusEndedWithUnknownOutcome},
		{"open", `{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING"}`, bgtask.StatusEndedWithUnknownOutcome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink := testAgent(t)
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintf(w, `{"steps":[%s]}`, tc.step) })
			require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "shell", Kind: bgtask.KindShell, Status: bgtask.StatusRunning}))
			a.root.background[2] = backgroundTask{rowKey: "shell", stepIndex: 2}
			a.root.steps[2] = &stepState{status: "CORTEX_STEP_STATUS_RUNNING"}
			require.NoError(t, a.applyBackgroundTasks(a.root, nil, true))
			row, ok := sink.BackgroundTask("shell")
			require.True(t, ok)
			assert.Equal(t, tc.status, row.Status)
			assert.False(t, row.EndedAt.IsZero())
		})
	}
}
func TestAntigravityRemovedBackgroundFetchesOneSuffix(t *testing.T) {
	a, sink := testAgent(t)
	var requests atomic.Int32
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			StepOffset int `json:"stepOffset"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, 2, request.StepOffset)
		_, _ = w.Write([]byte(`{"steps":[{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE"},{"type":"CORTEX_STEP_TYPE_CHECKPOINT","status":"CORTEX_STEP_STATUS_DONE"},{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE","runCommand":{"exitCode":1}}]}`))
	})
	for slot, index := range []int{2, 4} {
		key := fmt.Sprintf("shell-%d", index)
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindShell, Status: bgtask.StatusRunning}))
		a.root.background[index] = backgroundTask{rowKey: key, stepIndex: index, slot: slot}
		a.root.steps[index] = &stepState{status: "CORTEX_STEP_STATUS_RUNNING"}
	}
	require.NoError(t, a.applyBackgroundTasks(a.root, nil, true))
	assert.Equal(t, int32(1), requests.Load())
	first, _ := sink.BackgroundTask("shell-2")
	last, _ := sink.BackgroundTask("shell-4")
	assert.Equal(t, bgtask.StatusSucceeded, first.Status)
	assert.Equal(t, bgtask.StatusFailed, last.Status)
}

func TestAntigravityBackgroundIdentitySurvivesStepIndexChange(t *testing.T) {
	a, sink := testAgent(t)
	var requests atomic.Int32
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"steps":[{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE"}]}`))
	})
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "same-shell", Kind: bgtask.KindShell, Status: bgtask.StatusRunning}))
	a.root.background[2] = backgroundTask{rowKey: "same-shell", stepIndex: 2, slot: 0}
	a.root.steps[2] = &stepState{status: "CORTEX_STEP_STATUS_RUNNING"}
	var patch backgroundTasksPatch
	require.NoError(t, json.Unmarshal([]byte(`{"indices":[0],"totalLength":1,"backgroundTasks":[{"taskSnapshot":{"taskId":"same-shell","stepIndex":4}}]}`), &patch))
	require.NoError(t, a.applyBackgroundTasks(a.root, &patch, false))
	assert.Equal(t, int32(0), requests.Load())
	row, ok := sink.BackgroundTask("same-shell")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.True(t, row.EndedAt.IsZero())
	assert.NotContains(t, a.root.background, 2)
	assert.Contains(t, a.root.background, 4)
}
