package gemini

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiSessionUsageReadsTheInstalledNativeTokenShape(t *testing.T) {
	t.Parallel()
	usage := geminiSessionUsage(geminiSession{Messages: []geminiMessage{{Type: "gemini", Tokens: json.RawMessage(`{"input":42,"output":7,"cached":0,"total":49}`)}}})
	require.NotNil(t, usage)
	assert.Equal(t, int64(42), usage[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(7), usage[contracts.ContextUsageFieldOutputTokens])
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldCacheReadInputTokens])
}

func TestGeminiContextUsageReadsTheLastNativeRequestInsteadOfTheTurnSum(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	path := geminiFixtureSessionPath(directory, "root")
	writeGeminiSession(t, path, "root", hash, "main", "native prompt")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(`{"id":"first","type":"gemini","content":[{"text":"first answer"}],"tokens":{"input":100,"output":5,"cached":20,"thoughts":0,"total":105}}` + "\n" + `{"id":"last","type":"gemini","content":[{"text":"last answer"}],"tokens":{"input":140,"output":7,"cached":30,"thoughts":0,"total":147}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	sink := &agenttest.Sink{}
	services := &geminiOutputServices{ProviderServices: agent.NewProviderServices(sink), query: query, currentSession: func() string { return "root" }}
	require.NoError(t, services.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"stopReason":"end_turn","_meta":{"quota":{"token_count":{"input_tokens":240,"output_tokens":12}}}}`)}, agent.SpanInfo{}))
	usage, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens:              int64(110),
		contracts.ContextUsageFieldOutputTokens:             int64(7),
		contracts.ContextUsageFieldCacheReadInputTokens:     int64(30),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(0),
	}, usage)
}

func TestGeminiSessionUsagePreservesZeroAndRejectsInvalidCounts(t *testing.T) {
	t.Parallel()
	require.NotNil(t, geminiSessionUsage(geminiSession{Messages: []geminiMessage{{Type: "gemini", Tokens: json.RawMessage(`{"input":0,"output":0}`)}}}))
	for _, raw := range []string{`{`, `{}`, `null`, `{"input":-1,"output":0}`, `{"input":0,"output":-1}`, `{"input":1.5,"output":0}`, `{"input":0,"output":0,"cached":1}`, `{"input":1,"output":0,"cached":-1}`, `{"input":1}`, `{"output":1}`, `{"input_tokens":42,"output_tokens":7}`} {
		assert.Nil(t, geminiSessionUsage(geminiSession{Messages: []geminiMessage{{Type: "gemini", Tokens: json.RawMessage(raw)}}}), "record %q", raw)
	}
}
