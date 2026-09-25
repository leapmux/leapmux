package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// trySetStartupModel applies a requested model best-effort: it pushes when the
// request differs from the server's current (including when the server reports
// none), is a no-op when they match, and keeps the current model on rejection.
// The writer is resolved via effectiveSetModel, so the tests inject through
// modelSetter (the override effectiveSetModel prefers).

func TestTrySetStartupModel_PushesWhenServerReportsNoModel(t *testing.T) {
	t.Parallel()

	var base Base
	got := ""
	base.hooks.ModelSetter = func(m string) error {
		got = m
		base.model = m
		return nil
	}
	base.trySetStartupModel("user/arbitrary")
	assert.Equal(t, "user/arbitrary", got, "an arbitrary model must be pushed when the server advertises none")
	assert.Equal(t, "user/arbitrary", base.model)
}

func TestTrySetStartupModel_NoopWhenMatchesCurrent(t *testing.T) {
	t.Parallel()

	base := Base{}
	base.model = "anthropic/claude-sonnet-4"
	called := false
	base.hooks.ModelSetter = func(string) error {
		called = true
		return nil
	}
	base.trySetStartupModel("anthropic/claude-sonnet-4")
	assert.False(t, called, "no setModel when the request already matches the server's current")
}

// An agent that reports some options only after a model write (Kiro's effort
// axis) gets the write for the model that the session already runs.
func TestTrySetStartupModel_ModelWriteRevealsOptionsWritesTheSessionModel(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		requested string
	}{
		{name: "the session model requested", requested: "kiro/current"},
		{name: "no model requested", requested: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			base := Base{}
			base.model = "kiro/current"
			base.hooks.ModelWriteRevealsOptions = true
			var written []string
			base.hooks.ModelSetter = func(m string) error {
				written = append(written, m)
				return nil
			}
			base.trySetStartupModel(tc.requested)
			assert.Equal(t, []string{"kiro/current"}, written)
		})
	}
}

func TestTrySetStartupModel_ModelWriteRevealsOptionsWritesADifferentModelOnce(t *testing.T) {
	t.Parallel()

	base := Base{}
	base.model = "kiro/current"
	base.hooks.ModelWriteRevealsOptions = true
	var written []string
	base.hooks.ModelSetter = func(m string) error {
		written = append(written, m)
		base.model = m
		return nil
	}
	base.trySetStartupModel("kiro/other")
	assert.Equal(t, []string{"kiro/other"}, written)
	assert.Equal(t, "kiro/other", base.model)
}

func TestTrySetStartupModel_ModelWriteRevealsOptionsNeedsAModel(t *testing.T) {
	t.Parallel()

	base := Base{}
	base.hooks.ModelWriteRevealsOptions = true
	called := false
	base.hooks.ModelSetter = func(string) error {
		called = true
		return nil
	}
	base.trySetStartupModel("")
	assert.False(t, called, "a session that reports no model and a launch that requests none give nothing to write")
}

func TestTrySetStartupModel_NoopWhenNothingIsRequested(t *testing.T) {
	t.Parallel()

	base := Base{}
	base.model = "server/current"
	called := false
	base.hooks.ModelSetter = func(string) error {
		called = true
		return nil
	}
	base.trySetStartupModel("")
	assert.False(t, called, "without the hook, an empty request keeps the session model without a write")
}

func TestTrySetStartupModel_NonFatalOnRejection(t *testing.T) {
	t.Parallel()

	base := Base{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{ProviderName: "opencode"})}}
	base.model = "server/current"
	base.hooks.ModelSetter = func(string) error {
		return errors.New("unknown model")
	}
	base.trySetStartupModel("user/arbitrary")
	// Rejection is swallowed; the server's current model is kept so the session
	// stays usable.
	assert.Equal(t, "server/current", base.model)
}

// ACP servers report models through one of two channels: the SessionModelState
// `models` field, or a `model` select inside `configOptions`. acpHandshakeModelInfos
// merges both so every provider -- including ones we have not special-cased --
// surfaces its model list regardless of which channel the server uses.

func TestACPHandshakeModelInfos_UnionsBothChannels(t *testing.T) {
	t.Parallel()

	handshake := &SessionResult{
		CurrentModelID: "m1",
		Models:         []ModelInfo{{ModelID: "m1", Name: "Model 1"}, {ModelID: "m2", Name: "Model 2"}},
		ConfigOptions: []ConfigOption{{
			ID: ConfigOptionIDModel, CurrentValue: "cfg",
			Options: []ConfigOptionValue{
				{Value: "m2", Name: "Model 2 (dup)"}, // already in the models field -> deduped
				{Value: "cfg", Name: "Config-only"},
			},
		}},
	}

	models, current := acpHandshakeModelInfos(handshake)

	// models-field current wins; the config-only model is appended, the dup dropped.
	require.Equal(t, "m1", current)
	require.Len(t, models, 3)
	assert.Equal(t, "m1", models[0].ModelID)
	assert.Equal(t, "m2", models[1].ModelID)
	assert.Equal(t, "Model 2", models[1].Name) // models-field metadata kept over the dup
	assert.Equal(t, "cfg", models[2].ModelID)
}

func TestACPHandshakeModelInfos_FallsBackToConfigOptions(t *testing.T) {
	t.Parallel()

	// This is the OpenCode/Kilo shape: no `models` field, models in configOptions.
	handshake := &SessionResult{
		ConfigOptions: []ConfigOption{
			{ID: ConfigOptionIDMode, CurrentValue: "build", Options: []ConfigOptionValue{{Value: "build", Name: "Build"}}},
			{ID: ConfigOptionIDModel, CurrentValue: "anthropic/claude-sonnet-4", Options: []ConfigOptionValue{
				{Value: "anthropic/claude-sonnet-4", Name: "Claude Sonnet 4"},
				{Value: "openai/gpt-5", Name: "GPT-5"},
			}},
		},
	}

	models, current := acpHandshakeModelInfos(handshake)

	require.Equal(t, "anthropic/claude-sonnet-4", current)
	require.Len(t, models, 2)
	assert.Equal(t, "anthropic/claude-sonnet-4", models[0].ModelID)
	assert.Equal(t, "Claude Sonnet 4", models[0].Name)
	assert.Equal(t, "openai/gpt-5", models[1].ModelID)
}

func TestACPHandshakeModelInfos_NoModels(t *testing.T) {
	t.Parallel()

	handshake := &SessionResult{
		ConfigOptions: []ConfigOption{{ID: ConfigOptionIDMode, CurrentValue: "build"}},
	}

	models, current := acpHandshakeModelInfos(handshake)

	require.Empty(t, models)
	require.Equal(t, "", current)
}

// A server that repeats a model id within a single channel yields one entry.
func TestBuildACPModels_DedupsRepeatedID(t *testing.T) {
	t.Parallel()

	models := buildACPModels([]ModelInfo{
		{ModelID: "m1", Name: "Model 1"},
		{ModelID: "m1", Name: "Model 1 (dup)"},
	}, "m1", nil)

	require.Len(t, models, 1)
	assert.Equal(t, "Model 1", models[0].DisplayName)
}

