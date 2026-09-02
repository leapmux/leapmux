package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zcodeApplySettings folds a settings JSON document into an agent, the way every
// state-changing RPC reply and every session-scope patch does.
func zcodeApplySettings(t *testing.T, a *zcodeAgent, settingsJSON string) {
	t.Helper()
	var snap zcodeSettingsSnapshot
	require.NoError(t, json.Unmarshal([]byte(settingsJSON), &snap))
	a.mu.Lock()
	a.applySettingsSnapshotLocked(&snap)
	a.mu.Unlock()
}

// --- the mode axis ---

// `auto` is in the app-server's own enumeration and is NOT implemented in the
// shipped build: every tool call under it is denied. Offering it would give the user
// a mode in which nothing works.
func TestZCodeStaticOptionGroups_ModeListExcludesAuto(t *testing.T) {
	t.Parallel()

	group := optionids.GroupByID(
		AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE), OptionIDPermissionMode)
	require.NotNil(t, group)
	assert.Equal(t, ZCodeModeLabel, group.GetLabel())
	assert.Equal(t, contracts.ZCodeDefaultMode, group.GetDefaultValue())
	assert.True(t, group.GetMutable())

	ids := []string{}
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
		assert.NotEmpty(t, option.GetName(), "every mode needs a display name")
		assert.NotEmpty(t, option.GetDescription(), "every mode needs a description")
	}
	assert.Equal(t, []string{contracts.ZCodeModePlan, contracts.ZCodeModeBuild, contracts.ZCodeModeEdit, contracts.ZCodeModeYolo}, ids)
	assert.NotContains(t, ids, "auto")
}

// The model list is deliberately empty before an agent runs: every ZCode model comes
// from the user's own configuration, so a hardcoded entry would name a model a given
// installation does not have.
func TestZCodeFallbackModels_IsEmpty(t *testing.T) {
	t.Parallel()

	assert.Empty(t, zcodeFallbackModels)
}

// --- the settings snapshot ---

func TestApplySettingsSnapshot_ReadsTheAuthoritativeCurrentFields(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	zcodeApplySettings(t, a, `{
      "appliedProviderRevision": "rev-1",
      "mode": {"current": "plan"},
      "model": {"current": {"providerId": "builtin:zai", "modelId": "GLM-5.3"}},
      "thoughtLevel": {"enabled": true, "current": "high", "defaultLevel": "low",
                       "available": [{"value":"low","label":"Low"},{"value":"high","label":"High"}]}
    }`)

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, contracts.ZCodeModePlan, a.mode)
	assert.Equal(t, "builtin:zai/GLM-5.3", a.model)
	assert.Equal(t, "high", a.thoughtLevel)
	assert.Equal(t, "low", a.observedThoughtDefault)

	ids := effortIDs(a.observedThoughtLevels)
	assert.Equal(t, []string{EffortAuto, "high", "low"}, ids,
		"auto leads the list as the send-nothing sentinel, then the levels strongest first -- the same order the configured catalog uses, so the menu does not reorder itself when the first snapshot lands")
}

