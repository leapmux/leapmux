package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestQoderEffortsUseSharedLabels(t *testing.T) {
	t.Parallel()

	for _, tier := range qoderEffortLevels {
		assert.Equal(t, providerkit.EffortLabel(tier.Id), tier.Name, "effort %q must use the shared label", tier.Id)
	}
}

// The menu states every level the CLI admits and nothing else. Auto is not one
// of them: it is LeapMux's own sentinel for "send no level", so it sits ahead of
// the wire vocabulary rather than inside it.
func TestQoderEffortLevelsAreTheWireVocabulary(t *testing.T) {
	t.Parallel()

	wire := []string{
		contracts.QoderEffortLevelMax,
		contracts.QoderEffortLevelXhigh,
		contracts.QoderEffortLevelHigh,
		contracts.QoderEffortLevelMedium,
		contracts.QoderEffortLevelLow,
		contracts.QoderEffortLevelNone,
	}
	var offered []string
	for _, tier := range qoderEffortLevels {
		if tier.Id != agent.EffortAuto {
			offered = append(offered, tier.Id)
		}
	}
	assert.Equal(t, wire, offered, "the menu lists the levels strongest first")
	assert.Equal(t, agent.EffortAuto, qoderEffortLevels[0].Id)
	assert.True(t, qoderEffortLevels[0].Default, "Auto is the default a new session starts on")
}

// Auto and an unset effort send no flag, so the CLI keeps the model's own
// default. Every other level is one flag pair.
func TestQoderEffortArgs(t *testing.T) {
	t.Parallel()

	assert.Nil(t, qoderEffortArgs(""))
	assert.Nil(t, qoderEffortArgs(agent.EffortAuto))
	assert.Equal(t, []string{"--reasoning-effort", "high"}, qoderEffortArgs(contracts.QoderEffortLevelHigh))
	assert.Equal(t, []string{"--reasoning-effort", "none"}, qoderEffortArgs(contracts.QoderEffortLevelNone))
}

func TestQoderRegistrationOffersTheEffortAxis(t *testing.T) {
	t.Parallel()

	registration := Registration()
	assert.Contains(t, registration.AdditionalOptionIDs, agent.OptionIDEffort,
		"the settings allowlist must accept the effort axis")

	var ids []string
	for _, group := range registration.OptionGroups {
		if group.Id == agent.OptionIDEffort {
			for _, option := range group.Options {
				ids = append(ids, option.Id)
			}
		}
	}
	assert.Equal(t, []string{
		agent.EffortAuto,
		contracts.QoderEffortLevelMax,
		contracts.QoderEffortLevelXhigh,
		contracts.QoderEffortLevelHigh,
		contracts.QoderEffortLevelMedium,
		contracts.QoderEffortLevelLow,
		contracts.QoderEffortLevelNone,
	}, ids)
}

// An effort change takes the launch flag, so it settles only after a restart
// and the running agent keeps the level it was started with.
func TestQoderUpdateSettingsRestartsForEffort(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: contracts.QoderEffortLevelHigh})

	assert.True(t, result.AppliedLive, "nothing failed live: the axis is waiting for a restart")
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
	assert.Empty(t, result.ConfirmedOptions())
	assert.Equal(t, "", result.SurfacedOptions[agent.OptionIDEffort], "the running agent still has the launch level")
}

func TestQoderSettingsSnapshotCarriesTheEffort(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)

	a.effort = contracts.QoderEffortLevelXhigh
	snapshot := a.SettingsSnapshot()

	assert.Equal(t, contracts.QoderEffortLevelXhigh, snapshot.SurfacedOptions[agent.OptionIDEffort])
	assert.Len(t, a.OptionGroups(), 2)
}
