package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func messageSupplementObject(encoded []byte) (map[string]json.RawMessage, error) {
	if len(encoded) == 0 {
		return make(map[string]json.RawMessage), nil
	}
	return jsonObject(encoded, "the message supplement")
}

func supplementMetadata(fields map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	raw, present := fields[contracts.MessageSupplementFieldMetadata]
	if !present {
		return make(map[string]json.RawMessage), nil
	}
	return jsonObject(raw, "the Worker metadata")
}

func journalSupplement(t *testing.T, records string) []byte {
	t.Helper()
	return []byte(`{"metadata":{"notification_entries":` + records + `}}`)
}

func TestNotificationEntryRejectsAnExplicitUnknownTokenMaskedByAssembledCompletion(t *testing.T) {
	t.Parallel()
	for _, completion := range []MessageCompletion{MessageCompletionComplete, MessageCompletionInterrupted, MessageCompletionError, MessageCompletionFinished} {
		original, err := MarshalAssembledMessage(AssembledMessageKindText, "exact native bytes", completion)
		require.NoError(t, err)
		for _, key := range []string{"", "native-key"} {
			entry, err := NewNotificationEntry(2, 2, MessageContent{Original: original, IdempotencyKey: key, Completion: "unknown-explicit-completion"})
			assert.ErrorContains(t, err, "unknown completion")
			assert.Equal(t, NotificationEntry{}, entry)
			_, err = NewNotificationEntry(2, 2, MessageContent{Original: original, IdempotencyKey: key})
			assert.NoError(t, err, "an absent explicit token still permits assembled fallback")
		}
	}
}

