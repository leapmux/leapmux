package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestCapturedEnrichmentProviderPreparationCanReplaceItsPublisher(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"resolve", "extract", "paired"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			providerID := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
			provider := &reentrantTranscriptProvider{Provider: testRegistry.Plugin(providerID), stage: stage}
			svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, providerID, provider)))
			const ownerID = "enrichment-preparation-owner"
			require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{ID: ownerID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: providerID}))
			sink := svc.Output.NewSink(ownerID, providerID)
			sink.UpdateSessionID("original-session")
			sink.SetTurnState(agent.TurnState{Active: true}, 1)
			require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: []byte(`{"text":"paired-request"}`)}, agent.SpanInfo{SpanID: "paired-span"}))
			span := agent.SpanInfo{SpanID: "paired-span", Closing: true}
			content := sink.CaptureMessage(agent.MessageContent{Original: []byte(`{"text":"retained native bytes"}`)}, span)
			require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span))
			callbackResult := make(chan error, 1)
			replacementFinished := make(chan struct{})
			var once sync.Once
			provider.reenter = func() {
				invoke := false
				once.Do(func() { invoke = true })
				if !invoke {
					return
				}
				go func() {
					replacement := svc.Output.NewSink(ownerID, providerID)
					svc.Output.NoteAgentProcessStarted(ownerID)
					replacement.SetTurnState(agent.TurnState{Active: true}, 1)
					close(replacementFinished)
				}()
				select {
				case <-replacementFinished:
					callbackResult <- nil
				case <-time.After(30 * time.Second):
					callbackResult <- errors.New("enrichment preparation holds the root mutation lease across replacement")
				}
			}
			writer := &testResponseWriter{channelID: "enrichment-preparation-watcher"}
			registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
			provider.armed.Store(true)
			receipt := agent.NewMessageEnrichmentReceipt()
			written, err := sink.EnrichMessage(agent.MessageEnrichment{Publication: content.Publication, AgentSessionID: content.AgentSessionID,
				SpanID: span.SpanID, OriginalContent: content.Original, SupplementalContent: []byte(`{"native_enrichment":true}`), WriteReceipt: receipt})
			require.NoError(t, err)
			require.True(t, written)
			select {
			case <-replacementFinished:
			case <-ctx.Done():
				t.Fatal("the enrichment replacement did not finish after preparation returned")
			}
			assert.NoError(t, <-callbackResult)
			_, revision, supplemental, committed := receipt.CommittedEnrichment()
			assert.True(t, committed)
			assert.Equal(t, int64(1), revision)
			assert.Equal(t, []byte(`{"native_enrichment":true}`), supplemental)
			var enrichedMessages int
			for _, stream := range writer.streamsSnapshot() {
				message := decodeWatchAgentEvent(t, stream).GetAgentMessage()
				if message != nil && message.GetSupplementalRevision() == 1 {
					enrichedMessages++
					assert.True(t, message.GetTranscriptOnly(), "expired enrichment must publish as historical rendering")
				}
			}
			assert.Equal(t, 1, enrichedMessages)
		})
	}
}

func TestEnrichmentReceiptPrecedesWatcherPublication(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "enrichment-receipt-owner")
	original := []byte(`{"native":"result"}`)
	span := agent.SpanInfo{SpanID: "call", Closing: true}
	content := sink.CaptureMessage(agent.MessageContent{Original: original}, span)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span))
	receipt := agent.NewMessageEnrichmentReceipt()
	var inspected int
	writer := &turnAdmissionWatchingWriter{testResponseWriter: &testResponseWriter{channelID: "enrichment-receipt-watch"}}
	writer.onEvent = func(event *leapmuxv1.AgentEvent) {
		if message := event.GetAgentMessage(); message != nil && message.SupplementalRevision == 1 {
			inspected++
			previous, revision, supplemental, committed := receipt.CommittedEnrichment()
			assert.True(t, committed)
			assert.Zero(t, previous)
			assert.Equal(t, message.SupplementalRevision, revision)
			assert.Equal(t, []byte(`{"overlay":0}`), supplemental)
		}
	}
	registerAgentWatch(svc, writer.channelID, "enrichment-receipt-owner", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	written, err := sink.EnrichMessage(agent.MessageEnrichment{Publication: content.Publication, AgentSessionID: content.AgentSessionID,
		SpanID: span.SpanID, OriginalContent: original, SupplementalContent: []byte(`{"overlay":0}`), WriteReceipt: receipt})
	require.NoError(t, err)
	assert.True(t, written)
	assert.Equal(t, 1, inspected)
}