func TestACPModelInfosFromConfigOption_SkipsEmptyValues(t *testing.T) {
	t.Parallel()

	option := ConfigOption{
		ID:           ConfigOptionIDModel,
		CurrentValue: "openai/gpt-5",
		Options: []ConfigOptionValue{
			{Value: "", Name: "Ignored"},
			{Value: "openai/gpt-5", Name: "GPT-5"},
		},
	}

	infos, current := acpModelInfosFromConfigOption(option)

	require.Equal(t, "openai/gpt-5", current)
	require.Len(t, infos, 1)
	assert.Equal(t, "openai/gpt-5", infos[0].ModelID)
}

// applyHandshakeMode reads the permission mode from the modes channel, lets a
// `mode` config option override it for a provider that consumes it, and falls back
// to the provided default.

func handshakeWithConfigModeOverride() *SessionResult {
	return &SessionResult{
		CurrentModeID: "agent",
		Modes:         []ModeInfo{{ID: "agent", Name: "Agent"}, {ID: "plan", Name: "Plan"}},
		ConfigOptions: []ConfigOption{{
			ID: ConfigOptionIDMode, CurrentValue: "plan",
			Options: []ConfigOptionValue{{Value: "agent", Name: "Agent"}, {Value: "plan", Name: "Plan"}},
		}},
	}
}

// A provider that consumes the configOptions `mode` (ModeChannelPermissionMode --
// Copilot/Goose/Cursor) applies the override at handshake, matching the runtime and
// ClearContext paths.
func TestApplyHandshakeMode_ConfigOptionOverridesModesChannel(t *testing.T) {
	t.Parallel()

	base := Base{hooks: Hooks{ModeChannel: ModeChannelPermissionMode}}

	base.applyHandshakeMode(handshakeWithConfigModeOverride(), "fallback")

	// The config option's "plan" overrides the modes-channel "agent".
	assert.Equal(t, "plan", base.permissionMode)
	require.Len(t, base.availableModes, 2)
}

// A provider with an unmapped mode channel does NOT apply the configOptions
// `mode` override at handshake -- it keeps the modes-channel value and leaves the
// option to be surfaced as a mutable option group, so the handshake resolves the mode the same way the
// runtime and ClearContext paths do (which also gate the override on the mode channel)
// rather than applying it as the permission mode here but surfacing it uniformly everywhere else.
func TestApplyHandshakeMode_UnmappedProviderKeepsModesChannelValue(t *testing.T) {
	t.Parallel()

	var base Base // ModeChannelUnmapped

	base.applyHandshakeMode(handshakeWithConfigModeOverride(), "fallback")

	// The modes-channel "agent" wins; the configOptions "plan" is not applied.
	assert.Equal(t, "agent", base.permissionMode)
	require.Len(t, base.availableModes, 2)
}

func TestApplyHandshakeMode_FallsBackToDefaultWhenServerReportsNone(t *testing.T) {
	t.Parallel()

	var base Base
	base.applyHandshakeMode(&SessionResult{}, "default-mode")
	assert.Equal(t, "default-mode", base.permissionMode)
}

// S1 end-to-end: a provider with an unmapped mode channel whose handshake
// carries BOTH a modes channel and a configOptions `mode` must (a) keep the
// modes-channel permission mode -- the configOptions override is NOT applied writably --
// and (b) surface the configOptions `mode` as a mutable option group rather than
// dropping it. This is the seam the S1 gate protects: applyHandshakeModels surfaces the
// generic and the gated applyHandshakeMode leaves the mode on the modes channel, so the
// option is neither double-applied (as the permission mode AND as a option group) nor
// silently dropped, matching how the runtime and ClearContext paths resolve it.
func TestUnmappedProvider_HandshakeConfigMode_SurfacedGenericNotDoubleApplied(t *testing.T) {
	t.Parallel()

	var base Base // ModeChannelUnmapped
	handshake := &SessionResult{
		CurrentModeID: "default",
		Modes:         []ModeInfo{{ID: "default", Name: "Default"}, {ID: "plan", Name: "Plan"}},
		// The modes channel says "default" but the configOptions `mode` says "plan".
		ConfigOptions: []ConfigOption{{
			ID: ConfigOptionIDMode, CurrentValue: "plan",
			Options: []ConfigOptionValue{{Value: "default", Name: "Default"}, {Value: "plan", Name: "Plan"}},
		}},
	}

	base.applyHandshakeModels(handshake) // surfaces the unmapped `mode` as a option group
	base.applyHandshakeMode(handshake, "fallback")

	// (a) The permission mode stays on the modes channel; the configOptions "plan" override
	// is gated out for the unmapped provider.
	assert.Equal(t, "default", base.permissionMode)
	// (b) The configOptions `mode` is surfaced as a mutable option group, keyed by its id and carrying its
	// current value -- not dropped, not folded into the writable permission mode.
	require.Len(t, base.options.groups, 1)
	assert.Equal(t, ConfigOptionIDMode, base.options.groups[0].GetId())
	assert.Equal(t, "plan", base.options.values[ConfigOptionIDMode])
}

// The base dispatcher handles a config_option_update model change for ANY ACP
// provider with no per-provider wiring. OpenCode and Kilo register no config
// option handler at all, yet their model list and current model stay in sync --
// and the `mode` option (their primary agent) is left untouched.

// A runtime config_option_update carries only the configOptions `model` select.
// Models reported only through the SessionModelState `models` field at handshake
// must survive it -- applyConfigOptionModelsLocked re-unions the remembered
// models-field catalog so a split-catalog provider does not lose entries.
func TestApplyConfigOptionModelsLocked_ReunionsModelsFieldCatalog(t *testing.T) {
	t.Parallel()

	var base Base
	// Simulate a handshake that reported "field/x" only through the models field.
	base.modelsFieldInfos = []ModelInfo{{ModelID: "field/x", Name: "Field X"}}

	options := []ConfigOption{{
		ID: ConfigOptionIDModel, CurrentValue: "cfg/a",
		Options: []ConfigOptionValue{
			{Value: "cfg/a", Name: "Cfg A"},
			{Value: "cfg/b", Name: "Cfg B"},
		},
	}}
	base.Mu.Lock()
	modelChanged, listChanged := base.applyConfigOptionModelsLocked(options)
	base.Mu.Unlock()

	assert.True(t, modelChanged)
	assert.True(t, listChanged)
	require.Len(t, base.availableModels, 3)
	// models-field entry first, then the config-option models.
	assert.Equal(t, "field/x", base.availableModels[0].GetId())
	assert.Equal(t, "cfg/a", base.availableModels[1].GetId())
	assert.Equal(t, "cfg/b", base.availableModels[2].GetId())
	assert.True(t, base.availableModels[1].IsDefault) // current comes from the config option
	assert.Equal(t, "cfg/a", base.model)
}

// primaryAgentOptions returns nil (not an empty map) for an empty agent so a
// settings refresh preserves stored extras instead of clearing them; a
// non-empty agent yields the single-key map.
func TestPrimaryAgentExtras(t *testing.T) {
	t.Parallel()

	assert.Nil(t, primaryAgentOptions(""),
		"empty agent must yield nil so PersistSettingsRefresh keeps stored extras")
	assert.Equal(t, map[string]string{agent.OptionIDPrimaryAgent: "build"}, primaryAgentOptions("build"))
}

