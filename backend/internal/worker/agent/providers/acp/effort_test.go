package acp

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `enabled` is ZCode's on/off toggle for a model that offers no ladder, and its
// rank carries two obligations that are easy to break independently.
func TestEffortRank_RanksTheOnOffToggleAboveOff(t *testing.T) {
	t.Parallel()

	enabled, ok := providerkit.EffortRankOf("enabled")
	require.True(t, ok)
	assert.Positivef(t, enabled,
		"rank 0 means THINKING OFF: chooseDefaultEffort skips it and raiseEffortOffNone replaces it, "+
			"so a model already thinking would read as one that is not")
	assert.Less(t, providerkit.EffortRank["off"], enabled, "thinking on must not sort under thinking off")
	assert.Less(t, enabled, providerkit.EffortRank[agent.EffortHigh],
		"it must stay under a real level, so chooseDefaultEffort prefers one whenever a ladder offers it")

	// The pick a real ladder makes must not change because `enabled` joined the
	// table: it is only ever offered beside `off`.
	assert.Equal(t, agent.EffortHigh, chooseDefaultEffort(ConfigOption{Options: []ConfigOptionValue{
		{Value: "low"}, {Value: agent.EffortHigh}, {Value: "max"},
	}}))
	assert.Equal(t, "enabled", chooseDefaultEffort(ConfigOption{Options: []ConfigOptionValue{
		{Value: "off"}, {Value: "enabled"},
	}}), "a toggle axis stuck at off must be raised to on, not left there")
}