// The two lists of levels are built in two places -- the configured catalog
// (zcodeModelInfo) and this snapshot -- and the second REPLACES the first for the
// running model. Ordered differently, the menu would reorder itself the moment
// the agent reported its first snapshot, under a user reading it.
func TestApplySettingsSnapshot_OrdersTheLevelsLikeTheConfiguredCatalog(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {"builtin:zai": {"kind": "openai", "options": {"apiKey": "k"},
        "models": {"GLM-5.3": {"reasoning": {"enabled": true, "variants": ["low", "max", "high"]}}}}}
    }`)
	require.Len(t, catalog.Models, 1)
	fromConfig := []string{}
	for _, level := range catalog.Models[0].SupportedEfforts {
		fromConfig = append(fromConfig, level.GetId())
	}

	a := newZCodeTestAgent(t, &recordingControlSink{})
	// The app-server reports the levels in the model's own configured order, which
	// is exactly the order that states no ladder.
	zcodeApplySettings(t, a, `{
      "thoughtLevel": {"enabled": true, "current": "high",
                       "available": [{"value":"low"},{"value":"max"},{"value":"high"}]}
    }`)

	a.mu.Lock()
	defer a.mu.Unlock()
	fromSnapshot := effortIDs(a.observedThoughtLevels)
	assert.Equal(t, []string{EffortAuto, "max", "high", "low"}, fromSnapshot)
	assert.Equal(t, fromConfig, fromSnapshot, "the live list must not reorder the menu")
}

// A patch carries only the axes that changed. An absent axis must leave the agent's
// value alone, which is why every axis is a pointer.
func TestApplySettingsSnapshot_APartialPatchLeavesTheOtherAxesAlone(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.mu.Lock()
	a.model, a.thoughtLevel, a.mode = "p/m", "high", contracts.ZCodeModeBuild
	a.mu.Unlock()

	zcodeApplySettings(t, a, `{"mode": {"current": "yolo"}}`)

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, contracts.ZCodeModeYolo, a.mode)
	assert.Equal(t, "p/m", a.model, "an absent model axis must not clear the model")
	assert.Equal(t, "high", a.thoughtLevel)
}

func TestApplySettingsSnapshot_EmptyCurrentValuesAreNotApplied(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.mu.Lock()
	a.model, a.mode = "p/m", contracts.ZCodeModeEdit
	a.mu.Unlock()

	zcodeApplySettings(t, a, `{"mode": {"current": ""}, "model": {"current": {"providerId": "", "modelId": ""}}}`)

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, contracts.ZCodeModeEdit, a.mode)
	assert.Equal(t, "p/m", a.model)
}

// `lastUsed` fills an UNSET model only. It is the app-server's record of a previous
// session, so it must never override a model the running session pinned.
func TestApplySettingsSnapshot_LastUsedFillsOnlyAnUnsetModel(t *testing.T) {
	t.Parallel()

	t.Run("unset model takes lastUsed", func(t *testing.T) {
		t.Parallel()
		a := newZCodeTestAgent(t, &recordingControlSink{})
		zcodeApplySettings(t, a, `{"model": {"lastUsed": {"providerId": "p", "modelId": "m"}}}`)
		a.mu.Lock()
		defer a.mu.Unlock()
		assert.Equal(t, "p/m", a.model)
	})

	t.Run("a pinned model ignores lastUsed", func(t *testing.T) {
		t.Parallel()
		a := newZCodeTestAgent(t, &recordingControlSink{})
		a.mu.Lock()
		a.model = "pinned/model"
		a.mu.Unlock()
		zcodeApplySettings(t, a, `{"model": {"lastUsed": {"providerId": "p", "modelId": "m"}}}`)
		a.mu.Lock()
		defer a.mu.Unlock()
		assert.Equal(t, "pinned/model", a.model)
	})

	t.Run("current wins over lastUsed", func(t *testing.T) {
		t.Parallel()
		a := newZCodeTestAgent(t, &recordingControlSink{})
		zcodeApplySettings(t, a, `{"model": {
          "current": {"providerId": "now", "modelId": "m"},
          "lastUsed": {"providerId": "then", "modelId": "m"}}}`)
		a.mu.Lock()
		defer a.mu.Unlock()
		assert.Equal(t, "now/m", a.model)
	})
}

// A model that offers no thought level leaves the axis selectable-but-inert rather
// than showing a level the app-server would refuse.
func TestApplySettingsSnapshot_AModelWithNoThoughtLevelsClampsToAuto(t *testing.T) {
	t.Parallel()

	for name, settings := range map[string]string{
		"disabled":       `{"thoughtLevel": {"enabled": false, "available": [{"value":"low"}]}}`,
		"empty list":     `{"thoughtLevel": {"enabled": true, "available": []}}`,
		"blank values":   `{"thoughtLevel": {"enabled": true, "available": [{"value":""}]}}`,
		"no list at all": `{"thoughtLevel": {"enabled": true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newZCodeTestAgent(t, &recordingControlSink{})
			a.mu.Lock()
			a.thoughtLevel = "high"
			a.mu.Unlock()

			zcodeApplySettings(t, a, settings)

			a.mu.Lock()
			defer a.mu.Unlock()
			assert.Equal(t, EffortAuto, a.thoughtLevel)
			assert.Equal(t, EffortAuto, a.observedThoughtDefault)
			require.Len(t, a.observedThoughtLevels, 1)
			assert.Equal(t, EffortAuto, a.observedThoughtLevels[0].GetId())
		})
	}
}

