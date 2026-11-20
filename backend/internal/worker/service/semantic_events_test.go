package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/eventjournal"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
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
	_, err = svc.Queries.SetAgentWorkspaceArchived(t.Context(), db.SetAgentWorkspaceArchivedParams{ID: agentID, WorkspaceArchived: true})
	require.NoError(t, err)
	handle, err = svc.currentSemanticRequestHandle(t.Context(), journal, agentID, request)
	require.NoError(t, err)
	assert.Empty(t, handle, "archive already prevents answering the open prompt")
	_, err = svc.Queries.SetAgentWorkspaceArchived(t.Context(), db.SetAgentWorkspaceArchivedParams{ID: agentID, WorkspaceArchived: false})
	require.NoError(t, err)
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

func TestClosedAgentTreeWaitsForItsExitAndAdmittedChildPublication(t *testing.T) {
	t.Parallel()
	svc, dispatcher, childID, rootID := setupChildAgentTest(t)
	svc.EnableSemanticEvents()
	t.Cleanup(func() {
		svc.Shutdown()
		svc.FinalizeSemanticEvents(true)
	})
	exitEntered, exitReleased := make(chan struct{}), make(chan struct{})
	releaseExit := sync.OnceFunc(func() { close(exitReleased) })
	defer releaseExit()
	svc.Agents.SetOnExit(func(exit agent.ProcessExit) {
		close(exitEntered)
		<-exitReleased
		svc.HandleAgentProcessExit(exit)
		svc.Watchers.BroadcastAgentEvent(rootID, &leapmuxv1.AgentEvent{
			AgentId: rootID, Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}},
		})
	})
	provider := &shutdownWaitAgent{id: rootID, stopped: make(chan struct{})}
	_, err := svc.Agents.StartAgentWith(t.Context(), agent.Options{
		AgentID: rootID, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, WorkingDir: t.TempDir(),
	}, agenttest.Nop(), func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
		return provider, nil
	})
	require.NoError(t, err)

	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)
	root := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: rootID}
	child := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: childID}
	_, rootStart, err := journal.Open(t.Context(), root, nil)
	require.NoError(t, err)
	_, childStart, err := journal.Open(t.Context(), child, nil)
	require.NoError(t, err)

	// The child UI close leaves the native owner, resource and cursor live.
	closedChild := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure,
		&leapmuxv1.CloseTabRequest{Tab: agentTabIdent(childID)}, closedChild)
	require.Empty(t, closedChild.errors)
	childReader, childReady, err := journal.Open(t.Context(), child, childStart.GetCursor())
	require.NoError(t, err)
	page, _, err := childReader.Read(t.Context(), childReady.GetHighWaterSequence())
	require.NoError(t, err)
	require.Empty(t, page)

	ctx := testutil.DeadlineContext(t)
	childRow, err := svc.Queries.GetAgentByID(ctx, childID)
	require.NoError(t, err)
	finishChild, err := svc.admitAgentMutation(ctx, childRow)
	require.NoError(t, err)
	releaseChild := sync.OnceFunc(finishChild)
	defer releaseChild()
	closedRoot := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure,
		&leapmuxv1.CloseTabRequest{Tab: agentTabIdent(rootID)}, closedRoot)
	require.Empty(t, closedRoot.errors)
	select {
	case <-exitEntered:
	case <-ctx.Done():
		t.Fatal("the root exit publisher did not enter")
	}
	_, _, err = journal.Open(ctx, child, nil)
	require.ErrorIs(t, err, eventjournal.ErrAdmission)
	_, err = svc.admitAgentMutation(ctx, childRow)
	require.ErrorIs(t, err, errAgentPublicationClosed)

	releaseExit()
	require.NoError(t, svc.Agents.WaitForAgentExit(ctx, rootID))
	// The already-admitted child publisher still owns its result after the
	// provider completes. Its fact must be in the same retired history.
	svc.Watchers.BroadcastAgentEvent(childID, &leapmuxv1.AgentEvent{
		AgentId: childID, Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}},
	})
	releaseChild()
	caller := channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read agent:read"))
	for _, start := range []*leapmuxv1.SemanticWatchReady{rootStart, childStart} {
		writer := newTestWriter()
		svc.watchSemanticEvents(ctx, caller, &leapmuxv1.WatchSemanticEventsRequest{
			Tab: start.GetTab(), AfterCursor: start.GetCursor(),
		}, writer)
		messages := writer.streamsSnapshot()
		require.GreaterOrEqual(t, len(messages), 3)
		ends := 0
		for index, msg := range messages {
			require.Nil(t, msg.GetError())
			var frame leapmuxv1.WatchSemanticEventsResponse
			require.NoError(t, proto.Unmarshal(msg.GetPayload(), &frame))
			require.Nil(t, frame.GetGap())
			if frame.GetRecord().GetFact().GetAgentTurnEnd() != nil {
				ends++
			}
			if index == len(messages)-1 {
				require.NotNil(t, frame.GetRetired())
				assert.True(t, msg.GetEnd())
			}
		}
		assert.Equal(t, 1, ends)
	}
}

