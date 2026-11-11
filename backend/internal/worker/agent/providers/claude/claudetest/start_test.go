package claudetest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartEchoAcceptsRawServiceFramesWithoutNativeRequests(t *testing.T) {
	running, err := StartEcho(t.Context(), agent.Options{AgentID: "echo", WorkingDir: t.TempDir()}, agent.NewProviderServices(&agenttest.ControlSink{}))
	require.NoError(t, err)
	t.Cleanup(func() {
		running.Stop()
		_ = running.Wait()
	})
	frame := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"service-request","response":{"behavior":"allow"}}}`)
	require.NoError(t, running.SendRawInput(frame, agent.StopContext{}))
	running.Stop()
	require.ErrorContains(t, running.SendRawInput(frame, agent.StopContext{}), "stopped")
}

func TestStartEchoReturnsRawFramesWhileTheProcessStaysOpen(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	running, err := StartEcho(t.Context(), agent.Options{AgentID: "echo", WorkingDir: t.TempDir()}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { running.Stop(); _ = running.Wait() })
	goals, ok := running.(agent.GoalCapable)
	require.True(t, ok)
	assert.Empty(t, goals.SupportedGoalActions())
	require.NoError(t, running.SendRawInput([]byte("{\"type\":\"system\",\"subtype\":\"init\",\"slash_commands\":[\"goal\"]}\n"), agent.StopContext{}))
	require.Eventually(t, func() bool {
		return len(goals.SupportedGoalActions()) != 0
	}, 30*time.Second, time.Millisecond)
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, goals.SupportedGoalActions())
	assert.False(t, running.IsStopped())
}

func TestStartEchoWithGoalCommandAdvertisesBeforeReturning(t *testing.T) {
	t.Parallel()
	running, err := StartEchoWithGoalCommand(t.Context(), agent.Options{AgentID: "goal-echo", WorkingDir: t.TempDir()}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { running.Stop(); _ = running.Wait() })
	goals, ok := running.(agent.GoalCapable)
	require.True(t, ok)
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear}, goals.SupportedGoalActions())
	running.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","commands":[]}`))
	assert.Empty(t, goals.SupportedGoalActions(), "later output still controls the advertised capability")
}

func TestStartEchoWithGoalCommandRejectsAMissingWorkingDirectory(t *testing.T) {
	t.Parallel()
	running, err := StartEchoWithGoalCommand(t.Context(), agent.Options{AgentID: "missing-directory", WorkingDir: filepath.Join(t.TempDir(), "missing")}, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Nil(t, running)
}
