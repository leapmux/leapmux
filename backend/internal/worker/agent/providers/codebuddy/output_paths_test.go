package codebuddy

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

func TestCodebuddyNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	t.Parallel()
	home, working := t.TempDir(), t.TempDir()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.opts = agent.Options{HomeDir: home, WorkingDir: working}
	path := filepath.Join(home, ".codebuddy", "projects", codebuddyProjectSlug(working), "session-1", "tool-results", "full-output-call.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	text := "first\nactual omitted middle77\nlast42"
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"tool_use","id":"full-output-call","name":"Bash","input":{"command":"native command"}}]}}`))
	preview := fmt.Sprintf("<persisted-output>\nOutput too large (200KB). Full output saved to: %s\n\nPreview (first 2048 bytes):\nfirst\n...\n</persisted-output>", path)
	raw, err := json.Marshal(map[string]any{
		"type": "user", "session_id": "session-1", "parent_tool_use_id": nil,
		"message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": "full-output-call", "content": []map[string]string{{"type": "text", "text": preview}}}}},
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
