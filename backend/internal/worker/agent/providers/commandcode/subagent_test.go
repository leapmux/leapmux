package commandcode

import (
	"errors"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childSpawn(t *testing.T, a *Agent, background bool) {
	t.Helper()
	feedEvent(t, a, map[string]any{"type": "tool_queued", "toolCallId": "spawn", "toolName": "agent", "input": map[string]any{"description": "Native child", "prompt": "Exact native child prompt.", "run_in_background": background}})
	feedEvent(t, a, map[string]any{"type": "subagent_start", "toolCallId": "spawn", "subagentType": "general", "description": "Native child", "background": background})
}

func TestNativeForegroundChildKeepsItsPromptAndReport(t *testing.T) {
	a, sink := testAgent(t)
	childSpawn(t, a, false)
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	feedEvent(t, a, map[string]any{"type": "subagent_stop", "toolCallId": "spawn", "subagentType": "general", "tokensUsed": 2})
	feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "spawn", "toolName": "agent", "result": []map[string]string{{"type": "text", "text": "Exact native child report.\n\n<usage>total_tokens: 2</usage>"}}})
	child := sink.Child(ids[0])
	require.NotNil(t, child)
	assert.Contains(t, string(child.Messages()[0].Content), "Exact native child prompt.")
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Contains(t, reports[0]["text"], "Exact native child report.")
	row, exists := sink.BackgroundTask(childRowKey("native-session", "spawn"))
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Empty(t, a.children)
}

func TestNativeBackgroundChildCanFinishBeforeItsLaunchReply(t *testing.T) {
	a, sink := testAgent(t)
	childSpawn(t, a, true)
	feedEvent(t, a, map[string]any{"type": "subagent_stop", "toolCallId": "spawn", "subagentType": "general", "status": "completed"})
	feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "spawn", "toolName": "agent", "result": []map[string]string{{"type": "text", "text": "Background agent launched.\nagent_id: native-child\nThe agent is working in the background."}}})
	row, exists := sink.BackgroundTask(childRowKey("native-session", "native-child"))
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Empty(t, a.children)
}

func TestNativeChildProgressPreservesTheOriginalFrame(t *testing.T) {
	a, sink := testAgent(t)
	childSpawn(t, a, false)
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	raw := feedEvent(t, a, map[string]any{"type": "subagent_progress", "toolCallId": "spawn", "subagentType": "general", "toolName": "read_file", "toolInput": "/native/file.txt", "tokensUsed": 0})
	child := sink.Child(ids[0])
	require.NotNil(t, child)
	messages := child.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, raw, messages[1].Content)
}

func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	agenttest.AssertChildCapabilities(t, commandcodeProvider{}, (*Agent)(nil))
}

func TestCommandCodeRootInterruptClosesForegroundChildAndPreservesBackgroundChild(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	a.startTurn("turn_1")
	feedEvent(t, a, map[string]any{"type": "tool_queued", "toolCallId": "fg-spawn", "toolName": "agent", "input": map[string]any{"description": "Foreground child", "prompt": "Foreground prompt.", "run_in_background": false}})
	feedEvent(t, a, map[string]any{"type": "subagent_start", "toolCallId": "fg-spawn", "subagentType": "general", "description": "Foreground child", "background": false})
	feedEvent(t, a, map[string]any{"type": "tool_queued", "toolCallId": "bg-spawn", "toolName": "agent", "input": map[string]any{"description": "Background child", "prompt": "Background prompt.", "run_in_background": true}})
	feedEvent(t, a, map[string]any{"type": "subagent_start", "toolCallId": "bg-spawn", "subagentType": "general", "description": "Background child", "background": true})

	feedMethod(t, a, "turn/completed", map[string]any{"turnId": "turn_1", "stopReason": "interrupted"})

	fgRow, fgExists := sink.BackgroundTask(childRowKey("native-session", "fg-spawn"))
	require.True(t, fgExists)
	assert.Equal(t, agent.IncompleteTaskStatus(agent.MessageCompletionInterrupted), fgRow.Status, "foreground child must close on root interrupt")

	bgRow, bgExists := sink.BackgroundTask(childRowKey("native-session", "bg-spawn"))
	require.True(t, bgExists)
	assert.Equal(t, bgtask.StatusRunning, bgRow.Status, "background child must survive root interrupt")

	// Idle interrupt also preserves background children.
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	bgRow, _ = sink.BackgroundTask(childRowKey("native-session", "bg-spawn"))
	assert.Equal(t, bgtask.StatusRunning, bgRow.Status)

	// finishStream closes remaining background children on teardown.
	a.finishStream()
	bgRow, _ = sink.BackgroundTask(childRowKey("native-session", "bg-spawn"))
	assert.Equal(t, bgtask.StatusFailed, bgRow.Status)
}

type failingRenameSink struct {
	*agenttest.Sink
	renameErr error
}

func (s *failingRenameSink) RenameBackgroundTask(oldKey, newKey string) error {
	if s.renameErr != nil {
		return s.renameErr
	}
	return s.Sink.RenameBackgroundTask(oldKey, newKey)
}

