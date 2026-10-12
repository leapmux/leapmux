package muse

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseContextAndTokenUsageKeepTheirIndependentNativeFacts(t *testing.T) {
	a, sink := testAgent(t)
	feed(t, a, contracts.MuseMethodContextUsage, map[string]any{"sessionId": "session", "usedTokens": 900, "windowTokens": 1000, "pressure": "low"})
	feed(t, a, contracts.MuseMethodTokenUsage, map[string]any{
		"sessionId": "session", "turnId": "turn", "promptTokens": 155, "totalTokens": 175,
		"usage":      map[string]any{"inputTokens": 100, "outputTokens": 20, "cachedTokens": 25, "reasoningTokens": 7, "cacheReadTokens": 25, "cacheWriteTokens": 30},
		"cumulative": map[string]any{"promptTokens": 10000, "outputTokens": 1000, "totalTokens": 11000},
	})
	usage, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, exists)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens: int64(100), contracts.ContextUsageFieldOutputTokens: int64(20),
		contracts.ContextUsageFieldCacheReadInputTokens: int64(25), contracts.ContextUsageFieldCacheCreationInputTokens: int64(30),
		contracts.ContextUsageFieldContextTokens: int64(900), contracts.ContextUsageFieldContextWindow: int64(1000),
	}, usage)
	feed(t, a, contracts.MuseMethodContextUsage, map[string]any{"sessionId": "session", "usedTokens": 700, "pressure": "low"})
	usage, _ = sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	values, ok := usage.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(100), values[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(700), values[contracts.ContextUsageFieldContextTokens])
	assert.NotContains(t, values, contracts.ContextUsageFieldContextWindow)
}

func TestMuseUsageRetainsZeroAndAbsentOptionalCounters(t *testing.T) {
	a, sink := testAgent(t)
	feed(t, a, contracts.MuseMethodContextUsage, map[string]any{"sessionId": "session", "usedTokens": 0, "pressure": "low"})
	usage, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, exists)
	assert.Equal(t, map[string]any{contracts.ContextUsageFieldContextTokens: int64(0)}, usage)
	feed(t, a, contracts.MuseMethodTokenUsage, map[string]any{
		"sessionId": "session", "turnId": "turn", "promptTokens": 0, "totalTokens": 0,
		"usage":      map[string]any{"inputTokens": 0, "outputTokens": 0, "cachedTokens": 0, "reasoningTokens": 0},
		"cumulative": map[string]any{"promptTokens": 0, "outputTokens": 0, "totalTokens": 0},
	})
	usage, _ = sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	values, ok := usage.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(0), values[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(0), values[contracts.ContextUsageFieldOutputTokens])
	assert.NotContains(t, values, contracts.ContextUsageFieldCacheReadInputTokens)
	assert.NotContains(t, values, contracts.ContextUsageFieldCacheCreationInputTokens)
}

func TestMuseUsageRejectsMalformedCountersWithoutReplacingAValidReading(t *testing.T) {
	for _, raw := range []string{
		`{"sessionId":"session","usedTokens":-1}`,
		`{"sessionId":"session","usedTokens":1,"windowTokens":-1}`,
		`{"sessionId":"session","usedTokens":1.5}`,
		`{"sessionId":"session","usedTokens":null}`,
		`{"sessionId":"session","windowTokens":100}`,
		`{"sessionId":"session","usedTokens":9223372036854775808}`,
	} {
		t.Run(raw, func(t *testing.T) {
			a, sink := testAgent(t)
			feed(t, a, contracts.MuseMethodContextUsage, map[string]any{"sessionId": "session", "usedTokens": 0, "windowTokens": 100})
			previous := sink.LastSessionInfo()
			feed(t, a, contracts.MuseMethodContextUsage, json.RawMessage(raw))
			assert.Equal(t, 1, sink.SessionInfoCount())
			assert.Equal(t, previous, sink.LastSessionInfo())
		})
	}
}

func TestMuseUsageKeepsLargeNativeCountersAndIsolatesForeignSessions(t *testing.T) {
	a, sink := testAgent(t)
	const count = int64(9007199254740991)
	feed(t, a, contracts.MuseMethodContextUsage, map[string]any{"sessionId": "session", "usedTokens": count})
	usage, _ := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	assert.Equal(t, map[string]any{contracts.ContextUsageFieldContextTokens: count}, usage)
	feed(t, a, contracts.MuseMethodContextUsage, map[string]any{"sessionId": "foreign", "usedTokens": 0})
	assert.Equal(t, 1, sink.SessionInfoCount())
}

