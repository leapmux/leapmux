package providers

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// TestSavedDecisionCorpusPrompts requires that the Worker reads each captured
// request of testdata/saved_decision_corpus.json into the prompt that its case
// states, which is the prompt that the Worker records with the answer. The
// browser replays the same cases through each provider's saved-answer display
// with that prompt, so the words of a reloaded row come from a prompt that the
// Worker really records.
func TestSavedDecisionCorpusPrompts(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "saved_decision_corpus.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name     string          `json:"name"`
			Provider string          `json:"provider"`
			Request  json.RawMessage `json:"request"`
			Prompt   json.RawMessage `json:"prompt"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)

	plugins := make(map[leapmuxv1.AgentProvider]agent.Provider)
	for _, registration := range Registrations() {
		plugins[registration.Provider] = registration.Plugin
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			value, known := leapmuxv1.AgentProvider_value[tc.Provider]
			require.Truef(t, known, "the case states the unknown provider %q", tc.Provider)
			plugin, registered := plugins[leapmuxv1.AgentProvider(value)]
			require.Truef(t, registered, "no provider is registered for %q", tc.Provider)

			want := &leapmuxv1.ControlPrompt{}
			require.NoError(t, protojson.Unmarshal(tc.Prompt, want))
			got := agent.ReadControlPrompt(plugin, agent.NewControlRequestInput(tc.Request))
			agenttest.RequireProto(t, withCompactInput(t, want), withCompactInput(t, got))
		})
	}
}

// withCompactInput compacts the input_json of a permission prompt. A reader
// copies the request's own bytes into input_json, so the field follows the
// layout of the fixture file; the comparison reads it as JSON instead.
func withCompactInput(t *testing.T, prompt *leapmuxv1.ControlPrompt) *leapmuxv1.ControlPrompt {
	t.Helper()
	permission := prompt.GetPermission()
	if permission.GetInputJson() == "" {
		return prompt
	}
	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, []byte(permission.GetInputJson())))
	permission.InputJson = compact.String()
	return prompt
}
