package agent

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
)

// trySetStartupModel applies a requested model best-effort: it pushes when the
// request differs from the server's current (including when the server reports
// none), is a no-op when they match, and keeps the current model on rejection.
// The writer is resolved via effectiveSetModel, so the tests inject through
// modelSetter (the override effectiveSetModel prefers).

func TestTrySetStartupModel_PushesWhenServerReportsNoModel(t *testing.T) {
	var base acpBase
	got := ""
	base.modelSetter = func(m string) error {
		got = m
		base.model = m
		return nil
	}
	base.trySetStartupModel("user/arbitrary")
	assert.Equal(t, "user/arbitrary", got, "an arbitrary model must be pushed when the server advertises none")
	assert.Equal(t, "user/arbitrary", base.model)
}

func TestTrySetStartupModel_NoopWhenMatchesCurrent(t *testing.T) {
	base := acpBase{}
	base.model = "anthropic/claude-sonnet-4"
	called := false
	base.modelSetter = func(string) error {
		called = true
		return nil
	}
	base.trySetStartupModel("anthropic/claude-sonnet-4")
	assert.False(t, called, "no setModel when the request already matches the server's current")
}

func TestTrySetStartupModel_NonFatalOnRejection(t *testing.T) {
	base := acpBase{}
	base.providerName = "opencode"
	base.model = "server/current"
	base.modelSetter = func(string) error {
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
	handshake := &acpSessionResult{
		CurrentModelID: "m1",
		Models:         []acpModelInfo{{ModelID: "m1", Name: "Model 1"}, {ModelID: "m2", Name: "Model 2"}},
		ConfigOptions: []acpConfigOption{{
			ID: acpConfigOptionIDModel, CurrentValue: "cfg",
			Options: []acpConfigOptionValue{
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
	// This is the OpenCode/Kilo shape: no `models` field, models in configOptions.
	handshake := &acpSessionResult{
		ConfigOptions: []acpConfigOption{
			{ID: acpConfigOptionIDMode, CurrentValue: "build", Options: []acpConfigOptionValue{{Value: "build", Name: "Build"}}},
			{ID: acpConfigOptionIDModel, CurrentValue: "anthropic/claude-sonnet-4", Options: []acpConfigOptionValue{
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
	handshake := &acpSessionResult{
		ConfigOptions: []acpConfigOption{{ID: acpConfigOptionIDMode, CurrentValue: "build"}},
	}

	models, current := acpHandshakeModelInfos(handshake)

	require.Empty(t, models)
	require.Equal(t, "", current)
}

// buildACPModels dedups by the final (post-normalize) id, so a normalizer that
// collapses two distinct wire ids to one does not surface a duplicate model.
func TestBuildACPModels_DedupsByNormalizedID(t *testing.T) {
	models := buildACPModels([]acpModelInfo{
		{ModelID: cursorCLIModelAuto, Name: "Auto"},
		{ModelID: cursorCLIModelAutoWire, Name: "Auto (wire form)"}, // "default[]" -> "auto"
		{ModelID: "gpt-5", Name: "GPT-5"},
	}, cursorCLIModelAuto, normalizeCursorModelID)

	require.Len(t, models, 2)
	assert.Equal(t, cursorCLIModelAuto, models[0].GetId())
	assert.Equal(t, "Auto", models[0].DisplayName) // first occurrence wins
	assert.True(t, models[0].IsDefault)
	assert.Equal(t, "gpt-5", models[1].GetId())
}

// A server that repeats a model id within a single channel yields one entry.
func TestBuildACPModels_DedupsRepeatedID(t *testing.T) {
	models := buildACPModels([]acpModelInfo{
		{ModelID: "m1", Name: "Model 1"},
		{ModelID: "m1", Name: "Model 1 (dup)"},
	}, "m1", nil)

	require.Len(t, models, 1)
	assert.Equal(t, "Model 1", models[0].DisplayName)
}

func TestACPModelInfosFromConfigOption_SkipsEmptyValues(t *testing.T) {
	option := acpConfigOption{
		ID:           acpConfigOptionIDModel,
		CurrentValue: "openai/gpt-5",
		Options: []acpConfigOptionValue{
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

func handshakeWithConfigModeOverride() *acpSessionResult {
	return &acpSessionResult{
		CurrentModeID: "agent",
		Modes:         []acpModeInfo{{ID: "agent", Name: "Agent"}, {ID: "plan", Name: "Plan"}},
		ConfigOptions: []acpConfigOption{{
			ID: acpConfigOptionIDMode, CurrentValue: "plan",
			Options: []acpConfigOptionValue{{Value: "agent", Name: "Agent"}, {Value: "plan", Name: "Plan"}},
		}},
	}
}

// A provider that consumes the configOptions `mode` (modeChannelPermissionMode --
// Copilot/Goose/Cursor) applies the override at handshake, matching the runtime and
// ClearContext paths.
func TestApplyHandshakeMode_ConfigOptionOverridesModesChannel(t *testing.T) {
	base := acpBase{modeChannel: modeChannelPermissionMode}

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
	var base acpBase // modeChannelUnmapped

	base.applyHandshakeMode(handshakeWithConfigModeOverride(), "fallback")

	// The modes-channel "agent" wins; the configOptions "plan" is not applied.
	assert.Equal(t, "agent", base.permissionMode)
	require.Len(t, base.availableModes, 2)
}

func TestApplyHandshakeMode_FallsBackToDefaultWhenServerReportsNone(t *testing.T) {
	var base acpBase
	base.applyHandshakeMode(&acpSessionResult{}, "default-mode")
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
	var base acpBase // modeChannelUnmapped
	handshake := &acpSessionResult{
		CurrentModeID: "default",
		Modes:         []acpModeInfo{{ID: "default", Name: "Default"}, {ID: "plan", Name: "Plan"}},
		// The modes channel says "default" but the configOptions `mode` says "plan".
		ConfigOptions: []acpConfigOption{{
			ID: acpConfigOptionIDMode, CurrentValue: "plan",
			Options: []acpConfigOptionValue{{Value: "default", Name: "Default"}, {Value: "plan", Name: "Plan"}},
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
	assert.Equal(t, acpConfigOptionIDMode, base.options.groups[0].GetId())
	assert.Equal(t, "plan", base.options.values[acpConfigOptionIDMode])
}

// The base dispatcher handles a config_option_update model change for ANY ACP
// provider with no per-provider wiring. OpenCode and Kilo register no config
// option handler at all, yet their model list and current model stay in sync --
// and the `mode` option (their primary agent) is left untouched.

func TestHandleOpenCodeOutput_ConfigOptionUpdateRefreshesModelsGenerically(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "anthropic/claude-sonnet-4"
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, "openai/gpt-5", agent.model)
	require.Len(t, agent.availableModels, 2)
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.availableModels[0].GetId())
	assert.Equal(t, "openai/gpt-5", agent.availableModels[1].GetId())
	assert.True(t, agent.availableModels[1].IsDefault)
	// The `mode` config option carries the primary agent for OpenCode; a runtime
	// config_option_update syncs it alongside the model.
	assert.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	// The runtime model + primary-agent switch is broadcast once so the frontend
	// reflects it, carrying the new primary-agent extra (OpenCode has no permission mode).
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "openai/gpt-5", refresh.Model)
	assert.Equal(t, OpenCodePrimaryAgentPlan, refresh.Options[OptionIDPrimaryAgent])
}

// An idempotent config_option_update -- same current model AND same list as the
// agent already holds -- must trigger no broadcast at all (no settings write, no
// status refresh).
func TestHandleOpenCodeOutput_ConfigOptionUpdateNoBroadcastWhenUnchanged(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "openai/gpt-5"
	// Pre-seed the exact list the update carries so nothing actually changes.
	agent.availableModels = []*ModelInfo{
		{Id: "anthropic/claude-sonnet-4", DisplayName: "Claude Sonnet 4"},
		{Id: "openai/gpt-5", DisplayName: "GPT-5", IsDefault: true},
	}

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, "openai/gpt-5", agent.model)
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "no settings write when nothing changed")
	assert.Equal(t, 0, sink.StatusActiveCount(), "no status refresh when the list is identical")
}

// A config_option_update that keeps the current model but changes the available
// list must broadcast a status refresh (so the picker updates) without a settings
// DB write.
func TestHandleOpenCodeOutput_ConfigOptionUpdateBroadcastsListChange(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "openai/gpt-5"
	agent.availableModels = []*ModelInfo{
		{Id: "openai/gpt-5", DisplayName: "GPT-5", IsDefault: true},
	}

	// Same current model, but a new option appears in the list.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"openai/gpt-5","name":"GPT-5"},{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}]}]}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, "openai/gpt-5", agent.model)
	require.Len(t, agent.availableModels, 2)
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "no settings DB write when only the list changed")
	assert.Equal(t, 1, sink.StatusActiveCount(), "the new option list is broadcast via a status refresh")
}

// A runtime config_option_update carries only the configOptions `model` select.
// Models reported only through the SessionModelState `models` field at handshake
// must survive it -- applyConfigOptionModelsLocked re-unions the remembered
// models-field catalog so a split-catalog provider does not lose entries.
func TestApplyConfigOptionModelsLocked_ReunionsModelsFieldCatalog(t *testing.T) {
	var base acpBase
	// Simulate a handshake that reported "field/x" only through the models field.
	base.modelsFieldInfos = []acpModelInfo{{ModelID: "field/x", Name: "Field X"}}

	options := []acpConfigOption{{
		ID: acpConfigOptionIDModel, CurrentValue: "cfg/a",
		Options: []acpConfigOptionValue{
			{Value: "cfg/a", Name: "Cfg A"},
			{Value: "cfg/b", Name: "Cfg B"},
		},
	}}
	base.mu.Lock()
	modelChanged, listChanged := base.applyConfigOptionModelsLocked(options)
	base.mu.Unlock()

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

func TestHandleKiloOutput_ConfigOptionUpdateRefreshesModelsGenerically(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)
	agent.model = "anthropic/claude-sonnet-4"
	agent.currentPrimaryAgent = KiloPrimaryAgentCode

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"code","name":"Code"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, "openai/gpt-5", agent.model)
	require.Len(t, agent.availableModels, 2)
	assert.Equal(t, "openai/gpt-5", agent.availableModels[1].GetId())
	assert.True(t, agent.availableModels[1].IsDefault)
	// The `mode` config option carries the primary agent for Kilo; the runtime update
	// syncs it alongside the model (code -> plan).
	assert.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
}

// buildConfigOptionSelect dedups by value (first occurrence wins) and skips hidden
// ids, mirroring buildACPModels so a server repeating or leaking a pseudo-agent id
// does not surface duplicate or internal picker options.
func TestBuildConfigOptionSelect_DedupsAndFilters(t *testing.T) {
	options := []acpConfigOption{{
		ID: acpConfigOptionIDMode, CurrentValue: "build",
		Options: []acpConfigOptionValue{
			{Value: "build", Name: "Build"},
			{Value: "build", Name: "Build (dup)"},
			{Value: openCodeHiddenCompaction, Name: "Compaction"},
			{Value: "plan", Name: "Plan"},
		},
	}}

	built, current, ok := buildConfigOptionSelect(options, isHiddenPrimaryAgent)

	require.True(t, ok)
	assert.Equal(t, "build", current)
	require.Len(t, built, 2) // dup dropped, compaction filtered
	assert.Equal(t, "build", built[0].GetId())
	assert.Equal(t, "Build", built[0].GetName()) // first occurrence wins
	assert.Equal(t, "plan", built[1].GetId())
}

// A config_option_update that changes ONLY the available primary-agent list (same
// currentValue, no model) broadcasts a status refresh so the picker updates -- the
// primary-agent analogue of TestHandleOpenCodeOutput_ConfigOptionUpdateBroadcastsListChange
// for the model channel. Without it the new option never reaches the frontend until
// an unrelated change forces a status refresh.
func TestHandleOpenCodeOutput_ConfigOptionUpdatePrimaryAgentListOnlyBroadcasts(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: "Build"},
		{Id: OpenCodePrimaryAgentPlan, Name: "Plan"},
	}

	// Same currentValue ("build"), but "review" is added to the list; no model option.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"},{"value":"review","name":"Review"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Len(t, agent.availablePrimaryAgents, 3)
	assert.Equal(t, OpenCodePrimaryAgentBuild, agent.currentPrimaryAgent, "current primary agent unchanged")
	assert.Equal(t, 1, sink.StatusActiveCount(), "the new primary-agent option is broadcast via a status refresh")
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "no settings DB write when only the list changed")
}

// A runtime config_option_update normalizes primary-agent option names the same way
// the handshake (buildPrimaryAgentOptions) does: a whitespace-only name is blanked so
// the id is title-cased, rather than the runtime path leaking the literal whitespace.
func TestHandleOpenCodeOutput_ConfigOptionUpdateNormalizesAgentName(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild

	// "plan" reports a whitespace-only name; the runtime path must blank and
	// title-case the id, matching the handshake.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":" "}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Len(t, agent.availablePrimaryAgents, 2)
	planOpt := agent.availablePrimaryAgents[1]
	assert.Equal(t, OpenCodePrimaryAgentPlan, planOpt.GetId())
	assert.Equal(t, "Plan", planOpt.GetName(), "whitespace-only name is normalized and title-cased")
}

// A config_option_update whose `mode` currentValue is a hidden pseudo-agent must NOT
// adopt it as the current primary agent: the hidden id is filtered from the picker,
// so adopting it would leave the picker showing a selection it can't offer.
func TestHandleOpenCodeOutput_ConfigOptionUpdateIgnoresHiddenCurrentAgent(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: "Build"},
		{Id: OpenCodePrimaryAgentPlan, Name: "Plan"},
	}

	// currentValue is the hidden "compaction" pseudo-agent.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"compaction","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"},{"value":"compaction","name":"Compaction"}]}]}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, OpenCodePrimaryAgentBuild, agent.currentPrimaryAgent,
		"a hidden currentValue is not adopted as the current primary agent")
	require.Len(t, agent.availablePrimaryAgents, 2, "compaction is filtered from the list")
}

// primaryAgentOptions returns nil (not an empty map) for an empty agent so a
// settings refresh preserves stored extras instead of clearing them; a
// non-empty agent yields the single-key map.
func TestPrimaryAgentExtras(t *testing.T) {
	assert.Nil(t, primaryAgentOptions(""),
		"empty agent must yield nil so PersistSettingsRefresh keeps stored extras")
	assert.Equal(t, map[string]string{OptionIDPrimaryAgent: "build"}, primaryAgentOptions("build"))
}

// A runtime config_option_update that changes ONLY the primary agent (the `mode`
// select, no model) syncs currentPrimaryAgent and broadcasts the new selection,
// applying the hidden-agent filter to the rebuilt list. Mirrors how the
// permission-mode providers sync their mode.
func TestHandleOpenCodeOutput_ConfigOptionUpdateSyncsPrimaryAgent(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "anthropic/claude-sonnet-4"
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild
	agent.availableModels = []*ModelInfo{{Id: "anthropic/claude-sonnet-4"}}

	// `mode` select changes build -> plan and lists a hidden pseudo-agent.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"},{"value":"compaction","name":"Compaction"}]}]}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	// The hidden `compaction` pseudo-agent is filtered from the rebuilt list.
	require.Len(t, agent.availablePrimaryAgents, 2)
	// The change is broadcast once, carrying the new primary-agent extra.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, OpenCodePrimaryAgentPlan, sink.LastSettingsRefresh().Options[OptionIDPrimaryAgent])
}

// A runtime config_option_update that DROPS the active primary agent from the rebuilt
// option list (and names no replacement currentValue) must re-seed the current to the
// default-or-first option rather than leave it pointing at a value absent from the list.
// Without the re-seed the picker would show a selection it can no longer offer. [S1]
func TestHandleOpenCodeOutput_ConfigOptionUpdateReseedsOrphanedCurrent(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "anthropic/claude-sonnet-4"
	agent.currentPrimaryAgent = OpenCodePrimaryAgentPlan
	agent.availableModels = []*ModelInfo{{Id: "anthropic/claude-sonnet-4"}}
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: "Build"},
		{Id: OpenCodePrimaryAgentPlan, Name: "Plan"},
	}

	// The `mode` select drops the active "plan" and reports no current value.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"","options":[{"value":"build","name":"Build"},{"value":"review","name":"Review"}]}]}}}`
	agent.HandleOutput([]byte(input))

	// The orphaned "plan" is re-seeded to the default-or-first option ("build"), so the
	// current is always a member of the rebuilt list.
	assert.Equal(t, OpenCodePrimaryAgentBuild, agent.currentPrimaryAgent,
		"the orphaned current re-seeds to the first available option")
	require.Len(t, agent.availablePrimaryAgents, 2)
	assert.Equal(t, OpenCodePrimaryAgentBuild, agent.availablePrimaryAgents[0].GetId())
	assert.Equal(t, "review", agent.availablePrimaryAgents[1].GetId())
	// The re-seed is a current change: persisted and broadcast once with the new selection.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, OpenCodePrimaryAgentBuild, sink.LastSettingsRefresh().Options[OptionIDPrimaryAgent])
}

