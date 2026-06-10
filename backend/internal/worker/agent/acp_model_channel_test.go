package agent

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// trySetStartupModel applies a requested model best-effort: it pushes when the
// request differs from the server's current (including when the server reports
// none), is a no-op when they match, and keeps the current model on rejection.

func TestTrySetStartupModel_PushesWhenServerReportsNoModel(t *testing.T) {
	var base acpBase
	got := ""
	base.trySetStartupModel("user/arbitrary", func(m string) error {
		got = m
		base.model = m
		return nil
	})
	assert.Equal(t, "user/arbitrary", got, "an arbitrary model must be pushed when the server advertises none")
	assert.Equal(t, "user/arbitrary", base.model)
}

func TestTrySetStartupModel_NoopWhenMatchesCurrent(t *testing.T) {
	base := acpBase{}
	base.model = "anthropic/claude-sonnet-4"
	called := false
	base.trySetStartupModel("anthropic/claude-sonnet-4", func(string) error {
		called = true
		return nil
	})
	assert.False(t, called, "no setModel when the request already matches the server's current")
}

func TestTrySetStartupModel_NonFatalOnRejection(t *testing.T) {
	base := acpBase{}
	base.providerName = "opencode"
	base.model = "server/current"
	base.trySetStartupModel("user/arbitrary", func(string) error {
		return errors.New("unknown model")
	})
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
	assert.Equal(t, "Auto", models[0].GetDisplayName()) // first occurrence wins
	assert.True(t, models[0].GetIsDefault())
	assert.Equal(t, "gpt-5", models[1].GetId())
}

// A server that repeats a model id within a single channel yields one entry.
func TestBuildACPModels_DedupsRepeatedID(t *testing.T) {
	models := buildACPModels([]acpModelInfo{
		{ModelID: "m1", Name: "Model 1"},
		{ModelID: "m1", Name: "Model 1 (dup)"},
	}, "m1", nil)

	require.Len(t, models, 1)
	assert.Equal(t, "Model 1", models[0].GetDisplayName())
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

// A provider with an unmapped mode channel (Gemini) does NOT apply the configOptions
// `mode` override at handshake -- it keeps the modes-channel value and leaves the
// option to be surfaced read-only, so the handshake resolves the mode the same way the
// runtime and ClearContext paths do (which also gate the override on the mode channel)
// rather than applying it writably here but read-only everywhere else.
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

// S1 end-to-end: a provider with an unmapped mode channel (Gemini) whose handshake
// carries BOTH a modes channel and a configOptions `mode` must (a) keep the
// modes-channel permission mode -- the configOptions override is NOT applied writably --
// and (b) surface the configOptions `mode` as a read-only generic group rather than
// dropping it. This is the seam the S1 gate protects: applyHandshakeModels surfaces the
// generic and the gated applyHandshakeMode leaves the mode on the modes channel, so the
// option is neither double-applied (writable + read-only) nor silently dropped, matching
// how the runtime and ClearContext paths resolve it.
func TestUnmappedProvider_HandshakeConfigMode_SurfacedReadOnlyNotDoubleApplied(t *testing.T) {
	var base acpBase // modeChannelUnmapped (Gemini-like)
	handshake := &acpSessionResult{
		CurrentModeID: "default",
		Modes:         []acpModeInfo{{ID: "default", Name: "Default"}, {ID: "plan", Name: "Plan"}},
		// The modes channel says "default" but the configOptions `mode` says "plan".
		ConfigOptions: []acpConfigOption{{
			ID: acpConfigOptionIDMode, CurrentValue: "plan",
			Options: []acpConfigOptionValue{{Value: "default", Name: "Default"}, {Value: "plan", Name: "Plan"}},
		}},
	}

	base.applyHandshakeModels(handshake) // surfaces the unmapped `mode` as a generic group
	base.applyHandshakeMode(handshake, "fallback")

	// (a) The permission mode stays on the modes channel; the configOptions "plan" override
	// is gated out for the unmapped provider.
	assert.Equal(t, "default", base.permissionMode)
	// (b) The configOptions `mode` is surfaced read-only, keyed by its id and carrying its
	// current value -- not dropped, not folded into the writable permission mode.
	require.Len(t, base.genericOptionGroups, 1)
	assert.Equal(t, acpConfigOptionIDMode, base.genericOptionGroups[0].GetKey())
	assert.Equal(t, "plan", base.genericOptionValues[acpConfigOptionIDMode])
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
	assert.True(t, agent.availableModels[1].GetIsDefault())
	// The `mode` config option carries the primary agent for OpenCode; a runtime
	// config_option_update syncs it alongside the model.
	assert.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	// The runtime model + primary-agent switch is broadcast once so the frontend
	// reflects it, carrying the new primary-agent extra (OpenCode has no permission mode).
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "openai/gpt-5", refresh.Model)
	assert.Equal(t, OpenCodePrimaryAgentPlan, refresh.ExtraSettings[OptionGroupKeyPrimaryAgent])
}

