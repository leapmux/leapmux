package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestClaudeSubagentHandbackPersistsOneSharedReportInBothTranscripts(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newTestAgent(agentapi.NewProviderServices(sink))
	agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"local_agent","description":"Parser reviewer","prompt":"Inspect the parser."}`))
	agent.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"handback-1","name":"SubagentHandback","input":{"message":"**Parser report**\n\n- Finding"}}]},"parent_tool_use_id":"spawn-1","task_description":"Parser reviewer"}`))
	agent.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"handback-1","type":"tool_result","content":[{"type":"text","text":"{\"success\":true,\"message\":\"Report delivered to your caller.\"}"}]}]},"parent_tool_use_id":"spawn-1"}`))
	agent.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"handback-2","name":"SubagentHandback","input":{"message":"duplicate report"}}]},"parent_tool_use_id":"spawn-1","task_description":"Parser reviewer"}`))
	agent.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"handback-2","type":"tool_result","content":[{"type":"text","text":"{\"success\":false,\"message\":\"already delivered\"}"}]}]},"parent_tool_use_id":"spawn-1"}`))
	assert.Empty(t, sink.LeapMuxNotifications(), "the child call alone does not prove that Claude delivered the report to the parent")
	child := sink.Child("child-of-spawn-1")
	require.Len(t, child.LeapMuxNotifications(), 1, "the child transcript records the report where the child made it")
	agent.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"spawn-1","type":"tool_result","content":[{"type":"text","text":"report delivered separately"}]}]},"tool_use_result":{"status":"completed","agentId":"task-1","handback":"send","content":[{"type":"text","text":"report delivered separately"}]}}`))

	parentReports := sink.LeapMuxNotifications()
	require.Len(t, parentReports, 1)
	assert.Equal(t, "subagent_report", parentReports[0]["type"])
	assert.Equal(t, "**Parser report**\n\n- Finding", parentReports[0]["text"])
	assert.Equal(t, "Parser reviewer", parentReports[0]["label"])

	childReports := child.LeapMuxNotifications()
	require.Len(t, childReports, 1)
	assert.Equal(t, "subagent_report", childReports[0]["type"])
	assert.Equal(t, "**Parser report**\n\n- Finding", childReports[0]["text"])
	assert.Equal(t, "Parser reviewer", childReports[0]["label"])
	assert.NotContains(t, childReports[0], "status")
	assert.Equal(t, "send", parentReports[0]["status"])
	require.Len(t, child.Messages(), 1, "hand-back calls and acknowledgements must not render as tool rows")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, child.Messages()[0].Source)
}

func TestClaudeSubagentHandbackRespectsTheDeliveryOutcome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		outcome      string
		wantInParent bool
	}{
		{outcome: "flagged", wantInParent: true},
		{outcome: "withheld", wantInParent: false},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			sink := &agenttest.Sink{}
			agent := newTestAgent(agentapi.NewProviderServices(sink))
			agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))
			agent.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"handback-1","name":"SubagentHandback","input":{"message":"Report"}}]},"parent_tool_use_id":"spawn-1","task_description":"Reviewer"}`))
			agent.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"handback-1","type":"tool_result","content":"done"}]},"parent_tool_use_id":"spawn-1"}`))
			agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"spawn-1","type":"tool_result","content":"done"}]},"tool_use_result":{"handback":%q}}`, tc.outcome)))

			child := sink.Child("child-of-spawn-1")
			require.Len(t, child.LeapMuxNotifications(), 1)
			assert.NotContains(t, child.LeapMuxNotifications()[0], "status")
			if tc.wantInParent {
				require.Len(t, sink.LeapMuxNotifications(), 1)
				assert.Equal(t, tc.outcome, sink.LeapMuxNotifications()[0]["status"])
			} else {
				assert.Empty(t, sink.LeapMuxNotifications())
			}
		})
	}
}

func TestClaudeBackgroundSubagentHandbackUsesThePeerResultAndDropsTheChildEcho(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newTestAgent(agentapi.NewProviderServices(sink))
	agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))
	agent.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"handback-1","name":"SubagentHandback","input":{"message":"**Background report**\n\n- Finding"}}]},"parent_tool_use_id":"spawn-1","task_description":"Reviewer"}`))
	agent.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"handback-1","type":"tool_result","content":"done"}]},"parent_tool_use_id":"spawn-1"}`))
	agent.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"**Background report**\n\n- Finding"}]},"parent_tool_use_id":"spawn-1","task_description":"Reviewer"}`))
	peerResult := `{"type":"result","subtype":"success","result":"done","origin":{"kind":"peer","from":"task-1","senderTaskId":"task-1","body":"[Subagent hand-back] The report follows:\n  **Background report**\n  \n  - Finding","handback":true}}`
	agent.HandleOutput([]byte(peerResult))

	child := sink.Child("child-of-spawn-1")
	require.Len(t, child.Messages(), 1, "the child echo must not duplicate the shared report")
	require.Len(t, child.LeapMuxNotifications(), 1)

	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "Reviewer", reports[0]["label"])
	assert.Equal(t, "**Background report**\n\n- Finding", reports[0]["text"])
	assert.Equal(t, "send", reports[0]["status"])
	assert.Zero(t, sink.MessageCount(), "the peer result is a report carrier, not a turn-end divider")
}

func TestClaudePeerHandbackSurvivesMissingChildState(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newTestAgent(agentapi.NewProviderServices(sink))
	agent.HandleOutput([]byte(`{"type":"result","subtype":"success","result":"done","origin":{"kind":"peer","from":"orphan-reviewer","senderTaskId":"unknown-task","body":"SECURITY WARNING: review carefully\n[Subagent hand-back] The report follows:\n  **Recovered report**\n  \n  - Finding","handback":true,"flagged":true}}`))

	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "orphan-reviewer", reports[0]["label"])
	assert.Equal(t, "**Recovered report**\n\n- Finding", reports[0]["text"])
	assert.Equal(t, "flagged", reports[0]["status"])
	assert.Zero(t, sink.MessageCount())
}

func TestClaudePeerHandbackUsesEventIdentityAcrossRestarts(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newTestAgent(agentapi.NewProviderServices(sink))
	agent.HandleOutput([]byte(`{"type":"result","uuid":"report-1","origin":{"kind":"peer","from":"orphan-reviewer","senderTaskId":"unknown-task","body":"[Subagent hand-back] The report follows:\n  Draft report","handback":true}}`))
	agent.HandleOutput([]byte(`{"type":"result","uuid":"report-2","origin":{"kind":"peer","from":"orphan-reviewer","senderTaskId":"unknown-task","body":"[Subagent hand-back] The report follows:\n  Corrected report","handback":true}}`))

	assert.Len(t, sink.LeapMuxNotifications(), 2, "a restarted peer task must keep its new report")
}

func TestClaudePeerHandbackReplayKeepsOneEventIdentity(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newTestAgent(agentapi.NewProviderServices(sink))
	line := []byte(`{"type":"result","uuid":"report-1","origin":{"kind":"peer","from":"reviewer","senderTaskId":"task-1","body":"[Subagent hand-back] The report follows:\n  Report","handback":true}}`)
	agent.HandleOutput(line)
	agent.HandleOutput(line)

	assert.Len(t, sink.LeapMuxNotifications(), 1, "one provider event must stay idempotent across replay")
}

// TestClaude_PendingTaskEndRecordsAndConsumes verifies the pending result map for a Task whose final result precedes task_started.
// recordPendingTaskEnd indexes the final status by the spawning tool-use ID.
// startTask consumes it under the task index's i.mu mutex when handleClaudeTaskStarted processes the late start.
// This test directly exercises storage and consumption so the reordered result cannot leave a Running row.
func TestClaude_PendingTaskEndRecordsAndConsumes(t *testing.T) {
	t.Parallel()
	var index claudeTaskIndex

	// A final result for spawn span "tu-1" arrives before task_started.
	index.recordPendingTaskEnd("tu-1", bgtask.StatusSucceeded)

	got, ok := index.startTask("task-1", bgtask.KindSubagent, "tu-1", "tu-1")
	assert.True(t, ok, "pending end taken on the late task_started")
	assert.Equal(t, bgtask.StatusSucceeded, got)
	_, ok = index.startTask("task-1", bgtask.KindSubagent, "tu-1", "tu-1")
	assert.False(t, ok, "entry consumed so it cannot fire twice")
}

// TestClaude_PendingTaskEndIgnoresEmptySpan verifies a result with no
// parent_tool_use_id (a non-forwarded envelope) does not seed a pending end
// that could never be matched by a task_started.
func TestClaude_PendingTaskEndIgnoresEmptySpan(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.tasks.recordPendingTaskEnd("", bgtask.StatusSucceeded)
	assert.Nil(t, a.tasks.runs.pendingEnd, "no entry recorded for an empty spawn span")
}

