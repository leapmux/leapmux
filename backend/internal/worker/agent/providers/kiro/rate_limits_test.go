package kiro

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiroRateLimitValuesPreserveNativePrecisionAndCalendarDate(t *testing.T) {
	t.Parallel()
	var usage kiroUsageData
	require.NoError(t, json.Unmarshal([]byte(`{"planName":"Native Probe Plan","billingCycleReset":"2026-11-25","usageBreakdowns":[{"resourceType":"CREDIT","displayName":"Credits","used":25.5,"limit":100,"percentage":25,"hasLimit":true},{"resourceType":"REQUEST","displayName":"Requests","used":0,"limit":10,"hasLimit":true}]}`), &usage))
	assert.Equal(t, map[string]any{
		"CREDIT":  map[string]any{"rate_limit_type": "CREDIT", "label": "Native Probe Plan — Credits", "status": "allowed", "utilization": 0.255, "resets_on": "2026-11-25"},
		"REQUEST": map[string]any{"rate_limit_type": "REQUEST", "label": "Native Probe Plan — Requests", "status": "allowed", "utilization": float64(0), "resets_on": "2026-11-25"},
	}, kiroRateLimitValues(usage))
}

func TestKiroRateLimitValuesHandleInvalidAndUnlimitedResources(t *testing.T) {
	t.Parallel()
	for _, resource := range []string{
		`{"resourceType":"CREDIT","used":1,"limit":0,"hasLimit":true}`,
		`{"resourceType":"CREDIT","used":1,"limit":-1,"hasLimit":true}`,
		`{"resourceType":"CREDIT","used":-1,"limit":100,"hasLimit":true}`,
		`{"resourceType":"CREDIT","limit":100,"hasLimit":true}`,
		`{"resourceType":"CREDIT","used":1,"hasLimit":true}`,
		`{"resourceType":"CREDIT","used":1,"limit":100,"hasLimit":false}`,
		`{"resourceType":" ","used":1,"limit":100,"hasLimit":true}`,
	} {
		var usage kiroUsageData
		require.NoError(t, json.Unmarshal([]byte(`{"usageBreakdowns":[`+resource+`]}`), &usage))
		assert.Empty(t, kiroRateLimitValues(usage), resource)
	}
	var duplicate kiroUsageData
	require.NoError(t, json.Unmarshal([]byte(`{"usageBreakdowns":[{"resourceType":"CREDIT","used":1,"limit":10,"hasLimit":true},{"resourceType":" CREDIT ","used":2,"limit":10,"hasLimit":true}]}`), &duplicate))
	assert.Empty(t, kiroRateLimitValues(duplicate), "a repeated resource type cannot overwrite another native reading")
	assert.Empty(t, kiroRateLimitValues(kiroUsageData{}))
}

func TestKiroRateLimitValuesKeepOverageAndRejectInvalidDates(t *testing.T) {
	t.Parallel()
	for _, date := range []string{"", "2026-02-29", "2026-04-31", "0000-01-01", "2026-11-25T00:00:00Z", " 2026-11-25"} {
		var usage kiroUsageData
		require.NoError(t, json.Unmarshal([]byte(`{"billingCycleReset":`+strconv.Quote(date)+`,"usageBreakdowns":[{"resourceType":"CREDIT","used":120,"limit":100,"hasLimit":true}]}`), &usage))
		assert.Equal(t, map[string]any{"CREDIT": map[string]any{"rate_limit_type": "CREDIT", "label": "CREDIT", "status": "exceeded", "utilization": 1.2}}, kiroRateLimitValues(usage))
	}
	var leapDay kiroUsageData
	require.NoError(t, json.Unmarshal([]byte(`{"billingCycleReset":"2024-02-29","usageBreakdowns":[{"resourceType":"CREDIT","used":85,"limit":100,"hasLimit":true}]}`), &leapDay))
	assert.Equal(t, map[string]any{"CREDIT": map[string]any{"rate_limit_type": "CREDIT", "label": "CREDIT", "status": "allowed_warning", "utilization": 0.85, "resets_on": "2024-02-29"}}, kiroRateLimitValues(leapDay))
}

func TestKiroRateLimitValuesRefuseNonFiniteRatios(t *testing.T) {
	t.Parallel()
	for _, numbers := range [][2]float64{{math.NaN(), 1}, {math.Inf(1), 1}, {1, math.NaN()}, {1, math.Inf(1)}, {math.MaxFloat64, math.SmallestNonzeroFloat64}} {
		var usage kiroUsageData
		require.NoError(t, json.Unmarshal([]byte(`{"usageBreakdowns":[{"resourceType":"CREDIT","used":1,"limit":1,"hasLimit":true}]}`), &usage))
		usage.UsageBreakdowns[0].Used = &numbers[0]
		usage.UsageBreakdowns[0].Limit = &numbers[1]
		assert.Empty(t, kiroRateLimitValues(usage))
	}
}

func TestDecodeKiroUsagePreservesTheDifferenceBetweenMissingAndEmpty(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{`, `null`, `{}`, `{"success":false,"message":"Unavailable"}`, `{"success":true}`, `{"success":true,"data":{}}`, `{"success":true,"data":{"usageBreakdowns":null}}`, `{"success":true,"data":{"usageBreakdowns":"invalid"}}`, `{"success":true,"data":{"usageBreakdowns":[{}]}}`, `{"success":true,"data":{"usageBreakdowns":[{"resourceType":"CREDIT","used":1,"limit":10}]}}`, `{"success":true,"data":{"usageBreakdowns":[{"resourceType":"CREDIT","used":-1,"limit":10,"hasLimit":true}]}}`} {
		values, err := decodeKiroUsage(json.RawMessage(raw))
		assert.Error(t, err, raw)
		assert.Nil(t, values)
	}
	values, err := decodeKiroUsage(json.RawMessage(`{"success":true,"data":{"usageBreakdowns":[]}}`))
	require.NoError(t, err)
	assert.NotNil(t, values)
	assert.Empty(t, values)
	values, err = decodeKiroUsage(json.RawMessage(`{"success":true,"data":{"usageBreakdowns":[{"resourceType":"CREDIT","hasLimit":false}]}}`))
	require.NoError(t, err)
	assert.Empty(t, values, "a native unlimited resource does not create a recurring quota")
}
