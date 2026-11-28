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

func TestKimiSubagentTranscript(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "parent", 0)
	const spawn = `"type":"tool_call","item_id":"call_child","step_index":1,"name":"Agent","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"prompt":"Review the diff.","description":"Reviewer"},"agent_refs":[{"agent_id":"agent-0","role":"child"}]`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"parent","item":{`+spawn+`,"status":"running","stage":"executing"}}`)
	rig.startPublicTurn(t, "agent-0", "child", 0)
	rig.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"child","item":{"type":"user_message","item_id":"assignment","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[],"placement":"opener","origin":{"kind":"system"},"text":"Review the diff.","attachments":[],"skills":[]}}`)
	const read = `"type":"tool_call","item_id":"call_read","step_index":1,"name":"Read","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"path":"a.go"}`
	rig.conversationAgentFrame(t, "agent-0", `{"type":"item.started","turn_id":"child","item":{`+read+`,"status":"running","stage":"executing"}}`)
	rig.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"child","item":{`+read+`,"status":"ended","outcome":"completed","output":"package a"}}`)
	rig.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"child","item":{"type":"assistant_message","item_id":"answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Found it."}}`)
	rig.endPublicTurn(t, "agent-0", "child", 0, "completed")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"call_child","result_summary":"All good."}`))
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"parent","item":{`+spawn+`,"status":"ended","outcome":"completed","output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nAll good."}}`)
	awaitKimiTestOutput(t, rig.agent.sink)

	row, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.RoleChildActivity, row.Role)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, "Reviewer", row.Title)
	assert.Equal(t, "explore", row.Description)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
	task := nativeTaskOf(t, rig.sink, "task-child")
	assert.Equal(t, bgtask.RoleTaskOutcome, task.Role)
	assert.Equal(t, bgtask.StatusSucceeded, task.Status)

	parentRows := rig.sink.Messages()
	require.Len(t, parentRows, 2)
	spawnSpan, err := rig.sink.ChildSpawnSpan(row.ChildAgentID)
	require.NoError(t, err)
	require.NotEmpty(t, spawnSpan)
	assert.Equal(t, parentRows[0].SpanID, spawnSpan)
	assert.True(t, parentRows[0].NoSpan, "a spawn has no visible span rail")

	rows := child.Messages()
	require.Len(t, rows, 5)
	assignment, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	require.NotNil(t, assignment.item)
	require.NotNil(t, assignment.item.Text)
	assert.Equal(t, "Review the diff.", *assignment.item.Text)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.False(t, rows[1].Closing)
	assert.True(t, rows[2].Closing)
	assert.NotEmpty(t, rows[1].SpanID)
	assert.Equal(t, rows[1].SpanID, rows[2].SpanID)
	result, err := decodeKimiConversationRecord(rows[2].Content)
	require.NoError(t, err)
	require.NotNil(t, result.item)
	assert.Equal(t, "call_read", result.item.ItemID)
	assert.JSONEq(t, `"package a"`, string(result.item.Output))
	answer, err := decodeKimiConversationRecord(rows[3].Content)
	require.NoError(t, err)
	require.NotNil(t, answer.item)
	require.NotNil(t, answer.item.Text)
	assert.Equal(t, "Found it.", *answer.item.Text)
	assert.True(t, rows[4].TurnEnd)
	assert.Equal(t, []bool{true, false}, child.TurnActives())
	assert.False(t, rig.agent.ActiveChildTurnState(row.RowKey).Active)
	assert.NotEmpty(t, child.LeapMuxNotifications())
	assert.Empty(t, rig.sink.LeapMuxNotifications(), "the foreground result reaches the parent through its own tool card")
	assert.True(t, rig.agent.PublishTurnActive().Active)
}

