package service

import (
	"database/sql"
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
	write("same-turn-other-input", sessionID, "another-native-input", "own-turn", assistant, "Another input's completion", &outcome)
	write("second-reply", sessionID, "", "own-turn", assistant, "Second reply", nil)

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
	write("native-end", sessionID, "", "own-turn", assistant, "Completion marker", &outcome)
	write("too-late", sessionID, nativeID, "own-turn", assistant, "Arrived after the end", nil)
	terminal := read(inputID)
	assert.Equal(t, outcome, terminal.GetOutcome())
	assert.False(t, terminal.GetWaitingForUser())

	page := func(after int64, limit int32) *leapmuxv1.ListAgentMessagesResponse {
		t.Helper()
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{
			AgentId: agentID, ReplyInputId: inputID, IncludeReplyText: true,
			Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, ReplyCursor: after, Limit: limit,
		}, writer)
		require.Empty(t, writer.errors)
		return decodeQueueResponse(t, writer, &leapmuxv1.ListAgentMessagesResponse{})
	}
	firstPage := page(accepted, 1)
	require.Len(t, firstPage.GetMessages(), 1)
	assert.Equal(t, "First reply", firstPage.GetMessages()[0].GetReplyText())
	assert.True(t, firstPage.GetHasMore())
	assert.Equal(t, first, firstPage.GetNextReplyCursor())
	remaining := page(firstPage.GetNextReplyCursor(), 10)
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

