package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "pi-mcp-0123456789abcdef.txt")
	complete := "First line\nComplete output: 끝"
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	raw, err := json.Marshal(map[string]any{
		"type": "tool_execution_end", "toolCallId": "call", "toolName": "mcp__probe__read", "isError": false,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "First line\n[truncated]"}, map[string]any{"type": "image", "data": "AAAA", "mimeType": "image/png"}},
			"details": map[string]any{"server": "probe", "tool": "read", "fullOutputPath": path},
		},
	})
	require.NoError(t, err)
	raw = append([]byte(" \t"), raw...)
	sink := &agenttest.Sink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.handlePiToolExecutionEnd(raw)
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, raw, messages[0].Content)
	assert.NotContains(t, string(messages[0].Content), "Complete output: 끝")
	assert.NotContains(t, string(messages[0].SupplementalContent), "Complete output: 끝", "external output text must not enter Worker storage")
}
