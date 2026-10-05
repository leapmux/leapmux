package qoder

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// controlLogStdin records every control request in order and acknowledges each one on the same
// fake process channel. The real Qoder process answers on stdout instead.
type controlLogStdin struct {
	agent    *Agent
	mu       sync.Mutex
	requests []map[string]any
}

func (s *controlLogStdin) Write(data []byte) (int, error) {
	var frame struct {
		RequestID string         `json:"request_id"`
		Request   map[string]any `json:"request"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.requests = append(s.requests, frame.Request)
	s.mu.Unlock()
	s.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{}}}`, frame.RequestID)))
	return len(data), nil
}

func (*controlLogStdin) Close() error { return nil }

func (s *controlLogStdin) logged(subtype string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var matching []map[string]any
	for _, request := range s.requests {
		if request["subtype"] == subtype {
			matching = append(matching, request)
		}
	}
	return matching
}

// Qoder reads the effort from the launch flag, and the effort does not depend on the model. The
// Worker sends the merged options of a model-only edit: the new model, the stored effort, which
// equals the running one, and the stored permission mode. The model switch must apply live over
// the control channel, and it must send no `reasoningEffort` field, because Qoder then keeps the
// effort that the process runs at.
func TestQoderModelSwitchKeepsTheRunningEffortWithoutARestart(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"model-a","displayName":"Model A"},{"value":"model-b","displayName":"Model B"}],"currentModel":"model-a"}`))
	a.effort = contracts.QoderEffortLevelLow
	stdin := &controlLogStdin{agent: a}
	a.SetStdinForTest(stdin)

	result := a.UpdateSettings(map[string]string{
		agent.OptionIDModel:          "model-b",
		agent.OptionIDEffort:         contracts.QoderEffortLevelLow,
		agent.OptionIDPermissionMode: contracts.QoderModeDefault,
	})

	setModel := stdin.logged(contracts.QoderControlRequestSubtypeSetModel)
	require.Len(t, setModel, 1, "the model choice reaches the control channel once")
	assert.Equal(t, "model-b", setModel[0]["model"])
	assert.NotContains(t, setModel[0], "reasoningEffort", "the request leaves the effort of the process alone")
	assert.NotContains(t, setModel[0], "reasoning_effort")
	require.True(t, result.AppliedLive, "an unchanged effort needs no restart")
	assert.Equal(t, "model-b", result.SurfacedOptions[agent.OptionIDModel])
	assert.Equal(t, contracts.QoderEffortLevelLow, result.SurfacedOptions[agent.OptionIDEffort])
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDEffort].State)
}
