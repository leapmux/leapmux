package cursor

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorRefusesEmbeddedBlobAttachments(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
	for _, kind := range []contracts.AttachmentMediaKind{contracts.AttachmentMediaKindText, contracts.AttachmentMediaKindImage} {
		assert.NoError(t, provider.ValidateAttachment(agentapi.ClassifiedAttachment{Kind: kind, Filename: "input"}), kind)
	}
	for _, kind := range []contracts.AttachmentMediaKind{contracts.AttachmentMediaKindPdf, contracts.AttachmentMediaKindBinary} {
		assert.ErrorContains(t, provider.ValidateAttachment(agentapi.ClassifiedAttachment{Kind: kind, Filename: "input"}), string(kind))
	}
}

// Each answer below addresses the Worker's key of its request: "jsonrpc:"
// and the request's JSON-RPC id, as the Worker stores a Cursor request.

func TestResolveControlResponse_CursorCreatePlanTransformsResponse(t *testing.T) {
	t.Parallel()

	res := agenttest.ResolveAnswer(cursorProvider{}, []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"method":"cursor/create_plan",
			"params":{}
		}`), "jsonrpc:7", agentapi.ControlAnswer{Content: []byte(`{
			"response":{
				"request_id":"jsonrpc:7",
				"response":{"behavior":"deny","message":"Needs tests."}
			}
		}`)})

	// The plan decision renders from the transformed outcome alone, so the pruned context is
	// method-only.
	var normalized struct {
		ID     int `json:"id"`
		Result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			} `json:"outcome"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Content, &normalized))
	assert.Equal(t, 7, normalized.ID)
	assert.Equal(t, "rejected", normalized.Result.Outcome.Outcome)
	assert.Equal(t, "Needs tests.", normalized.Result.Outcome.Reason)
}

func TestResolveControlResponse_CursorCreatePlanAcceptsResponse(t *testing.T) {
	t.Parallel()

	res := agenttest.ResolveAnswer(cursorProvider{}, []byte(`{
			"jsonrpc":"2.0",
			"id":"plan-7",
			"method":"cursor/create_plan",
			"params":{}
		}`), `jsonrpc:"plan-7"`, agentapi.ControlAnswer{Content: []byte(`{
			"response":{
				"request_id":"jsonrpc:\"plan-7\"",
				"response":{"behavior":"allow"}
			}
		}`)})

	var normalized struct {
		ID     string `json:"id"`
		Result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			} `json:"outcome"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Content, &normalized))
	assert.Equal(t, "plan-7", normalized.ID)
	assert.Equal(t, "accepted", normalized.Result.Outcome.Outcome)
	assert.Empty(t, normalized.Result.Outcome.Reason)
}

func TestResolveControlResponse_CursorCreatePlanRejectsDefaultMessageAsReject(t *testing.T) {
	t.Parallel()

	res := agenttest.ResolveAnswer(cursorProvider{}, []byte(`{
			"jsonrpc":"2.0",
			"id":"plan-7",
			"method":"cursor/create_plan",
			"params":{}
		}`), `jsonrpc:"plan-7"`, agentapi.ControlAnswer{Content: []byte(`{
			"response":{
				"request_id":"jsonrpc:\"plan-7\"",
				"response":{"behavior":"deny","message":"Rejected by user."}
			}
		}`)})

	var normalized struct {
		Result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			} `json:"outcome"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Content, &normalized))
	assert.Equal(t, "rejected", normalized.Result.Outcome.Outcome)
	assert.Empty(t, normalized.Result.Outcome.Reason)
}

func TestResolveControlResponse_CursorCreatePlanIgnoresMalformedEnvelope(t *testing.T) {
	t.Parallel()

	// The response isn't the neutral envelope, so the transform bails and the create-plan request
	// falls through to the ACP permission context -- which has no options, so it degrades to
	// method-only. The raw response is forwarded with its result unchanged, under Cursor's own id.
	content := agentapi.JSONRPCResult("jsonrpc:7", map[string]any{"outcome": map[string]any{"outcome": "rejected", "reason": "No"}})
	res := agenttest.ResolveAnswer(cursorProvider{}, []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"method":"cursor/create_plan",
			"params":{}
		}`), "jsonrpc:7", agentapi.ControlAnswer{Content: content})

	require.NoError(t, res.Refusal())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"rejected","reason":"No"}}}`, string(res.Content))
}

// The cursor questions answer travels unchanged under Cursor's own id: only
// create_plan needs a transform.
func TestCursorProviderForwardsAQuestionAnswerUnchanged(t *testing.T) {
	t.Parallel()
	response := agentapi.JSONRPCResult("jsonrpc:7", map[string]any{"outcome": map[string]any{
		"outcome": "answered", "answers": []any{map[string]any{"questionId": "color", "selectedOptionIds": []string{"red"}}},
	}})
	res := agenttest.ResolveAnswer(cursorProvider{}, []byte(`{"id":7,"method":"cursor/ask_question","params":{}}`), "jsonrpc:7", agentapi.ControlAnswer{Content: response})
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"answered","answers":[{"questionId":"color","selectedOptionIds":["red"]}]}}}`, string(res.Content))
	assert.False(t, res.Withhold)
}

// A stop withdraws the plan request, and Cursor blocks until that request has an
// outcome, so the withdrawal has to send one.
func TestCursorPlanRequestCarriesARejectedCancelAnswer(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	agent := newCursorAgentWithSink(agentapi.NewProviderServices(sink))
	var stdin bytes.Buffer
	agent.SetStdinForTest(agenttest.NopStdin(&stdin))
	agent.SetSessionIDForTest("session-1")
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{}}`))
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","id":8,"method":"cursor/ask_question","params":{}}`))
	require.Len(t, sink.PublishedControls(), 2)
	require.NoError(t, agent.Interrupt(agentapi.StopContext{}))
	answers := agenttest.JSONRPCResultsByID(t, stdin.String())
	assert.JSONEq(t, `{"outcome":{"outcome":"rejected"}}`, answers[`7`])
	assert.NotContains(t, answers, `8`, "Cursor defines no outcome for a withdrawn question")
	assert.ElementsMatch(t, []string{"jsonrpc:7", "jsonrpc:8"}, sink.CanceledControls())
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}

func TestResolveControlResponse_CursorCreatePlanRefusesInvalidNeutralEnvelope(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		payload  string
		response string
	}{
		"unknown behavior": {
			payload:  `{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{}}`,
			response: `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"behavior":"maybe"}}}`,
		},
		"missing request id": {
			payload:  `{"jsonrpc":"2.0","method":"cursor/create_plan","params":{}}`,
			response: `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"behavior":"allow"}}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := agenttest.ResolveAnswerPastTheKeyCheck(cursorProvider{}, []byte(tc.payload), "jsonrpc:7", agentapi.ControlAnswer{Content: []byte(tc.response)})
			assert.True(t, res.Withhold, "invalid neutral create_plan response must be withheld")
			assert.Error(t, res.Refusal())
		})
	}
}
