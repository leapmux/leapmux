package kimi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The subagent tests replay the order the 2.0.2 server states a subagent in
// (kap-child and kap-swarm probes): the subagent's first turn.started arrives
// BEFORE the subagent.spawned that links it to the call that started it.

type kimiEventFeeder interface {
	feed(t *testing.T, payload map[string]any)
}

// spawnAgent replays one foreground Agent call up to its subagent's first text.
func spawnAgent(t *testing.T, feeder kimiEventFeeder, subagentID, callID string, extra map[string]any) {
	t.Helper()
	feeder.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": callID, "name": contracts.KimiToolAgent,
		"args": map[string]any{"prompt": "Review the diff.", "description": "Reviewer"}})
	feeder.feed(t, map[string]any{"type": contracts.KimiEventAgentCreated, "agentId": subagentID})
	feeder.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "agentId": subagentID, "turnId": 0,
		"origin": map[string]any{"kind": "system_trigger", "name": "subagent"}, "prompt": "<git-context>branch main</git-context>\nReview the diff."})
	feeder.feed(t, map[string]any{"type": contracts.KimiEventContextSpliced, "agentId": subagentID})
	feeder.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "agentId": subagentID, "turnId": 0, "toolCallId": "call_read", "name": contracts.KimiToolRead, "args": map[string]any{"path": "a.go"}})
	spawned := map[string]any{"type": contracts.KimiEventSubagentSpawned, "subagentId": subagentID, "subagentName": "explore",
		"parentToolCallId": callID, "parentAgentId": kimiMainAgentID, "description": "Reviewer", "taskId": "agent-task-" + subagentID}
	for key, value := range extra {
		spawned[key] = value
	}
	feeder.feed(t, spawned)
	feeder.feed(t, map[string]any{"type": contracts.KimiEventToolResult, "agentId": subagentID, "turnId": 0, "toolCallId": "call_read", "output": "package a"})
	feeder.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "agentId": subagentID, "turnId": 0, "delta": "Found it."})
}

func endSubagent(t *testing.T, feeder kimiEventFeeder, subagentID, eventName string, extra map[string]any) {
	t.Helper()
	feeder.feed(t, map[string]any{"type": contracts.KimiEventTurnStepCompleted, "agentId": subagentID, "turnId": 0})
	feeder.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "agentId": subagentID, "turnId": 0, "reason": "completed"})
	ended := map[string]any{"type": eventName, "subagentId": subagentID}
	for key, value := range extra {
		ended[key] = value
	}
	feeder.feed(t, ended)
}

func childOf(t *testing.T, sink *agenttest.ControlSink, rowKey string) (bgtask.Item, *agenttest.Sink) {
	t.Helper()
	row, ok := sink.BackgroundTask(rowKey)
	require.True(t, ok, "the registry has row %q", rowKey)
	require.NotEmpty(t, row.ChildAgentID)
	return row, sink.Child(row.ChildAgentID)
}

// userText reads the text of a user row a child transcript holds.
func userText(t *testing.T, message agenttest.Message) string {
	t.Helper()
	require.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, message.Source)
	var row struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(message.Content, &row))
	return row.Content
}

