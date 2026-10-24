package dirac

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The fake server follows Dirac's own rules (cli/src/agent/sessionConfig.ts of Dirac):
//
//   - A model write never touches reasoning_effort. The value set is the same for every
//     model, and the option is always present.
//   - The server sends config_option_update before it replies to the write.
//
// The worker merges the stored level into a model switch, so UpdateSettings receives
// {model: B, reasoning_effort: X}.
func TestDiracModelSwitchKeepsTheReasoningEffort(t *testing.T) {
	t.Parallel()
	levels := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	server := &acptest.ModelSwitchServer{
		EffortID: contracts.DiracConfigReasoningEffort,
		Models:   []string{"model-a", "model-b"},
		Levels:   map[string][]string{"model-a": levels, "model-b": levels},
	}
	server.Start("model-a", "xhigh")
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		server.Respond,
	)
	*a.HooksForTest() = a.configure(nil)
	server.Seed(a, true)

	result := a.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", contracts.DiracConfigReasoningEffort: "xhigh"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()), "the server kept the level, so no second write follows")
	assert.Equal(t, "xhigh", agent.CurrentOptions(a.OptionGroups())[contracts.DiracConfigReasoningEffort])
}

func TestYoloChangesSettlePendingToolsAndRestoreManualApproval(t *testing.T) {
	t.Parallel()
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(request agenttest.RecordedRequest) agenttest.RPCReply {
			assert.Equal(t, acp.MethodSessionSetConfigOption, request.Method)
			assert.Equal(t, contracts.DiracConfigYolo, request.Params["configId"])
			assert.Equal(t, "boolean", request.Params["type"])
			value, valid := request.Params["value"].(bool)
			assert.True(t, valid, "the native request must contain a boolean")
			encoded, err := json.Marshal(map[string]any{"configOptions": []map[string]any{
				{"id": contracts.DiracConfigYolo, "type": "boolean", "currentValue": value},
			}})
			assert.NoError(t, err)
			return agenttest.RPCReply{Result: encoded}
		},
	)
	*a.HooksForTest() = a.configure(nil)
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.Mu.Lock()
	a.ApplyOptionGroupsLockedForTest([]acp.ConfigOption{{ID: contracts.DiracConfigYolo, Type: "boolean", CurrentValue: "false"}})
	a.Mu.Unlock()
	publish := func(id int) {
		frame := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"name":"execute_command","title":"Execute command","kind":"execute"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}}`, id))
		a.PublishSessionControlRequest(providerkit.ParseLine(frame), map[string]any{"outcome": map[string]string{"outcome": "cancelled"}})
	}
	replies := func() map[string]string {
		var lines []string
		for _, request := range requests() {
			lines = append(lines, request.Raw)
		}
		return agenttest.JSONRPCResultsByID(t, strings.Join(lines, "\n"))
	}
	publish(101)
	require.Equal(t, 1, sink.PublishedControlCount())
	result := a.UpdateSettings(map[string]string{contracts.DiracConfigYolo: "true"})
	require.True(t, result.AppliedLive)
	assert.Equal(t, "true", result.ConfirmedOptions().Get(contracts.DiracConfigYolo))
	testutil.AssertEventually(t, func() bool { return replies()["101"] != "" }, "the pending native tool receives its approval")
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"allow_once"}}`, replies()["101"])
	testutil.AssertEventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, "the answered card retires")
	publish(102)
	testutil.AssertEventually(t, func() bool { return replies()["102"] != "" }, "the next native tool receives its approval")
	assert.JSONEq(t, replies()["101"], replies()["102"])
	assert.Equal(t, 1, sink.PublishedControlCount(), "the next tool needs no manual card")
	result = a.UpdateSettings(map[string]string{contracts.DiracConfigYolo: "false"})
	require.True(t, result.AppliedLive)
	assert.Equal(t, "false", result.ConfirmedOptions().Get(contracts.DiracConfigYolo))
	publish(103)
	assert.Equal(t, 2, sink.PublishedControlCount(), "disabling YOLO restores manual approval")
	assert.NotContains(t, replies(), "103")
}
