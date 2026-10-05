package codebuddy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// CodeBuddy reads the effort from the launch arguments of the process, and the effort does not
// depend on the model. The Worker sends the merged options of a model-only edit: the new model, the
// stored effort, which equals the launch effort, and the stored permission mode. The model switch
// must apply live, and it must send no effort in the `set_model` request, because the process then
// keeps the launch effort.
func TestCodebuddyModelSwitchKeepsTheLaunchEffortWithoutARestart(t *testing.T) {
	t.Parallel()
	a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(`{"session_id":"session-1","model":"custom-local:alternate","previous_model":"custom-local:primary"}`))
	a.mu.Lock()
	a.model = "custom-local:primary"
	a.models = []codebuddyModelInfo{{ID: "custom-local:primary"}, {ID: "custom-local:alternate"}}
	a.effort = contracts.CodebuddyEffortLevelLow
	a.permissionMode = contracts.CodebuddyModeDefault
	a.opts = agent.Options{Options: map[string]string{agent.OptionIDEffort: contracts.CodebuddyEffortLevelLow}}
	a.mu.Unlock()

	result := a.UpdateSettings(map[string]string{
		agent.OptionIDModel:          "custom-local:alternate",
		agent.OptionIDEffort:         contracts.CodebuddyEffortLevelLow,
		agent.OptionIDPermissionMode: contracts.CodebuddyModeDefault,
	})

	require.True(t, result.AppliedLive, "an unchanged effort needs no restart")
	assert.Equal(t, "custom-local:alternate", result.SurfacedOptions[agent.OptionIDModel])
	assert.Equal(t, contracts.CodebuddyEffortLevelLow, result.SurfacedOptions[agent.OptionIDEffort])
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDEffort].State)
	var setModel []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(peer.String()), "\n") {
		var sent struct {
			Request map[string]any `json:"request"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &sent))
		if sent.Request["subtype"] == "set_model" {
			setModel = append(setModel, sent.Request)
		}
	}
	require.Len(t, setModel, 1, "the model choice reaches the control channel once")
	assert.Equal(t, "custom-local:alternate", setModel[0]["model"])
	assert.NotContains(t, setModel[0], "effort", "the request leaves the effort of the process alone")
	assert.NotContains(t, setModel[0], "reasoning_effort")
}
