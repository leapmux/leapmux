package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestQoderResultBroadcastsNativeContextPercentage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"result","usage":{"context_usage_ratio":0.375,"input_tokens":0,"output_tokens":0}}`))
	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{contracts.ContextUsageFieldUsagePercent: 37.5}, value)

	a.HandleOutput([]byte(`{"type":"result","usage":{"context_usage_ratio":0}}`))
	value, ok = sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{contracts.ContextUsageFieldUsagePercent: float64(0)}, value)

	a.HandleOutput([]byte(`{"type":"result","usage":{"context_usage_ratio":1.5}}`))
	value, ok = sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{contracts.ContextUsageFieldUsagePercent: float64(100)}, value)
}

func TestQoderResultIgnoresMissingOrInvalidContextPercentage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	for _, frame := range []string{
		`{"type":"result"}`,
		`{"type":"result","usage":{}}`,
		`{"type":"result","usage":{"context_usage_ratio":null}}`,
		`{"type":"result","usage":{"context_usage_ratio":"half"}}`,
		`{"type":"result","usage":{"context_usage_ratio":-0.5}}`,
		`{"type":"result","parent_tool_use_id":"unknown-child","usage":{"context_usage_ratio":0.5}}`,
	} {
		a.HandleOutput([]byte(frame))
	}
	assert.Zero(t, sink.SessionInfoCount())
}
