package service

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/db/dbtest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
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
				AgentID: "agent-1", RequestID: "jsonrpc:1", ClaimToken: "claim",
				Payload: []byte(`{"id":1,"method":"item/commandExecution/requestApproval"}`),
			})
			sends := 0
			svc.sendControlResponseFn = func(string, agent.RawInput) error { sends++; return scenario.deliveryError }
			if scenario.recordingError {
				_, err := svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_recording BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'response storage unavailable'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
				require.NoError(t, err)
			}
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			state, errText, err := svc.deliverControlAnswer(row, "jsonrpc:1", workerAnswer{
				claimToken: "claim",
				content:    []byte(`{"id":"jsonrpc:1","result":{"decision":"accept"}}`),
			})
			require.NoError(t, err, "delivery outcomes must carry their typed state")
			require.Equal(t, scenario.state, state)
			require.Equal(t, scenario.deliveryError != nil || scenario.recordingError, errText != "")
			require.Equal(t, 1, sends)
			if scenario.recordingError {
				_, err := svc.DB.ExecContext(t.Context(), "DROP TRIGGER fail_control_recording")
				require.NoError(t, err)
			}
			dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceRecordControlResponseProcedure, &leapmuxv1.RecordControlResponseRequest{
				AgentId: "agent-1", RequestId: "jsonrpc:1", ClaimToken: "claim",
			}, writer)
			require.Empty(t, writer.errors)
			require.Len(t, writer.responses, 1)
			var response leapmuxv1.RecordControlResponseResponse
			require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
			expected := scenario.state
			if expected == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED {
				expected = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED
			}
			require.Equal(t, expected, response.State)
			require.Equal(t, 1, sends, "checking or recording a response must never send to the provider")
		})
	}
}

// An answer that addresses a request other than the one that the Worker read
// and the provider answered reaches no agent, and it claims neither request.
// Both requests exist, so only the refusal keeps the answer from the agent.
func TestDeliverControlAnswerRefusesAnAnswerToAnotherRequest(t *testing.T) {
	for _, tc := range []struct {
		name, requestID, content, want string
	}{
		{"another request", "other", `{"id":"request","result":{"decision":"accept"}}`, `the answer addresses the request "request", not "other"`},
		{"no request", "other", `{"result":{"decision":"accept"}}`, `the answer addresses the request "", not "other"`},
		{"a missing request ID", "", `{"result":{"decision":"accept"}}`, "the control request ID is missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			for _, requestID := range []string{"request", "other"} {
				createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
					AgentID: "agent-1", RequestID: requestID, ClaimToken: "claim",
					Payload: []byte(`{"id":"` + requestID + `","method":"item/commandExecution/requestApproval"}`),
				})
			}
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			svc.sendControlResponseFn = func(string, agent.RawInput) error {
				t.Fatal("an answer to another request cannot reach the agent")
				return nil
			}

			state, errText, err := svc.deliverControlAnswer(row, tc.requestID, workerAnswer{claimToken: "claim", content: []byte(tc.content)})

			require.EqualError(t, err, tc.want)
			assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNSPECIFIED, state)
			assert.Empty(t, errText)
			for _, requestID := range []string{"request", "other"} {
				pending, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: requestID})
				require.NoError(t, err, "the request %q stays open", requestID)
				assert.Equal(t, "claim", pending.ClaimToken)
				_, err = svc.Queries.GetControlResponseAnswer(t.Context(), db.GetControlResponseAnswerParams{AgentID: "agent-1", RequestID: requestID, ClaimToken: "claim"})
				assert.ErrorIs(t, err, sql.ErrNoRows, "the refusal claims no answer for %q", requestID)
			}
		})
	}
}

