package commandcode

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nativeEffortHost models the effort bookkeeping of the Command Code 1.74.1 host. It keeps one
// effort for each model. `session/set_model` changes the model and does not carry the effort over,
// so the state of the new model reports the effort that the host holds for that model, or none.
// `session/set_effort` records the effort for the current model.
type nativeEffortHost struct {
	model   string
	efforts map[string]string
	calls   []string
}

func (h *nativeEffortHost) handle(method string, params json.RawMessage) agenttest.RPCReply {
	h.calls = append(h.calls, method)
	switch method {
	case methodSetModel:
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(params, &request); err != nil || request.Model == "" {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"session/set_model requires a model"}`)}
		}
		h.model = request.Model
	case methodSetEffort:
		var request struct {
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(params, &request); err != nil || request.Effort == "" {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"session/set_effort requires an effort"}`)}
		}
		h.efforts[h.model] = request.Effort
	case methodSessionState:
		effort := "null"
		if held := h.efforts[h.model]; held != "" {
			effort = fmt.Sprintf("%q", held)
		}
		return agenttest.RPCReply{Result: json.RawMessage(fmt.Sprintf(
			`{"protocolVersion":1,"session":{"id":"native-session","model":%q,"effort":%s,"permissionMode":"default"}}`, h.model, effort))}
	default:
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"the fixture rejects this method"}`)}
	}
	return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
}

// The Worker sends the merged options of a model-only edit: the new model and the inherited effort.
// The host keeps its effort for each model, so the provider must send `session/set_effort` after
// `session/set_model` for the effort to survive. Without it the state of the new model reports the
// effort that the host holds for that model, or none.
func TestSettingsModelSwitchKeepsTheEffortThroughTheHost(t *testing.T) {
	for name, held := range map[string]map[string]string{
		"the host holds no effort for the new model":      {"model-a": "low"},
		"the host holds another effort for the new model": {"model-a": "low", "model-b": "high"},
	} {
		t.Run(name, func(t *testing.T) {
			host := &nativeEffortHost{model: "model-a", efforts: held}
			a := agentWithPeer(t, host.handle)
			a.Mu.Lock()
			a.model, a.effort = "model-a", "low"
			a.Mu.Unlock()

			result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "model-b", agent.OptionIDEffort: "low"})

			assert.Equal(t, []string{methodSetModel, methodSetEffort, methodSessionState}, host.calls, "the effort follows the model, then the state confirms both")
			require.True(t, result.AppliedLive, "the switch needs no restart")
			assert.Equal(t, "model-b", result.ConfirmedOptions()[agent.OptionIDModel])
			assert.Equal(t, "low", result.ConfirmedOptions()[agent.OptionIDEffort])
		})
	}
}
