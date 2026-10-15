package qoder

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestNativeAbortedAssistantRetainsCompletion(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"text", "thinking", "tool_use"} {
		for _, flag := range []any{true, false, nil, "true", 1} {
			name, err := json.Marshal(flag)
			require.NoError(t, err)
			t.Run(kind+"/"+string(name), func(t *testing.T) {
				t.Parallel()
				a, sink, _ := newGoalAgent(t)
				frame := map[string]any{
					"type": "assistant", "aborted": flag, "session_id": "session-1",
					"message": map[string]any{"content": []any{map[string]any{
						"type": kind, kind: "partial native output", "id": "call", "name": "Bash", "input": map[string]any{"command": "pwd"},
					}}},
				}
				raw, err := json.Marshal(frame)
				require.NoError(t, err)
				a.HandleOutput(raw)
				rows := sink.Messages()
				require.Len(t, rows, 1)
				assert.Equal(t, raw, rows[0].Content)
				if flag == true {
					assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
				} else {
					assert.Empty(t, rows[0].Completion)
				}
			})
		}
	}
}
