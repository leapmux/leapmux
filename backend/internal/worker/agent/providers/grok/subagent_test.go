package grok

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// grokToolMetaFixture is the `_meta` that states a Grok tool call's identity.
func grokToolMetaFixture(name string) map[string]any {
	return map[string]any{"x.ai/tool": map[string]any{"version": 1, "name": name, "kind": "task", "namespace": "grok_build"}}
}

// spawnCall is the first frame of a spawn, as Grok sends it.
func spawnCall(t *testing.T, toolCallID, description, prompt string, background bool) []byte {
	t.Helper()
	return sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": toolCallID, "title": "spawn_subagent",
		"rawInput": map[string]any{"prompt": prompt, "description": description, "background": background},
		"_meta":    grokToolMetaFixture("spawn_subagent"),
	})
}

// spawned is the notification that a child session started.
func spawned(t *testing.T, subagentID, description string, extra map[string]any) []byte {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": "subagent_spawned", "subagent_id": subagentID, "child_session_id": subagentID,
		"parent_session_id": grokTestSession, "subagent_type": "general-purpose", "description": description,
	}
	for key, value := range extra {
		update[key] = value
	}
	return notification(t, grokTestSession, update)
}

// finished is the notification that a child session ended.
func finished(t *testing.T, subagentID, status, output string) []byte {
	t.Helper()
	return notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "subagent_finished", "subagent_id": subagentID, "child_session_id": subagentID,
		"status": status, "output": output,
	})
}

// childChunk is one message chunk in a child session.
func childChunk(t *testing.T, childSession, text string) []byte {
	t.Helper()
	return sessionUpdate(t, childSession, map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text},
	})
}

// childTurnCompleted is the end of one turn of a child session.
func childTurnCompleted(t *testing.T, childSession string) []byte {
	t.Helper()
	return notification(t, childSession, map[string]any{"sessionUpdate": "turn_completed", "prompt_id": "p", "stop_reason": "end_turn"})
}

// childTexts reads the assembled text rows of one child transcript.
func childTexts(t *testing.T, child *agenttest.Sink) []string {
	t.Helper()
	var texts []string
	for _, message := range child.Messages() {
		var envelope map[string]string
		if json.Unmarshal(message.Content, &envelope) != nil || envelope["type"] != "assembled_message" {
			continue
		}
		texts = append(texts, envelope["text"])
	}
	return texts
}

// reportTexts reads the report notifications of one child transcript.
func reportTexts(child *agenttest.Sink) []string {
	var texts []string
	for _, notification := range child.LeapMuxNotifications() {
		if text, ok := notification["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

func TestGrokBlockingSubagentLifecycle(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	a.HandleOutput(spawnCall(t, "call_2_0", "List files", "FGCHILD: list the working directory.", false))
	task, ok := sink.BackgroundTask("call_2_0")
	require.True(t, ok, "the spawn opens a row at once")
	assert.Equal(t, bgtask.StatusRunning, task.Status)
	assert.Equal(t, "List files", task.Title)
	require.NotEmpty(t, task.ChildAgentID)
	child := sink.Child(task.ChildAgentID)

	a.HandleOutput(spawned(t, "sub-1", "List files", nil))
	a.HandleOutput(childChunk(t, "sub-1", "Listing "))
	a.HandleOutput(childChunk(t, "sub-1", "now."))
	a.HandleOutput(childTurnCompleted(t, "sub-1"))
	assert.Equal(t, []string{"Listing now."}, childTexts(t, child), "the child session's text reaches the child transcript")

	a.HandleOutput(finished(t, "sub-1", "completed", "Done."))
	task, _ = sink.BackgroundTask("call_2_0")
	assert.Equal(t, bgtask.StatusCompleted, task.Status)

	// The spawn's own result arrives after the finish, with the same report.
	a.HandleOutput(sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_2_0", "status": "completed",
		"content":   []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Done.\n\n<subagent_result>\nsubagent_id: sub-1\n</subagent_result>"}}},
		"rawOutput": map[string]any{"type": "SubagentCompleted", "output": "Done.", "subagent_id": "sub-1"},
	}))
	task, _ = sink.BackgroundTask("call_2_0")
	assert.Equal(t, bgtask.StatusCompleted, task.Status)
	assert.Equal(t, []string{"Done."}, reportTexts(child), "the two reports of one child are stored once")
	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "Listing now.", "the child's text stays out of the parent")
	}
}