// --- reconcileCurrentOptionID: the shared current-selection resolver ---

// reconcileCurrentOptionID resolves a secondary channel's current against a freshly built
// option list: an empty list means "unreported" so the reported (else stored) value is
// trusted unchanged; otherwise a valid reported value is adopted, a still-valid stored
// value is kept, and failing both the current re-seeds to the list's first option (ACP
// options carry no per-option default badge). The handshake, runtime, and ClearContext
// seams all route through it.
func TestReconcileCurrentOptionID(t *testing.T) {
	t.Parallel()

	opts := func(ids ...string) []*leapmuxv1.AvailableOption {
		built := make([]*leapmuxv1.AvailableOption, 0, len(ids))
		for _, id := range ids {
			built = append(built, &leapmuxv1.AvailableOption{Id: id})
		}
		return built
	}

	cases := []struct {
		name      string
		available []*leapmuxv1.AvailableOption
		reported  string
		stored    string
		want      string
	}{
		// Empty list == "unreported": trust the reported value, else the stored one,
		// mirroring acpSetMode's len(available)>0 guard (so an existing ClearContext
		// test that sends currentModeId with no availableModes keeps adopting it).
		{"empty list trusts reported", nil, "plan", "build", "plan"},
		{"empty list falls back to stored", nil, "", "build", "build"},
		{"empty list, nothing reported or stored", nil, "", "", ""},
		// Non-empty list: adopt a valid reported value over a different stored one.
		{"adopts valid reported over stored", opts("build", "plan"), "plan", "build", "plan"},
		// Reported absent but stored still valid: keep the stored selection (the S2/S1
		// "reject a reported value the list lacks" branch) instead of adopting a phantom.
		{"keeps stored when reported absent", opts("build", "plan"), "ghost", "plan", "plan"},
		// Reported empty and stored still valid: keep the stored selection.
		{"keeps stored when reported empty", opts("build", "plan"), "", "build", "build"},
		// Both reported and stored absent: re-seed to the first non-empty option (there
		// is no per-option default badge anymore).
		{"re-seeds to first when both absent", opts("build", "plan", "review"), "ghost", "stale", "build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, reconcileCurrentOptionID(tc.available, tc.reported, tc.stored))
		})
	}
}

// defaultOrFirstOption returns the first non-empty id, else "" -- skipping nil
// entries and empty ids. ACP options carry no per-option default badge, so "first"
// is the only sensible seed.
func TestDefaultOrFirstOption(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", defaultOrFirstOption(nil), "no options -> empty")
	assert.Equal(t, "build", defaultOrFirstOption([]*leapmuxv1.AvailableOption{
		{Id: "build"},
		{Id: "plan"},
	}), "first non-empty id")
	assert.Equal(t, "plan", defaultOrFirstOption([]*leapmuxv1.AvailableOption{
		nil,
		{Id: ""},
		{Id: "plan"},
	}), "skips nil entries and empty ids when picking the first")
}

// --- Config-option dispatch by spec `category` (with id fallback) ---

// acpConfigOptionByCategory prefers the spec's `category` signal; the well-known id
// is only a back-compat fallback for the providers we ship today, which omit it.

func TestACPConfigOptionByCategory_PrefersCategoryOverID(t *testing.T) {
	t.Parallel()

	options := []ConfigOption{
		{ID: "opaque", Category: acpConfigOptionCategoryModel, CurrentValue: "a"},
		{ID: ConfigOptionIDModel, CurrentValue: "b"}, // a coincidental id match
	}
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	require.True(t, ok)
	assert.Equal(t, "opaque", option.ID, "the category match wins over the id fallback")
}

func TestACPConfigOptionByCategory_FallsBackToIDWhenNoCategory(t *testing.T) {
	t.Parallel()

	// The shape every provider ships today: no `category`, well-known id.
	options := []ConfigOption{{ID: ConfigOptionIDMode, CurrentValue: "plan"}}
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryMode, ConfigOptionIDMode)
	require.True(t, ok)
	assert.Equal(t, ConfigOptionIDMode, option.ID)
}

func TestACPConfigOptionByCategory_SkipsNonSelectableType(t *testing.T) {
	t.Parallel()

	options := []ConfigOption{
		{ID: "x", Category: acpConfigOptionCategoryModel, Type: "text"}, // right category, unknown type
		{ID: ConfigOptionIDModel, Type: "select", CurrentValue: "m1"},   // selectable id fallback
	}
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	require.True(t, ok)
	assert.Equal(t, ConfigOptionIDModel, option.ID,
		"a non-selectable category match is skipped; the selectable id fallback wins")
}

