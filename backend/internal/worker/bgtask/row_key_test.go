package bgtask

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The typed row key keeps the two directions of a row key apart: fresh
// provider input (NewRowKey, which validates, escapes and derives) and stored
// identity (ParseRowIdentity, which validates without re-interpreting).
func TestNewRowKeyTypesFreshProviderInput(t *testing.T) {
	t.Parallel()

	t.Run("keeps a usable native key as its own bytes", func(t *testing.T) {
		key := NewRowKey("task-1")
		assert.Equal(t, "task-1", key.String())
		assert.NoError(t, key.Reason())
		assert.Equal(t, 6, key.RawByteLength())
		assert.True(t, key.Identity().String() == "task-1")
	})

	t.Run("escapes a native key that starts with a reserved prefix", func(t *testing.T) {
		raw := derivedRowKeyPrefix + "not-a-real-digest-but-a-valid-native-key"
		key := NewRowKey(raw)
		require.NoError(t, ValidateRowKey(raw), "the case must be one the rule accepts")
		assert.NotEqual(t, raw, key.String(), "a reserved-shaped native key must not pass through")
		assert.True(t, strings.HasPrefix(key.String(), escapedRowKeyPrefix))
		assert.ErrorIs(t, key.Reason(), ErrRowKeyEscapedPrefix)
		assert.NoError(t, ValidateRowKey(key.String()))
		// The other reserved prefix escapes too.
		escaped := NewRowKey(escapedRowKeyPrefix + "abc")
		assert.True(t, strings.HasPrefix(escaped.String(), escapedRowKeyPrefix))
		assert.NotEqual(t, escapedRowKeyPrefix+"abc", escaped.String())
	})

	t.Run("derives a key for unusable input and keeps the reason", func(t *testing.T) {
		raw := strings.Repeat("x", RowKeyByteLimit+1)
		key := NewRowKey(raw)
		assert.True(t, strings.HasPrefix(key.String(), derivedRowKeyPrefix))
		assert.Error(t, key.Reason())
		assert.Equal(t, RowKeyByteLimit+1, key.RawByteLength())
		assert.NoError(t, ValidateRowKey(key.String()))
	})

	t.Run("keeps two reserved-shaped native keys apart", func(t *testing.T) {
		raw := derivedRowKeyPrefix + "one"
		other := derivedRowKeyPrefix + "two"
		assert.NotEqual(t, NewRowKey(raw).String(), NewRowKey(other).String())
		// And apart from the derived key of the bytes they imitate.
		assert.NotEqual(t, NewRowKey(raw).String(), NormalizeRowKey(strings.TrimPrefix(raw, derivedRowKeyPrefix)))
	})

	t.Run("retains no raw key copy beyond the diagnostic facts", func(t *testing.T) {
		raw := "leapmux-derived-key:" + strings.Repeat("n", 200)
		key := NewRowKey(raw)
		require.ErrorIs(t, key.Reason(), ErrRowKeyEscapedPrefix)
		assert.NotContains(t, key.String(), strings.Repeat("n", 8), "the canonical key is a digest, not the raw bytes")
		assert.Equal(t, len(raw), key.RawByteLength())
	})
}

// ParseRowIdentity loads exactly what was stored: reserved-prefixed keys
// validate as themselves, and a malformed reserved spelling is corruption.
func TestParseRowIdentityLoadsStoredKeysWithoutInterpretingThem(t *testing.T) {
	t.Parallel()

	derived := NewRowKey(strings.Repeat("x", RowKeyByteLimit+1))
	escaped := NewRowKey(derivedRowKeyPrefix + "native")
	for _, stored := range []string{"task-1", derived.String(), escaped.String()} {
		identity, err := ParseRowIdentity(stored)
		require.NoErrorf(t, err, "%q is a storable identity", stored)
		assert.Equal(t, stored, identity.String())
	}

	for _, corrupt := range []string{
		derivedRowKeyPrefix + "short",
		escapedRowKeyPrefix + "UPPERCASEHEX" + strings.Repeat("a", 64-12),
		derivedRowKeyPrefix + strings.Repeat("z", 64),
	} {
		_, err := ParseRowIdentity(corrupt)
		assert.Errorf(t, err, "%q is not a spelling NewRowKey can produce", corrupt)
	}

	_, err := ParseRowIdentity(strings.Repeat("x", RowKeyByteLimit+1))
	assert.Error(t, err, "an unusable stored key is refused")
}

// NewRowKey and NormalizeRowKey answer the same canonical spelling, so a call
// site may pass fresh provider bytes through either form.
func TestNewRowKeyAgreesWithNormalizeRowKey(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"task-1", "", " child-key ", "a\xffb",
		strings.Repeat("x", RowKeyByteLimit+1),
		derivedRowKeyPrefix + "imitation",
	} {
		assert.Equal(t, NormalizeRowKey(raw), NewRowKey(raw).String(), "%q", raw)
	}
}