func TestKimiSubagentTranscript(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	spawnAgent(t, rig, "agent-0", "call_child", nil)
	endSubagent(t, rig, "agent-0", contracts.KimiEventSubagentCompleted, map[string]any{"resultSummary": "All good."})
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolResult, "turnId": 0, "toolCallId": "call_child", "output": "All good."})

	row, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, "Reviewer", row.Title)
	assert.Equal(t, "explore", row.Description)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)

	spawnSpan, err := rig.sink.ChildSpawnSpan(row.ChildAgentID)
	require.NoError(t, err)
	assert.Equal(t, kimiSpanID("session_1", kimiMainAgentID, 0, "call_child"), spawnSpan, "the child hangs off the Agent call's card")

	messages := child.Messages()
	require.GreaterOrEqual(t, len(messages), 4)
	assert.Equal(t, "Review the diff.", userText(t, messages[0]), "the spawn prompt opens the child transcript")
	assert.Equal(t, contracts.KimiEventToolCallStarted, eventType(t, messages[1]), "the events that waited for the link replay in order")
	assert.Equal(t, kimiSpanID("session_1", "agent-0", 0, "call_read"), messages[1].SpanID)
	assert.Equal(t, contracts.KimiEventToolResult, eventType(t, messages[2]))
	_, text := assembledRow(t, messages[3])
	assert.Equal(t, "Found it.", text)
	assert.Equal(t, []bool{true, false}, child.TurnActives(), "the child's own tab shows its turn")
	assert.NotEmpty(t, child.LeapMuxNotifications(), "the child transcript records its report")
	assert.Empty(t, rig.sink.LeapMuxNotifications(), "a foreground report reaches the parent through the call's own result")

	for _, active := range rig.sink.TurnActives() {
		assert.True(t, active, "no subagent event moves the main turn flag")
	}
	var spawnRow *agenttest.Message
	for i, message := range rig.sink.Messages() {
		if message.SpanID == kimiSpanID("session_1", kimiMainAgentID, 0, "call_child") && !message.Closing {
			spawnRow = &rig.sink.Messages()[i]
		}
	}
	require.NotNil(t, spawnRow)
	assert.True(t, spawnRow.NoSpan, "a spawn owns no span rail")
}

func TestKimiBackgroundSubagentReportsToTheParent(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	spawnAgent(t, rig, "agent-0", "call_child", map[string]any{"runInBackground": true})
	endSubagent(t, rig, "agent-0", contracts.KimiEventSubagentCompleted, map[string]any{"resultSummary": "Done in the background."})

	assert.NotEmpty(t, rig.sink.LeapMuxNotifications(), "a background subagent's result reaches the parent as its report")
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.NotEmpty(t, child.LeapMuxNotifications())
}

func TestKimiSubagentEndStatus(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		event  string
		extra  map[string]any
		status bgtask.Status
	}{
		{contracts.KimiEventSubagentCompleted, nil, bgtask.StatusCompleted},
		{contracts.KimiEventSubagentFailed, map[string]any{"error": map[string]any{"message": "provider exploded"}}, bgtask.StatusFailed},
		{contracts.KimiEventSubagentFailed, map[string]any{"error": "plain failure"}, bgtask.StatusFailed},
		{contracts.KimiEventSubagentCancelled, nil, bgtask.StatusStopped},
	} {
		t.Run(tc.event, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.startTurn(t, 0, contracts.KimiOriginUser)
			spawnAgent(t, rig, "agent-0", "call_child", nil)
			endSubagent(t, rig, "agent-0", tc.event, tc.extra)
			row, child := childOf(t, rig.sink, "session_1/agent-0")
			assert.Equal(t, tc.status, row.Status)
			if tc.status == bgtask.StatusFailed {
				last := child.Messages()[len(child.Messages())-1]
				_, text := assembledRow(t, last)
				assert.NotEmpty(t, text, "the failure is stated in the child transcript")
				assert.Equal(t, string(agent.MessageCompletionError), assembledCompletion(t, last))
			}
		})
	}
}