func TestInputAcceptanceExposesAnAlreadyPublishedControlForTheBoundTurn(t *testing.T) {
	t.Parallel()
	svc, dispatcher := setupInputQueueRPCService(t)
	ctx := t.Context()
	const agentID, inputID = "agent-1", "queued-acp-input"
	receipt := agent.NativeInputReceipt{SessionID: "acp-session", InputID: "native-input", TurnID: "native-turn"}
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: agentID, AgentSessionID: receipt.SessionID}))
	store := inputqueue.NewStore(svc.Output.db, nil)
	_, err := store.Enqueue(ctx, inputqueue.NewItem{AgentID: agentID, ID: inputID, Text: "run the check", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, agentID, false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = svc.Queries.StoreControlRequest(ctx, db.StoreControlRequestParams{
		AgentID: agentID, AgentSessionID: receipt.SessionID, NativeTurnID: receipt.TurnID,
		RequestID: "already-blocked", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	readReply := func() *leapmuxv1.AgentInputTurnReply {
		t.Helper()
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
			&leapmuxv1.ListAgentInputQueueRequest{AgentId: agentID, ReplyInputId: inputID}, writer)
		require.Empty(t, writer.errors)
		return decodeQueueResponse(t, writer, &leapmuxv1.ListAgentInputQueueResponse{}).GetInputReply()
	}
	assert.Nil(t, readReply())
	row, _, err := store.Accept(ctx, *prepared, inputqueue.DispatchResult{StartsTurn: true, NativeReceipt: receipt})
	require.NoError(t, err)
	reply := readReply()
	require.NotNil(t, reply)
	assert.Equal(t, row.Seq, reply.GetAcceptedSeq())
	assert.True(t, reply.GetWaitingForUser())
	assert.Zero(t, reply.GetOutcome())
}

func TestCanonicalInputReplyUsesNativeOrderAndConsumingBoundaries(t *testing.T) {
	t.Parallel()
	svc, dispatcher := setupInputQueueRPCService(t)
	ctx := t.Context()
	const agentID, sessionID = "agent-1", "session-a"
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: agentID, AgentSessionID: sessionID}))
	write := func(id, key, input string, source leapmuxv1.MessageSource) int64 {
		t.Helper()
		content, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, id, agent.MessageCompletionComplete)
		require.NoError(t, err)
		compressed, compression := msgcodec.Compress(content)
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID: id, AgentID: agentID, AgentSessionID: sessionID, NativeInputID: input,
			IdempotencyKey: key, Source: source, Content: compressed, ContentCompression: compression,
			CreatedAt: sqltime.NewSQLiteTime(nowMillis()),
		})
		require.NoError(t, err)
		return seq
	}
	user, assistant := leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
	inputA := write("input-a", inputqueue.InputRequestKeyPrefix+"a", "native-a", user)
	write("input-b", inputqueue.InputRequestKeyPrefix+"b", "native-b", user)
	write("input-c", inputqueue.InputRequestKeyPrefix+"c", "native-c", user)
	last := write("last reply", "key-last", "", assistant)
	first := write("first reply", "key-first", "", assistant)
	require.Greater(t, first, last)
	sink := &agentOutputSink{h: svc.Output, agentID: agentID}
	outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	turn := agent.NativeReplyTurn{SessionID: sessionID, TurnID: "canonical-turn", Outcome: outcome,
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "key-first"}, {Ordinal: 4, MessageKey: "key-last"}},
		Inputs: []agent.NativeReplyBoundary{{InputID: "native-a", AfterOrdinal: 1}, {InputID: "native-b", AfterOrdinal: 3}},
	}
	// One failed write must leave both the turn and its input boundaries absent.
	broken := turn
	broken.Items = []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "key-first"}, {Ordinal: 4, MessageKey: "missing"}}
	require.Error(t, sink.PublishNativeReplyTurn(broken))
	_, err := svc.Queries.GetNativeReplyTurn(ctx, db.GetNativeReplyTurnParams{
		AgentID: agentID, AgentSessionID: sessionID, NativeTurnID: turn.TurnID,
	})
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, sink.PublishNativeReplyTurn(turn))
	require.NoError(t, sink.PublishNativeReplyTurn(turn))
	broken = turn
	broken.Outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	require.Error(t, sink.PublishNativeReplyTurn(broken))
	write("foreign first", "foreign-first", "", assistant)
	write("foreign last", "foreign-last", "", assistant)
	broken = turn
	broken.Items = []agent.NativeReplyItem{{Ordinal: 3, MessageKey: "foreign-first"}, {Ordinal: 5, MessageKey: "foreign-last"}}
	require.ErrorIs(t, sink.PublishNativeReplyTurn(broken), errNativeReplyConflict)
	// A steer and its earlier input can share a reply row at different positions.
	write("steer ending", "key-steer-ending", "", assistant)
	steer := agent.NativeReplyTurn{
		SessionID: sessionID, TurnID: "steer-turn", Outcome: outcome,
		Inputs: []agent.NativeReplyBoundary{{InputID: "native-c", AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 6, MessageKey: "key-last"}, {Ordinal: 9, MessageKey: "key-steer-ending"}},
	}
	broken = steer
	broken.Items = []agent.NativeReplyItem{{Ordinal: 6, MessageKey: "key-last"}, {Ordinal: 9, MessageKey: "key-last"}}
	require.Error(t, sink.PublishNativeReplyTurn(broken), "one reply cannot list the same row twice")
	require.NoError(t, sink.PublishNativeReplyTurn(steer))

	read := func(id string) *leapmuxv1.AgentInputTurnReply {
		t.Helper()
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
			&leapmuxv1.ListAgentInputQueueRequest{AgentId: agentID, ReplyInputId: id}, writer)
		require.Empty(t, writer.errors)
		return decodeQueueResponse(t, writer, &leapmuxv1.ListAgentInputQueueResponse{}).GetInputReply()
	}
	page := func(id string, cursor int64) *leapmuxv1.ListAgentMessagesResponse {
		t.Helper()
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{
			AgentId: agentID, ReplyInputId: id, IncludeReplyText: true,
			Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, ReplyCursor: cursor, Limit: 1,
		}, writer)
		require.Empty(t, writer.errors)
		return decodeQueueResponse(t, writer, &leapmuxv1.ListAgentMessagesResponse{})
	}
	assert.Equal(t, outcome, read("input-a").GetOutcome())
	assert.Equal(t, inputA, read("input-a").GetAcceptedSeq())
	assert.Equal(t, outcome, read("input-b").GetOutcome())
	a := page("input-a", 0)
	require.Len(t, a.GetMessages(), 1)
	assert.Equal(t, first, a.GetMessages()[0].GetSeq())
	require.True(t, a.GetHasMore())
	assert.Equal(t, int64(2), a.GetNextReplyCursor())
	rest := page("input-a", a.GetNextReplyCursor())
	require.Len(t, rest.GetMessages(), 1)
	assert.Equal(t, last, rest.GetMessages()[0].GetSeq())
	assert.False(t, rest.GetHasMore())
	b := page("input-b", 0)
	require.Len(t, b.GetMessages(), 1)
	assert.Equal(t, last, b.GetMessages()[0].GetSeq())
	assert.False(t, b.GetHasMore())
	c := page("input-c", 0)
	require.Len(t, c.GetMessages(), 1)
	assert.Equal(t, last, c.GetMessages()[0].GetSeq())
	assert.True(t, c.GetHasMore())
	assert.Equal(t, int64(6), c.GetNextReplyCursor())
	lastPage := page("input-c", c.GetNextReplyCursor())
	require.Len(t, lastPage.GetMessages(), 1)
	assert.Equal(t, "steer ending", lastPage.GetMessages()[0].GetId())
	assert.False(t, lastPage.GetHasMore())

	_, err = svc.Queries.DeleteMessageByAgentAndID(ctx, db.DeleteMessageByAgentAndIDParams{AgentID: agentID, ID: "first reply"})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, read("input-a").GetOutcome())
	assert.Equal(t, outcome, read("input-b").GetOutcome(), "the withheld item preceded the second input")
	_, err = svc.Queries.DeleteMessageByAgentAndID(ctx, db.DeleteMessageByAgentAndIDParams{AgentID: agentID, ID: "last reply"})
	require.NoError(t, err)
	for _, id := range []string{"input-b", "input-c"} {
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, read(id).GetOutcome())
	}
}

