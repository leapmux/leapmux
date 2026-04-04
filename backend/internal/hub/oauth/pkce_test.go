package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratePKCE(t *testing.T) {
	verifier, challenge, err := GeneratePKCE()
	require.NoError(t, err)

	assert.Len(t, verifier, 43, "verifier should be 43 chars base64url (32 bytes)")
	assert.NotEmpty(t, challenge)

	// Verify the challenge is the S256 of the verifier.
	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])
	assert.Equal(t, expected, challenge)
}

func TestGeneratePKCE_Unique(t *testing.T) {
	v1, _, err := GeneratePKCE()
	require.NoError(t, err)
	v2, _, err := GeneratePKCE()
	require.NoError(t, err)

	assert.NotEqual(t, v1, v2, "two PKCE verifiers should be different")
}
