package service

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mutationLogTodoProvider struct{ agent.Provider }

func (provider mutationLogTodoProvider) ResolveProviderData(content agent.MessageContent) []byte {
	if len(content.Supplemental) != 0 {
		return bytes.Clone(content.Supplemental)
	}
	return bytes.Clone(content.Original)
}

func (provider mutationLogTodoProvider) ExtractTodoEvent(_ string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	var payload struct {
		CreateTask bool `json:"create_task"`
	}
	if json.Unmarshal(content, &payload) != nil || !payload.CreateTask {
		return todoevents.Event{}, false
	}
	return todoevents.Event{Kind: todoevents.KindCreate, Item: todoevents.Item{ID: "from-supplement", Content: "Create the task", Status: todoevents.StatusPending}}, true
}

func TestEnrichmentFailureLogReleasesTheMutationBeforeCallbacks(t *testing.T) {
	const agentID = "enrichment-log-owner"
	providerID := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	provider := mutationLogTodoProvider{Provider: testRegistry.Plugin(providerID)}
	svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, providerID, provider)))
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: providerID}))
	services := svc.Output.NewSink(agentID, providerID)
	services.UpdateSessionID("original-native-session")
	sink := requireRootOutputSink(t, svc.Output, agentID)
	original := []byte(`{"native_result":true}`)
	span := agent.SpanInfo{SpanID: "original-call", Closing: true}
	content := services.CaptureMessage(agent.MessageContent{Original: original}, span)
	require.NoError(t, services.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span))
	before, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), agentID)
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_enrichment_todo BEFORE INSERT ON agent_todos
WHEN NEW.agent_id = 'enrichment-log-owner' BEGIN SELECT RAISE(ABORT, 'to-do insert refused'); END`)
	require.NoError(t, err)
	receipt := agent.NewMessageEnrichmentReceipt()
	supplement := []byte(`{"create_task":true,"retained_overlay":0}`)
	writer := &testResponseWriter{channelID: "enrichment-log-wire"}
	registerAgentWatch(svc, writer.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	svc.Output.WaitActivityRefreshes()
	mutation := svc.Output.transcriptMutationMutex(agentID)
	var logs int
	var mutexFree bool
	var replacement *agentOutputSink
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
		if record.Message != "apply todo event from an enrichment" || record.Level != slog.LevelWarn {
			return
		}
		belongs := false
		record.Attrs(func(attribute slog.Attr) bool {
			belongs = belongs || attribute.Key == "agent_id" && attribute.Value.String() == agentID
			return true
		})
		if !belongs {
			return
		}
		logs++
		previous, revision, committedSupplement, committed := receipt.CommittedEnrichment()
		assert.True(t, committed, "the accepted CAS must fix its receipt before the failure log")
		assert.Equal(t, before.SupplementalRevision, previous)
		assert.Equal(t, before.SupplementalRevision+1, revision)
		assert.Equal(t, supplement, committedSupplement)
		mutexFree = mutation.TryLock()
		if !mutexFree {
			return
		}
		mutation.Unlock()
		svc.Output.NewSink(agentID, providerID)
		replacement = requireRootOutputSink(t, svc.Output, agentID)
	}}))
	defer slog.SetDefault(previousLogger)
	written, err := services.EnrichMessage(agent.MessageEnrichment{Publication: content.Publication, AgentSessionID: content.AgentSessionID,
		Seq: before.Seq, SpanID: span.SpanID, OriginalContent: original, PreviousRevision: before.SupplementalRevision,
		SupplementalContent: supplement, WriteReceipt: receipt})
	require.NoError(t, err, "a to-do failure must retain the accepted enrichment result")
	assert.True(t, written)
	assert.Equal(t, 1, logs)
	assert.True(t, mutexFree, "the enrichment log must release the root mutation before its handler")
	after, err := svc.Queries.GetMessageByAgentIDAndSeq(t.Context(), db.GetMessageByAgentIDAndSeqParams{AgentID: agentID, Seq: before.Seq})
	require.NoError(t, err)
	assert.Equal(t, before.Content, after.Content)
	assert.Equal(t, before.ContentCompression, after.ContentCompression)
	assert.Equal(t, before.AgentSessionID, after.AgentSessionID)
	assert.Equal(t, before.SupplementalRevision+1, after.SupplementalRevision)
	encoded, err := msgcodec.Decompress(after.SupplementalContent, after.SupplementalContentCompression)
	require.NoError(t, err)
	stored, err := agent.DecodeMessageSupplement(original, encoded)
	require.NoError(t, err)
	assert.JSONEq(t, string(supplement), string(stored.Supplemental))
	rows, err := svc.Queries.ListAgentTodosNewestFirst(t.Context(), db.ListAgentTodosNewestFirstParams{AgentID: agentID, Limit: 100})
	require.NoError(t, err)
	assert.Empty(t, rows)
	var enriched int
	for _, event := range decodeAgentEvents(writer) {
		if message := event.GetAgentMessage(); message != nil && message.GetId() == before.ID {
			enriched++
			assert.Equal(t, after.SupplementalRevision, message.GetSupplementalRevision())
			decoded, err := msgcodec.Decompress(message.Content, message.ContentCompression)
			require.NoError(t, err)
			assert.Equal(t, original, decoded)
		}
	}
	assert.Equal(t, 1, enriched)
	if assert.NotNil(t, replacement, "the actual logger callback must replace the root synchronously") {
		assert.NotSame(t, sink, replacement)
		assert.Equal(t, "original-native-session", replacement.currentMessageSessionID())
	}
}