func TestGrokBackgroundSubagentLinksByTheIDItsResultStates(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	a.HandleOutput(spawnCall(t, "call_8_0", "Say done", "BGCHILD: say done.", true))
	a.HandleOutput(sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_8_0", "status": "completed",
		"rawOutput": map[string]any{"type": "Text", "text": "Subagent started in background.\nsubagent_id: bg-1\ndescription: Say done\n"},
	}))
	task, _ := sink.BackgroundTask("call_8_0")
	assert.Equal(t, bgtask.StatusRunning, task.Status, "a background child outlives its spawn call")

	a.HandleOutput(spawned(t, "bg-1", "Say done", nil))
	a.HandleOutput(childChunk(t, "bg-1", "BG child done."))
	a.HandleOutput(childTurnCompleted(t, "bg-1"))
	assert.Equal(t, []string{"BG child done."}, childTexts(t, sink.Child(task.ChildAgentID)))

	a.HandleOutput(finished(t, "bg-1", "completed", "BG child done."))
	task, _ = sink.BackgroundTask("call_8_0")
	assert.Equal(t, bgtask.StatusCompleted, task.Status)
	_, extra := sink.BackgroundTask("bg-1")
	assert.False(t, extra, "the linked child takes no second row")
}

func TestGrokSubagentsWithTheSameDescriptionLinkInOrder(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnCall(t, "call-a", "Check", "first", false))
	a.HandleOutput(spawnCall(t, "call-b", "Check", "second", false))

	a.HandleOutput(spawned(t, "sub-a", "Check", nil))
	a.HandleOutput(spawned(t, "sub-b", "Check", nil))
	a.HandleOutput(finished(t, "sub-b", "failed", "boom"))

	first, _ := sink.BackgroundTask("call-a")
	second, _ := sink.BackgroundTask("call-b")
	assert.Equal(t, bgtask.StatusRunning, first.Status)
	assert.Equal(t, bgtask.StatusFailed, second.Status, "the second child belongs to the second spawn")
}

func TestGrokSubagentWithoutASpawnCallTakesItsOwnRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	// A goal starts its planner with no tool call of the parent's.
	a.HandleOutput(spawned(t, "planner-1", "goal plan writer", nil))
	task, ok := sink.BackgroundTask("planner-1")
	require.True(t, ok)
	assert.Equal(t, "goal plan writer", task.Title)
	assert.Equal(t, bgtask.StatusRunning, task.Status)
	require.NotEmpty(t, task.ChildAgentID)

	a.HandleOutput(childChunk(t, "planner-1", "Planning."))
	a.HandleOutput(childTurnCompleted(t, "planner-1"))
	a.HandleOutput(finished(t, "planner-1", "cancelled", ""))
	task, _ = sink.BackgroundTask("planner-1")
	assert.Equal(t, bgtask.StatusStopped, task.Status)
	assert.Equal(t, []string{"Planning."}, childTexts(t, sink.Child(task.ChildAgentID)))
}

func TestGrokSubagentStatusMapping(t *testing.T) {
	t.Parallel()
	assert.Equal(t, bgtask.StatusCompleted, grokSubagentStatus("completed"))
	assert.Equal(t, bgtask.StatusFailed, grokSubagentStatus("failed"))
	assert.Equal(t, bgtask.StatusStopped, grokSubagentStatus("cancelled"))
	assert.Equal(t, bgtask.StatusStopped, grokSubagentStatus("something-new"))
}

func TestGrokInterruptChildCancelsTheSubagent(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnCall(t, "call_2_0", "List files", "go", false))
	a.HandleOutput(spawned(t, "sub-1", "List files", nil))

	require.NoError(t, a.InterruptChild("call_2_0"))

	sent := requestsFor(requests(), grokSubagentCancelMethod)
	require.Len(t, sent, 1)
	assert.Equal(t, map[string]any{"subagentId": "sub-1"}, sent[0].Params)
}