func notificationFingerprintReference(provider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, completion leapmuxv1.MessageCompletion, content MessageContent) [sha256.Size]byte {
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

func notificationIdentityCorpus() []MessageContent {
	return []MessageContent{
		{IdempotencyKey: "native:0", Original: []byte(" {\"x\":1,\"x\":2,\"n\":1.00e+0,\"text\":\"한😀\"}\r\n"),
			Supplemental: []byte(" {\"n\":9007199254740993} \n"), Metadata: []byte(" {\"duration_ms\":0} "), Completion: MessageCompletionInterrupted},
		{IdempotencyKey: "native:1", Original: []byte{0xff, 0x00}},
		{IdempotencyKey: "native:2", Original: bytes.Repeat([]byte("large original bytes"), 1<<16)},
		{IdempotencyKey: "native:3"},
	}
}

func TestNotificationJournalFingerprintsEveryOriginalByteSequence(t *testing.T) {
	t.Parallel()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
	inputs := notificationIdentityCorpus()
	var encoded []byte
	for _, content := range inputs {
		entry, err := NewNotificationEntry(provider, source, content)
		require.NoError(t, err)
		encoded, err = AppendNotificationJournal(encoded, MessageContent{}, entry)
		require.NoError(t, err)
	}
	decoded, err := DecodeNotificationJournal(encoded)
	require.NoError(t, err)
	require.Len(t, decoded, len(inputs))
	for index, content := range inputs {
		_, completion := MessageMetadata(content)
		assert.Equal(t, content.IdempotencyKey, decoded[index].IdempotencyKey)
		assert.Equal(t, notificationFingerprintReference(provider, source, completion, content), decoded[index].Fingerprint)
		changed := content.Clone()
		changed.Original = append(changed.Original, ' ')
		entry, err := NewNotificationEntry(provider, source, changed)
		require.NoError(t, err)
		assert.NotEqual(t, decoded[index].Fingerprint, entry.Fingerprint)
	}
	known, err := NewNotificationEntry(provider, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, MessageContent{IdempotencyKey: "known", Original: []byte(`{}`), Completion: MessageCompletionInterrupted})
	require.NoError(t, err)
	assert.Equal(t, "da12b73c39b696172db662bf9e9dc7facc901325cef89472adc1e3ce485131a2", hex.EncodeToString(known.Fingerprint[:]))
	assert.Equal(t, "2hK3PDm2lhcttmK/np3H+syQEyXO+JRyrcHjzkhRMaI=", base64.StdEncoding.EncodeToString(known.Fingerprint[:]))
	assert.NotContains(t, string(encoded), "original_base64")
}

func TestNotificationJournalRejectsInvalidShapeAndNoncanonicalBase64(t *testing.T) {
	t.Parallel()
	valid := map[string]any{
		"idempotency_key": "native:0", "fingerprint_base64": "2hK3PDm2lhcttmK/np3H+syQEyXO+JRyrcHjzkhRMaI=",
	}
	for _, field := range []string{"idempotency_key", "fingerprint_base64"} {
		for _, missing := range []bool{false, true} {
			fields := make(map[string]any, len(valid))
			for key, value := range valid {
				fields[key] = value
			}
			if missing {
				delete(fields, field)
			} else {
				fields[field] = nil
			}
			raw, err := json.Marshal([]any{fields})
			require.NoError(t, err)
			_, err = DecodeNotificationJournal(journalSupplement(t, string(raw)))
			assert.Error(t, err, "%s missing=%v", field, missing)
		}
	}
	for _, value := range []string{"Zg", "Zh==", "Zg==\n", "-w==", "%%%", "2hK3PDm2lhcttmK/np3H+syQEyXO+JRyrcHjzkhRMaJ="} {
		fields := make(map[string]any, len(valid))
		for key, prior := range valid {
			fields[key] = prior
		}
		fields["fingerprint_base64"] = value
		raw, err := json.Marshal([]any{fields})
		require.NoError(t, err)
		_, err = DecodeNotificationJournal(journalSupplement(t, string(raw)))
		assert.ErrorContains(t, err, "canonical padded Base64")
	}
	for _, value := range []any{"", "Zg==", base64.StdEncoding.EncodeToString(make([]byte, 31)), base64.StdEncoding.EncodeToString(make([]byte, 33)), 0, false, []string{}} {
		raw, err := json.Marshal([]any{map[string]any{"idempotency_key": "key", "fingerprint_base64": value}})
		require.NoError(t, err)
		_, err = DecodeNotificationJournal(journalSupplement(t, string(raw)))
		assert.Error(t, err, "%v", value)
	}
	for _, raw := range []string{
		`[{"idempotency_key":"","fingerprint_base64":"2hK3PDm2lhcttmK/np3H+syQEyXO+JRyrcHjzkhRMaI="}]`,
		`[{"idempotency_key":"key","idempotency_key":"key","fingerprint_base64":"2hK3PDm2lhcttmK/np3H+syQEyXO+JRyrcHjzkhRMaI="}]`,
		`[{"idempotency_key":"key","fingerprint_base64":"2hK3PDm2lhcttmK/np3H+syQEyXO+JRyrcHjzkhRMaI=","future":0}]`,
	} {
		_, err := DecodeNotificationJournal(journalSupplement(t, raw))
		assert.Error(t, err, raw)
	}
	for _, raw := range []string{`null`, `{}`, `false`, `[null]`, `[{}]`, `[{"future":0}]`} {
		_, err := DecodeNotificationJournal(journalSupplement(t, raw))
		assert.Error(t, err, raw)
	}
	for _, supplement := range []string{`{`, `null`, `[]`, `{"metadata":null}`, `{"metadata":[]}`} {
		_, err := DecodeNotificationJournal([]byte(supplement))
		assert.Error(t, err, supplement)
	}
}

func TestNotificationFingerprintUsesOnlyRealDeclaredCompletion(t *testing.T) {
	t.Parallel()
	seen := make(map[[sha256.Size]byte]bool)
	for ordinal := range leapmuxv1.MessageCompletion_name {
		completion := leapmuxv1.MessageCompletion(ordinal)
		content := MessageContent{IdempotencyKey: "completion", Original: []byte(`{}`), Completion: messageCompletionToken(completion)}
		entry, err := NewNotificationEntry(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
		require.NoError(t, err)
		assert.Equal(t, notificationFingerprintReference(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, completion, content), entry.Fingerprint)
		assert.False(t, seen[entry.Fingerprint])
		seen[entry.Fingerprint] = true
		encoded, err := AppendNotificationJournal(nil, MessageContent{}, entry)
		require.NoError(t, err)
		entries, err := DecodeNotificationJournal(encoded)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, entry, entries[0])
		fields, err := messageSupplementObject(encoded)
		require.NoError(t, err)
		metadata, err := supplementMetadata(fields)
		require.NoError(t, err)
		var records []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(metadata[NotificationJournalField], &records))
		require.Len(t, records, 1)
		assert.NotContains(t, records[0], "completion")
		assert.Len(t, records[0], 2)
	}
	for _, token := range []string{"null", "0", "-1", "1.5", "9223372036854775807", "unknown", "false", "2147483647"} {
		for _, key := range []string{"", "key"} {
			_, err := NewNotificationEntry(0, 1, MessageContent{IdempotencyKey: key, Completion: MessageCompletion(token)})
			assert.ErrorContains(t, err, "unknown completion")
		}
	}
	encoded, err := AppendNotificationJournal(nil, MessageContent{}, NotificationEntry{})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"completion"`)
}

func messageCompletionToken(completion leapmuxv1.MessageCompletion) MessageCompletion {
	switch completion {
	case leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED:
		return ""
	case leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_COMPLETE:
		return MessageCompletionComplete
	case leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_INTERRUPTED:
		return MessageCompletionInterrupted
	case leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_ERROR:
		return MessageCompletionError
	case leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_FINISHED:
		return MessageCompletionFinished
	default:
		return MessageCompletion(fmt.Sprint(completion))
	}
}

func TestNotificationJournalOmitsEmptyKeysAndRejectsRepeatedNativeKeys(t *testing.T) {
	t.Parallel()
	encoded, err := AppendNotificationJournal(nil, MessageContent{}, NotificationEntry{IdempotencyKey: "native:0"})
	require.NoError(t, err)
	_, err = AppendNotificationJournal(encoded, MessageContent{}, NotificationEntry{IdempotencyKey: "native:0"})
	assert.ErrorContains(t, err, "repeats key")
	for range 2 {
		encoded, err = AppendNotificationJournal(encoded, MessageContent{}, NotificationEntry{})
		require.NoError(t, err)
	}
	entries, err := DecodeNotificationJournal(encoded)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	_, err = DecodeNotificationJournal(journalSupplement(t,
		`[{"idempotency_key":"same","fingerprint_base64":"2hK3PDm2lhcttmK/np3H+syQEyXO+JRyrcHjzkhRMaI="},{"idempotency_key":"same","fingerprint_base64":"2hK3PDm2lhcttmK/np3H+syQEyXO+JRyrcHjzkhRMaI="}]`))
	assert.ErrorContains(t, err, "repeats key")
}

func TestNotificationJournalPreservesRenderingSourcesAndRejectsMetadataInjection(t *testing.T) {
	t.Parallel()
	previous := []byte(`{"unknown_envelope_field":{"n":9007199254740993},"provider":{"left":1},"metadata":{"old_worker_field":0}}`)
	encoded, err := AppendNotificationJournal(previous, MessageContent{
		Supplemental: []byte(`{"right":2,"notification_entries":"provider data"}`), Metadata: []byte(`{"new_worker_field":0}`),
	}, NotificationEntry{IdempotencyKey: "native:0"})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `9007199254740993`)
	fields, err := messageSupplementObject(encoded)
	require.NoError(t, err)
	assert.JSONEq(t, `{"left":1,"right":2,"notification_entries":"provider data"}`, string(fields["provider"]))
	for _, key := range []string{"old_worker_field", "new_worker_field", NotificationJournalField} {
		metadata, err := supplementMetadata(fields)
		require.NoError(t, err)
		assert.Contains(t, metadata, key)
	}
	replaced, err := ReplaceMessageSupplementProvider(encoded, []byte(`{"recovered":true}`), 0)
	require.NoError(t, err)
	before, err := DecodeNotificationJournal(encoded)
	require.NoError(t, err)
	after, err := DecodeNotificationJournal(replaced)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	fields, err = messageSupplementObject(replaced)
	require.NoError(t, err)
	assert.JSONEq(t, `{"recovered":true}`, string(fields["provider"]))
	assert.JSONEq(t, `{"n":9007199254740993}`, string(fields["unknown_envelope_field"]))
	_, err = AppendNotificationJournal(encoded, MessageContent{Metadata: []byte(`{"notification_entries":[]}`)}, NotificationEntry{})
	assert.ErrorContains(t, err, "reserved notification_entries")
	_, err = EncodeMessageSupplement(MessageContent{Metadata: []byte(`{"notification_entries":null}`)})
	assert.ErrorContains(t, err, "reserved notification_entries")
	_, err = ReplaceMessageSupplementProvider(encoded, []byte(`{`), 0)
	assert.Error(t, err)
	unchanged, err := AppendNotificationJournal(encoded, MessageContent{}, NotificationEntry{})
	require.NoError(t, err)
	fields, err = messageSupplementObject(unchanged)
	require.NoError(t, err)
	assert.JSONEq(t, `{"left":1,"right":2,"notification_entries":"provider data"}`, string(fields["provider"]))
	primitive, err := AppendNotificationJournal(encoded, MessageContent{Supplemental: []byte(`0`)}, NotificationEntry{})
	require.NoError(t, err)
	fields, err = messageSupplementObject(primitive)
	require.NoError(t, err)
	assert.Equal(t, "0", string(fields["provider"]))
}