// Claude supports stopping one child through the stop_task control_request but provides no native child-input route.
// Assert both capability results here and through agenttest.AssertChildCapabilities.
func TestClaude_InterruptChildWithoutChildSteering(t *testing.T) {
	t.Parallel()

	assert.False(t, claudeProvider{}.ChildCapabilities(nil).AcceptsMessages,
		"Claude Code exposes no wire path that sends input to a subagent")
	_, sendsDirectInput := any(&Agent{}).(agentapi.ChildSteerer)
	assert.False(t, sendsDirectInput, "the agent must not implement ChildSteerer")
	_, interruptsChild := any(&Agent{}).(agentapi.ChildInterrupter)
	assert.True(t, interruptsChild, "the CLI's stop_task control_request stops one subagent alone")
}

// InterruptChild uses the registry row key, which equals Claude task_id, through this process's task index.
// A missing entry returns ErrChildRouteNotReady, which the handler can retry.
// Each of these absent routes must return the same sentinel:
//   - A previous process's task ID.
//   - An empty key.
//   - An early row still identified by its spawn span.
//   - A completed run whose notification removes its index entry.
func TestClaude_InterruptChildWithoutALiveTaskReturnsRetryable(t *testing.T) {
	t.Parallel()

	a := newTestAgent(agentapi.NewProviderServices(&agenttest.Sink{}))

	require.ErrorIs(t, a.InterruptChild("task-unknown", agentapi.StopContext{}), agentapi.ErrChildRouteNotReady)
	require.ErrorIs(t, a.InterruptChild("", agentapi.StopContext{}), agentapi.ErrChildRouteNotReady)
	require.ErrorIs(t, a.InterruptChild("prestart:spawn-1", agentapi.StopContext{}), agentapi.ErrChildRouteNotReady)

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))
	assert.True(t, a.tasks.knowsTask("task-1"), "a task_started of this process registers the route")
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-1","status":"completed"}`))
	require.ErrorIs(t, a.InterruptChild("task-1", agentapi.StopContext{}), agentapi.ErrChildRouteNotReady,
		"a finished run has nothing to stop, and its row key alone is no route")
}

// A forwarded child envelope can precede task_started, as a final result can precede the start through recordPendingTaskEnd.
// Before this correction, the missing task ID meant no child registry row.
// The tab therefore appeared idle with no thinking indicator while its transcript received live output.
func TestClaude_AForwardedEnvelopeBeforeTaskStartedOpensARunningRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"role": "assistant", "content": [{"type": "text", "text": "Working."}]}
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "the reordered envelope opens the row the task id could not")
	assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status,
		"the envelope that got here IS the subagent working")
	assert.Equal(t, "child-of-tu-spawn", tasks[0].ChildAgentID,
		"linked, or the child's own tab cannot find its run")
}

// The late task_started renames the early row to the task ID.
// Retain one row instead of creating another that orphans the first and counts the child twice.
func TestClaude_TheLateTaskStartedRenamesTheReorderedRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"role": "assistant", "content": [{"type": "text", "text": "Working."}]}
	}`))
	require.Len(t, sink.BackgroundTasks(), 1)

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "SCAN triage angle"
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "one run, one row")
	assert.Equal(t, "task-1", tasks[0].RowKey)
	assert.Equal(t, "SCAN triage angle", tasks[0].Title, "the real title lands on the renamed row")
	assert.Equal(t, "child-of-tu-spawn", tasks[0].ChildAgentID, "and it keeps its child")
}

// The ordinary order must be untouched: task_started first opens exactly one row
// under the task id, and its unconditional rename finds nothing to move.
func TestClaude_TaskStartedFirstOpensOneRowUnderTheTaskID(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "SCAN triage angle"
	}`))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"role": "assistant", "content": [{"type": "text", "text": "Working."}]}
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "task-1", tasks[0].RowKey)
}

// The child tab must open on its instruction instead of its first reply.
// In this test, task_started supplies the spawn prompt before forwarded output, so the prompt becomes the first child message.
// The separate reorder tests cover a forwarded envelope that arrives first.
func TestClaude_TaskStartedPersistsThePromptAsTheChildsFirstMessage(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "SCAN triage angle",
		"prompt": "Review the diff and **report** every finding."
	}`))

	child := sink.Child("child-of-tu-spawn")
	msgs := child.Messages()
	require.Len(t, msgs, 1, "the prompt is the child transcript's only message so far")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, msgs[0].Source,
		"a USER envelope, so it renders as markdown in a user bubble like a typed message")
	assert.JSONEq(t, `{"content":"Review the diff and **report** every finding."}`, string(msgs[0].Content))
	assert.Empty(t, msgs[0].SpanID, "the prompt belongs to no tool span")

	// The subagent's own output still lands after it.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"role": "assistant", "content": [{"type": "text", "text": "On it."}]}
	}`))
	assert.Len(t, child.Messages(), 2)
}

// A background Task persists its opening prompt through the same spawn path, independently of whether or when the reader opens its tab.
func TestClaude_TaskStartedPersistsThePromptWithNoDescription(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"prompt": "First line\nSecond line"
	}`))

	child := sink.Child("child-of-tu-spawn")
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"First line\nSecond line"}`, string(child.Messages()[0].Content))
}

// A task_started with no prompt must leave the transcript empty rather than
// persist a blank bubble.
func TestClaude_TaskStartedWithoutAPromptPersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "SCAN triage angle"
	}`))

	child := sink.Child("child-of-tu-spawn")
	assert.Empty(t, child.Messages())
}

// A shell task (local_bash) has no transcript at all, so nothing is written.
func TestClaude_TaskStartedForAShellPersistsNoPrompt(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_bash",
		"prompt": "npm test"
	}`))

	// Assert the registry row's ChildAgentID instead of reading ChildSink's messages.
	// ChildSink creates its recording sink on demand, so an empty message list cannot prove that no shell child agent exists.
	// The actual row linkage proves that condition.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind)
	assert.Empty(t, tasks[0].ChildAgentID, "a shell task never gets a child transcript")
}

// task_notification supplies output_file for Task children and background shells.
// The output-file upsert must therefore preserve the task's indexed kind.
// Hardcoded KindShell formerly changed child rows into shell rows and removed their transcript link in the sidebar.
// The actual child tab still existed, but its row could no longer open it.
func TestClaude_TaskNotificationWithOutputFileKeepsTheSubagentKind(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "Write two sentences about the ocean",
		"prompt": "Write two sentences about the ocean."
	}`))
	require.Len(t, sink.BackgroundTasks(), 1)
	require.Equal(t, bgtask.KindSubagent, sink.BackgroundTasks()[0].Kind)

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_notification",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"status": "completed",
		"output_file": "/tmp/task-1.log"
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindSubagent, tasks[0].Kind, "the notification must not rewrite the kind")
	assert.Equal(t, "/tmp/task-1.log", tasks[0].Description)
}

// The shell output-file upsert must preserve its explicit shell kind also.
// A row recreated after eviction needs that kind to enter the correct display pool.
// The database rejects an unspecified kind for a new row instead of assuming a task type.
func TestClaude_TaskNotificationWithOutputFileKeepsTheShellKind(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-shell",
		"task_type": "local_bash",
		"description": "npm test"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_notification",
		"task_id": "task-1",
		"tool_use_id": "tu-shell",
		"status": "completed",
		"output_file": "/tmp/shell.log"
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindShell, tasks[0].Kind)
}

// A dynamic workflow uses the same final notification path. The notification
// carries no task_type, so the task index must preserve the workflow kind.
func TestClaude_TaskNotificationWithOutputFileKeepsTheWorkflowKind(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "workflow-1",
		"tool_use_id": "tu-workflow",
		"task_type": "local_workflow",
		"workflow_name": "probe",
		"description": "Protocol probe"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_notification",
		"task_id": "workflow-1",
		"tool_use_id": "tu-workflow",
		"status": "stopped",
		"output_file": "/tmp/workflow-1.log",
		"summary": "Protocol probe"
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindWorkflow, tasks[0].Kind)
	assert.Equal(t, bgtask.StatusStopped, tasks[0].Status)
	assert.Equal(t, "/tmp/workflow-1.log", tasks[0].Description)
	assert.Empty(t, tasks[0].ChildAgentID)
}

// background_tasks_changed replaces the CLI's live background list and excludes tasks with isBackgrounded=false.
// A foreground shell registers as local_bash after two seconds and already owns a registry row, but appears in no background-list payload.
//
// Keep this event as a no-op because the registry intentionally retains foreground shells also.
// Applying replacement semantics would delete a visible foreground row.
// A full native event queue first discards an event that is neither start nor end.
// Losing a background-list event could therefore hide a real background shell during its run.
func TestClaude_BackgroundTasksChangedLeavesTheRegistryAlone(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-sh",
		"tool_use_id": "tu-shell",
		"task_type": "local_bash",
		"description": "task test-e2e"
	}`))
	require.Len(t, sink.BackgroundTasks(), 1)

	// The empty list is the payload a FOREGROUND shell produces: nothing is
	// backgrounded, so the CLI reports no live background task.
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "background_tasks_changed",
		"tasks": []
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "the level signal must not evict the row")
	assert.Equal(t, "task-sh", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	assert.Equal(t, "task test-e2e", tasks[0].Title)
	assertTaskEventConsumed(t, sink)
}