func TestMuseTokenUsageRejectsAnInvalidObservationAtomically(t *testing.T) {
	for _, usage := range []string{
		`{"inputTokens":-1,"outputTokens":1,"cachedTokens":0,"reasoningTokens":0}`,
		`{"inputTokens":1,"outputTokens":null,"cachedTokens":0,"reasoningTokens":0}`,
		`{"inputTokens":1,"outputTokens":1,"cachedTokens":0,"reasoningTokens":0,"cacheReadTokens":-1}`,
		`{"inputTokens":1,"outputTokens":1,"cachedTokens":0,"reasoningTokens":0,"cacheWriteTokens":1.5}`,
		`{"inputTokens":1,"outputTokens":1,"cachedTokens":0,"reasoningTokens":0,"cacheWriteTokens":9223372036854775808}`,
		`{}`,
	} {
		t.Run(usage, func(t *testing.T) {
			a, sink := testAgent(t)
			feed(t, a, contracts.MuseMethodContextUsage, map[string]any{"sessionId": "session", "usedTokens": 17, "windowTokens": 100})
			before := sink.LastSessionInfo()
			raw := `{"sessionId":"session","turnId":"turn","promptTokens":1,"totalTokens":2,"usage":` + usage + `,"cumulative":{"promptTokens":1,"outputTokens":1,"totalTokens":2}}`
			feed(t, a, contracts.MuseMethodTokenUsage, json.RawMessage(raw))
			assert.Equal(t, 1, sink.SessionInfoCount())
			assert.Equal(t, before, sink.LastSessionInfo())
		})
	}
}

func TestMuseSubscriptionUsageNormalizesAnAtomicObservation(t *testing.T) {
	a, sink := testAgent(t)
	feed(t, a, contracts.MuseMethodUsageChanged, map[string]any{
		"observedAtMs": int64(1791388800000), "tier": "native-tier",
		"weekly": map[string]any{"usedPercent": 125, "resetsAtMs": int64(1791993600000)},
		"window": map[string]any{"usedPercent": 0, "resetsAtMs": int64(1791406800000), "windowDurationMins": 300},
	})
	value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
	require.True(t, exists)
	assert.Equal(t, map[string]any{
		contracts.RateLimitUpdateFieldMode: contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: map[string]any{
			"five_hour": map[string]any{contracts.RateLimitFieldRateLimitType: "five_hour", contracts.RateLimitFieldUtilization: float64(0), contracts.RateLimitFieldResetsAt: float64(1791406800)},
			"seven_day": map[string]any{contracts.RateLimitFieldRateLimitType: "seven_day", contracts.RateLimitFieldUtilization: float64(1.25), contracts.RateLimitFieldResetsAt: float64(1791993600)},
		},
	}, value)
}

func TestMuseSubscriptionUsageRejectsInvalidAndAbsentNativeWindows(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"observedAtMs":0,"tier":"native","weekly":null,"window":null}`,
		`{"observedAtMs":-1,"tier":"native","weekly":{"usedPercent":0,"resetsAtMs":1},"window":{"usedPercent":0,"resetsAtMs":1,"windowDurationMins":300}}`,
		`{"observedAtMs":1,"tier":"native","weekly":{"usedPercent":-1,"resetsAtMs":1},"window":{"usedPercent":0,"resetsAtMs":1,"windowDurationMins":300}}`,
		`{"observedAtMs":1,"tier":"native","weekly":{"usedPercent":0,"resetsAtMs":1},"window":{"usedPercent":0,"resetsAtMs":1,"windowDurationMins":0}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			a, sink := testAgent(t)
			feed(t, a, contracts.MuseMethodUsageChanged, json.RawMessage(raw))
			assert.Zero(t, sink.SessionInfoCount())
		})
	}
}

func TestMuseSubscriptionUsageParsesTheLiveAccountFrame(t *testing.T) {
	t.Parallel()
	// The exact frame the installed host emitted against the signed-in Meta
	// account on 2026-10-10 (usage/changed during one real turn): raw bytes,
	// so the parse also proves the field spelling the live account sends.
	const live = `{"window":{"usedPercent":0,"windowDurationMins":300,"resetsAtMs":1791611340000},"weekly":{"usedPercent":0,"resetsAtMs":1791763200000},"tier":"27681393394859588","observedAtMs":1791593354940}`
	a, sink := testAgent(t)
	feed(t, a, contracts.MuseMethodUsageChanged, json.RawMessage(live))
	value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyRateLimits)
	require.True(t, exists, "the live account frame is a valid observation")
	assert.Equal(t, map[string]any{
		contracts.RateLimitUpdateFieldMode: contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: map[string]any{
			"five_hour": map[string]any{contracts.RateLimitFieldRateLimitType: "five_hour", contracts.RateLimitFieldUtilization: float64(0), contracts.RateLimitFieldResetsAt: float64(1791611340)},
			"seven_day": map[string]any{contracts.RateLimitFieldRateLimitType: "seven_day", contracts.RateLimitFieldUtilization: float64(0), contracts.RateLimitFieldResetsAt: float64(1791763200)},
		},
	}, value)
}
