package copilot

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopilotNativeModelCatalog(t *testing.T) {
	models, err := parseCopilotModels(json.RawMessage(`{"list":[
		{"id":"gpt-5.6-luna","name":"GPT-5.6 Luna","capabilities":{"limits":{"max_context_window_tokens":1050000},"supports":{"reasoning_effort":["none","low","medium","high","xhigh","max","high",""]}}},
		{"id":"plain","capabilities":{"limits":{"max_context_window_tokens":-1}}},
		{"id":"future","capabilities":{"supports":{"reasoning_effort":["future-level","high","another-level"]}}}
	]}`))
	require.NoError(t, err)
	require.Len(t, models, 3)
	require.Equal(t, int64(1050000), models[0].ContextWindow)
	require.Empty(t, models[0].DefaultEffort, "the session catalogue does not specify a default effort")
	var efforts []string
	for _, effort := range models[0].SupportedEfforts {
		efforts = append(efforts, effort.Id)
	}
	require.Equal(t, []string{"max", "xhigh", "high", "medium", "low", "none"}, efforts)
	require.Equal(t, "plain", models[1].DisplayName)
	require.Zero(t, models[1].ContextWindow)
	require.Empty(t, models[1].SupportedEfforts)
	require.Equal(t, "high", models[2].SupportedEfforts[0].Id)
	require.Equal(t, "future-level", models[2].SupportedEfforts[1].Id)
	require.Equal(t, "another-level", models[2].SupportedEfforts[2].Id)
}

func TestCopilotNativeCatalogReadsSessionModels(t *testing.T) {
	models, err := parseCopilotModels(json.RawMessage(`{"list":[
		{"id":"session-model","name":"Session model","capabilities":{"limits":{"max_context_window_tokens":1050000},"supports":{"reasoning_effort":["none","low","high"]}}},
		{"id":"custom/provider-model","name":"Custom model","capabilities":{"supports":{"reasoning_effort":["custom-level"]}}}
	],"resolvedAuthLogin":"session-account"}`))
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.Equal(t, "session-model", models[0].Id)
	require.Equal(t, int64(1050000), models[0].ContextWindow)
	require.Equal(t, "high", models[0].SupportedEfforts[0].Id)
	require.Equal(t, "custom/provider-model", models[1].Id)
	require.Equal(t, "custom-level", models[1].SupportedEfforts[0].Id)
}

func TestCopilotNativeModelCatalogRejectsInvalidShapes(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"list":null}`, `{"list":{}}`, `{"models":[]}`,
		`{"list":[{}]}`, `{"list":[{"id":1}]}`,
		`{"list":[{"id":"same"},{"id":"same"}]}`,
		`{"list":[{"id":"bad","capabilities":{"supports":{"reasoning_effort":[1]}}}]}`,
	} {
		_, err := parseCopilotModels(json.RawMessage(raw))
		require.Error(t, err, raw)
	}
	models, err := parseCopilotModels(json.RawMessage(`{"list":[]}`))
	require.NoError(t, err)
	require.Empty(t, models)
}
