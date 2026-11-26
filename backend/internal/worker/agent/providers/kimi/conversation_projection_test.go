package kimi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func persistConversationTestRecord(t *testing.T, writer *kimiConversationRecordWriter, record contracts.KimiConversationRecord, supplement []byte, span agent.SpanInfo) (*agent.TranscriptWriteReceipt, error) {
	t.Helper()
	raw, err := encodeKimiConversationRecord(record)
	require.NoError(t, err)
	view, err := decodeKimiConversationRecord(raw)
	require.NoError(t, err)
	var receipt *agent.TranscriptWriteReceipt
	err = agent.PersistCapturedTranscriptBatch(writer, func() []agent.CapturedTranscriptOperation {
		capture := agent.CaptureTranscript(writer, agent.MessageContent{Original: raw, AgentSessionID: record.SessionID, IdempotencyKey: view.key(), Supplemental: supplement}, span)
		receipt = capture.FrozenContent().WriteReceipt
		if record.Type == contracts.KimiConversationRecordKindTurn {
			return []agent.CapturedTranscriptOperation{capture.TurnEndOperation()}
		}
		return []agent.CapturedTranscriptOperation{capture.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)}
	})
	return receipt, err
}

func TestKimiConversationProjectionEnrichesOneTextRowAndPreservesItsAnchor(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	base := agent.NewProviderServices(sink)
	base.UpdateSessionID("native")
	writer, err := newKimiConversationRecordWriter(base)
	require.NoError(t, err)
	assert.Same(t, agent.OutputQueueFor(base), agent.OutputQueueFor(writer))
	record := contracts.KimiConversationRecord{
		Type: contracts.KimiConversationRecordKindItem, SessionID: "native", AgentID: "main", TurnID: "turn-1", Role: contracts.KimiConversationRecordRoleItem,
		Item: json.RawMessage(`{"type":"assistant_message","item_id":"answer-1","status":"running","created_at":"2026-11-18T00:00:00Z","text":"Prefix"}`),
	}
	_, err = persistConversationTestRecord(t, writer, record, []byte(`{"media":{"retained":true}}`), agent.SpanInfo{})
	require.NoError(t, err)
	first := sink.Messages()[0]
	record.Item = json.RawMessage(`{"type":"assistant_message","item_id":"answer-1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Authoritative answer"}`)
	receipt, err := persistConversationTestRecord(t, writer, record, nil, agent.SpanInfo{})
	require.NoError(t, err)
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, first.Content, rows[0].Content)
	assert.Equal(t, int64(1), rows[0].SupplementalRevision)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rows[0].SupplementalContent, &fields))
	assert.JSONEq(t, `{"retained":true}`, string(fields["media"]))
	latest, err := decodeKimiConversationRecord(fields[contracts.KimiConversationSupplementRoot])
	require.NoError(t, err)
	assert.Equal(t, "Authoritative answer", *latest.item.Text)
	seq, stored := receipt.StoredMessageSequence()
	assert.True(t, stored)
	assert.Equal(t, int64(1), seq)
	record.EventCreatedAt = "2026-11-18T00:01:00Z"
	_, err = persistConversationTestRecord(t, writer, record, nil, agent.SpanInfo{})
	require.NoError(t, err)
	assert.Len(t, sink.Messages(), 1)
	assert.Equal(t, int64(1), sink.Messages()[0].SupplementalRevision, "a new receipt timestamp changes no native data")
}

func TestKimiConversationProjectionCorrectsCompletionWithoutRepeatingTurnEffects(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	base := agent.NewProviderServices(sink)
	base.UpdateSessionID("native")
	writer, err := newKimiConversationRecordWriter(base)
	require.NoError(t, err)
	record := contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindTurn, SessionID: "native", AgentID: "main", TurnID: "turn-1",
		Turn: json.RawMessage(`{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}`)}
	raw, err := encodeKimiConversationRecord(record)
	require.NoError(t, err)
	view, err := decodeKimiConversationRecord(raw)
	require.NoError(t, err)
	for _, completion := range []agent.MessageCompletion{agent.MessageCompletionComplete, agent.MessageCompletionInterrupted} {
		err = agent.PersistCapturedTranscriptBatch(writer, func() []agent.CapturedTranscriptOperation {
			captured := agent.CaptureTranscript(writer, agent.MessageContent{Original: raw, AgentSessionID: "native", IdempotencyKey: view.key(), Completion: completion}, agent.SpanInfo{})
			return []agent.CapturedTranscriptOperation{captured.TurnEndOperation()}
		})
		require.NoError(t, err)
	}
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, raw, rows[0].Content)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0].Completion)
	assert.Equal(t, int64(1), rows[0].SupplementalRevision)
	completions := 0
	for _, event := range sink.TurnLifecycle() {
		if event == "turn_end" {
			completions++
		}
	}
	assert.Equal(t, 1, completions)
}

