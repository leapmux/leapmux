package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// replyTextRow is one stored row of a reply text test.
type replyTextRow struct {
	id             string
	source         leapmuxv1.MessageSource
	content        []byte
	compression    leapmuxv1.ContentCompression
	supplement     []byte
	agentSessionID string
	assembledKind  *leapmuxv1.AssembledMessageKind
}

// storeReplyTextRows writes the rows of agentID in order.
func storeReplyTextRows(t *testing.T, svc *Service, agentID string, rows []replyTextRow) {
	t.Helper()
	for _, row := range rows {
		compression := row.compression
		if compression == leapmuxv1.ContentCompression_CONTENT_COMPRESSION_UNSPECIFIED {
			compression = leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE
		}
		_, err := createMessageRow(t.Context(), svc.Queries, db.CreateMessageParams{
			ID: row.id, AgentID: agentID, AgentSessionID: row.agentSessionID,
			Source:  row.source,
			Content: row.content, ContentCompression: compression,
			SupplementalContent: row.supplement, SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			AssembledKind: row.assembledKind,
			CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
		})
		require.NoError(t, err, row.id)
	}
}

// listReplyTexts lists every row of agentID and returns the reply text of
// each row by its id, with nil for a row that carries none.
func listReplyTexts(t *testing.T, d *testServiceDispatcher, agentID string, include bool) map[string]*string {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID, Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, Limit: 50, IncludeReplyText: include,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var listed leapmuxv1.ListAgentMessagesResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &listed))
	texts := make(map[string]*string, len(listed.GetMessages()))
	for _, message := range listed.GetMessages() {
		texts[message.GetId()] = message.ReplyText
	}
	return texts
}

// assembledRow is the assembled envelope of a finished row of kind.
func assembledRow(t *testing.T, kind, text string) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{
		contracts.AssembledMessageFieldType:       contracts.AssembledMessageType,
		contracts.AssembledMessageFieldKind:       kind,
		contracts.AssembledMessageFieldText:       text,
		contracts.AssembledMessageFieldCompletion: contracts.AssembledMessageCompletionComplete,
	})
	require.NoError(t, err)
	return encoded
}

