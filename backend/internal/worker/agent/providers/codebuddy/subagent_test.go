package codebuddy

import (
	"encoding/json"
	"strings"
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
	index.rememberChildRoute("spawn", "old-row", "", false)
	check("spawn", "old-row", "")
	index.rememberChildRoute("spawn", "old-row", "child", false)
	check("spawn", "old-row", "child")
	index.rememberChildRoute("spawn", "current-row", "child", false)
	index.rememberChildRoute("alias", "current-row", "child", false)
	index.rememberChildRoute("other", "other-row", "other-child", false)
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

func TestCodebuddyLateChildFrameDoesNotReviveFinishedRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU1","name":"Agent","input":{"description":"Inspect","prompt":"Run check."}}]}}`))
	a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"TU1","content":"done","is_error":false}]}}`))

	row, ok := sink.BackgroundTask("TU1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, row.Status)
	childID := row.ChildAgentID
	require.NotEmpty(t, childID)

	// A late forwarded child frame must keep the finished row closed and never revive it.
	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"TU1","message":{"role":"assistant","content":[{"type":"text","text":"late child note"}]}}`))
	row, ok = sink.BackgroundTask("TU1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Empty(t, sink.RevivedTasks())
	messages := sink.Child(childID).Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[1].Content), "late child note")

	// A forwarded frame for an unknown parent tool must not open a phantom running row.
	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"unknown-parent","message":{"role":"assistant","content":[{"type":"text","text":"orphan"}]}}`))
	_, exists := sink.BackgroundTask("unknown-parent")
	assert.False(t, exists)
	assert.Equal(t, 2, sink.MessageCount(), "forwarded child frames stay out of the root transcript")
}

func TestCodebuddyObserveAgentToolResultsBackgroundVsError(t *testing.T) {
	t.Parallel()
	t.Run("background launch success before task_started stays running", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-bg-ok","name":"Agent","input":{"description":"BG worker","prompt":"Work.","run_in_background":true}}]}}`))
		a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"TU-bg-ok","content":"Launched","is_error":false}]}}`))

		row, ok := sink.BackgroundTask("TU-bg-ok")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusRunning, row.Status)
	})

	t.Run("background launch error before task_started closes as failed", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-bg-err","name":"Agent","input":{"description":"BG worker","prompt":"Work.","run_in_background":true}}]}}`))
		a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"TU-bg-err","content":"spawn failed","is_error":true}]}}`))

		row, ok := sink.BackgroundTask("TU-bg-err")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusFailed, row.Status)
	})

	t.Run("background launch error after task_started closes renamed row as failed", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-bg-err2","name":"Agent","input":{"description":"BG worker","prompt":"Work.","run_in_background":true}}]}}`))
		a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-bg-err2","tool_use_id":"TU-bg-err2","task_type":"Agent","description":"BG worker"}`))
		a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"TU-bg-err2","content":"spawn failed","is_error":true}]}}`))

		row, ok := sink.BackgroundTask("task-bg-err2")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusFailed, row.Status)
	})
}

func TestCodebuddyTurnEndAndProcessExitCloseForegroundChildrenOnly(t *testing.T) {
	t.Parallel()
	t.Run("handleResult closes foreground rows and keeps background rows", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-fg","name":"Agent","input":{"description":"FG child","prompt":"Do foreground work."}},{"type":"tool_use","id":"TU-bg","name":"Agent","input":{"description":"BG child","prompt":"Do background work.","run_in_background":true}}]}}`))
		// A background task_started with empty tool_use_id uses task_id as its spawnID;
		// foreground closing must rely on the explicit background flag, not rowKey == spawnID.
		a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-no-tool","task_type":"local_agent","description":"Toolless BG"}`))

		a.HandleOutput([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["boom"]}`))

		fg, ok := sink.BackgroundTask("TU-fg")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusFailed, fg.Status)

		bg, ok := sink.BackgroundTask("TU-bg")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusRunning, bg.Status)

		toollessBG, ok := sink.BackgroundTask("task-no-tool")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusRunning, toollessBG.Status)
	})

	t.Run("normal handleResult closes foreground row with unknown outcome", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-fg-ok","name":"Agent","input":{"description":"FG child","prompt":"Work."}}]}}`))
		a.HandleOutput([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"done"}`))

		fg, ok := sink.BackgroundTask("TU-fg-ok")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, fg.Status)
	})

	t.Run("interrupted handleResult closes foreground row as stopped", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-fg-int","name":"Agent","input":{"description":"FG child","prompt":"Work."}}]}}`))
		a.mu.Lock()
		a.interruptRequests = map[uint64]struct{}{1: {}}
		a.mu.Unlock()
		a.HandleOutput([]byte(codebuddyResultAbortedStreaming))

		fg, ok := sink.BackgroundTask("TU-fg-int")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusStopped, fg.Status)
	})

	t.Run("cleanupAfterProcessExit closes foreground rows and keeps background rows", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-fg-exit","name":"Task","input":{"description":"FG exit","prompt":"Work."}},{"type":"tool_use","id":"TU-bg-exit","name":"Agent","input":{"description":"BG exit","prompt":"Work.","run_in_background":true}}]}}`))

		a.NoteIntentionalStop()
		a.cleanupAfterProcessExit()

		fg, ok := sink.BackgroundTask("TU-fg-exit")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusStopped, fg.Status)

		bg, ok := sink.BackgroundTask("TU-bg-exit")
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusRunning, bg.Status)
	})
}

