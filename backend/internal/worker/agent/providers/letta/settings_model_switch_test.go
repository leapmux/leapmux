package letta

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Letta shows no effort axis in its menu, so an effort reaches it only through the CLI
// (`agent set --effort`) or the operator default. The Worker then stores the effort and sends it
// with every later edit, a model switch included. The provider must put the model and the stored
// effort into one `update_model` command, so the effort survives the switch. A model-only edit, with
// no stored effort, sends none, and the native model preset decides.
func TestUpdateSettingsModelSwitchCarriesTheStoredEffortInOneCommand(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		options optionmap.Map
		effort  any
		offered bool
	}{
		"a stored effort travels with the model": {
			options: optionmap.Map{agent.OptionIDModel: "openai-compatible/alternate", agent.OptionIDEffort: "high"},
			effort:  "high", offered: true,
		},
		"a model-only edit sends no effort": {
			options: optionmap.Map{agent.OptionIDModel: "openai-compatible/alternate"},
			offered: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake, a := newFakeAppServer(t)
			a.Mu.Lock()
			a.agentID = "agent-local-1"
			a.conversationID = "local-conv-1"
			a.settings.model = "openai-compatible/letta-e2e"
			a.settings.reasoningLevel = "high"
			a.Mu.Unlock()
			returned := make(chan agent.SettingsApplyResult, 1)
			go func() { returned <- a.UpdateSettings(tc.options) }()

			command := fake.nextCommand(t)
			require.Equal(t, "update_model", command["type"])
			payload, ok := command["payload"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "openai-compatible/alternate", payload["model_handle"])
			value, present := payload["reasoning_effort"]
			assert.Equal(t, tc.offered, present)
			assert.Equal(t, tc.effort, value)
			fake.replies <- []byte(fmt.Sprintf(`{"type":"update_model_response","request_id":%q,"success":true,"model_handle":"openai-compatible/alternate"}`, command["request_id"]))
			select {
			case result := <-returned:
				assert.Equal(t, "openai-compatible/alternate", result.ConfirmedOptions()[agent.OptionIDModel])
				if tc.offered {
					assert.Equal(t, "high", result.ConfirmedOptions()[agent.OptionIDEffort])
				} else {
					assert.NotContains(t, result.ConfirmedOptions(), agent.OptionIDEffort, "an edit that states no effort confirms none")
				}
			case <-time.After(30 * time.Second):
				t.Fatal("UpdateSettings did not return after the native model reply")
			}
		})
	}
}
