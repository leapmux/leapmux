package commandcode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandCodeNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	a, sink := testAgent(t)
	root := t.TempDir()
	a.runtimeDir = root
	path := filepath.Join(root, "commandcode", "shellout", "2026-10-02T15-43-58-781Z-fg-native.log")
	const hidden = "NATIVE_EXTERNAL_TEXT_MUST_NOT_ENTER_WORKER_7841"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(hidden), 0o600))
	feedEvent(t, a, map[string]any{"type": "tool_queued", "toolCallId": "native-path-call", "toolName": "shell_command", "input": map[string]string{"command": "native command"}})
	feedEvent(t, a, map[string]any{"type": "tool_update", "toolCallId": "native-path-call", "toolName": "shell_command", "partial": []map[string]string{{"type": "text", "text": hidden}}})
	preview := fmt.Sprintf("native first\n\n[... output truncated: %d chars total, showing first 13 and last part ...]\n[full output saved to: %s — read it with read_file (offset/limit) or grep]\nnative last", len(utf16.Encode([]rune(hidden))), path)
	raw := feedEvent(t, a, map[string]any{"type": "tool_completed", "toolCallId": "native-path-call", "toolName": "shell_command", "result": []map[string]string{{"type": "text", "text": preview}}})
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, raw, messages[1].Content)
	assert.NotContains(t, string(messages[1].Content), hidden)
	assert.NotContains(t, string(messages[1].SupplementalContent), hidden, "external output text must not enter Worker storage")
	var original map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(messages[1].Content, &original))
	assert.Contains(t, string(messages[1].Content), path)
}