// A level set for a DIFFERENT model, which this one does not offer, must be reported
// as auto: no level is pinned, and claiming the old one would be a lie about the next
// turn.
func TestApplySettingsSnapshot_ALevelTheNewModelDoesNotOfferClampsToAuto(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.mu.Lock()
	a.thoughtLevel = "max"
	a.mu.Unlock()

	zcodeApplySettings(t, a, `{"thoughtLevel": {"enabled": true, "defaultLevel": "low",
      "available": [{"value":"low"},{"value":"high"}]}}`)

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, EffortAuto, a.thoughtLevel)
}

// A level the new model DOES offer survives, and the app-server's own `current` wins
// over it when stated.
func TestApplySettingsSnapshot_CurrentLevelWinsAndAnOfferedLevelSurvives(t *testing.T) {
	t.Parallel()

	t.Run("current is authoritative", func(t *testing.T) {
		t.Parallel()
		a := newZCodeTestAgent(t, &recordingControlSink{})
		a.mu.Lock()
		a.thoughtLevel = "low"
		a.mu.Unlock()
		zcodeApplySettings(t, a, `{"thoughtLevel": {"enabled": true, "current": "high",
          "available": [{"value":"low"},{"value":"high"}]}}`)
		a.mu.Lock()
		defer a.mu.Unlock()
		assert.Equal(t, "high", a.thoughtLevel, "the observed value overrides the requested one")
	})

	t.Run("an offered level survives an absent current", func(t *testing.T) {
		t.Parallel()
		a := newZCodeTestAgent(t, &recordingControlSink{})
		a.mu.Lock()
		a.thoughtLevel = "low"
		a.mu.Unlock()
		zcodeApplySettings(t, a, `{"thoughtLevel": {"enabled": true,
          "available": [{"value":"low"},{"value":"high"}]}}`)
		a.mu.Lock()
		defer a.mu.Unlock()
		assert.Equal(t, "low", a.thoughtLevel)
	})

	t.Run("auto survives, because it produces no RPC at all", func(t *testing.T) {
		t.Parallel()
		a := newZCodeTestAgent(t, &recordingControlSink{})
		a.mu.Lock()
		a.thoughtLevel = EffortAuto
		a.mu.Unlock()
		zcodeApplySettings(t, a, `{"thoughtLevel": {"enabled": true, "available": [{"value":"low"}]}}`)
		a.mu.Lock()
		defer a.mu.Unlock()
		assert.Equal(t, EffortAuto, a.thoughtLevel)
	})
}

func TestApplySettingsSnapshot_NilIsANoop(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.mu.Lock()
	a.applySettingsSnapshotLocked(nil)
	mode := a.mode
	a.mu.Unlock()
	assert.Equal(t, contracts.ZCodeDefaultMode, mode)
}

// --- the option groups ---

func TestZCodeOptionGroups_ExposesModelThoughtLevelAndMode(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.mu.Lock()
	a.model, a.thoughtLevel, a.mode = "builtin:zai/GLM-5.3", "high", contracts.ZCodeModePlan
	a.mu.Unlock()

	groups := a.OptionGroups()

	modelGroup := optionids.GroupByID(groups, OptionIDModel)
	require.NotNil(t, modelGroup)
	modelIDs := effortIDs(modelGroup.GetOptions())
	assert.Contains(t, modelIDs, "builtin:zai/GLM-5.3")
	assert.Contains(t, modelIDs, "acme/acme-1")
	assert.Equal(t, "builtin:zai/GLM-5.3", modelGroup.GetCurrentValue())

	effortGroup := optionids.GroupByID(groups, OptionIDEffort)
	require.NotNil(t, effortGroup)
	assert.Equal(t, ZCodeThoughtLevelLabel, effortGroup.GetLabel(),
		"the app-server calls it a thought level, not a generic effort")
	assert.Equal(t, "high", effortGroup.GetCurrentValue())

	modeGroup := optionids.GroupByID(groups, OptionIDPermissionMode)
	require.NotNil(t, modeGroup)
	assert.Equal(t, contracts.ZCodeModePlan, modeGroup.GetCurrentValue())
}