func TestACPConfigOptionByCategory_NoneFound(t *testing.T) {
	t.Parallel()

	_, ok := acpConfigOptionByCategory([]ConfigOption{{ID: "other"}}, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	assert.False(t, ok)
}

// TestACPConfigOptionByCategory_DeterministicOnDuplicateCategory guards S2: a (pathological)
// daemon reporting two same-category options must resolve to the LOWEST id, deterministically,
// rather than to whichever the server happened to list first -- so the claimed axis can't flip
// between refreshes with server slice order.
func TestACPConfigOptionByCategory_DeterministicOnDuplicateCategory(t *testing.T) {
	t.Parallel()

	// Same two options in opposite server-reported orders must pick the same (lowest-id) winner.
	a := ConfigOption{ID: "bbb", Category: acpConfigOptionCategoryModel, CurrentValue: "1"}
	b := ConfigOption{ID: "aaa", Category: acpConfigOptionCategoryModel, CurrentValue: "2"}

	got1, ok1 := acpConfigOptionByCategory([]ConfigOption{a, b}, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	got2, ok2 := acpConfigOptionByCategory([]ConfigOption{b, a}, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	require.True(t, ok1)
	require.True(t, ok2)
	assert.Equal(t, "aaa", got1.ID, "the lowest id among same-category matches wins")
	assert.Equal(t, got1.ID, got2.ID, "the winner does not depend on server-reported slice order")
}

// TestACPConfigOptionByCategory_DeterministicOnDuplicateFallbackID guards S4: a (pathological) daemon
// reporting the well-known fallback id TWICE with NO category must resolve to a STABLE winner (the
// content-smallest, acpConfigOptionContentLess) rather than whichever the server listed first -- so
// the claimed axis can't flip between refreshes with server slice order, mirroring the category pass.
func TestACPConfigOptionByCategory_DeterministicOnDuplicateFallbackID(t *testing.T) {
	t.Parallel()

	// Two options share the fallback id but differ in their current value; neither carries a category.
	a := ConfigOption{ID: ConfigOptionIDModel, CurrentValue: "zzz"}
	b := ConfigOption{ID: ConfigOptionIDModel, CurrentValue: "aaa"}

	got1, ok1 := acpConfigOptionByCategory([]ConfigOption{a, b}, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	got2, ok2 := acpConfigOptionByCategory([]ConfigOption{b, a}, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	require.True(t, ok1)
	require.True(t, ok2)
	assert.Equal(t, "aaa", got1.CurrentValue, "the content-smallest duplicate (lowest current value) wins")
	assert.Equal(t, got1.CurrentValue, got2.CurrentValue, "the winner does not depend on server-reported slice order")
}

// TestACPConfigOptionByCategory_DeterministicOnSameCategorySameID covers the category-pass tie-break:
// a (doubly-pathological) daemon reporting two options that share BOTH category AND id must resolve to
// the content-smallest occurrence (acpConfigOptionContentLess), not whichever the server listed first.
// The plain lowest-id comparison can't break an exact-id tie, so without the content tie-break the
// claimed axis would still flip with server slice order in this case.
func TestACPConfigOptionByCategory_DeterministicOnSameCategorySameID(t *testing.T) {
	t.Parallel()

	a := ConfigOption{ID: ConfigOptionIDModel, Category: acpConfigOptionCategoryModel, CurrentValue: "zzz"}
	b := ConfigOption{ID: ConfigOptionIDModel, Category: acpConfigOptionCategoryModel, CurrentValue: "aaa"}

	got1, ok1 := acpConfigOptionByCategory([]ConfigOption{a, b}, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	got2, ok2 := acpConfigOptionByCategory([]ConfigOption{b, a}, acpConfigOptionCategoryModel, ConfigOptionIDModel)
	require.True(t, ok1)
	require.True(t, ok2)
	assert.Equal(t, "aaa", got1.CurrentValue, "the content-smallest same-category, same-id duplicate wins")
	assert.Equal(t, got1.CurrentValue, got2.CurrentValue, "the winner does not depend on server-reported slice order")
}

func TestIsSelectableConfigOption(t *testing.T) {
	t.Parallel()

	assert.True(t, isSelectableConfigOption(ConfigOption{Type: ""}), "empty type is treated as select")
	assert.True(t, isSelectableConfigOption(ConfigOption{Type: "select"}))
	assert.False(t, isSelectableConfigOption(ConfigOption{Type: "text"}))
	assert.False(t, isSelectableConfigOption(ConfigOption{Type: "checkbox"}))
}

// The model channel dispatches by `category`, so a spec-compliant agent using a
// non-literal opaque id still surfaces its models.
func TestACPHandshakeModelInfos_DispatchesModelByCategory(t *testing.T) {
	t.Parallel()

	handshake := &SessionResult{
		ConfigOptions: []ConfigOption{{
			ID:           "opaque-model-id", // NOT the literal "model"
			Category:     acpConfigOptionCategoryModel,
			CurrentValue: "openai/gpt-5",
			Options: []ConfigOptionValue{
				{Value: "openai/gpt-5", Name: "GPT-5"},
				{Value: "anthropic/claude-sonnet-4", Name: "Claude Sonnet 4"},
			},
		}},
	}

	models, current := acpHandshakeModelInfos(handshake)

	require.Equal(t, "openai/gpt-5", current)
	require.Len(t, models, 2)
	assert.Equal(t, "openai/gpt-5", models[0].ModelID)
}

// An option with the literal `model` id but an unknown (non-select) type is ignored
// defensively rather than parsed as the model channel.
func TestACPHandshakeModelInfos_IgnoresNonSelectableModelOption(t *testing.T) {
	t.Parallel()

	handshake := &SessionResult{
		ConfigOptions: []ConfigOption{{
			ID:      ConfigOptionIDModel,
			Type:    "text",
			Options: []ConfigOptionValue{{Value: "should/be/ignored"}},
		}},
	}

	models, current := acpHandshakeModelInfos(handshake)

	assert.Empty(t, models)
	assert.Equal(t, "", current)
}

// The mode channel dispatches by `category` too (covering both the permission-mode
// and primary-agent sync paths, which share buildConfigOptionSelect).
func TestBuildConfigOptionSelect_DispatchesModeByCategory(t *testing.T) {
	t.Parallel()

	options := []ConfigOption{{
		ID:           "opaque-mode-id",
		Category:     acpConfigOptionCategoryMode,
		CurrentValue: "plan",
		Options:      []ConfigOptionValue{{Value: "build", Name: "Build"}, {Value: "plan", Name: "Plan"}},
	}}

	built, current, ok := buildConfigOptionSelect(options, nil)

	require.True(t, ok)
	assert.Equal(t, "plan", current)
	require.Len(t, built, 2)
}

func TestBuildConfigOptionSelect_IgnoresNonSelectableModeOption(t *testing.T) {
	t.Parallel()

	options := []ConfigOption{{ID: ConfigOptionIDMode, Type: "text"}}
	_, _, ok := buildConfigOptionSelect(options, nil)
	assert.False(t, ok, "an unknown widget type is not dispatched as the mode channel")
}

// --- Surfacing of unmapped config options (mutable) ---

// A handshake carrying a third axis (thought_level) surfaces it as a mutable
// option group keyed by id, while the claimed model and mode options are excluded
// (no double-render).
func TestApplyOptionGroupsLocked_SurfacesUnmappedOption(t *testing.T) {
	t.Parallel()

	// A primary-agent provider consumes the mode channel, so its mode option is claimed
	// and excluded -- only the unmapped axis surfaces.
	base := Base{hooks: Hooks{ModeChannel: ModeChannelPrimaryAgent}}
	options := []ConfigOption{
		{ID: ConfigOptionIDMode, CurrentValue: "build", Options: []ConfigOptionValue{{Value: "build"}, {Value: "plan"}}},
		{ID: ConfigOptionIDModel, CurrentValue: "m1", Options: []ConfigOptionValue{{Value: "m1"}}},
		{ID: "thoughtLevel", Category: "thought_level", Name: "Thought Level", CurrentValue: "high",
			Options: []ConfigOptionValue{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}}},
	}

	base.Mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.Mu.Unlock()

	assert.True(t, valueChanged)
	assert.True(t, listChanged)
	require.Len(t, base.options.groups, 1, "only the unmapped option surfaces; model and mode are excluded")
	group := base.options.groups[0]
	assert.Equal(t, "thoughtLevel", group.GetId())
	assert.Equal(t, "Thought Level", group.GetLabel())
	require.Len(t, group.GetOptions(), 2)
	assert.Equal(t, "high", base.options.values["thoughtLevel"])
	assert.Equal(t, "high", group.GetDefaultValue(), "the currentValue marks the default option")
}

// An unmapped option declared with no name and no category (just a distinct id) still
// surfaces, labelled by its id.
func TestApplyOptionGroupsLocked_SurfacesIDOnlyOption(t *testing.T) {
	t.Parallel()

	var base Base
	options := []ConfigOption{
		{ID: ConfigOptionIDModel, CurrentValue: "m1", Options: []ConfigOptionValue{{Value: "m1"}}},
		{ID: "reasoning", CurrentValue: "medium", Options: []ConfigOptionValue{{Value: "low"}, {Value: "medium"}}},
	}

	base.Mu.Lock()
	base.applyOptionGroupsLocked(options)
	base.Mu.Unlock()

	require.Len(t, base.options.groups, 1)
	assert.Equal(t, "reasoning", base.options.groups[0].GetId())
	assert.Equal(t, "reasoning", base.options.groups[0].GetLabel(), "a nameless option is labelled by its id")
}

// The claimed mode option -- even when declared via `category` with a non-literal id
// -- is never surfaced as a option group, and a payload with no unmapped option
// leaves the stored state untouched (keep-stored guard).
func TestApplyOptionGroupsLocked_ExcludesClaimedModeByCategory(t *testing.T) {
	t.Parallel()

	// A permission-mode provider consumes the mode channel, so its mode option is
	// claimed by category and excluded from the option groups.
	base := Base{hooks: Hooks{ModeChannel: ModeChannelPermissionMode}}
	options := []ConfigOption{
		{ID: "opaque-mode", Category: acpConfigOptionCategoryMode, CurrentValue: "plan",
			Options: []ConfigOptionValue{{Value: "build"}, {Value: "plan"}}},
	}

	base.Mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.Mu.Unlock()

	assert.False(t, valueChanged)
	assert.False(t, listChanged)
	assert.Empty(t, base.options.groups, "the claimed mode is not double-rendered as a option group")
}

// A provider that consumes neither channel does NOT claim a mode option, so
// rather than silently dropping it, the mode surfaces as a mutable option group.
func TestApplyOptionGroupsLocked_SurfacesUnconsumedModeForNonSyncingProvider(t *testing.T) {
	t.Parallel()

	var base Base // modeChannel stays ModeChannelUnmapped
	options := []ConfigOption{
		{ID: ConfigOptionIDMode, Category: acpConfigOptionCategoryMode, CurrentValue: "plan",
			Options: []ConfigOptionValue{{Value: "build"}, {Value: "plan"}}},
	}

	base.Mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.Mu.Unlock()

	assert.True(t, valueChanged)
	assert.True(t, listChanged)
	require.Len(t, base.options.groups, 1, "an unconsumed mode option is surfaced, not dropped")
	assert.Equal(t, ConfigOptionIDMode, base.options.groups[0].GetId())
	assert.Equal(t, "plan", base.options.values[ConfigOptionIDMode])
}

// An unmapped option whose id collides with a reserved proto group key
// (primaryAgent/permissionMode) is never surfaced as a option group -- the mapped
// channel owns that key, and a second group with it would double-list the key.
func TestApplyOptionGroupsLocked_SkipsReservedGroupKeys(t *testing.T) {
	t.Parallel()

	base := Base{hooks: Hooks{ModeChannel: ModeChannelPrimaryAgent}}
	options := []ConfigOption{
		{ID: agent.OptionIDPrimaryAgent, CurrentValue: "x", Options: []ConfigOptionValue{{Value: "x"}, {Value: "y"}}},
		{ID: agent.OptionIDPermissionMode, CurrentValue: "a", Options: []ConfigOptionValue{{Value: "a"}, {Value: "b"}}},
	}

	base.Mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.Mu.Unlock()

	assert.False(t, valueChanged)
	assert.False(t, listChanged)
	assert.Empty(t, base.options.groups, "a reserved-key option is never surfaced as a option group")
}

// A complete configOptions payload that no longer carries a previously-surfaced option
// drops it. Every ACP provider sends a COMPLETE snapshot of the currently-applicable
// options (verified across Goose/Kilo/OpenCode/Cursor/Copilot/Reasonix; none emits a
// partial/delta), so an option absent from a non-empty payload no longer applies -- e.g.
// OpenCode/Kilo drop effort for a model without variants, Copilot drops reasoning_effort
// for a model without effort support. The dropped option is removed from the live state
// and (via surfacedGenericIDs) emitted as "" so the persisted value is deleted.
func TestApplyOptionGroupsLocked_CompletePayloadDropsAbsentOption(t *testing.T) {
	t.Parallel()

	var base Base
	seed := []ConfigOption{{ID: "thoughtLevel", Name: "Thought Level", CurrentValue: "high",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}}}

	base.Mu.Lock()
	base.applyOptionGroupsLocked(seed)
	// A later complete payload carries only the model -- the option no longer applies.
	valueChanged, listChanged := base.applyOptionGroupsLocked([]ConfigOption{
		{ID: ConfigOptionIDModel, CurrentValue: "m2", Options: []ConfigOptionValue{{Value: "m2"}}},
	})
	extras := base.options.mergeOptionValues(nil)
	base.Mu.Unlock()

	assert.True(t, valueChanged, "dropping the surfaced option is a value change")
	assert.True(t, listChanged, "the option group list shrank")
	assert.Empty(t, base.options.groups, "the no-longer-applicable option is dropped")
	_, live := base.options.values["thoughtLevel"]
	assert.False(t, live, "the dropped option is gone from the live values")
	// The model option is a claimed channel, so after the second payload g.values is empty --
	// this is the all-options-dropped case. Assert thoughtLevel is PRESENT in the delta with an
	// explicit "" (not merely absent): a bare `extras["thoughtLevel"] == ""` passes even when
	// mergeOptionValues returns nil (the bug), since a nil-map read also yields "". Requiring the
	// key's presence is what proves the stale persisted value is actually deleted.
	val, present := extras["thoughtLevel"]
	require.True(t, present, "the dropped option rides along as an explicit key so its stored value is deleted")
	assert.Equal(t, "", val, "with value \"\" so the merge DELETES the persisted value")
}

// An EMPTY configOptions payload carries no information (e.g. a session response before the
// model inventory resolved), so it must leave the stored options untouched -- the only
// preserve case once every non-empty payload is treated as a complete, authoritative set.
func TestApplyOptionGroupsLocked_EmptyPayloadPreservesStored(t *testing.T) {
	t.Parallel()

	var base Base
	base.Mu.Lock()
	base.applyOptionGroupsLocked([]ConfigOption{{ID: "thoughtLevel", CurrentValue: "high",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}}})
	valueChanged, listChanged := base.applyOptionGroupsLocked(nil)
	base.Mu.Unlock()

	assert.False(t, valueChanged)
	assert.False(t, listChanged)
	require.Len(t, base.options.groups, 1, "an empty payload preserves the stored option")
	assert.Equal(t, "high", base.options.values["thoughtLevel"])
}

// A later full payload that drops a previously-surfaced option (reports a smaller set)
// must emit the dropped id as an explicit "" in the persist extras, so the uniform refresh
// merge DELETES its stale stored value instead of preserving it (an absent key is kept).
func TestMergeExtras_DeletesDroppedOption(t *testing.T) {
	t.Parallel()

	var base Base
	base.Mu.Lock()
	defer base.Mu.Unlock()

	// Seed two options.
	base.applyOptionGroupsLocked([]ConfigOption{
		{ID: "effort", CurrentValue: "high", Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}},
		{ID: "allow_all", CurrentValue: "on", Options: []ConfigOptionValue{{Value: "on"}, {Value: "off"}}},
	})
	require.Equal(t, "on", base.options.values["allow_all"])

	// A later full payload reports only effort -- allow_all is dropped.
	base.applyOptionGroupsLocked([]ConfigOption{
		{ID: "effort", CurrentValue: "low", Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}},
	})
	_, stillLive := base.options.values["allow_all"]
	assert.False(t, stillLive, "the dropped option is gone from the live values")

	extras := base.options.mergeOptionValues(nil)
	assert.Equal(t, "low", extras["effort"], "the surviving option carries its new value")
	dropped, present := extras["allow_all"]
	assert.True(t, present, "the dropped option is present in the refresh extras...")
	assert.Equal(t, "", dropped, "...as an explicit empty value so the refresh merge deletes it")
}

// An advertised-but-never-valued option (e.g. reported with an empty current at handshake)
// is recorded as KNOWN (so a persisted preference stays re-pushable) but NOT surfaced, so
// mergeOptionValues must not emit a redundant "" delete for it -- doing so would wipe
// a persisted preference still awaiting re-push. This pins the knownGenericIDs (advertised)
// vs surfacedGenericIDs (once-valued) split.
func TestMergeExtras_AdvertisedButNeverValuedNotDeleted(t *testing.T) {
	t.Parallel()

	var base Base
	base.Mu.Lock()
	// A first-sighting option with an empty current and nothing stored: advertised but
	// value-less, so it is known but never surfaces a value.
	base.applyOptionGroupsLocked([]ConfigOption{
		{ID: "reasoning", Category: "thought_level", Name: "Reasoning", CurrentValue: "",
			Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}},
	})
	known := base.options.known.has("reasoning")
	surfaced := base.options.surfaced.has("reasoning")
	extras := base.options.mergeOptionValues(map[string]string{agent.OptionIDPrimaryAgent: "build"})
	base.Mu.Unlock()

	assert.True(t, known, "the advertised id is known, so a persisted preference stays re-pushable")
	assert.False(t, surfaced, "it never surfaced a value, so it is not in surfacedGenericIDs")
	_, emitted := extras["reasoning"]
	assert.False(t, emitted, "no redundant \"\" delete is emitted for a never-valued option")
	assert.Equal(t, "build", extras[agent.OptionIDPrimaryAgent], "the base extras are still carried")
}

