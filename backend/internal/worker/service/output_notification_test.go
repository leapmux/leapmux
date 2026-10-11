package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type storedNotificationEntry struct {
	IdempotencyKey    string `json:"idempotency_key"`
	FingerprintBase64 string `json:"fingerprint_base64"`
}

func TestNotificationAdmissionRejectsAnUnknownWriteModeBeforeEffects(t *testing.T) {
	t.Parallel()
	svc, services := setupRootSink(t, "unknown-notification-mode")
	sink := requireRootOutputSink(t, svc.Output, "unknown-notification-mode")
	services.UpdateSessionID("native-session")
	before, err := svc.Queries.GetAgentByID(t.Context(), sink.agentID)
	require.NoError(t, err)
	for _, mode := range []notificationWriteMode{0, 255} {
		content := services.CaptureMessage(agent.MessageContent{Original: []byte(`{"type":"system","text":"unchanged"}`), IdempotencyKey: "same-key", WriteReceipt: agent.NewTranscriptWriteReceipt()}, agent.SpanInfo{})
		stored, err := svc.Output.writeNotification(sink.agentID, sink.agentProvider, sink.plugin, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, mode)
		assert.ErrorContains(t, err, "write mode")
		assert.False(t, stored)
		_, committed := content.WriteReceipt.StoredMessageSequence()
		assert.False(t, committed)
	}
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sink.agentID})
	require.NoError(t, err)
	assert.Empty(t, rows)
	after, err := svc.Queries.GetAgentByID(t.Context(), sink.agentID)
	require.NoError(t, err)
	assert.Equal(t, before.MessageSeqHwm, after.MessageSeqHwm)
	stored, err := services.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"system","text":"unchanged"}`), IdempotencyKey: "same-key"})
	require.NoError(t, err)
	assert.True(t, stored)
}

func TestNotificationAdmissionRejectsAnExplicitUnknownCompletionBeforeEffects(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"message", "divider", "notification"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			svc, delegate, agentID, _ := setupBgTaskTestWithService(t)
			sink := agent.NewModelProgressResetSink(delegate)
			sink.UpdateSessionID("native-session")
			sink.ReportProgress(agent.NativeTokenProgress("current-model", 17))
			output := requireRootOutputSink(t, svc.Output, agentID)
			stopCapturedProgressTimers(output.progress)
			beforeProgress := output.progress.snapshotInfo()
			beforeRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
			require.NoError(t, err)
			beforeAgent, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			beforeTodos, err := svc.Output.LoadTodos(t.Context(), agentID)
			require.NoError(t, err)
			writer := &testResponseWriter{channelID: "unknown-completion-admission"}
			registerAgentWatch(svc, writer.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
			original, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "exact native bytes", agent.MessageCompletionComplete)
			require.NoError(t, err)
			receipt := agent.NewTranscriptWriteReceipt()
			content := agent.MessageContent{Original: original, IdempotencyKey: "native-key", Completion: "unknown-explicit-completion", WriteReceipt: receipt}
			persist := func(content agent.MessageContent) error {
				switch operation {
				case "message":
					return sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
				case "divider":
					return sink.PersistTurnEnd(content, agent.SpanInfo{})
				default:
					broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
					if content.Completion == "unknown-explicit-completion" {
						assert.False(t, broadcast)
					}
					return err
				}
			}
			err = persist(content)
			assert.ErrorContains(t, err, "unknown completion")
			afterRows, queryErr := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
			require.NoError(t, queryErr)
			assert.Equal(t, beforeRows, afterRows)
			afterAgent, queryErr := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, queryErr)
			assert.Equal(t, beforeAgent.MessageSeqHwm, afterAgent.MessageSeqHwm)
			afterTodos, queryErr := svc.Output.LoadTodos(t.Context(), agentID)
			require.NoError(t, queryErr)
			assert.Equal(t, beforeTodos, afterTodos)
			assert.Equal(t, beforeProgress, output.progress.snapshotInfo())
			assert.Empty(t, writer.streamsSnapshot(), "refused admission produces no stored row, divider or progress frame")
			_, stored := receipt.StoredMessageSequence()
			assert.False(t, stored)
			assert.False(t, receipt.ClaimModelReset())
			assert.False(t, receipt.ClaimSourceObservation())
			assert.False(t, receipt.ClaimContextUsage())
			assert.False(t, receipt.ClaimOutputCompletion())
			if err != nil {
				content.Completion = agent.MessageCompletionComplete
				content.WriteReceipt = agent.NewTranscriptWriteReceipt()
				require.NoError(t, persist(content))
				rows, queryErr := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
				require.NoError(t, queryErr)
				assert.Len(t, rows, len(beforeRows)+1, "refused admission must not claim the native key")
			}
		})
	}
}

func TestNotificationHistoricalObservationWithoutAReferenceKeepsTheEarlierAggregate(t *testing.T) {
	t.Parallel()
	svc, sink, agentID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("native-session")
	first := agent.MessageContent{Original: []byte(`{"type":"system","text":"first current notice"}`), IdempotencyKey: "first-current"}
	_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, first)
	require.NoError(t, err)
	before, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
	require.NoError(t, err)
	require.Len(t, before, 1)
	replacement := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	replacement.UpdateSessionID("native-session")
	replacement.ReportProgress(agent.NativeTokenProgress("current-model", 17))
	current := requireRootOutputSink(t, svc.Output, agentID)
	stopCapturedProgressTimers(current.progress)
	progress := current.progress.snapshotInfo()
	beforeTodos, err := svc.Output.LoadTodos(t.Context(), agentID)
	require.NoError(t, err)
	writer := &testResponseWriter{channelID: "new-historical-notice"}
	registerAgentWatch(svc, writer.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	fresh := sink.CaptureMessage(agent.MessageContent{Original: []byte(`{"type":"system","text":"new historical notice"}`), IdempotencyKey: "new-historical"}, agent.SpanInfo{})
	owner := fresh.Publication.Owner().(*transcriptOwner)
	assert.Nil(t, owner.thread.reference)
	assert.False(t, owner.IsCurrent())
	_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, fresh)
	require.NoError(t, err)
	after, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
	require.NoError(t, err)
	require.Len(t, after, 2)
	assert.Equal(t, before[0], after[0], "the new historical observation owns no earlier aggregate")
	assert.True(t, after[1].TranscriptOnly)
	assert.Equal(t, "native-session", after[1].AgentSessionID)
	entries := readStoredNotificationEntries(t, after[1])
	require.Len(t, entries, 1)
	assertNotificationEntryIdentity(t, fresh, after[1], entries[0])
	assert.Equal(t, progress, current.progress.snapshotInfo())
	afterTodos, err := svc.Output.LoadTodos(t.Context(), agentID)
	require.NoError(t, err)
	assert.Equal(t, beforeTodos, afterTodos)
	for _, stream := range writer.streamsSnapshot() {
		event := decodeWatchAgentEvent(t, stream)
		assert.Nil(t, event.GetTurnEnd())
		assert.Nil(t, event.GetTodosChanged())
		if message := event.GetAgentMessage(); message != nil {
			assert.Equal(t, after[1].ID, message.Id)
			assert.True(t, message.TranscriptOnly)
		}
	}
}

func readStoredNotificationEntries(t *testing.T, row db.Message) []storedNotificationEntry {
	t.Helper()
	encoded, err := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
	require.NoError(t, err)
	if len(encoded) == 0 {
		return nil
	}
	var envelope struct {
		Metadata struct {
			Entries []storedNotificationEntry `json:"notification_entries"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	return envelope.Metadata.Entries
}

