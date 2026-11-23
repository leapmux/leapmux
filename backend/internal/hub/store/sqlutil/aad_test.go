package sqlutil

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPasskeyPublicKeyAAD(t *testing.T) {
	aad := PasskeyPublicKeyAAD("cred-row-1")
	assert.Equal(t, keystore.ColumnAAD("passkey_credentials", "public_key", "cred-row-1"), aad)
}

func TestPasskeyAADBindingRejectsSwap(t *testing.T) {
	ks, err := keystore.NewRandom()
	require.NoError(t, err)
	ct, err := ks.Encrypt([]byte("cose-key"), PasskeyPublicKeyAAD("row-a"))
	require.NoError(t, err)
	_, err = ks.Decrypt(ct, PasskeyPublicKeyAAD("row-b"))
	assert.Error(t, err)
}
