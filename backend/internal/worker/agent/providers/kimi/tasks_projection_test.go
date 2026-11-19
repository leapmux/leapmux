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
	main := a.run("main")
	waitFor(t, func() bool { return main.conversationRecovery.Load() == nil }, "the native main page precedes live output")
	native.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	(kimiFinalityTool{background: true}).publish(t, native)
	child := a.run("agent-0")
	waitFor(t, func() bool { return child.conversationRecovery.Load() == nil }, "the child receives its own native page")
	a.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Old assignment"}`))
	a.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","result_summary":"A completed review."}`))
	a.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", `{"type":"turn.started","turn":{"turn_id":"independent","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`))
	waitFor(t, a.taskReportWork.Idle, "the completed report finishes before alias fault injection")
	awaitKimiTestOutput(t, a.sink)
	sink.renameErr = errors.New("registry alias write refused")
	(kimiFinalityTool{outcome: "completed", output: "Background work continues.", background: true, taskID: "old-task"}).publish(t, native)
	waitFor(t, a.taskRecoveryWork.Idle, "the exact native reference and task state establish the alias")
	identity := kimiFinalityIdentity()
	rig.agent.dispatchMu.Lock()
	invocation := rig.agent.run("main").childInvocations[identity]
	task := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "old-task"}]
	var bound *kimiChildInvocation
	if task != nil {
		bound = task.boundInvocation
	}
	rig.agent.finishDispatch()
	require.NotNil(t, task)
	require.NotNil(t, invocation)
	require.Same(t, invocation, bound, "only the verified native task reference joins this invocation")
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