// The configured variants are the base list because they cover every model; the live
// `thoughtLevel.available` covers only the running model and is authoritative for it.
func TestZCodeModelsForUI_OverridesOnlyTheCurrentModelsEfforts(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.mu.Lock()
	a.model = "builtin:zai/GLM-5.3"
	a.observedThoughtLevels = []*EffortInfo{zcodeAutoEffort, {Id: "turbo", Name: "Turbo"}}
	a.observedThoughtDefault = "turbo"
	a.mu.Unlock()

	models, current, _ := a.zcodeModelsForUI()
	assert.Equal(t, "builtin:zai/GLM-5.3", current)

	byID := map[string]*ModelInfo{}
	for _, m := range models {
		byID[m.GetId()] = m
	}

	live := byID["builtin:zai/GLM-5.3"]
	require.NotNil(t, live)
	liveIDs := effortIDs(live.SupportedEfforts)
	assert.Equal(t, []string{EffortAuto, "turbo"}, liveIDs)
	assert.Equal(t, "turbo", live.DefaultEffort)

	other := byID["acme/acme-1"]
	require.NotNil(t, other)
	assert.Empty(t, other.SupportedEfforts, "another model keeps its own configured list")

	// The override must be a COPY: the catalog entry is shared with every other
	// reader, and mutating it would leak one model's live levels into the static
	// fallback the picker shows before an agent runs.
	for _, m := range a.catalog.Models {
		if m.GetId() == "builtin:zai/GLM-5.3" {
			ids := effortIDs(m.SupportedEfforts)
			assert.Equal(t, []string{EffortAuto, "max", "high", "low"}, ids,
				"the shared catalog entry must not be mutated")
		}
	}
}

func TestZCodeModelsForUI_WithNoObservedLevelsReturnsTheCatalogUnchanged(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.mu.Lock()
	a.model = "builtin:zai/GLM-5.3"
	a.mu.Unlock()

	models, _, _ := a.zcodeModelsForUI()
	assert.Len(t, models, len(a.catalog.Models))
}

// --- state.updated ---

// The settings axes sit at the TOP LEVEL of a session-scope patch. A wrapper struct
// silently matched nothing and dropped every mid-turn mode switch.
func TestHandleZCodeStateUpdated_SessionPatchAppliesTopLevelAxes(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeStateLine(t, ZCodeScopeSession, "mode_changed", `{"mode":{"current":"plan"}}`))

	a.mu.Lock()
	mode := a.mode
	a.mu.Unlock()
	assert.Equal(t, contracts.ZCodeModePlan, mode)
	require.Equal(t, 1, sink.SettingsRefreshCount(),
		"a setting the AGENT changed must reach the picker, or a restart loses it")
	assert.Equal(t, contracts.ZCodeModePlan, sink.LastSettingsRefresh().PermissionMode)
}

// The workspace scope patches `modelCatalog`, whose keys the settings struct does not
// read. Applying it as settings would read an all-absent snapshot.
func TestHandleZCodeStateUpdated_NonSessionScopeIsNotSettings(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeStateLine(t, ZCodeScopeWorkspace, "catalog_changed", `{"mode":{"current":"yolo"}}`))

	a.mu.Lock()
	mode := a.mode
	a.mu.Unlock()
	assert.Equal(t, contracts.ZCodeDefaultMode, mode)
	assert.Equal(t, 0, sink.SettingsRefreshCount())
}

// A patch that changed only `status` must not persist a settings refresh: an all-nil
// snapshot compares equal, and the write would be pointless.
func TestHandleZCodeStateUpdated_StatusOnlyPatchPersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeStateLine(t, ZCodeScopeSession, "prompt_started", `{"status":"running"}`))

	assert.Equal(t, 0, sink.SettingsRefreshCount())
}

// A patch that restates the value the agent already has changed nothing, so it must
// not persist either.
func TestHandleZCodeStateUpdated_UnchangedAxesPersistNothing(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.mu.Lock()
	a.mode = contracts.ZCodeModePlan
	a.mu.Unlock()

	a.HandleOutput(zcodeStateLine(t, ZCodeScopeSession, "mode_changed", `{"mode":{"current":"plan"}}`))

	assert.Equal(t, 0, sink.SettingsRefreshCount())
}

// The shipped build sends `runtime` only in the session/read reply, but reading it
// here costs nothing and keeps a build that starts sending it working unchanged.
func TestHandleZCodeStateUpdated_RuntimeInAPatchIsApplied(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	a.HandleOutput(zcodeStateLine(t, ZCodeScopeSession, "runtime",
		`{"runtime":{"contextUsage":{"used":1234,"size":200000,"cost":{"amount":0.5,"currency":"USD"}}}}`))

	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(1234), usage["context_tokens"])
	assert.InDelta(t, 0.5, info["total_cost_usd"], 1e-9)
}