func TestKimiChildInterruptDoesNotClaimNativeCancellation(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	spec := kimiRecoveryChild{agentID: "agent-0", callID: "call-child", taskID: "child-task", omitTaskReference: true}
	spec.startNative(t, rig)
	before := rig.sink.BackgroundTasks()
	require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
	assert.Equal(t, before, rig.sink.BackgroundTasks(), "unsupported interruption cannot invent a task or child outcome")
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	native.conversationAgentFrame(t, "agent-0", `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"interrupted","end_reason":"aborted","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
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
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call_swarm","step_index":1,"name":"AgentSwarm","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Audit the packages","subagent_type":"explore","items":["package A","package B"],"prompt_template":"Check {{item}}."},"agent_refs":[{"agent_id":"agent-0","role":"member","swarm_index":0},{"agent_id":"agent-1","role":"member","swarm_index":1}]}}`)
	identity := kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", toolCallID: "call_swarm"}
	var childIDs []string
	for i, prompt := range []string{"Check package A.", "Check package B."} {
		actor := fmt.Sprintf("agent-%d", i)
		rig.startPublicTurn(t, actor, "child", 0)
		rig.conversationAgentFrame(t, actor, fmt.Sprintf(`{"type":"item.ended","turn_id":"child","item":{"type":"user_message","item_id":"member-assignment","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[],"placement":"opener","origin":{"kind":"system"},"text":%q,"attachments":[],"skills":[]}}`, prompt))
		require.NoError(t, agent.FlushPendingOutput(rig.agent.run(actor).childLifetime.writer))
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
		rows := child.Messages()
		require.Len(t, rows, 1)
		view, err := decodeKimiConversationRecord(rows[0].Content)
		require.NoError(t, err)
		require.NotNil(t, view.item)
		require.NotNil(t, view.item.Text)
		assert.Equal(t, prompt, *view.item.Text)
	}
	assert.NotEqual(t, childIDs[0], childIDs[1])
	assert.Len(t, rig.sink.BackgroundTasks(), 6, "two native tasks, two unmatched invocation outcomes and two activity markers remain distinct")
}