func notificationEntryCount(t *testing.T, svc *Service, agentID string) int {
	t.Helper()
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
	require.NoError(t, err)
	var count int
	for _, row := range rows {
		count += len(readStoredNotificationEntries(t, row))
	}
	return count
}

func notificationFingerprintReference(provider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, content agent.MessageContent) [sha256.Size]byte {
	_, completion := agent.MessageMetadata(content)
	var framed bytes.Buffer
	for _, ordinal := range []int32{int32(provider), int32(source), int32(completion)} {
		framed.Write(binary.BigEndian.AppendUint64(nil, 4))
		framed.Write(binary.BigEndian.AppendUint32(nil, uint32(ordinal)))
	}
	for _, part := range [][]byte{content.Original, content.Supplemental, content.Metadata} {
		framed.Write(binary.BigEndian.AppendUint64(nil, uint64(len(part))))
		framed.Write(part)
	}
	return sha256.Sum256(framed.Bytes())
}

func assertNotificationEntryIdentity(t *testing.T, expected agent.MessageContent, row db.Message, stored storedNotificationEntry) {
	t.Helper()
	assert.Equal(t, expected.IdempotencyKey, stored.IdempotencyKey)
	decoded, err := base64.StdEncoding.DecodeString(stored.FingerprintBase64)
	require.NoError(t, err)
	require.Len(t, decoded, sha256.Size)
	want := notificationFingerprintReference(row.AgentProvider, row.Source, expected)
	assert.Equal(t, want[:], decoded)
	assert.Equal(t, base64.StdEncoding.EncodeToString(decoded), stored.FingerprintBase64)
}