func TestHandleZCodeStateUpdated_MalformedAndEmptyPayloadsAreSurvivable(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)

	for _, line := range []string{
		`{"method":"state.updated"}`,
		`{"method":"state.updated","params":{}}`,
		`{"method":"state.updated","params":{"scope":"session"}}`,
		`{"method":"state.updated","params":{"scope":"session","patch":null}}`,
		`{"method":"state.updated","params":{"scope":"session","patch":"not an object"}}`,
		`{"method":"state.updated","params":{"scope":"session","patch":[1,2]}}`,
	} {
		a.HandleOutput([]byte(line))
	}

	assert.Equal(t, 0, sink.SettingsRefreshCount())
	a.mu.Lock()
	mode := a.mode
	a.mu.Unlock()
	assert.Equal(t, contracts.ZCodeDefaultMode, mode)
}

func TestZCodeStatePatchBody_HasSettings(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		`{"status":"running"}`:               false,
		`{}`:                                 false,
		`{"runtime":{"eventSeq":3}}`:         false,
		`{"mode":{"current":"plan"}}`:        true,
		`{"model":{"current":{}}}`:           true,
		`{"thoughtLevel":{"enabled":false}}`: true,
	}
	for patch, want := range cases {
		var body zcodeStatePatchBody
		require.NoError(t, json.Unmarshal([]byte(patch), &body))
		assert.Equalf(t, want, body.hasSettings(), "patch %s", patch)
	}
}

// --- the setters' guards ---

func TestZCodeSetters_RefuseWithoutASession(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.mu.Lock()
	a.sessionID = ""
	a.mu.Unlock()

	assert.ErrorIs(t, a.applyZCodeModel("builtin:zai/GLM-5.3", 0), errNoZCodeSession)
	assert.ErrorIs(t, a.applyZCodeThoughtLevel("high", 0), errNoZCodeSession)
	assert.ErrorIs(t, a.applyZCodeMode(contracts.ZCodeModePlan, 0), errNoZCodeSession)
}

// A model the catalog does not hold is refused BEFORE any RPC, so a typo does not
// reach the app-server as a request it would answer with a schema error.
func TestApplyZCodeModel_RefusesAnUnknownModelWithoutAnRPC(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	err := a.applyZCodeModel("nope", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope", "the message must name the id, because the usual cause is a stale configuration")
	assert.Empty(t, stdin.Frames(), "an unresolvable model must not reach the wire")
}

// The overlay carries the thought level so a model switch does not silently drop it.
//
// Auto resolves to the MODEL's own declared default rather than to nothing: a
// session told no level runs on the app-server's fallback, which is the lowest
// level, so "send nothing" would deliver the weakest thinking under a label that
// promises the model's default.
func TestApplyZCodeModel_OverlayCarriesTheLevelAndAutoResolvesTheDefault(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"high":     "high",
		"low":      "low",
		EffortAuto: "high",
		"":         "high",
	}
	for level, want := range cases {
		t.Run("level "+level, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
			a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
			a.mu.Lock()
			a.thoughtLevel = level
			a.mu.Unlock()

			// No reply ever arrives, so the call returns a context error. The REQUEST is
			// what this test is about.
			a.cancel()
			_ = a.applyZCodeModel("builtin:zai/GLM-5.3", 0)

			requests := stdin.Requests(t)
			require.Len(t, requests, 1)
			assert.Equal(t, ZCodeMethodSetModel, requests[0].Method)
			var params struct {
				SessionID    string            `json:"sessionId"`
				Model        zcodeModelRef     `json:"model"`
				RuntimeModel zcodeRuntimeModel `json:"runtimeModel"`
			}
			require.NoError(t, json.Unmarshal(requests[0].Params, &params))
			assert.Equal(t, "sess-1", params.SessionID)
			assert.Equal(t, "GLM-5.3", params.Model.ModelID)
			assert.Equal(t, want, params.RuntimeModel.ThoughtLevel)
			require.NotNil(t, params.RuntimeModel.Provider.APIKey)
			assert.Equal(t, "zai-key", params.RuntimeModel.Provider.APIKey.Value)
		})
	}
}

