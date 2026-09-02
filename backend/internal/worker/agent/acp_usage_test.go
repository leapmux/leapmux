package agent

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acpContextUsage returns the context_usage payload of the last broadcast.
func acpContextUsage(t *testing.T, sink *testSink) map[string]any {
	t.Helper()
	v, ok := lastSessionInfoValue(sink, contracts.SessionInfoKeyContextUsage)
	require.True(t, ok, "expected a context_usage broadcast")
	usage, ok := v.(map[string]any)
	require.True(t, ok, "context_usage must be an object, got %T", v)
	return usage
}

// The ACP projection of `context_usage`. It is the ONE provider whose shape had
// no test, and the one whose shape reads least like the others: the app-server
// reports a single used-token total and no breakdown, so the total lands on
// `input_tokens` and the other three counts are present and zero.
//
// Present and zero, not absent, is the load-bearing half. The popover renders a
// row per count, so an absent key blanks a row where a zero shows "0".
func TestACP_UsageUpdateProjectsTheSharedContextUsageShape(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	b := &acpBase{sink: sink}
	b.handleUsageUpdate(json.RawMessage(`{"used":1234,"size":200000}`))

	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens:              int64(1234),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(0),
		contracts.ContextUsageFieldCacheReadInputTokens:     int64(0),
		contracts.ContextUsageFieldOutputTokens:             int64(0),
		contracts.ContextUsageFieldContextWindow:            int64(200000),
	}, acpContextUsage(t, sink))
}

// ACP reports the window unconditionally, unlike every sibling provider, which
// omits it when it is not known. A zero therefore still ships -- the grid reads
// a 0 window as "unknown" and hides the gauge, and an omitted key would let a
// stale window from an earlier broadcast stand instead.
func TestACP_UsageUpdateShipsAZeroContextWindow(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	b := &acpBase{sink: sink}
	b.handleUsageUpdate(json.RawMessage(`{"used":10}`))

	usage := acpContextUsage(t, sink)
	require.Contains(t, usage, contracts.ContextUsageFieldContextWindow)
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldContextWindow])
}

func TestACP_UsageUpdateReportsACostOnlyWhenPositive(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		line     string
		wantCost bool
	}{
		"a positive cost ships": {`{"used":10,"size":100,"cost":{"amount":0.25,"currency":"USD"}}`, true},
		"a zero cost is absent": {`{"used":10,"size":100,"cost":{"amount":0,"currency":"USD"}}`, false},
		"no cost block at all":  {`{"used":10,"size":100}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			b := &acpBase{sink: sink}
			b.handleUsageUpdate(json.RawMessage(tc.line))

			cost, ok := lastSessionInfoValue(sink, contracts.SessionInfoKeyTotalCostUsd)
			assert.Equal(t, tc.wantCost, ok)
			if tc.wantCost {
				assert.Equal(t, 0.25, cost)
			}
		})
	}
}

// A malformed payload broadcasts NOTHING, rather than a usage map of zeros that
// would overwrite a real reading with an empty gauge.
func TestACP_UsageUpdateDropsAMalformedPayload(t *testing.T) {
	t.Parallel()

	for name, line := range map[string]string{
		"truncated":  `{"used":`,
		"an array":   `[1,2]`,
		"a string":   `"nope"`,
		"used typed": `{"used":"1234"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			b := &acpBase{sink: sink}
			b.handleUsageUpdate(json.RawMessage(line))

			_, ok := lastSessionInfoValue(sink, contracts.SessionInfoKeyContextUsage)
			assert.False(t, ok, "a payload this handler cannot read must broadcast nothing")
		})
	}
}
