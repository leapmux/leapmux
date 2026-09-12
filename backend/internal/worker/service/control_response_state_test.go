package service

import (
	"errors"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestControlResponseReportsDeliveryState(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		deliveryError  error
		recordingError bool
		state          leapmuxv1.ControlResponseState
	}{
		{"completed", nil, false, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED},
		{"not sent", errors.New("provider refused the response"), false, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY},
		{"uncertain", agent.ErrDeliveryUncertain, false, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNCERTAIN},
		{"recording failed", nil, true, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			svc, dispatcher, writer := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
				Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval"}`),
			})
			sends := 0
			svc.sendControlResponseFn = func(string, []byte) error { sends++; return scenario.deliveryError }
			if scenario.recordingError {
				_, err := svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_recording BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'response storage unavailable'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
				require.NoError(t, err)
			}
			dispatch(dispatcher, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
				AgentId: "agent-1", RequestId: "request", ClaimToken: "claim",
				Content: []byte(`{"id":"request","result":{"decision":"accept"}}`),
			}, writer)
			require.Empty(t, writer.errors, "delivery outcomes must carry their typed state")
			require.Len(t, writer.responses, 1)
			var response leapmuxv1.SendControlResponseResponse
			require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
			require.Equal(t, scenario.state, response.State)
			require.Equal(t, scenario.deliveryError != nil || scenario.recordingError, response.Error != "")
			require.Equal(t, 1, sends)
			if scenario.recordingError {
				_, err := svc.DB.ExecContext(t.Context(), "DROP TRIGGER fail_control_recording")
				require.NoError(t, err)
			}
			dispatch(dispatcher, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
				AgentId: "agent-1", RequestId: "request", ClaimToken: "claim", RecordOnly: true,
			}, writer)
			require.Empty(t, writer.errors)
			require.Len(t, writer.responses, 2)
			require.NoError(t, proto.Unmarshal(writer.responses[1].GetPayload(), &response))
			expected := scenario.state
			if expected == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED {
				expected = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED
			}
			require.Equal(t, expected, response.State)
			require.Equal(t, 1, sends, "checking or recording a response must never send to the provider")
		})
	}
}

func TestControlResponseRejectsMismatchedRecordingRequests(t *testing.T) {
	for _, request := range []*leapmuxv1.SendControlResponseRequest{
		{RequestId: "other", Content: []byte(`{"id":"request","result":{}}`)},
		{RequestId: "request", RecordOnly: true, Content: []byte(`{"id":"request","result":{}}`)},
		{RecordOnly: true},
	} {
		svc, dispatcher, writer := setupTestService(t)
		createClaimTestAgent(t, svc, "agent-1")
		request.AgentId = "agent-1"
		svc.sendControlResponseFn = func(string, []byte) error {
			t.Fatal("invalid recording requests cannot send to the provider")
			return nil
		}
		dispatch(dispatcher, "SendControlResponse", request, writer)
		require.Len(t, writer.errors, 1)
		require.Empty(t, writer.responses)
	}
}

func TestControlRequestReplayRetainsResponseState(t *testing.T) {
	for _, state := range []leapmuxv1.ControlResponseState{
		leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING,
		leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNCERTAIN,
		leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED,
		leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
	} {
		t.Run(state.String(), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			payload := []byte(` {"id":9007199254740993,"method":"item/commandExecution/requestApproval"} `)
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim", Payload: payload,
			})
			_, err := svc.DB.ExecContext(t.Context(), `INSERT INTO control_response_answers (agent_id,request_id,claim_token,state) VALUES (?,?,?,?)`, "agent-1", "request", "claim", int64(state))
			require.NoError(t, err)
			replayed := buildAgentControlRequest(svc.Queries, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				agent.ControlRequest{RequestID: "request", Payload: payload}, "claim")
			require.Equal(t, payload, replayed.Payload)
			// The column holds the enum, so the state written is the state read.
			require.Equal(t, state, replayed.ResponseState)
			newRequest := buildAgentControlRequest(svc.Queries, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				agent.ControlRequest{RequestID: "request", Payload: payload}, "replacement-claim")
			require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, newRequest.ResponseState)
		})
	}
}