// TestBoundedIDSet_LRUEviction verifies the config-option id set caps growth with LRU
// eviction (the backend mirror of settingsLabelCache): the least-recently-used id is evicted
// past the cap, and re-adding an existing id refreshes it (moving it off the eviction front)
// instead of duplicating it. The read methods are nil-safe.
func TestBoundedIDSet_LRUEviction(t *testing.T) {
	t.Parallel()

	s := newBoundedIDSet()
	for i := range maxOptionStateIDs {
		s.add(fmt.Sprintf("id-%d", i), nil)
	}
	// Re-add the oldest id: it becomes most-recently-used and must survive the next eviction.
	s.add("id-0", nil)
	// One more distinct id pushes the set over the cap, evicting the now-least-recent id-1.
	s.add("overflow", nil)

	assert.Len(t, s.keys(), maxOptionStateIDs, "the set never exceeds the cap")
	assert.True(t, s.has("id-0"), "a re-added id is refreshed and survives eviction")
	assert.True(t, s.has("overflow"), "the newest id is retained")
	assert.False(t, s.has("id-1"), "the least-recently-used id is evicted")
	assert.False(t, (*boundedIDSet)(nil).has("x"), "a nil set reads as empty")
	assert.Nil(t, (*boundedIDSet)(nil).keys(), "a nil set has no keys")
}

