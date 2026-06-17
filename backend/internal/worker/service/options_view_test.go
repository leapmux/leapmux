package service

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOverlayOptionGroupCurrents_SkipsOutOfListValue verifies the read model never
// forces a persisted current that isn't one of the group's options (which would
// render an invalid selection when a stale catalog built for another model is
// served); the catalog's own current is kept instead.
func TestOverlayOptionGroupCurrents_SkipsOutOfListValue(t *testing.T) {
	groups := []*leapmuxv1.AvailableOptionGroup{{
		Id:           agent.OptionIDEffort,
		CurrentValue: "high",
		Options:      []*leapmuxv1.AvailableOption{{Id: "high"}, {Id: "low"}},
	}}

	// "ultracode" is not an option for this (stale) effort group -> keep "high".
	out := overlayOptionGroupCurrents(groups, map[string]string{agent.OptionIDEffort: "ultracode"})
	assert.Equal(t, "high", out[0].GetCurrentValue())
	assert.Same(t, groups[0], out[0], "an unchanged group passes through by reference")

	// An in-list value overlays normally.
	out = overlayOptionGroupCurrents(groups, map[string]string{agent.OptionIDEffort: "low"})
	assert.Equal(t, "low", out[0].GetCurrentValue())

	// A group that enumerates no options accepts any value (free-form / not yet populated).
	free := []*leapmuxv1.AvailableOptionGroup{{Id: "x"}}
	out = overlayOptionGroupCurrents(free, map[string]string{"x": "anything"})
	assert.Equal(t, "anything", out[0].GetCurrentValue())
}

// TestResolveProviderDefaults_DoesNotMutateInput guards the clone-on-write contract:
// filling defaults must not mutate the caller's map (which may alias launch / DB
// options still in use elsewhere).
func TestResolveProviderDefaults_DoesNotMutateInput(t *testing.T) {
	input := map[string]string{}
	out := resolveProviderDefaults(input, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	assert.Empty(t, input, "the input map must not be mutated")
	assert.NotEmpty(t, out[agent.OptionIDModel], "the returned copy carries the model default")
	assert.NotEmpty(t, out[agent.OptionIDEffort], "the returned copy carries the effort default")
}

// TestResolveProviderDefaults_StampsEffortOnlyForNativeEffortProviders verifies the
// effort default is stamped only for providers that own a model-dependent effort
// catalog; an ACP provider (whose effort is a server-driven config option) gets the
// model default but no inert effort key.
func TestResolveProviderDefaults_StampsEffortOnlyForNativeEffortProviders(t *testing.T) {
	claude := resolveProviderDefaults(map[string]string{}, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	assert.NotEmpty(t, claude[agent.OptionIDEffort], "Claude owns an effort catalog -> default stamped")

	// Cursor is an ACP provider WITH a static model catalog but no effort tiers: the
	// model default is still stamped, the effort default is not.
	cursor := resolveProviderDefaults(map[string]string{}, leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	assert.Empty(t, cursor[agent.OptionIDEffort], "an ACP provider gets no stamped effort default")
	assert.NotEmpty(t, cursor[agent.OptionIDModel], "but the model default is still stamped")
}

// TestResolveProviderDefaults_HonorsEffortEnvOverrideForACPProvider verifies an
// explicit operator effort override (LEAPMUX_*_DEFAULT_EFFORT) IS stamped for an ACP
// provider that registers one (Kilo), so applyStartupOptions can re-push it to
// the server's "effort" config option. Only the blanket EffortAuto fallback is gated to
// catalog-effort providers -- so without the env var, Kilo gets no inert effort key.
func TestResolveProviderDefaults_HonorsEffortEnvOverrideForACPProvider(t *testing.T) {
	// Without the override, an ACP provider gets no effort key at all.
	kilo := resolveProviderDefaults(map[string]string{}, leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO)
	assert.Empty(t, kilo[agent.OptionIDEffort], "no override -> no stamped effort (server picks its own)")

	// With an explicit override, the chosen effort is stamped so it can be re-applied.
	t.Setenv("LEAPMUX_KILO_DEFAULT_EFFORT", "high")
	kiloOverride := resolveProviderDefaults(map[string]string{}, leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO)
	assert.Equal(t, "high", kiloOverride[agent.OptionIDEffort],
		"an explicit operator override is honored even for an ACP provider")
}

// TestOptionGroupsView_ServesPersistedCatalogWhileNotRunning verifies that, for an
// agent that is not running, the view serves the persisted last-live catalog (which
// carries provider groups the static registry reconstruction omits -- here Fast
// Mode) rather than the narrower static fallback.
func TestOptionGroupsView_ServesPersistedCatalogWhileNotRunning(t *testing.T) {
	m := agent.NewManager(nil)

	persisted := []*leapmuxv1.AvailableOptionGroup{
		{
			Id:           agent.OptionIDModel,
			Label:        "Model",
			Order:        10,
			CurrentValue: "claude-x",
			Options:      []*leapmuxv1.AvailableOption{{Id: "claude-x", Name: "Claude X"}},
		},
		{
			Id:           "fastMode",
			Label:        "Fast Mode",
			Order:        70,
			CurrentValue: "off",
			Mutable:      true,
			Options:      []*leapmuxv1.AvailableOption{{Id: "off", Name: "Off"}, {Id: "on", Name: "On"}},
		},
	}

	a := &db.Agent{
		ID:            "a1",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "claude-x"}),
		OptionGroups:  mustMarshalOptionGroups(t, persisted),
	}

	groups := optionGroupsView(m, a, nil)
	ids := map[string]bool{}
	for _, g := range groups {
		ids[g.GetId()] = true
	}
	require.NotEmpty(t, groups)
	assert.True(t, ids["fastMode"],
		"the persisted Fast Mode group (which the Claude static fallback omits) must be served while the agent is not running")
}