func TestKimiSwarmMembersAreOneWorkflow(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_swarm", "name": contracts.KimiToolAgentSwarm,
		"args": map[string]any{"description": "Audit the packages"}})
	for i, prompt := range []string{"Check package A.", "Check package B."} {
		id := fmt.Sprintf("agent-%d", i)
		rig.feed(t, map[string]any{"type": contracts.KimiEventAgentCreated, "agentId": id})
		rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentSpawned, "subagentId": id, "subagentName": "explore",
			"parentToolCallId": "call_swarm", "parentAgentId": kimiMainAgentID, "description": fmt.Sprintf("Audit #%d", i+1), "swarmIndex": i + 1})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "agentId": id, "turnId": 0,
			"origin": map[string]any{"kind": "system_trigger", "name": "subagent"}, "prompt": prompt})
	}

	rowA, childA := childOf(t, rig.sink, "session_1/agent-0")
	rowB, childB := childOf(t, rig.sink, "session_1/agent-1")
	assert.NotEqual(t, rowA.ChildAgentID, rowB.ChildAgentID, "each member has a transcript of its own")
	for _, row := range []bgtask.Item{rowA, rowB} {
		assert.Equal(t, bgtask.KindWorkflow, row.Kind)
		assert.Equal(t, "session_1/call_swarm", row.GroupKey)
		assert.Equal(t, "Audit the packages", row.GroupLabel)
	}
	require.NotEmpty(t, childA.Messages())
	require.NotEmpty(t, childB.Messages())
	assert.Equal(t, "Check package A.", userText(t, childA.Messages()[0]), "a member's own first prompt opens its transcript")
	assert.Equal(t, "Check package B.", userText(t, childB.Messages()[0]))
}

// A swarm member that hits a provider rate limit ends its first turn as failed.
// The swarm does not report it failed: it suspends the member, runs a retry turn
// later with no second subagent.spawned, and reports the end once the member
// completes (sessionSwarmService.resumeAttempt with retryTurn=true).
func TestKimiSwarmMemberThatTheRateLimitSuspends(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_swarm", "name": contracts.KimiToolAgentSwarm,
		"args": map[string]any{"description": "Audit the packages"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentSpawned, "subagentId": "agent-0", "subagentName": "explore",
		"parentToolCallId": "call_swarm", "parentAgentId": kimiMainAgentID, "description": "Audit #1", "swarmIndex": 1})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "agentId": "agent-0", "turnId": 0,
		"origin": map[string]any{"kind": contracts.KimiOriginSystemTrigger, "name": "subagent"}, "prompt": "Check package A."})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "agentId": "agent-0", "turnId": 0, "reason": contracts.KimiTurnEndFailed,
		"error": map[string]any{"code": "provider.rate_limit", "message": "429 Too Many Requests"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentSuspended, "subagentId": "agent-0", "reason": "rate_limit"})

	row, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "a suspended member still runs: the swarm retries it")

	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "agentId": "agent-0", "turnId": 1,
		"origin": map[string]any{"kind": contracts.KimiOriginRetry}, "prompt": ""})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "agentId": "agent-0", "turnId": 1, "reason": contracts.KimiTurnEndCompleted})
	row, _ = childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "the turn end of a run the swarm owns is not the end of the run")

	rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentCompleted, "subagentId": "agent-0", "resultSummary": "Package A is clean."})
	statuses := rig.sink.BackgroundTaskStatuses("session_1/agent-0")
	require.NotEmpty(t, statuses)
	assert.Equal(t, bgtask.StatusCompleted, statuses[len(statuses)-1], "the member that completed reads Completed")
	assert.NotContains(t, statuses, bgtask.StatusFailed, "the rate-limited turn never closed the row")
}

func TestKimiChildSpawnSpan(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "s/main/0/call", kimiChildSpawnSpan(kimiSpawn{spanID: "s/main/0/call", name: contracts.KimiToolAgent}, "agent-0"))
	assert.Equal(t, "s/main/0/call#agent-3", kimiChildSpawnSpan(kimiSpawn{spanID: "s/main/0/call", name: contracts.KimiToolAgentSwarm}, "agent-3"))
}

func TestKimiSwarmMemberOfAResumedSession(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	// No tool.call.started: the swarm call started before this process did.
	for i := range 2 {
		id := fmt.Sprintf("agent-%d", i)
		rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentSpawned, "subagentId": id, "parentToolCallId": "call_swarm",
			"parentAgentId": kimiMainAgentID, "description": "member", "swarmIndex": i + 1})
	}
	rowA, _ := childOf(t, rig.sink, "session_1/agent-0")
	rowB, _ := childOf(t, rig.sink, "session_1/agent-1")
	assert.NotEqual(t, rowA.ChildAgentID, rowB.ChildAgentID)
	assert.Equal(t, bgtask.KindWorkflow, rowA.Kind, "the swarm index marks a member whose call this process never saw")
}