func TestCodebuddyTaskTypeTaskOpensAndCloses(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-task-1","task_type":"Task","description":"Inspect package","prompt":"Check files."}`))

	row, ok := sink.BackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.NotEmpty(t, row.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-1","status":"completed","summary":"All clear."}`))
	row, ok = sink.BackgroundTask("task-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestCodebuddyOutOfOrderToolResultBeforeTaskStartedReusesChild(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	// Tool call arrives without run_in_background, and tool_result lands before task_started.
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-ooo","name":"Task","input":{"description":"Delayed BG","prompt":"Investigate."}}]}}`))
	before, ok := sink.BackgroundTask("TU-ooo")
	require.True(t, ok)
	require.NotEmpty(t, before.ChildAgentID)

	a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"TU-ooo","content":"Backgrounded","is_error":false}]}}`))
	closed, ok := sink.BackgroundTask("TU-ooo")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusSucceeded, closed.Status)

	// Late task_started must rename TU-ooo -> task-ooo, revive it, and reuse the existing child.
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-ooo","tool_use_id":"TU-ooo","task_type":"Task","description":"Delayed BG"}`))

	_, oldExists := sink.BackgroundTask("TU-ooo")
	assert.False(t, oldExists, "old spawn row key must be renamed to task_id")
	row, ok := sink.BackgroundTask("task-ooo")
	require.True(t, ok)
	assert.Equal(t, before.ChildAgentID, row.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, []string{"task-ooo"}, sink.RevivedTasks())
	assert.Len(t, sink.ChildAgentIDs(), 1)

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-ooo","status":"completed","summary":"Done."}`))
	row, ok = sink.BackgroundTask("task-ooo")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestCodebuddyRouteChildFramePostRestartAndEarlyArrival(t *testing.T) {
	t.Parallel()

	t.Run("routes forwarded child frame after restart when row was renamed from spawnID to taskID", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		beforeRestart := newOfflineAgent(t, sink)
		beforeRestart.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-bg-restart","name":"Agent","input":{"description":"BG worker","prompt":"Work.","run_in_background":true}}]}}`))
		beforeRestart.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-bg-restart","tool_use_id":"TU-bg-restart","task_type":"Agent","description":"BG worker"}`))
		row, ok := sink.BackgroundTask("task-bg-restart")
		require.True(t, ok)
		require.NotEmpty(t, row.ChildAgentID)

		// Simulate worker restart: fresh agent with empty in-memory tasks, sharing durable sink.
		afterRestart := newOfflineAgent(t, sink)
		afterRestart.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"TU-bg-restart","message":{"role":"assistant","content":[{"type":"text","text":"post-restart child output"}]}}`))

		messages := sink.Child(row.ChildAgentID).Messages()
		require.Len(t, messages, 2, "post-restart forwarded child frame must reach the renamed child's transcript")
		assert.Contains(t, string(messages[1].Content), "post-restart child output")
		_, phantomExists := sink.BackgroundTask("TU-bg-restart")
		assert.False(t, phantomExists, "routing must not recreate the old pre-rename row key")
	})

	t.Run("routes early forwarded child frame arriving before parent tool_use without phantom row", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)

		// Forwarded child output arrives before the parent tool_use frame.
		a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"TU-early","message":{"role":"assistant","content":[{"type":"text","text":"early child output"}]}}`))
		_, beforeRowExists := sink.BackgroundTask("TU-early")
		assert.False(t, beforeRowExists, "early frame must not open a phantom registry row before spawn")

		// Parent tool_use arrives next and adopts the same child transcript.
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"TU-early","name":"Agent","input":{"description":"Early child","prompt":"Start early."}}]}}`))
		row, ok := sink.BackgroundTask("TU-early")
		require.True(t, ok)
		require.NotEmpty(t, row.ChildAgentID)
		assert.Len(t, sink.ChildAgentIDs(), 1)

		messages := sink.Child(row.ChildAgentID).Messages()
		require.NotEmpty(t, messages)
		var foundEarly bool
		for _, m := range messages {
			if strings.Contains(string(m.Content), "early child output") {
				foundEarly = true
				break
			}
		}
		assert.True(t, foundEarly, "early child output must be preserved in the adopted child transcript")
	})
}