func TestNotificationEntryRetainsExactIdentityAndRenderingFields(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("native-session")
	entries := []agent.MessageContent{
		{Original: []byte(" {\"type\":\"system\",\"text\":\"한😀\"} \n"), IdempotencyKey: "native:0", Supplemental: []byte(" {\"native\":0} "), Metadata: []byte(" {\"duration_ms\":0} "), Completion: agent.MessageCompletionInterrupted},
		{Original: []byte(`{"type":"system","text":"second"}`), IdempotencyKey: "native:1"},
	}
	for attempt, index := range []int{0, 1, 0, 1} {
		broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, entries[index])
		require.NoError(t, err)
		assert.Equal(t, attempt < 2, broadcast)
	}
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "native-session", rows[0].AgentSessionID)
	stored := readStoredNotificationEntries(t, rows[0])
	require.Len(t, stored, len(entries))
	for index, entry := range stored {
		assertNotificationEntryIdentity(t, entries[index], rows[0], entry)
	}
	assert.False(t, rows[0].TranscriptOnly)
	assert.Nil(t, rows[0].Completion, "the latest accepted entry supplies no completion")
}

func TestNotificationEntryFailureRollsBackTheAggregateAndSequence(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "append"}[existing], func(t *testing.T) {
			svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
			sink.UpdateSessionID("native-session")
			if existing {
				_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					agent.MessageContent{Original: []byte(`{"type":"system","text":"first"}`), IdempotencyKey: "first"})
				require.NoError(t, err)
			}
			before, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
			require.NoError(t, err)
			agentBefore, err := svc.Queries.GetAgentByID(t.Context(), ownerID)
			require.NoError(t, err)
			entriesBefore := notificationEntryCount(t, svc, ownerID)
			statement := `CREATE TRIGGER refuse_notification_entry BEFORE INSERT ON messages
				BEGIN SELECT RAISE(FAIL, 'The store refused the entry'); END`
			if existing {
				statement = `CREATE TRIGGER refuse_notification_entry BEFORE UPDATE OF supplemental_content ON messages
					BEGIN SELECT RAISE(FAIL, 'The store refused the entry'); END`
			}
			_, err = svc.DB.ExecContext(t.Context(), statement)
			require.NoError(t, err)
			broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: []byte(`{"type":"system","text":"second"}`), IdempotencyKey: "second"})
			require.ErrorContains(t, err, "The store refused the entry")
			assert.False(t, broadcast)
			after, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Equal(t, entriesBefore, notificationEntryCount(t, svc, ownerID))
			agentAfter, err := svc.Queries.GetAgentByID(t.Context(), ownerID)
			require.NoError(t, err)
			assert.Equal(t, agentBefore.MessageSeqHwm, agentAfter.MessageSeqHwm)
			_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_notification_entry")
			require.NoError(t, err)
			_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: []byte(`{"type":"system","text":"second"}`), IdempotencyKey: "second"})
			require.NoError(t, err)
			assert.Equal(t, entriesBefore+1, notificationEntryCount(t, svc, ownerID))
		})
	}
}

func TestNotificationEntryConflictingKeyPreservesTheOriginalRecord(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"original", "supplemental", "metadata", "completion", "source", "provider"} {
		t.Run(field, func(t *testing.T) {
			svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
			sink.UpdateSessionID("native-session")
			content := agent.MessageContent{Original: []byte(`{"type":"system","text":"original"}`), IdempotencyKey: "native-key"}
			_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
			require.NoError(t, err)
			before, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
			require.NoError(t, err)
			source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
			switch field {
			case "original":
				content.Original = []byte(`{"type":"system","text":"replacement"}`)
			case "supplemental":
				content.Supplemental = []byte(`{"value":0}`)
			case "metadata":
				content.Metadata = []byte(`{"duration_ms":0}`)
			case "completion":
				content.Completion = agent.MessageCompletionComplete
			case "source":
				source = leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX
			case "provider":
				sink = svc.Output.NewSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
			}
			broadcast, err := sink.PersistNotification(source, content)
			require.ErrorContains(t, err, "identity conflict")
			assert.False(t, broadcast)
			after, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Equal(t, 1, notificationEntryCount(t, svc, ownerID))
		})
	}
}

