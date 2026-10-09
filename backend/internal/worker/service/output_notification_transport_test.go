package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type encryptedNotificationTransport struct {
	writer channel.ResponseWriter
	mu     sync.Mutex
	events []*leapmuxv1.AgentEvent
}

func (transport *encryptedNotificationTransport) snapshot() []*leapmuxv1.AgentEvent {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]*leapmuxv1.AgentEvent(nil), transport.events...)
}

func newEncryptedNotificationTransport(t *testing.T) *encryptedNotificationTransport {
	t.Helper()
	keys, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	transport := &encryptedNotificationTransport{}
	var initiator *noiseutil.Session
	var fragments []byte
	send := func(request *leapmuxv1.ConnectRequest) error {
		frame := request.GetChannelMessageResp()
		if frame == nil {
			return nil
		}
		transport.mu.Lock()
		defer transport.mu.Unlock()
		if initiator == nil {
			return fmt.Errorf("the encrypted channel has no initiator session")
		}
		plain, err := initiator.Decrypt(frame.Ciphertext)
		if err != nil {
			return err
		}
		fragments = append(fragments, plain...)
		if frame.Flags == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE {
			return nil
		}
		var inner leapmuxv1.InnerMessage
		if err := proto.Unmarshal(fragments, &inner); err != nil {
			return err
		}
		fragments = nil
		if stream := inner.GetStream(); stream != nil && len(stream.Payload) > 0 {
			var response leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(stream.Payload, &response); err != nil {
				return err
			}
			if event := response.GetAgentEvent(); event != nil {
				transport.events = append(transport.events, event)
			}
		}
		return nil
	}
	manager := channel.NewManager(keys, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
		send, func(request *leapmuxv1.ConnectRequest) bool { return send(request) == nil }, contracts.MaxPlaintextPerChunk, 0)
	t.Cleanup(manager.CloseAll)
	handshake, first, err := noiseutil.ClassicalInitiatorHandshake1(keys.X25519Public)
	require.NoError(t, err)
	opened := manager.HandleOpen(&leapmuxv1.ChannelOpenRequest{ChannelId: "notification-transport", UserId: "user-1",
		HandshakePayload: first, MaxMessageSize: uint64(2 * contracts.MaxPlaintextPerChunk), GrantedScopes: testChannelGrant})
	require.Empty(t, opened.Error)
	initiator, err = noiseutil.ClassicalInitiatorHandshake2(handshake, opened.HandshakePayload)
	require.NoError(t, err)
	ready := make(chan channel.ResponseWriter, 1)
	dispatcher := channel.NewDispatcher()
	dispatcher.Register("CaptureNotificationWriter", func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
		ready <- writer
	})
	manager.SetDispatcher(dispatcher)
	encoded, err := proto.Marshal(&leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_Request{Request: &leapmuxv1.InnerRpcRequest{Method: "CaptureNotificationWriter"}}})
	require.NoError(t, err)
	ciphertext, err := initiator.Encrypt(encoded)
	require.NoError(t, err)
	manager.HandleMessage(&leapmuxv1.ChannelMessage{ProtocolVersion: 1, ChannelId: "notification-transport", CorrelationId: 1, Ciphertext: ciphertext})
	select {
	case transport.writer = <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("the channel did not supply its encrypted response writer")
	}
	require.Equal(t, contracts.MaxPlaintextPerChunk, transport.writer.MaxPayloadBudget())
	return transport
}

func notificationTransportOriginal(random *rand.Rand) []byte {
	data := make([]byte, 6*1024)
	for index := range data {
		data[index] = byte(random.Uint64())
	}
	original, _ := json.Marshal(map[string]any{"method": "mcpServer/startupStatus/updated",
		"params": map[string]any{"name": "same-server", "status": "ready"}, "native_bytes": base64.StdEncoding.EncodeToString(data)})
	return original
}

func notificationTransportEvent(content, supplement []byte) *leapmuxv1.AgentEvent {
	compressedContent, contentCompression := msgcodec.Compress(content)
	compressedSupplement, supplementCompression := compressOptionalSupplement(supplement)
	return &leapmuxv1.AgentEvent{AgentId: "transport-owner", Event: &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{
		Id: "transport-row", Seq: 1, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, AgentSessionId: "native",
		Content: compressedContent, ContentCompression: contentCompression,
		SupplementalContent: compressedSupplement, SupplementalContentCompression: supplementCompression}}}
}