// countingDBTX counts the single-row queries that run through it, by the sqlc name
// that opens each statement (`-- name: GetAgentMessageBySpanIDAndSource :one`).
//
// Only QueryRowContext is wrapped, because the read this file counts is a `:one`
// query. A test that needs another shape wraps the method that carries it.
type countingDBTX struct {
	db.DBTX
	mu     sync.Mutex
	counts map[string]int
}

func (c *countingDBTX) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	if name, found := strings.CutPrefix(query, "-- name: "); found {
		if end := strings.IndexByte(name, ' '); end > 0 {
			c.mu.Lock()
			if c.counts == nil {
				c.counts = make(map[string]int)
			}
			c.counts[name[:end]]++
			c.mu.Unlock()
		}
	}
	return c.DBTX.QueryRowContext(ctx, query, args...)
}

func (c *countingDBTX) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[name]
}

func (c *countingDBTX) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.counts)
}

func TestPersistMessageStoresOriginalAndSupplementTogether(t *testing.T) {
	t.Parallel()
	sink, writer := newGitStatusFixture(t)
	sink.UpdateSessionID("provider-session")
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
	assert.Equal(t, "provider-session", row.AgentSessionID)
	decodedSupplement, err := agent.DecodeMessageSupplement(storedOriginal, storedSupplemental)
	require.NoError(t, err)
	assert.JSONEq(t, string(supplemental), string(decodedSupplement.Supplemental))
	assert.Equal(t, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_INTERRUPTED, workerdb.StorageEnumValue(row.Completion))
	assert.Equal(t, int64(0), row.SupplementalRevision)
	assert.Equal(t, before+1, writer.count(), "the first broadcast must contain both sources")
	broadcasts := messages.snapshot()
	require.Len(t, broadcasts, 1)
	message, err := messageToProto(&row)
	require.NoError(t, err)
	assert.True(t, proto.Equal(message, broadcasts[0]), "the first broadcast must match the stored row")
}

func TestPairedToolUseLookupResolvesSupplementalInput(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	sink.agentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE
	original := []byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","toolName":"Read","inputOmitted":true}}`)
	supplemental := []byte(`{"type":"tool.updated","payload":{"kind":"scheduled","toolCallId":"call","input":{"file_path":"/project/recovered.ts"}}}`)
	span := agent.SpanInfo{SpanID: "call", SpanType: "Read"}
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original, Supplemental: supplemental}, span))
	resolved := sink.h.pairedToolUseLookup(sink.agentID, sink.currentMessageSessionID(), span)()
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
	message, err := messageToProto(&after)
	require.NoError(t, err)
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

