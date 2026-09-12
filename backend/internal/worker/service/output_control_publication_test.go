package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type controlPublicationWriter struct {
	mockResponseWriter
	mu       sync.Mutex
	requests []*leapmuxv1.AgentControlRequest
}

func (w *controlPublicationWriter) SendStream(message *leapmuxv1.InnerStreamMessage) error {
	var response leapmuxv1.WatchEventsResponse
	if err := proto.Unmarshal(message.GetPayload(), &response); err != nil {
		return err
	}
	if request := response.GetAgentEvent().GetControlRequest(); request != nil {
		w.mu.Lock()
		w.requests = append(w.requests, request)
		w.mu.Unlock()
	}
	return nil
}

func (w *controlPublicationWriter) snapshot() []*leapmuxv1.AgentControlRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*leapmuxv1.AgentControlRequest(nil), w.requests...)
}

func TestControlPublicationStoresTheBroadcastPayloadAndClaim(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	writer := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "control-test"}}
	registerAgentWatch(svc, "control-test", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	payload := []byte(` {"jsonrpc":"2.0", "id":7,"wide":9007199254740993} `)
	var previousToken string
	for i := 0; i < 2; i++ {
		if i > 0 {
			sink.DeleteControlRequest("7")
		}
		require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "7", Payload: payload}))
		stored, err := svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "7"})
		require.NoError(t, err)
		requests := writer.snapshot()
		require.Len(t, requests, i+1)
		request := requests[i]
		assert.Equal(t, payload, stored.Payload)
		assert.Equal(t, stored.Payload, request.Payload)
		assert.NotEmpty(t, stored.ClaimToken)
		assert.Equal(t, stored.ClaimToken, request.ClaimToken)
		assert.NotEqual(t, previousToken, request.ClaimToken)
		assert.Equal(t, "7", request.RequestId)
		assert.Equal(t, "agent-1", request.AgentId)
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, request.AgentProvider)
		previousToken = request.ClaimToken
	}
}

func TestControlPublicationPersistsAndBroadcastsSourceSequence(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	writer := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "control-source"}}
	registerAgentWatch(svc, "control-source", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	request := agent.ControlRequest{RequestID: "dialog", Payload: []byte(` {"id":"dialog","method":"select"} `), SourceSeq: 23}
	require.NoError(t, sink.PublishControlRequest(request))
	stored, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "dialog"})
	require.NoError(t, err)
	assert.Equal(t, request.SourceSeq, stored.SourceSeq)
	assert.Equal(t, request.Payload, stored.Payload)
	require.Len(t, writer.snapshot(), 1)
	assert.Equal(t, request.SourceSeq, writer.snapshot()[0].SourceSeq)
	request.SourceSeq = 0
	require.NoError(t, sink.PublishControlRequest(request))
	assert.Equal(t, int64(23), writer.snapshot()[1].SourceSeq)
}

func TestControlPublicationDoesNotPublishAfterStorageFailure(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	writer := newTestWatcher("control-test")
	registerAgentWatch(svc, "control-test", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	_, err := svc.DB.ExecContext(context.Background(), `CREATE TRIGGER reject_control_request
		BEFORE INSERT ON control_requests BEGIN SELECT RAISE(ABORT, 'control storage unavailable'); END`)
	require.NoError(t, err)
	payload := []byte(`{"jsonrpc":"2.0","id":7,"method":"item/tool/requestUserInput","params":{}}`)
	require.ErrorContains(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "7", Payload: payload}), "control storage unavailable")
	assert.Zero(t, writer.streamCount.Load(), "a failed insert must not publish a request or change activity")
	state := svc.Output.activityFor("agent-1", "agent-1")
	state.mu.Lock()
	defer state.mu.Unlock()
	assert.Empty(t, state.pendingControl)
}

func TestControlPublicationKeepsClaimOnRepeatedAnnouncement(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	payload := []byte(`{"id":7,"request_id":"permission-1","method":"interaction/requestPermission"}`)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "permission-1", Payload: payload}))
	first, err := svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "permission-1"})
	require.NoError(t, err)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "permission-1", Payload: payload}))
	repeated, err := svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "permission-1"})
	require.NoError(t, err)
	assert.Equal(t, first.ClaimToken, repeated.ClaimToken, "a repeated announcement belongs to the same request instance")
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "permission-1", Payload: []byte(`{"id":7,"request_id":"permission-1","changed":true}`)}))
	changed, err := svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "permission-1"})
	require.NoError(t, err)
	assert.NotEqual(t, first.ClaimToken, changed.ClaimToken, "a revised request starts a new instance")
}

func TestControlPublicationConcurrentAnnouncementsShareOneClaim(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	writer := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "control-test"}}
	registerAgentWatch(svc, "control-test", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	const count = 8
	errors := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			errors <- sink.PublishControlRequest(agent.ControlRequest{RequestID: "same", Payload: []byte(`{"id":1,"request_id":"same"}`)})
		}()
	}
	for i := 0; i < count; i++ {
		require.NoError(t, <-errors)
	}
	stored, err := svc.Queries.GetControlRequest(context.Background(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "same"})
	require.NoError(t, err)
	requests := writer.snapshot()
	require.Len(t, requests, count)
	for _, request := range requests {
		assert.Equal(t, stored.ClaimToken, request.ClaimToken)
	}
}

func createTestControlRequest(t *testing.T, ctx context.Context, queries *db.Queries, params db.StoreControlRequestParams) {
	t.Helper()
	_, err := queries.StoreControlRequest(ctx, params)
	require.NoError(t, err)
}
