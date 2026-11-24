package antigravity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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
	a.root.background["old"] = backgroundTask{rowKey: "old", stepIndex: 2, slot: 0}
	a.root.background["other"] = backgroundTask{rowKey: "other", stepIndex: 4, slot: 1}
	assert.NoError(t, a.applyBackgroundTasks(a.root, &backgroundTasksPatch{TotalLength: 2}, false))
	assert.Contains(t, a.root.background, "old")
	assert.Contains(t, a.root.background, "other")
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
	require.NoError(t, agenttest.SendChildInput(a, child.id, agent.ChildInput{Content: "continue"}))
	row, ok := sink.BackgroundTask(child.id)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
	row, _ = sink.BackgroundTask(child.id)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestAntigravityBackgroundSlotReplacementAndEmptyPatch(t *testing.T) {
	a, _ := testAgent(t)
	a.root.background["old"] = backgroundTask{rowKey: "old", stepIndex: 2, slot: 0}
	a.root.background["other"] = backgroundTask{rowKey: "other", stepIndex: 6, slot: 1}
	var patch backgroundTasksPatch
	require.NoError(t, json.Unmarshal([]byte(`{"indices":[0],"backgroundTasks":[{"taskSnapshot":{"taskId":"new","stepIndex":4}}],"totalLength":2}`), &patch))
	require.NoError(t, a.applyBackgroundTasks(a.root, &patch, false))
	assert.NotContains(t, a.root.background, "old")
	assert.Contains(t, a.root.background, "new")
	assert.Contains(t, a.root.background, "other")
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
			a.root.background["shell"] = backgroundTask{rowKey: "shell", stepIndex: 2}
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
		a.root.background[key] = backgroundTask{rowKey: key, stepIndex: index, slot: slot}
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
	a.root.background["same-shell"] = backgroundTask{rowKey: "same-shell", stepIndex: 2, slot: 0}
	a.root.steps[2] = &stepState{status: "CORTEX_STEP_STATUS_RUNNING"}
	var patch backgroundTasksPatch
	require.NoError(t, json.Unmarshal([]byte(`{"indices":[0],"totalLength":1,"backgroundTasks":[{"taskSnapshot":{"taskId":"same-shell","stepIndex":4}}]}`), &patch))
	require.NoError(t, a.applyBackgroundTasks(a.root, &patch, false))
	assert.Equal(t, int32(0), requests.Load())
	row, ok := sink.BackgroundTask("same-shell")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.True(t, row.EndedAt.IsZero())
	assert.Contains(t, a.root.background, "same-shell")
	assert.Equal(t, 4, a.root.background["same-shell"].stepIndex)
}

func TestAntigravityAlreadyIdleFirstChildUpdateClosesRow(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interrupted bool
		steps       []json.RawMessage
		wantStatus  bgtask.Status
		wantError   bool
	}{
		{
			name:       "succeeded without steps",
			wantStatus: bgtask.StatusSucceeded,
		},
		{
			name:       "succeeded with done step",
			steps:      []json.RawMessage{plannerStep(contracts.AntigravityStepStatusDone, "already finished")},
			wantStatus: bgtask.StatusSucceeded,
		},
		{
			name:       "failed with held error",
			steps:      []json.RawMessage{json.RawMessage(`{"type":"CORTEX_STEP_TYPE_ERROR_MESSAGE","status":"CORTEX_STEP_STATUS_DONE","errorMessage":{"error":{"userErrorMessage":"child exploded"}}}`)},
			wantStatus: bgtask.StatusFailed,
			wantError:  true,
		},
		{
			name:        "interrupted",
			interrupted: true,
			wantStatus:  bgtask.StatusStopped,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink, child := childLifecycleTestAgent(t)
			if tc.interrupted {
				child.interrupted = true
			}
			var indices []int
			for i := range tc.steps {
				indices = append(indices, i)
			}
			update := updateWithSteps(cascadeStatusIdle, indices, tc.steps)
			require.NoError(t, a.applyUpdate(child, update, true))
			row, ok := sink.BackgroundTask(child.id)
			require.True(t, ok)
			assert.Equal(t, tc.wantStatus, row.Status)
			assert.False(t, child.rowOpen)
			if tc.wantError {
				childSink := sink.Child(child.childAgentID)
				require.NotEmpty(t, childSink.PersistedNotifications())
				assert.Contains(t, string(childSink.PersistedNotifications()[0].Content), "child exploded")
			}
		})
	}
}

func TestAntigravityPostTurnChildDeltaDoesNotReviveFinishedRow(t *testing.T) {
	for _, status := range []string{"", cascadeStatusIdle} {
		name := status
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			a, sink, child := childLifecycleTestAgent(t)
			require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusRunning}, false))
			require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))
			row, ok := sink.BackgroundTask(child.id)
			require.True(t, ok)
			require.Equal(t, bgtask.StatusSucceeded, row.Status)
			beforeRevives := len(sink.RevivedTasks())

			delta := updateWithSteps(status, []int{0}, []json.RawMessage{plannerStep(contracts.AntigravityStepStatusDone, "late child note")})
			require.NoError(t, a.applyUpdate(child, delta, false))

			assert.Len(t, sink.RevivedTasks(), beforeRevives, "post-turn child delta must not call ReviveBackgroundTask")
			row, _ = sink.BackgroundTask(child.id)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			assert.False(t, child.rowOpen)
			messages := transcriptMessages(sink.Child(child.childAgentID))
			require.Len(t, messages, 1)
			assert.Contains(t, string(messages[0].Content), "late child note")
		})
	}
}

