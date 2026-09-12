package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestPersistMessageStoresOriginalAndSupplementTogether(t *testing.T) {
	t.Parallel()
	sink, writer := newGitStatusFixture(t)
	original := []byte(`{"provider_field": "unchanged", "_leapmux":{"completion":"complete"}}`)
	supplemental := []byte(`{"extra": "available at creation"}`)
	before := writer.count()
	messages := &agentMessageCapturingWriter{channelID: "supplemental-watch"}
	sink.h.watcher.agents.setWatches(messages.channelID, []watchEntry{{id: sink.agentID, mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, messages)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original: original, Supplemental: supplemental, Completion: agent.MessageCompletionInterrupted,
	}, agent.SpanInfo{SpanID: "call"}))
	row, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	storedOriginal, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	require.NoError(t, err)
	storedSupplemental, err := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
	require.NoError(t, err)
	assert.Equal(t, original, storedOriginal)
	decodedSupplement, err := agent.DecodeMessageSupplement(storedOriginal, storedSupplemental)
	require.NoError(t, err)
	assert.JSONEq(t, string(supplemental), string(decodedSupplement.Supplemental))
	assert.Equal(t, int64(leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_INTERRUPTED), row.Completion)
	assert.Equal(t, int64(0), row.SupplementalRevision)
	assert.Equal(t, before+1, writer.count(), "the first broadcast must contain both sources")
	broadcasts := messages.snapshot()
	require.Len(t, broadcasts, 1)
	assert.True(t, proto.Equal(messageToProto(&row), broadcasts[0]), "the first broadcast must match the stored row")
}

func TestPairedToolUseLookupResolvesSupplementalInput(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	sink.agentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE
	original := []byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","toolName":"Read","inputOmitted":true}}`)
	supplemental := []byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","input":{"file_path":"/project/recovered.ts"}}}`)
	span := agent.SpanInfo{SpanID: "call", SpanType: "Read"}
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original, Supplemental: supplemental}, span))
	resolved := sink.h.pairedToolUseLookup(sink.agentID, span)()
	assert.JSONEq(t, `{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","toolName":"Read","inputOmitted":true,"input":{"file_path":"/project/recovered.ts"}}}`, string(resolved))
	row, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	stored, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	require.NoError(t, err)
	assert.Equal(t, original, stored)
}

func TestEnrichToolRequestBySequencePreservesLaterRows(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	sink.agentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE
	span := agent.SpanInfo{SpanID: "plan", SpanType: "ExitPlanMode"}
	original := []byte(` {"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"plan","toolName":"ExitPlanMode","inputOmitted":true}} `)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, span))
	request, err := sink.ReadToolRequest("plan")
	require.NoError(t, err)
	require.NotNil(t, request)
	later := []byte(`{"type":"tool.updated","payload":{"kind":"progress","toolCallId":"plan"}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: later}, span))
	supplement := []byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"plan","input":{"plan":"# Review this plan"}}}`)
	changed, err := sink.EnrichMessage(agent.MessageEnrichment{Seq: request.Seq, SpanID: "plan", OriginalContent: original, PreviousRevision: request.Revision, SupplementalContent: supplement})
	require.NoError(t, err)
	require.True(t, changed)
	reloaded, err := sink.ReadToolRequest("plan")
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, original, reloaded.Content.Original)
	assert.Equal(t, supplement, reloaded.Content.Supplemental)
	assert.Equal(t, int64(1), reloaded.Revision)
	last, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	decoded, err := msgcodec.Decompress(last.Content, last.ContentCompression)
	require.NoError(t, err)
	assert.Equal(t, later, decoded)
	lastSupplement, err := msgcodec.Decompress(last.SupplementalContent, last.SupplementalContentCompression)
	require.NoError(t, err)
	assert.Empty(t, lastSupplement)
	for _, invalid := range []agent.MessageEnrichment{
		{Seq: request.Seq, SpanID: "different", PreviousRevision: 1},
		{Seq: -1, SpanID: "plan", PreviousRevision: 1},
		{Seq: 999, SpanID: "plan", PreviousRevision: 1},
		{Seq: request.Seq, SpanID: "plan", PreviousRevision: 0},
	} {
		invalid.OriginalContent = original
		invalid.SupplementalContent = []byte(`{"changed":true}`)
		changed, err := sink.EnrichMessage(invalid)
		require.NoError(t, err)
		assert.False(t, changed)
	}
}

