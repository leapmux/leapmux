package commandcode

import (
	"testing"

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