func TestAntigravitySharedStepIndexBackgroundTasksSettleIndependently(t *testing.T) {
	a, sink := testAgent(t)
	var openPatch backgroundTasksPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"indices": [0, 1],
		"totalLength": 2,
		"backgroundTasks": [
			{"taskSnapshot": {"taskId": "task-a", "description": "cmd a", "stepIndex": 0}},
			{"taskSnapshot": {"taskId": "task-b", "description": "cmd b", "stepIndex": 0}}
		]
	}`), &openPatch))
	require.NoError(t, a.applyBackgroundTasks(a.root, &openPatch, true))
	rowA, okA := sink.BackgroundTask("task-a")
	rowB, okB := sink.BackgroundTask("task-b")
	require.True(t, okA)
	require.True(t, okB)
	assert.Equal(t, bgtask.StatusRunning, rowA.Status)
	assert.Equal(t, bgtask.StatusRunning, rowB.Status)

	// Remove task-a while task-b remains on the same stepIndex.
	var removeAPatch backgroundTasksPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"indices": [0],
		"totalLength": 1,
		"backgroundTasks": [
			{"taskSnapshot": {"taskId": "task-b", "description": "cmd b", "stepIndex": 0}}
		]
	}`), &removeAPatch))
	require.NoError(t, a.applyBackgroundTasks(a.root, &removeAPatch, false))
	rowA, _ = sink.BackgroundTask("task-a")
	rowB, _ = sink.BackgroundTask("task-b")
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, rowA.Status)
	assert.Equal(t, bgtask.StatusRunning, rowB.Status)

	// Two remaining tasks sharing stepIndex 2 both close when step 2 finishes.
	var pairPatch backgroundTasksPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"indices": [0, 1],
		"totalLength": 2,
		"backgroundTasks": [
			{"taskSnapshot": {"taskId": "task-c", "description": "cmd c", "stepIndex": 2}},
			{"taskSnapshot": {"taskId": "task-d", "description": "cmd d", "stepIndex": 2}}
		]
	}`), &pairPatch))
	require.NoError(t, a.applyBackgroundTasks(a.root, &pairPatch, true))
	step := antigravityStep{Type: contracts.AntigravityStepTypeRunCommand, Status: contracts.AntigravityStepStatusDone}
	a.root.steps[2] = &stepState{}
	require.NoError(t, a.applyStep(a.root, 2, step, json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE"}`)))
	rowC, _ := sink.BackgroundTask("task-c")
	rowD, _ := sink.BackgroundTask("task-d")
	assert.Equal(t, bgtask.StatusSucceeded, rowC.Status)
	assert.Equal(t, bgtask.StatusSucceeded, rowD.Status)

	// Empty taskId entries are skipped without polluting state.background.
	var emptyIDPatch backgroundTasksPatch
	require.NoError(t, json.Unmarshal([]byte(`{
		"indices": [0, 1],
		"totalLength": 2,
		"backgroundTasks": [
			{"taskSnapshot": {"taskId": "", "description": "blank", "stepIndex": 5}},
			{"taskSnapshot": {"taskId": "task-e", "description": "cmd e", "stepIndex": 5}}
		]
	}`), &emptyIDPatch))
	require.NoError(t, a.applyBackgroundTasks(a.root, &emptyIDPatch, true))
	assert.NotContains(t, a.root.background, "")
	assert.Contains(t, a.root.background, "task-e")
}

func TestAntigravityChildOpenStepWithEmptyStatusFlushesOnIdle(t *testing.T) {
	a, sink, child := childLifecycleTestAgent(t)
	openStep := plannerStep(contracts.AntigravityStepStatusRunning, "partial child answer")
	require.NoError(t, a.applyUpdate(child, updateWithSteps("", []int{0}, []json.RawMessage{openStep}), false))
	require.NoError(t, a.applyUpdate(child, agentStateUpdate{Status: cascadeStatusIdle}, false))

	messages := transcriptMessages(sink.Child(child.childAgentID))
	require.Len(t, messages, 1, "open step added during empty-status update must flush when cascade turns idle")
	assert.Contains(t, string(messages[0].Content), "partial child answer")
	row, ok := sink.BackgroundTask(child.id)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestAntigravityBackgroundForStepOrderIsDeterministic(t *testing.T) {
	a, _ := testAgent(t)
	a.root.background["task-c"] = backgroundTask{rowKey: "task-c", stepIndex: 2, slot: 2}
	a.root.background["task-a"] = backgroundTask{rowKey: "task-a", stepIndex: 2, slot: 0}
	a.root.background["task-b"] = backgroundTask{rowKey: "task-b", stepIndex: 2, slot: 1}
	for range 20 {
		got := a.backgroundForStep(a.root, 2)
		require.Len(t, got, 3)
		assert.Equal(t, []string{"task-a", "task-b", "task-c"}, []string{got[0].rowKey, got[1].rowKey, got[2].rowKey})
	}
}
