package service

import (
	"errors"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/require"
)

func TestControlResponseDoesNotDeliverAnUnmatchedAnswer(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	sends := 0
	svc.sendControlResponseFn = func(string, []byte) error { sends++; return nil }
	err = svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: []byte(`{"id":"request","result":{"decision":"accept"}}`), ClaimToken: "old-claim"})
	require.Error(t, err)
	require.Zero(t, sends)
}

func TestControlResponseRefusesAReplacedProviderSession(t *testing.T) {
	for _, staleSnapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(staleSnapshot), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "old-session"}))
			oldAgent, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", AgentSessionID: "old-session", RequestID: "request", ClaimToken: "claim",
				Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval"}`),
			})
			require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: "agent-1", AgentSessionID: "new-session"}))
			currentAgent, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			if staleSnapshot {
				currentAgent = oldAgent
			}
			sends := 0
			svc.sendControlResponseFn = func(string, []byte) error { sends++; return nil }
			err = svc.processControlResponse(currentAgent, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: []byte(`{"id":"request","result":{"decision":"accept"}}`), ClaimToken: "claim"})
			require.ErrorContains(t, err, "session")
			require.Zero(t, sends, "an earlier session cannot approve a request in its replacement")
		})
	}
}

func TestClearContextDoesNotSuppressOtherControlResponses(t *testing.T) {
	for _, tool := range []string{"Bash", "EnterPlanMode", "UnknownTool"} {
		t.Run(tool, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			}))
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
				Payload: []byte(fmt.Sprintf(`{"type":"control_request","request_id":"request","request":{"tool_name":%q}}`, tool)),
			})
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			var sent [][]byte
			svc.sendControlResponseFn = func(_ string, content []byte) error {
				sent = append(sent, append([]byte(nil), content...))
				return nil
			}
			response := []byte(`{"type":"control_response","clearContext":true,"response":{"subtype":"success","request_id":"request","response":{"behavior":"allow"}}}`)
			require.NoError(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim"}))
			require.Len(t, sent, 1, "only a plan exit can replace native delivery with context clearing")
			require.JSONEq(t, string(response), string(sent[0]))
			require.NoError(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim"}))
			require.Len(t, sent, 1, "a completed response must not send again")
		})
	}
}

type controlResponseEncoderForTest struct {
	agent.Provider
	content []byte
}

func (encoder *controlResponseEncoderForTest) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := encoder.Provider.ResolveControlResponse(ctx)
	result.Content = append([]byte(nil), encoder.content...)
	return result
}

func TestControlResponseRecoveryKeepsTheBytesThatWereDelivered(t *testing.T) {
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	encoder := &controlResponseEncoderForTest{
		Provider: testRegistry.Plugin(provider),
		content:  []byte(` {"id":"request","result":{"decision":"accept","encoder":"first"}} `),
	}
	svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, provider, encoder)))
	createClaimTestAgent(t, svc, "agent-1")
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
		Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval"}`),
	})
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_control_encoding BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'answer storage failed'); END`)
	require.NoError(t, err)
	var sent [][]byte
	svc.sendControlResponseFn = func(_ string, content []byte) error {
		sent = append(sent, append([]byte(nil), content...))
		return nil
	}
	response := []byte(`{"id":"request","result":{"decision":"accept"}}`)
	require.ErrorContains(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim"}), "answer storage failed")
	require.Len(t, sent, 1)
	encoder.content = []byte(`{"id":"request","result":{"decision":"accept","encoder":"changed"}}`)
	_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER fail_control_encoding")
	require.NoError(t, err)
	require.NoError(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim"}))
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "agent-1"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	stored := decodeStructuredControlResponse(t, rows[0])
	require.Equal(t, string(sent[0]), string(stored.Response))
	require.Len(t, sent, 1)
}

func TestLeapMuxPlanApprovalKeepsTheRequestWhenRecordingFails(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "plan", ClaimToken: "claim",
		Payload: []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_answer BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'answer storage failed'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
	require.NoError(t, err)
	svc.sendControlResponseFn = func(string, []byte) error {
		t.Fatal("LeapMux plan approval must not send a native control response")
		return nil
	}
	response := []byte(`{"response":{"request_id":"plan","response":{"behavior":"deny"}}}`)
	require.ErrorContains(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim"}), "answer storage failed")
	request, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "plan"})
	require.NoError(t, err)
	require.Equal(t, "claim", request.ClaimToken)
	_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER fail_control_answer")
	require.NoError(t, err)
	require.NoError(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim"}))
}

func TestLeapMuxPlanApprovalRejectsMissingDecisions(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "plan", ClaimToken: "claim",
		Payload: []byte(`{"request":{"tool_name":"CodexPlanModePrompt"}}`),
	})
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	sends := 0
	svc.sendControlResponseFn = func(string, []byte) error { sends++; return nil }
	err = svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: []byte(`{"response":{"request_id":"plan"}}`), ClaimToken: "claim"})
	require.Error(t, err)
	require.Zero(t, sends)
}

func TestControlResponseRecoversRecordingWithoutRepeatingDelivery(t *testing.T) {
	for _, replaceRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "same request", true: "reissued request"}[replaceRequest], func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
				Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`),
			})
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			_, err = svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_answer BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'answer storage failed'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
			require.NoError(t, err)
			sends := 0
			send := func(string, []byte) error { sends++; return nil }
			svc.sendControlResponseFn = send
			response := []byte(`{"id":"request","result":{"decision":"accept","count":0,"enabled":false,"text":""}}`)
			err = svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim"})
			require.ErrorContains(t, err, "answer storage failed")
			require.Equal(t, 1, sends)
			request, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
			require.NoError(t, err)
			require.Equal(t, "claim", request.ClaimToken)
			_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER fail_control_answer")
			require.NoError(t, err)

			if replaceRequest {
				createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
					AgentID: "agent-1", RequestID: "request", ClaimToken: "new-claim",
					Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval","params":{"command":"date"}}`),
				})
			}
			restored := New(svc.Config)
			t.Cleanup(restored.InputQueue.StopAndWait)
			restored.sendControlResponseFn = send
			changedAnswer := []byte(`{"id":"request","result":{"decision":"decline"}}`)
			require.NoError(t, restored.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: changedAnswer, ClaimToken: "claim"}))
			require.Equal(t, 1, sends, "recovery must retain the response that already reached the provider")
			rows, err := restored.Queries.ListMessagesByAgentID(t.Context(), db.ListMessagesByAgentIDParams{AgentID: "agent-1", Limit: 10})
			require.NoError(t, err)
			answers := controlResponseRows(rows)
			require.Len(t, answers, 1)
			stored := decodeStructuredControlResponse(t, answers[0])
			require.JSONEq(t, string(response), string(stored.Response))
			requests, err := restored.Queries.ListControlRequestsByAgentID(t.Context(), "agent-1")
			require.NoError(t, err)
			if replaceRequest {
				require.Len(t, requests, 1)
				require.Equal(t, "new-claim", requests[0].ClaimToken)
			} else {
				require.Empty(t, requests)
			}
		})
	}
}

