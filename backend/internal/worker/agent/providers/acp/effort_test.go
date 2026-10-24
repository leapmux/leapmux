package acp

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffortSelectionKeepsBooleanThinkingIndependent(t *testing.T) {
	t.Parallel()
	thinking := ConfigOption{ID: "thinking", Category: acpConfigOptionCategoryThoughtLevel, CurrentValue: "false", Options: []ConfigOptionValue{{Value: "false"}, {Value: "true"}}}
	effort := ConfigOption{ID: agent.OptionIDEffort, Category: acpConfigOptionCategoryThoughtLevel, CurrentValue: "low", Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}}
	assert.False(t, isEffortConfigOption(thinking, ""))
	selected, ok := acpEffortConfigOption([]ConfigOption{thinking, effort}, "")
	require.True(t, ok)
	assert.Equal(t, effort.ID, selected.ID)
	g := &optionState{templates: map[string]ConfigOption{thinking.ID: thinking, effort.ID: effort}}
	assert.Empty(t, g.thoughtLevelConfigOptionID(), "canonical effort does not map into the boolean switch")
	delete(g.templates, effort.ID)
	assert.Empty(t, g.thoughtLevelConfigOptionID(), "a boolean switch cannot receive an effort override")
}

func TestEffortSelectionPrefersDeclaredAndCanonicalAxes(t *testing.T) {
	t.Parallel()
	other := ConfigOption{ID: "other", Category: acpConfigOptionCategoryThoughtLevel, Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}}
	canonical := other
	canonical.ID = agent.OptionIDEffort
	declared := other
	declared.ID = "declared"
	options := []ConfigOption{other, canonical, declared}
	selected, ok := acpEffortConfigOption(options, declared.ID)
	require.True(t, ok)
	assert.Equal(t, declared.ID, selected.ID)
	selected, ok = acpEffortConfigOption(options, "")
	require.True(t, ok)
	assert.Equal(t, canonical.ID, selected.ID)
}

func TestBooleanConfigOption(t *testing.T) {
	t.Parallel()
	assert.True(t, isBooleanConfigOption(ConfigOption{Type: "boolean"}))
	assert.False(t, isEffortConfigOption(ConfigOption{Type: "boolean", Category: acpConfigOptionCategoryThoughtLevel}, ""))
	for _, tc := range []struct {
		name   string
		values []string
		want   bool
	}{
		{"absent options", nil, false},
		{"false only", []string{"false"}, true},
		{"true only", []string{"true"}, true},
		{"both boolean values", []string{"false", "true"}, true},
		{"duplicate boolean values", []string{"false", "false"}, true},
		{"ranked enable level", []string{"off", "enabled"}, false},
		{"mixed levels", []string{"false", "high"}, false},
		{"empty value", []string{""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var options []ConfigOptionValue
			for _, value := range tc.values {
				options = append(options, ConfigOptionValue{Value: value})
			}
			assert.Equal(t, tc.want, isBooleanConfigOption(ConfigOption{Options: options}))
		})
	}
}

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
