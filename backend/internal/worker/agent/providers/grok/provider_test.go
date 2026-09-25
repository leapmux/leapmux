package grok

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The worker stores a JSON-RPC request under the key `jsonrpc:<id>`, and the
// browser answers under that key.
const grokStoredRequestID = "jsonrpc:5"

// grokRequestPayload is a stored request of one method, with the native id 5.
func grokRequestPayload(t *testing.T, method string) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": method,
		"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_1"},
	})
	require.NoError(t, err)
	return payload
}

// neutralDecision is the browser's allow or deny envelope.
func neutralDecision(t *testing.T, requestID, behavior, message string) []byte {
	t.Helper()
	response := map[string]any{"behavior": behavior}
	if message != "" {
		response["message"] = message
	}
	data, err := json.Marshal(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": response},
	})
	require.NoError(t, err)
	return data
}

// selectedOption is the browser's permission reply that selects one option.
func selectedOption(t *testing.T, requestID, optionID string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": requestID,
		"result": map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}},
	})
	require.NoError(t, err)
	return data
}

func resolve(t *testing.T, method string, response []byte) agent.ControlResponseResolution {
	t.Helper()
	return grokProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       grokStoredRequestID,
		RequestPayload:  grokRequestPayload(t, method),
		ResponseContent: response,
	})
}

func TestGrokPlanApprovalReplies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		behavior string
		message  string
		want     string
		exit     bool
	}{
		{name: "approve", behavior: agent.ControlBehaviorAllow, want: `{"outcome":"approved"}`, exit: true},
		{name: "reject with a reason", behavior: agent.ControlBehaviorDeny, message: "Split step 2.", want: `{"outcome":"cancelled","feedback":"Split step 2."}`},
		{name: "reject bare", behavior: agent.ControlBehaviorDeny, want: `{"outcome":"cancelled"}`},
		{name: "reject with the placeholder", behavior: agent.ControlBehaviorDeny, message: agent.ControlRejectedByUserMessage, want: `{"outcome":"cancelled"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := resolve(t, contracts.GrokMethodExitPlanMode, neutralDecision(t, grokStoredRequestID, tc.behavior, tc.message))
			require.False(t, result.Withhold)
			var reply struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Result  json.RawMessage `json:"result"`
			}
			require.NoError(t, json.Unmarshal(result.Content, &reply))
			assert.Equal(t, "5", string(reply.ID), "the reply restores the native id")
			assert.JSONEq(t, tc.want, string(reply.Result))
			if tc.exit {
				assert.Equal(t, agent.PlanModeControlExit, result.PlanModeControl, "an approval leaves plan mode")
			} else {
				assert.Equal(t, agent.PlanModeControlNone, result.PlanModeControl)
			}
		})
	}
}

func TestGrokPlanApprovalWithholdsAnUnreadableAnswer(t *testing.T) {
	t.Parallel()
	for name, response := range map[string][]byte{
		"another request":  neutralDecision(t, "jsonrpc:6", agent.ControlBehaviorAllow, ""),
		"no request id":    neutralDecision(t, "", agent.ControlBehaviorAllow, ""),
		"unknown behavior": neutralDecision(t, grokStoredRequestID, "maybe", ""),
		"not json":         []byte(`{`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, resolve(t, contracts.GrokMethodExitPlanMode, response).Withhold)
		})
	}
}

func TestGrokElicitationRepliesUnderOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		action string
		want   string
	}{
		{action: "accept", want: `{"outcome":"accept","content":{"name":"x"}}`},
		{action: "decline", want: `{"outcome":"decline"}`},
		{action: "cancel", want: `{"outcome":"cancel"}`},
	} {
		t.Run(tc.action, func(t *testing.T) {
			t.Parallel()
			response, err := json.Marshal(map[string]any{
				"type": "control_response",
				"response": map[string]any{"request_id": grokStoredRequestID, "response": map[string]any{
					"action": tc.action, "content": map[string]any{"name": "x"},
				}},
			})
			require.NoError(t, err)
			result := resolve(t, contracts.GrokMethodMcpElicit, response)
			require.False(t, result.Withhold)
			var reply struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			require.NoError(t, json.Unmarshal(result.Content, &reply))
			assert.Equal(t, "5", string(reply.ID))
			assert.JSONEq(t, tc.want, string(reply.Result))
		})
	}
}

func TestGrokElicitationWithholdsAnUnreadableAnswer(t *testing.T) {
	t.Parallel()
	for name, response := range map[string]map[string]any{
		"unknown action":  {"request_id": grokStoredRequestID, "response": map[string]any{"action": "maybe"}},
		"another request": {"request_id": "jsonrpc:6", "response": map[string]any{"action": "accept"}},
		"a persist scope": {"request_id": grokStoredRequestID, "response": map[string]any{"action": "accept", "_meta": map[string]any{"persist": "always"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(map[string]any{"type": "control_response", "response": response})
			require.NoError(t, err)
			assert.True(t, resolve(t, contracts.GrokMethodMcpElicit, data).Withhold)
		})
	}
}

func TestGrokFolderTrustReplies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		response []byte
		want     string
	}{
		{name: "trust option", response: selectedOption(t, grokStoredRequestID, "trust"), want: `{"outcome":"trust"}`},
		{name: "reject option", response: selectedOption(t, grokStoredRequestID, "reject"), want: `{"outcome":"reject"}`},
		{name: "allow", response: neutralDecision(t, grokStoredRequestID, agent.ControlBehaviorAllow, ""), want: `{"outcome":"trust"}`},
		{name: "deny", response: neutralDecision(t, grokStoredRequestID, agent.ControlBehaviorDeny, "no"), want: `{"outcome":"reject"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := resolve(t, contracts.GrokMethodFolderTrust, tc.response)
			require.False(t, result.Withhold)
			var reply struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			require.NoError(t, json.Unmarshal(result.Content, &reply))
			assert.Equal(t, "5", string(reply.ID))
			assert.JSONEq(t, tc.want, string(reply.Result))
		})
	}
}

