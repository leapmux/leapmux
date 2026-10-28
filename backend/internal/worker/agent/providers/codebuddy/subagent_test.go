package codebuddy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestCodebuddyTaskIndexKeepsEachSpawnRouteTogether(t *testing.T) {
	t.Parallel()
	var index codebuddyTaskIndex
	check := func(spawn, row, child string) {
		t.Helper()
		actualRow, actualChild := index.child(spawn)
		assert.Equal(t, row, actualRow)
		assert.Equal(t, child, actualChild)
	}
	check("unknown", "", "")
	index.rememberChild("spawn", "old-row", "")
	check("spawn", "old-row", "")
	index.rememberChild("spawn", "old-row", "child")
	check("spawn", "old-row", "child")
	index.rememberChild("spawn", "current-row", "child")
	index.rememberChild("alias", "current-row", "child")
	index.rememberChild("other", "other-row", "other-child")
	index.forgetRow("old-row")
	check("spawn", "current-row", "child")
	check("alias", "current-row", "child")
	index.forgetRow("current-row")
	check("spawn", "", "")
	check("alias", "", "")
	check("other", "other-row", "other-child")
}

func TestCodebuddyWorkflowTaskGroupsItsChildAgent(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"workflow-1","tool_use_id":"run-workflow","task_type":"local_workflow","workflow_name":"Probe workflow","description":"Probe workflow"}`))

	workflow, ok := sink.BackgroundTask("workflow-1")
	require.True(t, ok, "a native local_workflow must open a registry row")
	assert.Equal(t, bgtask.KindWorkflow, workflow.Kind)
	assert.Equal(t, bgtask.StatusRunning, workflow.Status)
	assert.Equal(t, "workflow-1", workflow.GroupKey)
	assert.Equal(t, "Probe workflow", workflow.GroupLabel)

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"workflow-1","workflow_progress":[{"type":"workflow_phase","index":1,"title":"Probe"},{"type":"workflow_agent","index":1,"agentId":"v2:child-1","state":"start","label":"Probe child","phaseTitle":"Probe","phaseIndex":1}]}`))
	child, ok := sink.BackgroundTask("v2:child-1")
	require.True(t, ok, "a workflow_agent progress entry must open its child row")
	assert.Equal(t, bgtask.KindSubagent, child.Kind)
	assert.Empty(t, child.ChildAgentID, "the native journal must prove the child transcript before this row opens a tab")
	assert.Equal(t, "workflow-1", child.GroupKey)
	assert.Equal(t, "Probe workflow", child.GroupLabel)
	assert.Equal(t, bgtask.StatusRunning, child.Status)

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"workflow-1","status":"completed","summary":"The child answered."}`))
	workflow, ok = sink.BackgroundTask("workflow-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, workflow.Status)
}

func TestCodebuddyAgentTaskLinksAndClosesItsChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"child-task","tool_use_id":"spawn-1","task_type":"local_agent","description":"Inspect files","prompt":"Find the owner."}`))

	child, ok := sink.BackgroundTask("child-task")
	require.True(t, ok, "an ordinary Agent tool must open a child row")
	assert.Equal(t, bgtask.KindSubagent, child.Kind)
	assert.NotEmpty(t, child.ChildAgentID)
	assert.Equal(t, "Inspect files", child.Title)
	assert.Equal(t, bgtask.StatusRunning, child.Status)

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"child-task","status":"completed","summary":"Found the owner."}`))
	child, ok = sink.BackgroundTask("child-task")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, child.Status)
}

func TestCodebuddyNativeStreamTaskKinds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		native string
		kind   bgtask.Kind
	}{
		{native: "Agent", kind: bgtask.KindSubagent},
		{native: "Bash", kind: bgtask.KindShell},
		{native: "PowerShell", kind: bgtask.KindShell},
		{native: "Workflow", kind: bgtask.KindWorkflow},
	} {
		t.Run(tc.native, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			frame, err := json.Marshal(map[string]any{"type": "system", "subtype": "task_started", "task_id": "native-task", "tool_use_id": "native-spawn", "task_type": tc.native, "description": "Native task"})
			require.NoError(t, err)
			a.HandleOutput(frame)
			row, exists := sink.BackgroundTask("native-task")
			require.True(t, exists)
			assert.Equal(t, tc.kind, row.Kind)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
		})
	}
}

func TestCodebuddyNativeBackgroundAgentAckKeepsItsRunningChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"native-spawn","name":"Agent","input":{"name":"native-mate","description":"Native teammate","prompt":"Do the native task.","run_in_background":true}}]}}`))
	before, exists := sink.BackgroundTask("native-spawn")
	require.True(t, exists)
	require.NotEmpty(t, before.ChildAgentID)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"agent-native","tool_use_id":"native-spawn","task_type":"Agent","description":"Native teammate"}`))
	a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"native-spawn","content":[{"type":"text","text":"Spawned successfully.\nagent_id: native-mate@_auto_session-1\nname: native-mate\nteam_name: _auto_session-1\ntask_id: agent-native\nThe agent is now running and will receive instructions via mailbox."}],"is_error":false}]}}`))
	row, exists := sink.BackgroundTask("agent-native")
	require.True(t, exists)
	assert.Equal(t, before.ChildAgentID, row.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"native-spawn","message":{"role":"assistant","content":[{"type":"text","text":"Native teammate output"}]}}`))
	messages := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[1].Content), "Native teammate output")
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"agent-native","status":"killed","summary":"Native task stopped"}`))
	row, exists = sink.BackgroundTask("agent-native")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

func TestCodebuddyForegroundAgentRoutesItsForwardedChildTranscript(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"spawn-1","name":"Agent","input":{"description":"Probe child","prompt":"Reply with CHILD_OUTPUT."}}]}}`))

	child, ok := sink.BackgroundTask("spawn-1")
	require.True(t, ok, "a foreground Agent call must open a child row before any task_started")
	require.NotEmpty(t, child.ChildAgentID)
	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"spawn-1","message":{"role":"assistant","content":[{"type":"text","text":"CHILD_OUTPUT"}]}}`))

	messages := sink.Child(child.ChildAgentID).Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[0].Content), "Reply with CHILD_OUTPUT.")
	assert.Contains(t, string(messages[1].Content), "CHILD_OUTPUT")
	assert.Equal(t, 1, sink.MessageCount(), "the forwarded child answer stays out of the parent transcript")

	a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"spawn-1","content":"CHILD_OUTPUT"}]}}`))
	child, ok = sink.BackgroundTask("spawn-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, child.Status)
}
