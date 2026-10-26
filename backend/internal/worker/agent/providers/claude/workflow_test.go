package claude

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The workflow fixtures quote the frames of the Claude Code 2.1.310 probe
// (.tmp/claude-wf/frames-clwf.jsonl): a `local_workflow` task_started, full
// task_progress snapshots of its agents, and the closing task_notification.

const claudeWorkflowStartFrame = `{"type":"system","subtype":"task_started","task_id":"wf-1","tool_use_id":"workflow-probe",` +
	`"task_type":"local_workflow","workflow_name":"leapmux-e2e-probe","description":"Run one local probe"}`

const claudeWorkflowProgressStart = `{"type":"system","subtype":"task_progress","task_id":"wf-1","description":"Probe: probe",` +
	`"workflow_progress":[{"type":"workflow_phase","index":1,"title":"Probe"},` +
	`{"type":"workflow_agent","index":1,"label":"probe","phaseTitle":"Probe","agentId":"ac590a535ee34c6f5",` +
	`"state":"start","promptPreview":"Reply with WORKFLOW_CHILD_OK.","promptFramed":true}]}`

const claudeWorkflowProgressDone = `{"type":"system","subtype":"task_progress","task_id":"wf-1","description":"Probe: probe",` +
	`"workflow_progress":[{"type":"workflow_phase","index":1,"title":"Probe"},` +
	`{"type":"workflow_agent","index":1,"label":"probe","phaseTitle":"Probe","agentId":"ac590a535ee34c6f5",` +
	`"state":"done","promptPreview":"Reply with WORKFLOW_CHILD_OK.","resultPreview":"WORKFLOW_CHILD_OK"}]}`

const claudeWorkflowNotificationFrame = `{"type":"system","subtype":"task_notification","task_id":"wf-1",` +
	`"tool_use_id":"workflow-probe","status":"completed","summary":"Dynamic workflow \"Run one local probe\" completed"}`

// The first snapshot of a run draws one agent row in the run's group.
func TestClaudeWorkflowProgressDrawsAgentRowInRunGroup(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 2)
	run, agentRow := tasks[0], tasks[1]
	require.Equal(t, "wf-1", run.RowKey)
	require.Equal(t, "wf-1/wf/1", agentRow.RowKey)
	assert.Equal(t, bgtask.KindWorkflow, run.Kind)
	assert.Equal(t, bgtask.KindSubagent, agentRow.Kind)
	assert.Equal(t, "workflow:leapmux-e2e-probe", agentRow.GroupKey)
	assert.Equal(t, "leapmux-e2e-probe", agentRow.GroupLabel)
	assert.Equal(t, run.GroupKey, agentRow.GroupKey, "the agent row joins the run row's group")
	assert.Equal(t, "probe", agentRow.Title)
	assert.Equal(t, "Reply with WORKFLOW_CHILD_OK.", agentRow.Description)
	assert.Equal(t, bgtask.StatusRunning, agentRow.Status)
	assert.Equal(t, bgtask.StatusRunning, run.Status)
	assert.Empty(t, agentRow.ChildAgentID, "a workflow agent owns no transcript")
}

// A snapshot that settles an agent ends its row with its own outcome, and a
// repeated unchanged snapshot writes nothing new.
func TestClaudeWorkflowProgressSettlesAgentRow(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	a.HandleOutput([]byte(claudeWorkflowProgressDone))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 2)
	agentRow := tasks[1]
	assert.Equal(t, bgtask.StatusSucceeded, agentRow.Status)
	assert.Equal(t, "WORKFLOW_CHILD_OK", agentRow.Description)
	assert.False(t, agentRow.EndedAt.IsZero(), "a final snapshot closes the agent row")
}

// The run's notification ends every agent row that a snapshot did not settle.
func TestClaudeWorkflowNotificationClosesUnsettledAgents(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	a.HandleOutput([]byte(claudeWorkflowNotificationFrame))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 2)
	agentRow := tasks[1]
	assert.Equal(t, bgtask.StatusSucceeded, agentRow.Status, "the run's outcome ends the unsettled agent")
	assert.False(t, agentRow.EndedAt.IsZero())
}

