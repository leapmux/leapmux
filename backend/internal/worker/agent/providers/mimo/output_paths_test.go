package mimo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiMoNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MIMOCODE_HOME", home)
	directory := filepath.Join(home, "data", "tool-output")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	path := filepath.Join(directory, "tool_g001a0fa3a22ea001QhKvisYhb")
	full := "NATIVE_FIRST42\nNATIVE_MIDDLE77\nNATIVE_LAST42"
	require.NoError(t, os.WriteFile(path, []byte(full), 0o600))
	a, sink, _ := newSinkTestAgent(t)
	a.HandleOutput(messageEvent(t, "msg_outputFile", roleAssistant, mainActorID, false))
	part := map[string]any{
		"id": "prt_outputFile", "sessionID": testSessionID, "messageID": "msg_outputFile", "type": "tool", "tool": "bash", "callID": "native-full-output",
		"state": map[string]any{
			"status": "completed", "input": map[string]any{"command": "node actual-native-script.js"},
			"output": "Native limited output", "metadata": map[string]any{"exit": 0, "truncated": true, "outputPath": path},
		},
	}
	raw := eventJSON(t, "message.part.updated", map[string]any{"sessionID": testSessionID, "part": part})
	a.HandleOutput(raw)
	if transcript, ok := a.sink.(interface{ WaitForSupplementsForTest() }); ok {
		transcript.WaitForSupplementsForTest()
	}
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.JSONEq(t, string(raw), string(messages[0].Content))
	assert.Equal(t, "prt_outputFile", messages[0].SpanID)
	assert.True(t, messages[0].Closing)
	assert.NotContains(t, string(messages[0].Content), "NATIVE_MIDDLE77")
	assert.NotContains(t, string(messages[0].SupplementalContent), "NATIVE_MIDDLE77", "external output text must not enter Worker storage")
}
