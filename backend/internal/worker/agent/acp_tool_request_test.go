package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPToolRequestReceivesLateInputBeforeCompletion(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.handleToolCall(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"command","title":"bash","kind":"execute","status":"pending","rawInput":{}}`))
	agent.handleToolCallUpdate(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"command","title":"Run checks","rawInput":{"command":"npm test"},"status":"in_progress"}`))
	require.Len(t, sink.Messages(), 1, "the input update must preserve the request row")
	assert.JSONEq(t, `{"sessionUpdate":"tool_call","toolCallId":"command","title":"bash","kind":"execute","status":"pending","rawInput":{}}`, string(sink.Messages()[0].Content))
	assert.JSONEq(t, `{"sessionUpdate":"tool_call","toolCallId":"command","title":"Run checks","status":"pending","rawInput":{"command":"npm test"}}`, string(sink.Messages()[0].SupplementalContent))
	assert.False(t, sink.Messages()[0].Closing)

	originalResult := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"command","status":"completed","content":[{"type":"content","content":{"type":"text","text":"passed"}}]}`)
	agent.handleToolCallUpdate(originalResult)
	require.Len(t, sink.Messages(), 2)
	assert.Equal(t, []byte(originalResult), sink.Messages()[1].Content)
	assert.Contains(t, string(sink.Messages()[1].SupplementalContent), `"command":"npm test"`)
	assert.True(t, sink.Messages()[1].Closing)
}

func TestACPToolResultPreservesOriginalTerminalReference(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.handleToolCall(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"command","kind":"execute","rawInput":{"command":"printf output"}}`))
	exitCode := 0
	agent.completedTerminals = map[string]acpTerminalResult{"terminal": {Output: "output", ExitCode: &exitCode}}
	original := json.RawMessage(`{ "sessionUpdate": "tool_call_update", "toolCallId": "command", "status": "completed", "content": [{"type":"terminal","terminalId":"terminal"}], "rawOutput":{"providerField":9007199254740993} }`)
	agent.handleToolCallUpdate(original)
	require.Len(t, sink.Messages(), 2)
	message := sink.Messages()[1]
	assert.Equal(t, []byte(original), message.Content)
	assert.Contains(t, string(message.SupplementalContent), "output")
}