func TestKimiConversationProjectionKeepsRequestsImmutableAndCompletionEffectsSingular(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	base := agent.NewProviderServices(sink)
	base.UpdateSessionID("native")
	writer, err := newKimiConversationRecordWriter(base)
	require.NoError(t, err)
	request := contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindItem, SessionID: "native", AgentID: "main", TurnID: "turn-1",
		Role: contracts.KimiConversationRecordRoleRequest, Item: json.RawMessage(kimiConversationRecordTestTool)}
	_, err = persistConversationTestRecord(t, writer, request, nil, agent.SpanInfo{SpanID: "tool"})
	require.NoError(t, err)
	original := sink.Messages()[0].Content
	request.Item = json.RawMessage(strings.Replace(kimiConversationRecordTestTool, `"first"`, `"later output"`, 1))
	_, err = persistConversationTestRecord(t, writer, request, nil, agent.SpanInfo{SpanID: "tool"})
	require.NoError(t, err)
	assert.Equal(t, original, sink.Messages()[0].Content)
	assert.Empty(t, sink.Messages()[0].SupplementalContent)
	ended := contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindTurn, SessionID: "native", AgentID: "main", TurnID: "turn-1",
		Turn: json.RawMessage(`{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}`)}
	_, err = persistConversationTestRecord(t, writer, ended, nil, agent.SpanInfo{})
	require.NoError(t, err)
	ended.Turn = json.RawMessage(strings.Replace(string(ended.Turn), `"completed"`, `"failed"`, 1))
	_, err = persistConversationTestRecord(t, writer, ended, nil, agent.SpanInfo{})
	require.NoError(t, err)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[1].TurnEnd)
	assert.Equal(t, int64(1), rows[1].SupplementalRevision)
	var completions int
	for _, event := range sink.TurnLifecycle() {
		if event == "turn_end" {
			completions++
		}
	}
	assert.Equal(t, 1, completions)
}

type refuseConversationEnrichmentOnce struct {
	agent.ProviderServices
	refuse bool
}

func (sink *refuseConversationEnrichmentOnce) OutputQueue() *agent.OutputQueue {
	return agent.OutputQueueFor(sink.ProviderServices)
}

func (sink *refuseConversationEnrichmentOnce) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	if sink.refuse {
		sink.refuse = false
		return false, nil
	}
	return sink.ProviderServices.EnrichMessage(change)
}

func TestKimiConversationProjectionRetainsAConflictedCaptureAcrossSessionChanges(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	base := agent.NewProviderServices(sink)
	base.UpdateSessionID("native")
	peer := &refuseConversationEnrichmentOnce{ProviderServices: base}
	writer, err := newKimiConversationRecordWriter(peer)
	require.NoError(t, err)
	record := contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindItem, SessionID: "native", AgentID: "main", TurnID: "turn-1",
		Role: contracts.KimiConversationRecordRoleItem, Item: json.RawMessage(`{"type":"assistant_message","item_id":"answer-1","status":"running","created_at":"2026-11-18T00:00:00Z","text":"Prefix"}`)}
	_, err = persistConversationTestRecord(t, writer, record, nil, agent.SpanInfo{})
	require.NoError(t, err)
	record.Item = json.RawMessage(strings.Replace(string(record.Item), `"Prefix"`, `"Retained answer"`, 1))
	peer.refuse = true
	_, err = persistConversationTestRecord(t, writer, record, nil, agent.SpanInfo{})
	var pending *agent.PendingOutputError
	require.ErrorAs(t, err, &pending)
	assert.True(t, agent.OutputQueueFor(writer).HasPending())
	record.Item[0] = 'x'
	base.UpdateSessionID("replacement")
	require.NoError(t, agent.FlushPendingOutput(writer))
	assert.False(t, agent.OutputQueueFor(writer).HasPending())
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, "native", rows[0].AgentSessionID)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rows[0].SupplementalContent, &fields))
	latest, err := decodeKimiConversationRecord(fields[contracts.KimiConversationSupplementRoot])
	require.NoError(t, err)
	assert.Equal(t, "Retained answer", *latest.item.Text)
}