func TestGrokInterruptChildOfAnUnknownRowIsNotReady(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)
	// The spawn exists, but no child session belongs to it yet.
	a.HandleOutput(spawnCall(t, "call_2_0", "List files", "go", false))

	assert.ErrorIs(t, a.InterruptChild("call_2_0"), agent.ErrChildRouteNotReady)
	assert.ErrorIs(t, a.InterruptChild("never"), agent.ErrChildRouteNotReady)
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), grokSubagentCancelMethod))
}

func TestGrokInterruptChildReportsARefusal(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == grokSubagentCancelMethod {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"gone"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.HandleOutput(spawned(t, "sub-1", "helper", nil))

	assert.Error(t, a.InterruptChild("sub-1"))
}

func TestGrokWorkflowRunKeepsOneGroupedRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	workflow := func(status string, extra map[string]any) []byte {
		update := map[string]any{
			"sessionUpdate": "workflow_updated", "run_id": "run-1", "name": "deep-research",
			"objective": "Compare the caches", "status": status, "current_phase": "Gather",
			"active_agents": 2,
		}
		for key, value := range extra {
			update[key] = value
		}
		return notification(t, grokTestSession, update)
	}

	a.HandleOutput(workflow("active", nil))
	row, ok := sink.BackgroundTask("workflow:run-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindWorkflow, row.Kind)
	assert.Equal(t, "deep-research: Compare the caches", row.Title)
	assert.Equal(t, "run-1", row.GroupKey)
	assert.Equal(t, "deep-research", row.GroupLabel)
	assert.Equal(t, "Gather · 2 agents running", row.ActiveForm)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	// An agent of the run joins the run's group.
	a.HandleOutput(spawned(t, "wf-agent-1", "researcher", map[string]any{"workflow_run_id": "run-1"}))
	agentRow, ok := sink.BackgroundTask("wf-agent-1")
	require.True(t, ok)
	assert.Equal(t, "run-1", agentRow.GroupKey)
	assert.Equal(t, "deep-research", agentRow.GroupLabel)

	a.HandleOutput(workflow("infra_paused", map[string]any{"pause_message": "Rate limited"}))
	row, _ = sink.BackgroundTask("workflow:run-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "a paused run can resume")
	assert.Equal(t, "infra paused: Rate limited", row.ActiveForm)

	a.HandleOutput(workflow("complete", nil))
	row, _ = sink.BackgroundTask("workflow:run-1")
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
}

// subagentReports returns the report notifications of one transcript.
func subagentReports(sink *agenttest.Sink) []map[string]any {
	var reports []map[string]any
	for _, notification := range sink.LeapMuxNotifications() {
		if notification["type"] == "subagent_report" {
			reports = append(reports, notification)
		}
	}
	return reports
}

// A completed run states its result summary, which is the report of the run: a
// `/deep-research` run ends with its findings there. The row of a run has no
// transcript of its own, so the report reaches the transcript of the agent that
// started the run, once however often Grok repeats the final update.
func TestGrokWorkflowRunReportsItsResultSummary(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	workflow := func(status, summary string) []byte {
		update := map[string]any{
			"sessionUpdate": "workflow_updated", "run_id": "run-1", "name": "deep-research",
			"objective": "Compare the caches", "status": status,
		}
		if summary != "" {
			update["result_summary"] = summary
		}
		return notification(t, grokTestSession, update)
	}

	a.HandleOutput(workflow("active", ""))
	assert.Empty(t, subagentReports(&sink.Sink), "a running run reports nothing")
	a.HandleOutput(workflow("complete", "The cache wins.\n\n_Full report: /w/report.md_"))
	a.HandleOutput(workflow("complete", "The cache wins.\n\n_Full report: /w/report.md_"))

	reports := subagentReports(&sink.Sink)
	require.Len(t, reports, 1)
	assert.Equal(t, "The cache wins.\n\n_Full report: /w/report.md_", reports[0]["text"])
	assert.Equal(t, "deep-research: Compare the caches", reports[0]["label"])
	assert.Equal(t, bgtask.StatusWire(bgtask.StatusCompleted), reports[0]["status"])
	row, _ := sink.BackgroundTask("workflow:run-1")
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
}

// A run that ended without completing states no result, and a completed run
// with an empty summary has nothing to report.
func TestGrokWorkflowRunWithoutASummaryReportsNothing(t *testing.T) {
	t.Parallel()
	for _, update := range []map[string]any{
		{"status": "failed", "result_summary": "partial"},
		{"status": "complete", "result_summary": "  "},
		{"status": "complete"},
	} {
		a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
		update["sessionUpdate"] = "workflow_updated"
		update["run_id"] = "run-1"
		update["name"] = "deep-research"
		a.HandleOutput(notification(t, grokTestSession, update))
		assert.Empty(t, subagentReports(&sink.Sink), "%v", update)
	}
}

// A context clear ends the rows that the outgoing session opened: a subagent
// that runs in the background, a background command and a workflow run. Grok
// reports their ends under the retired session, which the base no longer
// reads, so nothing else would end them.
func TestGrokClearContextEndsTheRowsOfTheOutgoingSession(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, openingSession("session-2"))
	a.HandleOutput(spawned(t, "sub-background", "Watcher", nil))
	a.HandleOutput(frame(t, map[string]any{
		"method": grokTaskBackgroundedMethod,
		"params": map[string]any{"sessionId": grokTestSession, "update": map[string]any{
			"sessionUpdate": "task_backgrounded", "task_id": "t7", "command": "npm run dev",
		}},
	}))
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "workflow_updated", "run_id": "run-1", "name": "deep-research", "status": "active",
	}))

	_, err := a.ClearContext()
	require.NoError(t, err)

	for _, rowKey := range []string{"sub-background", "task:t7", "workflow:run-1"} {
		row, ok := sink.BackgroundTask(rowKey)
		require.True(t, ok, rowKey)
		assert.Equal(t, bgtask.StatusStopped, row.Status, rowKey)
	}
}