func TestBackgroundChildFailureMissingIDAndFailedRenameCloseCleanly(t *testing.T) {
	t.Run("tool error closes background row as failed", func(t *testing.T) {
		a, sink := testAgent(t)
		childSpawn(t, a, true)
		feedEvent(t, a, map[string]any{"type": "tool_errored", "toolCallId": "spawn", "toolName": "agent", "error": "launch failed"})
		row, exists := sink.BackgroundTask(childRowKey("native-session", "spawn"))
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusFailed, row.Status)
		assert.Empty(t, a.children)
	})

	t.Run("tool denied closes background row as failed", func(t *testing.T) {
		a, sink := testAgent(t)
		childSpawn(t, a, true)
		feedEvent(t, a, map[string]any{"type": "tool_denied", "toolCallId": "spawn", "toolName": "agent"})
		row, exists := sink.BackgroundTask(childRowKey("native-session", "spawn"))
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusFailed, row.Status)
		assert.Empty(t, a.children)
	})

	t.Run("missing agent_id still marks identified and closes on stop", func(t *testing.T) {
		a, sink := testAgent(t)
		childSpawn(t, a, true)
		feedEvent(t, a, map[string]any{"type": "subagent_stop", "toolCallId": "spawn", "subagentType": "general", "status": "completed"})
		feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "spawn", "toolName": "agent", "result": []map[string]string{{"type": "text", "text": "Background agent launched without identifier."}}})
		row, exists := sink.BackgroundTask(childRowKey("native-session", "spawn"))
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status)
		assert.Empty(t, a.children)
	})

	t.Run("missing agent_id closes when stop arrives after tool_completed", func(t *testing.T) {
		a, sink := testAgent(t)
		childSpawn(t, a, true)
		feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "spawn", "toolName": "agent", "result": []map[string]string{{"type": "text", "text": "Background agent launched without identifier."}}})
		feedEvent(t, a, map[string]any{"type": "subagent_stop", "toolCallId": "spawn", "subagentType": "general", "status": "completed"})
		row, exists := sink.BackgroundTask(childRowKey("native-session", "spawn"))
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status)
		assert.Empty(t, a.children)
	})

	t.Run("failed rename still marks identified and closes under original key", func(t *testing.T) {
		a, sink := testAgent(t)
		failing := &failingRenameSink{Sink: sink, renameErr: errors.New("rename refused")}
		a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(failing))
		childSpawn(t, a, true)
		feedEvent(t, a, map[string]any{"type": "subagent_stop", "toolCallId": "spawn", "subagentType": "general", "status": "completed"})
		feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "spawn", "toolName": "agent", "result": []map[string]string{{"type": "text", "text": "Background agent launched.\nagent_id: native-child"}}})
		row, exists := sink.BackgroundTask(childRowKey("native-session", "spawn"))
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status)
		assert.Empty(t, a.children)
	})
}

func TestChildStatusMapsKilledAndAbortedToStopped(t *testing.T) {
	assert.Equal(t, bgtask.StatusStopped, childStatus("killed"))
	assert.Equal(t, bgtask.StatusStopped, childStatus("aborted"))

	a, sink := testAgent(t)
	childSpawn(t, a, true)
	feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "spawn", "toolName": "agent", "result": []map[string]string{{"type": "text", "text": "agent_id: bg-killed"}}})
	feedEvent(t, a, map[string]any{"type": "subagent_stop", "toolCallId": "spawn", "subagentType": "general", "status": "killed"})
	row, exists := sink.BackgroundTask(childRowKey("native-session", "bg-killed"))
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
	assert.Empty(t, a.children)
}

func TestSubagentBackgroundedSetsBackgroundFlagWithoutPersistingRawRow(t *testing.T) {
	a, sink := testAgent(t)
	childSpawn(t, a, false)
	beforeMessages := len(sink.Messages())
	feedEvent(t, a, map[string]any{"type": "subagent_backgrounded", "toolCallId": "spawn"})
	assert.Equal(t, beforeMessages, len(sink.Messages()), "subagent_backgrounded must not persist a raw transcript row")
	a.Mu.Lock()
	state := a.children["spawn"]
	require.NotNil(t, state)
	bg := state.background
	a.Mu.Unlock()
	assert.True(t, bg, "subagent_backgrounded promotes child state to background")

	feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "spawn", "toolName": "agent", "result": []map[string]string{{"type": "text", "text": "agent_id: promoted-child"}}})
	row, exists := sink.BackgroundTask(childRowKey("native-session", "promoted-child"))
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusRunning, row.Status, "promoted background child stays running after launch reply")

	feedEvent(t, a, map[string]any{"type": "subagent_stop", "toolCallId": "spawn", "subagentType": "general", "status": "completed"})
	row, exists = sink.BackgroundTask(childRowKey("native-session", "promoted-child"))
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Empty(t, a.children)
}

func TestSubagentStopWithNonFinalStatusSettlesToSucceeded(t *testing.T) {
	a, sink := testAgent(t)
	childSpawn(t, a, true)
	feedEvent(t, a, map[string]any{"type": "subagent_stop", "toolCallId": "spawn", "subagentType": "general", "status": "running"})
	feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "spawn", "toolName": "agent", "result": []map[string]string{{"type": "text", "text": "agent_id: bg-nonfinal"}}})
	row, exists := sink.BackgroundTask(childRowKey("native-session", "bg-nonfinal"))
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Empty(t, a.children)
}
