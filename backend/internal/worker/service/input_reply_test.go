package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

func TestInputReplyReadsOnlyTheAcceptedInputsTurn(t *testing.T) {
	t.Parallel()
	svc, dispatcher := setupInputQueueRPCService(t)
	ctx := t.Context()
	const agentID, inputID, sessionID, nativeID = "agent-1", "accepted-input", "session-a", "native-input"
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: agentID, AgentSessionID: sessionID}))
	write := func(id, session, input, turn string, source leapmuxv1.MessageSource, text string, outcome *leapmuxv1.AgentInputTurnOutcome) int64 {
		t.Helper()
		content, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, text, agent.MessageCompletionComplete)
		require.NoError(t, err)
		compressed, compression := msgcodec.Compress(content)
		var requestKey string
		if id == inputID {
			requestKey = inputqueue.InputRequestKeyPrefix + "fixture"
		}
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID: id, AgentID: agentID, AgentSessionID: session, NativeInputID: input, NativeTurnID: turn,
			IdempotencyKey:   requestKey,
			InputTurnOutcome: outcome, Source: source, Content: compressed, ContentCompression: compression,
			CreatedAt: sqltime.NewSQLiteTime(nowMillis()),
		})
		require.NoError(t, err)
		return seq
	}
	user := leapmuxv1.MessageSource_MESSAGE_SOURCE_USER
	assistant := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
	accepted := write(inputID, sessionID, nativeID, "", user, "The accepted input", nil)
	first := write("first-reply", sessionID, nativeID, "own-turn", assistant, "First reply", nil)
	write("another-input", sessionID, "another-native-input", "another-turn", assistant, "Other input's reply", nil)
	write("another-session", "session-b", nativeID, "own-turn", assistant, "Other session's reply", nil)
	outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	write("wrong-turn", sessionID, nativeID, "another-turn", assistant, "Other turn's completion", &outcome)
	write("second-reply", sessionID, nativeID, "own-turn", assistant, "Second reply", nil)

	read := func(id string) *leapmuxv1.AgentInputTurnReply {
		t.Helper()
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
			&leapmuxv1.ListAgentInputQueueRequest{AgentId: agentID, ReplyInputId: id}, writer)
		require.Empty(t, writer.errors)
		return decodeQueueResponse(t, writer, &leapmuxv1.ListAgentInputQueueResponse{}).GetInputReply()
	}
	havePrompt := func(id, session, turn string) {
		t.Helper()
		_, err := svc.Queries.StoreControlRequest(ctx, db.StoreControlRequestParams{
			AgentID: agentID, AgentSessionID: session, NativeTurnID: turn, RequestID: id, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
	}
	havePrompt("other-input", sessionID, "another-turn")
	havePrompt("other-session", "session-b", "own-turn")
	assert.Nil(t, read("missing"))
	assert.Equal(t, accepted, read(inputID).GetAcceptedSeq())
	assert.False(t, read(inputID).GetWaitingForUser())
	havePrompt("own-prompt", sessionID, "own-turn")
	assert.False(t, read(inputID).GetWaitingForUser(), "the captured prompt has no input binding yet")
	sink := &agentOutputSink{h: svc.Output, agentID: agentID}
	receipt := agent.NativeInputReceipt{SessionID: sessionID, InputID: nativeID}
	require.NoError(t, sink.BindNativeInputTurn(receipt, "own-turn"))
	assert.True(t, read(inputID).GetWaitingForUser())
	assert.Zero(t, read(inputID).GetOutcome(), "a different turn's completion cannot settle the accepted input")
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: agentID, AgentSessionID: "session-b"}))
	assert.False(t, read(inputID).GetWaitingForUser(), "the accepted input's session was replaced")
	unproved := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	require.NoError(t, sink.RetireNativeInputTurn(receipt, unproved))
	assert.Equal(t, unproved, read(inputID).GetOutcome())

	// A historical completion can commit after later input started.
	write("native-end", sessionID, nativeID, "own-turn", assistant, "Completion marker", &outcome)
	write("too-late", sessionID, nativeID, "own-turn", assistant, "Arrived after the end", nil)
	terminal := read(inputID)
	assert.Equal(t, outcome, terminal.GetOutcome())
	assert.False(t, terminal.GetWaitingForUser())

	page := func(after int64, limit int32) *leapmuxv1.ListAgentMessagesResponse {
		t.Helper()
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{
			AgentId: agentID, ReplyInputId: inputID, IncludeReplyText: true,
			Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, CursorSeq: after, Limit: limit,
		}, writer)
		require.Empty(t, writer.errors)
		return decodeQueueResponse(t, writer, &leapmuxv1.ListAgentMessagesResponse{})
	}
	firstPage := page(accepted, 1)
	require.Len(t, firstPage.GetMessages(), 1)
	assert.Equal(t, "First reply", firstPage.GetMessages()[0].GetReplyText())
	assert.True(t, firstPage.GetHasMore())
	remaining := page(first, 10)
	require.Len(t, remaining.GetMessages(), 2)
	assert.Equal(t, "Second reply", remaining.GetMessages()[0].GetReplyText())
	assert.Equal(t, "native-end", remaining.GetMessages()[1].GetId())
	assert.False(t, remaining.GetHasMore())
}

func TestRestoreStateRetiresAnAcceptedInputWithoutAnOutcome(t *testing.T) {
	t.Parallel()
	svc, dispatcher := setupInputQueueRPCService(t)
	ctx := t.Context()
	const agentID, inputID = "agent-1", "crash-interrupted-input"
	content, compression := msgcodec.Compress([]byte(`{"content":"submitted input"}`))
	seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: inputID, AgentID: agentID, AgentSessionID: "old-session", NativeInputID: "accepted",
		IdempotencyKey: inputqueue.InputRequestKeyPrefix + "fixture",
		Source:         leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: content, ContentCompression: compression,
		CreatedAt: sqltime.NewSQLiteTime(nowMillis()),
	})
	require.NoError(t, err)

	svc.RestoreState()
	writer := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
		&leapmuxv1.ListAgentInputQueueRequest{AgentId: agentID, ReplyInputId: inputID}, writer)
	require.Empty(t, writer.errors)
	reply := decodeQueueResponse(t, writer, &leapmuxv1.ListAgentInputQueueResponse{}).GetInputReply()
	require.NotNil(t, reply)
	assert.Equal(t, seq, reply.GetAcceptedSeq())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, reply.GetOutcome())
}