func TestClosedAgentRecordsADeliveredControlOrReportsAGap(t *testing.T) {
	for _, recoverWrite := range []bool{true, false} {
		t.Run(map[bool]string{true: "records the delivered answer", false: "reports incomplete recording"}[recoverWrite], func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, _ := setupTestService(t)
			svc.EnableSemanticEvents()
			t.Cleanup(func() {
				svc.Shutdown()
				svc.FinalizeSemanticEvents(true)
			})
			const agentID = "retired-control-owner"
			const requestID = "jsonrpc:1"
			createClaimTestAgent(t, svc, agentID)
			journal := svc.semanticEvents.journal.Load()
			require.NotNil(t, journal)
			tab := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: agentID}
			ctx := testutil.DeadlineContext(t)
			_, start, err := journal.Open(ctx, tab, nil)
			require.NoError(t, err)
			sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
			require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{
				RequestID: requestID,
				Payload:   []byte(`{"id":1,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`),
			}))
			request, err := svc.Queries.GetControlRequest(ctx, db.GetControlRequestParams{AgentID: agentID, RequestID: requestID})
			require.NoError(t, err)
			row, err := svc.Queries.GetAgentByID(ctx, agentID)
			require.NoError(t, err)
			trigger := fmt.Sprintf(`CREATE TRIGGER fail_retired_control BEFORE INSERT ON messages WHEN NEW.mark_type=%d
				BEGIN SELECT RAISE(ABORT, 'answer storage failed'); END`, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE)
			_, err = svc.DB.ExecContext(ctx, trigger)
			require.NoError(t, err)
			var sends atomic.Int64
			svc.sendControlResponseFn = func(string, agent.RawInput) error { sends.Add(1); return nil }
			require.ErrorContains(t, svc.processControlAnswer(row, workerAnswer{
				content:    []byte(`{"id":"jsonrpc:1","result":{"decision":"accept"}}`),
				claimToken: request.ClaimToken,
				followUp:   agent.ControlFollowUp{Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: "follow up on the permission"},
			}), "answer storage failed")
			require.Equal(t, int64(1), sends.Load())
			if recoverWrite {
				_, err := svc.DB.ExecContext(ctx, "DROP TRIGGER fail_retired_control")
				require.NoError(t, err)
				_, err = svc.InputQueue.SetPaused(ctx, agentID, true)
				require.NoError(t, err)
				for i := range inputqueue.MaxItems {
					_, err := svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
						ID: fmt.Sprintf("retired-queue-%d", i), AgentID: agentID,
						Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "preserved input",
					})
					require.NoError(t, err)
				}
			}

			closed := newTestWriter()
			dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure,
				&leapmuxv1.CloseTabRequest{Tab: agentTabIdent(agentID)}, closed)
			require.Empty(t, closed.errors)
			writer := newTestWriter()
			caller := channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read agent:read"))
			svc.watchSemanticEvents(ctx, caller, &leapmuxv1.WatchSemanticEventsRequest{
				Tab: start.GetTab(), AfterCursor: start.GetCursor(),
			}, writer)
			messages := writer.streamsSnapshot()
			require.GreaterOrEqual(t, len(messages), 3)
			var latest, final int64
			var opened, completed, gaps int
			for index, msg := range messages {
				require.Nil(t, msg.GetError())
				var frame leapmuxv1.WatchSemanticEventsResponse
				require.NoError(t, proto.Unmarshal(msg.GetPayload(), &frame))
				switch {
				case frame.GetGap() != nil:
					assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT, frame.GetGap().GetReason())
					latest = frame.GetGap().GetResumeAfterSequence()
					gaps++
				case frame.GetRecord() != nil:
					record := frame.GetRecord()
					latest = record.GetSequence()
					assert.Empty(t, record.GetCurrentRequestHandle())
					if fact := record.GetFact().GetAgentRequest(); fact != nil {
						assert.Equal(t, requestID, fact.GetRequestId())
						opened++
					}
					if fact := record.GetFact().GetAgentRequestEnded(); fact != nil &&
						fact.GetState() == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED {
						assert.Equal(t, requestID, fact.GetRequestId())
						completed++
					}
				case frame.GetRetired() != nil:
					require.Equal(t, len(messages)-1, index)
					require.True(t, msg.GetEnd())
					final = frame.GetRetired().GetFinalSequence()
				}
			}
			assert.Equal(t, latest, final)
			assert.Positive(t, final)
			assert.Equal(t, 1, opened)
			want := leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED
			if recoverWrite {
				want = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED
				assert.Equal(t, 1, completed)
				assert.Zero(t, gaps)
			} else {
				assert.Zero(t, completed)
				assert.Equal(t, 1, gaps)
			}
			replayed, err := svc.recordControlResponse(row, &leapmuxv1.RecordControlResponseRequest{
				RequestId: requestID, ClaimToken: request.ClaimToken,
			})
			require.NoError(t, err)
			assert.Equal(t, want, replayed.GetState())
			answers, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: agentID, Limit: 10})
			require.NoError(t, err)
			assert.Len(t, controlResponseRows(answers), completed)
			queue, err := svc.InputQueue.Snapshot(ctx, agentID)
			require.NoError(t, err)
			if recoverWrite {
				assert.Len(t, queue.Items, inputqueue.MaxItems)
				for _, item := range queue.Items {
					assert.Equal(t, "preserved input", item.TextPreview)
				}
			} else {
				assert.Empty(t, queue.Items, "a retired agent cannot receive follow-up input")
			}
			_, resumed, err := journal.Open(ctx, tab, start.GetCursor())
			require.NoError(t, err)
			assert.Equal(t, final, resumed.GetHighWaterSequence())
			assert.Equal(t, int64(1), sends.Load(), "recording cannot resend the provider response")
		})
	}
}