// task_updated repeats task_notification's final status and can report a foreground-to-background change through patch.is_backgrounded.
// Consume it without changing the registry.
// Only the final notification determines finality, and a patch must not close a row independently.
// The background flag changes nothing because the registry already contains that shell row.
func TestClaude_TaskUpdatedDoesNotChangeTheRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-sh",
		"tool_use_id": "tu-shell",
		"task_type": "local_bash",
		"description": "sleep 30"
	}`))
	require.Len(t, sink.BackgroundTasks(), 1)

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_updated",
		"task_id": "task-sh",
		"patch": {"status": "completed", "end_time": 1760000000000, "is_backgrounded": true}
	}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status,
		"only task_notification ends a row")
	assert.True(t, tasks[0].EndedAt.IsZero(), "the row is still active")
	assertTaskEventConsumed(t, sink)
}

// A task event without an earlier task_started creates no row.
// Only start and final-notification events drive row creation.
// task_updated and background_tasks_changed therefore cannot create a row for an unknown task.
func TestClaude_TaskUpdatedForAnUnknownTaskCreatesNoRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_updated",
		"task_id": "task-never-started",
		"patch": {"is_backgrounded": true}
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "background_tasks_changed",
		"tasks": [{"task_id": "task-never-started", "task_type": "local_bash", "description": "sleep 30"}]
	}`))

	assert.Empty(t, sink.BackgroundTasks())
	assertTaskEventConsumed(t, sink)
}

// assertTaskEventConsumed verifies that a task event reaches neither persistence path.
// A system line declined by claudeHandleTaskEvent can enter PersistNotification when the classifier permits consolidation, or otherwise PersistMessage.
// Checking only one path could incorrectly pass when the line enters the other.
func assertTaskEventConsumed(t *testing.T, sink *agenttest.Sink) {
	t.Helper()
	assert.Empty(t, sink.Messages(), "a consumed event never reaches the transcript")
	assert.Empty(t, sink.PersistedNotifications(), "nor the notification thread")
}

// A replayed task_started (a re-attach after a worker restart) must not stack a
// second copy of the prompt on top of the transcript it already introduced.
func TestClaude_ReplayedTaskStartedDoesNotDuplicateThePrompt(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	started := []byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"prompt": "Do the thing."
	}`)
	a.HandleOutput(started)
	a.HandleOutput(started)

	child := sink.Child("child-of-tu-spawn")
	assert.Len(t, child.Messages(), 1)
}

// --- A subagent spawn owns no span ---

func TestClaude_ToolSpawnsSubagent(t *testing.T) {
	t.Parallel()

	assert.True(t, claudeToolSpawnsSubagent(ToolNameAgent))
	assert.True(t, claudeToolSpawnsSubagent(ToolNameTask),
		"Task is the legacy wire name for the SAME Agent tool")

	// The to-do tools share the Task prefix but start no subagent.
	for _, name := range []string{
		"Read", "Bash", "TaskCreate", "TaskUpdate", "TaskGet",
		"TaskList", "TaskOutput", "TaskStop", "AgentTool", "",
	} {
		assert.False(t, claudeToolSpawnsSubagent(name), "%q does not spawn", name)
	}
}

// A spawn tool_use opens no span and reserves no color.
// It still carries the span ID that the frontend pairs with tool_result and the span type later read through GetSpanType.
func TestClaude_AgentToolUseOpensNoSpan(t *testing.T) {
	t.Parallel()

	for _, toolName := range []string{ToolNameAgent, ToolNameTask} {
		t.Run(toolName, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a := newTestAgent(agentapi.NewProviderServices(sink))
			a.HandleOutput([]byte(`{
				"type": "assistant",
				"message": {"role": "assistant", "content": [
					{"type": "tool_use", "id": "tu-spawn", "name": "` + toolName + `",
					 "input": {"description": "explore", "prompt": "look around"}}
				]}
			}`))

			assert.Empty(t, sink.OpenSpans(), "a spawn opens no span")
			assert.Empty(t, sink.ReservedColorSpans(),
				"a spawn reserves no color, so none is blocked while it runs")
			assert.Equal(t, toolName, sink.GetSpanType("tu-spawn"),
				"the span type is still recorded for the tool_result")

			msgs := sink.Messages()
			require.Len(t, msgs, 1)
			assert.Equal(t, "tu-spawn", msgs[0].SpanID, "the row still carries the span id")
			assert.Empty(t, msgs[0].SpansOpenAtPersist, "nothing else was open, so no rail")
		})
	}
}

// The guard is not too wide: an ordinary tool still opens a span and reserves
// a color.
func TestClaude_OrdinaryToolUseStillOpensASpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))

	open := sink.OpenSpans()
	require.Len(t, open, 1)
	assert.Equal(t, "tu-read", open[0].SpanID)
	assert.Equal(t, []string{"tu-read"}, sink.ReservedColorSpans())
}

// One assistant envelope can carry parallel tool calls. The spawn among them
// opens nothing while its siblings still open their spans.
func TestClaude_ParallelBlocksOpenOnlyTheNonSpawns(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}},
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))

	open := sink.OpenSpans()
	require.Len(t, open, 1, "only the Read opens a span")
	assert.Equal(t, "tu-read", open[0].SpanID)
	// spanID/spanColor come from the FIRST tool_use block, which is the spawn,
	// so no color is reserved for this row at all.
	assert.Empty(t, sink.ReservedColorSpans())
}

// The spawn's tool_result closes nothing and draws no rail of its own.
func TestClaude_AgentToolResultDrawsNoRail(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "user",
		"message": {"role": "user", "content": [
			{"type": "tool_result", "tool_use_id": "tu-spawn", "content": "the finding"}
		]}
	}`))

	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.True(t, msgs[1].Closing, "the tool_result is still a closer")
	assert.Equal(t, ToolNameAgent, msgs[1].SpanType,
		"span_type survives because the spawn never closed a span to forget it")
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "and it draws no rail")
	assert.Empty(t, sink.OpenSpans())
}

// A spawn during an active Read call draws only that Read's rail, with one column rather than two.
func TestClaude_SpawnInsideOpenReadDrawsOneColumn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "user",
		"message": {"role": "user", "content": [
			{"type": "tool_result", "tool_use_id": "tu-spawn", "content": "the finding"}
		]}
	}`))

	msgs := sink.Messages()
	require.Len(t, msgs, 3)
	for i, msg := range msgs[1:] {
		require.Len(t, msg.SpansOpenAtPersist, 1, "spawn row %d draws exactly one column", i)
		assert.Equal(t, "tu-read", msg.SpansOpenAtPersist[0].SpanID)
	}
	// The Read span remains open after both spawn rows and is the only open span.
	// The spawn tool_result calls CloseSpan but owns no span to remove, so it cannot change the Read column.
	assert.Equal(t, []string{"tu-spawn"}, sink.ClosedSpans())
	open := sink.OpenSpans()
	require.Len(t, open, 1, "the Read is still the only span that ever opened")
	assert.Equal(t, "tu-read", open[0].SpanID)
	assert.Equal(t, ToolNameAgent, sink.GetSpanType("tu-spawn"),
		"the close kept the recorded type, so the tool_result persisted the real name")
}

// Workflow starts several agents and waits for the last to end, so it also owns no span.
// Its tool_use initially opens a span because the CLI feature flag prevents stable name-based detection.
// task_started supplies the first authoritative workflow classification and releases that span.
func TestClaude_WorkflowTaskStartedGivesTheSpanBack(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-wf", "name": "Workflow", "input": {"name": "review"}}
		]}
	}`))
	require.Len(t, sink.OpenSpans(), 1, "the tool_use opened one before we knew")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-wf",
		"tool_use_id": "tu-wf",
		"task_type": "local_workflow",
		"workflow_name": "review"
	}`))

	assert.Equal(t, []string{"tu-wf"}, sink.ClosedSpans(),
		"the span is given back although the workflow run keeps going")
	assert.Equal(t, "Workflow", sink.GetSpanType("tu-wf"),
		"and the recorded type survives, so the tool_result reads it back")

	// A tool row persisted after the discard draws no rail.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the workflow rail is gone")
}

// A shell keeps its ordinary Bash rail until tool_result closes it.
// Both background shells and foreground commands beyond the two-second registration threshold emit the same task_started shape.
// This test therefore verifies span preservation for both.
func TestClaude_ShellTaskStartedKeepsTheSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-bash", "name": "Bash", "input": {"command": "sleep 100"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-sh",
		"tool_use_id": "tu-bash",
		"task_type": "local_bash",
		"description": "sleep 100"
	}`))

	assert.Empty(t, sink.ClosedSpans(), "a background shell keeps its span")
	open := sink.OpenSpans()
	require.Len(t, open, 1)
	assert.Equal(t, "tu-bash", open[0].SpanID)
}

