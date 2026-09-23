package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleKiloOutput_ConfigOptionUpdateRefreshesModelsGenerically(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newKiloAgentWithSink(agent.NewProviderServices(sink))
	agent.SetModelForTest("anthropic/claude-sonnet-4")
	agent.SetCurrentPrimaryAgentForTest(PrimaryAgentCode)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"code","name":"Code"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, "openai/gpt-5", agent.ModelForTest())
	require.Len(t, agent.AvailableModelsForTest(), 2)
	assert.Equal(t, "openai/gpt-5", agent.AvailableModelsForTest()[1].GetId())
	assert.True(t, agent.AvailableModelsForTest()[1].IsDefault)
	// The `mode` config option carries the primary agent for Kilo; the runtime update
	// syncs it alongside the model (code -> plan).
	assert.Equal(t, opencode.PrimaryAgentPlan, agent.CurrentPrimaryAgentForTest())
}
