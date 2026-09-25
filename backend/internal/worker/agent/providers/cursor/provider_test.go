package cursor

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveControlResponse_CursorCreatePlanTransformsResponse(t *testing.T) {
	t.Parallel()

	res := cursorProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"method":"cursor/create_plan",
			"params":{}
		}`),
		ResponseContent: []byte(`{
			"response":{
				"request_id":"7",
				"response":{"behavior":"deny","message":"Needs tests."}
			}
		}`),
	})

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

	res := cursorProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0",
			"id":"plan-7",
			"method":"cursor/create_plan",
			"params":{}
		}`),
		ResponseContent: []byte(`{
			"response":{
				"request_id":"plan-7",
				"response":{"behavior":"allow"}
			}
		}`),
	})

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

	res := cursorProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0",
			"id":"plan-7",
			"method":"cursor/create_plan",
			"params":{}
		}`),
		ResponseContent: []byte(`{
			"response":{
				"request_id":"plan-7",
				"response":{"behavior":"deny","message":"Rejected by user."}
			}
		}`),
	})

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
	// method-only. The raw response is forwarded unchanged.
	content := []byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"rejected","reason":"No"}}}`)
	res := cursorProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload: []byte(`{
			"jsonrpc":"2.0",
			"id":7,
			"method":"cursor/create_plan",
			"params":{}
		}`),
		ResponseContent: content,
	})

	assert.Equal(t, content, res.Content)
}

// The cursor questions answer travels unchanged: only create_plan needs a transform.
func TestCursorProviderForwardsAQuestionAnswerUnchanged(t *testing.T) {
	t.Parallel()
	response := []byte(`{"id":7,"result":{"outcome":{"outcome":"answered","answers":[{"questionId":"color","selectedOptionIds":["red"]}]}}}`)
	res := cursorProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload:  []byte(`{"id":7,"method":"cursor/ask_question","params":{}}`),
		ResponseContent: response,
	})
	assert.Equal(t, response, res.Content)
	assert.False(t, res.Withhold)
}

// A stop withdraws the plan request, and Cursor blocks until that request has an
// outcome, so the withdrawal has to send one.
func TestCursorPlanRequestCarriesARejectedCancelAnswer(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	agent := newCursorAgentWithSink(agent.NewProviderServices(sink))
	var stdin bytes.Buffer
	agent.SetStdinForTest(agenttest.NopStdin(&stdin))
	agent.SetSessionIDForTest("session-1")
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{}}`))
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","id":8,"method":"cursor/ask_question","params":{}}`))
	require.Len(t, sink.PublishedControls(), 2)
	require.NoError(t, agent.Interrupt())
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