func TestNotificationJournalTransportMeasuresCompactGrowthAndContinuesReplay(t *testing.T) {
	t.Parallel()
	transport := newEncryptedNotificationTransport(t)
	random := rand.New(rand.NewPCG(1, 2))
	var stored []byte
	var originals [][]byte
	var first, last *leapmuxv1.AgentEvent
	for index := range 40 {
		original := notificationTransportOriginal(random)
		originals = append(originals, original)
		content := agent.MessageContent{Original: original, IdempotencyKey: fmt.Sprintf("native:%d", index)}
		entry, err := agent.NewNotificationEntry(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
		require.NoError(t, err)
		stored, err = agent.AppendNotificationJournal(stored, agent.MessageContent{}, entry)
		require.NoError(t, err)
		rendering, err := agent.ProjectMessageSupplement(stored, 1)
		require.NoError(t, err)
		last = notificationTransportEvent(wrapNotifContent(original), rendering)
		if index == 0 {
			first = last
		}
	}
	send := func(event *leapmuxv1.AgentEvent) error {
		return broadcastWatchEvent(transport.writer, &leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: event}})
	}
	require.NoError(t, send(first))
	require.NoError(t, send(last))
	payload, err := marshalWatchEvent(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: last}}, "transport-owner")
	require.NoError(t, err)
	envelope, err := proto.Marshal(&leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_Stream{Stream: &leapmuxv1.InnerStreamMessage{Payload: payload}}})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(envelope), channelwire.MaxReassembledMessageSize(transport.writer.MaxPayloadBudget()))
	writeNotificationTransportReceipt(t, notificationTransportByteMetrics(t, last, payload, envelope, stored, originals, transport.writer.MaxPayloadBudget()))
	replay := newReplaySink(transport.writer, 9007199254740993)
	broadcastReplayAgentEvent(replay, "transport-owner", last)
	broadcastReplayAgentEvent(replay, "transport-owner", &leapmuxv1.AgentEvent{AgentId: "transport-owner", Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}}})
	assert.True(t, replay.alive())
	events := transport.snapshot()
	require.Len(t, events, 4)
	assert.NotNil(t, events[3].GetTurnEnd())
	assert.Equal(t, uint64(9007199254740993), events[2].GetReplayId())
	assert.Equal(t, uint64(9007199254740993), events[3].GetReplayId())
	assert.Equal(t, events[1].GetAgentMessage(), events[2].GetAgentMessage())
	for _, event := range events[:3] {
		rendering, err := msgcodec.Decompress(event.GetAgentMessage().SupplementalContent, event.GetAgentMessage().SupplementalContentCompression)
		require.NoError(t, err)
		assert.NotContains(t, string(rendering), agent.NotificationJournalField)
		assert.NotContains(t, string(rendering), agent.NotificationReductionField)
	}
}