// A progress snapshot without a task_started draws nothing: no workflow name,
// no group to join.
func TestClaudeWorkflowProgressWithoutStartDrawsNothing(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowProgressStart))
	assert.Empty(t, sink.BackgroundTasks())
}

// A progress frame of a non-workflow task carries no workflow snapshot, so it
// draws no agent rows.
func TestClaudeWorkflowProgressOfSubagentDrawsNothing(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"local_agent","description":"Reviewer"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"task-1","description":"Reviewing","last_tool_name":"Grep"}`))
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "task-1", tasks[0].RowKey)
}

// A run of two agents keeps one row per agent, keyed by the stable index.
func TestClaudeWorkflowTwoAgentsDrawTwoRows(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowStartFrame))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"wf-1","workflow_progress":[` +
		`{"type":"workflow_agent","index":1,"label":"first","state":"start","promptPreview":"First prompt."},` +
		`{"type":"workflow_agent","index":2,"label":"second","state":"start","promptPreview":"Second prompt."}]}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_progress","task_id":"wf-1","workflow_progress":[` +
		`{"type":"workflow_agent","index":1,"label":"first","state":"done","resultPreview":"ONE"},` +
		`{"type":"workflow_agent","index":2,"label":"second","state":"error","error":"API Error: 500"}]}`))
	a.HandleOutput([]byte(claudeWorkflowNotificationFrame))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 3)
	first, second := tasks[1], tasks[2]
	require.Equal(t, "wf-1/wf/1", first.RowKey)
	require.Equal(t, "wf-1/wf/2", second.RowKey)
	assert.Equal(t, bgtask.StatusSucceeded, first.Status)
	assert.Equal(t, "ONE", first.Description)
	assert.Equal(t, bgtask.StatusFailed, second.Status)
	assert.Equal(t, "API Error: 500", second.Description)
	assert.False(t, first.EndedAt.IsZero())
	assert.False(t, second.EndedAt.IsZero())
}

// A notification without a start forgets nothing and fails nothing.
func TestClaudeWorkflowNotificationWithoutStartIsNoop(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(claudeWorkflowNotificationFrame))
	assert.Empty(t, sink.BackgroundTasks())
}

// An agent without a label takes the head of its prompt as the title.
func TestClaudeWorkflowUnlabelledAgentUsesPromptTitle(t *testing.T) {
	t.Parallel()

	w := &claudeWorkflowRuns{}
	w.started("wf-1", "workflow:run", "run")
	upserts := w.progress("wf-1", []claudeWorkflowProgressEntry{{
		Type: "workflow_agent", Index: 3, State: "start",
		PromptPreview: "Inspect the parser.\nSecond line.",
	}})
	require.Len(t, upserts, 1)
	assert.Equal(t, "Inspect the parser.", upserts[0].Title)
	assert.Equal(t, "wf-1/wf/3", upserts[0].RowKey)
}

// A repeated task_started keeps the recorded group, and a settled agent row
// keeps its own outcome when the run ends.
func TestClaudeWorkflowRunsStartedIsIdempotent(t *testing.T) {
	t.Parallel()

	w := &claudeWorkflowRuns{}
	w.started("wf-1", "workflow:one", "one")
	w.started("wf-1", "workflow:two", "two")
	upserts := w.progress("wf-1", []claudeWorkflowProgressEntry{
		{Type: "workflow_agent", Index: 1, Label: "a", State: "done", ResultPreview: "ok"},
	})
	require.Len(t, upserts, 1)
	assert.Equal(t, "workflow:one", upserts[0].GroupKey)

	closing := w.finished("wf-1", bgtask.StatusStopped)
	assert.Empty(t, closing, "a settled agent row keeps its own outcome")
	assert.Nil(t, w.runs["wf-1"], "finished forgets the run")
}
