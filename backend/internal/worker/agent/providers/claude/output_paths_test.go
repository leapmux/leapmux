package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	t.Parallel()
	home, working := t.TempDir(), t.TempDir()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.homeDir, a.workingDir = home, working
	a.sessionID = "session-1"
	path := filepath.Join(home, ".claude", "projects", mangleClaudePath(working), "session-1", "tool-results", "b3rkvpi3o.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	complete := "first\nactual omitted middle77\nlast42"
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"tool_use","id":"full-output-call","name":"Bash","input":{"command":"native command"}}]}}`))
	preview := fmt.Sprintf("<persisted-output>\nOutput too large (200KB). Full output saved to: %s\n\nPreview (first 2KB):\nfirst\n...\n</persisted-output>", path)
	raw, err := json.Marshal(map[string]any{
		"type": "user", "session_id": "session-1", "parent_tool_use_id": nil,
		"message":         map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": "full-output-call", "content": preview}}},
		"tool_use_result": map[string]any{"stdout": "first", "stderr": "", "interrupted": false, "isImage": false, "persistedOutputPath": path, "persistedOutputSize": len([]byte(complete))},
	})
	require.NoError(t, err)
	a.HandleOutput(raw)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, string(raw), string(rows[1].Content))
	assert.NotContains(t, string(rows[1].Content), "actual omitted middle77")
	assert.NotContains(t, string(rows[1].SupplementalContent), "actual omitted middle77", "native output paths must not cause full file text to enter Worker storage")
	assert.Contains(t, string(rows[1].Content), path)
}
