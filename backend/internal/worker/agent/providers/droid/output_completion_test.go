package droid

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestTurnEndPreservesUnfinishedGeneration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		payload    string
		completion agent.MessageCompletion
	}{
		{"cancelled", `{"type":"agent_turn_completed","reason":"cancelled"}`, agent.MessageCompletionInterrupted},
		{"failed", `{"type":"agent_turn_completed","reason":"error"}`, agent.MessageCompletionError},
		{"completed", `{"type":"agent_turn_completed","reason":"completed"}`, agent.MessageCompletionComplete},
		{"error", `{"type":"error","message":"model connection failed"}`, agent.MessageCompletionError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			for _, kind := range []string{"thinking_text_delta", "assistant_text_delta"} {
				payload, err := json.Marshal(map[string]any{"type": kind, "messageId": "partial", "blockIndex": 0, "textDelta": kind})
				require.NoError(t, err)
				a.HandleOutput(droidAuditNotification(t, "main-session", payload))
			}
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(tc.payload)))
			rows := sink.Messages()
			require.GreaterOrEqual(t, len(rows), 2)
			for i, wantKind := range []agent.AssembledMessageKind{agent.AssembledMessageKindReasoning, agent.AssembledMessageKindText} {
				kind, text, completion, ok := agenttest.DecodeAssembledMessage(rows[i].Content)
				require.True(t, ok, "generation precedes the turn end")
				assert.Equal(t, wantKind, kind)
				assert.NotEmpty(t, text)
				assert.Equal(t, tc.completion, completion)
			}
			before := len(rows)
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"assistant_text_complete","messageId":"partial","blockIndex":0}`)))
			assert.Len(t, sink.Messages(), before, "late completion cannot duplicate the saved text")
		})
	}
}

func TestCompletedTextRetainsBufferAfterPersistenceFailure(t *testing.T) {
	t.Parallel()
	for _, end := range []string{
		`{"type":"assistant_text_complete","messageId":"partial","blockIndex":0}`,
		`{"type":"agent_turn_completed","reason":"cancelled"}`,
	} {
		t.Run(end, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"assistant_text_delta","messageId":"partial","textDelta":"retained"}`)))
			sink.PersistErr = errors.New("write refused")
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(end)))
			state := a.outputStates["main-session"]
			_, text, present := state.generation.ScopeSnapshot("droid-msg-partial")
			assert.True(t, present)
			assert.Equal(t, "retained", text)
			sink.PersistErr = nil
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(end)))
			_, _, present = state.generation.ScopeSnapshot("droid-msg-partial")
			assert.False(t, present)
			rows := sink.Messages()
			_, text, _, ok := agenttest.DecodeAssembledMessage(rows[1].Content)
			require.True(t, ok)
			assert.Equal(t, "retained", text)
		})
	}
}