func TestNotificationEntryConcurrentDuplicatesProduceOneReceiptAndBroadcast(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("native-session")
	writer := &agentMessageCapturingWriter{channelID: "concurrent-notification-entry"}
	registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: []byte(`{"type":"system","text":"one native record"}`), IdempotencyKey: "native:0"})
			assert.NoError(t, err)
		})
	}
	group.Wait()
	assert.Equal(t, 1, notificationEntryCount(t, svc, ownerID))
	assert.Len(t, writer.snapshot(), 1)
}

func TestEmbeddedNotificationJournalRetainsOlderAggregateKeys(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("native")
	first := agent.MessageContent{Original: []byte(`{"type":"system","text":"first"}`), IdempotencyKey: "first"}
	_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, first)
	require.NoError(t, err)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"assistant","text":"between"}`)}, agent.SpanInfo{}))
	_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"system","text":"second"}`), IdempotencyKey: "second"})
	require.NoError(t, err)
	before, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	broadcast, err := agent.CaptureTranscript(sink, first, agent.SpanInfo{}).PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	require.NoError(t, err)
	assert.False(t, broadcast)
	after, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, 2, notificationEntryCount(t, svc, ownerID))
}

func TestEmbeddedNotificationJournalSourceOnlyAppendKeepsSequenceAndProgress(t *testing.T) {
	t.Parallel()
	svc, _, ownerID, _ := setupBgTaskTestWithService(t)
	sink := agent.NewModelProgressResetSink(svc.Output.NewSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
	sink.UpdateSessionID("native")
	content := agent.MessageContent{Original: raw(t, codexStartupStatus("codex_apps", "ready", nil)), IdempotencyKey: "first"}
	_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
	require.NoError(t, err)
	before, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	sink.ReportProgress(agent.NativeTokenProgress("current-model", 17))
	sink.ReportProgress(agent.OutputDeltaProgress("current-tool", 8))
	content.IdempotencyKey = "second"
	broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
	require.NoError(t, err)
	assert.False(t, broadcast)
	after, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, before.Seq, after.Seq)
	assert.Equal(t, before.Content, after.Content)
	assert.Equal(t, before.SupplementalRevision, after.SupplementalRevision)
	stored := readStoredNotificationEntries(t, after)
	require.Len(t, stored, 2)
	assert.Equal(t, "second", stored[1].IdempotencyKey)
	info := requireRootOutputSink(t, svc.Output, ownerID).progress.snapshotInfo()
	assert.Equal(t, int64(17), info[contracts.SessionInfoKeyThinkingTokens])
	assert.Equal(t, int64(8), info[contracts.SessionInfoKeyOutputBytes])
	broadcast, err = agent.CaptureTranscript(sink, content, agent.SpanInfo{}).PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	require.NoError(t, err)
	assert.False(t, broadcast)
	assert.Equal(t, 2, notificationEntryCount(t, svc, ownerID))
}

func TestEmbeddedNotificationJournalPreservesUnrelatedSupplementData(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	entries := []agent.MessageContent{
		{Original: []byte(`{"type":"system","text":"first"}`), IdempotencyKey: "first", Supplemental: []byte(`{"left":1}`), Metadata: []byte(`{"duration_ms":0,"unknown_worker_field":{"value":1}}`)},
		{Original: []byte(`{"type":"system","text":"second"}`), IdempotencyKey: "second", Supplemental: []byte(`{"right":2}`), Metadata: []byte(`{"num_tool_uses":0}`)},
	}
	for _, content := range entries {
		_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
		require.NoError(t, err)
	}
	row, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	supplement, err := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
	require.NoError(t, err)
	var fields map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(supplement, &fields))
	assert.JSONEq(t, `1`, string(fields["provider"]["left"]))
	assert.JSONEq(t, `2`, string(fields["provider"]["right"]))
	assert.JSONEq(t, `0`, string(fields["metadata"]["duration_ms"]))
	assert.JSONEq(t, `0`, string(fields["metadata"]["num_tool_uses"]))
	assert.JSONEq(t, `{"value":1}`, string(fields["metadata"]["unknown_worker_field"]))
	stored := readStoredNotificationEntries(t, row)
	require.Len(t, stored, 2)
	for index, entry := range stored {
		assertNotificationEntryIdentity(t, entries[index], row, entry)
	}
}

