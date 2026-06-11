package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateLaunchOptions_RejectsUnknownPermissionMode guards [S1]: for a CLI-managed provider
// whose permission modes are a FIXED enum (Claude/Codex), an explicitly-requested mode the provider
// doesn't offer is rejected, while a valid mode and an unsupplied mode pass.
func TestValidateLaunchOptions_RejectsUnknownPermissionMode(t *testing.T) {
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	pmg := optionids.GroupByID(AvailableOptionGroupsForProvider(claude), OptionIDPermissionMode)
	require.NotNil(t, pmg)
	require.NotEmpty(t, pmg.GetOptions(), "Claude has a fixed permission-mode enum")
	validMode := pmg.GetOptions()[0].GetId()

	require.NoError(t, ValidateLaunchOptions(claude, optionmap.Map{OptionIDPermissionMode: validMode}),
		"a valid permission mode passes")
	require.NoError(t, ValidateLaunchOptions(claude, optionmap.Map{}),
		"an unsupplied permission mode is skipped")
	require.Error(t, ValidateLaunchOptions(claude, optionmap.Map{OptionIDPermissionMode: "bogus-mode"}),
		"an unknown permission mode is rejected")
}

// TestValidateLaunchOptions_DoesNotValidateModelOrEffort guards [S1]: model and effort are NOT
// validated at spawn -- every provider (including Claude) discovers its model catalog and effort
// tiers from the running CLI, seeding only a fallback, so a value valid in the live catalog but
// absent from the seed must NOT be rejected. A model/effort not in any seed therefore passes.
func TestValidateLaunchOptions_DoesNotValidateModelOrEffort(t *testing.T) {
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	require.NoError(t, ValidateLaunchOptions(claude, optionmap.Map{OptionIDModel: "a-future-model-not-in-the-seed"}),
		"a model absent from the seed (but maybe in the live catalog) is not rejected")
	require.NoError(t, ValidateLaunchOptions(claude, optionmap.Map{OptionIDEffort: "a-non-seed-effort"}),
		"effort is not validated at spawn")
}

// TestValidateLaunchOptions_ACPProviderSkipsPermissionMode guards [S1]: an ACP provider discovers its
// permission modes from the daemon (its static group is only a seed), so a mode NOT in the seed must
// NOT be rejected at spawn -- the running session validates the real value.
func TestValidateLaunchOptions_ACPProviderSkipsPermissionMode(t *testing.T) {
	copilot := leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT
	require.NoError(t, ValidateLaunchOptions(copilot, optionmap.Map{OptionIDPermissionMode: "a-dynamic-daemon-mode"}),
		"an ACP provider's daemon-discovered permission mode is not rejected against the static seed")
}

// TestProviderManagesEffort distinguishes providers that own a model-dependent effort
// catalog (Claude/Codex/Pi -- effort default stamped by resolveProviderDefaults) from
// ACP providers whose effort, if any, is a server-driven config option.
func TestProviderManagesEffort(t *testing.T) {
	for _, p := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
	} {
		assert.True(t, ProviderManagesEffort(p), "%v owns a model-dependent effort catalog", p)
	}
	for _, p := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	} {
		assert.False(t, ProviderManagesEffort(p), "%v has no leapmux-managed effort default", p)
	}
}