// TestBoundedIDSet_ProtectsPinnedIDs verifies the eviction guard: a protected (live-valued)
// id is never the eviction victim even when it is the least-recently-used, so a config option
// carrying a value can't be dropped from the known/surfaced set by a non-conforming server
// churning distinct ids. The least-recently-used UNPROTECTED id is evicted instead.
func TestBoundedIDSet_ProtectsPinnedIDs(t *testing.T) {
	t.Parallel()

	s := newBoundedIDSet()
	// "pinned" is added first (oldest/LRU) and never touched again, so it would normally be the
	// first evicted; the protect predicate keeps it.
	protect := func(id string) bool { return id == "pinned" }
	s.add("pinned", protect)
	for i := range maxOptionStateIDs {
		s.add(fmt.Sprintf("id-%d", i), protect)
	}
	// The set is now one over the cap; the eviction skipped "pinned" and dropped id-0 (the
	// least-recently-used unprotected id).
	assert.True(t, s.has("pinned"), "a protected id is never evicted even as the LRU entry")
	assert.False(t, s.has("id-0"), "the least-recently-used UNPROTECTED id is evicted instead")
	assert.Len(t, s.keys(), maxOptionStateIDs, "the set holds at most the cap when an unprotected id is available")
}

// TestApplyOptionGroupsLocked_ProtectsFirstSightingValuedIDs guards the one-apply-behind
// eviction bug: a single configOptions payload carrying MORE than the cap of distinct,
// FIRST-SIGHTING options that each surface a concrete value must not evict the earliest of
// them from known/surfaced. The eviction guard (valued) reads g.values, which apply commits
// only AFTER its loop, so a first-sighting id is not yet in g.values when a LATER id in the
// same payload triggers eviction. apply now exposes the in-flight values via pendingValues so
// valued sees them; without that, the earliest valued ids would be dropped -- stranding their
// value/template and their pending "" delete. The whole set is genuinely live, so the bound
// permits the documented over-cap growth rather than shedding a valued id.
func TestApplyOptionGroupsLocked_ProtectsFirstSightingValuedIDs(t *testing.T) {
	t.Parallel()

	var base Base
	const extra = 50
	options := make([]ConfigOption, 0, maxOptionStateIDs+extra)
	for i := range maxOptionStateIDs + extra {
		id := fmt.Sprintf("opt-%04d", i)
		options = append(options, ConfigOption{
			ID:           id,
			Name:         id,
			CurrentValue: "v",
			Options:      []ConfigOptionValue{{Value: "v"}, {Value: "w"}},
		})
	}

	base.Mu.Lock()
	base.applyOptionGroupsLocked(options)
	base.Mu.Unlock()

	// Every id is valued in THIS payload, so none is an eviction victim and the set grows past
	// the cap (the documented all-valued over-cap case) -- the earliest ids must survive.
	assert.True(t, base.options.known.has("opt-0000"),
		"the first-sighting valued id is protected from same-payload eviction")
	assert.True(t, base.options.surfaced.has("opt-0000"),
		"the first-sighting valued id stays surfaced so a later drop can emit its \"\" delete")
	assert.Equal(t, "v", base.options.values["opt-0000"], "its value is recorded")
	assert.Len(t, base.options.known.keys(), maxOptionStateIDs+extra,
		"an all-valued payload retains every id rather than shedding a live one")
	// pendingValues is only set during apply; it must be cleared afterward.
	assert.Nil(t, base.options.pendingValues, "pendingValues is cleared once apply returns")
}

