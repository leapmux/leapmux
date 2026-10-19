package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeSteerWritesNextPriority(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	agent := &Agent{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Stdin: writer}), turnActive: true}
	require.NoError(t, agent.SteerInput("guide", nil))
	line, err := bufio.NewReader(reader).ReadBytes('\n')
	require.NoError(t, err)
	var input struct {
		Priority string `json:"priority"`
	}
	require.NoError(t, json.Unmarshal(line, &input))
	assert.Equal(t, "next", input.Priority)
}

func TestClaudeSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := &Agent{turnActive: true, sink: agent.NewProviderServices(sink)}
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, agent, agent.SendInput("later turn", nil))
}

func TestClaudeSteerMarksTheTurnAwaitingItsResult(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	require.NoError(t, a.SteerInput("steer", nil))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
	assert.ErrorIs(t, a.SendInput("next", nil), agent.ErrAgentBusy)
	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
	assert.NoError(t, a.SendInput("next", nil))
}