func TestGrokWorkflowStatusMapping(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bgtask.Status{
		"active": bgtask.StatusRunning, "user_paused": bgtask.StatusRunning, "blocked": bgtask.StatusRunning,
		"budget_limited": bgtask.StatusRunning, "complete": bgtask.StatusCompleted, "failed": bgtask.StatusFailed,
		"cancelled": bgtask.StatusStopped, "interrupted": bgtask.StatusStopped,
	} {
		assert.Equal(t, want, grokWorkflowStatus(status), status)
	}
	assert.Equal(t, "", grokWorkflowActivity("active", "", "", 0))
	assert.Equal(t, "blocked", grokWorkflowActivity("blocked", "Gather", "", 1))
	assert.Equal(t, "user paused: Waiting for you", grokWorkflowActivity("user_paused", "Gather", " Waiting for you ", 2),
		"a pause states its reason, not the phase")
	assert.Equal(t, "budget limited", grokWorkflowActivity("budget_limited", "", "  ", 0))
	assert.Equal(t, "Gather", grokWorkflowActivity("active", " Gather ", "", 0))
	assert.Equal(t, "3 agents running", grokWorkflowActivity("active", "", "", 3))
	assert.Equal(t, "", grokWorkflowActivity("active", "", "", -1), "a negative count states no agents")
}