// TestListAgentMessagesReadsTheReplyTextOnRequest pins the reply text that
// `leapmux control agent send --wait` and transcript readers print. The Worker
// fills it only when the request asks, for agent reply rows and neutral user
// prompt rows; a row whose bytes do not decode still lists, with no reply.
func TestListAgentMessagesReadsTheReplyTextOnRequest(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, d, _ := setupTestService(t)
	const agentID = "claude-agent"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	text := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_TEXT
	reasoning := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_REASONING
	compressed, compression := msgcodec.Compress(assembledRow(t, "text", "The tests pass."))
	storeReplyTextRows(t, svc, agentID, []replyTextRow{
		{id: "user", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content: []byte(`{"content":"  Run the tests.  "}`)},
		{id: "user-text-field", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content: []byte(`{"text":"Follow up."}`)},
		{id: "user-typed", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content: []byte(`{"type":"user","content":"Provider echo."}`)},
		{id: "leapmux", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content: []byte(`{"content":"Platform notice."}`)},
		{id: "reasoning", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: assembledRow(t, "reasoning", "Thinking."), assembledKind: &reasoning},
		{id: "reply", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: compressed, compression: compression, assembledKind: &text},
		{id: "native", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`)},
		{id: "blank", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: assembledRow(t, "text", "  "), assembledKind: &text},
		{id: "corrupt", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: []byte("not zstd"), compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD},
		{id: "user-text", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content: assembledRow(t, "text", "Not the agent's."), assembledKind: &text},
	})

	replies := listReplyTexts(t, d, agentID, true)
	require.Len(t, replies, 10, "every row lists, a corrupt one too")
	require.NotNil(t, replies["user"])
	assert.Equal(t, "Run the tests.", *replies["user"], "a neutral user content envelope")
	require.NotNil(t, replies["user-text-field"])
	assert.Equal(t, "Follow up.", *replies["user-text-field"], "a neutral user text envelope")
	require.NotNil(t, replies["reply"])
	assert.Equal(t, "The tests pass.", *replies["reply"], "a compressed assembled reply")
	require.NotNil(t, replies["native"])
	assert.Equal(t, "Done.", *replies["native"], "a native reply that the provider reads")
	for _, id := range []string{"user-typed", "leapmux", "reasoning", "blank", "corrupt", "user-text"} {
		assert.Nil(t, replies[id], "row %s holds no reply", id)
	}

	for id, reply := range listReplyTexts(t, d, agentID, false) {
		assert.Nil(t, reply, "row %s carries no reply text unless the request asks", id)
	}
}

// TestListAgentMessagesReadsTheReplyTextThroughTheSupplementAndTheSession
// covers the two inputs beside the content: the rendering supplement, which
// selects the part of a fast-agent archive row, and the row's session, which
// the archive must belong to.
func TestListAgentMessagesReadsTheReplyTextThroughTheSupplementAndTheSession(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, d, _ := setupTestService(t)
	const agentID = "fastagent-agent"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT,
	}))
	archive := func(field, channel string) []byte {
		meta := `{"format":"fast-agent/history-element/v1","captureId":"cap-1","sessionId":"fa-session","actor":"researcher",` +
			`"historyFile":"history_researcher.json","messageIndex":1,"role":"assistant","field":"` + field + `","index":0`
		if channel != "" {
			meta += `,"channel":"` + channel + `"`
		}
		supplement, err := agent.EncodeMessageSupplement(agent.MessageContent{Supplemental: []byte(`{"fast-agent/root-archive":` + meta + `}}`)})
		require.NoError(t, err)
		return supplement
	}
	block := []byte(`{"type":"text","text":"The research is done."}`)
	storeReplyTextRows(t, svc, agentID, []replyTextRow{
		{id: "content", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: block, supplement: archive("content", ""), agentSessionID: "fa-session"},
		{id: "reasoning", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: block, supplement: archive("channels", "reasoning"), agentSessionID: "fa-session"},
		{id: "foreign", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: block, supplement: archive("content", ""), agentSessionID: "other-session"},
		{id: "bare", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: block, agentSessionID: "fa-session"},
	})

	replies := listReplyTexts(t, d, agentID, true)
	require.Len(t, replies, 4)
	require.NotNil(t, replies["content"])
	assert.Equal(t, "The research is done.", *replies["content"])
	assert.Nil(t, replies["reasoning"], "the supplement selects the reasoning channel")
	assert.Nil(t, replies["foreign"], "the archive belongs to another session")
	assert.Nil(t, replies["bare"], "a text block with no archive supplement is an ACP frame")
}

func TestGetAgentMessageIncludesReplyTextOnRequest(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, d, _ := setupTestService(t)
	const agentID = "claude-get-msg"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	textKind := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_TEXT
	compressed, compression := msgcodec.Compress(assembledRow(t, "text", "Single reply."))
	userSeq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "user-1", AgentID: agentID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content: []byte(`{"content":"Single prompt."}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	agentSeq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "agent-1", AgentID: agentID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content: compressed, ContentCompression: compression, AssembledKind: &textKind,
		CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	typedUserSeq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "user-typed-1", AgentID: agentID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content: []byte(`{"type":"user","content":"Typed echo."}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	corruptUserSeq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "user-corrupt-1", AgentID: agentID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content: []byte("not zstd"), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD,
		CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)

	fetch := func(seq int64, include bool) *leapmuxv1.AgentChatMessage {
		w := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerAgentServiceGetAgentMessageProcedure, &leapmuxv1.GetAgentMessageRequest{
			AgentId: agentID, Seq: seq, IncludeReplyText: include,
		}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
		var resp leapmuxv1.GetAgentMessageResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		return resp.GetMessage()
	}

	userWith := fetch(userSeq, true)
	require.NotNil(t, userWith.ReplyText)
	assert.Equal(t, "Single prompt.", userWith.GetReplyText())
	assert.Nil(t, fetch(userSeq, false).ReplyText)

	agentWith := fetch(agentSeq, true)
	require.NotNil(t, agentWith.ReplyText)
	assert.Equal(t, "Single reply.", agentWith.GetReplyText())
	assert.Nil(t, fetch(agentSeq, false).ReplyText)

	typedUser := fetch(typedUserSeq, true)
	require.NotNil(t, typedUser)
	assert.Nil(t, typedUser.ReplyText, "typed user envelopes must not yield reply text")

	corruptUser := fetch(corruptUserSeq, true)
	require.NotNil(t, corruptUser)
	assert.Nil(t, corruptUser.ReplyText, "corrupt compressed user payload must return the row with nil reply text")
}

func TestGetAgentSpanMessagesIncludesReplyTextOnRequest(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, d, _ := setupTestService(t)
	const agentID = "claude-span-msg"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	textKind := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_TEXT
	reasoningKind := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_REASONING
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "span-user", AgentID: agentID, SpanID: "span-1", Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content: []byte(`{"content":"Span prompt."}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "span-agent", AgentID: agentID, SpanID: "span-1", Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content: assembledRow(t, "text", "Span reply."), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AssembledKind: &textKind, CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "span-reasoning", AgentID: agentID, SpanID: "span-1", Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content: assembledRow(t, "reasoning", "Thinking."), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AssembledKind: &reasoningKind, CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "span-user-typed", AgentID: agentID, SpanID: "span-1", Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content: []byte(`{"type":"user","content":"Typed span echo."}`), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "span-corrupt", AgentID: agentID, SpanID: "span-1", Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content: []byte("not zstd"), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD,
		CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)

	fetchSpan := func(include bool) map[string]*string {
		w := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerAgentServiceGetAgentSpanMessagesProcedure, &leapmuxv1.GetAgentSpanMessagesRequest{
			AgentId: agentID, SpanId: "span-1", IncludeReplyText: include,
		}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
		var resp leapmuxv1.GetAgentSpanMessagesResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		out := make(map[string]*string, len(resp.GetMessages()))
		for _, m := range resp.GetMessages() {
			out[m.GetId()] = m.ReplyText
		}
		return out
	}

	withReply := fetchSpan(true)
	require.Len(t, withReply, 5)
	require.NotNil(t, withReply["span-user"])
	assert.Equal(t, "Span prompt.", *withReply["span-user"])
	require.NotNil(t, withReply["span-agent"])
	assert.Equal(t, "Span reply.", *withReply["span-agent"])
	assert.Nil(t, withReply["span-reasoning"])
	assert.Nil(t, withReply["span-user-typed"])
	assert.Nil(t, withReply["span-corrupt"])

	for id, reply := range fetchSpan(false) {
		assert.Nil(t, reply, "span row %s carries no reply text unless requested", id)
	}
}

func TestWatchEventsIncludesReplyTextOnReplayAndLiveBroadcast(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, d, _ := setupTestService(t)
	const agentID = "claude-watch-reply"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	textKind := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_TEXT
	reasoningKind := leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_REASONING
	storeReplyTextRows(t, svc, agentID, []replyTextRow{
		{id: "replay-user", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content: []byte(`{"content":"Replay prompt."}`)},
		{id: "replay-agent", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: assembledRow(t, "text", "Replay answer."), assembledKind: &textKind},
		{id: "replay-reasoning", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: assembledRow(t, "reasoning", "Replay thinking."), assembledKind: &reasoningKind},
	})

	optedIn := &testResponseWriter{channelID: "ch-cli"}
	defaultWatcher := &testResponseWriter{channelID: "ch-browser"}
	dispatch(d, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, &leapmuxv1.WatchEventsRequest{
		UpdateId: 1,
		Agents: []*leapmuxv1.WatchAgentEntry{{
			AgentId:          agentID,
			Mode:             leapmuxv1.WatchMode_WATCH_MODE_FULL,
			ReplayId:         1,
			Replay:           leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST,
			IncludeReplyText: true,
		}},
	}, optedIn)
	dispatch(d, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, &leapmuxv1.WatchEventsRequest{
		UpdateId: 1,
		Agents: []*leapmuxv1.WatchAgentEntry{{
			AgentId:          agentID,
			Mode:             leapmuxv1.WatchMode_WATCH_MODE_FULL,
			ReplayId:         1,
			Replay:           leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST,
			IncludeReplyText: false,
		}},
	}, defaultWatcher)

	testutil.AssertEventually(t, func() bool {
		return countCatchUpCompletes(optedIn) == 1 && countCatchUpCompletes(defaultWatcher) == 1
	}, "both watchers must complete catch-up replay")

	collectMessages := func(w *testResponseWriter) map[string]*leapmuxv1.AgentChatMessage {
		out := make(map[string]*leapmuxv1.AgentChatMessage)
		for _, event := range decodeAgentEvents(w) {
			if msg := event.GetAgentMessage(); msg != nil {
				out[msg.GetId()] = msg
			}
		}
		return out
	}

	replayedOpted := collectMessages(optedIn)
	require.NotNil(t, replayedOpted["replay-user"].ReplyText)
	assert.Equal(t, "Replay prompt.", replayedOpted["replay-user"].GetReplyText())
	require.NotNil(t, replayedOpted["replay-agent"].ReplyText)
	assert.Equal(t, "Replay answer.", replayedOpted["replay-agent"].GetReplyText())
	assert.Nil(t, replayedOpted["replay-reasoning"].ReplyText)

	for id, msg := range collectMessages(defaultWatcher) {
		assert.Nil(t, msg.ReplyText, "browser watcher must receive nil reply_text on replayed row %s", id)
	}

	// Publish live user, agent reply, and reasoning messages through OutputHandler.
	require.NoError(t, svc.Output.persistAndBroadcast(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: []byte(`{"content":"Live prompt."}`)}, agent.SpanInfo{}, nil))
	require.NoError(t, svc.Output.persistAndBroadcast(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: assembledRow(t, "text", "Live answer.")}, agent.SpanInfo{}, nil))
	require.NoError(t, svc.Output.persistAndBroadcast(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: assembledRow(t, "reasoning", "Live thinking.")}, agent.SpanInfo{}, nil))

	testutil.AssertEventually(t, func() bool {
		return len(collectMessages(optedIn)) == 6 && len(collectMessages(defaultWatcher)) == 6
	}, "both watchers must receive all three live messages")

	var liveOptedTexts []string
	for _, event := range decodeAgentEvents(optedIn) {
		msg := event.GetAgentMessage()
		if msg == nil || msg.GetSeq() <= 3 {
			continue
		}
		if msg.ReplyText != nil {
			liveOptedTexts = append(liveOptedTexts, msg.GetReplyText())
		}
	}
	assert.Equal(t, []string{"Live prompt.", "Live answer."}, liveOptedTexts)

	for id, msg := range collectMessages(defaultWatcher) {
		assert.Nil(t, msg.ReplyText, "browser watcher must receive nil reply_text on live row %s", id)
	}
}