// A swarm member that hits a provider rate limit ends its first turn as failed.
// A failed child attempt and a suspended coordinator task retain separate outcomes.
// The next native child header resumes the same transcript without another adoption.
func TestKimiSwarmMemberThatTheRateLimitSuspends(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call_swarm","step_index":1,"name":"AgentSwarm","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Audit the packages","items":["package A"],"prompt_template":"Check {{item}}."},"agent_refs":[{"agent_id":"agent-0","role":"member","swarm_index":0}]}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"swarm-member","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"call_swarm","swarm_index":0}`))
	rig.startPublicTurn(t, "agent-0", "t0", 0)
	rig.publicOpener(t, "agent-0", "t0", `{"kind":"system"}`)
	rig.endPublicTurn(t, "agent-0", "t0", 0, "failed")
	first, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusFailed, first.Status)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"swarm-member","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"call_swarm","swarm_index":0,"phase":"suspended","state_reason":"rate_limit"}`))
	task, exists := rig.sink.BackgroundTask("session_1/task/main/swarm-member")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusRunning, task.Status)
	rig.startPublicTurn(t, "agent-0", "t1", 1)
	rig.publicOpener(t, "agent-0", "t1", `{"kind":"retry"}`)
	current, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, first.ChildAgentID, current.ChildAgentID)
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.endPublicTurn(t, "agent-0", "t1", 1, "completed")
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
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "first", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"first","item":{"type":"tool_call","item_id":"call_child","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"prompt":"Review the diff.","description":"Reviewer"},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	rig.startPublicTurn(t, "agent-0", "review", 0)
	rig.publicOpener(t, "agent-0", "review", `{"kind":"system"}`)
	rig.endPublicTurn(t, "agent-0", "review", 0, "completed")
	first, _ := childOf(t, rig.sink, "session_1/agent-0")

	rig.endPublicTurn(t, kimiMainAgentID, "first", 0, "completed")
	rig.startPublicTurn(t, kimiMainAgentID, "second", 1)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"second","item":{"type":"tool_call","item_id":"call_resume","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:01Z","started_at":"2026-11-18T00:00:01Z","input":{"resume":"agent-0","prompt":"Now check the tests."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	rig.startPublicTurn(t, "agent-0", "check-tests", 1)
	rig.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"check-tests","item":{"type":"user_message","item_id":"second-prompt","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","prompt_ids":[],"placement":"opener","origin":{"kind":"system"},"text":"Now check the tests.","attachments":[],"skills":[]}}`)
	require.NoError(t, agent.FlushPendingOutput(rig.agent.run("agent-0").childLifetime.writer))

	row, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, first.ChildAgentID, row.ChildAgentID)
	assert.Contains(t, rig.sink.RevivedTasks(), row.RowKey)
	rows := child.Messages()
	require.Len(t, rows, 3)
	prompt, err := decodeKimiConversationRecord(rows[2].Content)
	require.NoError(t, err)
	require.NotNil(t, prompt.item)
	require.NotNil(t, prompt.item.Text)
	assert.Equal(t, "Now check the tests.", *prompt.item.Text)
	assert.Equal(t, "check-tests", prompt.record.TurnID)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, rows[2].MarkType)
	assert.Equal(t, "Reviewer", row.Title)
	assert.True(t, rig.agent.ActiveChildTurnState(row.RowKey).Active)
}

func TestKimiSubagentFollowUpFromItsTab(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	adoptNativeKimiChild(t, rig.agent, "agent-0")
	rig.startPublicTurn(t, "agent-0", "t0", 0)
	rig.endPublicTurn(t, "agent-0", "t0", 0, "completed")
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	before := child.MessageCount()
	rig.startPublicTurn(t, "agent-0", "t1", 1)
	rig.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"t1","item":`+kimiPublicPromptItem(t, "follow-up", "opener", "follow-up")+`}`)
	assert.Contains(t, rig.sink.RevivedTasks(), "session_1/agent-0")
	assert.Equal(t, before, child.MessageCount(), "LeapMux already recorded the user's input")
	rig.endPublicTurn(t, "agent-0", "t1", 1, "interrupted")
	row, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
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

func TestKimiChildRouting(t *testing.T) {
	t.Parallel()

	newLinkedRig := func(t *testing.T) *kimiTestRig {
		t.Helper()
		rig := newKimiTestRig(t, agent.Options{})
		spec := kimiRecoveryChild{agentID: "agent-0", callID: "call-child", taskID: "child-task", omitTaskReference: true}
		spec.startNative(t, rig)
		return rig
	}

	finishChild := func(t *testing.T, rig *kimiTestRig) {
		t.Helper()
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		native.conversationAgentFrame(t, "agent-0", `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		require.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
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
		finishChild(t, rig)
		require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "One more thing."}))
		prompts := rig.fake.requestsTo("POST " + kimiSessionPath("session_1", "/prompts"))
		require.Len(t, prompts, 1)
		assert.Equal(t, "agent-0", decodePrompt(t, prompts[0]).AgentID)
	})

	t.Run("a steer is not possible", func(t *testing.T) {
		t.Parallel()
		rig := newLinkedRig(t)
		_, err := rig.agent.SteerChildInput("session_1/agent-0", agent.SteerRequest{Content: "Hurry."})
		require.ErrorIs(t, err, agent.ErrChildOperationUnsupported)
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
		finishChild(t, rig)
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
		finishChild(t, rig)
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

func TestKimiSpawnOfNoSubagentOpensNoRow(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "main", "../invalid", "agent/0"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
			rig.conversationFrame(t, fmt.Sprintf(`{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call_child","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"prompt":"Review."},"agent_refs":[{"agent_id":%q,"role":"child"}]}}`, id))
			assert.Empty(t, rig.sink.BackgroundTasks())
		})
	}
}

func TestKimiPublicSnapshotRevivesAnEndedChild(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	adoptNativeKimiChild(t, rig.agent, "agent-0")
	rig.startPublicTurn(t, "agent-0", "t0", 0)
	rig.endPublicTurn(t, "agent-0", "t0", 0, "completed")
	first, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, []bool{true, false}, child.TurnActives())
	rig.conversationAgentFrame(t, "agent-0", `{"type":"turns","has_more":false,"turns":[{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}]}`)
	row, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, first.ChildAgentID, row.ChildAgentID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, []bool{true, false, true}, child.TurnActives())
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}