// claudeToolSpawnsSubagent identifies a local_agent Task by name and opens no span for it.
// task_started still applies the task-type release check.
// That release changes nothing because no span exists and later rows therefore draw no spawn rail.
func TestClaude_AgentTaskStartedLeavesNoSpanOpen(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}}
		]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"prompt": "go"
	}`))

	assert.Empty(t, sink.OpenSpans())

	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the spawn left no rail behind")
}

// task_started determines spawn behavior even for an unlisted task type.
// Release that task's span also.
// Using only known tool names would leave its rail open throughout the child run.
func TestClaude_UnknownTaskTypeGivesTheSpanBack(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	// A spawning tool whose wire name is in no list, so its tool_use opens a span.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-x", "name": "Delegate", "input": {"prompt": "go"}}
		]}
	}`))
	require.Len(t, sink.OpenSpans(), 1, "the unknown name opened one before we knew")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-x",
		"tool_use_id": "tu-x",
		"task_type": "local_fleet",
		"prompt": "go"
	}`))

	assert.Equal(t, []string{"tu-x"}, sink.ClosedSpans())
	assert.Equal(t, "Delegate", sink.GetSpanType("tu-x"),
		"the recorded type survives, so the tool_result reads it back")

	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the unknown spawn's rail is gone")
}

// A workflow event without a tool_use_id has no span to identify, so it discards
// nothing rather than discarding the empty id.
func TestClaude_WorkflowTaskStartedWithoutToolUseIDClosesNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-wf",
		"task_type": "local_workflow"
	}`))

	assert.Empty(t, sink.ClosedSpans())
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.KindWorkflow, tasks[0].Kind)
	assert.Empty(t, tasks[0].GroupKey, "a workflow name is optional")
	assert.Empty(t, tasks[0].ChildAgentID)
}

// Reserve a child tool's color under its spawn span instead of the root.
// The reservation's parent determines its column, so using the root would color it for the wrong transcript.
// claudeSpanInfoFor accepts that parent from its caller, which supplies the only difference between these transcript paths.
func TestClaude_ChildTranscriptReservesUnderTheSpawnSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))

	child := sink.Child("child-of-tu-spawn")
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "tu-read", ParentSpanID: "tu-spawn"}},
		child.ReservedColors(), "the child reserves under the spawn span")
	assert.Empty(t, sink.ReservedColors(), "and nothing is reserved in the parent transcript")
}

// A spawn explicitly reports span_color=0, so persistence must preserve that value instead of replacing it with connector color.
// A main-session envelope can resolve ParentSpanID from its own tool_use_id, including a still-open span.
// Using that parent's color would tint a spawn card without a corresponding rail.
func TestClaude_SpawnRowUnderAnOpenParentStillTakesTheNeutralBorder(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))
	// A top-level envelope carrying tool_use_id: the parent span resolves to it.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"tool_use_id": "tu-read",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-spawn", "name": "Agent", "input": {"prompt": "go"}}
		]}
	}`))

	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Equal(t, "tu-read", msgs[1].ParentSpanID, "the spawn row does sit under the open Read")
	assert.True(t, msgs[1].NoSpan,
		"and it is marked as owning no span, so nothing substitutes a colour for its 0")
	assert.Equal(t, []string{"tu-read"}, sink.ReservedColorSpans(),
		"only the Read reserved a colour; the spawn reserved none")
}

// A child can start another child, and that nested spawn owns no span in the current child transcript.
// Its output belongs to its own separate transcript.
func TestClaude_NestedSpawnOpensNoSpanInTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	// A forwarded envelope carries the spawning tool_use id, which routes it to
	// the child transcript.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"role": "assistant", "content": [
			{"type": "tool_use", "id": "tu-nested", "name": "Agent", "input": {"prompt": "go deeper"}},
			{"type": "tool_use", "id": "tu-read", "name": "Read", "input": {"file_path": "/tmp/a"}}
		]}
	}`))

	assert.Empty(t, sink.OpenSpans(), "the parent transcript is untouched")

	child := sink.Child("child-of-tu-spawn")
	open := child.OpenSpans()
	require.Len(t, open, 1, "only the Read opens a span in the child transcript")
	assert.Equal(t, "tu-read", open[0].SpanID)
	assert.Empty(t, child.ReservedColorSpans(),
		"the row's color comes from the first block, which is the nested spawn")
	assert.Equal(t, ToolNameAgent, child.GetSpanType("tu-nested"),
		"the nested spawn's type is still recorded for its tool_result")
}

// --- SendMessage restart ---
//
// Claude restarts an ended child after a parent message and repeats task_started with the same task_id.
// That event alone also matches resumed-session hydration, which announces every preceding task with final registry status.
// Record delivery intent at SendMessage and consume it at the corresponding task_started.
// These tests verify both steps and the invalid restart classifications they exclude.

// spawnAndFinishSubagent replays a complete first run:
//   - Spawn.
//   - One reply.
//   - Final notification.
// It returns the child's sink.
// The actual lifecycle establishes a successful final row and its transcript link before testing restart behavior.
// No directly seeded row replaces that prerequisite.
func spawnAndFinishSubagent(t *testing.T, a *Agent, sink *agenttest.Sink) *agenttest.Sink {
	t.Helper()
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "Explore the parser",
		"prompt": "Find every caller."
	}`))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"content": [{"type": "text", "text": "Found three."}]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_notification",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"status": "completed",
		"summary": "done"
	}`))
	child := sink.Child("child-of-tu-spawn")
	_, status, found, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, found, "the first run left a registry row")
	require.True(t, status.IsFinished(), "the first run finished")
	return child
}

// spanIDs flattens the recorded opens to their span ids.
func spanIDs(opens []agenttest.SpanOpen) []string {
	ids := make([]string, 0, len(opens))
	for _, o := range opens {
		ids = append(ids, o.SpanID)
	}
	return ids
}

// sendMessageTo supplies the parent's SendMessage tool_use and records expected restart delivery.
func sendMessageTo(a *Agent, toolUseID, recipient string) {
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"content": [{
			"type": "tool_use",
			"id": "` + toolUseID + `",
			"name": "SendMessage",
			"input": {"to": "` + recipient + `", "message": "keep going"}
		}]}
	}`))
}

// restartTaskStarted supplies the CLI's confirmed restart event.
// The tool_use_id identifies the restarting SendMessage call instead of the original spawn.
// Its prompt contains the text that the child actually receives.
func restartTaskStarted(a *Agent, toolUseID, prompt string) {
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "` + toolUseID + `",
		"task_type": "local_agent",
		"description": "Explore the parser",
		"prompt": "` + prompt + `"
	}`))
}

func TestClaude_SendMessageRevivesAFinishedSubagent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "Also check the tests.")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"the armed task_started reopens the registry row")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status, "the row is active again")

	msgs := child.Messages()
	require.Len(t, msgs, before+1, "the delivered message is appended to the SAME transcript")
	last := msgs[len(msgs)-1]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, last.Source)
	assert.JSONEq(t, `{"content":"Also check the tests."}`, string(last.Content),
		"the text recorded is what task_started says the subagent received")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, last.MarkType,
		"a mid-transcript message carries a scroll-rail mark; the opening prompt does not")
}

// The restart task_started identifies SendMessage while that call still runs in the parent transcript.
// Keep its span open until its own tool_result closes it.
// An earlier release would leave connector_end without a preceding rail.
func TestClaude_ReviveDoesNotCloseTheSendMessageSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	require.Contains(t, spanIDs(sink.OpenSpans()), "tu-send", "SendMessage owns an ordinary span")
	restartTaskStarted(a, "tu-send", "more work")

	assert.NotContains(t, sink.ClosedSpans(), "tu-send",
		"a re-registration's tool_use_id is not a spawn span")
}

// A genuine first spawn still needs the task-type CloseSpan operation.
// Its output belongs to a separate transcript, so it owns no rail in this transcript.
func TestClaude_FirstTaskStartedStillClosesTheSpawnSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_workflow",
		"description": "a workflow run"
	}`))

	assert.Contains(t, sink.ClosedSpans(), "tu-spawn")
}

// Resumed-session hydration repeats task_started for an ended row without any current SendMessage.
// Reviving that row would reopen each previous child with no later close.
func TestClaude_TaskStartedWithoutASendMessageDoesNotRevive(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	restartTaskStarted(a, "tu-spawn", "(resumed agent)")

	assert.Empty(t, sink.RevivedTasks(), "an unarmed re-registration is not a revive")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, status, "the row keeps its final status")
	assert.Len(t, child.Messages(), before, "nothing is appended to the transcript")
}

// A delivery intent lasts only until its sending turn ends.
// An intent with no matching restart by then can describe one of these sends:
//   - A message to a live child.
//   - A foreign recipient.
//   - A send refused by the CLI.
// The turn end removes it.
func TestClaude_SendMessageArmExpiresAtTheTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	a.HandleOutput([]byte(`{"type": "result", "subtype": "success", "result": "ok"}`))
	restartTaskStarted(a, "tu-send", "too late")

	assert.Empty(t, sink.RevivedTasks(), "the arm did not survive the turn")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, status)
}

// to can identify a display name, another session, or a uds:/bridge:/did: address.
// None identifies a row in this registry, so its delivery intent matches no restart.
func TestClaude_SendMessageToAnUnknownRecipientIsInert(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "bridge:some-other-machine")
	restartTaskStarted(a, "tu-send", "not for us")

	assert.Empty(t, sink.RevivedTasks())
	assert.Len(t, child.Messages(), before)
}

// A SendMessage to a Running child changes no registry state.
// The CLI queues that message without emitting task_started.
func TestClaude_SendMessageToARunningSubagentDoesNotRevive(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"prompt": "Find every caller."
	}`))

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "more work")

	assert.Empty(t, sink.RevivedTasks(), "an active row has nothing to revive")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status)
}

