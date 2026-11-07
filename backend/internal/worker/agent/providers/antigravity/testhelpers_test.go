package antigravity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/require"
)

func testAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := newAgentState(agent.NewProviderServices(sink), antigravityRPC{}, t.TempDir(), nil)
	t.Cleanup(a.streamCancel)
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
	a.Mu.Lock()
	root := a.root
	a.Mu.Unlock()
	if err := a.applyUpdate(root, update, false); err != nil {
		t.Fatalf("apply update: %v", err)
	}
}

func testAntigravityRPC(t *testing.T, handler http.HandlerFunc) antigravityRPC {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	return antigravityRPC{endpoint: endpoint, timeout: 30 * time.Second}
}

func receiveTestValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	select {
	case value := <-values:
		return value
	case <-ctx.Done():
		t.Fatalf("receive the test result: %v", ctx.Err())
		var zero T
		return zero
	}
}

func newTestGate(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	return gate, release
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
