package codebuddy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

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
	assert.Equal(t, bgtask.StatusCompleted, workflow.Status)
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
	assert.Equal(t, bgtask.StatusCompleted, child.Status)
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
	assert.Equal(t, bgtask.StatusCompleted, child.Status)
}