// An idempotent config_option_update -- same current model AND same list as the
// agent already holds -- must trigger no broadcast at all (no settings write, no
// status refresh).
func TestHandleOpenCodeOutput_ConfigOptionUpdateNoBroadcastWhenUnchanged(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "openai/gpt-5"
	// Pre-seed the exact list the update carries so nothing actually changes.
	agent.availableModels = []*leapmuxv1.AvailableModel{
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
	agent.availableModels = []*leapmuxv1.AvailableModel{
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
	assert.True(t, base.availableModels[1].GetIsDefault()) // current comes from the config option
	assert.Equal(t, "cfg/a", base.model)
}

// A provider models decorator (Gemini's synthetic "auto") must run on the runtime
// model channel too, so the handshake and runtime model lists stay consistent.
func TestApplyConfigOptionModels_AppliesModelsDecorator(t *testing.T) {
	var base acpBase
	base.modelsDecorator = geminiEnsureAuto

	options := []acpConfigOption{{
		ID: acpConfigOptionIDModel, CurrentValue: "gemini-2.5-pro",
		Options: []acpConfigOptionValue{
			{Value: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
			{Value: "gemini-2.5-flash", Name: "Gemini 2.5 Flash"},
		},
	}}
	base.mu.Lock()
	modelChanged, listChanged := base.applyConfigOptionModelsLocked(options)
	base.mu.Unlock()

	assert.True(t, modelChanged)
	assert.True(t, listChanged)
	require.Len(t, base.availableModels, 3)
	assert.Equal(t, "auto", base.availableModels[0].GetId())
	assert.Equal(t, "gemini-2.5-pro", base.model)
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
	assert.True(t, agent.availableModels[1].GetIsDefault())
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
		{Id: OpenCodePrimaryAgentBuild, Name: "Build", IsDefault: true},
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
		{Id: OpenCodePrimaryAgentBuild, Name: "Build", IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: "Plan"},
	}

	// currentValue is the hidden "compaction" pseudo-agent.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"compaction","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"},{"value":"compaction","name":"Compaction"}]}]}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, OpenCodePrimaryAgentBuild, agent.currentPrimaryAgent,
		"a hidden currentValue is not adopted as the current primary agent")
	require.Len(t, agent.availablePrimaryAgents, 2, "compaction is filtered from the list")
}

// primaryAgentExtras returns nil (not an empty map) for an empty agent so a
// settings refresh preserves stored extras instead of clearing them; a
// non-empty agent yields the single-key map.
func TestPrimaryAgentExtras(t *testing.T) {
	assert.Nil(t, primaryAgentExtras(""),
		"empty agent must yield nil so PersistSettingsRefresh keeps stored extras")
	assert.Equal(t, map[string]string{OptionGroupKeyPrimaryAgent: "build"}, primaryAgentExtras("build"))
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
	agent.availableModels = []*leapmuxv1.AvailableModel{{Id: "anthropic/claude-sonnet-4"}}

	// `mode` select changes build -> plan and lists a hidden pseudo-agent.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"plan","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"},{"value":"compaction","name":"Compaction"}]}]}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	// The hidden `compaction` pseudo-agent is filtered from the rebuilt list.
	require.Len(t, agent.availablePrimaryAgents, 2)
	// The change is broadcast once, carrying the new primary-agent extra.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, OpenCodePrimaryAgentPlan, sink.LastSettingsRefresh().ExtraSettings[OptionGroupKeyPrimaryAgent])
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
	agent.availableModels = []*leapmuxv1.AvailableModel{{Id: "anthropic/claude-sonnet-4"}}
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: "Build", IsDefault: true},
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
	assert.Equal(t, OpenCodePrimaryAgentBuild, sink.LastSettingsRefresh().ExtraSettings[OptionGroupKeyPrimaryAgent])
}

