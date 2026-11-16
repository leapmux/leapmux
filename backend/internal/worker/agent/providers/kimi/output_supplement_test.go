package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiRetainedToolOutputPreservesExplicitNativeValues(t *testing.T) {
	t.Parallel()
	for _, value := range []any{nil, "", false, 0, "native output"} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			t.Parallel()
			record, err := json.Marshal(map[string]any{"type": "transcript.ops", "session_id": "session_1", "payload": map[string]any{"type": "transcript.ops", "agent_id": "main", "seq": 1, "ops": []any{map[string]any{"op": "frame.upsert", "turnId": "t0", "stepId": "t0.1", "frame": map[string]any{"kind": "tool", "frameId": "frame", "toolCallId": "call", "name": "Bash", "state": "done", "output": value}}}}})
			require.NoError(t, err)
			content, native := kimiRetainedTranscriptToolContent(record, "collected tail", true)
			require.True(t, native)
			assert.Equal(t, record, content.Original)
			assert.Empty(t, content.Supplemental)
		})
	}
}