// The stored supplement carries two halves, and an enrichment replaces one of
// them. The provider payload arrives again with the next change; the worker's
// metadata half does not. So a supplement that does not read is a REFUSAL: a
// replacement would write the new payload beside a nil metadata half and destroy
// the cost, the duration, the tool count and the context usage for good.
func TestUnreadableSupplementRefusesTheEnrichment(t *testing.T) {
	t.Parallel()
	sink, writer := newGitStatusFixture(t)
	original := []byte(` {"native":"unchanged"} `)
	metadata := []byte(`{"duration_ms":1234,"tool_uses":7,"total_cost_usd":0.42}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original, Metadata: metadata}, agent.SpanInfo{SpanID: "call", Closing: true}))
	row, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)

	// Damage the stored blob in place: it decompresses, and then fails to decode.
	damaged := []byte(`{"provider":`)
	corrupted, err := sink.h.queries.EnrichMessageContent(t.Context(), db.EnrichMessageContentParams{
		ID: row.ID, AgentID: sink.agentID,
		OriginalContent: row.Content, OriginalCompression: row.ContentCompression,
		PreviousRevision:    row.SupplementalRevision,
		SupplementalContent: damaged, SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		PreviousSupplementalContent:            row.SupplementalContent,
		PreviousSupplementalContentCompression: row.SupplementalContentCompression,
	})
	require.NoError(t, err)
	broadcasts := writer.count()

	updated, err := sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "call", OriginalContent: original, PreviousRevision: corrupted.SupplementalRevision,
		SupplementalContent: []byte(`{"rawOutput":{"content":"Recovered"}}`),
	})
	require.NoError(t, err, "a damaged supplement is a refusal, not a failure the caller retries")
	assert.False(t, updated)

	after, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
	require.NoError(t, err)
	assert.Equal(t, corrupted, after, "the row must stay exactly as it was, so a repair is still possible")
	assert.Equal(t, broadcasts, writer.count(), "a refused enrichment broadcasts nothing")
}

func TestNotificationEnrichmentPreservesPreparationErrorClasses(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		label       string
		supplement  []byte
		compression leapmuxv1.ContentCompression
		private     bool
	}{
		{label: "unreadable compression", supplement: []byte("invalid zstd"), compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD},
		{label: "malformed envelope", supplement: []byte(`{"provider":`), compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE},
		{label: "corrupt journal", supplement: []byte(`{"metadata":{"notification_entries":null}}`), compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, private: true},
		{label: "corrupt reduction", supplement: []byte(`{"metadata":{"notification_reduction":{"provider_slots":{"group":0}}}}`), compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, private: true},
		{label: "shadowed private journal", supplement: []byte(`{"metadata":{"notification_entries":[]},"metadata":{"duration_ms":0}}`), compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, private: true},
		{label: "repeated private journal", supplement: []byte(`{"metadata":{"notification_entries":null,"notification_entries":[]}}`), compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, private: true},
		{label: "shadowed private reduction", supplement: []byte(`{"metadata":{"notification_reduction":{}},"metadata":{}}`), compression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, private: true},
	} {
		t.Run(scenario.label, func(t *testing.T) {
			t.Parallel()
			sink, writer := newGitStatusFixture(t)
			original := []byte(`{"native":"unchanged"}`)
			require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original, Metadata: []byte(`{"duration_ms":0}`)}, agent.SpanInfo{SpanID: "call"}))
			row, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
			require.NoError(t, err)
			corrupted, err := sink.h.queries.EnrichMessageContent(t.Context(), db.EnrichMessageContentParams{
				ID: row.ID, AgentID: sink.agentID, OriginalContent: row.Content, OriginalCompression: row.ContentCompression,
				PreviousRevision: row.SupplementalRevision, PreviousSupplementalContent: row.SupplementalContent, PreviousSupplementalContentCompression: row.SupplementalContentCompression,
				SupplementalContent: scenario.supplement, SupplementalContentCompression: scenario.compression,
			})
			require.NoError(t, err)
			broadcasts := writer.count()
			receipt := agent.NewMessageEnrichmentReceipt()
			changed, err := sink.EnrichMessage(agent.MessageEnrichment{SpanID: "call", OriginalContent: original, PreviousRevision: corrupted.SupplementalRevision, SupplementalContent: []byte(`{"replacement":true}`), WriteReceipt: receipt})
			if scenario.private {
				assert.ErrorIs(t, err, agent.ErrInvalidNotificationStorage)
			} else {
				require.NoError(t, err)
			}
			assert.False(t, changed)
			_, _, _, committed := receipt.CommittedEnrichment()
			assert.False(t, committed)
			after, err := sink.h.queries.GetLatestMessageByAgentID(t.Context(), sink.agentID)
			require.NoError(t, err)
			assert.Equal(t, corrupted, after)
			assert.Equal(t, broadcasts, writer.count())
		})
	}
}

// ReadToolRequest resolves the FIRST row of a span and ReadToolResult the LAST, so
// the two answer different questions. A tool call writes an opener and then a result,
// and a supplement built from the result belongs on the second of them -- reading the
// opener there would enrich the row that states what the call ASKED for.
func TestReadToolRequestAndReadToolResultAnswerTheTwoEndsOfASpan(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	sink.UpdateSessionID("provider-session")
	opener := []byte(`{"sessionUpdate":"tool_call","toolCallId":"call","status":"pending"}`)
	result := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"completed"}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: opener}, agent.SpanInfo{SpanID: "call"}))
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: result}, agent.SpanInfo{SpanID: "call", Closing: true}))

	request, err := sink.ReadToolRequest("call")
	require.NoError(t, err)
	require.NotNil(t, request)
	assert.Equal(t, opener, request.Content.Original)

	last, err := sink.ReadToolResult("call")
	require.NoError(t, err)
	require.NotNil(t, last)
	assert.Equal(t, result, last.Content.Original)
	assert.Greater(t, last.Seq, request.Seq, "the result is the later row")
}

// A span with one row is its own opener AND its own result. A tool call that arrives
// already final is the reachable case: a `session/load` replay sends one frame.
func TestReadToolResultAnswersTheOnlyRowOfASingleFrameSpan(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	sink.UpdateSessionID("provider-session")
	only := []byte(`{"sessionUpdate":"tool_call","toolCallId":"solo","status":"completed"}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: only}, agent.SpanInfo{SpanID: "solo", Closing: true}))

	last, err := sink.ReadToolResult("solo")
	require.NoError(t, err)
	require.NotNil(t, last)
	assert.Equal(t, only, last.Content.Original)
}

