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
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/eventjournal"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
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

func TestClosedTerminalWatchRecordsAnInFlightExitBeforeRetirement(t *testing.T) {
	t.Parallel()
	svc, dispatcher, opened := setupTestService(t)
	svc.EnableSemanticEvents()
	t.Cleanup(func() {
		svc.Shutdown()
		svc.FinalizeSemanticEvents(true)
	})
	exitEntered, releaseExit := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseExit) })
	defer release()
	svc.startTerminalFn = func(ctx context.Context, opts terminal.Options, output terminal.OutputHandler, exit terminal.ExitHandler) error {
		return svc.Terminals.StartTerminal(ctx, opts, output, func(id string, code int) {
			close(exitEntered)
			<-releaseExit
			exit(id, code)
		})
	}
	terminalID := openTerminalViaRPC(t, svc, dispatcher, opened, t.TempDir())
	tab := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_TERMINAL, ID: terminalID}
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)
	_, checkpoint, err := journal.Open(t.Context(), tab, nil)
	require.NoError(t, err)

	closed := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab: terminalTabIdent(terminalID), WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	}, closed)
	require.Empty(t, closed.errors)
	require.Len(t, closed.responses, 1)
	ctx := testutil.DeadlineContext(t)
	select {
	case <-exitEntered:
	case <-ctx.Done():
		t.Fatal("the shell exit callback did not enter")
	}
	_, _, err = journal.Open(ctx, tab, nil)
	require.ErrorIs(t, err, eventjournal.ErrAdmission)
	reader, ready, err := journal.Open(ctx, tab, checkpoint.GetCursor())
	require.NoError(t, err)
	page, _, err := reader.Read(ctx, ready.GetHighWaterSequence())
	require.NoError(t, err)
	require.Empty(t, page, "closed history must wait for the shell exit callback")

	writer := newTestWriter()
	caller := channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read terminal:read"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.watchSemanticEvents(ctx, caller, &leapmuxv1.WatchSemanticEventsRequest{
			Tab: terminalTabIdent(terminalID), AfterCursor: checkpoint.GetCursor(),
		}, writer)
	}()
	release()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the closed history did not finish")
	}
	stream := writer.streamsSnapshot()
	require.GreaterOrEqual(t, len(stream), 3)
	for _, message := range stream {
		require.Nil(t, message.GetError())
	}
	var exit, retired leapmuxv1.WatchSemanticEventsResponse
	require.NoError(t, proto.Unmarshal(stream[len(stream)-2].GetPayload(), &exit))
	require.NoError(t, proto.Unmarshal(stream[len(stream)-1].GetPayload(), &retired))
	require.NotNil(t, exit.GetRecord().GetFact().GetTerminalExit())
	require.NotNil(t, retired.GetRetired())
	assert.Equal(t, exit.GetRecord().GetSequence(), retired.GetRetired().GetFinalSequence())
	assert.True(t, stream[len(stream)-1].GetEnd())
}

func TestTerminalShellExitLeavesItsJournalOpen(t *testing.T) {
	t.Parallel()
	svc, dispatcher, opened := setupTestService(t)
	svc.EnableSemanticEvents()
	t.Cleanup(func() {
		svc.Shutdown()
		svc.FinalizeSemanticEvents(true)
	})
	terminalID := openTerminalViaRPC(t, svc, dispatcher, opened, t.TempDir())
	tab := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_TERMINAL, ID: terminalID}
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)
	_, initial, err := journal.Open(t.Context(), tab, nil)
	require.NoError(t, err)
	exitTerminalAndWait(t, svc, dispatcher, terminalID, "")
	svc.Terminals.WaitForExit(terminalID)

	reader, start, err := journal.Open(t.Context(), tab, initial.GetCursor())
	require.NoError(t, err)
	page, _, err := reader.Read(t.Context(), start.GetAfterSequence())
	require.NoError(t, err)
	require.NotEmpty(t, page)
	assert.NotNil(t, page[len(page)-1].GetRecord().GetFact().GetTerminalExit())
	atExit, _, err := reader.Read(t.Context(), start.GetHighWaterSequence())
	require.NoError(t, err)
	assert.Empty(t, atExit, "a shell exit leaves an open tab history")
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
	createClaimTestAgent(t, svc, tab.ID)
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
