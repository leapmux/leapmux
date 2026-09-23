package acp

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOptionStateApply_AuthoritativeDropsStaleOutOfListValue is the regression guard for
// [C3]: on an authoritative payload (authoritativePayload) that reports a non-empty option
// list but an EMPTY current value, a stored value the new list no longer offers must NOT be
// surfaced. buildOptionValues does not inject the current into the option list, so surfacing
// an out-of-list stored value would render a group whose current has no matching radio. The
// option is dropped until a payload carries a concrete, in-list current -- matching the
// first-sighting behavior (empty current + nothing stored is likewise not surfaced).
func TestOptionStateApply_AuthoritativeDropsStaleOutOfListValue(t *testing.T) {
	t.Parallel()

	g := &optionState{}
	// A prior authoritative payload surfaced reasoning_effort=medium.
	g.apply([]ConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "medium",
		Options:      []ConfigOptionValue{{Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}}, authoritativePayload, ModeChannelUnmapped, "")
	require.Equal(t, "medium", g.values["reasoning_effort"], "the prior value is surfaced")

	// A later authoritative payload reports an empty current and a list that no longer offers
	// "medium" (a transient partial update where the prior selection is gone from the list).
	g.apply([]ConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "",
		Options:      []ConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, ModeChannelUnmapped, "")

	assert.Nil(t, optionids.GroupByID(g.groups, "reasoning_effort"),
		"a stale out-of-list value is dropped, not surfaced as a current absent from its own options")
	_, stillStored := g.values["reasoning_effort"]
	assert.False(t, stillStored, "the dropped value is not retained in the option values")

	// A value the new list DOES offer is still surfaced when reported as current.
	g.apply([]ConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "high",
		Options:      []ConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, ModeChannelUnmapped, "")
	grp := optionids.GroupByID(g.groups, "reasoning_effort")
	require.NotNil(t, grp, "a concrete in-list current surfaces the option again")
	assert.Equal(t, "high", grp.GetCurrentValue())
}

// TestOptionStateApply_AuthoritativeOutOfListCurrentIsInjected guards [E1]: a (non-conforming)
// server can report a non-empty CurrentValue that is ABSENT from the option's own value list.
// buildOptionValues builds the option list from the payload's Options ONLY -- it does not inject
// the current -- so without buildOptionGroup's injection the surfaced group would carry a
// CurrentValue with no matching option (an invalid radio selection that CurrentOptions /
// mergeOptionValues would then persist). The empty-current and prefer-stored paths already guard
// via storedIfOffered; this is the same guarantee for the server's OWN authoritative current.
func TestOptionStateApply_AuthoritativeOutOfListCurrentIsInjected(t *testing.T) {
	t.Parallel()

	g := &optionState{}
	// A plain (non-effort) select so no strongest-first reorder is involved: the server reports
	// "extreme" as current but only lists low/high.
	g.apply([]ConfigOption{{
		ID: "verbosity", Name: "Verbosity",
		CurrentValue: "extreme",
		Options:      []ConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, ModeChannelUnmapped, "")

	grp := optionids.GroupByID(g.groups, "verbosity")
	require.NotNil(t, grp, "the server's authoritative current surfaces the group")
	assert.Equal(t, "extreme", grp.GetCurrentValue())
	require.NotNil(t, agenttest.OptionByID(grp, "extreme"),
		"the off-list authoritative current is injected as a selectable option so its radio matches")
	assert.NotNil(t, agenttest.OptionByID(grp, "low"), "the advertised options are preserved")
	assert.NotNil(t, agenttest.OptionByID(grp, "high"), "the advertised options are preserved")
	assert.Equal(t, "extreme", g.values["verbosity"], "the current is stored")
	assert.Equal(t, "extreme", agent.CurrentOptions(g.groups)["verbosity"],
		"the readback reflects the current, which now has a matching option")
}

// TestOptionStateApply_ReorderOnlyResendIsNotAListChange is the [V12] regression guard: a server
// re-sending the same config options in a different ORDER must not report a list change, which
// would fire a redundant status broadcast + catalog write. Every ACP config group shares
// OptionOrderTrailing, so payload order is not a meaningful axis; the list compare is keyed by id
// (order-insensitive), matching the order-insensitive value compare.
func TestOptionStateApply_ReorderOnlyResendIsNotAListChange(t *testing.T) {
	t.Parallel()

	g := &optionState{}
	effort := ConfigOption{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "high",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}
	allow := ConfigOption{
		ID: "allow_all", Name: "Allow All", CurrentValue: "off",
		Options: []ConfigOptionValue{{Value: "off"}, {Value: "on"}},
	}
	g.apply([]ConfigOption{effort, allow}, authoritativePayload, ModeChannelUnmapped, "")

	// Same two options, same values, REVERSED order.
	valueChanged, listChanged := g.apply([]ConfigOption{allow, effort}, authoritativePayload, ModeChannelUnmapped, "")
	assert.False(t, valueChanged, "no value changed on a reorder")
	assert.False(t, listChanged, "a reorder-only re-send is not a list change")

	// A genuine list change (a new option) IS still reported.
	verbosity := ConfigOption{
		ID: "verbosity", Name: "Verbosity", CurrentValue: "med",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "med"}},
	}
	_, listChanged = g.apply([]ConfigOption{allow, effort, verbosity}, authoritativePayload, ModeChannelUnmapped, "")
	assert.True(t, listChanged, "adding an option is a real list change")

	// A value change on an existing option IS still reported.
	effortLow := effort
	effortLow.CurrentValue = "low"
	valueChanged, _ = g.apply([]ConfigOption{allow, effortLow, verbosity}, authoritativePayload, ModeChannelUnmapped, "")
	assert.True(t, valueChanged, "a changed current value is still detected")
}