// A model that declares NO default level gets no level: the overlay must not invent
// one the model would refuse.
func TestApplyZCodeModel_OverlayOmitsTheLevelWhenTheModelDeclaresNoDefault(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.cancel()
	_ = a.applyZCodeModel("builtin:zai/GLM-5.3-Flash", 0)

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var params struct {
		RuntimeModel zcodeRuntimeModel `json:"runtimeModel"`
	}
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Empty(t, params.RuntimeModel.ThoughtLevel)
}

// The parameter is REQUIRED even though the app-server's schema marks it optional:
// its handler throws for a missing value.
func TestApplyZCodeThoughtLevel_AlwaysSendsTheLevel(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.cancel()
	_ = a.applyZCodeThoughtLevel("max", 0)

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, ZCodeMethodSetThoughtLevel, requests[0].Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "max", params["thoughtLevel"])
	assert.Equal(t, "sess-1", params["sessionId"])
}

func TestApplyZCodeMode_SendsTheModeAndAppliesNoLocalValue(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.cancel()
	_ = a.applyZCodeMode(contracts.ZCodeModeYolo, 0)

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, ZCodeMethodSetMode, requests[0].Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, contracts.ZCodeModeYolo, params["mode"])

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, contracts.ZCodeDefaultMode, a.mode,
		"the mode is read from settings.mode.current, never assumed from the request")
}

// --- UpdateSettings ---

// Switching effort to Auto means "let the app-server pick", which the wire cannot
// express: setThoughtLevel requires a level. Only a restart re-resolves the default.
func TestZCodeUpdateSettings_EffortToAutoAsksForARestart(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.thoughtLevel = "high"
	a.mu.Unlock()

	assert.False(t, a.UpdateSettings(map[string]string{OptionIDEffort: EffortAuto}).AppliedLive)
	assert.Empty(t, stdin.Frames(), "nothing is attempted when a restart is the only route")
}

// Nothing to change means nothing to send, and the refresh still records the trio so
// the picker and the stored row agree.
func TestZCodeUpdateSettings_NoChangeSendsNothingAndSucceeds(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, sink, stdin)
	a.mu.Lock()
	a.model, a.thoughtLevel, a.mode = "p/m", "high", contracts.ZCodeModeBuild
	a.unresolvedSettings = map[string]struct{}{OptionIDEffort: {}}
	a.mu.Unlock()

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:          "p/m",
		OptionIDEffort:         "high",
		OptionIDPermissionMode: contracts.ZCodeModeBuild,
	})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, OptionSettlementUnresolved, result.Settlements[OptionIDEffort].State,
		"an axis stays unresolved until a provider snapshot reports it")
	assert.Empty(t, stdin.Frames())

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "p/m", refresh.Model)
	assert.Equal(t, "high", refresh.Effort)
	assert.Equal(t, contracts.ZCodeModeBuild, refresh.PermissionMode)
}

// A re-spelled separator is the same model, so it must not read as a switch and must
// not produce an RPC.
func TestZCodeUpdateSettings_ARespelledModelIsNotASwitch(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.model = "p/m"
	a.mu.Unlock()

	assert.True(t, a.UpdateSettings(map[string]string{OptionIDModel: `p\m`}).AppliedLive)
	assert.Empty(t, stdin.Frames())
}

// A failed apply must restore what was captured, so nothing in between reads a
// half-applied trio, and must ask for a restart rather than claim success.
func TestZCodeUpdateSettings_AFailedApplyRestoresTheCapturedTrio(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newZCodeTestAgent(t, sink)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.mu.Lock()
	a.model, a.thoughtLevel, a.mode = "builtin:zai/GLM-5.3", "high", contracts.ZCodeModeBuild
	a.mu.Unlock()
	// A cancelled context makes every setter fail at the wire.
	a.cancel()

	assert.False(t, a.UpdateSettings(map[string]string{
		OptionIDModel:          "builtin:zai/GLM-5.3-Flash",
		OptionIDPermissionMode: contracts.ZCodeModeYolo,
	}).AppliedLive)

	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, "builtin:zai/GLM-5.3", a.model)
	assert.Equal(t, "high", a.thoughtLevel)
	assert.Equal(t, contracts.ZCodeModeBuild, a.mode)
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "a refused change must not persist a refresh")
}

func TestNewInvalidZCodeModelError_NamesTheModel(t *testing.T) {
	t.Parallel()

	assert.Contains(t, newInvalidZCodeModelError("p/m").Error(), `"p/m"`)
}