// TestEffortSupportedByModel covers the helper sanitizeIncomingOptions uses to decide
// whether a requested effort survives a model switch. It reads each model's per-model effort
// sub_group (carried independently of which model is current), so it answers for a model
// other than the catalog's current one.
func TestEffortSupportedByModel(t *testing.T) {
	models := []*ModelInfo{
		{Id: "opus", DisplayName: "Opus", DefaultEffort: "high", SupportedEfforts: []*EffortInfo{
			{Id: "auto"}, {Id: "high"}, {Id: "xhigh"},
		}},
		{Id: "sonnet", DisplayName: "Sonnet", DefaultEffort: "high", SupportedEfforts: []*EffortInfo{
			{Id: "auto"}, {Id: "high"},
		}},
		{Id: "haiku", DisplayName: "Haiku"}, // no effort axis
	}
	// Build a catalog whose CURRENT model is opus, then query OTHER models too.
	catalog := []*leapmuxv1.AvailableOptionGroup{modelOptionGroup(models, "opus", effortSubGroups)}
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE

	assert.True(t, EffortSupportedByModel(catalog, claude, "opus", "xhigh"), "opus offers xhigh")
	assert.True(t, EffortSupportedByModel(catalog, claude, "sonnet", "high"), "sonnet offers high even though opus is current")
	assert.False(t, EffortSupportedByModel(catalog, claude, "sonnet", "xhigh"), "sonnet does not offer xhigh")
	assert.False(t, EffortSupportedByModel(catalog, claude, "haiku", "high"), "haiku has no effort axis")
	assert.False(t, EffortSupportedByModel(catalog, claude, "unknown-model", "high"), "an unlisted model is unsupported")
	assert.False(t, EffortSupportedByModel(nil, claude, "opus", "high"), "no catalog -> unsupported")
	assert.False(t, EffortSupportedByModel(catalog, claude, "opus", "low"), "a tier the model doesn't list is unsupported")

	// A re-spelled model alias must resolve to its canonical catalog id: the
	// fully-qualified CLI spelling "claude-opus-4-8[1m]" normalizes to the catalog's
	// "opus[1m]". Matching raw would miss the model and wrongly reset a valid effort.
	aliasModels := []*ModelInfo{
		{Id: "opus[1m]", DisplayName: "Opus (1M)", DefaultEffort: "high", SupportedEfforts: []*EffortInfo{
			{Id: "auto"}, {Id: "high"}, {Id: "xhigh"},
		}},
	}
	aliasCatalog := []*leapmuxv1.AvailableOptionGroup{modelOptionGroup(aliasModels, "opus[1m]", effortSubGroups)}
	assert.True(t, EffortSupportedByModel(aliasCatalog, claude, "claude-opus-4-8[1m]", "xhigh"),
		"a fully-qualified alias resolves to the canonical catalog id and finds its efforts")
	assert.True(t, EffortSupportedByModel(aliasCatalog, claude, "OPUS[1M]", "xhigh"),
		"an uppercased alias normalizes and matches")

	// [V5] A model the picker HIDES (e.g. Claude's standard-context "opus", surfaced only as
	// the model group's current value, never as a selectable option because modelOptionGroup
	// drops Hidden models) carries no per-model effort sub_group. The top-level effort group is
	// nonetheless built for that current model (modelAndEffortGroups resolves it via
	// FindAvailableModel, which does NOT filter Hidden), so its effort must validate against
	// that group rather than wrongly resetting a valid tier to auto.
	hiddenModels := []*ModelInfo{
		{Id: "opus", DisplayName: "Opus", DefaultEffort: "high", Hidden: true, SupportedEfforts: []*EffortInfo{
			{Id: "auto"}, {Id: "high"}, {Id: "xhigh"},
		}},
		{Id: "sonnet", DisplayName: "Sonnet", DefaultEffort: "high", SupportedEfforts: []*EffortInfo{
			{Id: "auto"}, {Id: "high"},
		}},
	}
	hiddenCatalog := modelAndEffortGroups(hiddenModels, "opus", "auto", EffortGroupLabel, nil)
	require.Nil(t, findAvailableOptionByID(optionids.GroupByID(hiddenCatalog, OptionIDModel), "opus"),
		"the hidden current model is not a selectable option")
	assert.True(t, EffortSupportedByModel(hiddenCatalog, claude, "opus", "xhigh"),
		"the hidden current model's effort validates against the top-level effort group")
	assert.False(t, EffortSupportedByModel(hiddenCatalog, claude, "opus", "low"),
		"a tier the hidden current model doesn't offer is still rejected")
	assert.True(t, EffortSupportedByModel(hiddenCatalog, claude, "sonnet", "high"),
		"a LISTED model still validates against its own per-model sub_group, not the current model's")
	assert.False(t, EffortSupportedByModel(hiddenCatalog, claude, "haiku", "high"),
		"a model neither listed nor the current one stays unsupported")
}

// findAvailableOptionByID returns the option with the given id in the group, or nil.
func findAvailableOptionByID(g *leapmuxv1.AvailableOptionGroup, id string) *leapmuxv1.AvailableOption {
	for _, o := range g.GetOptions() {
		if o.GetId() == id {
			return o
		}
	}
	return nil
}

