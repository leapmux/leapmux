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
// `leapmux control agent send --wait` prints. The Worker fills it only when
// the request asks, only for an agent's row, and only for a row that holds a
// reply; a row whose bytes do not decode still lists, with no reply.
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
		{id: "user", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content: []byte(`{"content":"Run the tests."}`)},
		{id: "reasoning", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: assembledRow(t, "reasoning", "Thinking."), assembledKind: &reasoning},
		{id: "reply", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: compressed, compression: compression, assembledKind: &text},
		{id: "native", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`)},
		{id: "blank", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: assembledRow(t, "text", "  "), assembledKind: &text},
		{id: "corrupt", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content: []byte("not zstd"), compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD},
		{id: "user-text", source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content: assembledRow(t, "text", "Not the agent's."), assembledKind: &text},
	})

	replies := listReplyTexts(t, d, agentID, true)
	require.Len(t, replies, 7, "every row lists, a corrupt one too")
	require.NotNil(t, replies["reply"])
	assert.Equal(t, "The tests pass.", *replies["reply"], "a compressed assembled reply")
	require.NotNil(t, replies["native"])
	assert.Equal(t, "Done.", *replies["native"], "a native reply that the provider reads")
	for _, id := range []string{"user", "reasoning", "blank", "corrupt", "user-text"} {
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
