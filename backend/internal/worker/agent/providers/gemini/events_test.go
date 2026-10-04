package gemini

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiModeTextCannotChangeNativePermissionState(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	agent := &Agent{}
	agent.SetPromptActiveForTest(true)
	agent.SetSinkForTest(services)
	agent.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	agent.SetPermissionModeForTest(contracts.GeminiModeDefault)
	agent.SetAvailableModesForTest(geminiModes())
	consumed := agent.handleSessionUpdate("native-session", services, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] yolo"}}`))
	assert.False(t, consumed, "native model text must remain conversation content")
	assert.Equal(t, contracts.GeminiModeDefault, agent.PermissionModeForTest())
	assert.Zero(t, sink.SessionInfoCount())
}

func TestGeminiRecoveredModeCannotReplaceALaterAcknowledgedSetter(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.SetPromptActiveForTest(true)
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.SetSessionIDForTest("root")
	a.SetAvailableModesForTest(geminiModes())
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	a.SetPermissionModeForTest(contracts.GeminiModePlan)
	a.HooksForTest().ModeSetter = func(mode string, acknowledged func(string)) error {
		require.True(t, a.observeModeSetterReply("root", mode, json.RawMessage(`{}`), nil, acknowledged))
		return nil
	}
	a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"exit_plan_mode__call","status":"in_progress"}`))
	a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"exit_plan_mode__call","status":"completed"}`))
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDPermissionMode: contracts.GeminiModeYolo})
	require.Equal(t, contracts.GeminiModeYolo, result.ConfirmedOptions()[agent.OptionIDPermissionMode])
	a.observeNativeMode("root", json.RawMessage(`{"id":"exit_plan_mode__call","name":"exit_plan_mode","status":"success","resultDisplay":"Plan approved: /native/plan.md","result":[{"functionResponse":{"response":{"output":"Plan approved. Switching to Default mode."}}}]}`))
	assert.Equal(t, contracts.GeminiModeYolo, a.PermissionModeForTest(), "the later native setter remains authoritative")
}

func TestGeminiModeChangeRequiresItsExactCompletedNativeTool(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	a := &Agent{}
	a.SetPromptActiveForTest(true)
	a.SetSinkForTest(services)
	a.SetSessionIDForTest("session-root")
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	a.SetPermissionModeForTest(contracts.GeminiModeDefault)
	a.SetAvailableModesForTest(geminiModes())
	const start = `{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__model-call","status":"in_progress"}`
	const success = `{"id":"enter_plan_mode__model-call","name":"enter_plan_mode","status":"success","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`
	assert.False(t, a.handleSessionUpdate("session-root", services, json.RawMessage(start)))
	assert.True(t, a.handleSessionUpdate("session-root", services, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] plan"}}`)))
	assert.Equal(t, contracts.GeminiModeDefault, a.PermissionModeForTest(), "text alone cannot change the native mode")
	a.observeNativeMode("session-foreign", json.RawMessage(success))
	assert.Equal(t, contracts.GeminiModeDefault, a.PermissionModeForTest())
	a.observeNativeMode("session-root", json.RawMessage(success))
	assert.Equal(t, contracts.GeminiModePlan, a.PermissionModeForTest())
	count := sink.SessionInfoCount()
	a.observeNativeMode("session-root", json.RawMessage(success))
	assert.Equal(t, count, sink.SessionInfoCount(), "a duplicate native record cannot replay the setting change")
}

func TestGeminiRejectsFailedUntrackedAndMalformedModeRecords(t *testing.T) {
	t.Parallel()
	for _, record := range []string{
		`null`, `{}`, `{`,
		`{"id":"enter_plan_mode__another-call","name":"enter_plan_mode","status":"success","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`,
		`{"id":"enter_plan_mode__call","name":"enter_plan_mode","status":"error","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`,
		`{"id":"enter_plan_mode__call","name":"enter_plan_mode","status":"success","result":[{"functionResponse":{"response":{"output":"User cancelled entering Plan Mode."}}}]}`,
		`{"id":"enter_plan_mode__call","name":"other","status":"success","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`,
	} {
		a := &Agent{}
		a.SetPromptActiveForTest(true)
		a.SetSessionIDForTest("root")
		a.SetPermissionModeForTest(contracts.GeminiModeDefault)
		a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__call"}`))
		a.observeNativeMode("root", json.RawMessage(record))
		assert.Equal(t, contracts.GeminiModeDefault, a.PermissionModeForTest(), record)
	}
}

func TestGeminiContextResetRemovesPendingModeCalls(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.SetPromptActiveForTest(true)
	a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__call"}`))
	a.resetNativeModes()
	assert.False(t, a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] plan"}}`)))
}
