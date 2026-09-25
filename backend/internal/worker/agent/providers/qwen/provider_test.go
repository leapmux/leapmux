package qwen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The worker stores a JSON-RPC request under the key `jsonrpc:<id>`, and the
// browser answers under that key.
const qwenStoredRequestID = "jsonrpc:5"

// planApprovalPayload is Qwen's plan approval, as the worker stores it.
const planApprovalPayload = `{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"s",` +
	`"options":[{"optionId":"restore_previous","name":"Yes, restore previous mode (default)","kind":"allow_once"},` +
	`{"optionId":"proceed_always","name":"Yes, and auto-accept edits","kind":"allow_always"},` +
	`{"optionId":"proceed_once","name":"Yes, and manually approve edits","kind":"allow_once"},` +
	`{"optionId":"cancel","name":"No, keep planning (esc)","kind":"reject_once"}],` +
	`"toolCall":{"toolCallId":"call_89a2ceb0ea","status":"pending","title":"Plan:","kind":"switch_mode","rawInput":{"plan":"1. Do X"},"_meta":{"toolName":"exit_plan_mode"}}}}`

// shellPermissionPayload is an ordinary permission request.
const shellPermissionPayload = `{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"s",` +
	`"options":[{"optionId":"proceed_once","name":"Allow","kind":"allow_once"},{"optionId":"cancel","name":"Reject","kind":"reject_once"}],` +
	`"toolCall":{"toolCallId":"call_1","kind":"execute","rawInput":{"command":"touch x"},"_meta":{"toolName":"run_shell_command"}}}}`

// decision is the browser's allow or deny envelope.
func decision(t *testing.T, requestID, behavior, message string) []byte {
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

func resolveWith(t *testing.T, payload string, response []byte, settings *leapmuxv1.PlanApprovalSettings) agent.ControlResponseResolution {
	t.Helper()
	return qwenProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       qwenStoredRequestID,
		RequestPayload:  json.RawMessage(payload),
		ResponseContent: response,
		PlanApproval:    settings,
	})
}

// selectedOptionOf reads the option a reply selects, and its native id.
func selectedOptionOf(t *testing.T, content []byte) (id, option string) {
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

func TestQwenPlanApprovalReplies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		response []byte
		settings *leapmuxv1.PlanApprovalSettings
		option   string
		exit     bool
		feedback string
	}{
		{name: "approve", response: decision(t, qwenStoredRequestID, agent.ControlBehaviorAllow, ""), option: contracts.QwenPermissionOptionProceedOnce, exit: true},
		{
			name: "approve into auto edit", response: decision(t, qwenStoredRequestID, agent.ControlBehaviorAllow, ""),
			settings: &leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.QwenModeAutoEdit},
			option:   contracts.QwenPermissionOptionProceedAlways, exit: true,
		},
		{
			name: "approve into yolo", response: decision(t, qwenStoredRequestID, agent.ControlBehaviorAllow, ""),
			settings: &leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.QwenModeYolo},
			option:   contracts.QwenPermissionOptionProceedOnce, exit: true,
		},
		{name: "reject with a reason", response: decision(t, qwenStoredRequestID, agent.ControlBehaviorDeny, "Split step 2."), option: contracts.QwenPermissionOptionCancel, feedback: "Split step 2."},
		{name: "reject bare", response: decision(t, qwenStoredRequestID, agent.ControlBehaviorDeny, ""), option: contracts.QwenPermissionOptionCancel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := resolveWith(t, planApprovalPayload, tc.response, tc.settings)
			require.False(t, result.Withhold)
			id, option := selectedOptionOf(t, result.Content)
			assert.Equal(t, "5", id, "the reply restores the native id")
			assert.Equal(t, tc.option, option)
			assert.Equal(t, tc.feedback, result.Feedback, "Qwen's reply has no reason field, so the reason follows as a message")
			if tc.exit {
				assert.Equal(t, agent.PlanModeControlExit, result.PlanModeControl)
			} else {
				assert.Equal(t, agent.PlanModeControlNone, result.PlanModeControl)
			}
		})
	}
}

func TestQwenPlanApprovalWithholdsAnUnreadableAnswer(t *testing.T) {
	t.Parallel()
	for name, response := range map[string][]byte{
		"another request":  decision(t, "jsonrpc:6", agent.ControlBehaviorAllow, ""),
		"no request id":    decision(t, "", agent.ControlBehaviorAllow, ""),
		"unknown behavior": decision(t, qwenStoredRequestID, "maybe", ""),
		"not json":         []byte(`{`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, resolveWith(t, planApprovalPayload, response, nil).Withhold)
		})
	}
}

// A stored plan approval that states no id has no request that a reply could
// answer, so the provider withholds the answer rather than send it with no id.
func TestQwenPlanApprovalWithNoStoredIDWithholdsTheAnswer(t *testing.T) {
	t.Parallel()
	payload := strings.Replace(planApprovalPayload, `"id":5,`, "", 1)
	require.NotEqual(t, planApprovalPayload, payload)
	require.True(t, isPlanApproval(json.RawMessage(payload)), "the request is still the plan approval")
	for _, behavior := range []string{agent.ControlBehaviorAllow, agent.ControlBehaviorDeny} {
		assert.True(t, resolveWith(t, payload, decision(t, qwenStoredRequestID, behavior, ""), nil).Withhold, behavior)
	}
}