func TestSemanticRetirementReportsAGapWhenItsPublisherCannotJoin(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, _, _ := setupTestService(t, withClock(clock))
	svc.EnableSemanticEvents()
	t.Cleanup(func() {
		svc.Shutdown()
		svc.FinalizeSemanticEvents(true)
	})
	const agentID = "held-retirement"
	createClaimTestAgent(t, svc, agentID)
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)
	ctx := testutil.DeadlineContext(t)
	tab := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: agentID}
	_, initial, err := journal.Open(ctx, tab, nil)
	require.NoError(t, err)
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, agentID)))
	timer := clock.Trap().AfterFunc(semanticRetirementTimerTag)
	defer timer.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	svc.retireSemanticHistory(tab.Type, []string{agentID}, func(context.Context) bool {
		close(entered)
		<-release
		return true
	})
	delay := testutil.WaitForTimer(t, ctx, timer)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the publisher join did not enter")
	}
	clock.Advance(delay).MustWait(ctx)
	writer := newTestWriter()
	svc.watchSemanticEvents(ctx, channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read agent:read")),
		&leapmuxv1.WatchSemanticEventsRequest{Tab: initial.GetTab(), AfterCursor: initial.GetCursor()}, writer)
	messages := writer.streamsSnapshot()
	require.Len(t, messages, 3, "ready, a coverage gap, and retirement")
	var gap, retired leapmuxv1.WatchSemanticEventsResponse
	require.NoError(t, proto.Unmarshal(messages[1].GetPayload(), &gap))
	require.NoError(t, proto.Unmarshal(messages[2].GetPayload(), &retired))
	require.NotNil(t, gap.GetGap())
	require.NotNil(t, retired.GetRetired())
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT, gap.GetGap().GetReason())
	assert.Equal(t, gap.GetGap().GetResumeAfterSequence(), retired.GetRetired().GetFinalSequence())
	assert.True(t, messages[2].GetEnd())
}