// TestOptionStateApply_IntraGroupOptionReorderIsNotAListChange guards the option-order half of
// [V12]: re-sending a group with its OPTION LIST in a different order (not the groups reordered
// among themselves) must not be a list change, or it would fire a redundant status broadcast +
// catalog write. allow_all is a non-effort select, so unlike the effort axis its option order is
// the server's order (not canonicalized), exercising optionGroupEqualExact's set comparison.
func TestOptionStateApply_IntraGroupOptionReorderIsNotAListChange(t *testing.T) {
	t.Parallel()

	g := &optionState{}
	allow := func(opts ...ConfigOptionValue) ConfigOption {
		return ConfigOption{ID: "allow_all", Name: "Allow All", CurrentValue: "off", Options: opts}
	}
	g.apply([]ConfigOption{allow(ConfigOptionValue{Value: "off"}, ConfigOptionValue{Value: "on"})},
		authoritativePayload, ModeChannelUnmapped, "")

	// Same option, same current, the OPTION list reordered (off,on -> on,off).
	valueChanged, listChanged := g.apply(
		[]ConfigOption{allow(ConfigOptionValue{Value: "on"}, ConfigOptionValue{Value: "off"})},
		authoritativePayload, ModeChannelUnmapped, "")
	assert.False(t, valueChanged, "no value changed on an intra-group option reorder")
	assert.False(t, listChanged, "reordering the options within a group is not a list change")

	// Adding a new option to the group IS a real list change.
	_, listChanged = g.apply(
		[]ConfigOption{allow(ConfigOptionValue{Value: "off"}, ConfigOptionValue{Value: "on"}, ConfigOptionValue{Value: "ask"})},
		authoritativePayload, ModeChannelUnmapped, "")
	assert.True(t, listChanged, "adding an option within a group is a real list change")
}

// TestOptionStateApply_DuplicateIDResolvesContentSmallest guards that a (non-conforming) server
// reporting the SAME config-option id twice resolves to the content-smallest occurrence
// DETERMINISTICALLY -- regardless of the order the duplicates are listed -- so the surfaced group
// can't flip its value between two payloads that list the duplicates in different orders (which would
// fire a redundant status broadcast + catalog write). Mirrors acpConfigOptionContentLess's tie-break
// for the claimed model/mode axes.
func TestOptionStateApply_DuplicateIDResolvesContentSmallest(t *testing.T) {
	t.Parallel()

	dupOff := ConfigOption{
		ID: "allow_all", Name: "Allow All", CurrentValue: "off",
		Options: []ConfigOptionValue{{Value: "off"}, {Value: "on"}},
	}
	dupOn := ConfigOption{
		ID: "allow_all", Name: "Allow All", CurrentValue: "on",
		Options: []ConfigOptionValue{{Value: "off"}, {Value: "on"}},
	}

	// Exactly one group is surfaced (a shared key never double-lists), and its value is the
	// content-smallest ("off" < "on") regardless of the order the duplicates arrive in.
	cases := []struct {
		name  string
		order []ConfigOption
	}{
		{"off-first", []ConfigOption{dupOff, dupOn}},
		{"on-first", []ConfigOption{dupOn, dupOff}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &optionState{}
			g.apply(tc.order, authoritativePayload, ModeChannelUnmapped, "")
			count := 0
			for _, grp := range g.groups {
				if grp.GetId() == "allow_all" {
					count++
				}
			}
			assert.Equal(t, 1, count, "a duplicate id surfaces exactly one group")
			grp := optionids.GroupByID(g.groups, "allow_all")
			require.NotNil(t, grp)
			assert.Equal(t, "off", grp.GetCurrentValue(),
				"the content-smallest duplicate wins deterministically, not the first sighting")
		})
	}

	// Re-applying the duplicates in the REVERSED order is not a value/list change: the winner is
	// stable, so no redundant broadcast/catalog write fires. (First-sighting dedup would flip the
	// value off<->on between these two payloads.)
	g := &optionState{}
	g.apply([]ConfigOption{dupOff, dupOn}, authoritativePayload, ModeChannelUnmapped, "")
	valueChanged, listChanged := g.apply([]ConfigOption{dupOn, dupOff}, authoritativePayload, ModeChannelUnmapped, "")
	assert.False(t, valueChanged, "the duplicate winner is stable across payload orders")
	assert.False(t, listChanged, "reordering duplicates is not a list change")
}