func TestNotificationJournalTransportDeliversConsolidatedLiveAndReplay(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	const ownerID = "lossless-notification-owner"
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: ownerID,
		WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: provider}))
	sink := agent.NewModelProgressResetSink(svc.Output.NewSink(ownerID, provider))
	sink.UpdateSessionID("native")
	live := newEncryptedNotificationTransport(t)
	registerAgentWatch(svc, live.writer.ChannelID(), ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, live.writer)
	random := rand.New(rand.NewPCG(1, 2))
	inputs := make([]agent.MessageContent, 0, 40)
	for index := range 40 {
		content := agent.MessageContent{Original: notificationTransportOriginal(random), IdempotencyKey: fmt.Sprintf("native:%d", index)}
		inputs = append(inputs, content)
		broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
		require.NoError(t, err)
		assert.True(t, broadcast)
	}
	events := live.snapshot()
	messages := make([]*leapmuxv1.AgentChatMessage, 0, len(events))
	for _, event := range events {
		assert.False(t, event.Replay)
		assert.Zero(t, event.ReplayId)
		if message := event.GetAgentMessage(); message != nil && message.Seq > 0 {
			messages = append(messages, message)
		}
	}
	require.Len(t, messages, len(inputs))
	for index, message := range messages {
		wrapper := decodeNotifWrapper(t, message.Content, message.ContentCompression)
		require.Len(t, wrapper.Messages, 1)
		assert.JSONEq(t, string(inputs[index].Original), string(wrapper.Messages[0]))
		assert.Equal(t, int64(index+1), message.Seq)
		if index > 0 {
			assert.Equal(t, int64(index), message.PreviousSeq)
		}
		rendering, err := msgcodec.Decompress(message.SupplementalContent, message.SupplementalContentCompression)
		require.NoError(t, err)
		assert.NotContains(t, string(rendering), agent.NotificationJournalField)
		assert.NotContains(t, string(rendering), agent.NotificationReductionField)
	}
	before, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	identities := readStoredNotificationEntries(t, before)
	require.Len(t, identities, len(inputs))
	for index, content := range inputs {
		assertNotificationEntryIdentity(t, content, before, identities[index])
	}
	wrapper := decodeNotifWrapper(t, before.Content, before.ContentCompression)
	require.Len(t, wrapper.Messages, 1)
	assert.JSONEq(t, string(inputs[39].Original), string(wrapper.Messages[0]))
	assert.Len(t, wrapper.OldSeqs, 16)
	sink.ReportProgress(agent.NativeTokenProgress("current-model", 17))
	sink.ReportProgress(agent.OutputDeltaProgress("current-tool", 8))
	countStoredMessages := func(events []*leapmuxv1.AgentEvent) int {
		count := 0
		for _, event := range events {
			if message := event.GetAgentMessage(); message != nil && message.Seq > 0 {
				count++
			}
		}
		return count
	}
	stableEvents := countStoredMessages(live.snapshot())
	agentBefore, err := svc.Queries.GetAgentByID(t.Context(), ownerID)
	require.NoError(t, err)
	for _, content := range inputs {
		broadcast, err := agent.CaptureTranscript(sink, content, agent.SpanInfo{}).PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
		require.NoError(t, err)
		assert.False(t, broadcast)
	}
	after, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, stableEvents, countStoredMessages(live.snapshot()))
	agentAfter, err := svc.Queries.GetAgentByID(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Equal(t, agentBefore.MessageSeqHwm, agentAfter.MessageSeqHwm)
	for _, event := range live.snapshot() {
		assert.Nil(t, event.GetTurnEnd())
	}
	info := requireRootOutputSink(t, svc.Output, ownerID).progress.snapshotInfo()
	assert.Equal(t, int64(17), info[contracts.SessionInfoKeyThinkingTokens])
	assert.Equal(t, int64(8), info[contracts.SessionInfoKeyOutputBytes])
	divider := agent.MessageContent{Original: []byte(`{"type":"result","result":"complete"}`), IdempotencyKey: "turn-end", Completion: agent.MessageCompletionComplete}
	require.NoError(t, sink.PersistTurnEnd(divider, agent.SpanInfo{}))
	dividerRow, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
	require.NoError(t, err)
	assert.Greater(t, dividerRow.Seq, before.Seq)
	liveOrdered := live.snapshot()
	lastNotice, dividerIndex, completionIndex := -1, -1, -1
	for index, event := range liveOrdered {
		if message := event.GetAgentMessage(); message != nil {
			if message.Id == before.ID && message.Seq == before.Seq {
				lastNotice = index
			}
			if message.Id == dividerRow.ID {
				dividerIndex = index
				content, err := msgcodec.Decompress(message.Content, message.ContentCompression)
				require.NoError(t, err)
				assert.Equal(t, divider.Original, content)
			}
		}
		if event.GetTurnEnd() != nil {
			completionIndex = index
		}
	}
	require.NotEqual(t, -1, lastNotice)
	require.NotEqual(t, -1, dividerIndex)
	require.NotEqual(t, -1, completionIndex)
	assert.Less(t, lastNotice, dividerIndex)
	assert.Less(t, dividerIndex, completionIndex)
	replayed := newEncryptedNotificationTransport(t)
	row, err := svc.Queries.GetAgentByID(t.Context(), ownerID)
	require.NoError(t, err)
	const replayID = uint64(9007199254740993)
	svc.replayAgentCatchUp(newReplaySink(replayed.writer, replayID), &leapmuxv1.WatchAgentEntry{AgentId: ownerID}, row, nil)
	replayMessages := 0
	for _, event := range replayed.snapshot() {
		assert.True(t, event.Replay)
		assert.Equal(t, replayID, event.ReplayId)
		if message := event.GetAgentMessage(); message != nil && message.Seq > 0 {
			replayMessages++
			if replayMessages == 1 {
				assert.Equal(t, before.ID, message.Id)
				assert.Equal(t, before.Seq, message.Seq)
				wrapper := decodeNotifWrapper(t, message.Content, message.ContentCompression)
				require.Len(t, wrapper.Messages, 1)
				assert.JSONEq(t, string(inputs[39].Original), string(wrapper.Messages[0]))
			} else {
				assert.Equal(t, dividerRow.ID, message.Id)
				assert.Equal(t, dividerRow.Seq, message.Seq)
				content, err := msgcodec.Decompress(message.Content, message.ContentCompression)
				require.NoError(t, err)
				assert.Equal(t, divider.Original, content)
			}
			rendering, err := msgcodec.Decompress(message.SupplementalContent, message.SupplementalContentCompression)
			require.NoError(t, err)
			assert.NotContains(t, string(rendering), agent.NotificationJournalField)
			assert.NotContains(t, string(rendering), agent.NotificationReductionField)
		}
	}
	assert.Equal(t, 2, replayMessages)
	ordered := replayed.snapshot()
	require.NotEmpty(t, ordered)
	assert.NotNil(t, ordered[len(ordered)-1].GetCatchUpComplete())
}

