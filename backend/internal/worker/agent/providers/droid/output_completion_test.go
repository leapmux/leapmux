package droid

import (
	"encoding/json"
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
		{"permission refused", `{"type":"agent_turn_completed","reason":"permission_rejected"}`, agent.MessageCompletionError},
		{"process exit", `{"type":"agent_turn_completed","reason":"process_exit"}`, agent.MessageCompletionInterrupted},
		{"handoff", `{"type":"agent_turn_completed","reason":"spec_handoff"}`, agent.MessageCompletionComplete},
		{"missing output", `{"type":"agent_turn_completed","reason":"structured_output_missing"}`, agent.MessageCompletionError},
		{"invalid output", `{"type":"agent_turn_completed","reason":"structured_output_invalid"}`, agent.MessageCompletionError},
		{"invalid schema", `{"type":"agent_turn_completed","reason":"structured_output_schema_invalid"}`, agent.MessageCompletionError},
		{"usage exhausted", `{"type":"agent_turn_completed","reason":"model_usage_exhausted"}`, agent.MessageCompletionError},
		{"authentication", `{"type":"agent_turn_completed","reason":"model_authentication_failed"}`, agent.MessageCompletionError},
		{"request rejected", `{"type":"agent_turn_completed","reason":"model_request_rejected"}`, agent.MessageCompletionError},
		{"unreachable", `{"type":"agent_turn_completed","reason":"model_provider_unreachable"}`, agent.MessageCompletionError},
		{"unavailable", `{"type":"agent_turn_completed","reason":"model_provider_unavailable"}`, agent.MessageCompletionError},
		{"rate limited", `{"type":"agent_turn_completed","reason":"model_rate_limited"}`, agent.MessageCompletionError},
		{"prompt rejected", `{"type":"agent_turn_completed","reason":"prompt_rejected"}`, agent.MessageCompletionError},
		{"persistence failed", `{"type":"agent_turn_completed","reason":"completion_persistence_failed"}`, agent.MessageCompletionError},
		{"no approver", `{"type":"agent_turn_completed","reason":"no_approver_available"}`, agent.MessageCompletionError},
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

func TestDroidFinishOutputAndChildCloseResetSpansAndProgress(t *testing.T) {
	t.Parallel()

	t.Run("finishOutput closes open root tools and resets spans and progress", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"tool_call","toolUse":{"id":"exit-1","name":"Execute","input":{"command":"sleep 10"}}}`)))
		a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"tool_progress_update","toolUseId":"exit-1","update":{"text":"partial"}}`)))

		a.finishOutput(agent.MessageCompletionError)

		rows := sink.Messages()
		require.Len(t, rows, 2)
		assert.Equal(t, "droid-tool-exit-1", rows[0].SpanID)
		assert.False(t, rows[0].Closing)
		assert.Equal(t, "droid-tool-exit-1", rows[1].SpanID)
		assert.True(t, rows[1].Closing)
		assert.Equal(t, agent.MessageCompletionError, rows[1].Completion)
		assert.Equal(t, 1, sink.ResetSpanCount())
		assert.Equal(t, agent.ProgressSnapshot{}, sink.ProgressSnapshot())
	})

	t.Run("closeChildSession closes remaining child open tools and resets child spans", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		a.HandleOutput([]byte(droidChildAvailable))
		task, found := sink.BackgroundTask("child-1")
		require.True(t, found)
		child := sink.Child(task.ChildAgentID)
		a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"tool_call","toolUse":{"id":"c-tool","name":"Read","input":{"file_path":"x.txt"}}}`)))
		a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"error","message":"child boom"}`)))

		assert.Contains(t, child.ClosedSpans(), "droid-tool-c-tool")
		assert.Equal(t, 1, child.ResetSpanCount())
		var closing []agenttest.Message
		for _, row := range child.Messages() {
			if row.SpanID == "droid-tool-c-tool" && row.Closing {
				closing = append(closing, row)
			}
		}
		require.Len(t, closing, 1)
		assert.Equal(t, agent.MessageCompletionError, closing[0].Completion)
	})
}
