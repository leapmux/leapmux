package opencode

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeUsageRejectsUnrepresentableCounters(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`-1`, `1.25`, `9007199254740992`, `null`, `"3"`, `1e400`} {
		_, err := openCodeUsageCounts([]byte(`{"input":` + input + `,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}`))
		require.Error(t, err, input)
	}
	_, err := openCodeUsageCounts([]byte(`{"input":0,"output":9007199254740991,"reasoning":9007199254740991,"cache":{"read":0,"write":0}}`))
	require.Error(t, err)
	_, err = openCodeUsageCounts([]byte(`{"input":0,"output":0,"reasoning":0}`))
	require.Error(t, err)
	counts, err := openCodeUsageCounts([]byte(`{"input":9007199254740991,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}`))
	require.NoError(t, err)
	assert.Equal(t, int64(9007199254740991), counts.Input)
}

func TestNativeUsageUsesTheStartedStepModel(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	other := a.root.catalog.models[1]
	a.root.catalog.models[1].Limit.Context = 64000
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.step.started", map[string]any{
		"sessionID": "ses_1", "assistantMessageID": "msg_1", "model": map[string]any{"providerID": other.ProviderID, "id": other.ID},
	})))
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.step.ended", map[string]any{
		"sessionID": "ses_1", "assistantMessageID": "msg_1", "tokens": map[string]any{"input": 12000, "output": 40, "reasoning": 0, "cache": map[string]any{"read": 0, "write": 0}},
	})))
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, int64(64000), value.(map[string]any)[contracts.ContextUsageFieldContextWindow])
	assert.Empty(t, a.root.stepModels)
}

func TestNativeUsageKeepsCumulativeCountersOutOfContext(t *testing.T) {
	t.Parallel()
	for _, cost := range []float64{0, 1.25, -1} {
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
		a, sink := assemblyAgent(t, rpc)
		require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.usage.updated", map[string]any{
			"sessionID": "ses_1", "cost": cost,
			"tokens": map[string]any{"input": 90000, "output": 5000, "reasoning": 5000, "cache": map[string]any{"read": 0, "write": 0}},
		})))
		_, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
		assert.False(t, ok)
		value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyTotalCostUsd)
		assert.Equal(t, cost >= 0, ok)
		if ok {
			assert.Equal(t, cost, value)
		}
	}
}

func TestNativeUsageRestoresTheLastAssistantCounts(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session/ses_1":
			_, _ = w.Write([]byte(`{"data":{"id":"ses_1","agent":"build"}}`))
		case "/api/session/ses_1/message":
			_, _ = w.Write([]byte(`{"data":[{"id":"msg_saved","type":"assistant","model":{"providerID":"provider","id":"model"},"content":[],"tokens":{"input":321,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}],"cursor":{}}`))
		case "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		}
	})
	a, sink := assemblyAgent(t, rpc)
	require.NoError(t, a.restoreRun(t.Context(), a.root))
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, int64(321), value.(map[string]any)[contracts.ContextUsageFieldInputTokens])
}

func TestNativeUsagePublishesPerStepCounts(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.step.ended", map[string]any{
		"sessionID": "ses_1", "assistantMessageID": "msg_1", "cost": 0,
		"tokens": map[string]any{"input": 12000, "output": 40, "reasoning": 6, "cache": map[string]any{"read": 100, "write": 200}},
	})))
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens: int64(12000), contracts.ContextUsageFieldOutputTokens: int64(46),
		contracts.ContextUsageFieldCacheReadInputTokens: int64(100), contracts.ContextUsageFieldCacheCreationInputTokens: int64(200),
		contracts.ContextUsageFieldContextWindow: int64(128000),
	}, value)
}

func TestNativeUsagePreservesZero(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	require.NoError(t, a.acceptNativeEvent(t.Context(), assemblyEvent(t, "session.step.ended", map[string]any{
		"sessionID": "ses_1", "assistantMessageID": "msg_1", "cost": 0,
		"tokens": map[string]any{"input": 0, "output": 0, "reasoning": 0, "cache": map[string]any{"read": 0, "write": 0}},
	})))
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage, ok := value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldInputTokens])
}