func TestKimiConversationProjectionRejectsIdentityAndOperationMismatches(t *testing.T) {
	t.Parallel()
	base := agent.NewProviderServices(&agenttest.Sink{})
	base.UpdateSessionID("native")
	writer, err := newKimiConversationRecordWriter(base)
	require.NoError(t, err)
	record := contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindItem, SessionID: "native", AgentID: "main", TurnID: "turn-1",
		Role: contracts.KimiConversationRecordRoleResult, Item: json.RawMessage(kimiConversationRecordTestTool)}
	raw, err := encodeKimiConversationRecord(record)
	require.NoError(t, err)
	view, err := decodeKimiConversationRecord(raw)
	require.NoError(t, err)
	content := agent.MessageContent{Original: raw, AgentSessionID: "native", IdempotencyKey: view.key()}
	require.Error(t, writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content, agent.SpanInfo{}))
	require.Error(t, writer.PersistTurnEnd(content, agent.SpanInfo{}))
	content.AgentSessionID = "another"
	require.Error(t, writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}))
	_, err = newKimiConversationRecordWriter(nil)
	require.Error(t, err)
	assert.False(t, errors.Is(err, agent.ErrOutputInFlight))
}

func TestKimiConversationResolveLatestViewHandlesSupplementalCases(t *testing.T) {
	t.Parallel()
	record := contracts.KimiConversationRecord{
		Type: contracts.KimiConversationRecordKindItem, SessionID: "native", AgentID: "main", TurnID: "turn-1", Role: contracts.KimiConversationRecordRoleItem,
		Item: json.RawMessage(`{"type":"assistant_message","item_id":"answer-1","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","text":"Prefix"}`),
	}
	raw, err := encodeKimiConversationRecord(record)
	require.NoError(t, err)
	original, err := decodeKimiConversationRecord(raw)
	require.NoError(t, err)

	// Empty supplemental returns original with an initialized map.
	got, fields, err := original.resolveLatestView(nil)
	require.NoError(t, err)
	assert.Equal(t, "Prefix", *got.item.Text)
	assert.NotNil(t, fields)
	assert.Empty(t, fields)

	// Unrelated supplemental fields return original and preserve those fields.
	got, fields, err = original.resolveLatestView([]byte(`{"media":{"retained":true}}`))
	require.NoError(t, err)
	assert.Equal(t, "Prefix", *got.item.Text)
	assert.JSONEq(t, `{"retained":true}`, string(fields["media"]))

	// Valid newer view with matching native identity replaces original.
	newer := record
	newer.Item = json.RawMessage(`{"type":"assistant_message","item_id":"answer-1","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Final"}`)
	newerRaw, err := encodeKimiConversationRecord(newer)
	require.NoError(t, err)
	supplemental, err := json.Marshal(map[string]json.RawMessage{contracts.KimiConversationSupplementRoot: newerRaw})
	require.NoError(t, err)
	got, _, err = original.resolveLatestView(supplemental)
	require.NoError(t, err)
	assert.Equal(t, "Final", *got.item.Text)

	// Malformed or foreign-identity supplemental values return an error.
	foreign := record
	foreign.Item = json.RawMessage(`{"type":"assistant_message","item_id":"other-item","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Wrong"}`)
	foreignRaw, err := encodeKimiConversationRecord(foreign)
	require.NoError(t, err)
	foreignSupplemental, err := json.Marshal(map[string]json.RawMessage{contracts.KimiConversationSupplementRoot: foreignRaw})
	require.NoError(t, err)

	for name, bad := range map[string][]byte{
		"non-JSON supplemental": []byte(`{bad`),
		"null supplemental":     []byte(`null`),
		"array supplemental":    []byte(`[]`),
		"malformed latest view": []byte(`{"` + contracts.KimiConversationSupplementRoot + `":"not a record"}`),
		"foreign latest view":   foreignSupplemental,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := original.resolveLatestView(bad)
			require.Error(t, err)
		})
	}
}