// --- reconcileCurrentOptionID: the shared current-selection resolver ---

// reconcileCurrentOptionID resolves a secondary channel's current against a freshly built
// option list: an empty list means "unreported" so the reported (else stored) value is
// trusted unchanged; otherwise a valid reported value is adopted, a still-valid stored
// value is kept, and failing both the current re-seeds to the list's default-or-first
// option. The handshake, runtime, and ClearContext seams all route through it.
func TestReconcileCurrentOptionID(t *testing.T) {
	opts := func(ids ...string) []*leapmuxv1.AvailableOption {
		built := make([]*leapmuxv1.AvailableOption, 0, len(ids))
		for _, id := range ids {
			built = append(built, &leapmuxv1.AvailableOption{Id: id})
		}
		return built
	}
	withDefault := []*leapmuxv1.AvailableOption{
		{Id: "build"},
		{Id: "plan", IsDefault: true},
		{Id: "review"},
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
		// Both reported and stored absent: re-seed to the default option when one is marked.
		{"re-seeds to default when both absent", withDefault, "ghost", "stale", "plan"},
		// Both absent and no default marked: re-seed to the first non-empty option.
		{"re-seeds to first when no default", opts("build", "plan"), "ghost", "stale", "build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, reconcileCurrentOptionID(tc.available, tc.reported, tc.stored))
		})
	}
}