// --- reconcileCurrentOptionID: the shared current-selection resolver ---

// reconcileCurrentOptionID resolves a secondary channel's current against a freshly built
// option list: an empty list means "unreported" so the reported (else stored) value is
// trusted unchanged; otherwise a valid reported value is adopted, a still-valid stored
// value is kept, and failing both the current re-seeds to the list's first option (ACP
// options carry no per-option default badge). The handshake, runtime, and ClearContext
// seams all route through it.
func TestReconcileCurrentOptionID(t *testing.T) {
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
	options := []acpConfigOption{
		{ID: "opaque", Category: acpConfigOptionCategoryModel, CurrentValue: "a"},
		{ID: acpConfigOptionIDModel, CurrentValue: "b"}, // a coincidental id match
	}
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
	require.True(t, ok)
	assert.Equal(t, "opaque", option.ID, "the category match wins over the id fallback")
}

func TestACPConfigOptionByCategory_FallsBackToIDWhenNoCategory(t *testing.T) {
	// The shape every provider ships today: no `category`, well-known id.
	options := []acpConfigOption{{ID: acpConfigOptionIDMode, CurrentValue: "plan"}}
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryMode, acpConfigOptionIDMode)
	require.True(t, ok)
	assert.Equal(t, acpConfigOptionIDMode, option.ID)
}