// TestThoughtLevelConfigOptionID covers the generic-daemon fallback startupEffortConfigID uses to
// map the well-known "effort" env-override onto a daemon's spec-categorized effort axis. It matches
// by the ACP `thought_level` category ALONE: a provider-convention id (reasoning_effort /
// thinking_effort) is declared on Base.effortConfigID instead, so a well-known effort id the
// daemon advertises but the running provider did not claim is NOT auto-discovered here -- the guard
// that stops a coincidental second axis from getting the override double-pushed.
func TestThoughtLevelConfigOptionID(t *testing.T) {
	t.Parallel()

	t.Run("non-effort id matched by thought_level category", func(t *testing.T) {
		g := &optionState{templates: map[string]ConfigOption{
			"thinking": {ID: "thinking", Category: acpConfigOptionCategoryThoughtLevel},
		}}
		assert.Equal(t, "thinking", g.thoughtLevelConfigOptionID())
	})
	t.Run("category-less convention id is NOT auto-discovered", func(t *testing.T) {
		// A well-known effort id without the thought_level category is a provider convention --
		// the provider declares it via effortConfigID; the category-only scan must not claim it,
		// or a coincidental second axis would get the override double-pushed (the S1 hazard).
		g := &optionState{templates: map[string]ConfigOption{
			"reasoning_effort": {ID: "reasoning_effort"},
			"model":            {ID: "model"},
		}}
		assert.Empty(t, g.thoughtLevelConfigOptionID())
	})
	t.Run("the well-known effort id is excluded even with the category", func(t *testing.T) {
		g := &optionState{templates: map[string]ConfigOption{
			agent.OptionIDEffort: {ID: agent.OptionIDEffort, Category: acpConfigOptionCategoryThoughtLevel},
		}}
		assert.Empty(t, g.thoughtLevelConfigOptionID(), "no mapping needed when the axis already uses \"effort\"")
	})
	t.Run("no thought_level axis yields empty", func(t *testing.T) {
		g := &optionState{templates: map[string]ConfigOption{
			"model": {ID: "model"}, "allow_all": {ID: "allow_all"},
		}}
		assert.Empty(t, g.thoughtLevelConfigOptionID())
	})
}

// TestApplyOptionGroupsLocked_SkipsEmptyCurrentOnFirstSighting verifies the C4
// fix: the first time a config option is reported with an empty server current AND no
// prior stored value, it is NOT surfaced (a blank-selection group the frontend would
// default to the strongest tier) and NOT recorded (an empty entry mergeOptionValues
// would emit as a DB delete). genericOptionValues is never seeded from the DB, so this
// first-handshake case has no stored fallback -- the value is "not yet known", not cleared.
func TestApplyOptionGroupsLocked_SkipsEmptyCurrentOnFirstSighting(t *testing.T) {
	t.Parallel()

	var base Base // empty genericOptionValues -> nothing stored to fall back on
	options := []ConfigOption{
		{ID: ConfigOptionIDModel, CurrentValue: "m1", Options: []ConfigOptionValue{{Value: "m1"}}},
		{ID: "reasoning", Category: "thought_level", Name: "Reasoning", CurrentValue: "",
			Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}},
	}

	base.Mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.Mu.Unlock()

	assert.False(t, valueChanged, "an empty-current first sighting records nothing")
	assert.False(t, listChanged)
	assert.Empty(t, base.options.groups, "no blank-selection group is surfaced")
	_, recorded := base.options.values["reasoning"]
	assert.False(t, recorded, "no empty entry is recorded (it would become a DB delete)")

	// A later payload with a concrete current surfaces it for real.
	base.Mu.Lock()
	valueChanged, listChanged = base.applyOptionGroupsLocked([]ConfigOption{
		{ID: "reasoning", Category: "thought_level", Name: "Reasoning", CurrentValue: "high",
			Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}},
	})
	base.Mu.Unlock()
	assert.True(t, valueChanged)
	assert.True(t, listChanged)
	require.Len(t, base.options.groups, 1, "a concrete current surfaces the group")
	assert.Equal(t, "high", base.options.values["reasoning"])
}

// A changed currentValue reports valueChanged; a same-value payload that only adds an
// option reports listChanged.
func TestApplyOptionGroupsLocked_ValueChangeVsListChange(t *testing.T) {
	t.Parallel()

	var base Base
	seed := []ConfigOption{{ID: "thoughtLevel", CurrentValue: "low",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}}}
	base.Mu.Lock()
	base.applyOptionGroupsLocked(seed)
	base.Mu.Unlock()

	// (1) Same option set, new current value -> valueChanged.
	base.Mu.Lock()
	valueChanged, _ := base.applyOptionGroupsLocked([]ConfigOption{{ID: "thoughtLevel", CurrentValue: "high",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}}}})
	base.Mu.Unlock()
	assert.True(t, valueChanged, "a changed currentValue is a value change")

	// (2) Same current value, an added option -> listChanged only.
	base.Mu.Lock()
	valueChanged2, listChanged2 := base.applyOptionGroupsLocked([]ConfigOption{{ID: "thoughtLevel", CurrentValue: "high",
		Options: []ConfigOptionValue{{Value: "low"}, {Value: "high"}, {Value: "max"}}}})
	base.Mu.Unlock()
	assert.False(t, valueChanged2, "the current value did not change")
	assert.True(t, listChanged2, "a new option is a list change")
}

// mergeOptionValues overlays options onto a base map, lets the base own its
// keys, INCLUDES cleared (empty) surfaced options as explicit "" entries (so the
// uniform refresh merge deletes them), and returns nil only when nothing at all is
// being reported (so the keep-stored contract holds).
func TestMergeExtras(t *testing.T) {
	t.Parallel()

	t.Run("nil when empty", func(t *testing.T) {
		var b Base
		assert.Nil(t, b.options.mergeOptionValues(nil))
	})
	t.Run("overlays options onto a nil base", func(t *testing.T) {
		var b Base
		b.options.values = map[string]string{"thoughtLevel": "high"}
		assert.Equal(t, optionmap.Map{"thoughtLevel": "high"}, b.options.mergeOptionValues(nil))
	})
	t.Run("base wins over a clashing option key", func(t *testing.T) {
		var b Base
		b.options.values = map[string]string{agent.OptionIDPrimaryAgent: "EVIL", "thoughtLevel": "high"}
		got := b.options.mergeOptionValues(map[string]string{agent.OptionIDPrimaryAgent: "build"})
		assert.Equal(t, optionmap.Map{agent.OptionIDPrimaryAgent: "build", "thoughtLevel": "high"}, got,
			"the base map owns primaryAgent; a clashing option value cannot clobber it")
	})
	t.Run("a surfaced-but-empty option clears rather than keeping stored", func(t *testing.T) {
		var b Base
		// The option is surfaced (genericOptionValues has the key) but its value was
		// cleared. The empty value is INCLUDED as an explicit "" entry so the uniform
		// refresh merge (present-empty = delete) drops the key; omitting it would
		// instead preserve the stale stored value.
		b.options.values = map[string]string{"thoughtLevel": ""}
		got := b.options.mergeOptionValues(nil)
		assert.Equal(t, optionmap.Map{"thoughtLevel": ""}, got)
	})
	t.Run("a cleared option does not wipe the base key", func(t *testing.T) {
		var b Base
		b.options.values = map[string]string{"thoughtLevel": ""}
		got := b.options.mergeOptionValues(map[string]string{agent.OptionIDPrimaryAgent: "build"})
		assert.Equal(t, optionmap.Map{agent.OptionIDPrimaryAgent: "build", "thoughtLevel": ""}, got,
			"the base primary agent survives; the cleared option rides along as \"\" to be deleted downstream")
	})
}

