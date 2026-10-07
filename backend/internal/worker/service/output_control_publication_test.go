package service

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type observedControlRead struct {
	db.DBTX
	read func(context.Context, string, ...any) *sql.Row
}

type observedControlStore struct {
	db.DBTX
	store func(context.Context, string, ...any) *sql.Row
}

type heldRetirementWriter struct {
	mockResponseWriter
	entered chan struct{}
	release chan struct{}
	held    atomic.Bool
}

func (w *heldRetirementWriter) SendStream(message *leapmuxv1.InnerStreamMessage) error {
	var response leapmuxv1.WatchEventsResponse
	if err := proto.Unmarshal(message.GetPayload(), &response); err != nil {
		return err
	}
	if activity := response.GetAgentEvent().GetActivityChanged(); activity != nil &&
		activity.GetState() == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE && w.held.CompareAndSwap(false, true) {
		close(w.entered)
		<-w.release
	}
	return nil
}

func (q observedControlStore) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.Contains(query, "-- name: StoreControlRequest :one") {
		return q.store(ctx, query, args...)
	}
	return q.DBTX.QueryRowContext(ctx, query, args...)
}

func (q observedControlRead) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.Contains(query, "-- name: GetControlRequest :one") {
		return q.read(ctx, query, args...)
	}
	return q.DBTX.QueryRowContext(ctx, query, args...)
}

type controlPublicationWriter struct {
	mockResponseWriter
	mu            sync.Mutex
	requests      []*leapmuxv1.AgentControlRequest
	cancellations []*leapmuxv1.AgentControlCancelRequest
}

func (w *controlPublicationWriter) SendStream(message *leapmuxv1.InnerStreamMessage) error {
	var response leapmuxv1.WatchEventsResponse
	if err := proto.Unmarshal(message.GetPayload(), &response); err != nil {
		return err
	}
	request := response.GetAgentEvent().GetControlRequest()
	if request == nil {
		request = response.GetAgentEvent().GetControlResponseChanged()
	}
	if request != nil {
		w.mu.Lock()
		w.requests = append(w.requests, request)
		w.mu.Unlock()
	}
	if cancellation := response.GetAgentEvent().GetControlCancel(); cancellation != nil {
		w.mu.Lock()
		w.cancellations = append(w.cancellations, cancellation)
		w.mu.Unlock()
	}
	return nil
}

func (w *controlPublicationWriter) cancellationSnapshot() []*leapmuxv1.AgentControlCancelRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*leapmuxv1.AgentControlCancelRequest(nil), w.cancellations...)
}

func TestControlCancellationBroadcastsTheRemovedInstanceToken(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	writer := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "cancel-test"}}
	registerAgentWatch(svc, "cancel-test", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	payload := []byte(`{"id":7,"method":"item/commandExecution/requestApproval"}`)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "request", Payload: payload}))
	first := writer.snapshot()[0]
	sink.CancelControlRequest("request")
	require.Len(t, writer.cancellationSnapshot(), 1)
	require.Equal(t, first.ClaimToken, writer.cancellationSnapshot()[0].ClaimToken)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "request", Payload: payload}))
	second := writer.snapshot()[1]
	require.NotEqual(t, first.ClaimToken, second.ClaimToken)
	svc.Output.ClearPendingControlRequests("agent-1")
	require.Len(t, writer.cancellationSnapshot(), 2)
	require.Equal(t, second.ClaimToken, writer.cancellationSnapshot()[1].ClaimToken)
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
			sink.CancelControlRequest("7")
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

func TestControlPublicationStartsANewClaimForAnotherProviderSession(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	request := agent.ControlRequest{RequestID: "same-request", Payload: []byte(`{"question":"Choose one"}`), SourceSeq: 10}
	sink.UpdateSessionID("old-session")
	require.NoError(t, sink.PublishControlRequest(request))
	before, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: request.RequestID})
	require.NoError(t, err)
	sink.UpdateSessionID("new-session")
	request.SourceSeq = 20
	require.NoError(t, sink.PublishControlRequest(request))
	after, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: request.RequestID})
	require.NoError(t, err)
	require.NotEqual(t, before.ClaimToken, after.ClaimToken)
	require.Equal(t, "new-session", after.AgentSessionID)
	require.Equal(t, int64(20), after.SourceSeq)
	require.Equal(t, before.Payload, after.Payload)
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

