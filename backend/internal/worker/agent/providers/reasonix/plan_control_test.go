package reasonix

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

const reasonixPlanPayload = `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"plan-session","toolCall":{"toolCallId":"gate-1","title":"exit_plan_mode","kind":"other","status":"pending","_meta":{"reasonix.io":{"approvalId":"1","fresh":false,"subject":"","tool":"exit_plan_mode"}}},"options":[{"optionId":"allow_once","name":"Allow","kind":"allow_once"},{"optionId":"reject_once","name":"Reject","kind":"reject_once"}]}}`

func reasonixDecision(t *testing.T, requestID, behavior, message string) []byte {
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

// resolveReasonixPlan resolves response to payload, stored as jsonrpc:3, as
// the Worker does. The answer carries no plan settings, as the Worker states
// none when it rebuilds a delivered answer from its row.
func resolveReasonixPlan(payload string, response []byte) agent.ControlResponseResolution {
	return agenttest.ResolveAnswer(reasonixProvider{}, []byte(payload), "jsonrpc:3", agent.ControlAnswer{Content: response})
}

func reasonixSelectedOption(t *testing.T, content []byte) (id, option string) {
	t.Helper()
	var reply struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(content, &reply))
	require.Equal(t, "selected", reply.Result.Outcome.Outcome)
	return string(reply.ID), reply.Result.Outcome.OptionID
}

func TestReasonixPlanApprovalReplies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		response []byte
		option   string
		exit     bool
		feedback string
	}{
		{name: "approve", response: reasonixDecision(t, "jsonrpc:3", contracts.ControlAnswerBehaviorAllow, ""), option: "allow_once", exit: true},
		{name: "reject with revision", response: reasonixDecision(t, "jsonrpc:3", contracts.ControlAnswerBehaviorDeny, "Split the second step."), option: "reject_once", feedback: "Split the second step."},
		{name: "reject bare", response: reasonixDecision(t, "jsonrpc:3", contracts.ControlAnswerBehaviorDeny, ""), option: "reject_once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := resolveReasonixPlan(reasonixPlanPayload, tc.response)
			require.False(t, result.Withhold)
			id, option := reasonixSelectedOption(t, result.Content)
			assert.Equal(t, "3", id)
			assert.Equal(t, tc.option, option)
			assert.Equal(t, agent.FeedbackFollowUp(tc.feedback), result.FollowUp)
			if tc.exit {
				assert.Equal(t, agent.PlanModeControlExit, result.PlanModeControl)
			} else {
				assert.Equal(t, agent.PlanModeControlNone, result.PlanModeControl)
			}
		})
	}
}

func TestReasonixPlanApprovalPreservesStringNativeID(t *testing.T) {
	t.Parallel()
	payload := strings.Replace(reasonixPlanPayload, `"id":3`, `"id":"native-3"`, 1)
	// The Worker keys a request with the JSON-RPC id "native-3" as below, and
	// the answer addresses that key.
	const requestID = `jsonrpc:"native-3"`
	result := agenttest.ResolveAnswer(reasonixProvider{}, []byte(payload), requestID,
		agent.ControlAnswer{Content: reasonixDecision(t, requestID, contracts.ControlAnswerBehaviorAllow, "")})
	require.False(t, result.Withhold)
	id, option := reasonixSelectedOption(t, result.Content)
	assert.Equal(t, `"native-3"`, id)
	assert.Equal(t, "allow_once", option)
}

// Each answer addresses the stored request, as every answer that the Worker
// resolves does. A stored request with no native id gives no key, so the
// Worker refuses it before Reasonix reads it (agent.StoredControlRequestAgrees),
// and the case pins Reasonix's own guard past that check.
func TestReasonixPlanApprovalWithholdsInvalidAnswers(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		payload  string
		response []byte
		reason   string
	}{
		"unknown behavior":  {payload: reasonixPlanPayload, response: reasonixDecision(t, "jsonrpc:3", "maybe", ""), reason: agent.RefusalNoDecision},
		"no behavior":       {payload: reasonixPlanPayload, response: reasonixDecision(t, "jsonrpc:3", "", ""), reason: agent.RefusalNoDecision},
		"missing native id": {payload: strings.Replace(reasonixPlanPayload, `"id":3,`, "", 1), response: reasonixDecision(t, "jsonrpc:3", contracts.ControlAnswerBehaviorAllow, ""), reason: agent.RefusalUnreadableRequest},
		"missing allow":     {payload: strings.Replace(reasonixPlanPayload, `{"optionId":"allow_once","name":"Allow","kind":"allow_once"},`, "", 1), response: reasonixDecision(t, "jsonrpc:3", contracts.ControlAnswerBehaviorAllow, ""), reason: agent.RefusalUnreadableRequest},
		"missing reject":    {payload: strings.Replace(reasonixPlanPayload, `,{"optionId":"reject_once","name":"Reject","kind":"reject_once"}`, "", 1), response: reasonixDecision(t, "jsonrpc:3", contracts.ControlAnswerBehaviorDeny, ""), reason: agent.RefusalUnreadableRequest},
		"malformed options": {payload: strings.Replace(reasonixPlanPayload, `"options":[`, `"options":{}, "unused":[`, 1), response: reasonixDecision(t, "jsonrpc:3", contracts.ControlAnswerBehaviorAllow, ""), reason: agent.RefusalUnreadableRequest},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := agenttest.ResolveAnswerPastTheKeyCheck(reasonixProvider{}, []byte(tc.payload), "jsonrpc:3", agent.ControlAnswer{Content: tc.response})
			assert.True(t, result.Withhold)
			assert.EqualError(t, result.Refusal(), tc.reason)
		})
	}
}

func TestReasonixPlanApprovalForwardsNativeReplies(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{
		`{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}`,
		`{"jsonrpc":"2.0","id":"jsonrpc:3","error":{"code":-32603,"message":"gone"}}`,
	} {
		result := resolveReasonixPlan(reasonixPlanPayload, []byte(reply))
		require.False(t, result.Withhold)
		assert.Equal(t, agent.PlanModeControlNone, result.PlanModeControl)
		assert.Equal(t, agent.FeedbackFollowUp(""), result.FollowUp)
	}
}

func TestReasonixPlanModePermissionModes(t *testing.T) {
	t.Parallel()
	provider := reasonixProvider{}
	assert.Equal(t, "plan", provider.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Equal(t, "normal", provider.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Empty(t, provider.PlanModePermissionMode(agent.PlanModeControlPrompt))
	assert.Empty(t, provider.PlanModePermissionMode(agent.PlanModeControlNone))
}