// A child's SendMessage reaches this provider through the child transcript router.
// That path must also record delivery intent for its recipient.
func TestClaude_SendMessageFromAChildTranscriptArms(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	// A DIFFERENT subagent sends the message, so the envelope is forwarded.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-other-spawn",
		"message": {"content": [{
			"type": "tool_use",
			"id": "tu-send",
			"name": "SendMessage",
			"input": {"to": "task-1", "message": "keep going"}
		}]}
	}`))
	restartTaskStarted(a, "tu-send", "from a sibling")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"a subagent's SendMessage arms the same restart the parent's does")
}

// sendMessageFromChild forwards a child's SendMessage under its spawn span.
// Its delivery intent belongs to that child's turn, which can outlive the root turn.
func sendMessageFromChild(a *Agent, spawnSpanID, toolUseID, recipient string) {
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "` + spawnSpanID + `",
		"message": {"content": [{
			"type": "tool_use",
			"id": "` + toolUseID + `",
			"name": "SendMessage",
			"input": {"to": "` + recipient + `", "message": "keep going"}
		}]}
	}`))
}

// A background child can outlive the root turn and later message an ended sibling.
// Keep that child's delivery intent after the root result.
// Clearing every scope at the root boundary would prevent the later task_started from reopening its recipient and recording the message.
func TestClaude_AChildArmSurvivesTheRootTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	sendMessageFromChild(a, "tu-other-spawn", "tu-send", "task-1")
	a.HandleOutput([]byte(`{"type": "result", "subtype": "success", "result": "ok"}`))
	restartTaskStarted(a, "tu-send", "from a sibling")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"the root's turn end drops only the root's own arms")
}

// The child's own forwarded result ends its delivery-intent lifetime.
// Remove each unconsumed intent then instead of retaining it for the agent lifetime.
func TestClaude_AChildArmExpiresAtTheChildTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	sendMessageFromChild(a, "tu-other-spawn", "tu-send", "task-1")
	a.HandleOutput([]byte(`{
		"type": "result",
		"parent_tool_use_id": "tu-other-spawn",
		"subtype": "success",
		"result": "ok"
	}`))
	restartTaskStarted(a, "tu-send", "too late")

	assert.Empty(t, sink.RevivedTasks(), "the arm did not survive the sending transcript's turn")
}

// A child turn's end must preserve root-scoped delivery intents.
// The scopes use distinct keys because a child commonly ends during an active root turn.
func TestClaude_AChildTurnEndKeepsTheRootArms(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	a.HandleOutput([]byte(`{
		"type": "result",
		"parent_tool_use_id": "tu-other-spawn",
		"subtype": "success",
		"result": "ok"
	}`))
	restartTaskStarted(a, "tu-send", "still armed")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"a subagent's turn end leaves the root's arms alone")
}

// Forwarded output after restart must use the child's existing transcript for either native tool-use ID.
// The original spawn span resolves one form, and the registry row key resolves the repeated-registration form.
//
// This test verifies transcript identity.
// TestClaude_RestartedSubagentKeepsOneRegistryRow separately requires one registry row for that same run.
// Preserving only the transcript formerly left a duplicate entry in the background-task list.
func TestClaude_RestartedSubagentOutputStaysInOneTranscript(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		parentToolUseID string
	}{
		{"original spawn span", "tu-spawn"},
		{"the re-registered tool_use id", "tu-send"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a := newTestAgent(agentapi.NewProviderServices(sink))
			child := spawnAndFinishSubagent(t, a, sink)
			before := len(child.Messages())

			sendMessageTo(a, "tu-send", "task-1")
			restartTaskStarted(a, "tu-send", "Also check the tests.")
			a.HandleOutput([]byte(`{
				"type": "assistant",
				"parent_tool_use_id": "` + tc.parentToolUseID + `",
				"message": {"content": [{"type": "text", "text": "Checked them."}]}
			}`))

			msgs := child.Messages()
			require.Len(t, msgs, before+2, "the restart message and the new reply both land here")
			assert.Contains(t, string(msgs[len(msgs)-1].Content), "Checked them.")
			// Require one registry row as well as one transcript for either envelope ID.
			// Two rows linked to the same child would list it twice and open the same tab from both entries.
			assert.Equal(t, []string{"task-1"}, agenttest.RowKeys(sink),
				"and the run keeps the single row it already had")
		})
	}
}

// A restart without a prompt still reopens its row.
// The child runs again and needs its thinking indicator even when no new text exists.
func TestClaude_ReviveWithoutAPromptStillReopensTheRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "   ")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks())
	assert.Len(t, child.Messages(), before, "a blank prompt persists no bubble")
}

// One SendMessage permits one restart.
// A duplicate task_started must not reopen the row after it closes again.
func TestClaude_OneSendMessageArmsOneRevive(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "first")
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_notification",
		"task_id": "task-1",
		"tool_use_id": "tu-send",
		"status": "completed"
	}`))
	restartTaskStarted(a, "tu-send", "second")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(), "the arm was consumed by the first")
}

// One assistant message can contain parallel SendMessage calls to two ended children.
// Record delivery intent for both recipients.
func TestClaude_SendMessageArmsEveryRecipientInOneMessage(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	for _, spawn := range []string{"tu-spawn-a", "tu-spawn-b"} {
		a.HandleOutput([]byte(`{
			"type": "system", "subtype": "task_started",
			"task_id": "task-` + spawn + `", "tool_use_id": "` + spawn + `",
			"task_type": "local_agent", "prompt": "go"
		}`))
		a.HandleOutput([]byte(`{
			"type": "system", "subtype": "task_notification",
			"task_id": "task-` + spawn + `", "status": "completed"
		}`))
	}

	a.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {"content": [
			{"type": "tool_use", "id": "tu-send-a", "name": "SendMessage",
			 "input": {"to": "task-tu-spawn-a", "message": "one"}},
			{"type": "tool_use", "id": "tu-send-b", "name": "SendMessage",
			 "input": {"to": "task-tu-spawn-b", "message": "two"}}
		]}
	}`))
	for _, spawn := range []string{"tu-spawn-a", "tu-spawn-b"} {
		a.HandleOutput([]byte(`{
			"type": "system", "subtype": "task_started",
			"task_id": "task-` + spawn + `", "tool_use_id": "tu-send-` + spawn + `",
			"task_type": "local_agent", "prompt": "more"
		}`))
	}

	assert.ElementsMatch(t, []string{"task-tu-spawn-a", "task-tu-spawn-b"}, sink.RevivedTasks(),
		"a parallel pair of sends arms a restart for each recipient")
}

// Unreadable SendMessage input must neither stop the turn nor record delivery intent.
// to can be absent in malformed input or use a non-string structure permitted by the varying schema.
func TestClaude_SendMessageWithUnreadableInputArmsNothing(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		`{"message": "no recipient"}`,
		`{"to": {"nested": "object"}, "message": "wrong type"}`,
		`"not an object at all"`,
		`{"to": "", "message": "blank recipient"}`,
	} {
		sink := &agenttest.Sink{}
		a := newTestAgent(agentapi.NewProviderServices(sink))
		spawnAndFinishSubagent(t, a, sink)

		a.HandleOutput([]byte(`{
			"type": "assistant",
			"message": {"content": [{
				"type": "tool_use", "id": "tu-send", "name": "SendMessage",
				"input": ` + input + `
			}]}
		}`))
		restartTaskStarted(a, "tu-send", "should not land")

		assert.Empty(t, sink.RevivedTasks(), "input %s must arm nothing", input)
	}
}

// A failed registry read supplies unknown state instead of proving that a row is absent.
// Treating it as absence formerly misclassified a restart during a worker's first registry read.
// That path released the active SendMessage span and treated the ended child as a new spawn.
func TestClaude_AnUnreadableRegistryDoesNotFreeTheSendMessageSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{LookupErr: errors.New("database is locked")}
	a := newTestAgent(agentapi.NewProviderServices(sink))

	sendMessageTo(a, "tu-send", "task-1")
	require.Contains(t, spanIDs(sink.OpenSpans()), "tu-send")
	restartTaskStarted(a, "tu-send", "more work")

	assert.NotContains(t, sink.ClosedSpans(), "tu-send",
		"a registry it could not read cannot prove this is a spawn")
	assert.NotContains(t, sink.ChildAgentIDs(), "child-of-tu-send",
		"nor can it prove the id is a spawn span worth opening a transcript from")
}