func TestKimiResumedSubagent(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	spawnAgent(t, rig, "agent-0", "call_child", nil)
	endSubagent(t, rig, "agent-0", contracts.KimiEventSubagentCompleted, map[string]any{"resultSummary": "First run."})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "turnId": 0, "reason": "completed"})

	// The model resumes the subagent with a new prompt in a later turn.
	rig.startTurn(t, 1, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 1, "toolCallId": "call_resume", "name": contracts.KimiToolAgent,
		"args": map[string]any{"resume": "agent-0", "prompt": "Now check the tests."}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "agentId": "agent-0", "turnId": 1,
		"origin": map[string]any{"kind": "system_trigger"}, "prompt": "Now check the tests."})
	rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentSpawned, "subagentId": "agent-0", "parentToolCallId": "call_resume",
		"parentAgentId": kimiMainAgentID, "description": "Reviewer", "taskId": "agent-task-2"})

	row, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Contains(t, rig.sink.RevivedTasks(), "session_1/agent-0", "the row comes back to Running")
	last := child.Messages()[len(child.Messages())-1]
	assert.Equal(t, "Now check the tests.", userText(t, last), "the new prompt lands where the transcript ends")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, last.MarkType)
	assert.Equal(t, "Reviewer", row.Title)

	linked, ok := rig.agent.children.get("agent-0")
	require.True(t, ok)
	assert.Equal(t, "agent-task-2", linked.taskID, "the resumed run's task is what stops it")
}

func TestKimiSubagentFollowUpFromItsTab(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	spawnAgent(t, rig, "agent-0", "call_child", nil)
	endSubagent(t, rig, "agent-0", contracts.KimiEventSubagentCompleted, nil)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	before := len(child.Messages())

	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "agentId": "agent-0", "turnId": 1, "origin": map[string]any{"kind": "user"}, "prompt": "One more thing."})
	assert.Contains(t, rig.sink.RevivedTasks(), "session_1/agent-0")
	assert.Len(t, child.Messages(), before, "LeapMux recorded the user's message itself")
	rig.feed(t, map[string]any{"type": contracts.KimiEventTurnEnded, "agentId": "agent-0", "turnId": 1, "reason": "cancelled"})
	statuses := rig.sink.BackgroundTaskStatuses("session_1/agent-0")
	assert.Equal(t, bgtask.StatusStopped, statuses[len(statuses)-1], "a follow-up turn runs as no task, so its end closes the row")
}

func TestKimiNestedSpawnWaitsForItsParent(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_outer", "name": contracts.KimiToolAgent,
		"args": map[string]any{"prompt": "Delegate.", "description": "Outer"}})
	// agent-0 starts agent-1 before agent-0's own spawn links it.
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "agentId": "agent-0", "turnId": 0, "toolCallId": "call_inner", "name": contracts.KimiToolAgent,
		"args": map[string]any{"prompt": "Do the inner part.", "description": "Inner"}})
	rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentSpawned, "agentId": "agent-0", "subagentId": "agent-1",
		"parentToolCallId": "call_inner", "parentAgentId": "agent-0", "description": "Inner"})
	_, found := rig.sink.BackgroundTask("session_1/agent-1")
	assert.False(t, found, "the inner spawn waits for its parent's transcript")

	rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentSpawned, "subagentId": "agent-0",
		"parentToolCallId": "call_outer", "parentAgentId": kimiMainAgentID, "description": "Outer"})
	outer, outerChild := childOf(t, rig.sink, "session_1/agent-0")
	inner, found := outerChild.BackgroundTask("session_1/agent-1")
	require.True(t, found, "the inner subagent's row belongs to its parent's transcript")
	assert.NotEqual(t, outer.ChildAgentID, inner.ChildAgentID)
}

