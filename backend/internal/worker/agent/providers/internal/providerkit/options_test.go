package providerkit

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveGroup_DefaultsEmptyCurrentToTemplateDefault verifies the overlay helper
// reads order/default from the template and falls back to the default when the
// caller supplies no current value (so a group whose current wasn't wired still
// renders a valid, in-list selection rather than a blank one).
func TestLiveGroup_DefaultsEmptyCurrentToTemplateDefault(t *testing.T) {
	t.Parallel()

	tmpl := &leapmuxv1.AvailableOptionGroup{
		Id:           "x",
		Label:        "X",
		DefaultValue: "b",
		Order:        agent.OptionOrderProviderFirst,
		Options:      []*leapmuxv1.AvailableOption{{Id: "a"}, {Id: "b"}},
	}

	withCurrent := LiveGroup(tmpl, "a")
	assert.Equal(t, "a", withCurrent.GetCurrentValue())
	assert.Equal(t, agent.OptionOrderProviderFirst, withCurrent.GetOrder(), "order comes from the template")

	empty := LiveGroup(tmpl, "")
	assert.Equal(t, "b", empty.GetCurrentValue(), "an empty current falls back to the template default")
}

// TestLiveGroup_HonorsTemplateMutable verifies LiveGroup carries the template's Mutable
// flag through instead of forcing every projected group editable -- so a provider can
// project an agent-controlled, read-only axis through LiveGroup.
func TestLiveGroup_HonorsTemplateMutable(t *testing.T) {
	t.Parallel()

	mutable := LiveGroup(&leapmuxv1.AvailableOptionGroup{Id: "x", DefaultValue: "a", Mutable: true, Options: []*leapmuxv1.AvailableOption{{Id: "a"}}}, "a")
	assert.True(t, mutable.GetMutable(), "a mutable template projects a mutable group")

	readOnly := LiveGroup(&leapmuxv1.AvailableOptionGroup{Id: "x", DefaultValue: "a", Mutable: false, Options: []*leapmuxv1.AvailableOption{{Id: "a"}}}, "a")
	assert.False(t, readOnly.GetMutable(), "a read-only template projects a read-only group")
}

// TestFilterGroupOptions_PreservesTemplateFields verifies FilterGroupOptions narrows the
// option list while carrying every other template field (id/label/current/default/mutable/
// order) through unchanged and without mutating the shared template -- the field-preservation
// contract the shared clone helper (cloneOptionGroupTemplate) exists to keep mechanical as
// AvailableOptionGroup grows fields.
func TestFilterGroupOptions_PreservesTemplateFields(t *testing.T) {
	t.Parallel()

	tmpl := &leapmuxv1.AvailableOptionGroup{
		Id:           "perm",
		Label:        "Permission",
		CurrentValue: "plan",
		DefaultValue: "default",
		Mutable:      true,
		Order:        agent.OptionOrderPermissionMode,
		Options:      []*leapmuxv1.AvailableOption{{Id: "auto"}, {Id: "plan"}, {Id: "default"}},
	}

	filtered := FilterGroupOptions(tmpl, func(o *leapmuxv1.AvailableOption) bool { return o.GetId() != "auto" })

	require.NotNil(t, filtered)
	ids := make([]string, 0, len(filtered.GetOptions()))
	for _, o := range filtered.GetOptions() {
		ids = append(ids, o.GetId())
	}
	assert.Equal(t, []string{"plan", "default"}, ids, "the keep predicate drops 'auto'")
	assert.Equal(t, "perm", filtered.GetId())
	assert.Equal(t, "Permission", filtered.GetLabel())
	assert.Equal(t, "plan", filtered.GetCurrentValue(), "current carried from the template")
	assert.Equal(t, "default", filtered.GetDefaultValue(), "default carried from the template")
	assert.True(t, filtered.GetMutable(), "mutability carried from the template")
	assert.Equal(t, agent.OptionOrderPermissionMode, filtered.GetOrder(), "order carried from the template")

	assert.Len(t, tmpl.GetOptions(), 3, "filtering must not mutate the shared template")
	assert.Nil(t, FilterGroupOptions(nil, func(*leapmuxv1.AvailableOption) bool { return true }))
}

// TestReadOnlyModelAndEffortGroups verifies the hidden-UI read-only projection: the model
// group surfaces the HUMANIZED display name (not the raw id) while keeping the raw id as
// the option value, the groups are non-mutable, and a concrete effort is surfaced while an
// auto/empty effort is suppressed.
func TestReadOnlyModelAndEffortGroups(t *testing.T) {
	t.Parallel()

	groups := ReadOnlyModelAndEffortGroups("opus[1m]", "Opus (1M context)", "high")
	mg := optionids.GroupByID(groups, agent.OptionIDModel)
	require.NotNil(t, mg)
	require.Len(t, mg.GetOptions(), 1)
	assert.Equal(t, "opus[1m]", mg.GetOptions()[0].GetId(), "the raw id stays as the option value")
	assert.Equal(t, "Opus (1M context)", mg.GetOptions()[0].GetName(),
		"the humanized display name is surfaced, not the raw bracketed id")
	assert.False(t, mg.GetMutable(), "the read-only model group is non-mutable")
	assert.NotNil(t, optionids.GroupByID(groups, agent.OptionIDEffort), "a concrete effort is surfaced")

	// An auto/empty effort is suppressed (only a concrete effort is surfaced).
	autoGroups := ReadOnlyModelAndEffortGroups("opus[1m]", "Opus", agent.EffortAuto)
	assert.Nil(t, optionids.GroupByID(autoGroups, agent.OptionIDEffort), "an auto effort is not surfaced read-only")
	assert.Nil(t, optionids.GroupByID(ReadOnlyModelAndEffortGroups("opus[1m]", "Opus", ""), agent.OptionIDEffort),
		"an empty effort is not surfaced read-only")

	// modelThenEffort omits an absent group rather than emitting a nil entry: an empty model with a
	// concrete effort yields the effort group ALONE (no model group, no panic), and both absent
	// yields an empty slice.
	effortOnly := ReadOnlyModelAndEffortGroups("", "", "high")
	assert.Nil(t, optionids.GroupByID(effortOnly, agent.OptionIDModel), "an empty model is not surfaced")
	require.NotNil(t, optionids.GroupByID(effortOnly, agent.OptionIDEffort), "the concrete effort still surfaces alone")
	assert.Len(t, effortOnly, 1, "only the effort group is present")
	assert.Empty(t, ReadOnlyModelAndEffortGroups("", "", ""), "absent model and effort yield no groups")
}
