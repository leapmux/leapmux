package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeContextUsageMap_EmptyReturnsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, zcodeContextUsageMap(zcodeUsage{}, 200000),
		"zeros must not overwrite a real reading")
}

func TestZCodeContextUsageMap_MapsTokensAndTheOptionalWindow(t *testing.T) {
	t.Parallel()

	got := zcodeContextUsageMap(zcodeUsage{
		InputTokens:      10,
		OutputTokens:     4,
		CacheReadTokens:  2,
		CacheWriteTokens: 3,
		TotalTokens:      19,
	}, 128000)
	assert.Equal(t, map[string]any{
		"input_tokens":                int64(10),
		"output_tokens":               int64(4),
		"cache_read_input_tokens":     int64(2),
		"cache_creation_input_tokens": int64(3),
		"context_window":              int64(128000),
	}, got)

	withoutWindow := zcodeContextUsageMap(zcodeUsage{InputTokens: 1}, 0)
	_, hasWindow := withoutWindow["context_window"]
	assert.False(t, hasWindow, "a zero window is unknown, not a real size")
}

func TestZCodeContextUsageFromRuntime_NilOrUnusedReturnsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, zcodeContextUsageFromRuntime(nil))
	assert.Nil(t, zcodeContextUsageFromRuntime(&zcodeContextUsage{}))
	assert.Nil(t, zcodeContextUsageFromRuntime(&zcodeContextUsage{Used: 0, Size: 200000}),
		"a zero used count is not a reading")
}

func TestZCodeContextUsageFromRuntime_PrefersHeldTokensAndCarriesCache(t *testing.T) {
	t.Parallel()

	got := zcodeContextUsageFromRuntime(&zcodeContextUsage{
		Used: 5000,
		Size: 200000,
		Cache: &struct {
			InputTokens      int64 `json:"inputTokens"`
			CacheReadTokens  int64 `json:"cacheReadTokens"`
			CacheWriteTokens int64 `json:"cacheWriteTokens"`
		}{InputTokens: 80, CacheReadTokens: 20, CacheWriteTokens: 5},
	})
	assert.Equal(t, int64(5000), got["context_tokens"],
		"context_tokens is what the context HOLDS, not the last request")
	assert.Equal(t, int64(200000), got["context_window"])
	assert.Equal(t, int64(80), got["input_tokens"])
	assert.Equal(t, int64(20), got["cache_read_input_tokens"])
	assert.Equal(t, int64(5), got["cache_creation_input_tokens"])
}

func TestZCodeContextUsage_CostOrNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, (*zcodeContextUsage)(nil).costOrNil())
	assert.Nil(t, (&zcodeContextUsage{}).costOrNil())

	usage := &zcodeContextUsage{Cost: &zcodeCost{Amount: 1.25, Currency: "USD"}}
	cost := usage.costOrNil()
	require.NotNil(t, cost)
	assert.Equal(t, 1.25, cost.Amount)
	assert.Equal(t, zcodeUsageCurrencyUSD, cost.Currency)
}

func TestApplyZCodeRuntimeState_NilIsANoop(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.applyZCodeRuntimeState(nil)
	assert.Equal(t, 0, sink.SessionInfoCount())
}

// A cost in another currency must not fill total_cost_usd: that field names
// dollars, and a converted or relabelled amount would be a wrong number.
func TestApplyZCodeRuntimeState_NonUSDCostIsDropped(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.applyZCodeRuntimeState(&zcodeRuntimeState{
		ContextUsage: &zcodeContextUsage{
			Used: 10,
			Cost: &zcodeCost{Amount: 1.2, Currency: "EUR"},
		},
	})

	info := sink.LastSessionInfo()
	require.NotNil(t, info)
	_, hasCost := info["total_cost_usd"]
	assert.False(t, hasCost)
	a.mu.Lock()
	known := a.sessionCostKnown
	a.mu.Unlock()
	assert.False(t, known)
}

