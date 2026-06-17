package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// TestOptionMap_Merge pins the empty-deletes wire semantics and clone-on-write: an empty
// incoming value deletes the key, a non-empty one sets it, and the receiver is never mutated.
func TestOptionMap_Merge(t *testing.T) {
	base := OptionMap{agent.OptionIDModel: "opus", agent.OptionIDEffort: "high"}
	got := base.Merge(OptionMap{agent.OptionIDEffort: "", agent.OptionIDPermissionMode: "plan"})

	assert.Equal(t, "opus", got[agent.OptionIDModel], "an untouched key is preserved")
	assert.NotContains(t, got, agent.OptionIDEffort, "an empty incoming value deletes the key")
	assert.Equal(t, "plan", got[agent.OptionIDPermissionMode], "a non-empty incoming value sets the key")
	// The receiver is untouched (clone-on-write).
	assert.Equal(t, "high", base[agent.OptionIDEffort], "Merge must not mutate the receiver")
}

// TestOptionMap_Clone returns an independent, never-nil copy.
func TestOptionMap_Clone(t *testing.T) {
	assert.Equal(t, OptionMap{}, OptionMap(nil).Clone(), "a nil map clones to a non-nil empty map")
	src := OptionMap{agent.OptionIDModel: "opus"}
	cp := src.Clone()
	cp[agent.OptionIDModel] = "sonnet"
	assert.Equal(t, "opus", src[agent.OptionIDModel], "mutating the clone must not touch the source")
}

// TestOptionMap_Marshal drops empty values and is stable (sorted keys).
func TestOptionMap_Marshal(t *testing.T) {
	assert.Equal(t, "{}", OptionMap(nil).Marshal())
	assert.Equal(t, "{}", OptionMap{"a": ""}.Marshal(), "an all-empty map marshals to {}")
	assert.Equal(t, `{"effort":"high","model":"opus"}`,
		OptionMap{agent.OptionIDModel: "opus", agent.OptionIDEffort: "high", "blank": ""}.Marshal(),
		"empties are dropped and keys are sorted")
}

// TestResolveProviderDefaults fills the model/effort defaults without mutating the input,
// and leaves an explicit value untouched.
func TestResolveProviderDefaults(t *testing.T) {
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	src := OptionMap{}
	got := resolveProviderDefaults(src, claude)
	assert.NotEmpty(t, got[agent.OptionIDModel], "a missing model is filled with the provider default")
	assert.Equal(t, agent.EffortAuto, got[agent.OptionIDEffort], "a catalog-effort provider gets EffortAuto")
	assert.Empty(t, src, "resolveProviderDefaults must not mutate the input")

	kept := resolveProviderDefaults(OptionMap{agent.OptionIDModel: "sonnet"}, claude)
	assert.Equal(t, "sonnet", kept[agent.OptionIDModel], "an explicit value is left untouched")
}

// TestOptionsChangeDelta pins the minimal-delta the settings-edit CAS persists: a changed or
// added key carries its new value, a removed key carries "" (the wire merge deletes it), and
// an unchanged key is omitted -- so a concurrent server-initiated refresh's keys aren't
// clobbered by the edit's stale snapshot.
func TestOptionsChangeDelta(t *testing.T) {
	from := map[string]string{"model": "opus", "effort": "high", "sandbox": "read-only"}
	to := map[string]string{"model": "sonnet", "effort": "high", "network": "enabled"}

	delta := optionsChangeDelta(from, to)

	assert.Equal(t, "sonnet", delta["model"], "a changed key carries its new value")
	assert.Equal(t, "enabled", delta["network"], "an added key carries its new value")
	assert.Equal(t, "", delta["sandbox"], "a removed key carries an empty value (a delete)")
	assert.Contains(t, delta, "sandbox", "the removed key is present so the merge deletes it")
	assert.NotContains(t, delta, "effort", "an unchanged key is omitted from the delta")
}

// TestConfirmedOptions_PreservesPersistedOnlyOption is the regression guard for [S12]: a
// provider's persisted-only option (Pi's pi_provider) lives in the REQUEST base but never in
// the catalog-derived CurrentOptions, so confirmedOptions must keep it -- it overlays the
// confirmed values ONTO the base rather than replacing the base with them. If a future change
// fed CurrentOptions as the base instead, a Pi agent's pi_provider would silently drop on every
// live settings change (applySettingsLive passes the request as the base for exactly this reason).
func TestConfirmedOptions_PreservesPersistedOnlyOption(t *testing.T) {
	pi := leapmuxv1.AgentProvider_AGENT_PROVIDER_PI
	// base = the request the edit carries, including the persisted-only pi_provider.
	base := OptionMap{agent.OptionIDModel: "gpt-5.5", agent.PiOptionProvider: "openai", agent.OptionIDEffort: "high"}
	// confirmed = the running agent's catalog-only CurrentOptions snapshot (NO pi_provider).
	confirmed := OptionMap{agent.OptionIDModel: "gpt-5.5", agent.OptionIDEffort: "medium"}

	settled := confirmedOptions(pi, base, confirmed)

	assert.Equal(t, "openai", settled[agent.PiOptionProvider],
		"the persisted-only pi_provider survives because confirmedOptions overlays onto the base")
	assert.Equal(t, "medium", settled[agent.OptionIDEffort], "a confirmed value overrides the requested one")
	assert.Equal(t, "gpt-5.5", settled[agent.OptionIDModel])
}