// TestRecordOptimistic_SynthesizesGroupForKnownButUnsurfacedID guards the [E12] readback gap:
// when the server advertises an option with an empty current (known, but not surfaced as a
// group) and later accepts a write WITHOUT echoing a refreshed configOptions, recordOptimistic
// must synthesize a group from the advertised template -- so OptionGroups()/CurrentOptions()
// (and applySettingsLive's orphan reconcile) reflect the accepted value instead of dropping it.
func TestRecordOptimistic_SynthesizesGroupForKnownButUnsurfacedID(t *testing.T) {
	t.Parallel()

	g := &optionState{}
	// Handshake advertises reasoning_effort with an empty current: marked known (with its
	// template) but not surfaced as a group.
	g.apply([]ConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "",
		Options:      []ConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, ModeChannelUnmapped, "")
	require.Nil(t, optionids.GroupByID(g.groups, "reasoning_effort"),
		"an option advertised with an empty current is known but not yet surfaced")

	// The server accepts a write but echoes no configOptions (off-spec): recordOptimistic must
	// surface the value.
	g.recordOptimistic("reasoning_effort", "high", "")

	grp := optionids.GroupByID(g.groups, "reasoning_effort")
	require.NotNil(t, grp, "the accepted value is surfaced as a synthesized group")
	assert.Equal(t, "high", grp.GetCurrentValue())
	// Synthesized from the real advertised template, so it carries the full option list (low,
	// high) -- not a degenerate single-value group.
	assert.Len(t, grp.GetOptions(), 2)
	assert.Equal(t, "high", agent.CurrentOptions(g.groups)["reasoning_effort"],
		"the readback CurrentOptions reflects the accepted value")
}

// TestRecordOptimistic_OffListValueIsSelectable guards that when the server accepts a value
// absent from the option's last-advertised list (off-spec) and echoes no configOptions, the
// synthesized group's CurrentValue still has a matching option -- otherwise the panel would
// render a current selection with no radio (buildOptionValues does not inject the current).
func TestRecordOptimistic_OffListValueIsSelectable(t *testing.T) {
	t.Parallel()

	g := &optionState{}
	// Advertise reasoning_effort with options low/high (no "ultracode").
	g.apply([]ConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "",
		Options:      []ConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, ModeChannelUnmapped, "")

	// The server accepts an off-list value ("ultracode") and echoes nothing.
	g.recordOptimistic("reasoning_effort", "ultracode", "")

	grp := optionids.GroupByID(g.groups, "reasoning_effort")
	require.NotNil(t, grp)
	assert.Equal(t, "ultracode", grp.GetCurrentValue())
	var values []string
	for _, o := range grp.GetOptions() {
		values = append(values, o.GetId())
	}
	assert.Contains(t, values, "ultracode", "the accepted off-list value is added so the current selection is selectable")
	assert.Contains(t, values, "low", "the advertised options are preserved")
	assert.Contains(t, values, "high", "the advertised options are preserved")
	assert.Len(t, g.templates["reasoning_effort"].Options, 2,
		"the stored template is not mutated by the off-list append")
}

// TestOptionState_OffersValue guards the value-validation setConfigOption uses to skip a write
// the current option list does not offer (e.g. a stale effort tier inherited across a model
// switch), while staying permissive for an option with no advertised list so a persisted
// preference can still be re-pushed.
func TestOptionState_OffersValue(t *testing.T) {
	t.Parallel()

	g := &optionState{}
	g.apply([]ConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", CurrentValue: "high",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}}, authoritativePayload, ModeChannelUnmapped, "")

	assert.True(t, g.offersValue("reasoning_effort", "low"), "an offered value is pushable")
	assert.False(t, g.offersValue("reasoning_effort", "xhigh"), "a value the current list does not offer is skipped")
	assert.True(t, g.offersValue("unknown_id", "anything"),
		"an option with no advertised template is permissive so a persisted preference can be re-pushed")
}

// TestIsReservedOptionKey pins the invariant that only the axes owning a dedicated
// mapped option group (model, permission mode, primary agent) are reserved against
// config-option shadowing -- and, critically, that EFFORT is NOT reserved: an
// ACP provider with an effort axis surfaces it as a server-driven config option, so excluding
// the effort key would silently drop that option group.
func TestIsReservedOptionKey(t *testing.T) {
	t.Parallel()

	for _, id := range []string{agent.OptionIDModel, agent.OptionIDPermissionMode, agent.OptionIDPrimaryAgent} {
		assert.True(t, isReservedOptionKey(id), "%q owns a dedicated mapped group and must be reserved", id)
	}
	for _, id := range []string{agent.OptionIDEffort, "reasoning_effort", "allow_all", "thought_level", ""} {
		assert.False(t, isReservedOptionKey(id), "%q is an option (or effort) key and must not be reserved", id)
	}
}
