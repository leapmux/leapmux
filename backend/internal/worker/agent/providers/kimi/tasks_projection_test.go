package kimi

import (
	"errors"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiExactTaskAliasRetainsSucceededOutcomeAcrossRenameRefusal(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	sink := &kimiTaskProjectionFailureSink{ControlSink: &agenttest.ControlSink{}}
	a, opts := newUnconnectedKimiAgent(t, agent.Options{}, agent.NewModelProgressResetSink(agent.NewProviderServices(sink)))
	require.NoError(t, a.connect(a.Context(), server.URL, fakeKapToken, opts, 30*time.Second))
	t.Cleanup(a.closeStream)
	require.NoError(t, a.openStartupSession(opts, 30*time.Second))
	rig := &kimiTestRig{agent: a, fake: fake, sink: sink.ControlSink}
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	native.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Old assignment","prompt":"Review it.","run_in_background":true},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Old assignment"}`))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","result_summary":"A completed review."}`))
	native.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"independent","state":"running","origin":{"kind":"user"}}}`)
	rig.fake.mu.Lock()
	rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
		return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": true, "child_agent_id": "agent-0", "parent_tool_call_id": "spawn", "result_summary": "A completed review."}
	}
	rig.fake.mu.Unlock()
	waitFor(t, rig.agent.taskReportWork.Idle, "the completed report finishes before alias fault injection")
	sink.renameErr = errors.New("registry alias write refused")
	native.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"done","input":{"description":"Old assignment","prompt":"Review it.","run_in_background":true},"output":"task_id: old-task\nstatus: running\nagent_id: agent-0\nactual_subagent_type: explore\nautomatic_notification: true\n\ndescription: Old assignment","agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	waitFor(t, rig.agent.taskRecoveryWork.Idle, "the exact native result and task state establish the alias")
	identity := kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "spawn", toolCallID: "spawn"}
	rig.agent.dispatchMu.Lock()
	invocation := rig.agent.run("main").childInvocations[identity]
	task := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "old-task"}]
	bound := task.boundInvocation
	rig.agent.finishDispatch()
	require.NotNil(t, invocation)
	require.Same(t, invocation, bound, "only the verified task-ID result joins this full invocation")
	aliasKey := kimiInvocationOutcomeKey(invocation, "agent-0")
	_, exists := rig.sink.BackgroundTask(aliasKey)
	assert.True(t, exists, "a refused rename cannot discard the original invocation row")
	before := nativeTaskOf(t, rig.sink, "old-task")
	assert.Equal(t, bgtask.StatusSucceeded, before.Status)
	queue := agent.OutputQueueFor(rig.agent.sink)
	require.NotNil(t, queue)
	assert.True(t, queue.HasPending())
	sink.renameErr = nil
	require.NoError(t, queue.Flush())
	_, exists = rig.sink.BackgroundTask(aliasKey)
	assert.False(t, exists, "queue retry completes adoption without another native frame")
	after := nativeTaskOf(t, rig.sink, "old-task")
	assert.Equal(t, bgtask.StatusSucceeded, after.Status)
	assert.Equal(t, before.ChildAgentID, after.ChildAgentID)
	assert.Len(t, rig.sink.ChildAgentIDs(), 1)
	marker, exists := rig.sink.BackgroundTask("session_1/agent-0")
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleChildActivity, marker.Role)
	assert.Equal(t, bgtask.StatusRunning, marker.Status)
	assert.True(t, rig.agent.ActiveChildTurnState(marker.RowKey).Active)
}
