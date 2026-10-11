package ohmypi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOhMyPiNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionFile := filepath.Join(t.TempDir(), "native-session.jsonl")
	r.agent.sessionFile = sessionFile
	outputFileDir := sessionFile[:len(sessionFile)-len(".jsonl")]
	require.NoError(t, os.Mkdir(outputFileDir, 0o700))
	path := filepath.Join(outputFileDir, "0.bash.log")
	full := "NATIVE_FIRST42\nNATIVE_MIDDLE77\nNATIVE_LAST42"
	require.NoError(t, os.WriteFile(path, []byte(full), 0o600))
	frame := map[string]any{
		"type": "tool_execution_end", "toolCallId": "native-full-output", "toolName": "bash", "isError": false,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "NATIVE_FIRST42\nNATIVE_LAST42\n[Read artifact://0 for full output]"}},
			"details": map[string]any{"wallTimeMs": 0, "meta": map[string]any{"truncation": map[string]any{"artifactId": "0", "totalBytes": len(full)}}},
		},
	}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	r.emit(string(raw))
	if transcript, ok := r.agent.sink.(interface{ WaitForSupplementsForTest() }); ok {
		transcript.WaitForSupplementsForTest()
	}
	messages := r.sink.Messages()
	require.Len(t, messages, 1)
	assert.JSONEq(t, string(raw), string(messages[0].Content))
	assert.Equal(t, "native-full-output", messages[0].SpanID)
	assert.True(t, messages[0].Closing)
	assert.NotContains(t, string(messages[0].Content), "NATIVE_MIDDLE77")
	assert.NotContains(t, string(messages[0].SupplementalContent), "NATIVE_MIDDLE77", "external output text must not enter Worker storage")
}
