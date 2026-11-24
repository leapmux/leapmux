package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func claudeTestSteerRequest(content string, attachments []*leapmuxv1.Attachment) agent.SteerRequest {
	return agent.SteerRequest{InputID: "queued-steer", AttemptID: "attempt-steer", Content: content, Attachments: attachments}
}

func TestClaudeSteerWritesNextPriority(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	steer := claudeTestSteerRequest("guide", nil)
	agent := &Agent{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Stdin: writer}), turnActive: true, sessionID: "s-1"}
	require.NoError(t, agent.SteerInput(steer))
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
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"s-1"}`))
	require.NoError(t, a.SteerInput(claudeTestSteerRequest("steer", nil)))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
	assert.ErrorIs(t, a.SendInput("next", nil), agent.ErrAgentBusy)
	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
	assert.NoError(t, a.SendInput("next", nil))
}