func TestACPConfigOptionByCategory_SkipsNonSelectableType(t *testing.T) {
	options := []acpConfigOption{
		{ID: "x", Category: acpConfigOptionCategoryModel, Type: "text"},  // right category, unknown type
		{ID: acpConfigOptionIDModel, Type: "select", CurrentValue: "m1"}, // selectable id fallback
	}
	option, ok := acpConfigOptionByCategory(options, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
	require.True(t, ok)
	assert.Equal(t, acpConfigOptionIDModel, option.ID,
		"a non-selectable category match is skipped; the selectable id fallback wins")
}

func TestACPConfigOptionByCategory_NoneFound(t *testing.T) {
	_, ok := acpConfigOptionByCategory([]acpConfigOption{{ID: "other"}}, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
	assert.False(t, ok)
}

// TestACPConfigOptionByCategory_DeterministicOnDuplicateCategory guards S2: a (pathological)
// daemon reporting two same-category options must resolve to the LOWEST id, deterministically,
// rather than to whichever the server happened to list first -- so the claimed axis can't flip
// between refreshes with server slice order.
func TestACPConfigOptionByCategory_DeterministicOnDuplicateCategory(t *testing.T) {
	// Same two options in opposite server-reported orders must pick the same (lowest-id) winner.
	a := acpConfigOption{ID: "bbb", Category: acpConfigOptionCategoryModel, CurrentValue: "1"}
	b := acpConfigOption{ID: "aaa", Category: acpConfigOptionCategoryModel, CurrentValue: "2"}

	got1, ok1 := acpConfigOptionByCategory([]acpConfigOption{a, b}, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
	got2, ok2 := acpConfigOptionByCategory([]acpConfigOption{b, a}, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
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
	// Two options share the fallback id but differ in their current value; neither carries a category.
	a := acpConfigOption{ID: acpConfigOptionIDModel, CurrentValue: "zzz"}
	b := acpConfigOption{ID: acpConfigOptionIDModel, CurrentValue: "aaa"}

	got1, ok1 := acpConfigOptionByCategory([]acpConfigOption{a, b}, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
	got2, ok2 := acpConfigOptionByCategory([]acpConfigOption{b, a}, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
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
	a := acpConfigOption{ID: acpConfigOptionIDModel, Category: acpConfigOptionCategoryModel, CurrentValue: "zzz"}
	b := acpConfigOption{ID: acpConfigOptionIDModel, Category: acpConfigOptionCategoryModel, CurrentValue: "aaa"}

	got1, ok1 := acpConfigOptionByCategory([]acpConfigOption{a, b}, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
	got2, ok2 := acpConfigOptionByCategory([]acpConfigOption{b, a}, acpConfigOptionCategoryModel, acpConfigOptionIDModel)
	require.True(t, ok1)
	require.True(t, ok2)
	assert.Equal(t, "aaa", got1.CurrentValue, "the content-smallest same-category, same-id duplicate wins")
	assert.Equal(t, got1.CurrentValue, got2.CurrentValue, "the winner does not depend on server-reported slice order")
}

func TestIsSelectableConfigOption(t *testing.T) {
	assert.True(t, isSelectableConfigOption(acpConfigOption{Type: ""}), "empty type is treated as select")
	assert.True(t, isSelectableConfigOption(acpConfigOption{Type: "select"}))
	assert.False(t, isSelectableConfigOption(acpConfigOption{Type: "text"}))
	assert.False(t, isSelectableConfigOption(acpConfigOption{Type: "checkbox"}))
}

// The model channel dispatches by `category`, so a spec-compliant agent using a
// non-literal opaque id still surfaces its models.
func TestACPHandshakeModelInfos_DispatchesModelByCategory(t *testing.T) {
	handshake := &acpSessionResult{
		ConfigOptions: []acpConfigOption{{
			ID:           "opaque-model-id", // NOT the literal "model"
			Category:     acpConfigOptionCategoryModel,
			CurrentValue: "openai/gpt-5",
			Options: []acpConfigOptionValue{
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
	handshake := &acpSessionResult{
		ConfigOptions: []acpConfigOption{{
			ID:      acpConfigOptionIDModel,
			Type:    "text",
			Options: []acpConfigOptionValue{{Value: "should/be/ignored"}},
		}},
	}

	models, current := acpHandshakeModelInfos(handshake)

	assert.Empty(t, models)
	assert.Equal(t, "", current)
}

// The mode channel dispatches by `category` too (covering both the permission-mode
// and primary-agent sync paths, which share buildConfigOptionSelect).
func TestBuildConfigOptionSelect_DispatchesModeByCategory(t *testing.T) {
	options := []acpConfigOption{{
		ID:           "opaque-mode-id",
		Category:     acpConfigOptionCategoryMode,
		CurrentValue: "plan",
		Options:      []acpConfigOptionValue{{Value: "build", Name: "Build"}, {Value: "plan", Name: "Plan"}},
	}}

	built, current, ok := buildConfigOptionSelect(options, nil)

	require.True(t, ok)
	assert.Equal(t, "plan", current)
	require.Len(t, built, 2)
}

func TestBuildConfigOptionSelect_IgnoresNonSelectableModeOption(t *testing.T) {
	options := []acpConfigOption{{ID: acpConfigOptionIDMode, Type: "text"}}
	_, _, ok := buildConfigOptionSelect(options, nil)
	assert.False(t, ok, "an unknown widget type is not dispatched as the mode channel")
}

// --- Surfacing of unmapped config options (mutable) ---

// A handshake carrying a third axis (thought_level) surfaces it as a mutable
// option group keyed by id, while the claimed model and mode options are excluded
// (no double-render).
func TestApplyOptionGroupsLocked_SurfacesUnmappedOption(t *testing.T) {
	// A primary-agent provider consumes the mode channel, so its mode option is claimed
	// and excluded -- only the unmapped axis surfaces.
	base := acpBase{modeChannel: modeChannelPrimaryAgent}
	options := []acpConfigOption{
		{ID: acpConfigOptionIDMode, CurrentValue: "build", Options: []acpConfigOptionValue{{Value: "build"}, {Value: "plan"}}},
		{ID: acpConfigOptionIDModel, CurrentValue: "m1", Options: []acpConfigOptionValue{{Value: "m1"}}},
		{ID: "thoughtLevel", Category: "thought_level", Name: "Thought Level", CurrentValue: "high",
			Options: []acpConfigOptionValue{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}}},
	}

	base.mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.mu.Unlock()

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
	var base acpBase
	options := []acpConfigOption{
		{ID: acpConfigOptionIDModel, CurrentValue: "m1", Options: []acpConfigOptionValue{{Value: "m1"}}},
		{ID: "reasoning", CurrentValue: "medium", Options: []acpConfigOptionValue{{Value: "low"}, {Value: "medium"}}},
	}

	base.mu.Lock()
	base.applyOptionGroupsLocked(options)
	base.mu.Unlock()

	require.Len(t, base.options.groups, 1)
	assert.Equal(t, "reasoning", base.options.groups[0].GetId())
	assert.Equal(t, "reasoning", base.options.groups[0].GetLabel(), "a nameless option is labelled by its id")
}

// The claimed mode option -- even when declared via `category` with a non-literal id
// -- is never surfaced as a option group, and a payload with no unmapped option
// leaves the stored state untouched (keep-stored guard).
func TestApplyOptionGroupsLocked_ExcludesClaimedModeByCategory(t *testing.T) {
	// A permission-mode provider consumes the mode channel, so its mode option is
	// claimed by category and excluded from the option groups.
	base := acpBase{modeChannel: modeChannelPermissionMode}
	options := []acpConfigOption{
		{ID: "opaque-mode", Category: acpConfigOptionCategoryMode, CurrentValue: "plan",
			Options: []acpConfigOptionValue{{Value: "build"}, {Value: "plan"}}},
	}

	base.mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.mu.Unlock()

	assert.False(t, valueChanged)
	assert.False(t, listChanged)
	assert.Empty(t, base.options.groups, "the claimed mode is not double-rendered as a option group")
}

// A provider that consumes neither channel does NOT claim a mode option, so
// rather than silently dropping it, the mode surfaces as a mutable option group.
func TestApplyOptionGroupsLocked_SurfacesUnconsumedModeForNonSyncingProvider(t *testing.T) {
	var base acpBase // modeChannel stays modeChannelUnmapped
	options := []acpConfigOption{
		{ID: acpConfigOptionIDMode, Category: acpConfigOptionCategoryMode, CurrentValue: "plan",
			Options: []acpConfigOptionValue{{Value: "build"}, {Value: "plan"}}},
	}

	base.mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.mu.Unlock()

	assert.True(t, valueChanged)
	assert.True(t, listChanged)
	require.Len(t, base.options.groups, 1, "an unconsumed mode option is surfaced, not dropped")
	assert.Equal(t, acpConfigOptionIDMode, base.options.groups[0].GetId())
	assert.Equal(t, "plan", base.options.values[acpConfigOptionIDMode])
}

// An unmapped option whose id collides with a reserved proto group key
// (primaryAgent/permissionMode) is never surfaced as a option group -- the mapped
// channel owns that key, and a second group with it would double-list the key.
func TestApplyOptionGroupsLocked_SkipsReservedGroupKeys(t *testing.T) {
	base := acpBase{modeChannel: modeChannelPrimaryAgent}
	options := []acpConfigOption{
		{ID: OptionIDPrimaryAgent, CurrentValue: "x", Options: []acpConfigOptionValue{{Value: "x"}, {Value: "y"}}},
		{ID: OptionIDPermissionMode, CurrentValue: "a", Options: []acpConfigOptionValue{{Value: "a"}, {Value: "b"}}},
	}

	base.mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.mu.Unlock()

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
	var base acpBase
	seed := []acpConfigOption{{ID: "thoughtLevel", Name: "Thought Level", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}}}

	base.mu.Lock()
	base.applyOptionGroupsLocked(seed)
	// A later complete payload carries only the model -- the option no longer applies.
	valueChanged, listChanged := base.applyOptionGroupsLocked([]acpConfigOption{
		{ID: acpConfigOptionIDModel, CurrentValue: "m2", Options: []acpConfigOptionValue{{Value: "m2"}}},
	})
	extras := base.options.mergeOptionValues(nil)
	base.mu.Unlock()

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
	var base acpBase
	base.mu.Lock()
	base.applyOptionGroupsLocked([]acpConfigOption{{ID: "thoughtLevel", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}}})
	valueChanged, listChanged := base.applyOptionGroupsLocked(nil)
	base.mu.Unlock()

	assert.False(t, valueChanged)
	assert.False(t, listChanged)
	require.Len(t, base.options.groups, 1, "an empty payload preserves the stored option")
	assert.Equal(t, "high", base.options.values["thoughtLevel"])
}

// A later full payload that drops a previously-surfaced option (reports a smaller set)
// must emit the dropped id as an explicit "" in the persist extras, so the uniform refresh
// merge DELETES its stale stored value instead of preserving it (an absent key is kept).
func TestMergeExtras_DeletesDroppedOption(t *testing.T) {
	var base acpBase
	base.mu.Lock()
	defer base.mu.Unlock()

	// Seed two options.
	base.applyOptionGroupsLocked([]acpConfigOption{
		{ID: "effort", CurrentValue: "high", Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}},
		{ID: "allow_all", CurrentValue: "on", Options: []acpConfigOptionValue{{Value: "on"}, {Value: "off"}}},
	})
	require.Equal(t, "on", base.options.values["allow_all"])

	// A later full payload reports only effort -- allow_all is dropped.
	base.applyOptionGroupsLocked([]acpConfigOption{
		{ID: "effort", CurrentValue: "low", Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}},
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
	var base acpBase
	base.mu.Lock()
	// A first-sighting option with an empty current and nothing stored: advertised but
	// value-less, so it is known but never surfaces a value.
	base.applyOptionGroupsLocked([]acpConfigOption{
		{ID: "reasoning", Category: "thought_level", Name: "Reasoning", CurrentValue: "",
			Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}},
	})
	known := base.options.known.has("reasoning")
	surfaced := base.options.surfaced.has("reasoning")
	extras := base.options.mergeOptionValues(map[string]string{OptionIDPrimaryAgent: "build"})
	base.mu.Unlock()

	assert.True(t, known, "the advertised id is known, so a persisted preference stays re-pushable")
	assert.False(t, surfaced, "it never surfaced a value, so it is not in surfacedGenericIDs")
	_, emitted := extras["reasoning"]
	assert.False(t, emitted, "no redundant \"\" delete is emitted for a never-valued option")
	assert.Equal(t, "build", extras[OptionIDPrimaryAgent], "the base extras are still carried")
}

// TestBoundedIDSet_LRUEviction verifies the config-option id set caps growth with LRU
// eviction (the backend mirror of settingsLabelCache): the least-recently-used id is evicted
// past the cap, and re-adding an existing id refreshes it (moving it off the eviction front)
// instead of duplicating it. The read methods are nil-safe.
func TestBoundedIDSet_LRUEviction(t *testing.T) {
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
	var base acpBase
	const extra = 50
	options := make([]acpConfigOption, 0, maxOptionStateIDs+extra)
	for i := range maxOptionStateIDs + extra {
		id := fmt.Sprintf("opt-%04d", i)
		options = append(options, acpConfigOption{
			ID:           id,
			Name:         id,
			CurrentValue: "v",
			Options:      []acpConfigOptionValue{{Value: "v"}, {Value: "w"}},
		})
	}

	base.mu.Lock()
	base.applyOptionGroupsLocked(options)
	base.mu.Unlock()

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
// thinking_effort) is declared on acpBase.effortConfigID instead, so a well-known effort id the
// daemon advertises but the running provider did not claim is NOT auto-discovered here -- the guard
// that stops a coincidental second axis from getting the override double-pushed.
func TestThoughtLevelConfigOptionID(t *testing.T) {
	t.Run("non-effort id matched by thought_level category", func(t *testing.T) {
		g := &optionState{templates: map[string]acpConfigOption{
			"thinking": {ID: "thinking", Category: acpConfigOptionCategoryThoughtLevel},
		}}
		assert.Equal(t, "thinking", g.thoughtLevelConfigOptionID())
	})
	t.Run("category-less convention id is NOT auto-discovered", func(t *testing.T) {
		// A well-known effort id without the thought_level category is a provider convention --
		// the provider declares it via effortConfigID; the category-only scan must not claim it,
		// or a coincidental second axis would get the override double-pushed (the S1 hazard).
		g := &optionState{templates: map[string]acpConfigOption{
			"reasoning_effort": {ID: "reasoning_effort"},
			"model":            {ID: "model"},
		}}
		assert.Empty(t, g.thoughtLevelConfigOptionID())
	})
	t.Run("the well-known effort id is excluded even with the category", func(t *testing.T) {
		g := &optionState{templates: map[string]acpConfigOption{
			OptionIDEffort: {ID: OptionIDEffort, Category: acpConfigOptionCategoryThoughtLevel},
		}}
		assert.Empty(t, g.thoughtLevelConfigOptionID(), "no mapping needed when the axis already uses \"effort\"")
	})
	t.Run("no thought_level axis yields empty", func(t *testing.T) {
		g := &optionState{templates: map[string]acpConfigOption{
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
	var base acpBase // empty genericOptionValues -> nothing stored to fall back on
	options := []acpConfigOption{
		{ID: acpConfigOptionIDModel, CurrentValue: "m1", Options: []acpConfigOptionValue{{Value: "m1"}}},
		{ID: "reasoning", Category: "thought_level", Name: "Reasoning", CurrentValue: "",
			Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}},
	}

	base.mu.Lock()
	valueChanged, listChanged := base.applyOptionGroupsLocked(options)
	base.mu.Unlock()

	assert.False(t, valueChanged, "an empty-current first sighting records nothing")
	assert.False(t, listChanged)
	assert.Empty(t, base.options.groups, "no blank-selection group is surfaced")
	_, recorded := base.options.values["reasoning"]
	assert.False(t, recorded, "no empty entry is recorded (it would become a DB delete)")

	// A later payload with a concrete current surfaces it for real.
	base.mu.Lock()
	valueChanged, listChanged = base.applyOptionGroupsLocked([]acpConfigOption{
		{ID: "reasoning", Category: "thought_level", Name: "Reasoning", CurrentValue: "high",
			Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}},
	})
	base.mu.Unlock()
	assert.True(t, valueChanged)
	assert.True(t, listChanged)
	require.Len(t, base.options.groups, 1, "a concrete current surfaces the group")
	assert.Equal(t, "high", base.options.values["reasoning"])
}

// A changed currentValue reports valueChanged; a same-value payload that only adds an
// option reports listChanged.
func TestApplyOptionGroupsLocked_ValueChangeVsListChange(t *testing.T) {
	var base acpBase
	seed := []acpConfigOption{{ID: "thoughtLevel", CurrentValue: "low",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}}}
	base.mu.Lock()
	base.applyOptionGroupsLocked(seed)
	base.mu.Unlock()

	// (1) Same option set, new current value -> valueChanged.
	base.mu.Lock()
	valueChanged, _ := base.applyOptionGroupsLocked([]acpConfigOption{{ID: "thoughtLevel", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}}})
	base.mu.Unlock()
	assert.True(t, valueChanged, "a changed currentValue is a value change")

	// (2) Same current value, an added option -> listChanged only.
	base.mu.Lock()
	valueChanged2, listChanged2 := base.applyOptionGroupsLocked([]acpConfigOption{{ID: "thoughtLevel", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}, {Value: "max"}}}})
	base.mu.Unlock()
	assert.False(t, valueChanged2, "the current value did not change")
	assert.True(t, listChanged2, "a new option is a list change")
}

// mergeOptionValues overlays options onto a base map, lets the base own its
// keys, INCLUDES cleared (empty) surfaced options as explicit "" entries (so the
// uniform refresh merge deletes them), and returns nil only when nothing at all is
// being reported (so the keep-stored contract holds).
func TestMergeExtras(t *testing.T) {
	t.Run("nil when empty", func(t *testing.T) {
		var b acpBase
		assert.Nil(t, b.options.mergeOptionValues(nil))
	})
	t.Run("overlays options onto a nil base", func(t *testing.T) {
		var b acpBase
		b.options.values = map[string]string{"thoughtLevel": "high"}
		assert.Equal(t, optionmap.Map{"thoughtLevel": "high"}, b.options.mergeOptionValues(nil))
	})
	t.Run("base wins over a clashing option key", func(t *testing.T) {
		var b acpBase
		b.options.values = map[string]string{OptionIDPrimaryAgent: "EVIL", "thoughtLevel": "high"}
		got := b.options.mergeOptionValues(map[string]string{OptionIDPrimaryAgent: "build"})
		assert.Equal(t, optionmap.Map{OptionIDPrimaryAgent: "build", "thoughtLevel": "high"}, got,
			"the base map owns primaryAgent; a clashing option value cannot clobber it")
	})
	t.Run("a surfaced-but-empty option clears rather than keeping stored", func(t *testing.T) {
		var b acpBase
		// The option is surfaced (genericOptionValues has the key) but its value was
		// cleared. The empty value is INCLUDED as an explicit "" entry so the uniform
		// refresh merge (present-empty = delete) drops the key; omitting it would
		// instead preserve the stale stored value.
		b.options.values = map[string]string{"thoughtLevel": ""}
		got := b.options.mergeOptionValues(nil)
		assert.Equal(t, optionmap.Map{"thoughtLevel": ""}, got)
	})
	t.Run("a cleared option does not wipe the base key", func(t *testing.T) {
		var b acpBase
		b.options.values = map[string]string{"thoughtLevel": ""}
		got := b.options.mergeOptionValues(map[string]string{OptionIDPrimaryAgent: "build"})
		assert.Equal(t, optionmap.Map{OptionIDPrimaryAgent: "build", "thoughtLevel": ""}, got,
			"the base primary agent survives; the cleared option rides along as \"\" to be deleted downstream")
	})
}

// A runtime config_option_update carrying an unmapped option surfaces it as a
// mutable option group (after the mapped primary-agent group) and persists its
// value into extra_settings alongside the primary agent.
func TestHandleOpenCodeOutput_ConfigOptionUpdateSurfacesGenericGroup(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "openai/gpt-5"
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild

	// No model/mode change; a new thought_level axis appears.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}}}`
	agent.HandleOutput([]byte(input))

	// The option group is surfaced after the mapped primary-agent group.
	groups := agent.OptionGroups()
	require.Len(t, groups, 2)
	assert.Equal(t, OptionIDPrimaryAgent, groups[0].GetId())
	assert.Equal(t, "thoughtLevel", groups[1].GetId())
	assert.Equal(t, "Thought Level", groups[1].GetLabel())

	// A option value change persists via a settings refresh (not a bare status
	// refresh), carrying both the primary agent and the option value.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	extras := sink.LastSettingsRefresh().Options
	assert.Equal(t, OpenCodePrimaryAgentBuild, extras[OptionIDPrimaryAgent])
	assert.Equal(t, "high", extras["thoughtLevel"])
}

// An option list-only change (same currentValue, a new option) broadcasts a status
// refresh, not a settings DB write -- the option analogue of the model/mode list
// channels.
func TestHandleOpenCodeOutput_ConfigOptionUpdateGenericListOnlyBroadcasts(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild
	// Pre-seed a surfaced option (thoughtLevel: low, options {low,high}).
	agent.options.groups = []*leapmuxv1.AvailableOptionGroup{{
		Id:    "thoughtLevel",
		Label: "Thought Level",
		Options: buildOptionValues(acpConfigOption{ID: "thoughtLevel", CurrentValue: "low",
			Options: []acpConfigOptionValue{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}}}, nil),
	}}
	agent.options.values = map[string]string{"thoughtLevel": "low"}

	// Same current value ("low"), but "max" is added; no model/mode option.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","name":"Thought Level","currentValue":"low","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"},{"value":"max","name":"Max"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Len(t, agent.options.groups[0].GetOptions(), 3)
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "no settings DB write when only the option list changed")
	assert.Equal(t, 1, sink.StatusActiveCount(), "the new config option is broadcast via a status refresh")
}

// TestOptionStateStructureGen_TracksStructuralFoldsForLiveBroadcast guards the building block of
// the live-UpdateSettings broadcast decision: structureGen bumps on EVERY fold that changes the
// group-set structure (a group surfacing or dropping) and NOT on a pure current-value change. Live
// UpdateSettings compares the generation before/after its writes instead of diffing the b.options.
// groups slice -- which the reader goroutine can reassign concurrently -- so it never spuriously
// attributes a reader's change to itself nor (the suppression bug) misses its OWN structural change
// when a concurrent reader fold reverts the structure back: each fold moves the counter, so a
// before != after holds even across a net-zero surface-then-drop.
func TestOptionStateStructureGen_TracksStructuralFoldsForLiveBroadcast(t *testing.T) {
	agent := newOpenCodeAgentWithSink(&testSink{})
	agent.model = "openai/gpt-5"
	agent.availableModels = []*ModelInfo{{Id: "openai/gpt-5", DisplayName: "GPT-5", IsDefault: true}}

	gen0 := agent.options.structureGen

	// Fold 1: surface a new thoughtLevel group -- a structural change.
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}}}`))
	gen1 := agent.options.structureGen
	require.Len(t, agent.options.groups, 1)
	assert.Greater(t, gen1, gen0, "surfacing a new group is a structural fold")

	// Fold 2: change ONLY the current value (same group, same option set) -- not structural.
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"low","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}}}`))
	gen2 := agent.options.structureGen
	assert.Equal(t, "low", agent.options.groups[0].GetCurrentValue(), "the value-only fold landed")
	assert.Equal(t, gen1, gen2, "a pure current-value change is not a structural fold")

	// Fold 3: a complete payload that no longer carries thoughtLevel drops the group -- structural,
	// and it returns the set to its original (empty) structure. The generation must STILL move, so a
	// live UpdateSettings that surfaced the group sees before != after even though a concurrent
	// reader fold reverted the structure (the net-comparison suppression this fix closes).
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"openai/gpt-5","name":"GPT-5"}]}]}}}`))
	gen3 := agent.options.structureGen
	assert.Empty(t, agent.options.groups, "the dropped group is gone")
	assert.Greater(t, gen3, gen2, "dropping a group is a structural fold even when it restores the prior structure")
}

// Membership-varies at the agent level: a complete config_option_update for a different
// model that no longer carries a previously-surfaced option (the new model doesn't
// support it) drops the option AND deletes it from the persisted extras --
// broadcastSettingsRefresh replaces stored extras wholesale, and the dropped option rides
// along as "" so it is removed, not kept.
func TestHandleOpenCodeOutput_ConfigOptionUpdateDropsNoLongerApplicableGeneric(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "openai/gpt-5"
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild
	agent.availableModels = []*ModelInfo{{Id: "openai/gpt-5", DisplayName: "GPT-5", IsDefault: true}}
	agent.options.groups = []*leapmuxv1.AvailableOptionGroup{{
		Id:    "thoughtLevel",
		Label: "Thought Level",
		Options: buildOptionValues(acpConfigOption{ID: "thoughtLevel", CurrentValue: "high",
			Options: []acpConfigOptionValue{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}}}, nil),
	}}
	agent.options.values = map[string]string{"thoughtLevel": "high"}
	// Mirror what applyOptionGroupsLocked records in production when an option
	// surfaces with a value, so the drop emits a delete for it.
	agent.options.markSurfaced("thoughtLevel")

	// A complete config_option_update switches to a model that no longer carries the
	// thoughtLevel option -- per the verified ACP contract this is the complete current
	// set, so the absent option no longer applies and must be dropped.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"openai/gpt-5","name":"GPT-5"},{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, "anthropic/claude-sonnet-4", agent.model)
	assert.Empty(t, agent.options.groups, "the no-longer-applicable option is dropped")
	_, live := agent.options.values["thoughtLevel"]
	assert.False(t, live, "the dropped option is gone from the live values")
	// The model switch persists; the dropped option is deleted from extras (rides as "").
	require.Equal(t, 1, sink.SettingsRefreshCount())
	extras := sink.LastSettingsRefresh().Options
	assert.Equal(t, OpenCodePrimaryAgentBuild, extras[OptionIDPrimaryAgent])
	assert.Equal(t, "", extras["thoughtLevel"], "the dropped option is deleted from extras, not kept")
}

// A settings key that was never surfaced as a config option (no handshake
// reported it) is structurally ignored by UpdateSettings: applyOptionUpdates
// only writes ids present in genericOptionValues, so no set_config_option RPC fires
// for an unknown key and the write still succeeds. (A surfaced config option, by
// contrast, IS writable -- see TestACPConfigOption_MutableUpdateRoundTrips.)
func TestPrimaryAgentUpdateSettings_IgnoresUnknownExtraKey(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPC(t)

	ok := agent.UpdateSettings(map[string]string{
		OptionIDModel:  "openai/gpt-5",
		"thoughtLevel": "high",
	})

	require.True(t, ok)
	recorded := requests()
	require.Len(t, recorded, 1, "only the model RPC fires; the unsurfaced extra key sends nothing")
	assert.Equal(t, acpMethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, acpConfigOptionIDModel, recorded[0].Params["configId"])
}

// TestSecondaryChannel_DerivesFromModeChannel locks in the single source of truth the
// unified UpdateSettings / reapply / refresh paths rely on: modeChannel alone determines
// the secondary axis's option id, field, log key, available-list pointer, persist shape, and
// config-override presence. The permission-mode and primary-agent families are distinct, and
// the unmapped zero value defaults to permission mode (with no config override).
func TestSecondaryChannel_DerivesFromModeChannel(t *testing.T) {
	pm := acpBase{modeChannel: modeChannelPermissionMode}
	scPM := pm.secondaryChannel()
	assert.Equal(t, OptionIDPermissionMode, scPM.optionID)
	assert.Equal(t, "permissionMode", scPM.logKey)
	assert.Same(t, &pm.permissionMode, scPM.field, "permission-mode family points at b.permissionMode")
	assert.Same(t, &pm.availableModes, scPM.available, "permission-mode family reads b.availableModes")
	// A permission-mode provider carries the secondary in PersistSettingsRefresh's mode arg,
	// NOT in the option values.
	pmBase, pmMode := scPM.persistShape("plan")
	assert.Nil(t, pmBase, "permission mode contributes no option-values base")
	assert.Equal(t, "plan", pmMode, "permission mode is carried in the persist mode arg")
	assert.NotNil(t, scPM.syncConfigOverride, "permission mode has a configOptions override")

	pa := acpBase{modeChannel: modeChannelPrimaryAgent}
	scPA := pa.secondaryChannel()
	assert.Equal(t, OptionIDPrimaryAgent, scPA.optionID)
	assert.Equal(t, "primaryAgent", scPA.logKey)
	assert.Same(t, &pa.currentPrimaryAgent, scPA.field, "primary-agent family points at b.currentPrimaryAgent")
	assert.Same(t, &pa.availablePrimaryAgents, scPA.available, "primary-agent family reads b.availablePrimaryAgents")
	// A primary-agent provider carries the secondary in the option values, NOT the mode arg.
	paBase, paMode := scPA.persistShape("build")
	assert.Equal(t, map[string]string{OptionIDPrimaryAgent: "build"}, paBase, "primary agent is carried in the option-values base")
	assert.Empty(t, paMode, "primary agent contributes no persist mode arg")
	assert.NotNil(t, scPA.syncConfigOverride, "primary agent has a configOptions override")

	var unmapped acpBase // modeChannelUnmapped zero value
	scU := unmapped.secondaryChannel()
	assert.Equal(t, OptionIDPermissionMode, scU.optionID,
		"the unmapped default is the permission-mode channel")
	assert.Nil(t, scU.syncConfigOverride,
		"the unmapped channel has no config override (reproduces the old switch's no-default no-op)")
}

// TestEffectiveSetModel_FallsBackToBaseSetter verifies the model writer defaults to the
// base setModel when no provider override is set (Cursor sets modelSetter to its
// wire-mapping setter; every other ACP provider leaves it nil).
func TestEffectiveSetModel_FallsBackToBaseSetter(t *testing.T) {
	var b acpBase
	assert.NotNil(t, b.effectiveSetModel(), "a nil modelSetter falls back to the base setModel")

	called := false
	b.modelSetter = func(string) error { called = true; return nil }
	_ = b.effectiveSetModel()("x")
	assert.True(t, called, "a set modelSetter override is used")
}