// TestSecondaryChannel_DerivesFromModeChannel locks in the single source of truth the
// unified UpdateSettings / reapply / refresh paths rely on: modeChannel alone determines
// the secondary axis's option id, field, log key, available-list pointer, persist shape, and
// config-override presence. The permission-mode and primary-agent families are distinct, and
// the unmapped zero value defaults to permission mode (with no config override).
func TestSecondaryChannel_DerivesFromModeChannel(t *testing.T) {
	t.Parallel()

	pm := Base{hooks: Hooks{ModeChannel: ModeChannelPermissionMode}}
	scPM := pm.secondaryChannel()
	assert.Equal(t, agent.OptionIDPermissionMode, scPM.optionID)
	assert.Equal(t, "permissionMode", scPM.logKey)
	assert.Same(t, &pm.permissionMode, scPM.field, "permission-mode family points at b.permissionMode")
	assert.Same(t, &pm.availableModes, scPM.available, "permission-mode family reads b.availableModes")
	// A permission-mode provider carries the secondary in PersistSettingsRefresh's mode arg,
	// NOT in the option values.
	pmBase, pmMode := scPM.persistShape("plan")
	assert.Nil(t, pmBase, "permission mode contributes no option-values base")
	assert.Equal(t, "plan", pmMode, "permission mode is carried in the persist mode arg")
	assert.NotNil(t, scPM.syncConfigOverride, "permission mode has a configOptions override")

	pa := Base{hooks: Hooks{ModeChannel: ModeChannelPrimaryAgent}}
	scPA := pa.secondaryChannel()
	assert.Equal(t, agent.OptionIDPrimaryAgent, scPA.optionID)
	assert.Equal(t, "primaryAgent", scPA.logKey)
	assert.Same(t, &pa.currentPrimaryAgent, scPA.field, "primary-agent family points at b.currentPrimaryAgent")
	assert.Same(t, &pa.availablePrimaryAgents, scPA.available, "primary-agent family reads b.availablePrimaryAgents")
	// A primary-agent provider carries the secondary in the option values, NOT the mode arg.
	paBase, paMode := scPA.persistShape("build")
	assert.Equal(t, map[string]string{agent.OptionIDPrimaryAgent: "build"}, paBase, "primary agent is carried in the option-values base")
	assert.Empty(t, paMode, "primary agent contributes no persist mode arg")
	assert.NotNil(t, scPA.syncConfigOverride, "primary agent has a configOptions override")

	var unmapped Base // ModeChannelUnmapped zero value
	scU := unmapped.secondaryChannel()
	assert.Equal(t, agent.OptionIDPermissionMode, scU.optionID,
		"the unmapped default is the permission-mode channel")
	assert.Nil(t, scU.syncConfigOverride,
		"the unmapped channel has no config override (reproduces the old switch's no-default no-op)")
}

// TestEffectiveSetModel_FallsBackToBaseSetter verifies the model writer defaults to the
// base setModel when no provider override is set (Cursor sets modelSetter to its
// wire-mapping setter; every other ACP provider leaves it nil).
func TestEffectiveSetModel_FallsBackToBaseSetter(t *testing.T) {
	t.Parallel()

	var b Base
	assert.NotNil(t, b.effectiveSetModel(), "a nil modelSetter falls back to the base setModel")

	called := false
	b.hooks.ModelSetter = func(string) error { called = true; return nil }
	_ = b.effectiveSetModel()("x")
	assert.True(t, called, "a set modelSetter override is used")
}

func TestModelDecorator_ReadsTheMetadataOfEachModel(t *testing.T) {
	t.Parallel()
	b := &Base{}
	seen := map[string]string{}
	b.hooks.ModelDecorator = func(m *agent.ModelInfo, meta json.RawMessage) {
		seen[m.Id] = string(meta)
		var window struct {
			Tokens int64 `json:"windowTokens"`
		}
		if json.Unmarshal(meta, &window) == nil {
			m.ContextWindow = window.Tokens
		}
	}
	models, current := b.buildModels([]ModelInfo{
		{ModelID: "alpha", Name: "Alpha", Meta: json.RawMessage(`{"windowTokens":128000}`)},
		{ModelID: "alpha", Name: "Alpha again", Meta: json.RawMessage(`{"windowTokens":1}`)},
		{ModelID: "beta", Name: "Beta"},
	}, "beta")

	require.Len(t, models, 2)
	assert.Equal(t, "beta", current)
	assert.Equal(t, int64(128000), models[0].ContextWindow, "the metadata of the kept duplicate")
	assert.Equal(t, int64(0), models[1].ContextWindow, "a model with no metadata")
	assert.Equal(t, map[string]string{"alpha": `{"windowTokens":128000}`, "beta": ""}, seen)
}

// A normalizer can map two raw ids to one model. The build keeps the first raw
// info of that model, and the decorator reads the metadata of that same info,
// not of the duplicate that the build dropped.
func TestModelDecorator_ReadsTheMetadataOfTheInfoThatTheBuildKept(t *testing.T) {
	t.Parallel()
	b := &Base{}
	b.hooks.ModelIDNormalizer = func(id string) string {
		if id == "default[]" {
			return "auto"
		}
		return id
	}
	seen := map[string]string{}
	b.hooks.ModelDecorator = func(m *agent.ModelInfo, meta json.RawMessage) {
		seen[m.Id] = string(meta)
	}

	models, current := b.buildModels([]ModelInfo{
		{ModelID: "default[]", Name: "Auto", Meta: json.RawMessage(`{"rank":1}`)},
		{ModelID: "auto", Name: "Auto again", Meta: json.RawMessage(`{"rank":2}`)},
	}, "default[]")

	require.Len(t, models, 1)
	assert.Equal(t, "auto", models[0].Id)
	assert.Equal(t, "Auto", models[0].DisplayName)
	assert.Equal(t, "auto", current)
	assert.Equal(t, map[string]string{"auto": `{"rank":1}`}, seen)
}

func TestModelInfo_AConfigOptionModelCarriesItsMetadata(t *testing.T) {
	t.Parallel()
	session, err := parseACPSessionResult(json.RawMessage(`{"sessionId":"s","configOptions":[{"type":"select","id":"model","name":"Model","category":"model","currentValue":"m","options":[` +
		`{"value":"m","name":"M","_meta":{"vendor":{"rate":1.3}}},{"value":"n","name":"N"}]}]}`))
	require.NoError(t, err)
	infos, current := acpHandshakeModelInfos(session)

	assert.Equal(t, "m", current)
	require.Len(t, infos, 2)
	assert.JSONEq(t, `{"vendor":{"rate":1.3}}`, string(infos[0].Meta), "the decorator reads a config option's model as it reads a models-field model")
	assert.Empty(t, infos[1].Meta)
}

func TestModelInfo_DecodesItsMetadata(t *testing.T) {
	t.Parallel()
	session, err := parseACPSessionResult(json.RawMessage(`{"sessionId":"s","models":{"currentModelId":"m","availableModels":[{"modelId":"m","name":"M","_meta":{"contextLimit":200000}}]}}`))
	require.NoError(t, err)
	require.Len(t, session.Models, 1)
	assert.JSONEq(t, `{"contextLimit":200000}`, string(session.Models[0].Meta))
}
