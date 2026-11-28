package antigravity

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustBuildCatalogJSON(t *testing.T, raw string) antigravityCatalog {
	t.Helper()
	var reply antigravityCatalogReply
	require.NoError(t, json.Unmarshal([]byte(raw), &reply))
	return buildAntigravityCatalog(reply)
}

func testCatalogSendRPC(t *testing.T, catalogBody string) (antigravityRPC, <-chan map[string]any) {
	t.Helper()
	sent := make(chan map[string]any, 1)
	rpc := testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case languageServerService + "GetCascadeModelConfigData":
			_, _ = w.Write([]byte(catalogBody))
		case languageServerService + "SendUserCascadeMessage":
			var request map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			sent <- request
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"response":{}}`))
		}
	})
	return rpc, sent
}

func sentPlanModel(t *testing.T, sent <-chan map[string]any) any {
	t.Helper()
	return receiveTestValue(t, sent)["cascadeConfig"].(map[string]any)["plannerConfig"].(map[string]any)["planModel"]
}

func TestAntigravityCatalogEffortSplit(t *testing.T) {
	for _, tc := range []struct{ id, family, effort string }{
		{id: "gemini-3.8-flash-high", family: "gemini-3.8-flash", effort: "high"},
		{id: "claude-opus-4-6-thinking", family: "claude-opus-4-6-thinking"},
		{id: "claude-sonnet-4-6", family: "claude-sonnet-4-6"},
		{id: "gpt-oss-120b-medium", family: "gpt-oss-120b", effort: "medium"},
		{id: "gemini-max", family: "gemini", effort: "max"},
	} {
		family, effort := splitModelEffort(tc.id)
		assert.Equal(t, tc.family, family, tc.id)
		assert.Equal(t, tc.effort, effort, tc.id)
	}
}

func TestAntigravityCatalogDropsDisabledAndResolvesDefault(t *testing.T) {
	catalog := buildAntigravityCatalog(antigravityCatalogReply{ClientModelConfigs: []antigravityModelConfig{
		{ModelID: "first-low", Label: "First", ModelOrAlias: antigravityModelAlias{Model: "FIRST"}},
		{ModelID: "disabled-high", Disabled: true, ModelOrAlias: antigravityModelAlias{Model: "DISABLED"}},
	}})
	assert.Equal(t, "first", catalog.defaultModel)
	assert.NotContains(t, catalog.families, "disabled")
	variant, effort, err := catalog.resolveModel(agent.DefaultModelSentinel, "")
	require.NoError(t, err)
	assert.Equal(t, "FIRST", variant.token)
	assert.Equal(t, agent.EffortAuto, effort)
	_, _, err = catalog.resolveModel("unknown", "")
	assert.ErrorContains(t, err, "unknown is not in the Antigravity catalog")
}

func TestAntigravityStrictConfigIsModeIndependent(t *testing.T) {
	first := cascadeConfigForToken("MODEL", "/workspace")
	second := cascadeConfigForToken("MODEL", "/workspace")
	assert.Equal(t, first, second)
	planner := first["plannerConfig"].(map[string]any)
	assert.Equal(t, "MODEL", planner["planModel"])
	tools := planner["toolConfig"].(map[string]any)
	permissions := tools["permissionConfig"].(map[string]any)
	assert.Equal(t, map[string]any{"allow": []string{"read_file(/workspace)"}}, permissions["effectiveGrants"])
	assert.Equal(t, false, tools["autoAllowAllInteractions"])
}

func TestAntigravityCatalogPreservesServerOrder(t *testing.T) {
	var reply antigravityCatalogReply
	require.NoError(t, json.Unmarshal([]byte(`{"clientModelConfigs":[{"modelId":"z-low","label":"Z","modelOrAlias":{"model":"ZL"}},{"modelId":"a","modelOrAlias":{"model":"A"}},{"modelId":"z-high","modelOrAlias":{"model":"ZH"}},{"modelId":"m","modelOrAlias":{"model":"M"}}]}`), &reply))
	for range 64 {
		catalog := buildAntigravityCatalog(reply)
		var ids []string
		for _, model := range catalog.models {
			ids = append(ids, model.Id)
		}
		assert.Equal(t, []string{"z", "a", "m"}, ids)
		require.NotEmpty(t, catalog.models)
		assert.True(t, catalog.models[0].IsDefault)
	}
}

func TestAntigravityCatalogSeparatesModelLabelsAndEffortTokens(t *testing.T) {
	catalog := mustBuildCatalogJSON(t, `{"clientModelConfigs":[
		{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEON_HIGH"}},
		{"modelId":"gemini-4-neon-xhigh","label":"Gemini 4 Neon (XHigh)","modelOrAlias":{"model":"NEON_XHIGH"}},
		{"modelId":"claude-opus-5-5-low","label":"Claude Opus 5.5 (Low)","modelOrAlias":{"model":"OPUS_LOW"}},
		{"modelId":"other","label":"Other (High)","modelOrAlias":{"model":"OTHER"}}
	]}`)
	require.Len(t, catalog.models, 3)
	assert.Equal(t, "Gemini 4 Neon", catalog.models[0].DisplayName)
	assert.Equal(t, "Claude Opus 5.5", catalog.models[1].DisplayName)
	assert.Equal(t, "Other (High)", catalog.models[2].DisplayName)
	for _, effort := range []string{"high", "xhigh"} {
		variant, selected, err := catalog.resolveModel("gemini-4-neon", effort)
		require.NoError(t, err)
		assert.Equal(t, effort, selected)
		assert.Equal(t, map[string]string{"high": "NEON_HIGH", "xhigh": "NEON_XHIGH"}[effort], variant.token)
	}
}

func TestAntigravityAccountRefreshCannotDelayInactiveTurn(t *testing.T) {
	for _, method := range []string{"GetCascadeModelConfigData", "RetrieveUserQuotaSummary"} {
		t.Run(method, func(t *testing.T) {
			a, sink := testAgent(t)
			entered := make(chan struct{})
			release, unblock := newTestGate(t)
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == languageServerService+method {
					close(entered)
					<-release
				}
				_, _ = w.Write([]byte(`{"response":{}}`))
			})
			defer func() { unblock(); a.accountRefreshWork.CloseAndWait() }()
			a.beginTurn(a.root)
			done := make(chan error, 1)
			go func() { done <- a.endTurn(a.root, agent.MessageCompletionComplete) }()
			receiveTestValue(t, entered)
			states := sink.TurnStates()
			require.NotEmpty(t, states)
			assert.Equal(t, agent.TurnState{}, states[len(states)-1])
			require.NoError(t, receiveTestValue(t, done))
		})
	}
}

func TestAntigravityCatalogRefreshSettlesUnavailableSelection(t *testing.T) {
	a, sink := testAgent(t)
	a.model, a.effort = "gemini-test", "high"
	rpc, sent := testCatalogSendRPC(t, `{"clientModelConfigs":[{"modelId":"gemini-new-low","modelOrAlias":{"model":"NEW_LOW"}}]}`)
	a.rpc = rpc
	a.refreshAccountCatalog()
	require.NoError(t, agenttest.SendInput(a, "continue the session", nil))
	assert.Equal(t, "NEW_LOW", sentPlanModel(t, sent))
	assert.Equal(t, "gemini-new", a.model)
	assert.Equal(t, agent.EffortAuto, a.effort)
	assert.Positive(t, sink.SettingsRefreshCount())
}

func TestAntigravityCatalogAutoEffortUsesServerDefaultVariant(t *testing.T) {
	catalog := mustBuildCatalogJSON(t, `{"clientModelConfigs":[
		{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEON_HIGH"}},
		{"modelId":"gemini-4-neon-xhigh","label":"Gemini 4 Neon (XHigh)","modelOrAlias":{"model":"NEON_XHIGH"}},
		{"modelId":"gemini-4-neon-medium","label":"Gemini 4 Neon (Medium)","modelOrAlias":{"model":"NEON_MEDIUM"}},
		{"modelId":"gemini-4-neon-low","label":"Gemini 4 Neon (Low)","modelOrAlias":{"model":"NEON_LOW"}},
		{"modelId":"gemini-4-carbon-medium","label":"Gemini 4 Carbon (Medium)","modelOrAlias":{"model":"CARBON_MEDIUM"}},
		{"modelId":"gemini-4-carbon-high","label":"Gemini 4 Carbon (High)","modelOrAlias":{"model":"CARBON_HIGH"}},
		{"modelId":"gemini-4-carbon-low","label":"Gemini 4 Carbon (Low)","modelOrAlias":{"model":"CARBON_LOW"}}
	]}`)

	neon := agent.FindAvailableModel(catalog.models, "gemini-4-neon")
	require.NotNil(t, neon)
	assert.Equal(t, agent.EffortAuto, neon.DefaultEffort)
	assert.Equal(t, []string{agent.EffortAuto, "xhigh", "high", "medium", "low"}, agenttest.OptionIDs(neon.SupportedEfforts))

	carbon := agent.FindAvailableModel(catalog.models, "gemini-4-carbon")
	require.NotNil(t, carbon)
	assert.Equal(t, agent.EffortAuto, carbon.DefaultEffort)
	assert.Equal(t, []string{agent.EffortAuto, "high", "medium", "low"}, agenttest.OptionIDs(carbon.SupportedEfforts))

	for _, requested := range []string{agent.EffortAuto, ""} {
		variant, settled, err := catalog.resolveModel("gemini-4-neon", requested)
		require.NoError(t, err)
		assert.Equal(t, "NEON_HIGH", variant.token)
		assert.Equal(t, agent.EffortAuto, settled)
	}
	variant, settled, err := catalog.resolveModel("gemini-4-carbon", agent.EffortAuto)
	require.NoError(t, err)
	assert.Equal(t, "CARBON_MEDIUM", variant.token)
	assert.Equal(t, agent.EffortAuto, settled)

	a, _ := testAgent(t)
	a.catalog = catalog
	a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "gemini-4-neon", agent.OptionIDEffort: agent.EffortAuto})
	effortGroup := optionids.GroupByID(a.OptionGroups(), agent.OptionIDEffort)
	require.NotNil(t, effortGroup)
	assert.Equal(t, agent.EffortAuto, effortGroup.GetCurrentValue())
	assert.Equal(t, agent.EffortAuto, effortGroup.GetDefaultValue())
	require.NotEmpty(t, effortGroup.GetOptions())
	assert.Equal(t, agent.EffortAuto, effortGroup.GetOptions()[0].GetId())
}

func TestAntigravityLoadCatalogResolvesInitialDefaultModel(t *testing.T) {
	a, _ := testAgent(t)
	a.model = agent.DefaultModelSentinel
	a.effort = agent.EffortAuto
	rpc, sent := testCatalogSendRPC(t, `{"clientModelConfigs":[
		{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEON_HIGH"}},
		{"modelId":"gemini-4-neon-low","label":"Gemini 4 Neon (Low)","modelOrAlias":{"model":"NEON_LOW"}},
		{"modelId":"gemini-3.8-flash-high","label":"Gemini 3.8 Flash (High)","modelOrAlias":{"model":"FLASH_HIGH"}}
	]}`)
	a.rpc = rpc

	require.NoError(t, a.loadCatalog())
	assert.Equal(t, "gemini-4-neon", a.model)
	assert.Equal(t, agent.EffortAuto, a.effort)
	assert.Nil(t, agent.FindAvailableModel(a.catalog.models, agent.DefaultModelSentinel))
	neon := agent.FindAvailableModel(a.catalog.models, "gemini-4-neon")
	require.NotNil(t, neon)
	assert.True(t, neon.IsDefault)

	snapshot := a.SettingsSnapshot()
	assert.Equal(t, "gemini-4-neon", snapshot.SurfacedOptions[agent.OptionIDModel])
	assert.Equal(t, agent.EffortAuto, snapshot.SurfacedOptions[agent.OptionIDEffort])

	groups := a.OptionGroups()
	modelGroup := optionids.GroupByID(groups, agent.OptionIDModel)
	require.NotNil(t, modelGroup)
	assert.Equal(t, "gemini-4-neon", modelGroup.GetCurrentValue())
	assert.Equal(t, "gemini-4-neon", modelGroup.GetDefaultValue())
	effortGroup := optionids.GroupByID(groups, agent.OptionIDEffort)
	require.NotNil(t, effortGroup)
	assert.Equal(t, agent.EffortAuto, effortGroup.GetCurrentValue())
	assert.Equal(t, agent.EffortAuto, effortGroup.GetDefaultValue())

	require.NoError(t, agenttest.SendInput(a, "hello", nil))
	assert.Equal(t, "NEON_HIGH", sentPlanModel(t, sent))
}

func TestAntigravityCatalogDeduplicatesRepeatedModelIDs(t *testing.T) {
	catalog := mustBuildCatalogJSON(t, `{"clientModelConfigs":[
		{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEON_HIGH_1"}},
		{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEON_HIGH_2"}},
		{"modelId":"gemini-4-neon-low","label":"Gemini 4 Neon (Low)","modelOrAlias":{"model":"NEON_LOW"}}
	]}`)
	neon := agent.FindAvailableModel(catalog.models, "gemini-4-neon")
	require.NotNil(t, neon)
	assert.Equal(t, []string{agent.EffortAuto, "high", "low"}, agenttest.OptionIDs(neon.SupportedEfforts))
}

func TestAntigravityCatalogDefaultOverrideSelectsFamilyAndAutoEffort(t *testing.T) {
	baseConfigs := `[
		{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEON_HIGH"}},
		{"modelId":"gemini-4-carbon-low","label":"Gemini 4 Carbon (Low)","modelOrAlias":{"model":"CARBON_LOW"}},
		{"modelId":"gemini-4-carbon-high","label":"Gemini 4 Carbon (High)","modelOrAlias":{"model":"CARBON_HIGH"}}
	]`
	for _, tc := range []struct {
		name      string
		override  string
		wantModel string
		wantToken string
	}{
		{
			name:      "matches by token",
			override:  `{"modelOrAlias":{"model":"CARBON_HIGH"}}`,
			wantModel: "gemini-4-carbon",
			wantToken: "CARBON_HIGH",
		},
		{
			name:      "matches by modelId with empty token",
			override:  `{"modelId":"gemini-4-carbon-high"}`,
			wantModel: "gemini-4-carbon",
			wantToken: "CARBON_HIGH",
		},
		{
			name:      "ignores disabled override",
			override:  `{"disabled":true,"modelOrAlias":{"model":"CARBON_HIGH"}}`,
			wantModel: "gemini-4-neon",
			wantToken: "NEON_HIGH",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := mustBuildCatalogJSON(t, `{"clientModelConfigs":`+baseConfigs+`,"defaultOverrideModelConfig":`+tc.override+`}`)

			for _, model := range catalog.models {
				assert.Equal(t, model.Id == tc.wantModel, model.IsDefault, "model %s", model.Id)
			}
			settledModel, settledEffort := catalog.settleSelection(agent.DefaultModelSentinel, agent.EffortAuto)
			assert.Equal(t, tc.wantModel, settledModel)
			assert.Equal(t, agent.EffortAuto, settledEffort)

			variant, effort, err := catalog.resolveModel(tc.wantModel, agent.EffortAuto)
			require.NoError(t, err)
			assert.Equal(t, tc.wantToken, variant.token)
			assert.Equal(t, agent.EffortAuto, effort)
		})
	}
}

func TestAntigravityCatalogRefreshIgnoresUnusableReply(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "only disabled configs",
			body: `{"clientModelConfigs":[{"modelId":"disabled-low","disabled":true,"modelOrAlias":{"model":"DISABLED"}}]}`,
		},
		{
			name: "empty defaultOverrideModelConfig",
			body: `{"defaultOverrideModelConfig":{}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink := testAgent(t)
			a.model, a.effort = "gemini-test", "high"
			beforeModels := a.catalog.models
			rpc, sent := testCatalogSendRPC(t, tc.body)
			a.rpc = rpc

			a.refreshAccountCatalog()
			assert.Equal(t, "gemini-test", a.model)
			assert.Equal(t, "high", a.effort)
			assert.True(t, agent.ModelInfosEqual(beforeModels, a.catalog.models))
			assert.Zero(t, sink.SettingsRefreshCount())
			require.NoError(t, agenttest.SendInput(a, "continue", nil))
			assert.Equal(t, "MODEL_TEST", sentPlanModel(t, sent))
		})
	}
}

func TestAntigravityCatalogEmptyOverrideFallsBackToFirstFamily(t *testing.T) {
	baseConfigs := []antigravityModelConfig{{
		ModelID:      "gemini-4-neon-high",
		Label:        "Gemini 4 Neon (High)",
		ModelOrAlias: antigravityModelAlias{Model: "NEON_HIGH"},
	}}
	for _, tc := range []struct {
		name     string
		override *antigravityModelConfig
	}{
		{name: "empty override", override: &antigravityModelConfig{}},
		{name: "unmatched tokenless modelId", override: &antigravityModelConfig{ModelID: "retired-high"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := buildAntigravityCatalog(antigravityCatalogReply{
				ClientModelConfigs:         baseConfigs,
				DefaultOverrideModelConfig: tc.override,
			})
			assert.Equal(t, "gemini-4-neon", catalog.defaultModel)
			assert.Nil(t, agent.FindAvailableModel(catalog.models, agent.DefaultModelSentinel))
			neon := agent.FindAvailableModel(catalog.models, "gemini-4-neon")
			require.NotNil(t, neon)
			assert.True(t, neon.IsDefault)
			variant, settled, err := catalog.resolveModel(agent.DefaultModelSentinel, agent.EffortAuto)
			require.NoError(t, err)
			assert.Equal(t, "NEON_HIGH", variant.token)
			assert.Equal(t, agent.EffortAuto, settled)
		})
	}
}

func TestAntigravityCatalogUnlistedOverrideKeepsSentinelFallback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		configs []antigravityModelConfig
	}{
		{name: "empty clientModelConfigs"},
		{
			name: "unrelated clientModelConfigs",
			configs: []antigravityModelConfig{{
				ModelID:      "gemini-3.8-flash-high",
				Label:        "Gemini 3.8 Flash (High)",
				ModelOrAlias: antigravityModelAlias{Model: "FLASH_HIGH"},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			override := &antigravityModelConfig{
				ModelOrAlias:       antigravityModelAlias{Model: "HIDDEN_DEFAULT"},
				SupportedMimeTypes: map[string]bool{"image/png": true},
			}
			catalog := buildAntigravityCatalog(antigravityCatalogReply{
				ClientModelConfigs:         tc.configs,
				DefaultOverrideModelConfig: override,
			})
			require.NotEmpty(t, catalog.models)
			assert.Equal(t, agent.DefaultModelSentinel, catalog.models[0].Id)

			model, effort := catalog.settleSelection(agent.DefaultModelSentinel, agent.EffortAuto)
			assert.Equal(t, agent.DefaultModelSentinel, model)
			assert.Empty(t, effort)

			model, effort = catalog.settleSelection("removed-model", "high")
			assert.Equal(t, agent.DefaultModelSentinel, model)
			assert.Empty(t, effort)

			variant, settled, err := catalog.resolveModel(agent.DefaultModelSentinel, "")
			require.NoError(t, err)
			assert.Equal(t, "HIDDEN_DEFAULT", variant.token)
			assert.True(t, variant.mimeTypes["image/png"])
			assert.Empty(t, settled)
		})
	}
}

func TestAntigravityCatalogEffortlessModelHasNoEffortAxis(t *testing.T) {
	catalog := mustBuildCatalogJSON(t, `{"clientModelConfigs":[
		{"modelId":"gemini-pro-orchestrator-neon","label":"Orchestrator (Neon)","modelOrAlias":{"model":"ORCH_NEON"}},
		{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEON_HIGH"}},
		{"modelId":"gemini-4-neon-low","label":"Gemini 4 Neon (Low)","modelOrAlias":{"model":"NEON_LOW"}}
	]}`)

	orchestrator := agent.FindAvailableModel(catalog.models, "gemini-pro-orchestrator-neon")
	require.NotNil(t, orchestrator)
	assert.Nil(t, orchestrator.SupportedEfforts)
	assert.Empty(t, orchestrator.DefaultEffort)

	fallbackModel, fallbackEffort := catalog.settleSelection("removed-model", "high")
	assert.Equal(t, "gemini-pro-orchestrator-neon", fallbackModel)
	assert.Empty(t, fallbackEffort)

	a, _ := testAgent(t)
	a.catalog = catalog
	a.model, a.effort = "gemini-4-neon", "high"
	rpc, sent := testCatalogSendRPC(t, `{}`)
	a.rpc = rpc

	a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "gemini-pro-orchestrator-neon"})
	assert.Equal(t, "gemini-pro-orchestrator-neon", a.model)
	assert.Empty(t, a.effort)
	assert.Nil(t, optionids.GroupByID(a.OptionGroups(), agent.OptionIDEffort))

	a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "gemini-4-neon"})
	assert.Equal(t, "gemini-4-neon", a.model)
	assert.Equal(t, agent.EffortAuto, a.effort)
	require.NoError(t, agenttest.SendInput(a, "hello", nil))
	assert.Equal(t, "NEON_HIGH", sentPlanModel(t, sent))
}