func TestNotificationJournalTransportRejectsOversizedVisiblePayloadAndContinuesReplay(t *testing.T) {
	t.Parallel()
	transport := newEncryptedNotificationTransport(t)
	random := rand.New(rand.NewPCG(3, 4))
	var visible []json.RawMessage
	for range 40 {
		visible = append(visible, notificationTransportOriginal(random))
	}
	content, err := json.Marshal(notifThreadWrapper{Type: notifThreadWrapperType, Messages: visible})
	require.NoError(t, err)
	oversized := notificationTransportEvent(content, nil)
	response := &leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: oversized}}
	require.ErrorIs(t, broadcastWatchEvent(transport.writer, response), channel.ErrMessageRejected)
	replay := newReplaySink(transport.writer, 23)
	broadcastReplayAgentEvent(replay, "transport-owner", oversized)
	broadcastReplayAgentEvent(replay, "transport-owner", &leapmuxv1.AgentEvent{AgentId: "transport-owner", Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}}})
	assert.True(t, replay.alive())
	events := transport.snapshot()
	require.Len(t, events, 1)
	assert.NotNil(t, events[0].GetTurnEnd())
	assert.Equal(t, uint64(23), events[0].ReplayId)
}

// notificationTransportByteMetrics measures the exact stored sources and serialized envelopes.
// native_bytes supplies synthetic high-entropy data. Codex needs no such field for this notification.
func notificationTransportByteMetrics(t *testing.T, event *leapmuxv1.AgentEvent, payload, envelope, supplement []byte, originals [][]byte, budget int) map[string]any {
	t.Helper()
	message := event.GetAgentMessage()
	storedCompressed, storedCompression := compressOptionalSupplement(supplement)
	visible, err := msgcodec.Decompress(message.Content, message.ContentCompression)
	require.NoError(t, err)
	messageBytes, err := proto.Marshal(message)
	require.NoError(t, err)
	eventBytes, err := proto.Marshal(event)
	require.NoError(t, err)
	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(supplement, &stored))
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(stored[contracts.MessageSupplementFieldMetadata], &metadata))
	journal := metadata[agent.NotificationJournalField]
	var records []json.RawMessage
	require.NoError(t, json.Unmarshal(journal, &records))
	entries, err := agent.DecodeNotificationJournal(supplement)
	require.NoError(t, err)
	require.Len(t, entries, len(records))
	require.Len(t, originals, len(entries))
	entryMetrics := make([]map[string]any, 0, len(entries))
	var originalBytes, fingerprintBase64Bytes, entryBytes, syntheticBinaryBytes, syntheticBase64Bytes int
	for index, entry := range entries {
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(records[index], &fields))
		fieldMetrics := make(map[string]any, len(fields)+1)
		var serializedFields int
		for name, value := range fields {
			encodedName, err := json.Marshal(name)
			require.NoError(t, err)
			propertyBytes := len(encodedName) + 1 + len(value)
			serializedFields += propertyBytes
			counts := map[string]int{"json_property_bytes": propertyBytes, "json_value_bytes": len(value)}
			switch name {
			case "fingerprint_base64":
				var encoded string
				require.NoError(t, json.Unmarshal(value, &encoded))
				decoded, err := base64.StdEncoding.DecodeString(encoded)
				require.NoError(t, err)
				counts["base64_characters"] = len(encoded)
				counts["source_bytes"] = len(decoded)
				fingerprintBase64Bytes += len(encoded)
			case "idempotency_key":
				counts["source_bytes"] = len(entry.IdempotencyKey)
			}
			fieldMetrics[name] = counts
		}
		require.Equal(t, len(records[index]), serializedFields+len(fields)-1+2)
		var originalFields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(originals[index], &originalFields))
		var synthetic string
		require.NoError(t, json.Unmarshal(originalFields["native_bytes"], &synthetic))
		binary, err := base64.StdEncoding.DecodeString(synthetic)
		require.NoError(t, err)
		delete(originalFields, "native_bytes")
		protocolOriginal, err := json.Marshal(originalFields)
		require.NoError(t, err)
		entryMetrics = append(entryMetrics, map[string]any{
			"index": index, "key": entry.IdempotencyKey, "original_bytes": len(originals[index]),
			"canonical_entry_json_bytes": len(records[index]), "fields": fieldMetrics,
			"synthetic_native_binary_bytes": len(binary), "synthetic_native_base64_characters": len(synthetic),
			"protocol_original_without_synthetic_field_bytes": len(protocolOriginal),
		})
		originalBytes += len(originals[index])
		entryBytes += len(records[index])
		syntheticBinaryBytes += len(binary)
		syntheticBase64Bytes += len(synthetic)
	}
	projected := proto.Clone(event).(*leapmuxv1.AgentEvent)
	projectedMessage := projected.GetAgentMessage()
	rendering, err := agent.ProjectMessageSupplement(supplement, 1)
	require.NoError(t, err)
	projectedMessage.SupplementalContent = nil
	projectedMessage.SupplementalContentCompression = leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE
	if len(rendering) != 0 {
		projectedMessage.SupplementalContent, projectedMessage.SupplementalContentCompression = msgcodec.Compress(rendering)
	}
	projectedPayload, err := marshalWatchEvent(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: projected}}, "transport-owner")
	require.NoError(t, err)
	projectedEnvelope, err := proto.Marshal(&leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_Stream{Stream: &leapmuxv1.InnerStreamMessage{Payload: projectedPayload}}})
	require.NoError(t, err)
	lastOriginal := originals[len(originals)-1]
	var protocolFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(lastOriginal, &protocolFields))
	delete(protocolFields, "native_bytes")
	protocolOriginal, err := json.Marshal(protocolFields)
	require.NoError(t, err)
	protocolEvent := notificationTransportEvent(wrapNotifContent(protocolOriginal), nil)
	protocolEvent.GetAgentMessage().SupplementalContent = nil
	protocolEvent.GetAgentMessage().SupplementalContentCompression = leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE
	protocolPayload, err := marshalWatchEvent(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: protocolEvent}}, "transport-owner")
	require.NoError(t, err)
	protocolEnvelope, err := proto.Marshal(&leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_Stream{Stream: &leapmuxv1.InnerStreamMessage{Payload: protocolPayload}}})
	require.NoError(t, err)
	overhead := len(envelope) - len(message.Content) - len(message.SupplementalContent)
	return map[string]any{
		"entries": len(entries), "compressed_rendering_supplement": len(message.SupplementalContent),
		"compressed_stored_supplement_bytes": len(storedCompressed), "stored_supplement_compression": storedCompression.String(),
		"payload": len(payload), "inner_envelope": len(envelope), "negotiated_budget": budget,
		"reassembled_limit":                channelwire.MaxReassembledMessageSize(budget),
		"compressed_visible_content_bytes": len(message.Content), "uncompressed_visible_content_bytes": len(visible),
		"uncompressed_supplement_bytes": len(supplement), "uncompressed_journal_array_bytes": len(journal),
		"journal_outer_metadata_bytes": len(supplement) - len(journal), "entry_json_bytes_sum": entryBytes,
		"original_bytes_sum": originalBytes, "fingerprint_base64_characters_sum": fingerprintBase64Bytes,
		"synthetic_native_binary_bytes_sum": syntheticBinaryBytes, "synthetic_native_base64_characters_sum": syntheticBase64Bytes,
		"protobuf_overhead_bytes": overhead,
		"protobuf_overhead": map[string]int{
			"agent_chat_message": len(messageBytes) - len(message.Content) - len(message.SupplementalContent),
			"agent_event":        len(eventBytes) - len(messageBytes), "watch_events_response": len(payload) - len(eventBytes),
			"inner_message": len(envelope) - len(payload),
		},
		"rendering_supplement_without_journal_bytes": len(rendering), "projected_inner_envelope_bytes": len(projectedEnvelope),
		"protocol_only_visible_content_bytes": len(wrapNotifContent(protocolOriginal)), "protocol_only_inner_envelope_bytes": len(protocolEnvelope),
		"entry_fields": entryMetrics,
	}
}

func writeNotificationTransportReceipt(t *testing.T, metrics any) {
	t.Helper()
	_, source, _, valid := runtime.Caller(0)
	require.True(t, valid)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../../.."))
	directory := filepath.Join(root, ".tmp")
	require.NoError(t, os.MkdirAll(directory, 0700))
	encoded, err := json.MarshalIndent(metrics, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(directory, "notification-transport-measurement-"+strings.ReplaceAll(t.Name(), "/", "-")+".json")
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0600))
}
