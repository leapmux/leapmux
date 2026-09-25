package grok

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func parsedLine(t *testing.T, raw []byte) *providerkit.ParsedLine {
	t.Helper()
	line := providerkit.ParseLine(raw)
	require.NotNil(t, line)
	return line
}

func TestGrokConsumesItsOwnNotificationsSilently(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	for _, method := range []string{
		"_x.ai/sessions/changed", "_x.ai/session/interjection", "_x.ai/mcp/servers_changed", "_x.ai/setup/phase",
	} {
		line := parsedLine(t, frame(t, map[string]any{"method": method, "params": map[string]any{"sessionId": grokTestSession}}))
		assert.True(t, a.handleExtraMethod(line), method)
	}
	assert.Zero(t, sink.NotificationCount(), "Grok's TUI chrome never reaches the transcript")
}

func TestGrokLeavesAnUnknownRequestToTheBase(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, nil)
	// A REQUEST that LeapMux does not answer must reach the base, which refuses
	// it, or Grok would wait for an answer forever.
	assert.False(t, a.handleExtraMethod(parsedLine(t, frame(t, map[string]any{"id": 4, "method": "_x.ai/hooks/run", "params": map[string]any{}}))))
	assert.False(t, a.handleExtraMethod(parsedLine(t, frame(t, map[string]any{"method": "_other/notice", "params": map[string]any{}}))),
		"a method outside Grok's namespace is not Grok's")
}

func TestGrokIgnoresAnUnreadableSessionNotification(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	for _, raw := range [][]byte{
		frame(t, map[string]any{"method": "_x.ai/session_notification", "params": "text"}),
		frame(t, map[string]any{"method": "_x.ai/session_notification", "params": map[string]any{"sessionId": grokTestSession, "update": "text"}}),
		notification(t, grokTestSession, map[string]any{"sessionUpdate": "tool_call_delta_chunk", "tool_call_id": "c", "arguments_delta": "{"}),
		notification(t, grokTestSession, map[string]any{"sessionUpdate": "subagent_progress", "subagent_id": "s"}),
	} {
		a.HandleOutput(raw)
	}
	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.NotificationCount())
	assert.Empty(t, sink.BackgroundTasks())
}

// After a context clear, what the retired session reports reaches nothing: a
// subagent, a background command, a workflow run and the usage of that session
// stay out of the registry and away from the reader, as its session updates do.
func TestGrokIgnoresTheNotificationsOfARetiredSession(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, openingSession("session-2"))
	_, err := a.ClearContext()
	require.NoError(t, err)
	sessionInfos := sink.SessionInfoCount()

	a.HandleOutput(spawned(t, "sub-late", "Late helper", nil))
	a.HandleOutput(frame(t, map[string]any{
		"method": grokTaskBackgroundedMethod,
		"params": map[string]any{"sessionId": grokTestSession, "update": map[string]any{
			"sessionUpdate": "task_backgrounded", "task_id": "t9", "command": "sleep 60",
		}},
	}))
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "workflow_updated", "run_id": "run-late", "name": "deep-research", "status": "active",
	}))
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "response_completed", "usage": map[string]any{"input_tokens": 10, "output_tokens": 2},
	}))

	assert.Empty(t, sink.BackgroundTasks())
	assert.Equal(t, sessionInfos, sink.SessionInfoCount())
}

// A notification of the current session and one of a subagent session that a
// row routes are both read.
func TestGrokReadsTheNotificationsOfTheSessionsThatItServes(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(spawned(t, "sub-1", "Helper", nil))
	a.HandleOutput(notification(t, "sub-1", map[string]any{
		"sessionUpdate": "subagent_spawned", "subagent_id": "sub-nested", "child_session_id": "sub-nested",
		"parent_session_id": "sub-1", "subagent_type": "explore", "description": "Nested helper",
	}))

	_, parent := sink.BackgroundTask("sub-1")
	_, nested := sink.BackgroundTask("sub-nested")
	assert.True(t, parent)
	assert.True(t, nested, "a subagent session reports its own subagents")
}

func TestGrokReadsTheReplayTwinOfTheSessionNotification(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"method": grokSessionUpdateMethod,
		"params": map[string]any{"sessionId": grokTestSession, "update": map[string]any{
			"sessionUpdate": "goal_updated", "goal_id": "g-1", "objective": "Ship", "status": "active",
		}},
	}))
	_, ok := sink.LastGoal()
	assert.True(t, ok)
}

// A notification that states no session is the main session's.
func TestGrokNotificationWithNoSessionIsTheMainSessions(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"method": "_x.ai/session_notification",
		"params": map[string]any{"update": map[string]any{"sessionUpdate": "goal_updated", "goal_id": "g-1", "objective": "Ship", "status": "active"}},
	}))
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "g-1", goal.NativeID)
}
