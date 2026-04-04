package keystore

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFullKeyRotationLifecycle exercises the complete key rotation workflow:
// generate → encrypt → rotate → verify dual-key → re-encrypt → remove old key.
func TestFullKeyRotationLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	// 1. Auto-generate initial key ring (version 1).
	ks1, err := LoadOrGenerate(path)
	require.NoError(t, err)
	assert.Equal(t, byte(1), ks1.ActiveVersion())
	assert.Len(t, ks1.Versions(), 1)

	// 2. Encrypt data with version 1.
	plaintext1 := []byte("secret-data-1")
	plaintext2 := []byte("secret-data-2")
	aad := []byte("test-context")
	ct1, err := ks1.Encrypt(plaintext1, aad)
	require.NoError(t, err)
	ct2, err := ks1.Encrypt(plaintext2, aad)
	require.NoError(t, err)
	assert.Equal(t, byte(1), ct1[0], "ciphertext should be version 1")
	assert.Equal(t, byte(1), ct2[0])

	// Save original ciphertexts for later verification.
	origCt1 := make([]byte, len(ct1))
	copy(origCt1, ct1)

	// 3. Rotate key — adds version 2.
	newVer, err := RotateKey(path)
	require.NoError(t, err)
	assert.Equal(t, byte(2), newVer)

	// 4. Reload keystore from file.
	ks2, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, byte(2), ks2.ActiveVersion())
	assert.Len(t, ks2.Versions(), 2)

	// 5. Old ciphertext (version 1) still decrypts with new keystore.
	got1, err := ks2.Decrypt(ct1, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext1, got1)

	got2, err := ks2.Decrypt(ct2, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext2, got2)

	// 6. New encryption uses version 2.
	ct3, err := ks2.Encrypt([]byte("new-data"), aad)
	require.NoError(t, err)
	assert.Equal(t, byte(2), ct3[0], "new ciphertext should be version 2")

	// 7. Re-encrypt old data with new key (simulating reencrypt-secrets).
	reencrypted1, err := ks2.Encrypt(plaintext1, aad)
	require.NoError(t, err)
	assert.Equal(t, byte(2), reencrypted1[0], "re-encrypted data should be version 2")
	assert.NotEqual(t, ct1, reencrypted1, "re-encrypted ciphertext should differ")

	// Verify re-encrypted data decrypts correctly.
	gotRe1, err := ks2.Decrypt(reencrypted1, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext1, gotRe1)

	// 8. Remove old key version 1.
	err = RemoveKey(path, 1)
	require.NoError(t, err)

	// 9. Reload — only version 2 remains.
	ks3, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, byte(2), ks3.ActiveVersion())
	assert.Len(t, ks3.Versions(), 1)

	// 10. Old version-1 ciphertext can no longer be decrypted.
	_, err = ks3.Decrypt(origCt1, aad)
	assert.Error(t, err, "old v1 ciphertext should fail with v1 removed")
	assert.Contains(t, err.Error(), "unknown key version")

	// 11. Re-encrypted (version 2) data still works.
	gotFinal, err := ks3.Decrypt(reencrypted1, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext1, gotFinal)

	// 12. Cannot remove the active (highest) version.
	err = RemoveKey(path, 2)
	assert.Error(t, err, "should not be able to remove active version")
	assert.Contains(t, err.Error(), "cannot remove active")

	// 13. Cannot remove a version that's no longer in the ring.
	err = RemoveKey(path, 1)
	assert.Error(t, err, "should not be able to remove already-removed version")
	assert.Contains(t, err.Error(), "not in ring")
}