func TestKimiUnlinkedAgentEvents(t *testing.T) {
	t.Parallel()

	t.Run("an agent no spawn claims holds a limited backlog", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		for i := range kimiMaxPendingEvents + 50 {
			rig.feed(t, map[string]any{"type": contracts.KimiEventAssistantDelta, "agentId": "agent-9", "turnId": 0, "delta": fmt.Sprint(i)})
		}
		rig.agent.Mu.Lock()
		held := len(rig.agent.runs["agent-9"].pending)
		rig.agent.Mu.Unlock()
		assert.Equal(t, kimiMaxPendingEvents, held)
		assert.Zero(t, rig.sink.MessageCount())
	})

	t.Run("a control request never waits for a link", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.feed(t, map[string]any{"type": contracts.KimiEventApprovalRequested, "agentId": "agent-4", "agent_id": "agent-4",
			"approval_id": "approval_1", "tool_name": contracts.KimiToolBash, "tool_input_display": map[string]any{"kind": "command", "command": "ls"}})
		assert.Equal(t, 1, rig.sink.PublishedControlCount())
		rig.feed(t, map[string]any{"type": contracts.KimiEventApprovalResolved, "agentId": "agent-4", "approval_id": "approval_1"})
		assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls())
	})
}

func TestKimiStripGitContext(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Review it.", kimiStripGitContext("<git-context>\nbranch: main\n</git-context>\n\nReview it."))
	assert.Equal(t, "Review it.", kimiStripGitContext("  Review it.  "))
	assert.Equal(t, "<git-context> unterminated", kimiStripGitContext("<git-context> unterminated"))
	assert.Equal(t, "", kimiStripGitContext("<git-context>x</git-context>"))
}

func TestKimiErrorText(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "boom", kimiErrorText(" boom "))
	assert.Equal(t, "boom", kimiErrorText(map[string]any{"message": "boom", "code": "x"}))
	assert.Empty(t, kimiErrorText(map[string]any{"code": "x"}))
	assert.Empty(t, kimiErrorText(nil))
	assert.Empty(t, kimiErrorText(42.0))
}

