package agent

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageSupplementProjectionPreservesOrdinaryBytes(t *testing.T) {
	t.Parallel()
	for _, supplement := range [][]byte{
		nil,
		{},
		[]byte(" \n{\"z\":9007199254740993,\"provider\":{\"x\":1,\"x\":2,\"n\":1.00e+0},\"metadata\":{\"z\":0,\"a\":1.0}}\r\n"),
		[]byte(`{"metadata":{},"provider":{"notification_entries":"native","notification_reduction":"native"}}`),
		[]byte(" {\"metadata\":{\"first\":1.00e+0},\"metadata\":{\"last\":9007199254740993},\"provider\":{\"x\":1,\"x\":2}}\n"),
		[]byte(`{"metadata":null,"metadata":{"duration_ms":0}}`),
	} {
		before := bytes.Clone(supplement)
		projected, err := ProjectMessageSupplement(supplement, 0)
		require.NoError(t, err)
		assert.Equal(t, supplement, projected)
		if len(projected) != 0 {
			projected[0] ^= 0xff
			assert.Equal(t, before, supplement, "the projection must return an independent byte slice")
		}
	}
}

func TestMessageSupplementProjectionRejectsShadowedOrRepeatedPrivateFields(t *testing.T) {
	t.Parallel()
	for _, supplement := range []string{
		`{"metadata":{"notification_entries":[]},"metadata":{"duration_ms":0}}`,
		`{"metadata":{"duration_ms":0},"metadata":{"notification_entries":[]}}`,
		`{"metadata":{"notification_entries":null},"metadata":{}}`,
		`{"metadata":{"notification_reduction":{}},"metadata":{}}`,
		`{"metadata":{"notification_reduction":null},"metadata":{"duration_ms":0}}`,
		`{"metadata":{"notification_entries":null,"notification_entries":[]}}`,
		`{"metadata":{"notification_entries":[],"notification_entries":[]}}`,
		`{"metadata":{"notification_reduction":null,"notification_reduction":{}}}`,
		`{"metadata":{"notification_entries":[],"notific\u0061tion_entries":[]}}`,
	} {
		t.Run(supplement, func(t *testing.T) {
			original := []byte(supplement)
			before := bytes.Clone(original)
			parsed, err := ParseStoredMessageSupplement(original)
			assert.ErrorIs(t, err, ErrInvalidNotificationStorage)
			assert.Nil(t, parsed)
			for range 2 {
				projected, err := ProjectMessageSupplement(original, 0)
				assert.ErrorIs(t, err, ErrInvalidNotificationStorage)
				assert.Empty(t, projected, "a corrupt private envelope must produce no routine rendering payload")
				_, err = DecodeNotificationJournal(original)
				assert.ErrorIs(t, err, ErrInvalidNotificationStorage)
				_, err = DecodeNotificationReduction(original, 0)
				assert.ErrorIs(t, err, ErrInvalidNotificationStorage)
				_, err = ReplaceMessageSupplementProvider(original, []byte(`{"new":true}`), 0)
				assert.ErrorIs(t, err, ErrInvalidNotificationStorage)
			}
			assert.Equal(t, before, original)
		})
	}
}

func TestNotificationReductionRejectsInvalidStoredSchema(t *testing.T) {
	t.Parallel()
	for _, reduction := range []string{
		`null`, `[]`, `false`, `{"future":0}`, `{"provider_slots":null}`,
		`{"provider_slots":{"key":0}}`, `{"provider_slots":{"key":-1}}`,
		`{"provider_slots":{"key":1.5}}`, `{"provider_slots":{"key":3}}`,
		`{"provider_slots":{"key":9223372036854775807}}`, `{"provider_slots":{"key":"1"}}`,
		`{"provider_slots":{"key":null}}`, `{"provider_slots":{"key":1,"key":2}}`,
		`{"provider_slots":{"a":1,"b":1}}`, `{"provider_slots":{},"provider_slots":{}}`,
		`{"cancelled_settings":null}`, `{"cancelled_settings":{"model":null}}`,
		`{"cancelled_settings":{"model":0}}`, `{"cancelled_settings":{"model":"a","model":"b"}}`,
	} {
		_, err := DecodeNotificationReduction([]byte(`{"metadata":{"notification_reduction":`+reduction+`}}`), 2)
		assert.Error(t, err, reduction)
	}
}