// A span with no row and an empty id are the two answers that mean "nothing to read".
// Neither is an error: the caller builds no supplement and moves on.
func TestReadToolResultAnswersNothingForASpanWithNoRow(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	sink.UpdateSessionID("provider-session")

	missing, err := sink.ReadToolResult("never-persisted")
	require.NoError(t, err)
	assert.Nil(t, missing)

	empty, err := sink.ReadToolResult("")
	require.NoError(t, err)
	assert.Nil(t, empty)
}

// ONE enrichment reads the paired tool_use ONCE.
//
// The enrichment path extracts twice on purpose: it compares what the new supplement
// yields against what the row had ALREADY yielded. The paired tool_use behind those
// extractions is a database read, so the two share one memo -- and the apply that
// follows takes the event they produced rather than the bytes, which would build a
// memo of its own and read the row a second time.
func TestEnrichmentReadsThePairedToolUseOnce(t *testing.T) {
	t.Parallel()
	sink, _ := newGitStatusFixture(t)
	createUse := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskCreate","input":{"subject":"Original"}}]}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: createUse}, agent.SpanInfo{SpanID: "create-1", SpanType: "TaskCreate"}))
	createResult := []byte(`{"type":"user","message":{"content":[]},"tool_use_result":{"task":{"id":"1","subject":"Original"}}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		agent.MessageContent{Original: createResult}, agent.SpanInfo{SpanID: "create-1", SpanType: "TaskCreate"}))
	counter := &countingDBTX{DBTX: sink.h.db}
	sink.h.queries = db.New(counter)

	span := agent.SpanInfo{SpanID: "task-1", SpanType: "TaskUpdate"}
	use := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskUpdate",` +
		`"input":{"taskId":"1","subject":"Renamed"}}]}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: use}, span))
	result := []byte(`{"type":"user","message":{"content":[]},` +
		`"tool_use_result":{"success":true,"taskId":"1","statusChange":{"from":"pending","to":"in_progress"}}}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: result}, span))
	// The persist path extracts once of its own. This test is about the enrichment.
	counter.reset()

	written, err := sink.EnrichMessage(agent.MessageEnrichment{
		SpanID: "task-1", OriginalContent: result,
		SupplementalContent: []byte(`{"recovered":true}`),
	})
	require.NoError(t, err)
	require.True(t, written)
	assert.Equal(t, 1, counter.count("GetAgentMessageBySpanIDAndSource"),
		"both extractions of one enrichment share one paired read")
}