func TestNativeInputControlProofCannotChangeAReplacementClaim(t *testing.T) {
	t.Parallel()
	svc, dispatcher := setupInputQueueRPCService(t)
	ctx := t.Context()
	const agentID, sessionID, requestID = "agent-1", "session", "pending-question"
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: agentID, AgentSessionID: sessionID}))
	for _, inputID := range []string{"input-a", "input-b"} {
		content, compression := msgcodec.Compress([]byte("{}"))
		_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID: inputID, AgentID: agentID, AgentSessionID: sessionID, NativeInputID: inputID,
			IdempotencyKey: inputqueue.InputRequestKeyPrefix + inputID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content: content, ContentCompression: compression, CreatedAt: sqltime.NewSQLiteTime(nowMillis()),
		})
		require.NoError(t, err)
	}
	payload := []byte("{}")
	storeClaim := func(claim string) {
		t.Helper()
		_, err := svc.Queries.StoreControlRequest(ctx, db.StoreControlRequestParams{
			AgentID: agentID, AgentSessionID: sessionID, RequestID: requestID, ClaimToken: claim, Payload: payload,
		})
		require.NoError(t, err)
	}
	waiting := func(inputID string) bool {
		t.Helper()
		writer := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
			&leapmuxv1.ListAgentInputQueueRequest{AgentId: agentID, ReplyInputId: inputID}, writer)
		require.Empty(t, writer.errors)
		return decodeQueueResponse(t, writer, &leapmuxv1.ListAgentInputQueueResponse{}).GetInputReply().GetWaitingForUser()
	}
	storeClaim("claim-a")
	sink := &agentOutputSink{h: svc.Output, agentID: agentID}
	oldProof := agent.NativeControlInputs{SessionID: sessionID, RequestID: requestID, InstanceID: "claim-a", InputIDs: []string{"input-a"}}
	require.NoError(t, sink.SetNativeControlInputs(oldProof))
	assert.True(t, waiting("input-a"))
	_, err := svc.Queries.DeleteControlRequestInstance(ctx, db.DeleteControlRequestInstanceParams{
		AgentID: agentID, RequestID: requestID, ClaimToken: "claim-a",
	})
	require.NoError(t, err)
	storeClaim("claim-b")
	newProof := oldProof
	newProof.InstanceID = "claim-b"
	newProof.InputIDs = []string{"input-b"}
	require.NoError(t, sink.SetNativeControlInputs(newProof))
	assert.True(t, waiting("input-b"))
	assert.False(t, waiting("input-a"))

	staleClear := oldProof
	staleClear.InputIDs = nil
	require.NoError(t, sink.SetNativeControlInputs(staleClear))
	assert.True(t, waiting("input-b"), "the retired claim cannot clear its replacement's proof")
	require.NoError(t, sink.SetNativeControlInputs(oldProof))
	assert.False(t, waiting("input-a"), "the retired claim cannot attach its inputs to the replacement")
	assert.True(t, waiting("input-b"))
}