func TestEnrichMessagePreservesIdentityAndRejectsStaleContent(t *testing.T) {
	t.Parallel()
	sink, writer := newGitStatusFixture(t)
	original := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"completed","supplemental_content":{"provider_field":true},"_leapmux":"provider value"}`)
	enriched := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"completed","rawOutput":{"content":"recovered"}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "call", SpanType: "read", Closing: true}))
	before, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	broadcasts := writer.count()
	change := agent.MessageEnrichment{SpanID: "call", OriginalContent: original, SupplementalContent: enriched}
	updated, err := sink.EnrichMessage(change)
	require.NoError(t, err)
	require.True(t, updated)
	after, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	assert.Equal(t, before.ID, after.ID)
	assert.Equal(t, before.Seq, after.Seq)
	assert.Equal(t, before.CreatedAt, after.CreatedAt)
	assert.Equal(t, before.SpanLines, after.SpanLines)
	assert.Equal(t, before.Content, after.Content, "enrichment must preserve the original provider bytes")
	assert.Equal(t, before.ContentCompression, after.ContentCompression)
	assert.Equal(t, int64(1), after.SupplementalRevision)
	decoded, err := msgcodec.Decompress(after.Content, after.ContentCompression)
	require.NoError(t, err)
	assert.JSONEq(t, string(original), string(decoded))
	supplement, err := msgcodec.Decompress(after.SupplementalContent, after.SupplementalContentCompression)
	require.NoError(t, err)
	decodedSupplement, err := agent.DecodeMessageSupplement(decoded, supplement)
	require.NoError(t, err)
	assert.JSONEq(t, string(enriched), string(decodedSupplement.Supplemental))
	assert.Equal(t, broadcasts+1, writer.count())
	message := messageToProto(&after)
	assert.Equal(t, after.Content, message.Content)
	assert.Equal(t, after.SupplementalContent, message.SupplementalContent)

	for _, stale := range []agent.MessageEnrichment{
		{SpanID: "call", OriginalContent: original, SupplementalContent: []byte(`{"stale":true}`)},
		{SpanID: "call", OriginalContent: []byte(`{"other":true}`), PreviousRevision: 1, SupplementalContent: []byte(`{"stale":true}`)},
		{SpanID: "missing", OriginalContent: original, SupplementalContent: enriched},
		{OriginalContent: original, SupplementalContent: enriched},
	} {
		updated, err := sink.EnrichMessage(stale)
		require.NoError(t, err)
		assert.False(t, updated)
	}
	assert.Equal(t, broadcasts+1, writer.count(), "stale and absent records must not produce updates")
	unchanged, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	assert.Equal(t, after, unchanged)

	newSupplement := []byte(`{"complete":"second enrichment"}`)
	updated, err = sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "call", OriginalContent: original, PreviousRevision: 1, SupplementalContent: newSupplement,
	})
	require.NoError(t, err)
	assert.True(t, updated)
	latest, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	assert.Equal(t, before.Content, latest.Content)
	assert.Equal(t, int64(2), latest.SupplementalRevision)
	assert.Equal(t, broadcasts+2, writer.count())
}

func TestEnrichmentRetainsWorkerMetadata(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	original := []byte(` {"native":"unchanged"} `)
	metadata := []byte(`{"duration_ms":0,"future":9007199254740993}`)
	providerData := []byte(`{"metadata":{"duration_ms":99},"rawOutput":{"content":"Recovered"}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original, Metadata: metadata}, agent.SpanInfo{SpanID: "call", Closing: true}))
	updated, err := sink.EnrichMessage(agent.MessageEnrichment{SpanID: "call", OriginalContent: original, SupplementalContent: providerData})
	require.NoError(t, err)
	require.True(t, updated)
	row, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	stored, err := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
	require.NoError(t, err)
	decoded, err := agent.DecodeMessageSupplement(original, stored)
	require.NoError(t, err)
	assert.Equal(t, metadata, decoded.Metadata)
	assert.Equal(t, providerData, decoded.Supplemental)
	updated, err = sink.EnrichMessage(agent.MessageEnrichment{SpanID: "call", OriginalContent: original, PreviousRevision: 1, SupplementalContent: providerData})
	require.NoError(t, err)
	assert.False(t, updated, "the same provider data does not create another revision")
}

func TestInvalidSupplementDoesNotDiscardOriginalMessage(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	original := []byte(` {"native":"unchanged"} `)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original, Supplemental: []byte(`{invalid`)}, agent.SpanInfo{}))
	row, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	stored, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	require.NoError(t, err)
	assert.Equal(t, original, stored)
}
