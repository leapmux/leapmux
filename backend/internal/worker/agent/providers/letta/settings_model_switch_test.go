package letta

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestLettaModelUpdateRejectsUnconfirmedSnapshots(t *testing.T) {
	t.Parallel()
	for name, alter := range map[string]func(map[string]any){
		"another runtime": func(reply map[string]any) {
			reply["runtime"] = map[string]string{"agent_id": "foreign", "conversation_id": "foreign"}
		},
		"absent model":       func(reply map[string]any) { delete(reply, "model_handle") },
		"absent settings":    func(reply map[string]any) { delete(reply, "model_settings") },
		"malformed settings": func(reply map[string]any) { reply["model_settings"] = []string{"high"} },
		"another command":    func(reply map[string]any) { reply["type"] = "conversation_update_response" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake, a := openedAgent(t)
			configureLettaEffortTest(a)
			returned := make(chan agent.SettingsApplyResult, 1)
			go func() { returned <- a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "openai/two"}) }()
			command := fake.nextCommand(t)
			require.Equal(t, "update_model", command["type"])
			reply := map[string]any{
				"type": "update_model_response", "request_id": command["request_id"], "success": true,
				"runtime": command["runtime"], "model_handle": "openai/two", "model_settings": map[string]any{"reasoning_effort": "high"},
			}
			alter(reply)
			raw, err := json.Marshal(reply)
			require.NoError(t, err)
			fake.replies <- raw
			result := waitModeResult(t, returned)
			assert.False(t, result.AppliedLive)
			assert.Empty(t, result.ConfirmedOptions())
			assert.Equal(t, "openai/one", a.settings.model)
			assert.Equal(t, "low", a.settings.reasoningLevel)
		})
	}
}

// A model switch selects a native preset. The reply, not the request, confirms effort.
func TestLettaModelSwitchConfirmsNativeEffort(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		options optionmap.Map
		variant string
		applied string
	}{
		"a stored effort travels with the model": {
			options: optionmap.Map{agent.OptionIDModel: "openai/two", agent.OptionIDEffort: "high"},
			variant: "two-high", applied: "high",
		},
		"a model-only edit sends no effort": {
			options: optionmap.Map{agent.OptionIDModel: "openai/two"},
			applied: "low",
		},
		"the native reply overrides the requested effort": {
			options: optionmap.Map{agent.OptionIDModel: "openai/two", agent.OptionIDEffort: "high"},
			variant: "two-high", applied: "low",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake, a := newFakeAppServer(t)
			a.Mu.Lock()
			a.agentID = "agent-local-1"
			a.conversationID = "local-conv-1"
			a.settings.model = "openai/one"
			a.settings.reasoningLevel = "high"
			a.catalog.models = []lettaModel{{id: "openai/two", variants: []lettaModelVariant{
				{id: "two-low", effort: "low", providerType: "openai"},
				{id: "two-high", effort: "high", providerType: "openai"},
			}}}
			a.Mu.Unlock()
			returned := make(chan agent.SettingsApplyResult, 1)
			go func() { returned <- a.UpdateSettings(tc.options) }()

			command := fake.nextCommand(t)
			require.Equal(t, "update_model", command["type"])
			payload, ok := command["payload"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "openai/two", payload["model_handle"])
			assert.NotContains(t, payload, "reasoning_effort", "ordinary model handles ignore this field")
			if tc.variant == "" {
				assert.NotContains(t, payload, "model_id")
			} else {
				assert.Equal(t, tc.variant, payload["model_id"])
			}
			fake.replies <- []byte(fmt.Sprintf(`{"type":"update_model_response","request_id":%q,"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"success":true,"model_handle":"openai/two","model_settings":{"provider_type":"openai","reasoning_effort":%q}}`, command["request_id"], tc.applied))
			select {
			case result := <-returned:
				assert.Equal(t, "openai/two", result.ConfirmedOptions()[agent.OptionIDModel])
				assert.Equal(t, tc.applied, result.ConfirmedOptions()[agent.OptionIDEffort])
			case <-time.After(30 * time.Second):
				t.Fatal("UpdateSettings did not return after the native model reply")
			}
		})
	}
}
