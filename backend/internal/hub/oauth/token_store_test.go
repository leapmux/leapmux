package oauth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/leapmux/leapmux/internal/hub/keystore"
)

func newTestKeystore(t *testing.T) *keystore.Keystore {
	t.Helper()
	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	return ks
}

func TestTokenStore_EncryptedCiphertextDiffersFromPlaintext(t *testing.T) {
	ks := newTestKeystore(t)
	plain := "my-access-token-plaintext"
	aad := keystore.AccessTokenAAD("user1", "provider1")

	ct, err := ks.Encrypt([]byte(plain), aad)
	require.NoError(t, err)

	assert.NotEqual(t, []byte(plain), ct, "ciphertext must differ from plaintext")
	assert.Greater(t, len(ct), len(plain), "ciphertext must be longer (version+nonce+tag)")
}

func TestTokenStore_DecryptRoundtrip(t *testing.T) {
	ks := newTestKeystore(t)
	access := "access-token-value"
	refresh := "refresh-token-value"
	accessAAD := keystore.AccessTokenAAD("user1", "provider1")
	refreshAAD := keystore.RefreshTokenAAD("user1", "provider1")

	ctAccess, err := ks.Encrypt([]byte(access), accessAAD)
	require.NoError(t, err)
	ctRefresh, err := ks.Encrypt([]byte(refresh), refreshAAD)
	require.NoError(t, err)

	gotAccess, err := ks.Decrypt(ctAccess, accessAAD)
	require.NoError(t, err)
	assert.Equal(t, access, string(gotAccess))

	gotRefresh, err := ks.Decrypt(ctRefresh, refreshAAD)
	require.NoError(t, err)
	assert.Equal(t, refresh, string(gotRefresh))
}

func TestTokenStore_WrongKeyFails(t *testing.T) {
	ks1 := newTestKeystore(t)
	ks2 := newTestKeystore(t)
	aad := keystore.AccessTokenAAD("user1", "provider1")

	ct, err := ks1.Encrypt([]byte("secret"), aad)
	require.NoError(t, err)

	_, err = ks2.Decrypt(ct, aad)
	assert.Error(t, err, "decrypting with wrong key must fail")
}

func TestTokenStore_WrongAADFails(t *testing.T) {
	ks := newTestKeystore(t)
	aad := keystore.AccessTokenAAD("user1", "provider1")
	wrongAAD := keystore.AccessTokenAAD("user2", "provider1")

	ct, err := ks.Encrypt([]byte("secret"), aad)
	require.NoError(t, err)

	_, err = ks.Decrypt(ct, wrongAAD)
	assert.Error(t, err, "decrypting with wrong AAD must fail")
}

func TestTokenStore_KeyVersionMatchesActive(t *testing.T) {
	ks := newTestKeystore(t)

	ct, err := ks.Encrypt([]byte("test"), nil)
	require.NoError(t, err)

	ver, err := keystore.CiphertextVersion(ct)
	require.NoError(t, err)
	assert.Equal(t, ks.ActiveVersion(), ver, "ciphertext version must match active key version")
}

func TestTokenStore_CrossRecordAADPreventsSwap(t *testing.T) {
	ks := newTestKeystore(t)
	aadUser1 := keystore.AccessTokenAAD("user1", "provider1")
	aadUser2 := keystore.AccessTokenAAD("user2", "provider1")

	ct1, err := ks.Encrypt([]byte("user1-token"), aadUser1)
	require.NoError(t, err)

	// Attempting to decrypt user1's token with user2's AAD must fail.
	_, err = ks.Decrypt(ct1, aadUser2)
	assert.Error(t, err, "cross-record AAD swap must fail")
}

func TestTokenSetFromOAuth2Token_PreservesExpiryAsAbsolute(t *testing.T) {
	expiry := time.Now().Add(1 * time.Hour).UTC()
	token := &oauth2.Token{
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "bearer",
		Expiry:       expiry,
	}

	ts := TokenSetFromOAuth2Token(token)
	assert.Equal(t, expiry, ts.ExpiresAt, "ExpiresAt must match the original token Expiry exactly")
	assert.Equal(t, "access", ts.AccessToken)
	assert.Equal(t, "refresh", ts.RefreshToken)
}

func TestTokenSetFromOAuth2Token_ZeroExpiry(t *testing.T) {
	token := &oauth2.Token{
		AccessToken: "access",
		TokenType:   "bearer",
	}

	ts := TokenSetFromOAuth2Token(token)
	assert.True(t, ts.ExpiresAt.IsZero(), "ExpiresAt should be zero when token has no expiry")
}