func TestNotificationEnrichmentCASRejectsAConcurrentInvisibleJournalAppend(t *testing.T) {
	t.Parallel()
	svc, _, ownerID, _ := setupBgTaskTestWithService(t)
	sink := svc.Output.NewSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	payload := raw(t, codexStartupStatus("codex_apps", "ready", nil))
	_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: payload, IdempotencyKey: "first"})
	require.NoError(t, err)
	before, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	previous, err := msgcodec.Decompress(before.SupplementalContent, before.SupplementalContentCompression)
	require.NoError(t, err)
	fields := make(map[string]json.RawMessage)
	if len(previous) > 0 {
		require.NoError(t, json.Unmarshal(previous, &fields))
	}
	fields["provider"] = json.RawMessage(`{"enriched":true}`)
	next, err := json.Marshal(fields)
	require.NoError(t, err)
	compressed, compression := msgcodec.Compress(next)
	broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: payload, IdempotencyKey: "second"})
	require.NoError(t, err)
	assert.False(t, broadcast)
	appended, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, before.Content, appended.Content)
	assert.Equal(t, before.Seq, appended.Seq)
	assert.Equal(t, before.SupplementalRevision, appended.SupplementalRevision)
	_, err = svc.Queries.EnrichMessageContent(t.Context(), db.EnrichMessageContentParams{
		ID: before.ID, AgentID: ownerID, OriginalContent: before.Content, OriginalCompression: before.ContentCompression,
		PreviousRevision: before.SupplementalRevision, SupplementalContent: compressed, SupplementalContentCompression: compression,
		PreviousSupplementalContent:            before.SupplementalContent,
		PreviousSupplementalContentCompression: before.SupplementalContentCompression,
	})
	require.ErrorIs(t, err, sql.ErrNoRows, "the prior supplement must not overwrite a journal append at the same revision")
	after, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, appended, after)
	assert.Len(t, readStoredNotificationEntries(t, after), 2)
}

func TestCapturedEnrichmentUsesItsOriginalSessionAfterReplacement(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	original := []byte(`{"type":"tool_result","text":"same native bytes"}`)
	span := agent.SpanInfo{SpanID: "call", Closing: true}
	sink.UpdateSessionID("original-session")
	captured := sink.CaptureMessage(agent.MessageContent{Original: original}, span)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, captured, span))
	sink.UpdateSessionID("replacement-session")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, span))
	updated, err := sink.EnrichMessage(agent.MessageEnrichment{
		Publication: captured.Publication, AgentSessionID: captured.AgentSessionID,
		SpanID: "call", OriginalContent: original, SupplementalContent: []byte(`{"original_result":true}`),
	})
	require.NoError(t, err)
	require.True(t, updated)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for index, row := range rows {
		encoded, err := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
		require.NoError(t, err)
		decoded, err := agent.DecodeMessageSupplement(original, encoded)
		require.NoError(t, err)
		if index == 0 {
			assert.JSONEq(t, `{"original_result":true}`, string(decoded.Supplemental))
		} else {
			assert.Empty(t, decoded.Supplemental, "a captured enrichment must not update an identical span in a replacement session")
		}
	}
}