func TestControlPublicationReadFailureKeepsTheCurrentInstance(t *testing.T) {
	t.Parallel()
	svc, _, sink, _ := stopOwnershipFixture(t)
	publishStopControl(t, sink, "request")
	before, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	writer := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "failed-read"}}
	registerAgentWatch(svc, "failed-read", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	svc.Output.queries = db.New(observedControlRead{DBTX: svc.DB, read: func(ctx context.Context, _ string, _ ...any) *sql.Row {
		return svc.DB.QueryRowContext(ctx, "SELECT missing_control_column")
	}})
	require.ErrorContains(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "request", Payload: []byte(`{"changed":true}`)}), "read the pending control request")
	after, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Empty(t, writer.snapshot())
}

func TestControlPublicationStoreFailureKeepsTheCurrentInstance(t *testing.T) {
	t.Parallel()
	svc, _, sink, _ := stopOwnershipFixture(t)
	publishStopControl(t, sink, "request")
	before, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	writer := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "failed-store"}}
	registerAgentWatch(svc, "failed-store", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER reject_control_replacement
		BEFORE INSERT ON control_requests BEGIN SELECT RAISE(ABORT, 'control replacement unavailable'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "request", Payload: []byte(`{"changed":true}`)}), "control replacement unavailable")
	after, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Empty(t, writer.snapshot())
}

func TestControlPublicationRenewsAnUnknownLiveAssociation(t *testing.T) {
	t.Parallel()
	svc, _, sink, _ := stopOwnershipFixture(t)
	publishStopControl(t, sink, "request")
	before, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	st := svc.Output.activityFor("agent-1", "agent-1")
	st.mu.Lock()
	st.pendingControl = nil
	st.mu.Unlock()
	publishStopControl(t, sink, "request")
	after, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	assert.Equal(t, before.Payload, after.Payload)
	assert.NotEqual(t, before.ClaimToken, after.ClaimToken)
}

func TestControlPublicationAddsASequenceAfterAnUnsequencedAnnouncement(t *testing.T) {
	t.Parallel()
	svc, _, sink, _ := stopOwnershipFixture(t)
	request := agent.ControlRequest{RequestID: "request", Payload: []byte(`{"question":"Choose one"}`)}
	require.NoError(t, sink.PublishControlRequest(request))
	before, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	request.SourceSeq = 37
	require.NoError(t, sink.PublishControlRequest(request))
	after, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	assert.Equal(t, before.ClaimToken, after.ClaimToken)
	assert.Equal(t, int64(37), after.SourceSeq)
}

func TestControlPublicationKeepsOwnershipDuringATurnOrProcessChange(t *testing.T) {
	for _, boundary := range []string{"turn", "process"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			svc, _, oldSink, _ := stopOwnershipFixture(t)
			request := agent.ControlRequest{RequestID: "request", Payload: []byte(`{"question":"Choose one"}`), SourceSeq: 11}
			require.NoError(t, oldSink.PublishControlRequest(request))
			before, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
			require.NoError(t, err)
			entered, release := make(chan struct{}), make(chan struct{})
			var reads atomic.Int32
			svc.Output.queries = db.New(observedControlRead{DBTX: svc.DB, read: func(ctx context.Context, query string, args ...any) *sql.Row {
				if reads.Add(1) == 1 {
					close(entered)
					<-release
				}
				return svc.DB.QueryRowContext(ctx, query, args...)
			}})
			oldDone := make(chan error, 1)
			go func() { oldDone <- oldSink.PublishControlRequest(request) }()
			<-entered
			newSink := oldSink
			if boundary == "turn" {
				oldSink.SetTurnState(agent.TurnState{}, 2)
				oldSink.SetTurnState(agent.TurnState{Active: true}, 3)
			} else {
				newSink = svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
				svc.Output.NoteAgentProcessStarted("agent-1")
				newSink.SetTurnState(agent.TurnState{Active: true}, 1)
			}
			close(release)
			require.NoError(t, <-oldDone)
			request.SourceSeq = 22
			require.NoError(t, newSink.PublishControlRequest(request))
			after, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
			require.NoError(t, err)
			assert.NotEqual(t, before.ClaimToken, after.ClaimToken)
			assert.Equal(t, int64(22), after.SourceSeq)
		})
	}
}