// TestModelEffortKnown covers the gate the effort reset uses to avoid clobbering a valid effort for a
// model the catalog doesn't describe (a tier valid in a running provider's live catalog but absent
// from a stopped agent's static seed): known for a selectable model and for the hidden current model
// with a top-level effort group, UNKNOWN for a model absent from the catalog entirely.
func TestModelEffortKnown(t *testing.T) {
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	models := []*ModelInfo{
		{Id: "opus", DisplayName: "Opus", DefaultEffort: "high", SupportedEfforts: []*EffortInfo{{Id: "auto"}, {Id: "high"}, {Id: "xhigh"}}},
		{Id: "sonnet", DisplayName: "Sonnet", DefaultEffort: "high", SupportedEfforts: []*EffortInfo{{Id: "auto"}, {Id: "high"}}},
		{Id: "haiku", DisplayName: "Haiku"}, // selectable, but no effort axis
	}
	catalog := []*leapmuxv1.AvailableOptionGroup{modelOptionGroup(models, "opus", effortSubGroups)}

	assert.True(t, ModelEffortKnown(catalog, claude, "opus"), "a selectable model is known")
	assert.True(t, ModelEffortKnown(catalog, claude, "sonnet"), "a non-current selectable model is known")
	assert.True(t, ModelEffortKnown(catalog, claude, "haiku"), "a selectable but effort-less model is still known")
	assert.True(t, ModelEffortKnown(catalog, claude, "claude-opus-4-8"), "a re-spelled alias normalizes to a selectable id")
	assert.False(t, ModelEffortKnown(catalog, claude, "future-model"), "a model absent from the catalog is unknown")
	assert.False(t, ModelEffortKnown(nil, claude, "opus"), "no catalog -> unknown")

	// A hidden current model (not selectable, but the model group's current value) is known via the
	// top-level effort group built for it. sonnet is selectable so the model group exists at all.
	hiddenModels := []*ModelInfo{
		{Id: "opus", DisplayName: "Opus", Hidden: true, DefaultEffort: "high", SupportedEfforts: []*EffortInfo{{Id: "auto"}, {Id: "high"}, {Id: "xhigh"}}},
		{Id: "sonnet", DisplayName: "Sonnet", DefaultEffort: "high", SupportedEfforts: []*EffortInfo{{Id: "auto"}, {Id: "high"}}},
	}
	hiddenCatalog := modelAndEffortGroups(hiddenModels, "opus", "auto", EffortGroupLabel, nil)
	require.Nil(t, findAvailableOptionByID(optionids.GroupByID(hiddenCatalog, OptionIDModel), "opus"),
		"the hidden current model is not a selectable option")
	assert.True(t, ModelEffortKnown(hiddenCatalog, claude, "opus"),
		"the hidden current model is known via the top-level effort group")
	assert.False(t, ModelEffortKnown(hiddenCatalog, claude, "future-model"),
		"a model that is neither selectable nor the current value is unknown even with an effort group present")
}

