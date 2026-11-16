package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Native child frames can arrive before their parent tool supplies the linking reference.

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
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`)
	// The child speaks before its parent's native reference links its transcript.
	rig.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"other"},"prompt":"<git-context>branch main</git-context>\nReview the diff."}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"read","toolCallId":"call_read","name":"Read","state":"running","input":{"path":"a.go"}}}`)
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"call_child","name":"Agent","state":"running","input":{"prompt":"Review the diff.","description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	rig.transcriptAgentOps(t, "agent-0", 2,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"read","toolCallId":"call_read","name":"Read","state":"done","input":{"path":"a.go"},"output":"package a"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Found it."}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"completed"}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"completed","origin":{"kind":"other"}}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"call_child","result_summary":"All good."}`))
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"call_child","name":"Agent","state":"done","input":{"prompt":"Review the diff.","description":"Reviewer"},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nAll good.","agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	row, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.RoleChildActivity, row.Role)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, "Reviewer", row.Title)
	assert.Equal(t, "explore", row.Description)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
	task, exists := rig.sink.BackgroundTask("session_1/task/main/task-child")
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleTaskOutcome, task.Role)
	assert.Equal(t, bgtask.StatusSucceeded, task.Status)
	spawnSpan, err := rig.sink.ChildSpawnSpan(row.ChildAgentID)
	require.NoError(t, err)
	assert.Equal(t, "session_1/"+kimiTranscriptScope("main", "t0", "t0.1", "spawn"), spawnSpan)
	messages := child.Messages()
	require.GreaterOrEqual(t, len(messages), 4)
	assert.Equal(t, "Review the diff.", userText(t, messages[0]))
	running, finished := -1, -1
	var readSpan string
	var narrative []string
	for i, message := range messages {
		var head struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(message.Content, &head))
		if head.Type == contracts.AssembledMessageType {
			_, text := assembledRow(t, message)
			narrative = append(narrative, text)
		}
		record, valid := readKimiTranscriptRecord(message.Content)
		if !valid {
			continue
		}
		var frame struct{ Name, State string }
		if json.Unmarshal(record.operation.Frame, &frame) != nil || frame.Name != "Read" {
			continue
		}
		if frame.State == "running" {
			running = i
			readSpan = message.SpanID
		}
		if frame.State == "done" {
			finished = i
			assert.Equal(t, readSpan, message.SpanID)
		}
	}
	require.NotEqual(t, -1, running)
	assert.Equal(t, "session_1/"+kimiTranscriptScope("agent-0", "t0", "t0.1", "read"), readSpan)
	assert.Contains(t, narrative, "Found it.")
	assert.Equal(t, []bool{true, false}, child.TurnActives())
	assert.Greater(t, finished, running, "held native child records replay before their final result")
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	assert.NotEmpty(t, child.LeapMuxNotifications())
	assert.Empty(t, rig.sink.LeapMuxNotifications(), "the foreground result reaches the parent through its own tool card")
	for _, active := range rig.sink.TurnActives() {
		assert.True(t, active, "child activity cannot end the main turn")
	}
	var spawnRow *agenttest.Message
	for _, message := range rig.sink.Messages() {
		if message.SpanID == spawnSpan && !message.Closing {
			copy := message
			spawnRow = &copy
			break
		}
	}
	require.NotNil(t, spawnRow)
	assert.True(t, spawnRow.NoSpan, "a spawn owns no span rail")
}

func TestKimiChildInterruptDoesNotClaimNativeCancellation(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	spec := kimiRecoveryChild{agentID: "agent-0", callID: "call-child", taskID: "child-task"}
	spec.start(t, rig)
	before := rig.sink.BackgroundTasks()
	require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
	assert.Equal(t, before, rig.sink.BackgroundTasks(), "unsupported interruption cannot invent a task or child outcome")
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	native.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"cancelled","origin":{"kind":"other"}}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"child-task","kind":"subagent","status":"ended","outcome":"killed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"call-child"}`))
	marker, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusStopped, marker.Status, "native cancellation does not acquire a user-interruption claim")
	rig.fake.mu.Lock()
	commands := append([]fakeKimiTaskCommand(nil), rig.fake.taskCommands...)
	rig.fake.mu.Unlock()
	for _, command := range commands {
		assert.NotEqual(t, kimiFrameTaskStop, command.Type)
	}
}

