package codewhale

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func int64p(v int64) *int64 { return &v }

func TestRequestUsageCounts(t *testing.T) {
	t.Parallel()
	// DeepSeek states the whole prompt and its cache split.
	assert.Equal(t, providerkit.ContextTokenCounts{Input: 8750, CacheRead: 8448, Output: 62},
		requestUsage{InputTokens: 17198, OutputTokens: 62, CacheHitTokens: int64p(8448), CacheMissTokens: int64p(8750)}.counts())
	// A route with a write count and no miss count: the prompt minus the cached parts.
	assert.Equal(t, providerkit.ContextTokenCounts{Input: 70, CacheRead: 20, CacheWrite: 10, Output: 5},
		requestUsage{InputTokens: 100, OutputTokens: 5, CacheHitTokens: int64p(20), CacheWriteTokens: int64p(10)}.counts())
	// A route that states no cache.
	assert.Equal(t, providerkit.ContextTokenCounts{Input: 10, Output: 5}, requestUsage{InputTokens: 10, OutputTokens: 5}.counts())
	// A split larger than the prompt never goes negative.
	assert.Equal(t, providerkit.ContextTokenCounts{CacheRead: 50}, requestUsage{InputTokens: 10, CacheHitTokens: int64p(50)}.counts())
}

func TestTurnUsageIsBroadcast(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(runtimeEvent(1, "turn.usage", testTurnID, "", map[string]any{"usage": map[string]any{"input_tokens": 100, "output_tokens": 7, "prompt_cache_hit_tokens": 60, "prompt_cache_miss_tokens": 40}}))
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage := value.(map[string]any)
	assert.EqualValues(t, 40, usage[contracts.ContextUsageFieldInputTokens])
	assert.EqualValues(t, 60, usage[contracts.ContextUsageFieldCacheReadInputTokens])
	assert.EqualValues(t, 7, usage[contracts.ContextUsageFieldOutputTokens])

	// A report with nothing in it, and a malformed one, broadcast nothing.
	count := sink.SessionInfoCount()
	a.HandleOutput(runtimeEvent(2, "turn.usage", testTurnID, "", map[string]any{"usage": map[string]any{}}))
	a.HandleOutput(runtimeEvent(3, "turn.usage", testTurnID, "", map[string]any{"usage": "x"}))
	assert.Equal(t, count, sink.SessionInfoCount())
}

func TestTheContextReportIsAuthoritative(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.applyContextReport(json.RawMessage(`{"window_tokens":128000,"input_tokens":9000}`))
	a.HandleOutput(runtimeEvent(1, "turn.usage", testTurnID, "", map[string]any{"usage": map[string]any{"input_tokens": 100, "output_tokens": 7}}))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage := value.(map[string]any)
	assert.EqualValues(t, 9000, usage[contracts.ContextUsageFieldContextTokens], "a request's counts never overwrite the runtime's own total")
	assert.EqualValues(t, 128000, usage[contracts.ContextUsageFieldContextWindow])
	assert.EqualValues(t, 100, usage[contracts.ContextUsageFieldInputTokens])

	count := sink.SessionInfoCount()
	for _, raw := range []string{`{"window_tokens":null,"input_tokens":null}`, `not json`} {
		a.applyContextReport(json.RawMessage(raw))
	}
	assert.Equal(t, count, sink.SessionInfoCount())
}

func TestTurnEndContentCarriesTheCountAndTheUsage(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	raw := []byte(`{"event":"turn.completed"}`)
	bare := a.turnEndContent(raw)
	assert.Equal(t, raw, bare.Original)
	assert.Equal(t, map[string]any{contracts.MessageMetadataFieldToolUses: float64(0)}, decodeJSON(t, bare.Metadata))

	a.TurnToolUses = 3
	a.applyContextReport(json.RawMessage(`{"window_tokens":1000,"input_tokens":10}`))
	content := a.turnEndContent(raw)
	metadata := decodeJSON(t, content.Metadata)
	assert.EqualValues(t, 3, metadata[contracts.MessageMetadataFieldToolUses])
	assert.Contains(t, metadata, contracts.SessionInfoKeyContextUsage)
}

func TestTheContextRouteIsReadOnlyWhereItExists(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := threadPath(testThreadID, threadRouteContext)
	rt.respondJSON(http.MethodGet, route, http.StatusOK, map[string]any{"window_tokens": 1000, "input_tokens": 10})
	a, _ := newTestAgent(t, rt)
	a.refreshContextUsage()
	assert.Empty(t, rt.requestsTo(http.MethodGet, route), "0.9.13 has no context route")

	assert.True(t, a.probeContextRoute(testThreadID))
	a.runtime.hasContextRoute = true
	a.refreshContextUsage()
	assert.Eventually(t, func() bool { return len(rt.requestsTo(http.MethodGet, route)) == 2 }, 30*time.Second, 5*time.Millisecond)

	missing := newFakeRuntime(t)
	b, _ := newTestAgent(t, missing)
	assert.False(t, b.probeContextRoute(testThreadID))
}

// A fresh thread's report can state the window with no context total yet. The
// window is still the runtime's own reading, and a request's counts must not
// drop it.
func TestARequestKeepsTheWindowOfAReportWithNoTotal(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.applyContextReport(json.RawMessage(`{"window_tokens":128000,"input_tokens":0}`))
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage := value.(map[string]any)
	assert.EqualValues(t, 128000, usage[contracts.ContextUsageFieldContextWindow])
	assert.NotContains(t, usage, contracts.ContextUsageFieldContextTokens, "a zero total is no reading")

	a.HandleOutput(runtimeEvent(1, "turn.usage", testTurnID, "", map[string]any{"usage": map[string]any{"input_tokens": 100, "output_tokens": 7}}))
	value, ok = sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage = value.(map[string]any)
	assert.EqualValues(t, 128000, usage[contracts.ContextUsageFieldContextWindow], "the request's counts keep the runtime's window")
	assert.EqualValues(t, 100, usage[contracts.ContextUsageFieldInputTokens])
	assert.EqualValues(t, 7, usage[contracts.ContextUsageFieldOutputTokens])
}

// Each request states all four counts, so a later request replaces every count
// of the one before it.
func TestALaterRequestReplacesEveryCount(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(runtimeEvent(1, "turn.usage", testTurnID, "", map[string]any{"usage": map[string]any{"input_tokens": 100, "output_tokens": 7, "prompt_cache_hit_tokens": 60, "prompt_cache_miss_tokens": 40}}))
	a.HandleOutput(runtimeEvent(2, "turn.usage", testTurnID, "", map[string]any{"usage": map[string]any{"input_tokens": 30, "output_tokens": 2}}))
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, providerkit.ContextUsageMap(providerkit.ContextTokenCounts{Input: 30, Output: 2}), value, "no cache count of the first request survives")
}