// A JSON-RPC error that the browser sends for the plan approval is Qwen's own
// reply already, so the provider forwards it with the native id and does not
// read it as an allow or a deny.
func TestQwenForwardsAnErrorReplyOfThePlanApproval(t *testing.T) {
	t.Parallel()
	reply := []byte(`{"jsonrpc":"2.0","id":"jsonrpc:5","error":{"code":-32603,"message":"gone"}}`)
	result := resolveWith(t, planApprovalPayload, reply, nil)
	require.False(t, result.Withhold)
	assert.Equal(t, agent.PlanModeControlNone, result.PlanModeControl, "an error reply decides no plan")
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"error":{"code":-32603,"message":"gone"}}`, string(result.Content))
}

func TestQwenIsJSONRPCReply(t *testing.T) {
	t.Parallel()
	for content, want := range map[string]bool{
		`{"jsonrpc":"2.0","id":1,"result":{}}`:        true,
		`{"jsonrpc":"2.0","id":1,"result":null}`:      true,
		`{"jsonrpc":"2.0","id":1,"error":{"code":1}}`: true,
		`{"type":"control_response","response":{}}`:   false,
		`{}`:              false,
		`[{"result":{}}]`: false,
		`"result"`:        false,
		`not json`:        false,
		``:                false,
	} {
		assert.Equal(t, want, isJSONRPCReply([]byte(content)), content)
	}
}

func TestQwenForwardsASelectedOptionOfThePlanApproval(t *testing.T) {
	t.Parallel()
	reply := []byte(`{"jsonrpc":"2.0","id":"jsonrpc:5","result":{"outcome":{"outcome":"selected","optionId":"restore_previous"}}}`)
	result := resolveWith(t, planApprovalPayload, reply, nil)
	require.False(t, result.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"restore_previous"}}}`, string(result.Content))
}

func TestQwenForwardsAnOrdinaryPermissionAndAQuestion(t *testing.T) {
	t.Parallel()
	reply := []byte(`{"jsonrpc":"2.0","id":"jsonrpc:5","result":{"outcome":{"outcome":"selected","optionId":"proceed_once"},"answers":{"0":"Blue"}}}`)
	result := resolveWith(t, shellPermissionPayload, reply, nil)
	require.False(t, result.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":5,"result":{"outcome":{"outcome":"selected","optionId":"proceed_once"},"answers":{"0":"Blue"}}}`, string(result.Content),
		"the answers of a question survive the forward")
}

func TestQwenIsPlanApproval(t *testing.T) {
	t.Parallel()
	assert.True(t, isPlanApproval(json.RawMessage(planApprovalPayload)))
	assert.False(t, isPlanApproval(json.RawMessage(shellPermissionPayload)))
	assert.False(t, isPlanApproval(json.RawMessage(`{"method":"session/request_permission"}`)))
	assert.False(t, isPlanApproval(json.RawMessage(`{"method":"_x/other","params":{"toolCall":{"_meta":{"toolName":"exit_plan_mode"}}}}`)))
	assert.False(t, isPlanApproval(json.RawMessage(`not json`)))
}

func TestQwenPlanModePermissionModes(t *testing.T) {
	t.Parallel()
	p := qwenProvider{}
	assert.Equal(t, contracts.QwenModePlan, p.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Equal(t, contracts.QwenModeDefault, p.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlPrompt))
	assert.Empty(t, p.PlanModePermissionMode(agent.PlanModeControlNone))
}

func TestQwenResumeHandleIsAToken(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}

// Qwen raises every control request as the STANDARD Agent Client Protocol
// permission request, so its requests reach the shared dispatcher and keep the
// shared identity rule.
func TestQwenControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	agenttest.AssertControlIdentitiesStaySeparate(t, sink, a.HandleOutput, "session/request_permission")
}

func TestQwenRegistrationStatesTheSafeDefaults(t *testing.T) {
	t.Parallel()
	registration := Registration()
	assert.Equal(t, contracts.QwenModeDefault, registration.PermissionDefaults.Fallback)
	assert.Equal(t, contracts.QwenModeDefault, registration.PermissionDefaults.NewSession[agent.OptionIDPermissionMode],
		"Qwen's own default is auto, whose classifier approves without asking")
	assert.Equal(t, []string{contracts.QwenConfigReasoningEffort}, registration.AdditionalOptionIDs)
	assert.Equal(t, "LEAPMUX_QWEN_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Equal(t, "LEAPMUX_QWEN_DEFAULT_EFFORT", registration.EnvEffortKey)
	assert.Equal(t, contracts.QwenModeDefault, launchApprovalMode(""))
	assert.Equal(t, contracts.QwenModeYolo, launchApprovalMode(contracts.QwenModeYolo))
	assert.Equal(t, contracts.QwenModeDefault, launchApprovalMode("bypass"), "a mode Qwen does not know launches in the safe default")
}

func TestQwenDecorateModelReadsTheContextLimit(t *testing.T) {
	t.Parallel()
	model := &agent.ModelInfo{Id: "mock-model(openai)"}
	decorateModel(model, json.RawMessage(`{"contextLimit":200000}`))
	assert.Equal(t, int64(200000), model.ContextWindow)
	for _, meta := range []string{``, `null`, `{}`, `{"contextLimit":0}`, `{"contextLimit":-1}`, `{"contextLimit":"x"}`, `not json`} {
		model := &agent.ModelInfo{Id: "m", ContextWindow: 7}
		decorateModel(model, json.RawMessage(meta))
		assert.Equal(t, int64(7), model.ContextWindow, meta)
	}
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