func TestKimiSwarmSeparatesWorkflowOutcomesAndChildActivity(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"swarm","toolCallId":"call_swarm","name":"AgentSwarm","state":"running","input":{"description":"Audit the packages","subagent_type":"explore","items":["package A","package B"],"prompt_template":"Check {{item}}."},"agentRefs":[{"agentId":"agent-0","role":"member"},{"agentId":"agent-1","role":"member"}]}}`)
	identity := kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "swarm", toolCallID: "call_swarm"}
	var childIDs []string
	for i, prompt := range []string{"Check package A.", "Check package B."} {
		actor := fmt.Sprintf("agent-%d", i)
		rig.transcriptAgentOps(t, actor, 1, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"child","state":"running","origin":{"kind":"other"},"prompt":%q}}`, prompt))
		raw, err := json.Marshal(map[string]any{"type": "task", "session_id": "session_1", "agent_id": "main", "task_id": fmt.Sprintf("member-%d", i), "kind": "subagent", "status": "running", "detached": false, "child_agent_id": actor, "parent_tool_call_id": "call_swarm", "description": fmt.Sprintf("Audit #%d", i+1), "swarm_index": i})
		require.NoError(t, err)
		rig.agent.HandleOutput(raw)
		marker, child := childOf(t, rig.sink, "session_1/"+actor)
		childIDs = append(childIDs, marker.ChildAgentID)
		assert.Equal(t, bgtask.RoleChildActivity, marker.Role)
		assert.Equal(t, bgtask.KindSubagent, marker.Kind)
		assert.Empty(t, marker.GroupKey, "current child activity is separate from parent workflow outcomes")
		outcome := invocationOutcomeOf(t, rig.sink, identity, actor)
		assert.Equal(t, bgtask.RoleTaskOutcome, outcome.Role)
		assert.Equal(t, bgtask.KindWorkflow, outcome.Kind)
		assert.Equal(t, kimiInvocationGroupKey(identity), outcome.GroupKey)
		assert.Equal(t, "Audit the packages", outcome.GroupLabel)
		assert.Equal(t, marker.ChildAgentID, outcome.ChildAgentID)
		task, exists := rig.sink.BackgroundTask(fmt.Sprintf("session_1/task/main/member-%d", i))
		require.True(t, exists)
		assert.Equal(t, bgtask.RoleTaskOutcome, task.Role)
		assert.Equal(t, bgtask.KindWorkflow, task.Kind)
		assert.Empty(t, task.GroupKey, "a bare native call ID does not prove the full workflow identity")
		require.NotEmpty(t, child.Messages())
		assert.Equal(t, prompt, userText(t, child.Messages()[0]))
	}
	assert.NotEqual(t, childIDs[0], childIDs[1])
	assert.Len(t, rig.sink.BackgroundTasks(), 6, "two native tasks, two unmatched invocation outcomes and two activity markers remain distinct")
}

// A swarm member that hits a provider rate limit ends its first turn as failed.
// A failed child attempt and a suspended coordinator task retain separate outcomes.
// The next native child header resumes the same transcript without another adoption.
func TestKimiSwarmMemberThatTheRateLimitSuspends(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"swarm","toolCallId":"call_swarm","name":"AgentSwarm","state":"running","input":{"description":"Audit the packages","items":["package A"],"prompt_template":"Check {{item}}."},"agentRefs":[{"agentId":"agent-0","role":"member"}]}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"swarm-member","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"call_swarm","swarm_index":0}`))
	rig.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"other"},"prompt":"Check package A."}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"other"},"error":"429 Too Many Requests"}}`)
	first, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusFailed, first.Status)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"swarm-member","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"call_swarm","swarm_index":0,"phase":"suspended","state_reason":"rate_limit"}`))
	task, exists := rig.sink.BackgroundTask("session_1/task/main/swarm-member")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusRunning, task.Status)
	rig.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":"running","origin":{"kind":"other","payload":{"kind":"retry"}}}}`)
	current, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, first.ChildAgentID, current.ChildAgentID)
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.transcriptAgentOps(t, "agent-0", 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":"completed","origin":{"kind":"other"}}}`)
	task, _ = rig.sink.BackgroundTask("session_1/task/main/swarm-member")
	assert.Equal(t, bgtask.StatusRunning, task.Status, "a child header does not finish its coordinator task")
	end := []byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"swarm-member","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"call_swarm","swarm_index":0,"result_summary":"Package A is clean."}`)
	rig.agent.HandleOutput(end)
	rig.agent.HandleOutput(end)
	task, _ = rig.sink.BackgroundTask("session_1/task/main/swarm-member")
	assert.Equal(t, bgtask.StatusSucceeded, task.Status)
	require.Len(t, child.LeapMuxNotifications(), 1)
	assert.Contains(t, fmt.Sprint(child.LeapMuxNotifications()[0]), "Package A is clean.")
}

func TestKimiChildSpawnSpan(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "s/main/0/call", kimiChildSpawnSpan(kimiSpawn{spanID: "s/main/0/call", name: contracts.KimiToolAgent}, "agent-0"))
	assert.Equal(t, "s/main/0/call#agent-3", kimiChildSpawnSpan(kimiSpawn{spanID: "s/main/0/call", name: contracts.KimiToolAgentSwarm}, "agent-3"))
}