func TestKimiChildRouting(t *testing.T) {
	t.Parallel()

	newLinkedRig := func(t *testing.T) *kimiTestRig {
		t.Helper()
		rig := newKimiTestRig(t, agent.Options{})
		rig.feed(t, map[string]any{"type": contracts.KimiEventTurnStarted, "turnId": 0, "origin": map[string]any{"kind": "user"}})
		spawnAgent(t, rig, "agent-0", "call_child", nil)
		return rig
	}

	t.Run("a message to a running subagent waits", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		err := rig.agent.SendChildInput("session_1/agent-0", "Hurry.", nil)
		require.ErrorIs(t, err, agent.ErrAgentBusy)
		assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	})

	t.Run("a message to an idle subagent runs as its next turn", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		endSubagent(t, rig, "agent-0", contracts.KimiEventSubagentCompleted, nil)
		assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
		require.NoError(t, rig.agent.SendChildInput("session_1/agent-0", "One more thing.", nil))
		prompts := rig.fake.requestsTo("POST " + kimiSessionPath("session_1", "/prompts"))
		require.Len(t, prompts, 1)
		assert.Equal(t, "agent-0", decodePrompt(t, prompts[0]).AgentID)
	})

	t.Run("a steer is not possible", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		require.ErrorIs(t, rig.agent.SteerChildInput("session_1/agent-0", "Hurry.", nil), agent.ErrChildOperationUnsupported)
	})

	t.Run("an interrupt cancels the subagent's task", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		require.NoError(t, rig.agent.InterruptChild("session_1/agent-0"))
		assert.Len(t, rig.fake.requestsTo("POST "+kimiItemPath("session_1", "tasks", "agent-task-agent-0", kimiActionCancel)), 1)
	})

	t.Run("a subagent that runs as no task cannot be stopped", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		endSubagent(t, rig, "agent-0", contracts.KimiEventSubagentCompleted, nil)
		require.ErrorIs(t, rig.agent.InterruptChild("session_1/agent-0"), agent.ErrChildOperationUnsupported)
	})

	t.Run("an interrupt the server refuses fails", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		rig.fake.reply("POST "+kimiItemPath("session_1", "tasks", "agent-task-agent-0", kimiActionCancel), fakeKapReply{Code: 40401, Msg: "task gone"})
		require.ErrorContains(t, rig.agent.InterruptChild("session_1/agent-0"), "task gone")
	})

	t.Run("a task id that Kimi Code does not issue is refused", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		rig.agent.children.update("agent-0", func(c *kimiChild) { c.taskID = "../x" })
		before := len(rig.fake.routes())
		require.ErrorContains(t, rig.agent.InterruptChild("session_1/agent-0"), "task id")
		assert.Len(t, rig.fake.routes(), before)
	})

	t.Run("a message with a PDF is refused before it posts", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		endSubagent(t, rig, "agent-0", contracts.KimiEventSubagentCompleted, nil)
		err := rig.agent.SendChildInput("session_1/agent-0", "Read this.", []*leapmuxv1.Attachment{{Filename: "a.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}})
		require.ErrorContains(t, err, "PDF")
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", "/prompts")))
	})

	t.Run("a row of this session that is not linked yet is not ready", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		require.ErrorIs(t, rig.agent.SendChildInput("session_1/agent-7", "Hello.", nil), agent.ErrChildRouteNotReady)
		assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-7").Active)
	})

	t.Run("a row of another session is unknown", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		err := rig.agent.SendChildInput("session_old/agent-0", "Hello.", nil)
		require.ErrorIs(t, err, errKimiChildUnknown)
		assert.NotErrorIs(t, err, agent.ErrChildRouteNotReady)
		require.ErrorIs(t, rig.agent.InterruptChild("session_old/agent-0"), errKimiChildUnknown)
	})
}

func TestKimiChildTurnStatus(t *testing.T) {
	t.Parallel()

	assert.Equal(t, bgtask.StatusCompleted, kimiChildTurnStatus(contracts.KimiTurnEndCompleted))
	assert.Equal(t, bgtask.StatusStopped, kimiChildTurnStatus(contracts.KimiTurnEndCancelled))
	assert.Equal(t, bgtask.StatusFailed, kimiChildTurnStatus(contracts.KimiTurnEndFailed))
	assert.Equal(t, bgtask.StatusFailed, kimiChildTurnStatus(contracts.KimiTurnEndBlocked))
	assert.Equal(t, bgtask.StatusFailed, kimiChildTurnStatus("paused"), "a reason this build does not know did not complete")
}

func TestKimiSpawnParent(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	spawnAgent(t, rig, "agent-0", "call_child", nil)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "agentId": "agent-0", "turnId": 0, "toolCallId": "call_inner",
		"name": contracts.KimiToolAgent, "args": map[string]any{"prompt": "Do the inner part."}})

	assert.Equal(t, "agent-0", rig.agent.spawnParent("call_inner"), "the agent whose run opened the call spawned the subagent")
	assert.Equal(t, kimiMainAgentID, rig.agent.spawnParent("call_child"))
	assert.Equal(t, kimiMainAgentID, rig.agent.spawnParent(""), "a roster entry that states no call is the main agent's")
	assert.Equal(t, kimiMainAgentID, rig.agent.spawnParent("call_gone"), "a call that opened during a gap is in no run")
}