func TestControlResponseKeepsTheRequestWhenTheAgentCannotReceiveIt(t *testing.T) {
	svc, dispatcher, writer := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
		Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`),
	})
	dispatch(dispatcher, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1", ClaimToken: "claim",
		Content: []byte(`{"id":"request","result":{"decision":"accept"}}`),
	}, writer)
	require.Empty(t, writer.errors)
	response := decodeQueueResponse(t, writer, &leapmuxv1.SendControlResponseResponse{})
	require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY, response.State)
	require.NotEmpty(t, response.Error)
	request, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err, "a response that did not reach the agent must not remove its request")
	require.Equal(t, "claim", request.ClaimToken)
	rows, err := svc.Queries.ListMessagesByAgentID(t.Context(), db.ListMessagesByAgentIDParams{AgentID: "agent-1", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, controlResponseRows(rows), "an undelivered answer must not appear as an accepted response")
}

func TestControlResponseRetriesOnlyAfterConfirmedDeliveryFailure(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "not sent", true: "uncertain"}[uncertain], func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
				Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`),
			})
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			sends := 0
			failure := errors.New("the response was not written")
			if uncertain {
				failure = agent.ErrDeliveryUncertain
			}
			svc.sendControlResponseFn = func(string, []byte) error {
				sends++
				if sends == 1 {
					return failure
				}
				return nil
			}
			response := []byte(`{"id":"request","result":{"decision":"accept"}}`)
			err = svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim"})
			require.ErrorIs(t, err, failure)
			requests, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), "agent-1")
			require.NoError(t, err)
			require.Len(t, requests, 1)
			err = svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: response, ClaimToken: "claim"})
			if uncertain {
				require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
				require.Equal(t, 1, sends)
			} else {
				require.NoError(t, err)
				require.Equal(t, 2, sends)
			}
		})
	}
}

func TestDeliveredControlResponseKeepsAReissuedRequest(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "request", ClaimToken: "old-claim",
		Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`),
	})
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	svc.sendControlResponseFn = func(string, []byte) error {
		createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
			AgentID: "agent-1", RequestID: "request", ClaimToken: "new-claim",
			Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval","params":{"command":"date"}}`),
		})
		return nil
	}
	err = svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: []byte(`{"id":"request","result":{"decision":"accept"}}`), ClaimToken: "old-claim"})
	require.NoError(t, err)
	request, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	require.Equal(t, "new-claim", request.ClaimToken)
}

// processControlResponseForTest captures bytes at the native delivery boundary.
// The caller owns the service and invokes this helper without concurrent dispatch.
func processControlResponseForTest(svc *Service, agentID string, row db.Agent, content []byte, claimToken string) ([]byte, bool, error) {
	previous := svc.sendControlResponseFn
	defer func() { svc.sendControlResponseFn = previous }()
	var received []byte
	delivered := false
	svc.sendControlResponseFn = func(_ string, data []byte) error {
		received = append([]byte(nil), data...)
		delivered = true
		return nil
	}
	err := svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: agentID, Content: content, ClaimToken: claimToken})
	return received, delivered, err
}
