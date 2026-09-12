package service

import (
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeRejectionQueuesFeedbackWithoutApprovingTheRequest(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"ExitPlanMode", "AskUserQuestion"} {
		for _, feedback := range []string{"approve", " \n\uFEFFapprove\t", "  Keep this feedback.\n"} {
			t.Run(tool+feedback, func(t *testing.T) {
				t.Parallel()
				svc, _, _ := setupTestService(t)
				require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
					ID: "agent-1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
					WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
				}))
				require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "session-1"}))
				_, err := svc.InputQueue.SetPaused(t.Context(), "agent-1", true)
				require.NoError(t, err)
				payload, err := json.Marshal(map[string]any{
					"id": 7, "method": "interaction/requestUserInput", "request_id": "request-1",
					"request": map[string]any{"tool_name": tool, "tool_use_id": "tool-1", "input": map[string]any{"questions": []any{map[string]string{"question": "Review"}}}},
					"params":  map[string]any{"requestId": "request-1", "sessionId": "session-1", "toolName": tool},
				})
				require.NoError(t, err)
				createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
					AgentID: "agent-1", AgentSessionID: "session-1", RequestID: "request-1", ClaimToken: "claim-1", Payload: payload,
				})
				row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
				require.NoError(t, err)
				var sent [][]byte
				svc.sendControlResponseFn = func(_ string, content []byte) error {
					sent = append(sent, append([]byte(nil), content...))
					return nil
				}
				response, err := json.Marshal(map[string]any{"response": map[string]any{
					"request_id": "request-1", "response": map[string]string{"behavior": "deny", "message": feedback},
				}})
				require.NoError(t, err)
				failedTable := "messages"
				if tool == "AskUserQuestion" {
					failedTable = "agent_input_queue_items"
				}
				_, err = svc.DB.ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER fail_feedback_commit BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'feedback storage failed'); END", failedTable))
				require.NoError(t, err)
				require.ErrorContains(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim-1"}), "feedback storage failed")
				pending, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request-1"})
				require.NoError(t, err)
				assert.Equal(t, "claim-1", pending.ClaimToken)
				failedQueue, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				assert.Empty(t, failedQueue.Items)
				messages, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "agent-1"})
				require.NoError(t, err)
				assert.Empty(t, messages)
				_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER fail_feedback_commit")
				require.NoError(t, err)
				require.NoError(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim-1"}))
				require.Len(t, sent, 1)
				var reply struct {
					Result struct {
						Action string `json:"action"`
					} `json:"result"`
				}
				require.NoError(t, json.Unmarshal(sent[0], &reply))
				assert.Equal(t, "decline", reply.Result.Action, "rejection must not depend on feedback text")
				queue, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				require.Len(t, queue.Items, 1, "the native mapper cannot carry rejection feedback")
				assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, queue.Items[0].Kind)
				assert.Equal(t, feedback, queue.Items[0].Text)
				require.NoError(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim-1"}))
				assert.Len(t, sent, 1)
				again, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				assert.Equal(t, queue, again)
			})
		}
	}
}

func TestControlFeedbackCannotReachAReplacementSession(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	startEchoAgent(t, svc, "agent-1")
	require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "new-session"}))
	_, err := svc.DB.ExecContext(t.Context(), `INSERT INTO control_response_answers
		(agent_id, request_id, claim_token, state, agent_session_id, input_id, feedback)
		VALUES ('agent-1', 'request', 'claim', ?, 'old-session', 'feedback-input', 'feedback')`,
		int64(leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED))
	require.NoError(t, err)
	item := inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ID: "feedback-input", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: "feedback",
	}}
	adapter := &agentInputQueueAdapter{svc: svc}
	_, err = adapter.Dispatch(item)
	require.ErrorContains(t, err, "original provider session")
	_, err = adapter.Steer(item)
	require.ErrorContains(t, err, "original provider session")
}