func TestControlPublicationKeepsItsActivityEntryUntilTheStoreCompletes(t *testing.T) {
	t.Parallel()
	svc, _, childID, rootID := setupChildAgentTest(t)
	svc.Agents.PutAgentForTest(rootID, &heldStopAgent{calls: make(chan heldStopCall, 1)})
	root := svc.Output.sinkForAgent(rootID)
	require.NotNil(t, root)
	svc.Output.NoteAgentProcessStarted(rootID)
	sink := root.ChildSink(childID)
	st := svc.Output.activityFor(childID, rootID)
	entered, release := make(chan struct{}), make(chan struct{})
	svc.Output.queries = db.New(observedControlStore{DBTX: svc.DB, store: func(ctx context.Context, query string, args ...any) *sql.Row {
		close(entered)
		<-release
		return svc.DB.QueryRowContext(ctx, query, args...)
	}})
	done := make(chan error, 1)
	go func() {
		done <- sink.PublishControlRequest(agent.ControlRequest{RequestID: "child-control", Payload: []byte(`{"question":"Choose one"}`)})
	}()
	<-entered
	require.NoError(t, root.CloseBackgroundTask("row-key-1", bgtask.StatusCompleted))
	holdSettles(t, svc.Output).close()
	svc.Output.refreshActivityFrom(childID, rootID, nil, svc.Output.activitySeq.Add(1), settleImmediate)
	current, retained := svc.Output.activity.Load(childID)
	assert.True(t, retained, "activity retirement must retain a control mutation that still owns the entry")
	assert.Same(t, st, current)
	close(release)
	require.NoError(t, <-done)
}

func TestControlFinalizationWaitsForAnInFlightRepeatedPublication(t *testing.T) {
	t.Parallel()
	svc, _, sink, _ := stopOwnershipFixture(t)
	publishStopControl(t, sink, "request")
	before, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	row := requireAgentRow(t, svc, "agent-1")
	entered, release := make(chan struct{}), make(chan struct{})
	var stores atomic.Int32
	svc.Output.queries = db.New(observedControlStore{DBTX: svc.DB, store: func(ctx context.Context, query string, args ...any) *sql.Row {
		if stores.Add(1) == 1 {
			close(entered)
			<-release
		}
		return svc.DB.QueryRowContext(ctx, query, args...)
	}})
	publicationDone := make(chan error, 1)
	go func() {
		publicationDone <- sink.PublishControlRequest(agent.ControlRequest{RequestID: "request", Payload: before.Payload})
	}()
	<-entered
	svc.sendControlResponseFn = func(string, []byte) error { return nil }
	responseDone := make(chan error, 1)
	go func() {
		responseDone <- svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{
			AgentId: "agent-1", ClaimToken: before.ClaimToken,
			Content: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"request","response":{"behavior":"allow"}}}`),
		})
	}()
	st := svc.Output.activityFor("agent-1", "agent-1")
	admitted := assert.Eventually(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.controlMutations == 2
	}, 30*time.Second, 5*time.Millisecond, "the actual answer finalizer must wait behind the publication")
	close(release)
	require.NoError(t, <-publicationDone)
	require.NoError(t, <-responseDone)
	require.True(t, admitted)
	_, err = svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.ErrorIs(t, err, sql.ErrNoRows, "the finalizer removes the completed instance after its last announcement")
	publishStopControl(t, sink, "request")
	after, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: "agent-1", RequestID: "request"})
	require.NoError(t, err)
	assert.NotEqual(t, before.ClaimToken, after.ClaimToken, "a later announcement cannot reuse the retired claim")
}

func TestActivityRetirementKeepsATurnThatStartsDuringTheBroadcast(t *testing.T) {
	t.Parallel()
	svc, root, rootID, childID := setupActivityRegistryTest(t)
	svc.Output.refreshActivityTree(rootID, settleImmediate)
	st := svc.Output.activityFor(childID, rootID)
	writer := &heldRetirementWriter{
		mockResponseWriter: mockResponseWriter{channelID: "retirement-test"},
		entered:            make(chan struct{}), release: make(chan struct{}),
	}
	registerAgentWatch(svc, "retirement-test", childID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	require.NoError(t, root.CloseBackgroundTask("row-key-1", bgtask.StatusCompleted))
	done := make(chan struct{})
	go func() { defer close(done); holdSettles(t, svc.Output).close() }()
	<-writer.entered
	root.ChildSink(childID).SetTurnState(agent.TurnState{Active: true}, 1)
	close(writer.release)
	<-done
	current, retained := svc.Output.activity.Load(childID)
	assert.True(t, retained, "the old idle broadcast cannot retire the new native turn")
	if retained {
		assert.Same(t, st, current)
	}
}