// defaultOrFirstOption returns the default option's id, else the first non-empty id,
// else "" -- skipping nil entries and empty ids on both passes.
func TestDefaultOrFirstOption(t *testing.T) {
	assert.Equal(t, "", defaultOrFirstOption(nil), "no options -> empty")
	assert.Equal(t, "build", defaultOrFirstOption([]*leapmuxv1.AvailableOption{
		{Id: "build"},
		{Id: "plan"},
	}), "no default marked -> first non-empty id")
	assert.Equal(t, "plan", defaultOrFirstOption([]*leapmuxv1.AvailableOption{
		{Id: "build"},
		{Id: "plan", IsDefault: true},
	}), "default marked -> the default id, even when not first")
	assert.Equal(t, "plan", defaultOrFirstOption([]*leapmuxv1.AvailableOption{
		nil,
		{Id: ""},
		{Id: "plan"},
	}), "skips nil entries and empty ids when picking the first")
	assert.Equal(t, "real", defaultOrFirstOption([]*leapmuxv1.AvailableOption{
		{Id: "", IsDefault: true}, // an empty-id default is ignored
		{Id: "real"},
	}), "an empty-id default is skipped in favor of the first non-empty id")
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

// --- Generic surfacing of unmapped config options (read-only) ---

// A handshake carrying a third axis (thought_level) surfaces it as a read-only
// generic group keyed by id, while the claimed model and mode options are excluded
// (no double-render).
func TestApplyGenericConfigOptionsLocked_SurfacesUnmappedOption(t *testing.T) {
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
	valueChanged, listChanged := base.applyGenericConfigOptionsLocked(options)
	base.mu.Unlock()

	assert.True(t, valueChanged)
	assert.True(t, listChanged)
	require.Len(t, base.genericOptionGroups, 1, "only the unmapped option surfaces; model and mode are excluded")
	group := base.genericOptionGroups[0]
	assert.Equal(t, "thoughtLevel", group.GetKey())
	assert.Equal(t, "Thought Level", group.GetLabel())
	require.Len(t, group.GetOptions(), 2)
	assert.Equal(t, "high", base.genericOptionValues["thoughtLevel"])
	assert.True(t, group.GetOptions()[1].GetIsDefault(), "the currentValue marks the default option")
}

// An unmapped option declared with no name and no category (just a distinct id) still
// surfaces, labelled by its id.
func TestApplyGenericConfigOptionsLocked_SurfacesIDOnlyOption(t *testing.T) {
	var base acpBase
	options := []acpConfigOption{
		{ID: acpConfigOptionIDModel, CurrentValue: "m1", Options: []acpConfigOptionValue{{Value: "m1"}}},
		{ID: "reasoning", CurrentValue: "medium", Options: []acpConfigOptionValue{{Value: "low"}, {Value: "medium"}}},
	}

	base.mu.Lock()
	base.applyGenericConfigOptionsLocked(options)
	base.mu.Unlock()

	require.Len(t, base.genericOptionGroups, 1)
	assert.Equal(t, "reasoning", base.genericOptionGroups[0].GetKey())
	assert.Equal(t, "reasoning", base.genericOptionGroups[0].GetLabel(), "a nameless option is labelled by its id")
}

// The claimed mode option -- even when declared via `category` with a non-literal id
// -- is never surfaced as a generic group, and a payload with no unmapped option
// leaves the stored state untouched (keep-stored guard).
func TestApplyGenericConfigOptionsLocked_ExcludesClaimedModeByCategory(t *testing.T) {
	// A permission-mode provider consumes the mode channel, so its mode option is
	// claimed by category and excluded from the generic groups.
	base := acpBase{modeChannel: modeChannelPermissionMode}
	options := []acpConfigOption{
		{ID: "opaque-mode", Category: acpConfigOptionCategoryMode, CurrentValue: "plan",
			Options: []acpConfigOptionValue{{Value: "build"}, {Value: "plan"}}},
	}

	base.mu.Lock()
	valueChanged, listChanged := base.applyGenericConfigOptionsLocked(options)
	base.mu.Unlock()

	assert.False(t, valueChanged)
	assert.False(t, listChanged)
	assert.Empty(t, base.genericOptionGroups, "the claimed mode is not double-rendered as a generic group")
}

// A provider that consumes neither channel (Gemini) does NOT claim a mode option, so
// rather than silently dropping it, the mode surfaces as a read-only generic group.
func TestApplyGenericConfigOptionsLocked_SurfacesUnconsumedModeForNonSyncingProvider(t *testing.T) {
	var base acpBase // modeChannel stays modeChannelUnmapped (Gemini-like)
	options := []acpConfigOption{
		{ID: acpConfigOptionIDMode, Category: acpConfigOptionCategoryMode, CurrentValue: "plan",
			Options: []acpConfigOptionValue{{Value: "build"}, {Value: "plan"}}},
	}

	base.mu.Lock()
	valueChanged, listChanged := base.applyGenericConfigOptionsLocked(options)
	base.mu.Unlock()

	assert.True(t, valueChanged)
	assert.True(t, listChanged)
	require.Len(t, base.genericOptionGroups, 1, "an unconsumed mode option is surfaced, not dropped")
	assert.Equal(t, acpConfigOptionIDMode, base.genericOptionGroups[0].GetKey())
	assert.Equal(t, "plan", base.genericOptionValues[acpConfigOptionIDMode])
}

// An unmapped option whose id collides with a reserved proto group key
// (primaryAgent/permissionMode) is never surfaced as a generic group -- the mapped
// channel owns that key, and a second group with it would double-list the key.
func TestApplyGenericConfigOptionsLocked_SkipsReservedGroupKeys(t *testing.T) {
	base := acpBase{modeChannel: modeChannelPrimaryAgent}
	options := []acpConfigOption{
		{ID: OptionGroupKeyPrimaryAgent, CurrentValue: "x", Options: []acpConfigOptionValue{{Value: "x"}, {Value: "y"}}},
		{ID: OptionGroupKeyPermissionMode, CurrentValue: "a", Options: []acpConfigOptionValue{{Value: "a"}, {Value: "b"}}},
	}

	base.mu.Lock()
	valueChanged, listChanged := base.applyGenericConfigOptionsLocked(options)
	base.mu.Unlock()

	assert.False(t, valueChanged)
	assert.False(t, listChanged)
	assert.Empty(t, base.genericOptionGroups, "a reserved-key option is never surfaced as a generic group")
}

// The keep-stored wipe trap: a model-only update (the common runtime case) must not
// wipe previously surfaced generics.
func TestApplyGenericConfigOptionsLocked_KeepsStoredOnModelOnlyUpdate(t *testing.T) {
	var base acpBase
	seed := []acpConfigOption{{ID: "thoughtLevel", Name: "Thought Level", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}}}

	base.mu.Lock()
	base.applyGenericConfigOptionsLocked(seed)
	// A later model-only update carries no unmapped option.
	valueChanged, listChanged := base.applyGenericConfigOptionsLocked([]acpConfigOption{
		{ID: acpConfigOptionIDModel, CurrentValue: "m2", Options: []acpConfigOptionValue{{Value: "m2"}}},
	})
	base.mu.Unlock()

	assert.False(t, valueChanged)
	assert.False(t, listChanged)
	require.Len(t, base.genericOptionGroups, 1, "the model-only update must not wipe the stored generic")
	assert.Equal(t, "high", base.genericOptionValues["thoughtLevel"])
}