func TestKimiResumedSwarmKeepsNativeMembersWithoutInventingAGroup(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	for i := range 2 {
		raw, err := json.Marshal(map[string]any{"type": "task", "session_id": "session_1", "agent_id": "main", "task_id": fmt.Sprintf("member-%d", i), "kind": "subagent", "status": "running", "detached": false, "child_agent_id": fmt.Sprintf("agent-%d", i), "parent_tool_call_id": "call_swarm", "description": "member", "swarm_index": i})
		require.NoError(t, err)
		rig.agent.HandleOutput(raw)
	}
	rowA, _ := childOf(t, rig.sink, "session_1/agent-0")
	rowB, _ := childOf(t, rig.sink, "session_1/agent-1")
	assert.NotEqual(t, rowA.ChildAgentID, rowB.ChildAgentID)
	for i, marker := range []bgtask.Item{rowA, rowB} {
		assert.Equal(t, bgtask.RoleChildActivity, marker.Role)
		assert.Equal(t, bgtask.KindSubagent, marker.Kind)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, marker.Status, "a task list does not invent a running child turn")
		task, exists := rig.sink.BackgroundTask(fmt.Sprintf("session_1/task/main/member-%d", i))
		require.True(t, exists)
		assert.Equal(t, bgtask.RoleTaskOutcome, task.Role)
		assert.Equal(t, bgtask.KindWorkflow, task.Kind, "native swarm membership belongs to the task outcome")
		assert.Equal(t, marker.ChildAgentID, task.ChildAgentID)
		assert.Empty(t, task.GroupKey)
	}
}

func TestKimiResumedSubagent(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"call_child","name":"Agent","state":"running","input":{"prompt":"Review the diff.","description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	rig.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"other"},"prompt":"Review the diff."}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"other"}}}`)
	first, _ := childOf(t, rig.sink, "session_1/agent-0")
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":{"kind":"tool","frameId":"resume","toolCallId":"call_resume","name":"Agent","state":"running","input":{"resume":"agent-0","prompt":"Now check the tests."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	rig.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":"running","origin":{"kind":"other"},"prompt":"Now check the tests."}}`)
	row, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, first.ChildAgentID, row.ChildAgentID)
	assert.Contains(t, rig.sink.RevivedTasks(), "session_1/agent-0")
	messages := child.Messages()
	require.NotEmpty(t, messages)
	last := messages[len(messages)-1]
	assert.Equal(t, "Now check the tests.", userText(t, last))
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, last.MarkType)
	assert.Equal(t, "Reviewer", row.Title)
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiSubagentFollowUpFromItsTab(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	adoptNativeKimiChild(t, rig.agent, "agent-0")
	rig.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"other"}}}`)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	before := child.MessageCount()
	rig.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":"running","origin":{"kind":"user"},"prompt":"One more thing."}}`)
	assert.Contains(t, rig.sink.RevivedTasks(), "session_1/agent-0")
	assert.Equal(t, before, child.MessageCount(), "LeapMux already recorded the user's input")
	rig.transcriptAgentOps(t, "agent-0", 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":"cancelled","origin":{"kind":"user"}}}`)
	row, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiNestedSpawnWaitsForItsParent(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"inner","toolCallId":"call_inner","name":"Agent","state":"running","input":{"prompt":"Do the inner part.","description":"Inner"},"agentRefs":[{"agentId":"agent-1","role":"child"}]}}`)
	_, found := rig.sink.BackgroundTask("session_1/agent-1")
	assert.False(t, found, "the inner reference waits for its parent's transcript")
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"outer","toolCallId":"call_outer","name":"Agent","state":"running","input":{"prompt":"Delegate.","description":"Outer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	outer, outerChild := childOf(t, rig.sink, "session_1/agent-0")
	inner, found := outerChild.BackgroundTask("session_1/agent-1")
	require.True(t, found)
	assert.NotEqual(t, outer.ChildAgentID, inner.ChildAgentID)
	assert.Equal(t, "agent-0", rig.agent.run("agent-1").childParentAgentID)
}

func TestKimiUnlinkedAgentEvents(t *testing.T) {
	t.Parallel()

	t.Run("a control request never waits for a link", func(t *testing.T) {
		t.Parallel()
		rig := newKimiOutputRig(t)
		request := approvalEvent("approval_1", map[string]any{"kind": "command", "command": "ls"})
		request["agent_id"] = "agent-4"
		rig.feed(t, request)
		assert.Equal(t, 1, rig.sink.PublishedControlCount())
		rig.feed(t, map[string]any{"type": contracts.KimiFrameInteraction, "kind": contracts.KimiInteractionKindApproval, "status": kimiInteractionEnded, "outcome": "cancelled", "agent_id": "agent-4", "interaction_id": "approval_1"})
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

func TestKimiChildRouting(t *testing.T) {
	t.Parallel()

	newLinkedRig := func(t *testing.T) *kimiTestRig {
		t.Helper()
		rig := newKimiTestRig(t, agent.Options{})
		spec := kimiRecoveryChild{agentID: "agent-0", callID: "call-child", taskID: "child-task"}
		spec.start(t, rig)
		return rig
	}

	finishChild := func(rig *kimiTestRig) {
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"other"}}}`)
	}

	t.Run("a message to a running subagent waits", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		err := agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Hurry."})
		require.ErrorIs(t, err, agent.ErrAgentBusy)
		assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	})

	t.Run("a message to an idle subagent runs as its next turn", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		finishChild(rig)
		assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
		require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "One more thing."}))
		prompts := rig.fake.requestsTo("POST " + kimiSessionPath("session_1", "/prompts"))
		require.Len(t, prompts, 1)
		assert.Equal(t, "agent-0", decodePrompt(t, prompts[0]).AgentID)
	})

	t.Run("a steer is not possible", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		require.ErrorIs(t, rig.agent.SteerChildInput("session_1/agent-0", "Hurry.", nil), agent.ErrChildOperationUnsupported)
	})

	t.Run("an interrupt cannot guess a task from current child metadata", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
		assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
		rig.fake.mu.Lock()
		commands := append([]fakeKimiTaskCommand(nil), rig.fake.taskCommands...)
		rig.fake.mu.Unlock()
		for _, command := range commands {
			assert.NotEqual(t, kimiFrameTaskStop, command.Type)
		}
	})

	t.Run("a subagent that runs as no task cannot be stopped", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		finishChild(rig)
		require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
	})

	t.Run("an unsupported interrupt never reaches the server", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		rig.fake.mu.Lock()
		rig.fake.taskCommandFor = func(command fakeKimiTaskCommand) (int, string) {
			assert.NotEqual(t, kimiFrameTaskStop, command.Type)
			return 0, ""
		}
		rig.fake.mu.Unlock()
		require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
		_, linked := rig.agent.children.get("agent-0")
		require.True(t, linked)
		assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	})

	t.Run("a message with a PDF is refused before it posts", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		finishChild(rig)
		err := agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Read this.", Attachments: []*leapmuxv1.Attachment{{Filename: "a.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}}})
		require.ErrorContains(t, err, "PDF")
		assert.Empty(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", "/prompts")))
	})

	t.Run("a row of this session that is not linked yet is not ready", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		require.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-7", agent.ChildInput{Content: "Hello."}), agent.ErrChildRouteNotReady)
		assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-7").Active)
	})

	t.Run("a row of another session is unknown", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		err := agenttest.SendChildInput(rig.agent, "session_old/agent-0", agent.ChildInput{Content: "Hello."})
		require.ErrorIs(t, err, errKimiChildUnknown)
		assert.NotErrorIs(t, err, agent.ErrChildRouteNotReady)
		require.ErrorIs(t, interruptKimiChild(rig.agent, "session_old/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
	})
}