func TestGrokBackgroundCommandRows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		snapshot map[string]any
		want     bgtask.Status
	}{
		{name: "success", snapshot: map[string]any{"task_id": "t1", "exit_code": 0, "completed": true}, want: bgtask.StatusCompleted},
		{name: "failure", snapshot: map[string]any{"task_id": "t1", "exit_code": 2, "completed": true}, want: bgtask.StatusFailed},
		{name: "killed", snapshot: map[string]any{"task_id": "t1", "explicitly_killed": true}, want: bgtask.StatusStopped},
		{name: "signal", snapshot: map[string]any{"task_id": "t1", "signal": "SIGTERM"}, want: bgtask.StatusStopped},
		{name: "numeric id", snapshot: map[string]any{"task_id": 1, "exit_code": 0}, want: bgtask.StatusCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			backgroundID := "t1"
			if tc.name == "numeric id" {
				backgroundID = "1"
			}
			a.HandleOutput(frame(t, map[string]any{
				"method": grokTaskBackgroundedMethod,
				"params": map[string]any{"sessionId": grokTestSession, "update": map[string]any{
					"sessionUpdate": "task_backgrounded", "tool_call_id": "call_1", "task_id": tc.snapshot["task_id"],
					"command": "npm test", "cwd": "/w", "output_file": "/tmp/out",
				}},
			}))
			row, ok := sink.BackgroundTask("task:" + backgroundID)
			require.True(t, ok)
			assert.Equal(t, bgtask.KindShell, row.Kind)
			assert.Equal(t, "npm test", row.Title)
			assert.True(t, row.TitleIsCommand)
			assert.Equal(t, bgtask.StatusRunning, row.Status)

			a.HandleOutput(frame(t, map[string]any{
				"method": grokTaskCompletedMethod,
				"params": map[string]any{"sessionId": grokTestSession, "update": map[string]any{
					"sessionUpdate": "task_completed", "task_snapshot": tc.snapshot,
				}},
			}))
			row, _ = sink.BackgroundTask("task:" + backgroundID)
			assert.Equal(t, tc.want, row.Status)
		})
	}
}

func TestGrokBackgroundCommandPrefersItsDescription(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"method": grokTaskBackgroundedMethod,
		"params": map[string]any{"sessionId": grokTestSession, "update": map[string]any{
			"sessionUpdate": "task_backgrounded", "task_id": "t2", "command": "npm run dev", "description": "Dev server",
		}},
	}))
	row, ok := sink.BackgroundTask("task:t2")
	require.True(t, ok)
	assert.Equal(t, "Dev server", row.Title)
	assert.False(t, row.TitleIsCommand)
}

func TestGrokBackgroundCommandWithoutAnIDWritesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"method": grokTaskBackgroundedMethod,
		"params": map[string]any{"sessionId": grokTestSession, "update": map[string]any{"sessionUpdate": "task_backgrounded", "command": "x"}},
	}))
	a.HandleOutput(frame(t, map[string]any{
		"method": grokTaskCompletedMethod,
		"params": map[string]any{"sessionId": grokTestSession, "update": map[string]any{"sessionUpdate": "task_completed", "task_snapshot": map[string]any{}}},
	}))
	assert.Empty(t, sink.BackgroundTasks())
}

func TestGrokClearDropsTheChildLinks(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{Options: map[string]string{contracts.GrokOptionApprovalMode: contracts.GrokApprovalModeAuto}}, nil)
	a.HandleOutput(spawnCall(t, "call_2_0", "List files", "go", false))
	a.HandleOutput(spawned(t, "sub-1", "List files", nil))
	a.HandleOutput(spawnCall(t, "call_3_0", "Still pending", "go", false))
	a.HandleOutput(frame(t, map[string]any{
		"id": 9, "method": contracts.GrokMethodAskUserQuestion,
		"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_q", "questions": []any{}},
	}))
	ownPromptID(t, a)
	a.HandleOutput(queueChanged(t, grokTestSession, "goal-round-1"))
	require.Equal(t, "call_2_0", a.childRowForSession("sub-1"))

	a.clearProviderState()

	assert.Empty(t, a.childRowForSession("sub-1"))
	assert.ErrorIs(t, a.InterruptChild("call_2_0"), agent.ErrChildRouteNotReady)
	a.stateMu.Lock()
	assert.Equal(t, childState{}, a.children, "no link of the old session stays")
	assert.Empty(t, a.controls.byToolCall, "no request of the old session stays")
	assert.Equal(t, turnState{}, a.turns, "no prompt of the old session stays")
	assert.Equal(t, contracts.GrokApprovalModeAuto, a.approval.current, "the approval mode belongs to the process and stays")
	a.stateMu.Unlock()
}