// A changed currentValue reports valueChanged; a same-value payload that only adds an
// option reports listChanged.
func TestApplyGenericConfigOptionsLocked_ValueChangeVsListChange(t *testing.T) {
	var base acpBase
	seed := []acpConfigOption{{ID: "thoughtLevel", CurrentValue: "low",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}}}
	base.mu.Lock()
	base.applyGenericConfigOptionsLocked(seed)
	base.mu.Unlock()

	// (1) Same option set, new current value -> valueChanged.
	base.mu.Lock()
	valueChanged, _ := base.applyGenericConfigOptionsLocked([]acpConfigOption{{ID: "thoughtLevel", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}}}})
	base.mu.Unlock()
	assert.True(t, valueChanged, "a changed currentValue is a value change")

	// (2) Same current value, an added option -> listChanged only.
	base.mu.Lock()
	valueChanged2, listChanged2 := base.applyGenericConfigOptionsLocked([]acpConfigOption{{ID: "thoughtLevel", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "low"}, {Value: "high"}, {Value: "max"}}}})
	base.mu.Unlock()
	assert.False(t, valueChanged2, "the current value did not change")
	assert.True(t, listChanged2, "a new option is a list change")
}

// mergeGenericExtrasLocked overlays generics onto a base map, lets the base own its
// keys, skips empty generic values, and returns nil only when nothing at all is being
// reported (so the keep-stored contract holds).
func TestMergeGenericExtrasLocked(t *testing.T) {
	t.Run("nil when empty", func(t *testing.T) {
		var b acpBase
		assert.Nil(t, b.mergeGenericExtrasLocked(nil))
	})
	t.Run("overlays generics onto a nil base", func(t *testing.T) {
		var b acpBase
		b.genericOptionValues = map[string]string{"thoughtLevel": "high"}
		assert.Equal(t, map[string]string{"thoughtLevel": "high"}, b.mergeGenericExtrasLocked(nil))
	})
	t.Run("base wins over a clashing generic key", func(t *testing.T) {
		var b acpBase
		b.genericOptionValues = map[string]string{OptionGroupKeyPrimaryAgent: "EVIL", "thoughtLevel": "high"}
		got := b.mergeGenericExtrasLocked(map[string]string{OptionGroupKeyPrimaryAgent: "build"})
		assert.Equal(t, map[string]string{OptionGroupKeyPrimaryAgent: "build", "thoughtLevel": "high"}, got,
			"the base map owns primaryAgent; a clashing generic value cannot clobber it")
	})
	t.Run("a surfaced-but-empty generic clears rather than keeping stored", func(t *testing.T) {
		var b acpBase
		// The option is surfaced (genericOptionValues has the key) but its value was
		// cleared. The empty value is skipped from the map, yet the result is a non-nil
		// empty map -- not nil -- so the persist path replaces the stored extras and the
		// stale value doesn't linger. Returning nil here would keep the stale value.
		b.genericOptionValues = map[string]string{"thoughtLevel": ""}
		got := b.mergeGenericExtrasLocked(nil)
		require.NotNil(t, got)
		assert.Empty(t, got)
	})
	t.Run("a cleared generic does not wipe the base key", func(t *testing.T) {
		var b acpBase
		b.genericOptionValues = map[string]string{"thoughtLevel": ""}
		got := b.mergeGenericExtrasLocked(map[string]string{OptionGroupKeyPrimaryAgent: "build"})
		assert.Equal(t, map[string]string{OptionGroupKeyPrimaryAgent: "build"}, got,
			"the base primary agent survives while the cleared generic is dropped")
	})
}