// A recording addresses one request instance, so a request with no ID fails,
// and the RPC answers InvalidArgument.
func TestRecordControlResponseRefusesAMissingRequestID(t *testing.T) {
	svc, dispatcher, writer := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	svc.sendControlResponseFn = func(string, agent.RawInput) error {
		t.Fatal("a recording cannot send to the agent")
		return nil
	}

	response, err := svc.recordControlResponse(row, &leapmuxv1.RecordControlResponseRequest{ClaimToken: "claim"})
	require.EqualError(t, err, "the control request ID is missing")
	assert.Nil(t, response)

	dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceRecordControlResponseProcedure, &leapmuxv1.RecordControlResponseRequest{
		AgentId: "agent-1", ClaimToken: "claim",
	}, writer)
	require.Len(t, writer.errors, 1)
	assert.Equal(t, int32(codes.InvalidArgument), writer.errors[0].code)
	assert.Equal(t, "the control request ID is missing", writer.errors[0].message)
	assert.Empty(t, writer.responses)
}

// A recording of a request that has no saved answer only reports the state of
// the instance: READY while the instance waits, and CANCELED once the request
// is gone or another instance replaced it. It sends nothing to the agent, and
// it claims, deletes and writes nothing.
func TestRecordControlResponseOnlyReportsTheStateOfAnUnansweredRequest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stored     bool
		claimToken string
		want       leapmuxv1.ControlResponseState
	}{
		{"a waiting instance", true, "claim", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY},
		{"a replaced instance", true, "retired-claim", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED},
		{"a gone request", false, "claim", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			if tc.stored {
				createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
					AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
					Payload: []byte(`{"id":"request","method":"item/commandExecution/requestApproval"}`),
				})
			}
			watcher := &responseChangedWriter{mockResponseWriter: mockResponseWriter{channelID: "record-only"}}
			registerAgentWatch(svc, "record-only", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, watcher)
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			svc.sendControlResponseFn = func(string, agent.RawInput) error {
				t.Fatal("a recording cannot send to the agent")
				return nil
			}

			response, err := svc.recordControlResponse(row, &leapmuxv1.RecordControlResponseRequest{RequestId: "request", ClaimToken: tc.claimToken})

			require.NoError(t, err)
			assert.Equal(t, tc.want, response.GetState())
			assert.Empty(t, response.GetError())
			requests, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), "agent-1")
			require.NoError(t, err)
			if tc.stored {
				require.Len(t, requests, 1, "a recording leaves the request open")
				assert.Equal(t, "claim", requests[0].ClaimToken)
			} else {
				assert.Empty(t, requests)
			}
			_, err = svc.Queries.GetControlResponseAnswer(t.Context(), db.GetControlResponseAnswerParams{AgentID: "agent-1", RequestID: "request", ClaimToken: tc.claimToken})
			assert.ErrorIs(t, err, sql.ErrNoRows, "a recording claims no answer")
			messages, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "agent-1"})
			require.NoError(t, err)
			assert.Empty(t, messages)
			events := watcher.snapshot()
			if tc.want == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY {
				require.Len(t, events, 1, "every window learns the state of the waiting instance")
				assert.Equal(t, tc.want, events[0].ResponseState)
				assert.Equal(t, tc.claimToken, events[0].ClaimToken)
			} else {
				assert.Empty(t, events, "an instance that no longer waits and has no answer has no state to publish")
			}
		})
	}
}

func TestControlRequestReplayRetainsResponseState(t *testing.T) {
	for _, state := range []struct {
		stored leapmuxv1.ControlDeliveryState
		wire   leapmuxv1.ControlResponseState
	}{
		{storedStatePending, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING},
		{storedStateUncertain, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNCERTAIN},
		{storedStateDelivered, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED},
		{storedStateCompleted, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED},
	} {
		t.Run(state.wire.String(), func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			payload := []byte(` {"id":9007199254740993,"method":"item/commandExecution/requestApproval"} `)
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim", Payload: payload,
			})
			err := dbtest.StoreControlResponse(t.Context(), svc.DB, db.ControlResponseAnswer{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim", State: state.stored,
			})
			require.NoError(t, err)
			replayed, err := buildAgentControlRequest(svc.Queries, agent.ProviderDefaults{}, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				agent.ControlRequest{RequestID: "request", Payload: payload}, "claim")
			require.NoError(t, err)
			require.Equal(t, payload, replayed.Payload)
			// Stored delivery states map to public response states.
			require.Equal(t, state.wire, replayed.ResponseState)
			newRequest, err := buildAgentControlRequest(svc.Queries, agent.ProviderDefaults{}, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				agent.ControlRequest{RequestID: "request", Payload: payload}, "replacement-claim")
			require.NoError(t, err)
			require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, newRequest.ResponseState)
		})
	}
}

