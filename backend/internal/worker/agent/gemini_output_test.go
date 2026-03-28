package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func newGeminiAgentWithSink(sink OutputSink) *GeminiCLIAgent {
	return &GeminiCLIAgent{
		processBase: processBase{
			agentID: "test-agent",
		},
		sink:      sink,
		sessionID: "test-session",
	}
}

func TestHandleGeminiOutput_AgentMessageChunk(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"sessionUpdate","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello Gemini"}}}}`
	handleGeminiCLIOutput(agent, []byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.Method != "agent_message_chunk" {
		t.Fatalf("expected method agent_message_chunk, got %q", got.Method)
	}
	if string(got.Content) != "Hello Gemini" {
		t.Fatalf("expected content 'Hello Gemini', got %q", string(got.Content))
	}
}

func TestHandleGeminiOutput_ToolCallOpensSpan(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"sessionUpdate","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"shell","kind":"execute","status":"pending"}}}`
	handleGeminiCLIOutput(agent, []byte(input))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	msg := sink.Messages()[0]
	if msg.SpanID != "tc-1" {
		t.Fatalf("expected spanID tc-1, got %q", msg.SpanID)
	}
	if msg.SpanType != "execute" {
		t.Fatalf("expected spanType execute, got %q", msg.SpanType)
	}
	if len(sink.OpenSpans()) != 1 {
		t.Fatalf("expected span to be opened")
	}
}

func TestHandleGeminiOutput_RequestPermission(t *testing.T) {
	sink := &controlTestSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"requestPermission","params":{"sessionId":"s1","options":[{"optionId":"proceed_once","name":"Allow","kind":"allow_once"}],"toolCall":{"toolCallId":"tc-1","title":"shell","kind":"execute"}}}`
	handleGeminiCLIOutput(agent, []byte(input))

	if sink.PersistedControlCount() != 1 {
		t.Fatalf("expected 1 persisted control request, got %d", sink.PersistedControlCount())
	}
	if got := sink.LastPersistedControl().RequestID; got != "7" {
		t.Fatalf("expected control request id 7, got %q", got)
	}
}

func TestHandleGeminiOutput_LegacyNotificationNames(t *testing.T) {
	sink := &controlTestSink{}
	agent := newGeminiAgentWithSink(sink)

	update := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello legacy Gemini"}}}}`
	handleGeminiCLIOutput(agent, []byte(update))

	request := `{"jsonrpc":"2.0","id":8,"method":"session/request_permission","params":{"sessionId":"s1","options":[{"optionId":"approve","name":"Approve","kind":"allow_once"}]}}`
	handleGeminiCLIOutput(agent, []byte(request))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	if got := sink.LastPersistedControl().RequestID; got != "8" {
		t.Fatalf("expected control request id 8, got %q", got)
	}
}

func TestHandleGeminiOutput_UsageUpdateBroadcastsSessionInfo(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"sessionUpdate","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":321,"size":12345,"cost":{"amount":0.25,"currency":"USD"}}}}`
	handleGeminiCLIOutput(agent, []byte(input))

	if sink.SessionInfoCount() != 1 {
		t.Fatalf("expected 1 session info update, got %d", sink.SessionInfoCount())
	}
	info := sink.LastSessionInfo()
	usage, ok := info["contextUsage"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected contextUsage map, got %#v", info["contextUsage"])
	}
	if usage["inputTokens"] != int64(321) {
		t.Fatalf("expected inputTokens 321, got %#v", usage["inputTokens"])
	}
	if usage["contextWindow"] != int64(12345) {
		t.Fatalf("expected contextWindow 12345, got %#v", usage["contextWindow"])
	}
	if info["totalCostUsd"] != 0.25 {
		t.Fatalf("expected totalCostUsd 0.25, got %#v", info["totalCostUsd"])
	}
}

func TestHandleGeminiOutput_CurrentModeUpdateBroadcastsPermissionMode(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}}`
	handleGeminiCLIOutput(agent, []byte(input))

	if agent.permissionMode != "plan" {
		t.Fatalf("expected permission mode plan, got %q", agent.permissionMode)
	}
	if got := sink.PermissionMode(); got != "plan" {
		t.Fatalf("expected sink permission mode plan, got %q", got)
	}
}

func TestGeminiHandlePromptResponsePersistsTurn(t *testing.T) {
	sink := &testSink{}
	agent := newGeminiAgentWithSink(sink)

	agent.turnThinkingText.WriteString("thinking")
	agent.turnAssistantText.WriteString("answer")

	resp := json.RawMessage(`{"stopReason":"end_turn","_meta":{"quota":{"token_count":{"input_tokens":1,"output_tokens":2}}}}`)
	agent.handlePromptResponse(resp)

	if sink.MessageCount() != 3 {
		t.Fatalf("expected 3 persisted messages, got %d", sink.MessageCount())
	}
	if sink.Messages()[2].Role != leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT {
		t.Fatalf("expected final message to be a result")
	}
	if sink.SessionInfoCount() != 1 {
		t.Fatalf("expected 1 session info broadcast, got %d", sink.SessionInfoCount())
	}
}

func TestBuildGeminiCLIModels_withAuto(t *testing.T) {
	models := []geminiCLIModelInfo{
		{ModelID: "auto", Name: "Auto", Description: "Automatic"},
		{ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Description: "Detailed"},
	}
	result := buildGeminiCLIModels(models, "auto")
	if len(result) != 2 {
		t.Fatalf("expected 2 models, got %d", len(result))
	}
	if result[0].Id != "auto" || !result[0].IsDefault {
		t.Fatalf("expected auto to be default, got id=%q default=%v", result[0].Id, result[0].IsDefault)
	}
}

func TestBuildGeminiCLIModels_withoutAuto(t *testing.T) {
	models := []geminiCLIModelInfo{
		{ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Description: "Detailed"},
		{ModelID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Description: "Fast"},
	}
	result := buildGeminiCLIModels(models, "gemini-2.5-pro")
	if len(result) != 3 {
		t.Fatalf("expected 3 models (synthetic auto + 2), got %d", len(result))
	}
	if result[0].Id != "auto" || result[0].IsDefault {
		t.Fatalf("expected synthetic auto first and not default, got id=%q default=%v", result[0].Id, result[0].IsDefault)
	}
	if result[1].Id != "gemini-2.5-pro" || !result[1].IsDefault {
		t.Fatalf("expected gemini-2.5-pro to be default, got id=%q default=%v", result[1].Id, result[1].IsDefault)
	}
}

func TestBuildGeminiCLIModels_withoutAutoEmptyCurrentModel(t *testing.T) {
	models := []geminiCLIModelInfo{
		{ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
	}
	result := buildGeminiCLIModels(models, "")
	if len(result) != 2 {
		t.Fatalf("expected 2 models, got %d", len(result))
	}
	if result[0].Id != "auto" || !result[0].IsDefault {
		t.Fatalf("expected synthetic auto to be default when currentModelID is empty, got id=%q default=%v", result[0].Id, result[0].IsDefault)
	}
}

func TestGeminiCurrentSettingsIncludesPermissionMode(t *testing.T) {
	agent := &GeminiCLIAgent{
		model:          "auto",
		permissionMode: GeminiCLIModePlan,
	}

	settings := agent.CurrentSettings()
	if settings.GetModel() != "auto" {
		t.Fatalf("expected model auto, got %q", settings.GetModel())
	}
	if settings.GetPermissionMode() != GeminiCLIModePlan {
		t.Fatalf("expected permission mode %q, got %q", GeminiCLIModePlan, settings.GetPermissionMode())
	}
}