// TestOptionStateApply_AuthoritativeDropsStaleOutOfListValue is the regression guard for
// [C3]: on an authoritative payload (authoritativePayload) that reports a non-empty option
// list but an EMPTY current value, a stored value the new list no longer offers must NOT be
// surfaced. buildOptionValues does not inject the current into the option list, so surfacing
// an out-of-list stored value would render a group whose current has no matching radio. The
// option is dropped until a payload carries a concrete, in-list current -- matching the
// first-sighting behavior (empty current + nothing stored is likewise not surfaced).
func TestOptionStateApply_AuthoritativeDropsStaleOutOfListValue(t *testing.T) {
	g := &optionState{}
	// A prior authoritative payload surfaced reasoning_effort=medium.
	g.apply([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "medium",
		Options:      []acpConfigOptionValue{{Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}}, authoritativePayload, modeChannelUnmapped)
	require.Equal(t, "medium", g.values["reasoning_effort"], "the prior value is surfaced")

	// A later authoritative payload reports an empty current and a list that no longer offers
	// "medium" (a transient partial update where the prior selection is gone from the list).
	g.apply([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "",
		Options:      []acpConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, modeChannelUnmapped)

	assert.Nil(t, optionids.GroupByID(g.groups, "reasoning_effort"),
		"a stale out-of-list value is dropped, not surfaced as a current absent from its own options")
	_, stillStored := g.values["reasoning_effort"]
	assert.False(t, stillStored, "the dropped value is not retained in the option values")

	// A value the new list DOES offer is still surfaced when reported as current.
	g.apply([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "high",
		Options:      []acpConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, modeChannelUnmapped)
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
	g := &optionState{}
	// A plain (non-effort) select so no strongest-first reorder is involved: the server reports
	// "extreme" as current but only lists low/high.
	g.apply([]acpConfigOption{{
		ID: "verbosity", Name: "Verbosity",
		CurrentValue: "extreme",
		Options:      []acpConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, modeChannelUnmapped)

	grp := optionids.GroupByID(g.groups, "verbosity")
	require.NotNil(t, grp, "the server's authoritative current surfaces the group")
	assert.Equal(t, "extreme", grp.GetCurrentValue())
	require.NotNil(t, findAvailableOptionByID(grp, "extreme"),
		"the off-list authoritative current is injected as a selectable option so its radio matches")
	assert.NotNil(t, findAvailableOptionByID(grp, "low"), "the advertised options are preserved")
	assert.NotNil(t, findAvailableOptionByID(grp, "high"), "the advertised options are preserved")
	assert.Equal(t, "extreme", g.values["verbosity"], "the current is stored")
	assert.Equal(t, "extreme", CurrentOptions(g.groups)["verbosity"],
		"the readback reflects the current, which now has a matching option")
}

// TestOptionStateApply_ReorderOnlyResendIsNotAListChange is the [V12] regression guard: a server
// re-sending the same config options in a different ORDER must not report a list change, which
// would fire a redundant status broadcast + catalog write. Every ACP config group shares
// OptionOrderTrailing, so payload order is not a meaningful axis; the list compare is keyed by id
// (order-insensitive), matching the order-insensitive value compare.
func TestOptionStateApply_ReorderOnlyResendIsNotAListChange(t *testing.T) {
	g := &optionState{}
	effort := acpConfigOption{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}
	allow := acpConfigOption{
		ID: "allow_all", Name: "Allow All", CurrentValue: "off",
		Options: []acpConfigOptionValue{{Value: "off"}, {Value: "on"}},
	}
	g.apply([]acpConfigOption{effort, allow}, authoritativePayload, modeChannelUnmapped)

	// Same two options, same values, REVERSED order.
	valueChanged, listChanged := g.apply([]acpConfigOption{allow, effort}, authoritativePayload, modeChannelUnmapped)
	assert.False(t, valueChanged, "no value changed on a reorder")
	assert.False(t, listChanged, "a reorder-only re-send is not a list change")

	// A genuine list change (a new option) IS still reported.
	verbosity := acpConfigOption{
		ID: "verbosity", Name: "Verbosity", CurrentValue: "med",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "med"}},
	}
	_, listChanged = g.apply([]acpConfigOption{allow, effort, verbosity}, authoritativePayload, modeChannelUnmapped)
	assert.True(t, listChanged, "adding an option is a real list change")

	// A value change on an existing option IS still reported.
	effortLow := effort
	effortLow.CurrentValue = "low"
	valueChanged, _ = g.apply([]acpConfigOption{allow, effortLow, verbosity}, authoritativePayload, modeChannelUnmapped)
	assert.True(t, valueChanged, "a changed current value is still detected")
}

// TestOptionStateApply_IntraGroupOptionReorderIsNotAListChange guards the option-order half of
// [V12]: re-sending a group with its OPTION LIST in a different order (not the groups reordered
// among themselves) must not be a list change, or it would fire a redundant status broadcast +
// catalog write. allow_all is a non-effort select, so unlike the effort axis its option order is
// the server's order (not canonicalized), exercising optionGroupEqualExact's set comparison.
func TestOptionStateApply_IntraGroupOptionReorderIsNotAListChange(t *testing.T) {
	g := &optionState{}
	allow := func(opts ...acpConfigOptionValue) acpConfigOption {
		return acpConfigOption{ID: "allow_all", Name: "Allow All", CurrentValue: "off", Options: opts}
	}
	g.apply([]acpConfigOption{allow(acpConfigOptionValue{Value: "off"}, acpConfigOptionValue{Value: "on"})},
		authoritativePayload, modeChannelUnmapped)

	// Same option, same current, the OPTION list reordered (off,on -> on,off).
	valueChanged, listChanged := g.apply(
		[]acpConfigOption{allow(acpConfigOptionValue{Value: "on"}, acpConfigOptionValue{Value: "off"})},
		authoritativePayload, modeChannelUnmapped)
	assert.False(t, valueChanged, "no value changed on an intra-group option reorder")
	assert.False(t, listChanged, "reordering the options within a group is not a list change")

	// Adding a new option to the group IS a real list change.
	_, listChanged = g.apply(
		[]acpConfigOption{allow(acpConfigOptionValue{Value: "off"}, acpConfigOptionValue{Value: "on"}, acpConfigOptionValue{Value: "ask"})},
		authoritativePayload, modeChannelUnmapped)
	assert.True(t, listChanged, "adding an option within a group is a real list change")
}

// TestOptionStateApply_DuplicateIDResolvesContentSmallest guards that a (non-conforming) server
// reporting the SAME config-option id twice resolves to the content-smallest occurrence
// DETERMINISTICALLY -- regardless of the order the duplicates are listed -- so the surfaced group
// can't flip its value between two payloads that list the duplicates in different orders (which would
// fire a redundant status broadcast + catalog write). Mirrors acpConfigOptionContentLess's tie-break
// for the claimed model/mode axes.
func TestOptionStateApply_DuplicateIDResolvesContentSmallest(t *testing.T) {
	dupOff := acpConfigOption{
		ID: "allow_all", Name: "Allow All", CurrentValue: "off",
		Options: []acpConfigOptionValue{{Value: "off"}, {Value: "on"}},
	}
	dupOn := acpConfigOption{
		ID: "allow_all", Name: "Allow All", CurrentValue: "on",
		Options: []acpConfigOptionValue{{Value: "off"}, {Value: "on"}},
	}

	// Exactly one group is surfaced (a shared key never double-lists), and its value is the
	// content-smallest ("off" < "on") regardless of the order the duplicates arrive in.
	cases := []struct {
		name  string
		order []acpConfigOption
	}{
		{"off-first", []acpConfigOption{dupOff, dupOn}},
		{"on-first", []acpConfigOption{dupOn, dupOff}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &optionState{}
			g.apply(tc.order, authoritativePayload, modeChannelUnmapped)
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
	g.apply([]acpConfigOption{dupOff, dupOn}, authoritativePayload, modeChannelUnmapped)
	valueChanged, listChanged := g.apply([]acpConfigOption{dupOn, dupOff}, authoritativePayload, modeChannelUnmapped)
	assert.False(t, valueChanged, "the duplicate winner is stable across payload orders")
	assert.False(t, listChanged, "reordering duplicates is not a list change")
}

// TestRecordOptimistic_SynthesizesGroupForKnownButUnsurfacedID guards the [E12] readback gap:
// when the server advertises an option with an empty current (known, but not surfaced as a
// group) and later accepts a write WITHOUT echoing a refreshed configOptions, recordOptimistic
// must synthesize a group from the advertised template -- so OptionGroups()/CurrentOptions()
// (and applySettingsLive's orphan reconcile) reflect the accepted value instead of dropping it.
func TestRecordOptimistic_SynthesizesGroupForKnownButUnsurfacedID(t *testing.T) {
	g := &optionState{}
	// Handshake advertises reasoning_effort with an empty current: marked known (with its
	// template) but not surfaced as a group.
	g.apply([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "",
		Options:      []acpConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, modeChannelUnmapped)
	require.Nil(t, optionids.GroupByID(g.groups, "reasoning_effort"),
		"an option advertised with an empty current is known but not yet surfaced")

	// The server accepts a write but echoes no configOptions (off-spec): recordOptimistic must
	// surface the value.
	g.recordOptimistic("reasoning_effort", "high")

	grp := optionids.GroupByID(g.groups, "reasoning_effort")
	require.NotNil(t, grp, "the accepted value is surfaced as a synthesized group")
	assert.Equal(t, "high", grp.GetCurrentValue())
	// Synthesized from the real advertised template, so it carries the full option list (low,
	// high) -- not a degenerate single-value group.
	assert.Len(t, grp.GetOptions(), 2)
	assert.Equal(t, "high", CurrentOptions(g.groups)["reasoning_effort"],
		"the readback CurrentOptions reflects the accepted value")
}

// TestRecordOptimistic_OffListValueIsSelectable guards that when the server accepts a value
// absent from the option's last-advertised list (off-spec) and echoes no configOptions, the
// synthesized group's CurrentValue still has a matching option -- otherwise the panel would
// render a current selection with no radio (buildOptionValues does not inject the current).
func TestRecordOptimistic_OffListValueIsSelectable(t *testing.T) {
	g := &optionState{}
	// Advertise reasoning_effort with options low/high (no "ultracode").
	g.apply([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", Name: "Reasoning Effort",
		CurrentValue: "",
		Options:      []acpConfigOptionValue{{Value: "low"}, {Value: "high"}},
	}}, authoritativePayload, modeChannelUnmapped)

	// The server accepts an off-list value ("ultracode") and echoes nothing.
	g.recordOptimistic("reasoning_effort", "ultracode")

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
	g := &optionState{}
	g.apply([]acpConfigOption{{
		ID: "reasoning_effort", Category: "thought_level", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}}, authoritativePayload, modeChannelUnmapped)

	assert.True(t, g.offersValue("reasoning_effort", "low"), "an offered value is pushable")
	assert.False(t, g.offersValue("reasoning_effort", "xhigh"), "a value the current list does not offer is skipped")
	assert.True(t, g.offersValue("unknown_id", "anything"),
		"an option with no advertised template is permissive so a persisted preference can be re-pushed")
}

// TestIsEffortConfigOption covers the category-then-id matching that lets the effort override
// and the strongest-first sort fire whether or not the daemon supplies the thought_level
// category -- mirroring the model/mode channels' well-known-id fallback.
func TestIsEffortConfigOption(t *testing.T) {
	assert.True(t, isEffortConfigOption(acpConfigOption{ID: "x", Category: "thought_level"}), "category match")
	assert.True(t, isEffortConfigOption(acpConfigOption{ID: OptionIDEffort}), "OpenCode/Kilo effort id (no category)")
	assert.True(t, isEffortConfigOption(acpConfigOption{ID: "reasoning_effort"}), "Copilot effort id (no category)")
	assert.True(t, isEffortConfigOption(acpConfigOption{ID: "thinking_effort"}), "Goose effort id (no category)")
	assert.False(t, isEffortConfigOption(acpConfigOption{ID: "allow_all"}), "a non-effort config option is not matched")
	assert.False(t, isEffortConfigOption(acpConfigOption{ID: "model", Category: "model"}), "the model channel is not effort")
}

// TestBuildOptionGroup_EffortSortedByKnownIDWithoutCategory guards that an effort axis is
// reordered strongest-first even when the daemon omits the thought_level category, as long as
// its id is a well-known effort id (isEffortConfigOption). Servers report effort weakest-first.
func TestBuildOptionGroup_EffortSortedByKnownIDWithoutCategory(t *testing.T) {
	grp := buildOptionGroup(acpConfigOption{
		ID: "reasoning_effort", Name: "Reasoning Effort", // no Category
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}, "high")
	var order []string
	for _, o := range grp.GetOptions() {
		order = append(order, o.GetId())
	}
	assert.Equal(t, []string{"high", "medium", "low"}, order,
		"effort options are reordered strongest-first by id even without a thought_level category")
}

// TestIsReservedOptionKey pins the invariant that only the axes owning a dedicated
// mapped option group (model, permission mode, primary agent) are reserved against
// config-option shadowing -- and, critically, that EFFORT is NOT reserved: an
// ACP provider with an effort axis surfaces it as a server-driven config option, so excluding
// the effort key would silently drop that option group.
func TestIsReservedOptionKey(t *testing.T) {
	for _, id := range []string{OptionIDModel, OptionIDPermissionMode, OptionIDPrimaryAgent} {
		assert.True(t, isReservedOptionKey(id), "%q owns a dedicated mapped group and must be reserved", id)
	}
	for _, id := range []string{OptionIDEffort, "reasoning_effort", "allow_all", "thought_level", ""} {
		assert.False(t, isReservedOptionKey(id), "%q is an option (or effort) key and must not be reserved", id)
	}
}

// TestModelInfoEqual_HiddenFlagRegisters guards that a Hidden-only difference is
// treated as a genuine catalog change, so the "idempotent re-report vs real
// change" check can't silently miss a picker-visibility flip.
func TestModelInfoEqual_HiddenFlagRegisters(t *testing.T) {
	base := &ModelInfo{Id: "m", DisplayName: "M", DefaultEffort: "high"}
	hidden := &ModelInfo{Id: "m", DisplayName: "M", DefaultEffort: "high", Hidden: true}

	assert.True(t, base.equal(&ModelInfo{Id: "m", DisplayName: "M", DefaultEffort: "high"}),
		"identical entries are equal")
	assert.False(t, base.equal(hidden), "a Hidden-flag difference must register as a change")
	assert.False(t, hidden.equal(base), "equality is symmetric for the Hidden flag")
}

// TestLiveGroup_DefaultsEmptyCurrentToTemplateDefault verifies the overlay helper
// reads order/default from the template and falls back to the default when the
// caller supplies no current value (so a group whose current wasn't wired still
// renders a valid, in-list selection rather than a blank one).
func TestLiveGroup_DefaultsEmptyCurrentToTemplateDefault(t *testing.T) {
	tmpl := &leapmuxv1.AvailableOptionGroup{
		Id:           "x",
		Label:        "X",
		DefaultValue: "b",
		Order:        OptionOrderProviderFirst,
		Options:      []*leapmuxv1.AvailableOption{{Id: "a"}, {Id: "b"}},
	}

	withCurrent := liveGroup(tmpl, "a")
	assert.Equal(t, "a", withCurrent.GetCurrentValue())
	assert.Equal(t, OptionOrderProviderFirst, withCurrent.GetOrder(), "order comes from the template")

	empty := liveGroup(tmpl, "")
	assert.Equal(t, "b", empty.GetCurrentValue(), "an empty current falls back to the template default")
}

// TestLiveGroup_HonorsTemplateMutable verifies liveGroup carries the template's Mutable
// flag through instead of forcing every projected group editable -- so a provider can
// project an agent-controlled, read-only axis through liveGroup.
func TestLiveGroup_HonorsTemplateMutable(t *testing.T) {
	mutable := liveGroup(&leapmuxv1.AvailableOptionGroup{Id: "x", DefaultValue: "a", Mutable: true, Options: []*leapmuxv1.AvailableOption{{Id: "a"}}}, "a")
	assert.True(t, mutable.GetMutable(), "a mutable template projects a mutable group")

	readOnly := liveGroup(&leapmuxv1.AvailableOptionGroup{Id: "x", DefaultValue: "a", Mutable: false, Options: []*leapmuxv1.AvailableOption{{Id: "a"}}}, "a")
	assert.False(t, readOnly.GetMutable(), "a read-only template projects a read-only group")
}

// TestFilterGroupOptions_PreservesTemplateFields verifies filterGroupOptions narrows the
// option list while carrying every other template field (id/label/current/default/mutable/
// order) through unchanged and without mutating the shared template -- the field-preservation
// contract the shared clone helper (cloneOptionGroupTemplate) exists to keep mechanical as
// AvailableOptionGroup grows fields.
func TestFilterGroupOptions_PreservesTemplateFields(t *testing.T) {
	tmpl := &leapmuxv1.AvailableOptionGroup{
		Id:           "perm",
		Label:        "Permission",
		CurrentValue: "plan",
		DefaultValue: "default",
		Mutable:      true,
		Order:        OptionOrderPermissionMode,
		Options:      []*leapmuxv1.AvailableOption{{Id: "auto"}, {Id: "plan"}, {Id: "default"}},
	}

	filtered := filterGroupOptions(tmpl, func(o *leapmuxv1.AvailableOption) bool { return o.GetId() != "auto" })

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
	assert.Equal(t, OptionOrderPermissionMode, filtered.GetOrder(), "order carried from the template")

	assert.Len(t, tmpl.GetOptions(), 3, "filtering must not mutate the shared template")
	assert.Nil(t, filterGroupOptions(nil, func(*leapmuxv1.AvailableOption) bool { return true }))
}

// TestCodexStaticOptionGroups_CarryOrder guards the regression where the Codex
// static-fallback templates omitted Order (0), which sorts a provider group ahead
// of the model group (order 10) in the frontend's order-based layout.
func TestCodexStaticOptionGroups_CarryOrder(t *testing.T) {
	groups := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	require.NotEmpty(t, groups)
	for _, g := range groups {
		assert.NotZero(t, g.GetOrder(),
			"registered Codex group %q must carry a non-zero display order so the static fallback can't sort it ahead of the model group", g.GetId())
		assert.Greater(t, g.GetOrder(), OptionOrderModel,
			"registered Codex group %q must sort after the model group", g.GetId())
	}
}

// TestCodexStaticOptionGroups_DefaultMatchesSeed guards that each Codex axis's picker
// default badge (the static group's DefaultValue) agrees with the value seeded into a fresh
// agent's launch options (codexOptionDefaults). The two are stamped from independent
// constants of equal value, so this catches a future edit that changes one without the other
// -- which would launch the agent on one value while the popover badges a different default.
func TestCodexStaticOptionGroups_DefaultMatchesSeed(t *testing.T) {
	seeds := codexOptionDefaults()
	require.NotEmpty(t, seeds)
	groups := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	require.NotEmpty(t, groups)
	matched := 0
	for _, g := range groups {
		seed, ok := seeds[g.GetId()]
		if !ok {
			continue // model/effort/permission axes are not seeded via codexOptionDefaults
		}
		matched++
		assert.Equal(t, seed, g.GetDefaultValue(),
			"axis %q: static group default must match the launch seed", g.GetId())
	}
	assert.Equal(t, len(seeds), matched, "every seeded axis has a matching static group")
}

// TestStaticOptionGroupsForProvider_CodexOrdersAfterModel verifies the assembled
// static fallback never places a non-model group ahead of the model group.
func TestStaticOptionGroupsForProvider_CodexOrdersAfterModel(t *testing.T) {
	groups := staticOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "")
	require.NotEmpty(t, groups)
	var sawModel bool
	for _, g := range groups {
		if g.GetId() == OptionIDModel {
			sawModel = true
			continue
		}
		assert.GreaterOrEqual(t, g.GetOrder(), OptionOrderModel,
			"static-fallback group %q must not sort before the model group", g.GetId())
	}
	assert.True(t, sawModel, "Codex static fallback includes a model group from its default catalog")
}

// TestModelAndEffortGroups_EffortLabel verifies the effort axis is labeled per the
// caller: Pi calls it "Thinking Level" (its CLI's set_thinking_level concept), Codex
// "Effort". The label rides on both the top-level group and each model's sub_groups so
// a model switch stays consistently named.
func TestModelAndEffortGroups_EffortLabel(t *testing.T) {
	models := []*ModelInfo{{
		Id:               "m1",
		SupportedEfforts: []*EffortInfo{{Id: "low", Name: "Low"}, {Id: "high", Name: "High"}},
		DefaultEffort:    "high",
	}}

	pi := modelAndEffortGroups(models, "m1", "high", PiThinkingLevelLabel, nil)
	piEffort := optionids.GroupByID(pi, OptionIDEffort)
	require.NotNil(t, piEffort)
	assert.Equal(t, PiThinkingLevelLabel, piEffort.GetLabel(), "Pi labels its effort axis 'Thinking Level'")

	// The per-model sub_groups carry the same label (used by the model-switch swap).
	piModel := optionids.GroupByID(pi, OptionIDModel)
	require.NotNil(t, piModel)
	require.NotEmpty(t, piModel.GetOptions())
	piSubEffort := optionids.GroupByID(piModel.GetOptions()[0].GetSubGroups(), OptionIDEffort)
	require.NotNil(t, piSubEffort)
	assert.Equal(t, PiThinkingLevelLabel, piSubEffort.GetLabel(), "the model-switch sub_group label matches")

	codex := modelAndEffortGroups(models, "m1", "high", EffortGroupLabel, nil)
	codexEffort := optionids.GroupByID(codex, OptionIDEffort)
	require.NotNil(t, codexEffort)
	assert.Equal(t, "Effort", codexEffort.GetLabel(), "Codex keeps the default 'Effort' label")
}

// TestPiStaticOptionGroups_ThinkingLevelLabel verifies the not-running static fallback
// for Pi (built from the registered modelSubGroups) also labels the thinking-level
// group "Thinking Level", so the popover stays consistent while a Pi agent restarts.
func TestPiStaticOptionGroups_ThinkingLevelLabel(t *testing.T) {
	groups := staticOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, PiDefaultModel)
	eg := optionids.GroupByID(groups, OptionIDEffort)
	require.NotNil(t, eg, "Pi's static fallback surfaces a thinking-level group for its default model")
	assert.Equal(t, PiThinkingLevelLabel, eg.GetLabel())
}

// TestModelInfoClone_DeepCopiesSupportedEfforts verifies clone() does not alias the
// shared static catalog's effort slice: mutating a clone's efforts must not reach the
// original (a shallow copy would corrupt every model sharing that effort list).
func TestModelInfoClone_DeepCopiesSupportedEfforts(t *testing.T) {
	orig := &ModelInfo{
		Id:               "m1",
		SupportedEfforts: []*EffortInfo{{Id: "low", Name: "Low"}, {Id: "high", Name: "High"}},
	}
	c := orig.clone()
	c.SupportedEfforts[0].Name = "MUTATED"
	c.SupportedEfforts = append(c.SupportedEfforts, &EffortInfo{Id: "max"})

	assert.Equal(t, "Low", orig.SupportedEfforts[0].Name, "the original effort element must be untouched")
	assert.Len(t, orig.SupportedEfforts, 2, "the original effort slice length must be untouched")
}

// TestReadOnlyModelAndEffortGroups verifies the hidden-UI read-only projection: the model
// group surfaces the HUMANIZED display name (not the raw id) while keeping the raw id as
// the option value, the groups are non-mutable, and a concrete effort is surfaced while an
// auto/empty effort is suppressed.
func TestReadOnlyModelAndEffortGroups(t *testing.T) {
	groups := readOnlyModelAndEffortGroups("opus[1m]", "Opus (1M context)", "high")
	mg := optionids.GroupByID(groups, OptionIDModel)
	require.NotNil(t, mg)
	require.Len(t, mg.GetOptions(), 1)
	assert.Equal(t, "opus[1m]", mg.GetOptions()[0].GetId(), "the raw id stays as the option value")
	assert.Equal(t, "Opus (1M context)", mg.GetOptions()[0].GetName(),
		"the humanized display name is surfaced, not the raw bracketed id")
	assert.False(t, mg.GetMutable(), "the read-only model group is non-mutable")
	assert.NotNil(t, optionids.GroupByID(groups, OptionIDEffort), "a concrete effort is surfaced")

	// An auto/empty effort is suppressed (only a concrete effort is surfaced).
	autoGroups := readOnlyModelAndEffortGroups("opus[1m]", "Opus", EffortAuto)
	assert.Nil(t, optionids.GroupByID(autoGroups, OptionIDEffort), "an auto effort is not surfaced read-only")
	assert.Nil(t, optionids.GroupByID(readOnlyModelAndEffortGroups("opus[1m]", "Opus", ""), OptionIDEffort),
		"an empty effort is not surfaced read-only")

	// modelThenEffort omits an absent group rather than emitting a nil entry: an empty model with a
	// concrete effort yields the effort group ALONE (no model group, no panic), and both absent
	// yields an empty slice.
	effortOnly := readOnlyModelAndEffortGroups("", "", "high")
	assert.Nil(t, optionids.GroupByID(effortOnly, OptionIDModel), "an empty model is not surfaced")
	require.NotNil(t, optionids.GroupByID(effortOnly, OptionIDEffort), "the concrete effort still surfaces alone")
	assert.Len(t, effortOnly, 1, "only the effort group is present")
	assert.Empty(t, readOnlyModelAndEffortGroups("", "", ""), "absent model and effort yield no groups")
}
