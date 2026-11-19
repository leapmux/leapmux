package kimi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	root := t.TempDir()
	t.Setenv(kimiHomeEnv, root)
	rig := newKimiConversationRig(t)
	const callID = "native-path-call"
	const hidden = "NATIVE_EXTERNAL_TEXT_MUST_NOT_ENTER_WORKER_7841"
	complete := strings.Repeat(hidden+"\n", 2000)
	directory := filepath.Join(root, "sessions", kimiWorkDirKey(rig.agent.workingDir), "session_1", "agents", "main", "tasks", "bash-n4hig35g")
	path := filepath.Join(directory, "output.log")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	task, err := json.Marshal(map[string]any{"kind": "process", "taskId": "bash-n4hig35g", "parentToolCallId": callID, "command": "native-probe", "description": "Read the native output.", "pid": 42, "status": "completed", "exitCode": 0, "startedAt": 1, "endedAt": 2})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(directory), "bash-n4hig35g.json"), task, 0o600))
	require.Greater(t, len(utf16.Encode([]rune(complete))), 50_000)
	pointer := fmt.Sprintf("Tool output exceeded 50000 characters; the full output was saved to a file.\ntool_name: Bash\ntool_call_id: %s\noutput_size_chars: %d\noutput_size_bytes: %d\noutput_path: %s\nnext_step: Use Read with output_path to page through the saved output, or Grep to search it.\n\n[preview: chars [0, 10)]\noutput", callID, len(utf16.Encode([]rune(complete))), len(complete), path)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	item := map[string]any{"type": "tool_call", "item_id": callID, "name": "Bash", "status": "running", "stage": "executing",
		"created_at": "2026-11-18T00:00:00Z", "started_at": "2026-11-18T00:00:00Z", "input": map[string]any{}}
	wire, err := json.Marshal(item)
	require.NoError(t, err)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":`+string(wire)+`}`)
	delta, err := json.Marshal(complete)
	require.NoError(t, err)
	rig.conversationFrame(t, fmt.Sprintf(`{"type":"item.delta","turn_id":"t0","item_id":%q,"field":"output","delta":%s}`, callID, delta))
	item["status"], item["outcome"], item["output"] = "ended", "completed", pointer
	delete(item, "stage")
	wire, err = json.Marshal(item)
	require.NoError(t, err)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+string(wire)+`}`)
	messages := rig.sink.Messages()
	require.Len(t, messages, 2)
	assert.NotContains(t, string(messages[1].Content), hidden)
	assert.NotContains(t, string(messages[1].SupplementalContent), hidden, "external output text must not enter Worker storage")
	record, err := decodeKimiConversationRecord(messages[1].Content)
	require.NoError(t, err)
	require.NotNil(t, record.item)
	assert.JSONEq(t, fmt.Sprintf("%q", pointer), string(record.item.Output))
}
