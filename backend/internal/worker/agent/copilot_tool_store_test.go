package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCopilotNativeTool(t *testing.T) {
	t.Parallel()
	request := `{"type":"tool.execution_start","agentId":"owner","data":{"toolCallId":"call","toolName":"task","parentToolCallId":"parent","arguments":{"prompt":"Read sample","counter":9007199254740993}}}`
	started := `{"type":"subagent.started","agentId":"child","data":{"toolCallId":"call"}}`
	finished := `{"type":"subagent.completed","agentId":"child","data":{"toolCallId":"call","totalTokens":0}}`
	result := `{"type":"tool.execution_complete","data":{"toolCallId":"call","success":true,"result":{"content":"Report"}}}`
	path := filepath.Join(t.TempDir(), "events.jsonl")
	// A later large event pushes the request outside the first tail window.
	padding, err := json.Marshal(map[string]any{"type": "assistant.message", "data": map[string]string{"content": strings.Repeat("x", 90*1024)}})
	require.NoError(t, err)
	content := strings.Join([]string{request, started, finished, result, string(padding), `{"type":`}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	record, err := readCopilotNativeTool(context.Background(), path, "call")
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, "task", record.ToolName)
	assert.Equal(t, "owner", record.AgentID)
	assert.Equal(t, "parent", record.ParentToolCallID)
	assert.Equal(t, request, string(record.Request))
	assert.Equal(t, result, string(record.Result))
	assert.Equal(t, started, string(record.Started))
	assert.Equal(t, finished, string(record.Finished))
	assert.Contains(t, string(record.Arguments), "9007199254740993")
	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(stored))
}

func TestReadCopilotNativeToolMissingAndInvalidSources(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "events.jsonl")
	record, err := readCopilotNativeTool(context.Background(), path, "call")
	require.NoError(t, err)
	assert.Nil(t, record)
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	record, err = readCopilotNativeTool(context.Background(), path, "call")
	require.NoError(t, err)
	assert.Nil(t, record)
	_, err = readCopilotNativeTool(context.Background(), directory, "call")
	assert.ErrorContains(t, err, "not a regular file")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = readCopilotNativeTool(ctx, path, "call")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestReadCopilotNativeToolDoesNotMatchAnotherCall(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"type\":\"tool.execution_start\",\"data\":{\"toolCallId\":\"other\",\"toolName\":\"task\"}}\n"), 0o600))
	record, err := readCopilotNativeTool(context.Background(), path, "call")
	require.NoError(t, err)
	assert.Nil(t, record)
}