func TestKimiSpawnOfNoSubagentOpensNoRow(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	rig.feed(t, map[string]any{"type": contracts.KimiEventToolCallStarted, "turnId": 0, "toolCallId": "call_child", "name": contracts.KimiToolAgent,
		"args": map[string]any{"prompt": "Review it."}})
	for _, id := range []string{"", kimiMainAgentID} {
		rig.feed(t, map[string]any{"type": contracts.KimiEventSubagentSpawned, "subagentId": id, "parentToolCallId": "call_child",
			"parentAgentId": kimiMainAgentID, "description": "Reviewer"})
	}
	assert.Empty(t, rig.sink.BackgroundTasks(), "the main agent is no subagent of itself")
}

// reconcileSubagents runs after a gap that the stream could not replay, and it
// sends no request: the resync reads the roster and the task list first.
func TestKimiReconcileSubagents(t *testing.T) {
	t.Parallel()

	now := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }

	t.Run("a closed subagent that runs again is revived", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.startTurn(t, 0, contracts.KimiOriginUser)
		spawnAgent(t, rig, "agent-0", "call_child", nil)
		endSubagent(t, rig, "agent-0", contracts.KimiEventSubagentCompleted, nil)
		_, child := childOf(t, rig.sink, "session_1/agent-0")
		require.Equal(t, []bool{true, false}, child.TurnActives())

		rig.agent.reconcileSubagents([]kimiRosterSubagent{
			{ID: "agent-0", Status: kimiWireStatusRunning, Phase: kimiSubagentPhaseWorking, ParentToolCall: "call_child"},
		}, nil, true)
		assert.Contains(t, rig.sink.RevivedTasks(), "session_1/agent-0", "the model resumed it during the gap")
		assert.Equal(t, []bool{true, false, true}, child.TurnActives())
		row, _ := rig.sink.BackgroundTask("session_1/agent-0")
		assert.Equal(t, bgtask.StatusRunning, row.Status)
	})

	t.Run("the main agent and an id that Kimi Code does not issue are no subagents", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.agent.reconcileSubagents(
			[]kimiRosterSubagent{{ID: kimiMainAgentID, Status: kimiWireStatusRunning}, {ID: "../x", Status: kimiWireStatusRunning}},
			[]kimiTaskItem{
				{ID: "task_1", Kind: kimiWireTaskKindSubagent, Status: kimiWireStatusRunning, AgentID: kimiMainAgentID, StartedAt: now()},
				{ID: "task_2", Kind: kimiWireTaskKindSubagent, Status: kimiWireStatusRunning, AgentID: "a/b", StartedAt: now()},
			}, true)
		assert.Empty(t, rig.sink.BackgroundTasks())
	})

	t.Run("a subagent task that started before the agent attached is history", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.agent.attachedAt = time.Now()
		rig.agent.reconcileSubagents(nil, []kimiTaskItem{{
			ID: "agent-task-5", Kind: kimiWireTaskKindSubagent, Status: kimiWireStatusRunning, AgentID: "agent-5",
			StartedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
		}}, true)
		assert.Empty(t, rig.sink.BackgroundTasks(), "the session's old work gets no row")
	})

	t.Run("a running task states the id that stops its subagent", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		rig.startTurn(t, 0, contracts.KimiOriginUser)
		spawnAgent(t, rig, "agent-0", "call_child", map[string]any{"taskId": "", "runInBackground": true})
		rig.agent.reconcileSubagents(nil, []kimiTaskItem{
			{ID: "agent-task-7", Kind: kimiWireTaskKindSubagent, Status: kimiWireStatusRunning, AgentID: "agent-0", StartedAt: now()},
		}, true)
		child, ok := rig.agent.children.get("agent-0")
		require.True(t, ok)
		assert.Equal(t, "agent-task-7", child.taskID, "its task.started was lost with the gap")

		rig.agent.reconcileSubagents(nil, []kimiTaskItem{
			{ID: "agent-task-8", Kind: kimiWireTaskKindSubagent, Status: kimiWireStatusRunning, AgentID: "agent-0", StartedAt: now()},
		}, true)
		child, _ = rig.agent.children.get("agent-0")
		assert.Equal(t, "agent-task-7", child.taskID, "a task id the stream reported is kept")
	})
}