func TestControlResponseBroadcastsDeliveryProgress(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	watcher := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "response-state"}}
	registerAgentWatch(svc, "response-state", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, watcher)
	payload := []byte(`{"id":1,"method":"item/commandExecution/requestApproval"}`)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "jsonrpc:1", Payload: payload}))
	first := watcher.snapshot()[0]
	_, err := svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_broadcast BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'response storage unavailable'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
	require.NoError(t, err)
	svc.sendControlResponseFn = func(string, agent.RawInput) error {
		events := watcher.snapshot()
		require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING, events[len(events)-1].ResponseState)
		return nil
	}
	deliverControlAnswerForTest(t, svc, "agent-1", "jsonrpc:1", workerAnswer{
		claimToken: first.ClaimToken,
		content:    []byte(`{"id":"jsonrpc:1","result":{"decision":"accept"}}`),
	})
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
			payload := []byte(` {"id":1,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}} `)
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "jsonrpc:1", ClaimToken: "old", Payload: payload, SourceSeq: 23,
			})
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			_, err = svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_control_replay BEFORE INSERT ON messages WHEN NEW.mark_type=%d BEGIN SELECT RAISE(ABORT, 'response storage unavailable'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE))
			require.NoError(t, err)
			sends := 0
			svc.sendControlResponseFn = func(string, agent.RawInput) error { sends++; return nil }
			require.Error(t, svc.processControlAnswer(row, workerAnswer{content: []byte(`{"id":"jsonrpc:1","result":{"decision":"accept"}}`), claimToken: "old"}))
			svc.Output.ClearPendingControlRequests("agent-1")
			if replaced {
				createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
					AgentID: "agent-1", RequestID: "jsonrpc:1", ClaimToken: "new", SourceSeq: 99,
					Payload: []byte(`{"id":1,"method":"item/commandExecution/requestApproval","params":{"command":"date"}}`),
				})
			}
			replay := newTestWriter()
			svc.replayAgentCatchUp(newReplaySink(replay, 0, nil, nil), &leapmuxv1.WatchAgentEntry{AgentId: "agent-1"}, row, nil)
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
			response, err := svc.recordControlResponse(row, &leapmuxv1.RecordControlResponseRequest{
				RequestId: "jsonrpc:1", ClaimToken: "old",
			})
			require.NoError(t, err)
			require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, response.State)
			require.Equal(t, 1, sends)
			cancellations := watcher.cancellationSnapshot()
			require.Len(t, cancellations, 1, "every window must remove the completed recovery control")
			require.Equal(t, "old", cancellations[0].ClaimToken)
			require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, cancellations[0].ResponseState)
			if replaced {
				remaining, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "jsonrpc:1"})
				require.NoError(t, err)
				require.Equal(t, "new", remaining.ClaimToken)
			}
		})
	}
}