func TestKimiChildTurnStatus(t *testing.T) {
	t.Parallel()

	assert.Equal(t, bgtask.StatusSucceeded, kimiChildTurnStatus(contracts.KimiTranscriptTurnCompleted))
	assert.Equal(t, bgtask.StatusStopped, kimiChildTurnStatus(contracts.KimiTranscriptTurnCancelled))
	assert.Equal(t, bgtask.StatusFailed, kimiChildTurnStatus(contracts.KimiTranscriptTurnFailed))
	assert.Equal(t, bgtask.StatusFailed, kimiChildTurnStatus("paused"), "a reason this build does not know did not complete")
}

func TestKimiSpawnOfNoSubagentOpensNoRow(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "main", "../invalid", "agent/0"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"call_child","name":"Agent","state":"running","input":{"prompt":"Review."},"agentRefs":[{"agentId":%q,"role":"child"}]}}`, id))
			assert.Empty(t, rig.sink.BackgroundTasks())
		})
	}
}

func TestKimiNativeResetRevivesAnEndedChild(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	adoptNativeKimiChild(t, rig.agent, "agent-0")
	rig.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"other"}}}`)
	first, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, []bool{true, false}, child.TurnActives())
	rig.agent.HandleOutput([]byte(`{"type":"transcript.reset","session_id":"session_1","payload":{"type":"transcript.reset","agent_id":"agent-0","seq":2,"snapshot":{"items":[{"kind":"turn","turnId":"t1","state":"running","origin":{"kind":"other"}}],"interactions":[],"attachments":[],"prompts":[]}}}`))
	row, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, first.ChildAgentID, row.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, []bool{true, false, true}, child.TurnActives())
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}