// A runtime config_option_update carrying an unmapped option surfaces it as a
// read-only generic group (after the mapped primary-agent group) and persists its
// value into extra_settings alongside the primary agent.
func TestHandleOpenCodeOutput_ConfigOptionUpdateSurfacesGenericGroup(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "openai/gpt-5"
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild

	// No model/mode change; a new thought_level axis appears.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}}}`
	agent.HandleOutput([]byte(input))

	// The generic group is surfaced after the mapped primary-agent group.
	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 2)
	assert.Equal(t, OptionGroupKeyPrimaryAgent, groups[0].GetKey())
	assert.Equal(t, "thoughtLevel", groups[1].GetKey())
	assert.Equal(t, "Thought Level", groups[1].GetLabel())

	// A generic value change persists via a settings refresh (not a bare status
	// refresh), carrying both the primary agent and the generic value.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	extras := sink.LastSettingsRefresh().ExtraSettings
	assert.Equal(t, OpenCodePrimaryAgentBuild, extras[OptionGroupKeyPrimaryAgent])
	assert.Equal(t, "high", extras["thoughtLevel"])
}

// A generic list-only change (same currentValue, a new option) broadcasts a status
// refresh, not a settings DB write -- the generic analogue of the model/mode list
// channels.
func TestHandleOpenCodeOutput_ConfigOptionUpdateGenericListOnlyBroadcasts(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild
	// Pre-seed a surfaced generic (thoughtLevel: low, options {low,high}).
	agent.genericOptionGroups = []*leapmuxv1.AvailableOptionGroup{{
		Key:   "thoughtLevel",
		Label: "Thought Level",
		Options: buildOptionValues(acpConfigOption{ID: "thoughtLevel", CurrentValue: "low",
			Options: []acpConfigOptionValue{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}}}, nil),
	}}
	agent.genericOptionValues = map[string]string{"thoughtLevel": "low"}

	// Same current value ("low"), but "max" is added; no model/mode option.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"thoughtLevel","name":"Thought Level","currentValue":"low","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"},{"value":"max","name":"Max"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Len(t, agent.genericOptionGroups[0].GetOptions(), 3)
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "no settings DB write when only the generic list changed")
	assert.Equal(t, 1, sink.StatusActiveCount(), "the new generic option is broadcast via a status refresh")
}

// The wipe trap at the agent level: a model-only update switches the model but must
// keep the stored generic, and the model refresh re-includes the generic value in
// extras (broadcastSettingsRefresh replaces stored extras wholesale).
func TestHandleOpenCodeOutput_ConfigOptionUpdateModelOnlyKeepsGenerics(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.model = "openai/gpt-5"
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild
	agent.availableModels = []*leapmuxv1.AvailableModel{{Id: "openai/gpt-5", DisplayName: "GPT-5", IsDefault: true}}
	agent.genericOptionGroups = []*leapmuxv1.AvailableOptionGroup{{
		Key:   "thoughtLevel",
		Label: "Thought Level",
		Options: buildOptionValues(acpConfigOption{ID: "thoughtLevel", CurrentValue: "high",
			Options: []acpConfigOptionValue{{Value: "low", Name: "Low"}, {Value: "high", Name: "High"}}}, nil),
	}}
	agent.genericOptionValues = map[string]string{"thoughtLevel": "high"}

	// A model-only update switches the model; it must NOT wipe the stored generic.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"openai/gpt-5","name":"GPT-5"},{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, "anthropic/claude-sonnet-4", agent.model)
	require.Len(t, agent.genericOptionGroups, 1, "the generic survives the model-only update")
	assert.Equal(t, "high", agent.genericOptionValues["thoughtLevel"])
	// The model switch persists, carrying the surviving generic value in extras.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	extras := sink.LastSettingsRefresh().ExtraSettings
	assert.Equal(t, OpenCodePrimaryAgentBuild, extras[OptionGroupKeyPrimaryAgent])
	assert.Equal(t, "high", extras["thoughtLevel"], "the model refresh re-includes the stored generic value")
}

// primaryAgentUpdateSettings reads only the model and the primaryAgent extra key, so
// an unknown extras key (a future generic axis) is structurally ignored: no RPC is
// sent for it and the write still succeeds. This is the read-only guarantee for the
// surfaced generic groups -- they display and sync but never write back.
func TestPrimaryAgentUpdateSettings_IgnoresUnknownExtraKey(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPC(t)

	ok := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:         "openai/gpt-5",
		ExtraSettings: map[string]string{"thoughtLevel": "high"},
	})

	require.True(t, ok)
	recorded := requests()
	require.Len(t, recorded, 1, "only the model RPC fires; the unknown extra key sends nothing")
	assert.Equal(t, acpMethodSessionSetModel, recorded[0].Method)
}