// A genuine zero (a free plan) is a reported cost, not an absence.
func TestApplyZCodeRuntimeState_ZeroUSDIsReported(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.applyZCodeRuntimeState(&zcodeRuntimeState{
		ContextUsage: &zcodeContextUsage{
			Used: 10,
			Cost: &zcodeCost{Amount: 0, Currency: zcodeUsageCurrencyUSD},
		},
	})

	info := sink.LastSessionInfo()
	require.NotNil(t, info)
	assert.Equal(t, float64(0), info["total_cost_usd"])
	a.mu.Lock()
	known, amount := a.sessionCostKnown, a.sessionCostUsd
	a.mu.Unlock()
	assert.True(t, known)
	assert.Equal(t, float64(0), amount)
}

// An absent apiRetry means the retry finished. A missing key would leave the
// last attempt stuck on the session info.
func TestApplyZCodeRuntimeState_AbsentAPIRetryClearsTheIndicator(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.applyZCodeRuntimeState(&zcodeRuntimeState{
		APIRetry: &zcodeAPIRetry{Attempt: 2, MaxRetries: 5, RetryDelayMs: 400, Error: "429"},
	})
	retry, ok := sink.LastSessionInfo()["zcode_api_retry"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 2, retry["attempt"])

	a.applyZCodeRuntimeState(&zcodeRuntimeState{})
	cleared, present := sink.LastSessionInfo()["zcode_api_retry"]
	require.True(t, present)
	assert.Nil(t, cleared)
}

func TestRecordZCodeUsage_DoesNotReplaceAuthoritativeContextTokens(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.mu.Lock()
	a.latestContextUsage = map[string]any{
		"context_tokens":              int64(5000),
		"input_tokens":                int64(1),
		"output_tokens":               int64(1),
		"cache_read_input_tokens":     int64(0),
		"cache_creation_input_tokens": int64(0),
	}
	a.mu.Unlock()

	a.recordZCodeUsage(zcodeUsage{InputTokens: 80, OutputTokens: 20, CacheReadTokens: 4, CacheWriteTokens: 2})

	a.mu.Lock()
	got := a.latestContextUsage
	a.mu.Unlock()
	assert.Equal(t, int64(5000), got["context_tokens"],
		"the per-request counts must not make the fill gauge jump backwards after a compaction")
	assert.Equal(t, int64(80), got["input_tokens"])
	assert.Equal(t, int64(20), got["output_tokens"])
	assert.Equal(t, int64(4), got["cache_read_input_tokens"])
	assert.Equal(t, int64(2), got["cache_creation_input_tokens"])
}

func TestRecordZCodeUsage_FillsWhenNoAuthoritativeReadingExists(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.recordZCodeUsage(zcodeUsage{InputTokens: 80, OutputTokens: 20})

	info := sink.LastSessionInfo()
	require.NotNil(t, info)
	usage, ok := info["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(80), usage["input_tokens"])
	_, hasHeld := usage["context_tokens"]
	assert.False(t, hasHeld, "per-request counts do not know what the context holds")
}

func TestRecordZCodeUsage_EmptyIsANoop(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.recordZCodeUsage(zcodeUsage{})
	assert.Equal(t, 0, sink.SessionInfoCount())
}

func TestZCodeAugmentWithUsage_EmptySnapshotReturnsRaw(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":"turn.completed"}`)
	assert.Equal(t, raw, zcodeAugmentWithUsage(raw, zcodeUsageSnapshot{}))
}

func TestZCodeAugmentWithUsage_InjectsCostAndContext(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":"turn.completed","payload":{}}`)
	out := zcodeAugmentWithUsage(raw, zcodeUsageSnapshot{
		ContextUsage: map[string]any{"context_tokens": int64(900)},
		CostUSD:      0.42,
		HasCost:      true,
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "turn.completed", got["type"])
	assert.InDelta(t, 0.42, got["total_cost_usd"], 1e-9)
	usage, ok := got["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(900), usage["context_tokens"])
}

func TestZCodeAugmentWithUsage_MalformedJSONReturnsRawUnchanged(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":`)
	assert.Equal(t, raw, zcodeAugmentWithUsage(raw, zcodeUsageSnapshot{
		HasCost: true, CostUSD: 1,
	}))
}
