package dirac

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiracTaskletJournalKeepsExplicitLifetimeAndOutcomeIdentities(t *testing.T) {
	t.Parallel()
	raw := `[
{"id":"accepted","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},
{"id":"outcome","taskletId":"Witten","createdAt":1,"communication":{"type":"outcome","outcome":{"kind":"completed","response":"Done","completedAt":1}}},
{"id":"resumed","taskletId":"Witten","createdAt":2,"communication":{"type":"lifecycle","state":"resumed","assignment":""}}
]`
	journal, err := decodeDiracTaskletJournal([]byte(raw), "Witten")
	require.NoError(t, err)
	assert.Equal(t, "resumed", journal.Lifecycle.ID)
	assert.Equal(t, "resumed", journal.Lifecycle.State)
	require.NotNil(t, journal.Lifecycle.Assignment)
	assert.Empty(t, *journal.Lifecycle.Assignment)
	assert.JSONEq(t, `{"kind":"completed","response":"Done","completedAt":1}`, string(journal.Outcome), "native idempotent outcome reuse remains visible across resumes")
}

func TestDiracTaskletJournalAcceptsNativeCommunicationVariants(t *testing.T) {
	t.Parallel()
	for _, communication := range []string{
		`{"type":"waiting_for_message"}`,
		`{"type":"response","response":{}}`,
		`{"type":"message","message":{"taskletId":"Witten","type":"guidance","text":"Continue"}}`,
		`{"type":"message","message":{"taskletId":"Witten","type":"question","text":"Historical question"}}`,
		`{"type":"interaction","request":{"id":"approval","status":"pending"}}`,
		`{"type":"interaction","request":{"id":"approval","status":"resolved"}}`,
		`{"type":"interaction","request":{"id":"approval","status":"retired"}}`,
	} {
		t.Run(communication, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal([]any{map[string]any{"id": "native-event", "taskletId": "Witten", "createdAt": 0, "communication": json.RawMessage(communication)}})
			require.NoError(t, err)
			_, err = decodeDiracTaskletJournal(raw, "Witten")
			require.NoError(t, err)
		})
	}
}

func TestDiracTaskletJournalRefusesMalformedOrForeignEvents(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`null`, `{}`, `[null]`,
		`[{"id":"foreign","taskletId":"Other","createdAt":0,"communication":{"type":"waiting_for_message"}}]`,
		`[{"id":"missing-time","taskletId":"Witten","communication":{"type":"waiting_for_message"}}]`,
		`[{"id":"","taskletId":"Witten","createdAt":0,"communication":{"type":"waiting_for_message"}}]`,
		`[{"id":"unknown","taskletId":"Witten","createdAt":0,"communication":{"type":"other"}}]`,
		`[{"id":"invalid","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"other"}}]`,
		`[{"id":"invalid","taskletId":"Witten","createdAt":0,"communication":{"type":"outcome","outcome":{}}}]`,
		`[{"id":"invalid","taskletId":"Witten","createdAt":0,"communication":{"type":"response","response":null}}]`,
		`[{"id":"invalid","taskletId":"Witten","createdAt":0,"communication":{"type":"interaction","request":{"id":"","status":"pending"}}}]`,
		`[{"id":"invalid","taskletId":"Witten","createdAt":0,"communication":{"type":"message","message":{"taskletId":"Other","type":"guidance"}}}]`,
		`[{"id":"duplicate","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},{"id":"duplicate","taskletId":"Witten","createdAt":1,"communication":{"type":"lifecycle","state":"resumed"}}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := decodeDiracTaskletJournal([]byte(raw), "Witten")
			require.Error(t, err)
		})
	}
	for _, raw := range [][]byte{nil, []byte(`[]`)} {
		journal, err := decodeDiracTaskletJournal(raw, "Witten")
		require.NoError(t, err)
		assert.Empty(t, journal.Lifecycle.ID)
		assert.Empty(t, journal.Outcome)
	}
}