// TestResolveOptionValueLabel_SelfNamedValuePrefersLive is the regression guard for the
// presence check: an option whose display name equals its id (a self-named value) is still
// resolved from the LIVE catalog, not mistaken for "absent from live" and resolved from a
// stale historical catalog. Only a value genuinely absent from live falls back to prev.
func TestResolveOptionValueLabel_SelfNamedValuePrefersLive(t *testing.T) {
	// Live offers "build" self-named (name == id); prev carries different names for "build"
	// and a "plan" value the live catalog no longer offers.
	live := []*leapmuxv1.AvailableOptionGroup{{
		Id:      "primaryAgent",
		Options: []*leapmuxv1.AvailableOption{{Id: "build", Name: "build"}},
	}}
	prev := []*leapmuxv1.AvailableOptionGroup{{
		Id: "primaryAgent",
		Options: []*leapmuxv1.AvailableOption{
			{Id: "build", Name: "STALE NAME"},
			{Id: "plan", Name: "Old Plan"},
		},
	}}

	assert.Equal(t, "build", resolveOptionValueLabel(live, prev, "primaryAgent", "build"),
		"a self-named live value wins over a stale historical name")
	assert.Equal(t, "Old Plan", resolveOptionValueLabel(live, prev, "primaryAgent", "plan"),
		"a value absent from live falls back to the historical name")
}

// TestOptionGroupsEqual_OrderInsensitive verifies the catalog-change comparison is keyed by
// group id (group-order-insensitive) with option lists compared as sets, matching the ACP
// layer's own change detection -- so a server merely re-sending the same groups/options in a
// different order is "unchanged" and does not churn a redundant option_groups write, while a
// genuine value or group-set change is still detected.
func TestOptionGroupsEqual_OrderInsensitive(t *testing.T) {
	a := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Options: []*leapmuxv1.AvailableOption{{Id: "opus"}, {Id: "sonnet"}}},
		{Id: agent.OptionIDEffort, Options: []*leapmuxv1.AvailableOption{{Id: "low"}, {Id: "high"}}},
	}
	// Same groups + options, both the group order AND the within-group option order reversed.
	reordered := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDEffort, Options: []*leapmuxv1.AvailableOption{{Id: "high"}, {Id: "low"}}},
		{Id: agent.OptionIDModel, Options: []*leapmuxv1.AvailableOption{{Id: "sonnet"}, {Id: "opus"}}},
	}
	assert.True(t, optionGroupsEqual(a, reordered), "a pure reorder of groups/options is not a change")

	// A changed current value IS a real difference.
	valueChanged := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, CurrentValue: "opus", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}, {Id: "sonnet"}}},
		{Id: agent.OptionIDEffort, Options: []*leapmuxv1.AvailableOption{{Id: "low"}, {Id: "high"}}},
	}
	assert.False(t, optionGroupsEqual(a, valueChanged), "a changed current value is a real difference")

	// A different group set IS a real difference.
	fewer := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Options: []*leapmuxv1.AvailableOption{{Id: "opus"}, {Id: "sonnet"}}},
	}
	assert.False(t, optionGroupsEqual(a, fewer), "a different group count is a real difference")
}

// TestParseOptionGroups_AllOrNothing pins the all-or-nothing decode: a fully-valid catalog
// round-trips, but a SINGLE malformed group drops the WHOLE catalog (returns nil) rather than a
// truncated slice. A truncated slice would silently omit a whole axis from the offline picker and
// never compare equal to the full live catalog (churning the column on every push); returning nil
// falls back to the static reconstruction and self-heals on the next clean push. This mirrors
// marshalOptionGroups, which errors rather than drop a group.
func TestParseOptionGroups_AllOrNothing(t *testing.T) {
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Options: []*leapmuxv1.AvailableOption{{Id: "opus"}, {Id: "sonnet"}}},
		{Id: agent.OptionIDEffort, Options: []*leapmuxv1.AvailableOption{{Id: "high"}}},
	}
	raw, err := marshalOptionGroups(groups)
	require.NoError(t, err)
	parsed := parseOptionGroups(raw)
	require.Len(t, parsed, 2, "a fully-valid catalog round-trips")
	assert.Equal(t, agent.OptionIDModel, parsed[0].GetId())
	assert.Equal(t, agent.OptionIDEffort, parsed[1].GetId())

	// A valid first group followed by a malformed one (options typed as a string, not an array)
	// drops the entire catalog -- the valid group is NOT surfaced on its own.
	mixed := `[{"id":"model","options":[{"id":"opus"}]},{"id":"effort","options":"not-an-array"}]`
	assert.Nil(t, parseOptionGroups(mixed),
		"a single malformed group invalidates the whole catalog (no truncation)")

	// Empty / sentinel / invalid-outer-JSON inputs all yield nil.
	assert.Nil(t, parseOptionGroups(""))
	assert.Nil(t, parseOptionGroups("[]"))
	assert.Nil(t, parseOptionGroups("{not json"))
}
