//go:build unix

package claude

import (
	"io"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newClaudeGoalAgent gives a Claude agent whose stdin is a real pipe. The read
// end drains so a long command cannot fill the pipe buffer.
func newClaudeGoalAgent(t *testing.T, sink agent.ProviderServices) *Agent {
	t.Helper()
	readPipe, writePipe, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = writePipe.Close()
		_ = readPipe.Close()
	})
	go func() { _, _ = io.Copy(io.Discard, readPipe) }()
	agent := newTestAgent(sink)
	agent.SetStdinForTest(writePipe)
	return agent
}

// A goal command that enters through the normal input queue must update the
// local goal row after the provider accepts it. The provider cannot observe
// delivery before Manager.SendInput returns successfully.
func TestManagerGoal_SendInputObservesADeliveredClaudeGoalCommand(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	provider := newClaudeGoalAgent(t, agent.NewProviderServices(sink))
	provider.HandleOutput([]byte(
		`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	m := agent.NewManager(claudeTestRegistry, nil)
	m.PutAgentForTest("root", provider)

	require.NoError(t, m.SendInput("root", "/goal ship the release", nil))

	goal, ok := sink.LastGoal()
	require.True(t, ok, "delivery must update the local goal row")
	assert.Equal(t, "ship the release", goal.Objective)
}

func TestManagerGoal_SendInputDoesNotObserveARefusedCommand(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	provider := newClaudeGoalAgent(t, agent.NewProviderServices(sink))
	provider.HandleOutput([]byte(
		`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	provider.SetStoppedForTest(true)
	m := agent.NewManager(claudeTestRegistry, nil)
	m.PutAgentForTest("root", provider)

	assert.Error(t, m.SendInput("root", "/goal ship the release", nil))
	assert.Empty(t, sink.Goals())
}

func TestManagerGoal_UpdateGoalReturnsTextRouteCommand(t *testing.T) {
	t.Parallel()
	provider := newClaudeGoalAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	provider.HandleOutput([]byte(
		`{"type":"system","subtype":"init","slash_commands":["goal"]}`))
	m := agent.NewManager(claudeTestRegistry, nil)
	m.PutAgentForTest("root", provider)

	command, err := m.UpdateGoal("root", agent.GoalActionSet, "  ship\n the release ")

	require.NoError(t, err)
	assert.Equal(t, "/goal ship the release", command)
}