func TestEmbeddedNotificationJournalRejectsReservedWorkerMetadata(t *testing.T) {
	t.Parallel()
	for _, notification := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary message", true: "notification"}[notification], func(t *testing.T) {
			t.Parallel()
			svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
			content := agent.MessageContent{Original: []byte(`{"type":"system"}`), Metadata: []byte(`{"notification_entries":[]}`)}
			var err error
			if notification {
				_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
			} else {
				err = sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
			}
			require.ErrorContains(t, err, "notification_entries")
			rows, readErr := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
			require.NoError(t, readErr)
			assert.Empty(t, rows)
		})
	}
}

func TestEmbeddedNotificationJournalValidAppendAfterAnInvalidOriginal(t *testing.T) {
	t.Parallel()
	for _, original := range []struct {
		label string
		bytes []byte
	}{
		{"empty", nil}, {"invalid JSON", []byte(`{"invalid":`)},
		{"invalid UTF-8", append(append([]byte(`{"text":"`), 0xff), []byte(`"}`)...)},
	} {
		t.Run(original.label, func(t *testing.T) {
			t.Parallel()
			svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
			first := agent.MessageContent{Original: original.bytes, IdempotencyKey: "first"}
			_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, first)
			require.NoError(t, err)
			second := agent.MessageContent{Original: []byte(`{"type":"system","text":"valid"}`), IdempotencyKey: "second"}
			_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, second)
			require.NoError(t, err)
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
			require.Len(t, wrapper.Messages, 1)
			assert.JSONEq(t, string(second.Original), string(wrapper.Messages[0]))
			assert.Equal(t, []int64{1}, wrapper.OldSeqs)
			stored := readStoredNotificationEntries(t, rows[0])
			require.Len(t, stored, 2)
			assertNotificationEntryIdentity(t, first, rows[0], stored[0])
			assertNotificationEntryIdentity(t, second, rows[0], stored[1])
		})
	}
}

func TestNotificationReplayDoesNotChangeAggregateProvenance(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("native")
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	content := agent.MessageContent{Original: []byte(`{"type":"system","text":"original"}`), IdempotencyKey: "native-key"}
	first := agent.CaptureTranscript(sink, content, agent.SpanInfo{})
	_, err := first.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	require.NoError(t, err)
	before, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	sameOwner := sink.CaptureMessage(content, agent.SpanInfo{})
	sink.SetTurnState(agent.TurnState{}, 2)
	sink.SetTurnState(agent.TurnState{Active: true}, 3)
	broadcast, err := agent.CaptureTranscript(sink, sameOwner, agent.SpanInfo{}).PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	require.NoError(t, err)
	assert.False(t, broadcast)
	row, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	stored := readStoredNotificationEntries(t, row)
	require.Len(t, stored, 1)
	assertNotificationEntryIdentity(t, content, row, stored[0])
	assert.False(t, row.TranscriptOnly)
	assert.Equal(t, before, row)
}

func TestEmbeddedNotificationJournalUsesNoSecondaryStorageTable(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"system"}`), IdempotencyKey: "native-key"})
	require.NoError(t, err)
	var count int
	require.NoError(t, svc.DB.QueryRowContext(t.Context(),
		`SELECT count(*) FROM sqlite_master WHERE name IN ('notification_entries', 'idx_messages_notification_owner', 'idx_notification_entries_idempotency_key', 'idx_notification_entries_message_id')`).Scan(&count))
	assert.Zero(t, count)
	assert.Equal(t, 1, notificationEntryCount(t, svc, ownerID))
}

func TestNotificationEntryOriginalOuterSinkRetainsItsThreadAfterReplacement(t *testing.T) {
	t.Parallel()
	svc, originalSink, ownerID, _ := setupBgTaskTestWithService(t)
	originalSink.UpdateSessionID("original-session")
	_, err := originalSink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"system","text":"original first"}`), IdempotencyKey: "original-first"})
	require.NoError(t, err)
	originalNext := agent.CaptureTranscript(originalSink,
		agent.MessageContent{Original: []byte(`{"type":"system","text":"original second"}`), IdempotencyKey: "original-second"}, agent.SpanInfo{})
	replacement := svc.Output.NewSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	replacement.UpdateSessionID("replacement-session")
	_, err = replacement.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"system","text":"replacement first"}`), IdempotencyKey: "replacement-first"})
	require.NoError(t, err)
	_, err = originalNext.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	require.NoError(t, err, "the original outer writer must retain its exact destination after process replacement")
	_, err = replacement.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"system","text":"replacement second"}`), IdempotencyKey: "replacement-second"})
	require.NoError(t, err)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.Len(t, decodeNotifWrapper(t, row.Content, row.ContentCompression).Messages, 2)
		assert.Equal(t, row.AgentSessionID == "original-session", row.TranscriptOnly)
	}
}