// A subagent whose child session has an id of its own is still one child: its
// updates reach the child's transcript, and the end of a turn in that session
// stores the text that the turn assembled.
func TestGrokSubagentWithASeparateChildSessionEndsItsTurns(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnCall(t, "call_2_0", "List files", "go", false))
	task, ok := sink.BackgroundTask("call_2_0")
	require.True(t, ok)
	child := sink.Child(task.ChildAgentID)

	a.HandleOutput(spawned(t, "sub-1", "List files", map[string]any{"child_session_id": "child-session-1"}))
	a.HandleOutput(childChunk(t, "child-session-1", "Listing now."))
	a.HandleOutput(childTurnCompleted(t, "child-session-1"))

	assert.Equal(t, []string{"Listing now."}, childTexts(t, child), "the turn end stores the child's text")
	require.NoError(t, a.InterruptChild("call_2_0"), "the row still reaches the subagent under its own id")
}

func TestGrokChildStateLinkReplacesAnEarlierLink(t *testing.T) {
	t.Parallel()
	var children childState
	_, ok := children.unlink("never")
	assert.False(t, ok, "an unknown subagent unlinks nothing")

	children.link("sub-1", linkedSubagent{rowKey: "row-a", childSession: "session-a"})
	children.link("sub-1", linkedSubagent{rowKey: "row-b", childSession: "session-b"})
	assert.Equal(t, map[string]string{"row-b": "sub-1"}, children.subagentByRow, "no reverse entry of the replaced link stays")
	assert.Equal(t, map[string]string{"session-b": "sub-1"}, children.subagentBySession)

	linked, ok := children.unlink("sub-1")
	require.True(t, ok)
	assert.Equal(t, linkedSubagent{rowKey: "row-b", childSession: "session-b"}, linked)
	assert.Empty(t, children.subagents)
	assert.Empty(t, children.subagentByRow)
	assert.Empty(t, children.subagentBySession)
}

func TestGrokSpawnWithNoDescriptionTakesAFallbackTitle(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnCall(t, "call_1", "  ", "go", false))
	row, ok := sink.BackgroundTask("call_1")
	require.True(t, ok)
	assert.Equal(t, "Grok subagent", row.Title)

	// A tool call of another tool, or with no tool identity, opens no row.
	a.HandleOutput(sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "call_2", "title": "read_file", "_meta": grokToolMetaFixture("read_file"),
	}))
	a.HandleOutput(sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "call_3", "title": "spawn_subagent", "_meta": map[string]any{"x.ai/tool": map[string]any{"kind": "task"}},
	}))
	assert.Len(t, sink.BackgroundTasks(), 1)
}

// A blocking spawn whose call failed closes its row, and it waits for no child
// any more: a later child with the same description takes a row of its own.
func TestGrokFailedBlockingSpawnClosesItsRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnCall(t, "call_2_0", "List files", "go", false))
	a.HandleOutput(sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_2_0", "status": "failed",
	}))
	row, ok := sink.BackgroundTask("call_2_0")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status)

	a.HandleOutput(spawned(t, "sub-late", "List files", nil))
	_, own := sink.BackgroundTask("sub-late")
	assert.True(t, own, "the failed spawn no longer claims a child")
}

// A background spawn can state its subagent id in its content alone.
func TestGrokBackgroundSpawnReadsTheIDFromItsContent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnCall(t, "call_8_0", "Say done", "BGCHILD: say done.", true))
	a.HandleOutput(sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "call_8_0", "status": "completed",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Subagent started in background.\nsubagent_id: bg-1\n"}}},
	}))
	task, _ := sink.BackgroundTask("call_8_0")
	assert.Equal(t, bgtask.StatusRunning, task.Status)

	a.HandleOutput(spawned(t, "bg-1", "A description that differs", nil))
	_, extra := sink.BackgroundTask("bg-1")
	assert.False(t, extra, "the id links the child, whatever its description states")
	a.HandleOutput(finished(t, "bg-1", "completed", "Done."))
	task, _ = sink.BackgroundTask("call_8_0")
	assert.Equal(t, bgtask.StatusCompleted, task.Status)
}