// A restart without a stored child link cannot resolve its transcript through that row.
// A failed linkage write can cause this state; display eviction preserves a linked row in storage.
// The event's tool_use_id identifies SendMessage, so EnsureChildAgent must not use it as a spawn span.
// Doing so creates a second transcript that later original-span envelopes duplicate.
func TestClaude_ReviveWithAnUnlinkedRowOpensNoSecondTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())
	// The child transcript exists; only the row's linkage to it is missing.
	sink.UnlinkBackgroundTask("task-1")

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "Also check the tests.")

	assert.NotContains(t, sink.ChildAgentIDs(), "child-of-tu-send",
		"a SendMessage id must never open a transcript")
	assert.Len(t, child.Messages(), before, "the router writes nothing to a transcript the row does not identify")

	// Refusing the SendMessage ID does not remove the child's existing transcript.
	// Its own output can still resolve that transcript through the original spawn span.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"content": [{"type": "text", "text": "Checked them."}]}
	}`))

	msgs := child.Messages()
	require.Len(t, msgs, before+1, "the reply lands in the subagent's own transcript")
	assert.Contains(t, string(msgs[before].Content), "Checked them.")
}

// Consume delivery intent only after a restart resolves its child transcript.
// An unresolved task_started retains that intent for a later retry.
// A failed registry revival preserves it for the same reason.
func TestClaude_ATaskStartedThatResolvesNoChildKeepsTheArm(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)
	sink.UnlinkBackgroundTask("task-1")

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "Also check the tests.")

	assert.Empty(t, sink.RevivedTasks(), "no transcript resolved, so no revive happened")
	assert.True(t, a.tasks.takeClaudeRestart("task-1"), "the arm is still standing for a retry")
}

// The CLI can forward a restarted result under the original spawn span.
// The first run removes that span from the tool-use index, while restart also indexes the SendMessage call.
// Without the child-to-task fallback, a missing original-span index would leave the reopened row Running for the agent lifetime.
func TestClaude_ARestartedResultUnderTheSpawnSpanClosesTheRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "Also check the tests.")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusRunning, status, "the restart reopened the row")

	a.HandleOutput([]byte(`{
		"type": "result",
		"parent_tool_use_id": "tu-spawn",
		"subtype": "success"
	}`))

	_, status, ok, _ = sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, status,
		"the restarted run's result closes the row it reopened")
}

// --- Wake restart ---
//
// Claude Code 2.1.233 also restarts an ended child when its own background shell completes.
// task_started then carries a <task-notification> prompt without tool_use_id.
// Reopen the row but exclude that model-facing harness notification from the user transcript.

// wakePrompt is the block the CLI hands a subagent when its shell completes.
func wakePrompt(shellTaskID string) string {
	return "<task-notification>\n<task-id>" + shellTaskID + "</task-id>\n" +
		"<tool-use-id>tu-bash</tool-use-id>\n<status>completed</status>\n</task-notification>"
}

// finishShellTask replays a backgrounded shell of the subagent, start to finish,
// so this process saw the id the wake block identifies.
func finishShellTask(a *Agent, shellTaskID string) {
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "` + shellTaskID + `", "tool_use_id": "tu-bash", "task_type": "local_bash"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_notification",
		"task_id": "` + shellTaskID + `", "tool_use_id": "tu-bash", "status": "completed"
	}`))
}

func TestClaude_AShellWakeRevivesTheSubagentWithoutAMessage(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())
	finishShellTask(a, "shell-1")

	// The wake carries no tool_use_id at all, which is the shape the CLI emits.
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "task_type": "local_agent",
		"prompt": ` + strconv.Quote(wakePrompt("shell-1")) + `
	}`))

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(), "the woken subagent's row reopens")
	assert.Len(t, child.Messages(), before,
		"a wake block is harness plumbing, not a message the user asked for")
}

// Exclude resumed-session hydration, which repeats earlier task prompts.
// The shell identified by an earlier wake belongs to a preceding process's completed-shell record, not this process's record.
func TestClaude_AWakeIdentifyingAnUnseenShellDoesNotRevive(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "task_type": "local_agent",
		"prompt": ` + strconv.Quote(wakePrompt("shell-from-a-previous-process")) + `
	}`))

	assert.Empty(t, sink.RevivedTasks(), "a wake this process cannot corroborate is not proof")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, status)
}

// Read the task ID regardless of its line position when both notification tags exist.
// claudeWakeRestartedTask then requires a shell completed by this process, and revival requires an existing final child row.
// Those later checks provide the restart evidence.
// Rejecting a valid native layout would leave the row final while the child runs again.
func TestClaude_WakeTaskIDReadsTheBlockWhateverItsShape(t *testing.T) {
	t.Parallel()

	for name, prompt := range map[string]string{
		"multi-line":         wakePrompt("shell-9"),
		"one line":           "<task-notification><task-id>shell-9</task-id><status>completed</status></task-notification>",
		"id beside siblings": "<task-notification>\n<tool-use-id>tu</tool-use-id><task-id>shell-9</task-id>\n</task-notification>",
		"trailing prose":     "<task-notification>\n<task-id>shell-9</task-id>\n</task-notification>\nReport when done.",
		"padded id":          "<task-notification>\n<task-id> shell-9 </task-id>\n</task-notification>",
	} {
		id, ok := claudeWakeTaskID(prompt)
		assert.True(t, ok, "%s must read as a wake", name)
		assert.Equal(t, "shell-9", id, "%s", name)
	}

	for name, prompt := range map[string]string{
		"empty":              "",
		"open tag only":      "Explain how <task-notification> blocks work.\nThe <task-id>shell-9</task-id> is the shell.",
		"close tag only":     "Some preamble\n<task-id>shell-9</task-id>\n</task-notification>",
		"unopened id":        "<task-notification>\n<status>completed</status>\n</task-notification>",
		"empty id":           "<task-notification>\n<task-id></task-id>\n</task-notification>",
		"whitespace-only id": "<task-notification>\n<task-id>   </task-id>\n</task-notification>",
	} {
		_, ok := claudeWakeTaskID(prompt)
		assert.False(t, ok, "%s must not read as a wake", name)
	}
}

// The CLI can emit the complete wake block on one line.
// Accept that layout also.
// The preceding line-anchored parser rejected it and left the row final during the next active run.
func TestClaude_AOneLineWakeBlockRevivesTheRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)
	finishShellTask(a, "shell-7")

	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "task_type": "local_agent",
		"prompt": ` + strconv.Quote("<task-notification><task-id>shell-7</task-id><status>completed</status></task-notification>") + `
	}`))

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"a wake block on one line restarts the subagent like any other")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status)
}

// Resolve a restart through the existing registry child link instead of its event's SendMessage tool-use ID.
// Passing that ID to EnsureChildAgent can miss both the row link and original spawn span.
// It would then create a second transcript and replace the durable row link with that orphan.
func TestClaude_ReviveResolvesTheChildFromTheRegistryRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "Also check the tests.")

	assert.NotContains(t, sink.ChildAgentIDs(), "child-of-tu-send",
		"the SendMessage id must not open a transcript of its own")
	require.Len(t, child.Messages(), before+1, "the message lands in the transcript the ROW points at")
}

// After a failed registry revival, preserve the delivered message and restore delivery intent.
// The first-start fallback cannot append that text because PersistChildPrompt preserves a transcript that already contains messages.
func TestClaude_AFailedReviveKeepsTheMessageAndRearms(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{ReviveErr: errors.New("database is locked")}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "Also check the tests.")

	msgs := child.Messages()
	require.Len(t, msgs, before+1, "the delivered message is recorded although the row write failed")
	assert.JSONEq(t, `{"content":"Also check the tests."}`, string(msgs[len(msgs)-1].Content))
	assert.True(t, a.tasks.takeClaudeRestart("task-1"), "the arm is back, so a later task_started can retry")
}

// A child can message an ended sibling, which must classify as a restart at both handleClaudeTaskStarted checks.
// Its SendMessage span type belongs to the sender's child tracker.
// The root tracker returns "" for that ID, just as it does for a spawn.
// With an unlinked recipient row, a mistaken classification would pass SendMessage to EnsureChildAgent and create another transcript.
// The durable row would then point to that orphan while actual child messages continue under the original spawn span.
func TestClaude_ASiblingSendOpensNoSecondTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)
	// The sender: a second, still-running subagent of the same root.
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-2", "tool_use_id": "tu-spawn-2", "task_type": "local_agent",
		"description": "Sibling", "prompt": "Coordinate."
	}`))
	sink.UnlinkBackgroundTask("task-1")

	sendMessageFromChild(a, "tu-spawn-2", "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "keep going")

	assert.NotContains(t, sink.ChildAgentIDs(), "child-of-tu-send",
		"a sibling's SendMessage id must never reach EnsureChildAgent")
}

// A sibling's SendMessage still runs in that sibling's transcript when its recipient restarts.
// Keep the sender's rail until its own tool_result supplies the ending connector.
// Releasing it at the recipient's task_started would leave that connector without a preceding line.
func TestClaude_ASiblingSendKeepsItsSpanOpen(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-2", "tool_use_id": "tu-spawn-2", "task_type": "local_agent",
		"description": "Sibling", "prompt": "Coordinate."
	}`))

	sendMessageFromChild(a, "tu-spawn-2", "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "keep going")

	assert.NotContains(t, sink.ClosedSpans(), "tu-send",
		"the restart call still runs in the sibling's transcript")
}

// The root and a sibling can both address one recipient during a single root turn.
// A single sender value formerly let the second sender replace the first scope.
// Whichever turn ended first could then remove the other sender's required delivery intent.
// The restarted row remained final and lost its delivered message.
func TestClaude_ASecondSenderDoesNotCancelTheFirstsArm(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-2", "tool_use_id": "tu-spawn-2", "task_type": "local_agent",
		"description": "Sibling", "prompt": "Coordinate."
	}`))

	// The root records delivery intent first, then a live sibling addresses the same recipient.
	sendMessageTo(a, "tu-send-root", "task-1")
	sendMessageFromChild(a, "tu-spawn-2", "tu-send-child", "task-1")
	// The sibling turn ends and removes only its own delivery intents.
	a.HandleOutput([]byte(`{
		"type": "result", "parent_tool_use_id": "tu-spawn-2", "subtype": "success"
	}`))

	restartTaskStarted(a, "tu-send-root", "keep going")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(),
		"the root's arm survives the sibling's turn end")
}