func TestClosedAgentDoesNotConfirmCoverageWhenAPromptCannotBeWithdrawn(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	svc.EnableSemanticEvents()
	t.Cleanup(func() {
		svc.Shutdown()
		svc.FinalizeSemanticEvents(true)
	})
	const agentID = "held-prompt"
	const requestID = "jsonrpc:1"
	createClaimTestAgent(t, svc, agentID)
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)
	ctx := testutil.DeadlineContext(t)
	_, initial, err := journal.Open(ctx, eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: agentID}, nil)
	require.NoError(t, err)
	sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{
		RequestID: requestID,
		Payload:   []byte(`{"id":1,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`),
	}))
	request, err := svc.Queries.GetControlRequest(ctx, db.GetControlRequestParams{AgentID: agentID, RequestID: requestID})
	require.NoError(t, err)
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(ctx, `CREATE TRIGGER fail_prompt_withdrawal BEFORE DELETE ON control_requests
		BEGIN SELECT RAISE(ABORT, 'prompt withdrawal failed'); END`)
	require.NoError(t, err)
	closed := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure,
		&leapmuxv1.CloseTabRequest{Tab: agentTabIdent(agentID)}, closed)
	require.Empty(t, closed.errors)
	writer := newTestWriter()
	svc.watchSemanticEvents(ctx, channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read agent:read")),
		&leapmuxv1.WatchSemanticEventsRequest{Tab: initial.GetTab(), AfterCursor: initial.GetCursor()}, writer)
	messages := writer.streamsSnapshot()
	require.GreaterOrEqual(t, len(messages), 4)
	var gap, retired leapmuxv1.WatchSemanticEventsResponse
	require.NoError(t, proto.Unmarshal(messages[len(messages)-2].GetPayload(), &gap))
	require.NoError(t, proto.Unmarshal(messages[len(messages)-1].GetPayload(), &retired))
	require.NotNil(t, gap.GetGap())
	require.NotNil(t, retired.GetRetired())
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT, gap.GetGap().GetReason())
	reply, err := svc.recordControlResponse(row, &leapmuxv1.RecordControlResponseRequest{
		RequestId: requestID, ClaimToken: request.ClaimToken,
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, reply.GetState())
	raw := newTestWriter()
	registerAgentWatch(svc, raw.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, raw)
	refused, err := svc.answerControl(row, &leapmuxv1.AnswerControlRequest{
		RequestId: requestID, ClaimToken: request.ClaimToken,
		NativeResponse: &leapmuxv1.NativeControlAnswer{
			Content: []byte(`{"id":"jsonrpc:1","result":{"decision":"accept"}}`),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED, refused.GetState())
	assert.Empty(t, raw.streamsSnapshot(), "a closed prompt cannot publish an answerable state")
}

func TestClosedAgentDoesNotRetryAnUnprovedControlDelivery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		delivery leapmuxv1.ControlDeliveryState
		state    leapmuxv1.ControlResponseState
	}{
		{"pending", storedStatePending, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING},
		{"uncertain", storedStateUncertain, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_UNCERTAIN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, dispatcher, _ := setupTestService(t)
			svc.EnableSemanticEvents()
			t.Cleanup(func() {
				svc.Shutdown()
				svc.FinalizeSemanticEvents(true)
			})
			const agentID = "retired-unproved-answer"
			const requestID = "jsonrpc:1"
			createClaimTestAgent(t, svc, agentID)
			ctx := testutil.DeadlineContext(t)
			journal := svc.semanticEvents.journal.Load()
			require.NotNil(t, journal)
			tab := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: agentID}
			_, initial, err := journal.Open(ctx, tab, nil)
			require.NoError(t, err)
			sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
			payload := []byte(`{"id":1,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`)
			require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, Payload: payload}))
			request, err := svc.Queries.GetControlRequest(ctx, db.GetControlRequestParams{AgentID: agentID, RequestID: requestID})
			require.NoError(t, err)
			delivery, err := svc.Output.claimControlResponseAnswer(db.StoreControlResponseAnswerParams{
				AgentID: agentID, RequestID: requestID, ClaimToken: request.ClaimToken,
				RequestPayload: request.Payload, AgentSessionID: request.AgentSessionID,
				ResolvedContent: []byte{}, ResponseContent: []byte(`{"id":"jsonrpc:1","result":{"decision":"accept"}}`),
			})
			require.NoError(t, err)
			require.NotEmpty(t, delivery)
			if tc.delivery == storedStateUncertain {
				require.NoError(t, setControlDeliveryState(svc.Queries, delivery, storedStatePending, storedStateUncertain))
			}
			row, err := svc.Queries.GetAgentByID(ctx, agentID)
			require.NoError(t, err)
			var sends atomic.Int64
			svc.sendControlResponseFn = func(string, agent.RawInput) error { sends.Add(1); return nil }

			closed := newTestWriter()
			dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure,
				&leapmuxv1.CloseTabRequest{Tab: agentTabIdent(agentID)}, closed)
			require.Empty(t, closed.errors)
			writer := newTestWriter()
			svc.watchSemanticEvents(ctx, channel.NewCaller(svc.RegisteredBy(), mustScopes("worker:read agent:read")),
				&leapmuxv1.WatchSemanticEventsRequest{Tab: initial.GetTab(), AfterCursor: initial.GetCursor()}, writer)
			messages := writer.streamsSnapshot()
			require.GreaterOrEqual(t, len(messages), 3)
			var gap, retired leapmuxv1.WatchSemanticEventsResponse
			require.NoError(t, proto.Unmarshal(messages[len(messages)-2].GetPayload(), &gap))
			require.NoError(t, proto.Unmarshal(messages[len(messages)-1].GetPayload(), &retired))
			require.NotNil(t, gap.GetGap())
			require.NotNil(t, retired.GetRetired())
			assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT, gap.GetGap().GetReason())
			reply, err := svc.recordControlResponse(row, &leapmuxv1.RecordControlResponseRequest{
				RequestId: requestID, ClaimToken: request.ClaimToken,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.state, reply.GetState())
			stored, err := svc.Queries.GetControlResponseAnswer(ctx, db.GetControlResponseAnswerParams{
				AgentID: agentID, RequestID: requestID, ClaimToken: request.ClaimToken,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.delivery, stored.State)
			_, final, err := journal.Open(ctx, tab, initial.GetCursor())
			require.NoError(t, err)
			assert.Equal(t, retired.GetRetired().GetFinalSequence(), final.GetHighWaterSequence())
			assert.Zero(t, sends.Load())
		})
	}
}