func TestReplyTextOfMessage_CompressedSupplementAndCorruptPayloads(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	supplement, err := agent.EncodeMessageSupplement(agent.MessageContent{
		Supplemental: []byte(`{"fast-agent/root-archive":{"format":"fast-agent/history-element/v1","captureId":"cap-1","sessionId":"fa-1","actor":"researcher","historyFile":"history_researcher.json","messageIndex":1,"role":"assistant","field":"content","index":0}}`),
	})
	require.NoError(t, err)
	compSupp, compSuppMode := msgcodec.Compress(supplement)

	got := svc.replyTextOfMessage(&leapmuxv1.AgentChatMessage{
		Id:                             "live-fa-valid",
		AgentProvider:                  leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT,
		AgentSessionId:                 "fa-1",
		Source:                         leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:                        []byte(`{"type":"text","text":"Compressed supplement reply."}`),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContent:            compSupp,
		SupplementalContentCompression: compSuppMode,
	})
	require.NotNil(t, got)
	assert.Equal(t, "Compressed supplement reply.", *got)

	assert.Nil(t, svc.replyTextOfMessage(&leapmuxv1.AgentChatMessage{
		Id:                             "live-fa-corrupt-supp",
		AgentProvider:                  leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT,
		AgentSessionId:                 "fa-1",
		Source:                         leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:                        []byte(`{"type":"text","text":"Unreachable."}`),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContent:            []byte("not zstd"),
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD,
	}), "corrupt compressed supplement must return nil reply text")

	assert.Nil(t, svc.replyTextOfMessage(&leapmuxv1.AgentChatMessage{
		Id:                 "live-user-typed",
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:            []byte(`{"type":"assembled_message","content":"Typed user payload."}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	}), "typed user payload must return nil reply text")

	assert.Nil(t, svc.replyTextOfMessage(&leapmuxv1.AgentChatMessage{
		Id:                 "live-user-corrupt",
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:            []byte("not zstd"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD,
	}), "corrupt compressed user payload must return nil reply text")
}
