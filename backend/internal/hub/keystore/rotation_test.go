package keystore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"os"
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
	assert.Equal(t, uint32(1), ks1.ActiveVersion())
	assert.Len(t, ks1.Versions(), 1)

	// 2. Encrypt data with version 1.
	plaintext1 := []byte("secret-data-1")
	plaintext2 := []byte("secret-data-2")
	aad := []byte("test-context")
	ct1, err := ks1.Encrypt(plaintext1, aad)
	require.NoError(t, err)
	ct2, err := ks1.Encrypt(plaintext2, aad)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), binary.BigEndian.Uint32(ct1[:4]), "ciphertext should be version 1")
	assert.Equal(t, uint32(1), binary.BigEndian.Uint32(ct2[:4]))

	// Save original ciphertexts for later verification.
	origCt1 := make([]byte, len(ct1))
	copy(origCt1, ct1)

	// 3. Rotate key — adds version 2.
	newVer, err := RotateKey(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), newVer)

	// 4. Reload keystore from file.
	ks2, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ks2.ActiveVersion())
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
	assert.Equal(t, uint32(2), binary.BigEndian.Uint32(ct3[:4]), "new ciphertext should be version 2")

	// 7. Re-encrypt old data with new key (simulating reencrypt-secrets).
	reencrypted1, err := ks2.Encrypt(plaintext1, aad)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), binary.BigEndian.Uint32(reencrypted1[:4]), "re-encrypted data should be version 2")
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
	assert.Equal(t, uint32(2), ks3.ActiveVersion())
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

// TestPepperStableAcrossRotation is the regression test for the api_token
// breakage bug: the token pepper must NOT change when encryption keys are
// rotated or removed, so existing tokens keep validating.
func TestPepperStableAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	ks1, err := LoadOrGenerate(path)
	require.NoError(t, err)
	p0 := ks1.Pepper()
	assert.NotEqual(t, [keySize]byte{}, p0, "pepper should be seeded on generate")

	// Rotate twice — the pepper must not change.
	_, err = RotateKey(path)
	require.NoError(t, err)
	_, err = RotateKey(path)
	require.NoError(t, err)

	ks2, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(3), ks2.ActiveVersion())
	assert.Equal(t, p0, ks2.Pepper(), "pepper must survive encryption-key rotation")

	// Remove an old key version — the pepper must still not change.
	require.NoError(t, RemoveKey(path, 1))
	ks3, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, p0, ks3.Pepper(), "pepper must survive key removal")
}

// TestRegeneratePepperChangesPepperOnly verifies the explicit pepper-rotation
// path: the pepper changes, the encryption key ring is untouched.
func TestRegeneratePepperChangesPepperOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	_, err := LoadOrGenerate(path)
	require.NoError(t, err)
	_, err = RotateKey(path) // two key versions now
	require.NoError(t, err)

	before, err := LoadFromFile(path)
	require.NoError(t, err)
	oldPepper := before.Pepper()
	versionsBefore := before.Versions()

	require.NoError(t, RegeneratePepper(path))

	after, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.NotEqual(t, oldPepper, after.Pepper(), "pepper must change")
	assert.Equal(t, versionsBefore, after.Versions(), "encryption key ring must be unchanged")
	assert.Equal(t, before.ActiveVersion(), after.ActiveVersion())
}

// TestLegacyKeyRingSeedsPepper verifies that a key-ring file written before
// the dedicated pepper existed (no pepper line) is seeded with a stable pepper
// on load.
func TestLegacyKeyRingSeedsPepper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	// Write a legacy ring file: one version line, no pepper.
	key, err := GenerateKey()
	require.NoError(t, err)
	legacy := "1:" + base64.StdEncoding.EncodeToString(key[:]) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o600))

	ks, err := LoadOrGenerate(path)
	require.NoError(t, err)
	p := ks.Pepper()
	assert.NotEqual(t, [keySize]byte{}, p, "legacy file should be seeded with a pepper")

	// The seeded pepper persisted and is stable on reload.
	ks2, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, p, ks2.Pepper(), "seeded pepper must persist")
}

// TestTokenHashSurvivesRotation closes the loop end-to-end: a token-secret
// HMAC computed with the pepper before an encryption-key rotation+removal
// still matches after, so existing tokens keep validating.
func TestTokenHashSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	ks1, err := LoadOrGenerate(path)
	require.NoError(t, err)
	p1 := ks1.Pepper()
	mac := hmac.New(sha256.New, p1[:])
	mac.Write([]byte("token-secret"))
	want := mac.Sum(nil)

	// Rotate the encryption key, then remove the old version.
	_, err = RotateKey(path)
	require.NoError(t, err)
	require.NoError(t, RemoveKey(path, 1))

	ks2, err := LoadFromFile(path)
	require.NoError(t, err)
	p2 := ks2.Pepper()
	mac2 := hmac.New(sha256.New, p2[:])
	mac2.Write([]byte("token-secret"))
	assert.True(t, hmac.Equal(want, mac2.Sum(nil)),
		"a token hash must still validate after encryption-key rotation + removal")
}

// TestRotateKeySeedsLegacyPepper drives a legacy ring file (no pepper line)
// through RotateKey and verifies a non-zero pepper is seeded, never all-zero.
func TestRotateKeySeedsLegacyPepper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	key, err := GenerateKey()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("1:"+base64.StdEncoding.EncodeToString(key[:])+"\n"), 0o600))

	_, err = RotateKey(path)
	require.NoError(t, err)

	ks, err := LoadFromFile(path)
	require.NoError(t, err)
	p := ks.Pepper()
	assert.NotEqual(t, [keySize]byte{}, p, "RotateKey on a legacy file must seed a non-zero pepper")

	ks2, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, p, ks2.Pepper(), "seeded pepper must be stable")
}

// TestRemoveKeySeedsLegacyPepper covers the other ensurePepper caller:
// RemoveKey on a legacy file (no pepper) must seed a non-zero pepper.
func TestRemoveKeySeedsLegacyPepper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	k1, err := GenerateKey()
	require.NoError(t, err)
	k2, err := GenerateKey()
	require.NoError(t, err)
	content := "1:" + base64.StdEncoding.EncodeToString(k1[:]) + "\n" +
		"2:" + base64.StdEncoding.EncodeToString(k2[:]) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	require.NoError(t, RemoveKey(path, 1))

	ks, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.NotEqual(t, [keySize]byte{}, ks.Pepper(), "RemoveKey on a legacy file must seed a non-zero pepper")
	assert.Len(t, ks.Versions(), 1)
}
