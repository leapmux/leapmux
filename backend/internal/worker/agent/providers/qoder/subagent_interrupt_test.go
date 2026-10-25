package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Qoder CLI 1.1.78 stops an Agent tool's child with stop_task, but a Workflow
// script's child is not a task of the session, so only a task child offers
// an interrupt.
func TestQoderChildCapabilitiesFollowTheChildKind(t *testing.T) {
	t.Parallel()
	plugin := qoderProvider{}
	assert.Equal(t, agent.ChildCapabilities{AcceptsInterrupt: true}, plugin.ChildCapabilities(optionmap.Map{qoderOptionChildKind: qoderChildKindTask}))
	for name, options := range map[string]optionmap.Map{
		"no options":        nil,
		"a workflow child":  {},
		"another kind word": {qoderOptionChildKind: "workflow"},
	} {
		assert.Equal(t, agent.ChildCapabilities{}, plugin.ChildCapabilities(options), name)
	}
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil), optionmap.Map{qoderOptionChildKind: qoderChildKindTask})
}

func TestQoderTaskChildOpensAsAnInterruptibleTask(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(qoderChildStarted))
	row, found := sink.BackgroundTask("spawn-1")
	require.True(t, found)
	require.NotEmpty(t, row.ChildAgentID)
	assert.Equal(t, qoderChildKindTask, sink.Child(row.ChildAgentID).LastSettingsRefresh().Options[qoderOptionChildKind])
	a.mu.Lock()
	assert.Equal(t, map[string]string{"spawn-1": "task-1"}, a.childTaskIDs)
	a.mu.Unlock()
}

func TestQoderWorkflowChildOpensWithoutATaskKind(t *testing.T) {
	t.Parallel()
	fixture := newQoderWorkflowFixture(t)
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: fixture.homeDir, WorkingDir: fixture.workingDir}
	a.HandleOutput([]byte(qoderWorkflowLaunch))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"child-task-1","tool_use_id":"run-workflow","task_type":"local_agent","subagent_type":"workflow-subagent","description":"First child","prompt":"Reply with FIRST."}`))
	row, found := sink.BackgroundTask("child-task-1")
	require.True(t, found)
	require.NotEmpty(t, row.ChildAgentID)
	assert.Zero(t, sink.Child(row.ChildAgentID).SettingsRefreshCount(), "a workflow child opens with no child kind")
	a.mu.Lock()
	assert.Empty(t, a.childTaskIDs, "stop_task cannot stop a workflow child")
	a.mu.Unlock()
	assert.ErrorIs(t, a.InterruptChild("child-task-1", agent.StopContext{}), agent.ErrChildRouteNotReady)
}

func TestQoderInterruptChildSendsStopTask(t *testing.T) {
	t.Parallel()
	a, script := newQoderControlScript(t, map[string][]qoderScriptedReply{"stop_task": {{response: noPayload}}})
	a.HandleOutput([]byte(qoderChildStarted))
	require.NoError(t, a.InterruptChild("spawn-1", agent.StopContext{}))
	require.Equal(t, []string{"stop_task"}, script.subtypes())
	script.mu.Lock()
	request := string(script.requests[0].request)
	script.mu.Unlock()
	assert.JSONEq(t, `{"subtype":"stop_task","task_id":"task-1"}`, request)
}

func TestQoderInterruptChildRefusals(t *testing.T) {
	t.Parallel()
	t.Run("an unknown child", func(t *testing.T) {
		t.Parallel()
		a, script := newQoderControlScript(t, nil)
		assert.ErrorIs(t, a.InterruptChild("spawn-unknown", agent.StopContext{}), agent.ErrChildRouteNotReady)
		assert.Empty(t, script.subtypes())
	})
	t.Run("a native refusal", func(t *testing.T) {
		t.Parallel()
		a, _ := newQoderControlScript(t, map[string][]qoderScriptedReply{"stop_task": {{refusal: "stop_task failed"}}})
		a.HandleOutput([]byte(qoderChildStarted))
		err := a.InterruptChild("spawn-1", agent.StopContext{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stop_task failed")
	})
}

// Qoder's own notification closes the stopped child, and the task ID goes with
// it, so a late Interrupt sends nothing.
func TestQoderStoppedChildDropsItsTaskID(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, script := newQoderControlScript(t, nil)
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(qoderChildStarted))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-1","tool_use_id":"spawn-1","status":"stopped","summary":"Ask for one word"}`))
	row, found := sink.BackgroundTask("spawn-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.ErrorIs(t, a.InterruptChild("spawn-1", agent.StopContext{}), agent.ErrChildRouteNotReady)
	assert.Empty(t, script.subtypes())
}
