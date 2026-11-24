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

// savedDecisionCase is one case of testdata/saved_decision_corpus.json.
type savedDecisionCase struct {
	Name      string          `json:"name"`
	Provider  string          `json:"provider"`
	RequestID string          `json:"requestId"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
	Prompt    json.RawMessage `json:"prompt"`
	Decision  json.RawMessage `json:"decision"`
}

// savedDecisionCorpus reads the cases of testdata/saved_decision_corpus.json.
func savedDecisionCorpus(t *testing.T) []savedDecisionCase {
	t.Helper()
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "saved_decision_corpus.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []savedDecisionCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	return fixture.Cases
}

// pluginOf is the registered provider of one case.
func (tc savedDecisionCase) pluginOf(t *testing.T) agent.Provider {
	t.Helper()
	value, known := leapmuxv1.AgentProvider_value[tc.Provider]
	require.Truef(t, known, "the case states the unknown provider %q", tc.Provider)
	for _, registration := range Registrations() {
		if registration.Provider == leapmuxv1.AgentProvider(value) {
			return registration.Plugin
		}
	}
	require.Failf(t, "no provider is registered", "%q", tc.Provider)
	return nil
}

// TestSavedDecisionCorpusPrompts requires that the Worker reads each captured
// request of testdata/saved_decision_corpus.json into the prompt that its case
// states, which is the prompt that the Worker records with the answer. The
// browser reads the same cases' prompts and decisions through the shared
// saved-answer display, so the words of a reloaded row come from a prompt that
// the Worker really records.
func TestSavedDecisionCorpusPrompts(t *testing.T) {
	t.Parallel()
	for _, tc := range savedDecisionCorpus(t) {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			want := &leapmuxv1.ControlPrompt{}
			require.NoError(t, protojson.Unmarshal(tc.Prompt, want))
			got := agent.ReadControlPrompt(tc.pluginOf(t), agent.NewControlRequestInput(tc.Request))
			agenttest.RequireProto(t, withCompactInput(t, want), withCompactInput(t, got))
		})
	}
}

// TestSavedDecisionCorpusDecisions requires that the decision of each case is
// the one that its captured response delivered: the provider answers the
// stored request with the decision, the answer addresses that request, and
// the Worker's resolution before delivery rebuilds the captured bytes. The
// browser words the saved row from the same decision, so the words state what
// the runtime received.
func TestSavedDecisionCorpusDecisions(t *testing.T) {
	t.Parallel()
	for _, tc := range savedDecisionCorpus(t) {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			plugin := tc.pluginOf(t)
			decision := &leapmuxv1.ControlDecision{}
			require.NoError(t, protojson.Unmarshal(tc.Decision, decision))
			answer, err := agenttest.AnswerControl(plugin, tc.Request, tc.RequestID, decision)
			require.NoError(t, err)
			require.Equal(t, tc.RequestID, plugin.ControlResponseRequestID(answer.Content), "the answer addresses the stored request")
			resolution := agenttest.ResolveAnswer(plugin, tc.Request, tc.RequestID, answer)
			require.False(t, resolution.Withhold, "the provider refuses the answer: %v", resolution.Refusal())
			delivered := resolution.Content
			if len(delivered) == 0 {
				// The Worker delivers the answer's own bytes when the resolution states none.
				delivered = answer.Content
			}
			require.JSONEq(t, string(tc.Response), string(delivered))
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