// The setter must keep settings.model.current from the reply, never the id it asked
// for. Overwriting after the snapshot would report a model the session does not have
// whenever the app-server clamps the request.
func TestApplyZCodeModel_ReportsTheObservedModel(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.applyZCodeModel("builtin:zai/GLM-5.3-Flash", time.Second)
	}()

	req := waitZCodeRequest(t, stdin, ZCodeMethodSetModel)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(
		`{"settings":{"model":{"current":{"providerId":"builtin:zai","modelId":"GLM-5.3"}}}}`,
	)))

	require.NoError(t, <-errCh)
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, "builtin:zai/GLM-5.3", a.model,
		"the observed snapshot wins over the requested id")
}

// session/create HONORS its mode parameter, and openSession sends one on every
// create, so the opened session already runs in the requested mode and the setter is
// one blocking RPC of pure repetition.
//
// Measured against the shipped app-server: a create with `mode:"plan"` answers
// `settings.mode.current:"plan"` and `settings.permission.mode:"plan"`. Only
// `session.mode` keeps reading "build", because that field reports the projection's
// seed -- which is what makes the reply easy to misread.
func TestApplyStartupSettings_SkipsTheModeSetterWhenTheSessionRunsInThatMode(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.model = ""
	a.thoughtLevel = EffortAuto
	a.mu.Unlock()
	// The mode the create reply folded in through settings.mode.current.
	zcodeApplySettings(t, a, `{"mode":{"current":"plan"}}`)

	a.applyStartupSettings(zcodeSettingsRequest{Mode: contracts.ZCodeModePlan}, 0)

	assert.Empty(t, stdin.Frames(), "a session that already runs in the requested mode needs no setter")
}

// The skip needs the app-server's own report. A reply that carried no
// `settings.mode.current` leaves `mode` holding what the LAUNCH asked for, and
// comparing that against itself would skip the setter on no evidence at all.
func TestApplyStartupSettings_PinsTheModeWhenTheSessionReportedNone(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.model = ""
	a.thoughtLevel = EffortAuto
	a.mode = contracts.ZCodeModePlan // the launch request, echoed into the field, not an observation
	a.mu.Unlock()
	a.cancel()

	a.applyStartupSettings(zcodeSettingsRequest{Mode: contracts.ZCodeModePlan}, 0)

	requests := stdin.Requests(t)
	require.Len(t, requests, 1, "with no observed mode the setter must run")
	assert.Equal(t, ZCodeMethodSetMode, requests[0].Method)
}

// A launch that asks for no mode runs on the mode the app-server chose. That mode is
// not always contracts.ZCodeDefaultMode: a create with no `mode` parameter inherits the last
// mode session/setMode applied, so the observed value is the only truth here.
func TestApplyStartupSettings_SkipsTheModeSetterWhenTheLaunchAsksForNoMode(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.model = ""
	a.thoughtLevel = EffortAuto
	a.mode = contracts.ZCodeModeYolo
	a.mu.Unlock()

	a.applyStartupSettings(zcodeSettingsRequest{}, 0)

	assert.Empty(t, stdin.Frames())
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, contracts.ZCodeModeYolo, a.mode, "the observed mode stands where the launch asked for none")
}

// The setter still runs where the two differ: a resumed session was left in another
// mode, or the launch asks for a mode the create did not settle on.
func TestApplyStartupSettings_SendsTheModeSetterWhenTheSessionRunsInAnotherMode(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.model = ""
	a.thoughtLevel = EffortAuto
	a.mode = contracts.ZCodeModeYolo
	a.mu.Unlock()
	// No reply ever arrives, so the setter returns a context error. The REQUEST is what
	// this test is about.
	a.cancel()

	a.applyStartupSettings(zcodeSettingsRequest{Mode: contracts.ZCodeModePlan}, 0)

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, ZCodeMethodSetMode, requests[0].Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "sess-1", params["sessionId"])
	assert.Equal(t, contracts.ZCodeModePlan, params["mode"])
}

// --- thought-level labels ---
//
// ZCode spells a level as a bare id and repeats it as the label, and the level list
// reaches the popover through two paths: the configured catalog (every model) and
// the live snapshot (the running model). One label function serves both, or the same
// level reads "Low" in one group and "low" in the other.