// Require completed-shell evidence for a wake.
// Recording all ended tasks formerly let a subagent's own ID satisfy that requirement.
// Resumed-session hydration repeats that child's old wake prompt and would then reopen a row with no later close.
func TestClaude_AWakeIdentifyingASubagentIsNotProof(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	// task-1 is an ended subagent, so its ID must not count as completed-shell evidence in a replayed wake prompt.
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "task_type": "local_agent",
		"prompt": ` + strconv.Quote(wakePrompt("task-1")) + `
	}`))

	assert.Empty(t, sink.RevivedTasks(), "only a finished shell corroborates a wake")
	_, status, ok, _ := sink.LookupBackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, status)
}

// A failed registry read alone supplies no restart evidence.
// It often occurs at a process's first registry access, which can describe a genuine first start.
// Suppressing the prompt fallback there formerly left a blank registry title.
// EnsureChildAgent then chose a pooled tab name instead of the spawn prompt's title.
func TestClaude_AnUnreadableRegistryKeepsThePromptTitleForAFirstStart(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{LookupErr: errors.New("database is locked")}
	a := newTestAgent(agentapi.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "tool_use_id": "tu-spawn", "task_type": "local_agent",
		"prompt": "Find every caller of parseHeader."
	}`))

	_, _, _, lookupErr := sink.LookupBackgroundTask("task-1")
	require.Error(t, lookupErr, "the fake reports the registry as unreadable")
	rows := sink.BackgroundTasks()
	idx := slices.IndexFunc(rows, func(i bgtask.Item) bool { return i.RowKey == "task-1" })
	require.GreaterOrEqual(t, idx, 0, "the row is still created")
	assert.Equal(t, "Find every caller of parseHeader.", rows[idx].Title,
		"a first start still takes its title from the prompt")
}

// A wake block belongs to the model harness and must never become a row title.
// A failed registry read leaves known.exists=false because existence remains unknown.
// When the title check considered only SendMessage evidence, a wake used the prompt fallback and displayed a literal <task-notification> in the sidebar.
func TestClaude_AWakeBlockNeverBecomesTheRowTitle(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{LookupErr: errors.New("database is locked")}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	finishShellTask(a, "shell-7")

	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "task_type": "local_agent",
		"prompt": ` + strconv.Quote(wakePrompt("shell-7")) + `
	}`))

	rows := sink.BackgroundTasks()
	idx := slices.IndexFunc(rows, func(i bgtask.Item) bool { return i.RowKey == "task-1" })
	require.GreaterOrEqual(t, idx, 0, "the event still upserts a row")
	assert.Empty(t, rows[idx].Title, "a wake block must not become the row title")
}

// task_started still records task kind when it omits tool_use_id, as a wake event does.
// A later task_notification needs that kind to preserve the child's registry type.
// Skip the tool-use pair instead of inserting an empty key.
func TestClaude_StartTaskWithoutAToolUseIDStillRecordsTheKind(t *testing.T) {
	t.Parallel()

	var idx claudeTaskIndex

	pending, hasPending := idx.startTask("task-1", bgtask.KindSubagent, "", "")
	assert.False(t, hasPending, "no spawn span means no pending close to take")
	assert.Equal(t, bgtask.StatusPending, pending)
	assert.Equal(t, bgtask.KindSubagent, idx.kindForTask("task-1"))
	// Inspect the maps directly.
	// taskIDForToolUse("") returns before reading either map, so it cannot prove that startTask inserts no empty-key entries.
	assert.Empty(t, idx.runs.toolUseTask, "no reverse entry is written under an empty key")
	assert.Empty(t, idx.runs.taskToolUse, "and no forward set either")

	// And with a spawn span the pair IS written, both ways.
	idx.startTask("task-2", bgtask.KindShell, "tu-2", "tu-2")
	assert.Equal(t, "task-2", idx.taskIDForToolUse("tu-2"))
	assert.Equal(t, bgtask.KindShell, idx.kindForTask("task-2"))
}

// Exercise the title selector directly.
// The full handler needs six sink calls for each setup, and three of four branches require an injected failure there.
func TestClaude_TaskStartedTitleRule(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "desc",
		claudeTaskStartedTitle(&claudeTaskEnvelope{Description: "desc", Prompt: "p\nq"}, claudeKnownTask{}, claudeRestartEvidence{}),
		"a description wins outright")
	assert.Equal(t, "p",
		claudeTaskStartedTitle(&claudeTaskEnvelope{Prompt: "p\nq"}, claudeKnownTask{}, claudeRestartEvidence{}),
		"a first start falls back to the prompt's first line")
	assert.Empty(t,
		claudeTaskStartedTitle(&claudeTaskEnvelope{Prompt: "p"}, claudeKnownTask{exists: true}, claudeRestartEvidence{}),
		"a row that already exists keeps the title it has")
	assert.Empty(t,
		claudeTaskStartedTitle(&claudeTaskEnvelope{Prompt: "p"}, claudeKnownTask{}, claudeRestartEvidence{wake: true}),
		"and a wake never renames the row to its own block")
	assert.Empty(t,
		claudeTaskStartedTitle(&claudeTaskEnvelope{Prompt: "p"}, claudeKnownTask{}, claudeRestartEvidence{sendMessage: true}),
		"nor does the message the parent just sent")
	assert.Equal(t, "p",
		claudeTaskStartedTitle(&claudeTaskEnvelope{Prompt: "p"}, claudeKnownTask{unreadable: true}, claudeRestartEvidence{}),
		"an UNREADABLE registry is not evidence, so the fallback still runs")
}

// startTask indexes the restarted run's original spawn span and the call ID from its repeated registration.
// Both must resolve the run, and final notification must remove both.
// A retained ID could route another child's output into this ended transcript.
func TestClaude_StartTaskIndexesEveryToolUseIDOfARun(t *testing.T) {
	t.Parallel()

	var idx claudeTaskIndex

	idx.startTask("task-1", bgtask.KindSubagent, "tu-spawn", "tu-send")
	idx.rememberTaskChild("task-1", "child-1")
	assert.Equal(t, "task-1", idx.taskIDForToolUse("tu-spawn"))
	assert.Equal(t, "task-1", idx.taskIDForToolUse("tu-send"))

	idx.forgetTaskIndex("task-1")
	assert.Empty(t, idx.taskIDForToolUse("tu-spawn"), "the closing notification drops the spawn span")
	assert.Empty(t, idx.taskIDForToolUse("tu-send"), "and the call that restarted the task")
	assert.Equal(t, "task-1", idx.taskIDForChild("child-1"), "the durable transcript link outlives the run")
}

func TestClaude_ForgetTaskIndexDropsAnUndeliveredHandback(t *testing.T) {
	t.Parallel()

	var idx claudeTaskIndex
	idx.startTask("task-1", bgtask.KindSubagent, "spawn-1", "spawn-1")
	require.True(t, idx.rememberHandbackToolUse("spawn-1", "task-1", "handback-1", "Reviewer", "Report"))

	idx.forgetTaskIndex("task-1")

	var echo messageEnvelope
	echo.Message.RawContent = json.RawMessage(`[{"type":"text","text":"Report"}]`)
	assert.False(t, idx.isHandbackEcho("spawn-1", &echo))
	_, found := idx.takeHandbackForPeerResult("task-1")
	assert.False(t, found)
}

// A final result can precede task_started under either supplied tool-use ID.
// The late start must consume that close and finish its new row instead of leaving it Running.
func TestClaude_StartTaskTakesAPendingCloseUnderEitherToolUseID(t *testing.T) {
	t.Parallel()

	for _, recordedUnder := range []string{"tu-spawn", "tu-send"} {
		t.Run(recordedUnder, func(t *testing.T) {
			t.Parallel()

			var idx claudeTaskIndex
			idx.recordPendingTaskEnd(recordedUnder, bgtask.StatusFailed)

			pending, hasPending := idx.startTask("task-1", bgtask.KindSubagent, "tu-spawn", "tu-send")
			assert.True(t, hasPending, "the reordered close is taken")
			assert.Equal(t, bgtask.StatusFailed, pending)

			_, hasPending = idx.startTask("task-1", bgtask.KindSubagent, "tu-spawn", "tu-send")
			assert.False(t, hasPending, "the close is consumed, not left to fire twice")
		})
	}
}

// When both IDs hold a final result, the original spawn span determines the outcome because forwarded run envelopes use it.
// Still consume the other pending close so it cannot affect a later run with the same durable spawn span.
func TestClaude_StartTakesTheSpawnSpansCloseWhenBothIDsHoldOne(t *testing.T) {
	t.Parallel()

	var idx claudeTaskIndex
	idx.recordPendingTaskEnd("tu-spawn", bgtask.StatusFailed)
	idx.recordPendingTaskEnd("tu-send", bgtask.StatusStopped)

	pending, hasPending := idx.startTask("task-1", bgtask.KindSubagent, "tu-spawn", "tu-send")
	assert.True(t, hasPending)
	assert.Equal(t, bgtask.StatusFailed, pending, "the spawn span decides, not the last id visited")
	assert.Empty(t, idx.runs.pendingEnd, "and both entries are consumed")
}

// A task can recover an original span absent from its current event, leaving two task sets with one ID.
// The later writer owns the reverse entry.
// An earlier task's final notification must preserve that entry because the active run's forwarded envelopes use it.
func TestClaude_ForgetTaskIndexKeepsAnIDAnotherTaskNowOwns(t *testing.T) {
	t.Parallel()

	var idx claudeTaskIndex
	idx.startTask("task-A", bgtask.KindSubagent, "tu-spawn", "")
	idx.startTask("task-B", bgtask.KindSubagent, "tu-spawn", "")

	idx.forgetTaskIndex("task-A")
	assert.Equal(t, "task-B", idx.taskIDForToolUse("tu-spawn"),
		"the id still resolves to the task that owns it")
}

// A first run's forwarded output can precede task_started before a task ID exists.
// Create an early row under the spawn span and rename it when the start event arrives.
// One run then retains one row instead of an orphan beside another row.
func TestClaude_AFirstStartRenamesItsPreStartRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))

	// The reorder: the subagent talks before the CLI announces it.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"content": [{"type": "text", "text": "Working."}]}
	}`))
	require.Equal(t, []string{"prestart:tu-spawn"}, agenttest.RowKeys(sink),
		"with no task id the row opens under the spawn span")

	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "tool_use_id": "tu-spawn",
		"task_type": "local_agent", "description": "Explore the parser"
	}`))

	assert.Equal(t, []string{"task-1"}, agenttest.RowKeys(sink),
		"the late task_started folds the pre-start row into the run")
	childID, status, ok, err := sink.LookupBackgroundTask("task-1")
	require.NoError(t, err)
	require.True(t, ok, "the renamed row answers under the task id")
	assert.Equal(t, "child-of-tu-spawn", childID, "and it carries the transcript the pre-start row opened")
	assert.Equal(t, bgtask.StatusRunning, status)
}

// A restarted run can also forward output before task_started.
// Its child transcript already exists, so routeSubagentMessage resolves the actual row through that child without creating an early row.
//
// Require that no second row exists at any point.
// Renaming a duplicate afterwards would only repair the symptom, and the target key is already occupied on this path.
// See TestBgTask_RenameOntoOccupiedKeyDropsALoserTheWinnerSupersedes for a reorder that outlives the process able to resolve it.
func TestClaude_ARestartReorderOpensNoPreStartRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)

	sendMessageTo(a, "tu-send", "task-1")
	// The reorder: output before the task_started that announces the restart.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"content": [{"type": "text", "text": "Already going."}]}
	}`))
	require.Equal(t, []string{"task-1"}, agenttest.RowKeys(sink),
		"the child resolves the run, so no pre-start row opens")

	restartTaskStarted(a, "tu-send", "Also check the tests.")

	assert.Equal(t, []string{"task-1"}, agenttest.RowKeys(sink),
		"and the late task_started leaves it at one")
	child := sink.Child("child-of-tu-spawn")
	require.NotEmpty(t, child.Messages())
	assert.Contains(t, string(child.Messages()[len(child.Messages())-1].Content), "Also check the tests.",
		"the delivered message still reaches the transcript the row carries")
}

