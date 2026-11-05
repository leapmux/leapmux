package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func testAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := newAgentState(agent.NewProviderServices(sink), antigravityRPC{}, t.TempDir(), nil)
	a.catalog = buildAntigravityCatalog(antigravityCatalogReply{ClientModelConfigs: []antigravityModelConfig{{
		ModelID: "gemini-test-high", Label: "Gemini Test",
		ModelOrAlias: struct {
			Model string `json:"model"`
		}{Model: "MODEL_TEST"},
		SupportedMimeTypes: map[string]bool{"image/png": true, "application/pdf": true, "audio/wav": true, "video/mp4": true},
	}}})
	a.model = agent.DefaultModelSentinel
	a.Mu.Lock()
	a.installRoot("cascade", false)
	a.Mu.Unlock()
	return a, sink
}

func feedUpdate(t *testing.T, a *Agent, update agentStateUpdate) {
	t.Helper()
	requireApplyUpdate(t, a, update)
}

func requireApplyUpdate(t *testing.T, a *Agent, update agentStateUpdate) {
	t.Helper()
	a.Mu.Lock()
	root := a.root
	a.Mu.Unlock()
	if err := a.applyUpdate(root, update, false); err != nil {
		t.Fatalf("apply update: %v", err)
	}
}

func plannerStep(status, text string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"type": "CORTEX_STEP_TYPE_PLANNER_RESPONSE", "status": status,
		"plannerResponse": map[string]any{"modifiedResponse": text},
	})
	return raw
}

func updateWithSteps(status string, indices []int, steps []json.RawMessage) agentStateUpdate {
	return agentStateUpdate{Status: status, MainTrajectory: trajectoryPatch{StepsUpdate: stepsPatch{
		Indices: indices, Steps: steps, TotalLength: len(indices),
	}}}
}

func transcriptMessages(sink *agenttest.Sink) []agenttest.Message {
	messages := sink.Messages()
	filtered := make([]agenttest.Message, 0, len(messages))
	for _, message := range messages {
		if !message.TurnEnd {
			filtered = append(filtered, message)
		}
	}
	return filtered
}