func TestSemanticJournalReportsAFailedFinalOutputFlushAfterRestart(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	svc.EnableSemanticEvents()
	const agentID = "shutdown-output"
	createClaimTestAgent(t, svc, agentID)
	journal := svc.semanticEvents.journal.Load()
	require.NotNil(t, journal)
	tab := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: agentID}
	_, initial, err := journal.Open(ctx, tab, nil)
	require.NoError(t, err)
	sink := newTestRootOutputSink(t, svc.Output, agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	completion := agent.CaptureTranscript(sink, agent.MessageContent{
		Original: []byte(`{"type":"result","subtype":"success"}`),
	}, agent.SpanInfo{})
	_, err = svc.DB.ExecContext(ctx, `CREATE TRIGGER refuse_final_output BEFORE INSERT ON messages
		BEGIN SELECT RAISE(ABORT, 'the completion write failed'); END`)
	require.NoError(t, err)
	queue := svc.Output.outputQueue(agentID)
	queue.Append(agent.OutputWrite{Owner: agent.NewOutputOwner(), Label: "persist the result", Run: completion.PersistTurnEnd})
	require.ErrorContains(t, queue.Flush(), "the completion write failed")

	svc.Shutdown()
	svc.FinalizeSemanticEvents(true)
	_, err = svc.DB.ExecContext(ctx, "DROP TRIGGER refuse_final_output")
	require.NoError(t, err)
	restarted, err := eventjournal.Start(ctx, svc.DB, svc.WorkerID, svc.RegisteredBy(), svc.Clock)
	require.NoError(t, err)
	defer func() { assert.NoError(t, restarted.Stop(ctx, true)) }()
	reader, ready, err := restarted.Open(ctx, tab, initial.GetCursor())
	require.NoError(t, err)
	frames, _, err := reader.Read(ctx, ready.GetAfterSequence())
	require.NoError(t, err)
	require.NotEmpty(t, frames)
	require.NotNil(t, frames[len(frames)-1].GetGap())
	assert.Equal(t, leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_UNCLEAN_STOP, frames[len(frames)-1].GetGap().GetReason())
}

func TestAdmittedRelaunchDoesNotLaunchAfterItsRowCloses(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	ctx := testutil.DeadlineContext(t)
	const agentID = "closed-during-relaunch"
	createClaimTestAgent(t, svc, agentID)
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	readStarted, resumeRead := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resumeRead) })
	defer release()
	originalGet := svc.getAgentByIDFn
	var first atomic.Bool
	svc.getAgentByIDFn = func(ctx context.Context, id string) (db.Agent, error) {
		result, err := originalGet(ctx, id)
		if id == agentID && first.CompareAndSwap(false, true) {
			close(readStarted)
			<-resumeRead
		}
		return result, err
	}
	var starts atomic.Int64
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		starts.Add(1)
		return nil, assert.AnError
	}
	restarted := make(chan error, 1)
	go func() {
		_, err := svc.restartAgentPreservingSession(row, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
		restarted <- err
	}()
	select {
	case <-readStarted:
	case <-ctx.Done():
		t.Fatal("the relaunch did not read the original live row")
	}
	closed := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerTabServiceCloseTabProcedure,
		&leapmuxv1.CloseTabRequest{Tab: agentTabIdent(agentID)}, closed)
	require.Empty(t, closed.errors)
	release()
	select {
	case err := <-restarted:
		require.Error(t, err)
	case <-ctx.Done():
		t.Fatal("the admitted relaunch did not finish")
	}
	assert.Zero(t, starts.Load())
}
