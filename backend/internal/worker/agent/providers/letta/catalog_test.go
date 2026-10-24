package letta

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestLettaEffortCatalog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		availability any
		current      string
		wantModels   []string
		wantEfforts  []string
	}{
		{"available reasoning model", []string{"openai/one", "custom/plain"}, "openai/one", []string{"openai/one", "custom/plain"}, []string{agent.EffortAuto, "none", "low", "high"}},
		{"absent availability", nil, "openai/one", []string{"openai/one", "custom/plain", "openai/hidden"}, []string{agent.EffortAuto, "none", "low", "high"}},
		{"empty availability", []string{}, "openai/one", []string{"openai/one"}, nil},
		{"effort-less model", []string{"openai/one", "custom/plain"}, "custom/plain", []string{"openai/one", "custom/plain"}, nil},
		{"unknown current model", []string{"custom/plain"}, "custom/missing", []string{"custom/plain", "custom/missing"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &Agent{sink: agent.NewProviderServices(&agenttest.Sink{})}
			a.settings.model = tc.current
			a.settings.reasoningLevel = "high"
			var frame map[string]any
			require.NoError(t, json.Unmarshal([]byte(`{"type":"list_models_response","request_id":"lm-1","success":true,"entries":[
				{"id":"one-none","handle":"openai/one","label":"One","updateArgs":{"reasoning_effort":"none","provider_type":"openai"}},
				{"id":"one-low","handle":"openai/one","label":"One (Low)","updateArgs":{"reasoning_effort":"low"}},
				{"id":"one-high","handle":"openai/one","label":"One (High)","updateArgs":{"reasoning_effort":"high"}},
				{"id":"one-high-duplicate","handle":"openai/one","updateArgs":{"reasoning_effort":"high"}},
				{"id":"plain","handle":"custom/plain"},
				{"id":"plain-empty","handle":"custom/plain","updateArgs":{"reasoning_effort":""}},
				{"id":"plain-null","handle":"custom/plain","updateArgs":{"reasoning_effort":null}},
				{"id":"hidden","handle":"openai/hidden","updateArgs":{"reasoning_effort":"medium"}},
				{"id":"invalid","handle":"","updateArgs":{"reasoning_effort":"max"}}
			]}`), &frame))
			if tc.availability != nil {
				frame["available_handles"] = tc.availability
			}
			raw, err := json.Marshal(frame)
			require.NoError(t, err)
			a.handleFrame(raw)
			groups := a.OptionGroups()
			models := optionids.GroupByID(groups, agent.OptionIDModel)
			require.NotNil(t, models)
			var ids []string
			for _, model := range models.GetOptions() {
				ids = append(ids, model.GetId())
				if model.GetId() == "custom/plain" {
					assert.Empty(t, model.GetSubGroups())
					assert.Equal(t, "custom/plain", model.GetName())
				}
				if model.GetId() == "openai/one" && len(tc.wantEfforts) > 0 {
					assert.Equal(t, "One", model.GetName())
					subgroup := optionids.GroupByID(model.GetSubGroups(), agent.OptionIDEffort)
					require.NotNil(t, subgroup)
					assert.Len(t, subgroup.GetOptions(), len(tc.wantEfforts))
				}
			}
			assert.Equal(t, tc.wantModels, ids)
			effort := optionids.GroupByID(groups, agent.OptionIDEffort)
			if len(tc.wantEfforts) == 0 {
				assert.Nil(t, effort)
				return
			}
			require.NotNil(t, effort)
			ids = nil
			for _, option := range effort.GetOptions() {
				ids = append(ids, option.GetId())
			}
			assert.Equal(t, tc.wantEfforts, ids)
			assert.Equal(t, "high", effort.GetCurrentValue())
			assert.Equal(t, agent.EffortAuto, effort.GetDefaultValue())
			assert.True(t, effort.GetMutable())
			// A refusal must preserve both model choices and their effort capabilities.
			a.handleFrame([]byte(`{"type":"list_models_response","request_id":"lm-2","success":false,"entries":[]}`))
			assert.Equal(t, groups, a.OptionGroups())
		})
	}
}

func TestListModelsResponseFillsTheModelGroup(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.settings.model = "openai-compatible/one"
	a.handleFrame([]byte(`{"type":"list_models_response","request_id":"lm-1","success":true,"entries":[{"id":"one","handle":"openai-compatible/one","label":"One"},{"id":"two","handle":"openai-compatible/two","label":"Two"},{"id":"invalid","handle":"","label":"Invalid"}],"available_handles":["openai-compatible/one","openai-compatible/two"]}`))

	var modelIDs []string
	var modelNames []string
	for _, group := range a.OptionGroups() {
		if group.GetId() != agent.OptionIDModel {
			continue
		}
		for _, option := range group.GetOptions() {
			modelIDs = append(modelIDs, option.GetId())
			modelNames = append(modelNames, option.GetName())
		}
	}
	require.Len(t, modelIDs, 2)
	assert.Equal(t, []string{"openai-compatible/one", "openai-compatible/two"}, modelIDs)
	assert.Equal(t, []string{"One", "Two"}, modelNames)
}

func TestListModelsResponseExcludesUnavailableAndDuplicateHandles(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.settings.model = "openai-compatible/current"
	a.handleFrame([]byte(`{"type":"list_models_response","request_id":"lm-1","success":true,"entries":[{"handle":"openai-compatible/available","label":"Available"},{"handle":"openai-compatible/available","label":"Duplicate"},{"handle":"openai-compatible/unavailable","label":"Unavailable"},{"handle":"","label":"Invalid"}],"available_handles":["openai-compatible/available"]}`))

	var models []string
	for _, group := range a.OptionGroups() {
		if group.GetId() != agent.OptionIDModel {
			continue
		}
		for _, option := range group.GetOptions() {
			models = append(models, option.GetId())
		}
	}
	assert.Equal(t, []string{"openai-compatible/available", "openai-compatible/current"}, models)
}

func TestListModelsFailureKeepsTheLastCatalog(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.settings.model = "openai-compatible/one"
	a.handleFrame([]byte(`{"type":"list_models_response","request_id":"lm-1","success":true,"entries":[{"handle":"openai-compatible/one","label":"One"},{"handle":"openai-compatible/two","label":"Two"}]}`))
	a.handleFrame([]byte(`{"type":"list_models_response","request_id":"lm-2","success":false,"error":"catalog unavailable","entries":[]}`))

	var models []string
	for _, group := range a.OptionGroups() {
		if group.GetId() != agent.OptionIDModel {
			continue
		}
		for _, option := range group.GetOptions() {
			models = append(models, option.GetId())
		}
	}
	assert.Equal(t, []string{"openai-compatible/one", "openai-compatible/two"}, models)
}

func TestLettaCatalogAliasesFollowSuccessfulReplacement(t *testing.T) {
	t.Parallel()
	a := &Agent{sink: agent.NewProviderServices(&agenttest.Sink{})}
	a.handleFrame([]byte(`{"type":"list_models_response","success":true,"entries":[],"byok_provider_aliases":{"chatgpt-alias":"openai-codex"}}`))
	assert.Equal(t, "openai-codex", a.catalog.providerAliases["chatgpt-alias"])
	a.handleFrame([]byte(`{"type":"list_models_response","success":false,"entries":[],"byok_provider_aliases":{}}`))
	assert.Equal(t, "openai-codex", a.catalog.providerAliases["chatgpt-alias"])
	a.handleFrame([]byte(`{"type":"list_models_response","success":true,"entries":[]}`))
	assert.Empty(t, a.catalog.providerAliases)
}
