package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/eventjournal"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type pausedSemanticWatchWriter struct {
	*testResponseWriter
	sentFirst chan struct{}
	resume    chan struct{}
}

func (w *pausedSemanticWatchWriter) SendStream(message *leapmuxv1.InnerStreamMessage) error {
	if err := w.testResponseWriter.SendStream(message); err != nil {
		return err
	}
	var frame leapmuxv1.WatchSemanticEventsResponse
	if err := proto.Unmarshal(message.GetPayload(), &frame); err == nil && frame.GetRecord().GetSequence() == 1 {
		close(w.sentFirst)
		<-w.resume
	}
	return nil
}

func TestWatchSemanticEventsRevokesAnOpenHistoryPage(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	svc.EnableSemanticEvents()
	t.Cleanup(func() {
		svc.Shutdown()
		svc.FinalizeSemanticEvents(true)
	})
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)

	const terminalID = "terminal-with-history"
	require.NoError(t, svc.Queries.UpsertTerminal(t.Context(), db.UpsertTerminalParams{
		ID: terminalID, WorkingDir: t.TempDir(), Cols: 200, Rows: 24, Screen: []byte{},
	}))
	_, ready, err := journal.Open(t.Context(), eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_TERMINAL, ID: terminalID}, nil)
	require.NoError(t, err)
	for range 2 {
		svc.Watchers.BroadcastTerminalEvent(terminalID, &leapmuxv1.TerminalEvent{
			TerminalId: terminalID,
			Event:      &leapmuxv1.TerminalEvent_Bell{Bell: &leapmuxv1.TerminalBell{}},
		})
	}
	writer := &pausedSemanticWatchWriter{testResponseWriter: newTestWriter(), sentFirst: make(chan struct{}), resume: make(chan struct{})}
	release := sync.OnceFunc(func() { close(writer.resume) })
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	caller := channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read terminal:read"))
	go func() {
		defer close(done)
		svc.watchSemanticEvents(ctx, caller, &leapmuxv1.WatchSemanticEventsRequest{
			Tab: terminalTabIdent(terminalID), AfterCursor: ready.GetCursor(),
		}, writer)
	}()
	select {
	case <-writer.sentFirst:
	case <-ctx.Done():
		t.Fatal("the watch did not send the first retained record")
	}
	require.Equal(t, IdentityNewOwner, svc.ApplyWorkerIdentity(&leapmuxv1.WorkerIdentity{RegisteredBy: "another-owner"}))
	release()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the watch did not end after owner revocation")
	}
	messages := writer.streamsSnapshot()
	require.Len(t, messages, 3, "ready, one record and a permission refusal")
	assert.Equal(t, int32(codes.PermissionDenied), messages[2].GetError().GetCode())
	assert.True(t, messages[2].GetEnd())
}

func TestWatchSemanticEventsRequiresTerminalReadBeforeUsingTheCursor(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	writer := newTestWriter()
	caller := channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read"))
	svc.watchSemanticEvents(t.Context(), caller, &leapmuxv1.WatchSemanticEventsRequest{
		Tab: terminalTabIdent("terminal"), AfterCursor: []byte("invalid cursor"),
	}, writer)
	messages := writer.streamsSnapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, int32(codes.PermissionDenied), messages[0].GetError().GetCode())
	assert.Contains(t, messages[0].GetError().GetMessage(), "terminal:read")
}

func TestSemanticRequestHandleMatchesOnlyTheCurrentClaim(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	svc.EnableSemanticEvents()
	t.Cleanup(func() {
		svc.Shutdown()
		svc.FinalizeSemanticEvents(true)
	})
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)
	const agentID = "agent-with-control-history"
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	markPending := func(claim string) {
		_, err := svc.Queries.StoreControlRequest(t.Context(), db.StoreControlRequestParams{
			AgentID: agentID, RequestID: "reused ID", ClaimToken: claim, Payload: []byte("{}"),
		})
		require.NoError(t, err)
	}
	markPending("old claim")
	request := &leapmuxv1.SemanticAgentRequest{
		RequestId: "reused ID", RequestInstance: journal.RequestInstance(agentID, "old claim"),
		PromptKind: leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_PERMISSION,
	}
	handle, err := svc.currentSemanticRequestHandle(t.Context(), journal, agentID, request)
	require.NoError(t, err)
	assert.Equal(t, "old claim", handle)
	request.PromptKind = leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_UNREADABLE
	handle, err = svc.currentSemanticRequestHandle(t.Context(), journal, agentID, request)
	require.NoError(t, err)
	assert.Empty(t, handle, "an unreadable request cannot take an answer")
	request.PromptKind = leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_PERMISSION
	markPending("replacement claim")
	handle, err = svc.currentSemanticRequestHandle(t.Context(), journal, agentID, request)
	require.NoError(t, err)
	assert.Empty(t, handle)
}

func TestWatchSemanticEventsRequiresTheAgentGrantForAnAgentTab(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	writer := newTestWriter()
	caller := channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read terminal:read"))
	svc.watchSemanticEvents(t.Context(), caller, &leapmuxv1.WatchSemanticEventsRequest{
		Tab: agentTabIdent("agent"), AfterCursor: []byte("invalid cursor"),
	}, writer)
	messages := writer.streamsSnapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, int32(codes.PermissionDenied), messages[0].GetError().GetCode())
	assert.Contains(t, messages[0].GetError().GetMessage(), "agent:read")
}

func TestSemanticJournalRetainsAnAdmittedHandlerPublicationDuringShutdown(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	svc.EnableSemanticEvents()
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)
	tab := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: "agent"}
	_, checkpoint, err := journal.Open(t.Context(), tab, nil)
	require.NoError(t, err)
	svc.Shutdown()

	// A plain dispatcher handler can finish after the Service drains its
	// processes. Its control response still has a publication to make.
	svc.Watchers.BroadcastAgentEvent(tab.ID, &leapmuxv1.AgentEvent{
		AgentId: tab.ID,
		Event: &leapmuxv1.AgentEvent_ControlCancel{ControlCancel: &leapmuxv1.AgentControlCancelRequest{
			RequestId: "answered", ClaimToken: "claim", ResponseState: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
		}},
	})
	svc.FinalizeSemanticEvents(true)
	restarted, err := eventjournal.Start(t.Context(), svc.DB, svc.WorkerID, svc.RegisteredBy(), svc.Clock)
	require.NoError(t, err)
	defer func() { assert.NoError(t, restarted.Stop(t.Context(), true)) }()
	reader, _, err := restarted.Open(t.Context(), tab, checkpoint.GetCursor())
	require.NoError(t, err)
	frames, _, err := reader.Read(t.Context(), checkpoint.GetAfterSequence())
	require.NoError(t, err)
	require.Len(t, frames, 1)
	ended := frames[0].GetRecord().GetFact().GetAgentRequestEnded()
	require.NotNil(t, ended)
	assert.Equal(t, "answered", ended.GetRequestId())
}
