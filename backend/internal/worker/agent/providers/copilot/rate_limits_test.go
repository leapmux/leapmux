package copilot

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func copilotQuotaEvent(t *testing.T, agentID string, snapshots any) []byte {
	t.Helper()
	event := map[string]any{
		"id": "native-quota", "type": contracts.CopilotEventAssistantUsage, "ephemeral": true,
		"data": map[string]any{"model": "gpt-5.6-luna", "quotaSnapshots": snapshots},
	}
	if agentID != "" {
		event["agentId"] = agentID
	}
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session.event",
		"params": map[string]any{"sessionId": "session-1", "event": event},
	})
	require.NoError(t, err)
	return raw
}

func copilotQuotaValues(t *testing.T, sink *agenttest.Sink) map[string]interface{} {
	t.Helper()
	update, ok := sink.LastSessionInfo()[contracts.SessionInfoKeyRateLimits].(map[string]interface{})
	require.True(t, ok, "the real ephemeral usage event must publish quota state")
	assert.Equal(t, contracts.RateLimitUpdateModeMerge, update[contracts.RateLimitUpdateFieldMode])
	values, ok := update[contracts.RateLimitUpdateFieldValues].(map[string]interface{})
	require.True(t, ok)
	return values
}

func TestCopilotEphemeralUsagePublishesNativeQuota(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)
	a.HandleOutput(copilotQuotaEvent(t, "", map[string]any{"premium_interactions": map[string]any{
		"isUnlimitedEntitlement": false, "entitlementRequests": 300, "usedRequests": 219,
		"remainingPercentage": 27, "overage": 0, "usageAllowedWithExhaustedQuota": false,
		"overageAllowedWithExhaustedQuota": false, "resetDate": "2026-10-01T00:00:00Z",
	}}))
	assert.Equal(t, map[string]interface{}{
		"premium_interactions": map[string]interface{}{
			contracts.RateLimitFieldRateLimitType:  "premium_interactions",
			contracts.RateLimitFieldStatus:         "allowed",
			contracts.RateLimitFieldUtilization:    0.73,
			contracts.RateLimitFieldResetsAt:       int64(1790812800),
			contracts.RateLimitFieldIsUsingOverage: false,
		},
	}, copilotQuotaValues(t, sink))
	assert.Empty(t, sink.Messages(), "an ephemeral quota snapshot must not create a transcript row")
}

func TestCopilotQuotaStatusPreservesZeroAndNativeUsagePermission(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		remaining    float64
		usageAllowed bool
		overage      float64
		status       string
	}{
		{name: "unused quota", remaining: 100, status: "allowed"},
		{name: "warning threshold", remaining: 20, status: "allowed_warning"},
		{name: "exhausted quota", remaining: 0, status: "exceeded"},
		{name: "paid overage", remaining: 0, usageAllowed: true, overage: 2, status: "allowed_warning"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newNativeCopilotForEvents(t)
			a.HandleOutput(copilotQuotaEvent(t, "", map[string]any{"chat": map[string]any{
				"remainingPercentage": test.remaining, "usageAllowedWithExhaustedQuota": test.usageAllowed,
				"overageAllowedWithExhaustedQuota": test.usageAllowed, "overage": test.overage,
				"resetDate": "1970-01-01T00:00:00Z",
			}}))
			info, ok := copilotQuotaValues(t, sink)["chat"].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, test.status, info[contracts.RateLimitFieldStatus])
			assert.InDelta(t, (100-test.remaining)/100, info[contracts.RateLimitFieldUtilization], 0.000001)
			assert.Equal(t, int64(0), info[contracts.RateLimitFieldResetsAt])
			assert.Equal(t, test.overage > 0, info[contracts.RateLimitFieldIsUsingOverage])
			if test.overage > 0 {
				assert.Equal(t, int64(0), info[contracts.RateLimitFieldOverageResetsAt])
				assert.Equal(t, "allowed", info[contracts.RateLimitFieldOverageStatus])
			}
		})
	}
}

func TestCopilotQuotaUnlimitedResourceClearsFiniteUsageFields(t *testing.T) {
	t.Parallel()
	info := copilotQuotaInfo("chat", json.RawMessage(`{"isUnlimitedEntitlement":true,"remainingPercentage":0,"resetDate":"2026-10-01T00:00:00Z"}`))
	assert.Equal(t, map[string]interface{}{
		contracts.RateLimitFieldRateLimitType: "chat",
		contracts.RateLimitFieldStatus:        "allowed",
	}, info)
}