// A wake from a shell completed in an earlier process has no current-process restart proof and supplies no tool_use_id.
// The durable registry row still links the child transcript.
// Read its original spawn span independently of whether the current process can prove that wake.
func TestClaude_ARestartWithNoPerProcessEvidenceStillKeepsOneRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	spawnAndFinishSubagent(t, newTestAgent(agentapi.NewProviderServices(sink)), sink)

	// A fresh agent with the same sink models worker restart.
	// The registry and child rows survive, while the tool-use index and childTask map start empty.
	a := newTestAgent(agentapi.NewProviderServices(sink))
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"content": [{"type": "text", "text": "Back at it."}]}
	}`))
	a.HandleOutput([]byte(`{
		"type": "system", "subtype": "task_started",
		"task_id": "task-1", "task_type": "local_agent",
		"description": "Explore the parser"
	}`))

	assert.Equal(t, []string{"task-1"}, agenttest.RowKeys(sink),
		"the durable spawn span folds the pre-start row back into the run")
}

// A unique tool-use ID recorded as SendMessage must never later identify a spawn.
// Keep that classification after the sending turn ends.
// The CLI ends the recipient's previous run before delivery, so its task_started can arrive after that sending turn.
func TestClaude_ARestartCallIsRefusedAfterTheTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)
	sink.UnlinkBackgroundTask("task-1")

	sendMessageTo(a, "tu-send", "task-1")
	a.HandleOutput([]byte(`{"type": "result", "subtype": "success", "result": "ok"}`))
	restartTaskStarted(a, "tu-send", "too late")

	assert.NotContains(t, sink.ChildAgentIDs(), "child-of-tu-send",
		"a SendMessage id is not a spawn span, whichever turn its task_started lands in")
	assert.NotContains(t, sink.ClosedSpans(), "tu-send",
		"and its rail is not freed either")
}

// A restart can fail to read the child's original spawn span and must still deliver its message and reopen its row.
// The span only improves routing resolution.
// Refusing the restart because that read fails would lose the parent's message without a later text retry.
func TestClaude_ARestartSurvivesAnUnreadableSpawnSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{SpawnSpanErr: errors.New("boom")}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	child := spawnAndFinishSubagent(t, a, sink)
	before := len(child.Messages())

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "Also check the tests.")

	assert.Equal(t, []string{"task-1"}, sink.RevivedTasks(), "the row still reopens")
	require.Len(t, child.Messages(), before+1, "the delivered message still lands")

	// The run still owns one registry row.
	// Without the span index, routeSubagentMessage resolves through the existing child.
	// Otherwise this envelope would create prestart:tu-spawn beside task-1 for the same transcript, with no later close for the additional row.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"content": [{"type": "text", "text": "Checked them."}]}
	}`))
	assert.Equal(t, []string{"task-1"}, agenttest.RowKeys(sink),
		"an unreadable spawn span still costs no second row")
}

// A failed child-link write can leave no transcript from which to read the original span.
// Do not treat the restarting call ID as a replacement spawn span or create a child through that call.
func TestClaude_ARestartOfAnUnlinkedRowOpensNoChildUnderTheRestartCall(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agentapi.NewProviderServices(sink))
	spawnAndFinishSubagent(t, a, sink)
	sink.UnlinkBackgroundTask("task-1")

	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "Also check the tests.")

	assert.NotContains(t, sink.ChildAgentIDs(), "child-of-tu-send",
		"a re-registration's tool_use id is not a spawn span")

	// A row without its child link still retains the original task identity.
	// The first start records childTask, and completion preserves it.
	// The forwarded envelope resolves through that map instead of opening an additional early row.
	a.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-spawn",
		"message": {"content": [{"type": "text", "text": "Checked them."}]}
	}`))
	assert.Equal(t, []string{"task-1"}, agenttest.RowKeys(sink),
		"an unlinked row still costs no second row")
}

// A restarted child forwards output under its original spawn span while task_started identifies the restarting call.
// Retain exactly one registry row for either restart form.
// A second spawn-span row would link the same transcript, list the child twice, and open one tab from both entries.
//
// Both native restart forms omit the original span from their task_started event.
// Each therefore requires its own restart evidence.
func TestClaude_RestartedSubagentKeepsOneRegistryRow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		restart func(a *Agent)
		// want is every row key the run legitimately leaves behind, sorted. The
		// wake case runs a shell of its own, so it owns one more.
		want []string
	}{
		{"SendMessage", func(a *Agent) {
			sendMessageTo(a, "tu-send", "task-1")
			restartTaskStarted(a, "tu-send", "Also check the tests.")
		}, []string{"task-1"}},
		{"shell wake", func(a *Agent) {
			finishShellTask(a, "shell-1")
			a.HandleOutput([]byte(`{
				"type": "system", "subtype": "task_started",
				"task_id": "task-1", "task_type": "local_agent",
				"prompt": ` + strconv.Quote(wakePrompt("shell-1")) + `
			}`))
		}, []string{"shell-1", "task-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a := newTestAgent(agentapi.NewProviderServices(sink))
			spawnAndFinishSubagent(t, a, sink)
			tc.restart(a)
			a.HandleOutput([]byte(`{
				"type": "assistant",
				"parent_tool_use_id": "tu-spawn",
				"message": {"content": [{"type": "text", "text": "Checked them."}]}
			}`))

			assert.Equal(t, tc.want, agenttest.RowKeys(sink),
				"the restarted run keeps the row it already had")
		})
	}
}
