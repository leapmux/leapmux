package deepseekharness

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two models that offer the same tiers, as the models of one native connection do: the native
// adapter states one effort list for the whole connection. The models differ in their default.
const sharedTiersFixture = `{"default":{"provider":"deepseek-official","model":"deepseek-flash"},"groups":[{"id":"deepseek-official","name":"DeepSeek","models":[{"id":"deepseek-flash","name":"Flash","reasoning":{"defaultEffort":"high","efforts":[{"id":"off"},{"id":"low"},{"id":"high"},{"id":"max"}]}},{"id":"deepseek-pro","name":"Pro","reasoning":{"defaultEffort":"max","efforts":[{"id":"off"},{"id":"low"},{"id":"high"},{"id":"max"}]}}]}]}`

// The Worker sends the merged options of a model-only edit: the new model and the inherited effort.
// When the new model offers that tier, the tier survives. The native process accepts the model and
// the effort in one request, and it rejects an effort that the model does not offer without
// clamping it.
func TestUpdateSettingsKeepsTheInheritedEffortOnAModelSwitch(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	catalog, err := convertModelCatalog(decodeNativeCatalog(t, sharedTiersFixture))
	require.NoError(t, err)
	a.catalog = catalog
	a.selection = modelSelection{Provider: "deepseek-official", Model: "deepseek-flash", Effort: "low"}
	calls := serveNativeSettings(t, a)

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "deepseek-official/deepseek-pro", agent.OptionIDEffort: "low"})

	assert.Equal(t, "deepseek-official/deepseek-pro", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, "low", result.ConfirmedOptions()[agent.OptionIDEffort], "the new model offers low, so the tier survives instead of its default max")
	selection := calls.first("session/selectModel")
	require.NotNil(t, selection)
	assert.Equal(t, map[string]any{"sessionId": "native-root", "provider": "deepseek-official", "model": "deepseek-pro", "reasoningEffort": "low"}, selection["request"])
	assert.Equal(t, 1, countCalls(calls, "session/selectModel"), "the model and the effort travel in one request")
}

func countCalls(calls *nativeCalls, method string) int {
	count := 0
	for _, name := range calls.methods() {
		if name == method {
			count++
		}
	}
	return count
}
