package qoder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQoderNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	workingDir := t.TempDir()
	path := filepath.Join(home, ".qoder", "tmp", qoderProjectSlug(workingDir), "tool-outputs", "session-session-1", "b8rwntbj1.output")
	complete := "native first42\n" + strings.Repeat("native middle77\n", 1000) + "native tail42"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts.HomeDir = home
	a.opts.WorkingDir = workingDir
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"tool_use","id":"native-full-output","name":"Bash","input":{"command":"native command"}}]}}`))
	excerpt := fmt.Sprintf("Output too large (%.1f KB). Full output saved to:\n%s\n\nPreview (first 2000 characters):\nnative first42\n...", float64(len(complete))/1024, path)
	raw, err := json.Marshal(map[string]any{
		"type": "user", "session_id": "session-1", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "native-full-output", "is_error": false, "content": excerpt}}},
		"tool_use_result": map[string]any{
			"kind": "completed", "stdout": "native first42", "stderr": "", "exitCode": 0, "signal": nil,
			"persistedOutput": map[string]any{"path": path, "originalSizeBytes": len(complete), "savedSizeBytes": len(complete), "truncated": false, "preview": "native first42"},
		},
	})
	require.NoError(t, err)
	a.HandleOutput(raw)
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","subtype":"success","result":"The native tool ended."}`))
	var resultFound bool
	for _, message := range sink.Messages() {
		if string(message.Content) != string(raw) {
			continue
		}
		resultFound = true
		assert.Equal(t, "native-full-output", message.SpanID)
		assert.True(t, message.Closing)
		assert.NotContains(t, string(message.Content), "native middle77")
		assert.NotContains(t, string(message.SupplementalContent), "native middle77", "external output text must not enter Worker storage")
		assert.Contains(t, string(message.Content), path)
	}
	require.True(t, resultFound, "the original native result must reach the transcript")
}