func TestAntigravityEffortSettlementBoundaries(t *testing.T) {
	lowHigh := catalogFamily{
		variants:      map[string]catalogVariant{"low": {token: "LOW"}, "high": {token: "HIGH"}},
		defaultEffort: "high",
	}
	singleMedium := catalogFamily{
		variants:      map[string]catalogVariant{"medium": {token: "MEDIUM"}},
		defaultEffort: "medium",
	}
	mixed := catalogFamily{
		variants:      map[string]catalogVariant{"": {token: "BASE"}, "high": {token: "HIGH"}},
		defaultEffort: "",
	}
	unrankedOnly := catalogFamily{
		variants:      map[string]catalogVariant{"custom": {token: "CUSTOM"}},
		defaultEffort: "custom",
	}
	for _, tc := range []struct {
		name        string
		family      catalogFamily
		requested   string
		wantWire    string
		wantSettled string
	}{
		{name: "empty on low/high", family: lowHigh, requested: "", wantWire: "high", wantSettled: agent.EffortAuto},
		{name: "auto on low/high", family: lowHigh, requested: agent.EffortAuto, wantWire: "high", wantSettled: agent.EffortAuto},
		{name: "unknown on low/high", family: lowHigh, requested: "unknown-tier", wantWire: "high", wantSettled: agent.EffortAuto},
		{name: "medium tie on low/high", family: lowHigh, requested: "medium", wantWire: "low", wantSettled: "low"},
		{name: "max on low/high", family: lowHigh, requested: "max", wantWire: "high", wantSettled: "high"},
		{name: "empty on single medium", family: singleMedium, requested: "", wantWire: "medium", wantSettled: agent.EffortAuto},
		{name: "auto on single medium", family: singleMedium, requested: agent.EffortAuto, wantWire: "medium", wantSettled: agent.EffortAuto},
		{name: "unknown on single medium", family: singleMedium, requested: "unknown-tier", wantWire: "medium", wantSettled: agent.EffortAuto},
		{name: "high on single medium", family: singleMedium, requested: "high", wantWire: "medium", wantSettled: "medium"},
		{name: "empty on mixed", family: mixed, requested: "", wantWire: "", wantSettled: agent.EffortAuto},
		{name: "auto on mixed", family: mixed, requested: agent.EffortAuto, wantWire: "", wantSettled: agent.EffortAuto},
		{name: "unknown on mixed", family: mixed, requested: "unknown-tier", wantWire: "", wantSettled: agent.EffortAuto},
		{name: "max skips empty on mixed", family: mixed, requested: "max", wantWire: "high", wantSettled: "high"},
		{name: "ranked request on unranked-only family falls back to auto", family: unrankedOnly, requested: "high", wantWire: "custom", wantSettled: agent.EffortAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, settled := settleAntigravityEffort(tc.family, tc.requested)
			assert.Equal(t, tc.wantWire, wire)
			assert.Equal(t, tc.wantSettled, settled)
		})
	}
}