func TestControlResponseBroadcastsDeliveryProgress(t *testing.T) {
	svc, dispatcher, writer := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	watcher := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "response-state"}}
	registerAgentWatch(svc, "response-state", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, watcher)
	payload := []byte(`{"id":"request","method":"item/commandExecution/requestApproval"}`)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "request", Payload: payload}))
	first := watcher.snapshot()[0]
	_, err := svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_broadcast BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'response storage unavailable'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
	require.NoError(t, err)
	svc.sendControlResponseFn = func(string, []byte) error {
		events := watcher.snapshot()
		require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING, events[len(events)-1].ResponseState)
		return nil
	}
	dispatch(dispatcher, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
		AgentId: "agent-1", RequestId: "request", ClaimToken: first.ClaimToken,
		Content: []byte(`{"id":"request","result":{"decision":"accept"}}`),
	}, writer)
	events := watcher.snapshot()
	require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED, events[len(events)-1].ResponseState)
	for _, event := range events {
		require.Equal(t, payload, event.Payload)
		require.Equal(t, first.ClaimToken, event.ClaimToken)
	}
}

func TestControlResponseReplayRecoversADeletedRequest(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(fmt.Sprint(replaced), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			payload := []byte(` {"id":"request","method":"item/commandExecution/requestApproval","params":{"command":"pwd"}} `)
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "old", Payload: payload, SourceSeq: 23,
			})
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			_, err = svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_replay BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'response storage unavailable'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
			require.NoError(t, err)
			sends := 0
			svc.sendControlResponseFn = func(string, []byte) error { sends++; return nil }
			require.Error(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", Content: []byte(`{"id":"request","result":{"decision":"accept"}}`), ClaimToken: "old"}))
			svc.Output.ClearPendingControlRequests("agent-1")
			if replaced {
				createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
					AgentID: "agent-1", RequestID: "request", ClaimToken: "new", SourceSeq: 99,
					Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval","params":{"command":"date"}}`),
				})
			}
			replay := newTestWriter()
			svc.replayAgentCatchUp(newReplaySink(replay), &leapmuxv1.WatchAgentEntry{AgentId: "agent-1"}, row, nil)
			var recovered *leapmuxv1.AgentControlRequest
			for _, stream := range replay.streamsSnapshot() {
				request := decodeWatchAgentEvent(t, stream).GetControlRequest()
				if request != nil && request.ClaimToken == "old" {
					require.Nil(t, recovered, "a saved response needs only one recovery control")
					recovered = request
				}
			}
			require.NotNil(t, recovered)
			require.Equal(t, payload, recovered.Payload)
			require.Equal(t, int64(23), recovered.SourceSeq)
			require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED, recovered.ResponseState)
			watcher := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "recovered-response"}}
			registerAgentWatch(svc, "recovered-response", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, watcher)
			_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER fail_control_replay")
			require.NoError(t, err)
			response, err := svc.respondToControlRequest(row, &leapmuxv1.SendControlResponseRequest{
				RequestId: "request", ClaimToken: "old", RecordOnly: true,
			})
			require.NoError(t, err)
			require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, response.State)
			require.Equal(t, 1, sends)
			cancellations := watcher.cancellationSnapshot()
			require.Len(t, cancellations, 1, "every window must remove the completed recovery control")
			require.Equal(t, "old", cancellations[0].ClaimToken)
			require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, cancellations[0].ResponseState)
			if replaced {
				remaining, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
				require.NoError(t, err)
				require.Equal(t, "new", remaining.ClaimToken)
			}
		})
	}
}

func TestControlCancellationRetainsConfirmedDelivery(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	watcher := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "delivered-cancel"}}
	registerAgentWatch(svc, "delivered-cancel", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, watcher)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "request", Payload: []byte(`{"id":1}`)}))
	request := watcher.snapshot()[0]
	_, err := svc.DB.ExecContext(t.Context(), `INSERT INTO control_response_answers (agent_id,request_id,claim_token,state) VALUES (?,?,?,?)`, "agent-1", "request", request.ClaimToken, int64(leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED))
	require.NoError(t, err)
	sink.CancelControlRequest("request")
	cancellations := watcher.cancellationSnapshot()
	require.Len(t, cancellations, 1)
	require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED, cancellations[0].ResponseState)
}