func TestNotificationReductionProjectsAndReplacesOnlyPrivateMetadata(t *testing.T) {
	t.Parallel()
	prior := []byte(`{"extra":{"n":1.00e+0},"provider":{"notification_reduction":"native"},"metadata":{"duration_ms":0}}`)
	entry, err := NewNotificationEntry(2, 1, MessageContent{IdempotencyKey: "key", Original: []byte(`{}`)})
	require.NoError(t, err)
	stored, err := AppendNotificationJournal(prior, MessageContent{}, entry)
	require.NoError(t, err)
	state := NotificationReductionState{ProviderSlots: map[string]int{"": 1}, CancelledSettings: map[string]string{"model": ""}}
	stored, err = WithNotificationReduction(stored, state, 1)
	require.NoError(t, err)
	decoded, err := DecodeNotificationReduction(stored, 1)
	require.NoError(t, err)
	assert.Equal(t, state, decoded)
	replaced, err := ReplaceMessageSupplementProvider(stored, []byte(`{"next":0}`), 1)
	require.NoError(t, err)
	after, err := DecodeNotificationReduction(replaced, 1)
	require.NoError(t, err)
	assert.Equal(t, state, after)
	identities, err := DecodeNotificationJournal(replaced)
	require.NoError(t, err)
	assert.Equal(t, []NotificationEntry{entry}, identities)
	projected, err := ProjectMessageSupplement(replaced, 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"extra":{"n":1.00e+0},"provider":{"next":0},"metadata":{"duration_ms":0}}`, string(projected))
	assert.NotContains(t, string(projected), NotificationJournalField)
	assert.NotContains(t, string(projected), NotificationReductionField)
	decoded.CancelledSettings["model"] = "changed"
	assert.Equal(t, "", state.CancelledSettings["model"])
	for _, key := range []string{NotificationJournalField, NotificationReductionField} {
		_, err := EncodeMessageSupplement(MessageContent{Metadata: []byte(`{"` + key + `":null}`)})
		assert.ErrorContains(t, err, "reserved "+key)
	}
}

func TestMessageSupplementProjectionReusesImmutableParsedResult(t *testing.T) {
	t.Parallel()
	entry, err := NewNotificationEntry(2, 1, MessageContent{IdempotencyKey: "key", Original: []byte(`{}`)})
	require.NoError(t, err)
	stored, err := AppendNotificationJournal([]byte(`{"provider":{"n":1.00e+0},"metadata":{"duration_ms":0}}`), MessageContent{}, entry)
	require.NoError(t, err)
	want := NotificationReductionState{ProviderSlots: map[string]int{"group": 1}, CancelledSettings: map[string]string{"model": ""}}
	stored, err = WithNotificationReduction(stored, want, 1)
	require.NoError(t, err)
	parsed, err := ParseStoredMessageSupplement(stored)
	require.NoError(t, err)
	stored[0] = '!'
	assert.True(t, parsed.HasNotificationReduction())
	identities := parsed.NotificationJournal()
	identities[0].IdempotencyKey = "changed"
	identities[0].Fingerprint[0] ^= 0xff
	assert.Equal(t, []NotificationEntry{entry}, parsed.NotificationJournal())
	state, err := parsed.NotificationReduction(1)
	require.NoError(t, err)
	state.ProviderSlots["group"] = 0
	state.CancelledSettings["model"] = "changed"
	state, err = parsed.NotificationReduction(1)
	require.NoError(t, err)
	assert.Equal(t, want, state)
	original := []byte(`{"original":1}`)
	content := parsed.MessageContent(original)
	wantContent := content.Clone()
	original[0] = '!'
	for _, part := range [][]byte{content.Original, content.Supplemental, content.Metadata} {
		part[0] = '!'
	}
	assert.Equal(t, wantContent, parsed.MessageContent(wantContent.Original))
	first, err := parsed.Project(1)
	require.NoError(t, err)
	before := bytes.Clone(first)
	first[0] = '!'
	second, err := parsed.Project(1)
	require.NoError(t, err)
	assert.Equal(t, before, second)
	replacement := []byte(`{"replacement":true}`)
	originalReplacement := bytes.Clone(replacement)
	replaced, err := parsed.ReplaceProvider(replacement, 1)
	require.NoError(t, err)
	assert.Contains(t, string(replaced), `"replacement":true`)
	expectedReplacement := bytes.Clone(replaced)
	replacement[0] = '!'
	replaced[0] = '!'
	repeatedReplacement, err := parsed.ReplaceProvider(originalReplacement, 1)
	require.NoError(t, err)
	assert.Equal(t, expectedReplacement, repeatedReplacement)
	assert.Equal(t, []NotificationEntry{entry}, parsed.NotificationJournal())
	third, err := parsed.Project(1)
	require.NoError(t, err)
	assert.Equal(t, before, third)
	_, err = parsed.Project(0)
	assert.ErrorContains(t, err, "invalid content index")
	_, err = parsed.ReplaceProvider([]byte(`{}`), -1)
	assert.ErrorContains(t, err, "must not be negative")
}

func TestNotificationReductionClassifiesOnlyPrivateSchemaErrors(t *testing.T) {
	t.Parallel()
	for _, malformed := range []string{`{`, `null`, `[]`, `{"metadata":null}`} {
		_, err := ParseStoredMessageSupplement([]byte(malformed))
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrInvalidNotificationStorage)
	}
	for _, malformed := range []string{`{"metadata":{"notification_entries":null}}`, `{"metadata":{"notification_reduction":{"provider_slots":{"group":0}}}}`} {
		_, err := ParseStoredMessageSupplement([]byte(malformed))
		assert.ErrorIs(t, err, ErrInvalidNotificationStorage)
	}
}