// The catch-up replay carries the non_blocking flag on both recovery paths: a
// stored request row, and a delivered answer whose request row is gone.
func TestControlRequestReplayCarriesNonBlocking(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	payload := []byte(` {"id":"question","method":"item/tool/requestUserInput"} `)
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "question-1", ClaimToken: "claim-1", Payload: payload, NonBlocking: true,
	})
	createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
		AgentID: "agent-1", RequestID: "approval-1", ClaimToken: "claim-2",
		Payload: []byte(` {"id":"approval","method":"item/commandExecution/requestApproval"} `),
	})
	require.NoError(t, dbtest.StoreControlResponse(t.Context(), svc.DB, db.ControlResponseAnswer{
		AgentID: "agent-1", RequestID: "question-2", ClaimToken: "claim-3", State: storedStateDelivered,
		RequestPayload: payload, NonBlocking: true,
	}))
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	replay := newTestWriter()
	svc.replayAgentCatchUp(newReplaySink(replay, 0, nil, nil), &leapmuxv1.WatchAgentEntry{AgentId: "agent-1"}, row, nil)

	replayed := map[string]*leapmuxv1.AgentControlRequest{}
	for _, stream := range replay.streamsSnapshot() {
		if request := decodeWatchAgentEvent(t, stream).GetControlRequest(); request != nil {
			replayed[request.RequestId] = request
		}
	}
	require.Contains(t, replayed, "question-1")
	require.Contains(t, replayed, "question-2")
	require.Contains(t, replayed, "approval-1")
	assert.True(t, replayed["question-1"].NonBlocking)
	assert.True(t, replayed["question-2"].NonBlocking, "the answer replay keeps the presentation of its request")
	assert.False(t, replayed["approval-1"].NonBlocking)
}

// responseChangedWriter records the controlResponseChanged events alone. A
// catch-up replay of the same rows emits controlRequest events, which this
// writer drops so a state assertion cannot absorb one.
type responseChangedWriter struct {
	mockResponseWriter
	mu     sync.Mutex
	events []*leapmuxv1.AgentControlRequest
}

func (w *responseChangedWriter) SendStream(message *leapmuxv1.InnerStreamMessage) error {
	var response leapmuxv1.WatchEventsResponse
	if err := proto.Unmarshal(message.GetPayload(), &response); err != nil {
		return err
	}
	if event := response.GetAgentEvent().GetControlResponseChanged(); event != nil {
		w.mu.Lock()
		w.events = append(w.events, event)
		w.mu.Unlock()
	}
	return nil
}

func (w *responseChangedWriter) snapshot() []*leapmuxv1.AgentControlRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*leapmuxv1.AgentControlRequest(nil), w.events...)
}

func TestBroadcastControlResponseStateCarriesNonBlocking(t *testing.T) {
	for _, branch := range []string{"request row", "answer row"} {
		t.Run(branch, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			createClaimTestAgent(t, svc, "agent-1")
			watcher := &responseChangedWriter{mockResponseWriter: mockResponseWriter{channelID: "state-change"}}
			registerAgentWatch(svc, "state-change", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, watcher)
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			payload := []byte(` {"id":"question","method":"item/tool/requestUserInput"} `)
			if branch == "request row" {
				createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
					AgentID: "agent-1", RequestID: "question-1", ClaimToken: "claim-1", Payload: payload, NonBlocking: true,
				})
			} else {
				require.NoError(t, dbtest.StoreControlResponse(t.Context(), svc.DB, db.ControlResponseAnswer{
					AgentID: "agent-1", RequestID: "question-1", ClaimToken: "claim-1", State: storedStateDelivered,
					RequestPayload: payload, NonBlocking: true,
				}))
			}

			svc.broadcastControlResponseState(row, "question-1", "claim-1")

			events := watcher.snapshot()
			require.Len(t, events, 1)
			assert.True(t, events[0].NonBlocking, "the state change carries the presentation of its request")
			want := leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY
			if branch == "answer row" {
				want = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED
			}
			assert.Equal(t, want, events[0].ResponseState)
		})
	}
}

func TestControlCancellationRetainsConfirmedDelivery(t *testing.T) {
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	watcher := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "delivered-cancel"}}
	registerAgentWatch(svc, "delivered-cancel", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, watcher)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "request", Payload: []byte(`{"id":1}`)}))
	request := watcher.snapshot()[0]
	err := dbtest.StoreControlResponse(t.Context(), svc.DB, db.ControlResponseAnswer{
		AgentID: "agent-1", RequestID: "request", ClaimToken: request.ClaimToken,
		State: storedStateDelivered,
	})
	require.NoError(t, err)
	sink.CancelControlRequest("request")
	cancellations := watcher.cancellationSnapshot()
	require.Len(t, cancellations, 1)
	require.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED, cancellations[0].ResponseState)
}