func TestZCodeEffortTier_UsesTheSharedLabel(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Low", zcodeEffortTier("low", "low", "").GetName())
	assert.Equal(t, "Max", zcodeEffortTier("max", "max", "").GetName())
	assert.Equal(t, "Off", zcodeEffortTier("off", "off", "").GetName())
	assert.Equal(t, "Enabled", zcodeEffortTier("enabled", "ENABLED", "").GetName(),
		"a label that only differs in case says nothing the table does not")
	assert.Equal(t, "Deep thinking", zcodeEffortTier("max", "Deep thinking", "").GetName(),
		"a label that differs carries what the shared table cannot know")

	tier := zcodeEffortTier("high", "", "burns tokens")
	assert.Equal(t, "high", tier.GetId())
	assert.Equal(t, "High", tier.GetName())
	assert.Equal(t, "burns tokens", tier.GetDescription())
}

func TestZCodeOptionGroups_LabelsTheLiveAndConfiguredLevelsAlike(t *testing.T) {
	t.Parallel()

	a := newZCodeTestAgent(t, &recordingControlSink{})
	a.catalog = zcodeTestCatalog(t, zcodeTwoProviderConfig)
	a.mu.Lock()
	// The running model is the one whose levels come from the LIVE snapshot;
	// GLM-5.3's sub-group therefore still comes from the configured catalog, which is
	// what makes the two paths comparable in one snapshot of the groups.
	a.model = "acme/acme-1"
	a.mu.Unlock()
	var snap zcodeSettingsSnapshot
	require.NoError(t, json.Unmarshal([]byte(`{"thoughtLevel":{"enabled":true,"current":"max",
	  "defaultLevel":"max","available":[{"value":"low","label":"low"},{"value":"max","label":"max"}]}}`), &snap))
	a.mu.Lock()
	a.applySettingsSnapshotLocked(&snap)
	a.mu.Unlock()

	groups := a.OptionGroups()
	live := map[string]string{}
	for _, e := range optionids.GroupByID(groups, OptionIDEffort).GetOptions() {
		live[e.GetId()] = e.GetName()
	}
	assert.Equal(t, map[string]string{EffortAuto: "Auto", "low": "Low", "max": "Max"}, live)

	// The catalog path builds the level list for every OTHER model, and reads the same.
	configured := map[string]string{}
	for _, m := range optionids.GroupByID(groups, OptionIDModel).GetOptions() {
		if m.GetId() != "builtin:zai/GLM-5.3" {
			continue
		}
		for _, g := range m.GetSubGroups() {
			if g.GetId() != OptionIDEffort {
				continue
			}
			for _, o := range g.GetOptions() {
				configured[o.GetId()] = o.GetName()
			}
		}
	}
	assert.Equal(t, map[string]string{EffortAuto: "Auto", "low": "Low", "high": "High", "max": "Max"}, configured)

	for id, name := range live {
		if configuredName, ok := configured[id]; ok {
			assert.Equal(t, name, configuredName, "one spelling for level %q, whichever path built it", id)
		}
	}
}

// --- the launch request survives the snapshot fold ---

// Opening a session folds the app-server's own settings over the agent's mirror, and
// its `thoughtLevel.current` is its FALLBACK -- the lowest level, not the default the
// model declares. Reading the fields back after that fold compares the observed level
// against itself, so a launch that asked for `max` sent no setter at all.
func TestApplyStartupSettings_TheRequestedLevelWinsOverTheFoldedOne(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	// What openSession left behind: the app-server's own fallback.
	a.thoughtLevel = "low"
	a.mode = ""
	a.model = ""
	a.mu.Unlock()
	a.cancel()

	a.applyStartupSettings(zcodeSettingsRequest{ThoughtLevel: "max"}, 0)

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	assert.Equal(t, ZCodeMethodSetThoughtLevel, requests[0].Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "max", params["thoughtLevel"], "the LAUNCH asked for max, not the app-server")
}

// Where the caller asked for nothing on an axis, the observed value stands: there is
// no request to restore, and the app-server's own answer is the truth.
func TestApplyStartupSettings_AnEmptyRequestKeepsTheObservedValue(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, &recordingControlSink{}, stdin)
	a.mu.Lock()
	a.thoughtLevel = "low"
	a.model = ""
	a.mode = ""
	a.mu.Unlock()
	a.cancel()

	a.applyStartupSettings(zcodeSettingsRequest{}, 0)

	a.mu.Lock()
	level := a.thoughtLevel
	a.mu.Unlock()
	assert.Equal(t, "low", level)
}
