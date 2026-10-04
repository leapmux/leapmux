package kiro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiroNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	home := t.TempDir()
	workingDir := t.TempDir()
	directory := filepath.Join(home, ".kiro", "sessions", kiroWorkspaceKey(workingDir), kiroTestSession, "tool-outputs")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	path := filepath.Join(directory, "execute_bash-36bf2bff.txt")
	output := strings.Repeat("NATIVE_MIDDLE77\n", 2500) + "NATIVE_LAST42\n"
	full := "Output:\n" + output + "\n\nExit Code: 0"
	require.NoError(t, os.WriteFile(path, []byte(full), 0o600))
	a, sink, _ := newKiroAgent(t, agent.Options{HomeDir: home, WorkingDir: workingDir}, nil)
	a.SetSessionIDForTest(kiroTestSession)
	a.HandleOutput(sessionUpdate(t, kiroTestSession, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "run_command_native-full-output", "kind": "execute", "status": "pending", "rawInput": map[string]any{"command": "node actual-native-script.js"}, "_meta": map[string]any{"kiro": map[string]any{"toolOrigin": "default"}}}))
	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_native-full-output", output[:8192]))
	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_native-full-output", output[8192:]))
	update := map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "run_command_native-full-output", "status": "completed",
		"kind": "execute", "title": "Run the scripted command", "rawInput": map[string]any{"command": "node actual-native-script.js"},
		"rawOutput": map[string]any{"output": "Native limited output", "exitCode": 0, "message": "Native offloaded result"},
		"_meta":     map[string]any{"kiro": map[string]any{"toolOrigin": "default", "outputTransformation": map[string]any{"kind": "offloaded", "absFilePath": path, "totalChars": len(full)}}},
	}
	a.HandleOutput(sessionUpdate(t, kiroTestSession, update))
	messages := sink.Messages()
	require.Len(t, messages, 2)
	result := messages[1]
	encoded, err := json.Marshal(update)
	require.NoError(t, err)
	assert.JSONEq(t, string(encoded), string(result.Content))
	assert.Equal(t, "run_command_native-full-output", result.SpanID)
	assert.True(t, result.Closing)
	assert.NotContains(t, string(result.Content), "NATIVE_MIDDLE77")
	assert.NotContains(t, string(result.SupplementalContent), "NATIVE_MIDDLE77", "external output text must not enter Worker storage")
	assert.Contains(t, string(result.Content), path)
}