func TestAntigravityCatalogEmptyReturnsError(t *testing.T) {
	catalog := buildAntigravityCatalog(antigravityCatalogReply{
		ClientModelConfigs: []antigravityModelConfig{
			{ModelID: "disabled-low", Disabled: true, ModelOrAlias: antigravityModelAlias{Model: "DISABLED"}},
			{ModelID: "", ModelOrAlias: antigravityModelAlias{Model: "NO_ID"}},
			{ModelID: "no-token-high"},
		},
	})
	assert.Empty(t, catalog.models)
	model, effort := catalog.settleSelection("removed-model", "high")
	assert.Equal(t, agent.DefaultModelSentinel, model)
	assert.Empty(t, effort)
	_, _, err := catalog.resolveModel(agent.DefaultModelSentinel, agent.EffortAuto)
	assert.ErrorContains(t, err, "the Antigravity catalog has no default model")
}

func TestAntigravityLoadCatalogRejectsUnusableReply(t *testing.T) {
	a, _ := testAgent(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"clientModelConfigs":[{"modelId":"disabled-low","disabled":true,"modelOrAlias":{"model":"DISABLED"}}]}`))
	})
	err := a.loadCatalog()
	require.Error(t, err)
}

func TestAntigravityCatalogOverridePreservesUnlistedTokenAndDisambiguatesSharedToken(t *testing.T) {
	t.Run("unlisted override token wins over matching modelId", func(t *testing.T) {
		catalog := mustBuildCatalogJSON(t, `{
			"clientModelConfigs":[
				{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEON_HIGH"}}
			],
			"defaultOverrideModelConfig":{
				"modelId":"gemini-4-neon-high",
				"modelOrAlias":{"model":"CANARY_TOKEN"},
				"supportedMimeTypes":{"image/png":true}
			}
		}`)
		model, effort := catalog.settleSelection(agent.DefaultModelSentinel, agent.EffortAuto)
		assert.Equal(t, agent.DefaultModelSentinel, model)
		assert.Empty(t, effort)
		variant, settled, err := catalog.resolveModel(agent.DefaultModelSentinel, agent.EffortAuto)
		require.NoError(t, err)
		assert.Equal(t, "CANARY_TOKEN", variant.token)
		assert.True(t, variant.mimeTypes["image/png"])
		assert.Empty(t, settled)
	})

	t.Run("override modelId disambiguates shared wire token", func(t *testing.T) {
		catalog := mustBuildCatalogJSON(t, `{
			"clientModelConfigs":[
				{"modelId":"first-low","label":"First (Low)","modelOrAlias":{"model":"SHARED"}},
				{"modelId":"second-high","label":"Second (High)","modelOrAlias":{"model":"SHARED"}}
			],
			"defaultOverrideModelConfig":{
				"modelId":"second-high",
				"modelOrAlias":{"model":"SHARED"}
			}
		}`)
		model, effort := catalog.settleSelection(agent.DefaultModelSentinel, agent.EffortAuto)
		assert.Equal(t, "second", model)
		assert.Equal(t, agent.EffortAuto, effort)
		second := agent.FindAvailableModel(catalog.models, "second")
		require.NotNil(t, second)
		assert.True(t, second.IsDefault)
	})

	t.Run("overwritten duplicate token does not falsely match listed variant", func(t *testing.T) {
		catalog := mustBuildCatalogJSON(t, `{
			"clientModelConfigs":[
				{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"OLD_TOKEN"}},
				{"modelId":"gemini-4-neon-high","label":"Gemini 4 Neon (High)","modelOrAlias":{"model":"NEW_TOKEN"}}
			],
			"defaultOverrideModelConfig":{
				"modelOrAlias":{"model":"OLD_TOKEN"}
			}
		}`)
		model, effort := catalog.settleSelection(agent.DefaultModelSentinel, agent.EffortAuto)
		assert.Equal(t, agent.DefaultModelSentinel, model)
		assert.Empty(t, effort)
		variant, settled, err := catalog.resolveModel(agent.DefaultModelSentinel, agent.EffortAuto)
		require.NoError(t, err)
		assert.Equal(t, "OLD_TOKEN", variant.token)
		assert.Empty(t, settled)
	})
}

func TestAntigravityCatalogBareEffortSuffixKeepsNonEmptyFamily(t *testing.T) {
	family, effort := splitModelEffort("-high")
	assert.Equal(t, "-high", family)
	assert.Empty(t, effort)

	catalog := mustBuildCatalogJSON(t, `{"clientModelConfigs":[
		{"modelId":"-high","label":"Dash High","modelOrAlias":{"model":"DASH_HIGH"}}
	]}`)
	assert.Equal(t, "-high", catalog.defaultModel)
	variant, settled, err := catalog.resolveModel(agent.DefaultModelSentinel, agent.EffortAuto)
	require.NoError(t, err)
	assert.Equal(t, "DASH_HIGH", variant.token)
	assert.Empty(t, settled)
}

func TestAntigravityCatalogMixedFamilyUsesUnsuffixedBaseForAutoRegardlessOfOrder(t *testing.T) {
	catalog := mustBuildCatalogJSON(t, `{"clientModelConfigs":[
		{"modelId":"foo-high","label":"Foo (High)","modelOrAlias":{"model":"FOO_HIGH"}},
		{"modelId":"foo","label":"Foo","modelOrAlias":{"model":"FOO_BASE"}}
	]}`)
	for _, requested := range []string{"", agent.EffortAuto} {
		variant, settled, err := catalog.resolveModel("foo", requested)
		require.NoError(t, err)
		assert.Equal(t, "FOO_BASE", variant.token)
		assert.Equal(t, agent.EffortAuto, settled)
	}
	variant, settled, err := catalog.resolveModel("foo", "high")
	require.NoError(t, err)
	assert.Equal(t, "FOO_HIGH", variant.token)
	assert.Equal(t, "high", settled)
}

func TestAntigravityCatalogSentinelFamilyDoesNotRecurse(t *testing.T) {
	t.Run("listed sentinel family resolves without recursion", func(t *testing.T) {
		catalog := mustBuildCatalogJSON(t, `{"clientModelConfigs":[
			{"modelId":"default-high","label":"Default (High)","modelOrAlias":{"model":"DEFAULT_HIGH"}}
		]}`)
		require.Len(t, catalog.models, 1)
		variant, settled, err := catalog.resolveModel(agent.DefaultModelSentinel, agent.EffortAuto)
		require.NoError(t, err)
		assert.Equal(t, "DEFAULT_HIGH", variant.token)
		assert.Equal(t, agent.EffortAuto, settled)
	})

	t.Run("unlisted override replaces sentinel family entry without duplicate model", func(t *testing.T) {
		catalog := mustBuildCatalogJSON(t, `{
			"clientModelConfigs":[
				{"modelId":"default-high","label":"Default (High)","modelOrAlias":{"model":"DEFAULT_HIGH"}}
			],
			"defaultOverrideModelConfig":{"modelOrAlias":{"model":"OVERRIDE_HIDDEN"}}
		}`)
		require.Len(t, catalog.models, 1)
		assert.Equal(t, agent.DefaultModelSentinel, catalog.models[0].Id)
		assert.Nil(t, catalog.models[0].SupportedEfforts)
		variant, settled, err := catalog.resolveModel(agent.DefaultModelSentinel, agent.EffortAuto)
		require.NoError(t, err)
		assert.Equal(t, "OVERRIDE_HIDDEN", variant.token)
		assert.Empty(t, settled)
	})
}
