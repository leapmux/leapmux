package password

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashProducesArgon2idFormat(t *testing.T) {
	hash, err := Hash("test-password")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$"), "hash should start with $argon2id$")
}

func TestVerifyCorrectPassword(t *testing.T) {
	hash, err := Hash("my-password")
	require.NoError(t, err)

	match, err := Verify(hash, "my-password")
	require.NoError(t, err)
	assert.True(t, match)
}

func TestVerifyWrongPassword(t *testing.T) {
	hash, err := Hash("correct-password")
	require.NoError(t, err)

	match, err := Verify(hash, "wrong-password")
	require.NoError(t, err)
	assert.False(t, match)
}

func TestDifferentSaltsProduceDifferentHashes(t *testing.T) {
	hash1, err := Hash("same-password")
	require.NoError(t, err)
	hash2, err := Hash("same-password")
	require.NoError(t, err)

	assert.NotEqual(t, hash1, hash2, "same password should produce different hashes due to different salts")
}

func TestVerifyMalformedHashReturnsError(t *testing.T) {
	_, err := Verify("not-a-valid-hash", "password")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid argon2id hash format")
}

func TestVerifyTruncatedArgon2idReturnsError(t *testing.T) {
	_, err := Verify("$argon2id$broken", "password")
	assert.Error(t, err)
}