// An agent of a workflow run belongs to the run, never to a blocking spawn of
// the parent with the same description.
func TestGrokWorkflowAgentNeverTakesAPendingSpawn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawnCall(t, "call_1", "researcher", "go", false))

	a.HandleOutput(spawned(t, "wf-agent-1", "researcher", map[string]any{"workflow_run_id": "run-1"}))
	agentRow, ok := sink.BackgroundTask("wf-agent-1")
	require.True(t, ok, "the workflow agent takes a row of its own")
	assert.Equal(t, "run-1", agentRow.GroupKey)

	a.HandleOutput(spawned(t, "sub-1", "researcher", nil))
	_, extra := sink.BackgroundTask("sub-1")
	assert.False(t, extra, "the spawn still waits for its own child")
	assert.Equal(t, "call_1", a.childRowForSession("sub-1"))
}

func TestGrokSubagentFinishedReportsTheError(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawned(t, "sub-1", "helper", nil))
	task, _ := sink.BackgroundTask("sub-1")
	child := sink.Child(task.ChildAgentID)

	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "subagent_finished", "subagent_id": "sub-1", "status": "failed", "error": "rate limited",
	}))

	task, _ = sink.BackgroundTask("sub-1")
	assert.Equal(t, bgtask.StatusFailed, task.Status)
	assert.Equal(t, []string{"rate limited"}, reportTexts(child), "a child with no output reports its error")
}

// A child that states no session of its own runs in the session of its id, and
// a child that states no description takes its type as the title.
func TestGrokSubagentSpawnedWithoutASessionOrADescription(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "subagent_spawned", "subagent_id": "sub-x", "subagent_type": "explore",
	}))
	task, ok := sink.BackgroundTask("sub-x")
	require.True(t, ok)
	assert.Equal(t, "explore", task.Title)

	a.HandleOutput(childChunk(t, "sub-x", "Exploring."))
	a.HandleOutput(childTurnCompleted(t, "sub-x"))
	assert.Equal(t, []string{"Exploring."}, childTexts(t, sink.Child(task.ChildAgentID)))
}

func TestGrokUnreadableSubagentNotificationsChangeNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	for _, update := range []map[string]any{
		{"sessionUpdate": "subagent_spawned", "description": "no id"},
		{"sessionUpdate": "subagent_spawned", "subagent_id": 7},
		{"sessionUpdate": "subagent_finished", "status": "completed"},
		{"sessionUpdate": "workflow_updated", "name": "no run id", "status": "active"},
		{"sessionUpdate": "workflow_updated", "run_id": 7},
		{"sessionUpdate": "task_backgrounded", "task_id": true, "command": "x"},
		{"sessionUpdate": "task_completed", "task_snapshot": "text"},
	} {
		a.HandleOutput(notification(t, grokTestSession, update))
	}
	assert.Empty(t, sink.BackgroundTasks())
}

func TestGrokTaskID(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		`"t1"`: "t1", `1`: "1", `1.5`: "1.5", `12345678901234567890`: "12345678901234567890",
		`true`: "", `{}`: "", `null`: "", ``: "",
	} {
		assert.Equal(t, want, grokTaskID(json.RawMessage(raw)), raw)
	}
}

func TestGrokBackgroundCommandTakesTheMonitorDescription(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"method": grokTaskBackgroundedMethod,
		"params": map[string]any{"sessionId": grokTestSession, "update": map[string]any{
			"sessionUpdate": "task_backgrounded", "task_id": "t3", "command": "tail -f log", "monitor_description": "Watch the log",
		}},
	}))
	row, ok := sink.BackgroundTask("task:t3")
	require.True(t, ok)
	assert.Equal(t, "Watch the log", row.Title)
	assert.False(t, row.TitleIsCommand)
}

func TestGrokWorkflowRunWithNoNameOrObjective(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "workflow_updated", "run_id": "run-2", "name": "  ", "objective": " ", "status": "active",
	}))
	row, ok := sink.BackgroundTask("workflow:run-2")
	require.True(t, ok)
	assert.Equal(t, "Workflow", row.Title)
	assert.Equal(t, "Workflow", row.GroupLabel)
	assert.Empty(t, row.ActiveForm)
}