func TestCopilotQuotaMissingOptionalFieldsStayAbsent(t *testing.T) {
	t.Parallel()
	info := copilotQuotaInfo("chat", json.RawMessage(`{"remainingPercentage":50}`))
	require.NotNil(t, info)
	assert.Equal(t, 0.5, info[contracts.RateLimitFieldUtilization])
	assert.NotContains(t, info, contracts.RateLimitFieldIsUsingOverage)
	assert.NotContains(t, info, contracts.RateLimitFieldOverageStatus)
	assert.NotContains(t, info, contracts.RateLimitFieldResetsAt)
	assert.NotContains(t, info, contracts.RateLimitFieldOverageResetsAt)
}

func TestCopilotQuotaNativeDenialOverridesRemainingPercentage(t *testing.T) {
	t.Parallel()
	info := copilotQuotaInfo("chat", json.RawMessage(`{"remainingPercentage":50,"hasQuota":false}`))
	require.NotNil(t, info)
	assert.Equal(t, "exceeded", info[contracts.RateLimitFieldStatus])
	assert.Equal(t, 0.5, info[contracts.RateLimitFieldUtilization])
}

func TestCopilotQuotaRejectsInvalidNativeCounters(t *testing.T) {
	t.Parallel()
	for _, raw := range []json.RawMessage{
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`{"remainingPercentage":-1}`),
		json.RawMessage(`{"remainingPercentage":101}`),
		json.RawMessage(`{"remainingPercentage":1e999}`),
		json.RawMessage(`{"remainingPercentage":50,"entitlementRequests":-1}`),
		json.RawMessage(`{"remainingPercentage":50,"usedRequests":-1}`),
		json.RawMessage(`{"remainingPercentage":50,"overage":-1}`),
	} {
		assert.Nil(t, copilotQuotaInfo("chat", raw), string(raw))
	}
	assert.Nil(t, copilotQuotaInfo("", json.RawMessage(`{"remainingPercentage":50}`)))
}

func TestCopilotQuotaPreservesLargeCountsAndFractionalPercentages(t *testing.T) {
	t.Parallel()
	info := copilotQuotaInfo("chat", json.RawMessage(`{"remainingPercentage":27.5,"entitlementRequests":1e300,"usedRequests":1e299}`))
	require.NotNil(t, info)
	assert.Equal(t, 0.725, info[contracts.RateLimitFieldUtilization])
	assert.Equal(t, "allowed", info[contracts.RateLimitFieldStatus])
}

func TestCopilotSubagentQuotaUsesItsOwnSessionSurface(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "task-quota", "toolName": contracts.CopilotToolTask,
		"arguments": map[string]any{"name": "Reviewer", "prompt": "Review the diff."},
	}))
	a.HandleOutput(nativeCopilotEvent(t, "quota-child", contracts.CopilotEventSubagentStarted, map[string]any{
		"toolCallId": "task-quota", "agentName": "reviewer", "agentDisplayName": "Reviewer",
	}))
	row, ok := copilotBackgroundRow(t, sink, "quota-child")
	require.True(t, ok)
	child := sink.Child(row.ChildAgentID)
	a.HandleOutput(copilotQuotaEvent(t, "quota-child", map[string]any{"chat": map[string]any{"remainingPercentage": 50}}))
	assert.Zero(t, sink.SessionInfoCount(), "a child quota event must not overwrite the root state")
	assert.Contains(t, copilotQuotaValues(t, child), "chat")
}

func TestCopilotQuotaIgnoresMalformedTiersWithoutDiscardingValidTiers(t *testing.T) {
	t.Parallel()
	a, sink := newNativeCopilotForEvents(t)
	a.HandleOutput(copilotQuotaEvent(t, "", map[string]any{
		"chat":       map[string]any{"remainingPercentage": 50, "resetDate": "invalid"},
		"negative":   map[string]any{"remainingPercentage": -1},
		"too_large":  map[string]any{"remainingPercentage": 101},
		"missing":    map[string]any{},
		"wrong_type": map[string]any{"remainingPercentage": "50"},
	}))
	values := copilotQuotaValues(t, sink)
	require.Len(t, values, 1)
	info, ok := values["chat"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 0.5, info[contracts.RateLimitFieldUtilization])
	assert.NotContains(t, info, contracts.RateLimitFieldResetsAt)
}

func TestCopilotQuotaWithoutAnyNativeSnapshotPublishesNothing(t *testing.T) {
	t.Parallel()
	for _, snapshots := range []any{nil, map[string]any{}, []any{}, "invalid"} {
		a, sink := newNativeCopilotForEvents(t)
		a.HandleOutput(copilotQuotaEvent(t, "", snapshots))
		assert.Zero(t, sink.SessionInfoCount())
		assert.Empty(t, sink.Messages())
	}
}
