package grok

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

func TestGrokNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	workingDir := t.TempDir()
	callID := "native-full-output"
	path := filepath.Join(home, ".grok", "sessions", grokEncodeCwd(workingDir), grokTestSession, "terminal", callID+".log")
	complete := "native first42\n" + strings.Repeat("native middle77\n", 1000) + "native tail42"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	a, sink, _ := newGrokAgent(t, agent.Options{HomeDir: home, WorkingDir: workingDir}, nil)
	a.HandleOutput(sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": callID, "kind": "execute", "title": "run_terminal_command",
		"rawInput": map[string]any{"command": "native command"},
		"_meta":    map[string]any{"x.ai/tool": map[string]any{"version": 1, "name": "run_terminal_command"}},
	}))
	excerpt := "native first42\n... output omitted ...\nnative tail42"
	update := map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": "completed", "kind": "execute",
		"rawOutput": map[string]any{"type": "Bash", "output": []int{110, 97, 116, 105, 118, 101}, "exit_code": 0, "truncated": true, "output_file": path, "total_bytes": len(complete), "current_dir": workingDir},
		"content":   []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": excerpt}}},
	}
	a.HandleOutput(sessionUpdate(t, grokTestSession, update))
	a.HandleOutput(turnCompleted(t, "native-full-output-turn"))
	var resultFound bool
	for _, message := range sink.Messages() {
		if message.SpanID != callID || !message.Closing {
			continue
		}
		resultFound = true
		expected, err := json.Marshal(update)
		require.NoError(t, err)
		assert.JSONEq(t, string(expected), string(message.Content))
		assert.NotContains(t, string(message.Content), "native middle77")
		assert.NotContains(t, string(message.SupplementalContent), "native middle77", "external output text must not enter Worker storage")
		assert.Contains(t, string(message.Content), path)
	}
	require.True(t, resultFound, "the actual native result must reach its original tool span")
}
