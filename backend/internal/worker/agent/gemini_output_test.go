package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

func newGeminiAgentWithSink(sink OutputSink) *GeminiCLIAgent {
	a := &GeminiCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:      "test-agent",
				providerName: "gemini",
			}},
			sink:      sink,
			sessionID: "test-session",
		},
	}
	a.extraSessionUpdate = a.handleExtraSessionUpdate
	return a
}

func TestHandleGeminiOutput_AgentMessageChunk(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello Gemini"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "agent_message_chunk", got.Method)
	require.Equal(t, "Hello Gemini", string(got.Content))
}

func TestHandleGeminiOutput_ToolCallOpensSpan(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"shell","kind":"execute","status":"pending"}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.Equal(t, "tc-1", msg.SpanID)
	require.Equal(t, "execute", msg.SpanType)
	require.Len(t, sink.OpenSpans(), 1)
}

func TestHandleGeminiOutput_RequestPermission(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"s1","options":[{"optionId":"proceed_once","name":"Allow","kind":"allow_once"}],"toolCall":{"toolCallId":"tc-1","title":"shell","kind":"execute"}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PersistedControlCount())
	require.Equal(t, "7", sink.LastPersistedControl().RequestID)
}

func TestHandleGeminiOutput_UsageUpdateBroadcastsSessionInfo(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":321,"size":12345,"cost":{"amount":0.25,"currency":"USD"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, int64(321), usage["input_tokens"])
	require.Equal(t, int64(12345), usage["context_window"])
	require.Equal(t, 0.25, info["total_cost_usd"])
}

func TestHandleGeminiOutput_CurrentModeUpdateBroadcastsPermissionMode(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, "plan", agent.permissionMode)
	require.Equal(t, "plan", sink.PermissionMode())
}

func TestGeminiHandlePromptResponsePersistsTurn(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	agent.turnThinkingText.WriteString("thinking")
	agent.turnAssistantText.WriteString("answer")

	resp := json.RawMessage(`{"stopReason":"end_turn","_meta":{"quota":{"token_count":{"input_tokens":1,"output_tokens":2}}}}`)
	agent.handleACPPromptResponse(resp, func(r json.RawMessage) {
		broadcastGeminiQuotaSessionInfo(agent.sink, r)
	})

	require.Equal(t, 3, sink.MessageCount())
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_TURN_END, sink.Messages()[2].Role)
	require.Equal(t, 1, sink.SessionInfoCount())
}

func TestBuildGeminiCLIModels_withAuto(t *testing.T) {
	models := []acpModelInfo{
		{ModelID: "auto", Name: "Auto", Description: "Automatic"},
		{ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Description: "Detailed"},
	}
	result := buildGeminiCLIModels(models, "auto")
	require.Len(t, result, 2)
	require.Equal(t, "auto", result[0].Id)
	require.True(t, result[0].IsDefault)
}

func TestBuildGeminiCLIModels_withoutAuto(t *testing.T) {
	models := []acpModelInfo{
		{ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Description: "Detailed"},
		{ModelID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Description: "Fast"},
	}
	result := buildGeminiCLIModels(models, "gemini-2.5-pro")
	require.Len(t, result, 3)
	require.Equal(t, "auto", result[0].Id)
	require.False(t, result[0].IsDefault)
	require.Equal(t, "gemini-2.5-pro", result[1].Id)
	require.True(t, result[1].IsDefault)
}

func TestBuildGeminiCLIModels_withoutAutoEmptyCurrentModel(t *testing.T) {
	models := []acpModelInfo{
		{ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
	}
	result := buildGeminiCLIModels(models, "")
	require.Len(t, result, 2)
	require.Equal(t, "auto", result[0].Id)
	require.True(t, result[0].IsDefault)
}

func TestGeminiCurrentSettingsIncludesPermissionMode(t *testing.T) {
	agent := &GeminiCLIAgent{
		acpBase: acpBase{model: "auto", permissionMode: GeminiCLIModePlan},
	}

	settings := agent.CurrentSettings()
	require.Equal(t, "auto", settings.GetModel())
	require.Equal(t, GeminiCLIModePlan, settings.GetPermissionMode())
}
