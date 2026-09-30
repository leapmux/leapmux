package fastagent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestFastagentStatusLineReportsContextUsageFromOuterACPMetadata(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{}
	services := agent.NewProviderServices(sink)
	a.SetSinkForTest(services)
	*a.HooksForTest() = a.hooks("gpt-4o", services)
	a.SetSessionIDForTest("session-1")
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}},"_meta":{"field_meta":{"openhands.dev/metrics":{"status_line":"1,200 in, 80 out, 2 tools (9.5%)"}}}}}`))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok, "Fast Agent's native status line must reach the Worker context state")
	usage, ok := value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(1200), usage[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(80), usage[contracts.ContextUsageFieldOutputTokens])
	assert.Equal(t, float64(9.5), usage[contracts.ContextUsageFieldUsagePercent])
	assert.NotContains(t, usage, contracts.ContextUsageFieldContextWindow,
		"the native status line reports a percentage but no exact context window")
}

func TestFastagentStatusLineParserRejectsMalformedCounts(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		"1,20 in, 80 out",
		"1,200 in, -80 out",
		"1,200 in, 80 out trailing text",
		"999999999999999999999999999999 in, 80 out",
		"1,200 in, 80 out (infinity%)",
	} {
		usage, ok := fastagentStatusLineUsage(line)
		assert.False(t, ok, line)
		assert.Nil(t, usage, line)
	}
}

func TestFastagentStatusLineParserKeepsCountsWithoutPercentage(t *testing.T) {
	t.Parallel()
	usage, ok := fastagentStatusLineUsage("120 in, 30 out")
	require.True(t, ok)
	assert.Equal(t, int64(120), usage[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(30), usage[contracts.ContextUsageFieldOutputTokens])
	assert.NotContains(t, usage, contracts.ContextUsageFieldUsagePercent)
}
