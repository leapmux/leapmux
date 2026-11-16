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
	rig := newKimiOutputRig(t)
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
	opening := map[string]any{"op": "frame.upsert", "turnId": "t0", "stepId": "t0.1", "frame": map[string]any{"kind": "tool", "frameId": "output-call", "toolCallId": callID, "name": "Bash", "state": "running", "input": map[string]any{}}}
	frame, err := json.Marshal(opening)
	require.NoError(t, err)
	rig.transcriptOps(t, 1, string(frame))
	progress := opening["frame"].(map[string]any)
	progress["progress"] = map[string]any{"kind": "stdout", "text": complete}
	frame, err = json.Marshal(opening)
	require.NoError(t, err)
	rig.transcriptOps(t, 2, string(frame))
	progress["state"], progress["output"] = "done", pointer
	delete(progress, "progress")
	frame, err = json.Marshal(opening)
	require.NoError(t, err)
	rig.transcriptOps(t, 3, string(frame))
	messages := rig.sink.Messages()
	require.Len(t, messages, 2)
	assert.NotContains(t, string(messages[1].Content), hidden)
	assert.NotContains(t, string(messages[1].SupplementalContent), hidden, "external output text must not enter Worker storage")
	record, valid := readKimiTranscriptRecord(messages[1].Content)
	require.True(t, valid)
	var result kimiTranscriptToolFrame
	require.NoError(t, json.Unmarshal(record.operation.Frame, &result))
	assert.JSONEq(t, fmt.Sprintf("%q", pointer), string(result.Output))
}
