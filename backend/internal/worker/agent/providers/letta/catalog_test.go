package letta

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

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