func TestGrokFolderTrustWithholdsAnUnreadableAnswer(t *testing.T) {
	t.Parallel()
	for name, response := range map[string][]byte{
		// Grok reads any word but `trust` as a rejection that it keeps for the
		// rest of the process, so a guess is never sent.
		"unknown option":   selectedOption(t, grokStoredRequestID, "allow-once"),
		"another request":  selectedOption(t, "jsonrpc:6", "trust"),
		"cancelled":        []byte(`{"jsonrpc":"2.0","id":"jsonrpc:5","result":{"outcome":{"outcome":"cancelled"}}}`),
		"unknown behavior": neutralDecision(t, grokStoredRequestID, "maybe", ""),
		"not json":         []byte(`nope`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, resolve(t, contracts.GrokMethodFolderTrust, response).Withhold)
		})
	}
}

func TestGrokForwardsQuestionAndPermissionReplies(t *testing.T) {
	t.Parallel()
	// The browser already writes Grok's own reply for these two.
	question := []byte(`{"jsonrpc":"2.0","id":"jsonrpc:5","result":{"outcome":"accepted","answers":{"Which?":["A"]}}}`)
	result := resolve(t, contracts.GrokMethodAskUserQuestion, question)
	require.False(t, result.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"outcome":"accepted","answers":{"Which?":["A"]}}}`, string(result.Content))

	permission := selectedOption(t, grokStoredRequestID, "allow-once")
	result = resolve(t, "session/request_permission", permission)
	require.False(t, result.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"allow-once"}}}`, string(result.Content))
}

func TestGrokResolveWithoutAStoredRequestForwards(t *testing.T) {
	t.Parallel()
	response := selectedOption(t, grokStoredRequestID, "trust")
	result := grokProvider{}.ResolveControlResponse(agent.ControlResponseContext{ResponseContent: response})
	assert.False(t, result.Withhold)
	assert.Equal(t, response, result.Content)
}

func TestGrokPlanModePermissionModes(t *testing.T) {
	t.Parallel()
	p := grokProvider{}
	assert.Equal(t, contracts.GrokModePlan, p.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Equal(t, contracts.GrokModeDefault, p.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlPrompt))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlNone))
}

func TestGrokRecognizesTheACPInterrupt(t *testing.T) {
	t.Parallel()
	assert.True(t, grokProvider{}.IsInterrupt(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`))
	assert.False(t, grokProvider{}.IsInterrupt(`{"jsonrpc":"2.0","method":"_x.ai/interject"}`))
}

func TestGrokResumeHandleIsAToken(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}

// A stored dialog that states no id has no request that a reply could answer,
// so the provider withholds each rewritten answer rather than send it with no
// id.
func TestGrokWithholdsAnAnswerToAStoredRequestWithNoID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		method   string
		response []byte
	}{
		{method: contracts.GrokMethodExitPlanMode, response: neutralDecision(t, grokStoredRequestID, agent.ControlBehaviorAllow, "")},
		{method: contracts.GrokMethodFolderTrust, response: neutralDecision(t, grokStoredRequestID, agent.ControlBehaviorAllow, "")},
		{method: contracts.GrokMethodFolderTrust, response: selectedOption(t, grokStoredRequestID, "trust")},
	} {
		payload, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": tc.method,
			"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_1"},
		})
		require.NoError(t, err)
		result := grokProvider{}.ResolveControlResponse(agent.ControlResponseContext{
			RequestID: grokStoredRequestID, RequestPayload: payload, ResponseContent: tc.response,
		})
		assert.True(t, result.Withhold, tc.method)
	}
}

// A stored payload that is not JSON states no dialog, so the shared resolution
// takes the answer. That rule cannot restore the native id of a request that
// it cannot read, so it withholds the reply rather than send it with the
// browser's id.
func TestGrokUnreadableStoredRequestTakesTheSharedRule(t *testing.T) {
	t.Parallel()
	result := grokProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID: grokStoredRequestID, RequestPayload: json.RawMessage(`not json`), ResponseContent: selectedOption(t, grokStoredRequestID, "trust"),
	})
	assert.True(t, result.Withhold)
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
