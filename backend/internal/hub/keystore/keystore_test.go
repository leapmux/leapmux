package keystore

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	ks := newTestKeystore(t)
	plaintext := []byte("hello world")
	aad := []byte("test-context")

	ct, err := ks.Encrypt(plaintext, aad)
	require.NoError(t, err)

	got, err := ks.Decrypt(ct, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

func TestEncryptWithAAD(t *testing.T) {
	ks := newTestKeystore(t)
	plaintext := []byte("secret")
	aad := []byte("bound-context")

	ct, err := ks.Encrypt(plaintext, aad)
	require.NoError(t, err)

	// Same AAD succeeds.
	got, err := ks.Decrypt(ct, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

func TestTwoEncryptionsProduceDifferentCiphertext(t *testing.T) {
	ks := newTestKeystore(t)
	plaintext := []byte("same input")

	ct1, err := ks.Encrypt(plaintext, nil)
	require.NoError(t, err)
	ct2, err := ks.Encrypt(plaintext, nil)
	require.NoError(t, err)

	assert.NotEqual(t, ct1, ct2, "two encryptions of same plaintext should produce different ciphertext")
}

func TestCiphertextStartsWithKeyVersion(t *testing.T) {
	ks := newTestKeystore(t)

	ct, err := ks.Encrypt([]byte("test"), nil)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), binary.BigEndian.Uint32(ct[:4]), "ciphertext should start with key version 1")
}

func TestDecryptWrongKeyFails(t *testing.T) {
	ks1 := newTestKeystore(t)
	ks2 := newTestKeystore(t)

	ct, err := ks1.Encrypt([]byte("secret"), nil)
	require.NoError(t, err)

	_, err = ks2.Decrypt(ct, nil)
	assert.Error(t, err)
}

func TestDecryptWrongAADFails(t *testing.T) {
	ks := newTestKeystore(t)
	plaintext := []byte("secret")

	ct, err := ks.Encrypt(plaintext, []byte("correct-aad"))
	require.NoError(t, err)

	_, err = ks.Decrypt(ct, []byte("wrong-aad"))
	assert.Error(t, err)
}

func TestCiphertextVersion(t *testing.T) {
	ks := newTestKeystore(t)

	ct, err := ks.Encrypt([]byte("test"), nil)
	require.NoError(t, err)

	ver, err := CiphertextVersion(ct)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), ver)
}

func TestCiphertextVersion_MultiVersion(t *testing.T) {
	key1, err := GenerateKey()
	require.NoError(t, err)
	key2, err := GenerateKey()
	require.NoError(t, err)

	ks, err := New(map[uint32][keySize]byte{1: key1, 5: key2})
	require.NoError(t, err)

	ct, err := ks.Encrypt([]byte("test"), nil)
	require.NoError(t, err)

	ver, err := CiphertextVersion(ct)
	require.NoError(t, err)
	assert.Equal(t, uint32(5), ver)
}

func TestCiphertextVersion_TooShort(t *testing.T) {
	_, err := CiphertextVersion([]byte{1, 2})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestDecryptTruncatedCiphertextFails(t *testing.T) {
	ks := newTestKeystore(t)
	_, err := ks.Decrypt([]byte{1, 2, 3}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestDecryptCorruptedCiphertextFails(t *testing.T) {
	ks := newTestKeystore(t)
	ct, err := ks.Encrypt([]byte("test"), nil)
	require.NoError(t, err)

	// Flip a bit in the ciphertext (after version + nonce).
	ct[len(ct)-1] ^= 0xFF

	_, err = ks.Decrypt(ct, nil)
	assert.Error(t, err)
}

func TestDecryptUnknownVersionFails(t *testing.T) {
	ks := newTestKeystore(t)
	ct, err := ks.Encrypt([]byte("test"), nil)
	require.NoError(t, err)

	// Change version to unknown.
	binary.BigEndian.PutUint32(ct[:4], 99)

	_, err = ks.Decrypt(ct, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown key version")
}

func TestMultiVersionKeyRing(t *testing.T) {
	key1, err := GenerateKey()
	require.NoError(t, err)
	key2, err := GenerateKey()
	require.NoError(t, err)

	ks, err := New(map[uint32][keySize]byte{1: key1, 2: key2})
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ks.ActiveVersion())

	// Encrypt produces version 2.
	ct, err := ks.Encrypt([]byte("new data"), nil)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), binary.BigEndian.Uint32(ct[:4]))

	// Old version 1 data still decrypts.
	ksOld, err := New(map[uint32][keySize]byte{1: key1})
	require.NoError(t, err)
	ctOld, err := ksOld.Encrypt([]byte("old data"), nil)
	require.NoError(t, err)

	got, err := ks.Decrypt(ctOld, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("old data"), got)
}

func TestAutoGenerateKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	ks, err := LoadOrGenerate(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), ks.ActiveVersion())

	// Verify file permissions. Skipped on Windows: NTFS uses ACLs, not Unix
	// mode bits, so os.FileInfo.Mode only reports a coarse approximation
	// (typically 0o666). Per-user security on Windows is enforced via the
	// inherited ACL of %LOCALAPPDATA%, not via chmod.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestLoadFromBase64File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	// Auto-generate, then reload.
	_, err := LoadOrGenerate(path)
	require.NoError(t, err)

	ks, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 1)
}

func TestKeyRingFileParsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	// Generate then rotate.
	_, err := LoadOrGenerate(path)
	require.NoError(t, err)

	_, err = RotateKey(path)
	require.NoError(t, err)

	ks, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 2)
}

func TestDuplicateVersionFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")

	key, _ := GenerateKey()
	_ = writeKeyRingFile(path, map[uint32][keySize]byte{1: key})

	// Manually append a duplicate version 1 line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n")
	_ = f.Close()

	_, err := LoadFromFile(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate version")
}

func TestInvalidBase64Fails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")
	_ = os.WriteFile(path, []byte("1:not-valid-base64!!!\n"), 0o600)

	_, err := LoadFromFile(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid base64")
}

func TestWrongKeyLengthFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")
	// 16 bytes instead of 32.
	_ = os.WriteFile(path, []byte("1:AAAAAAAAAAAAAAAAAAAAAA==\n"), 0o600)

	_, err := LoadFromFile(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be 32 bytes")
}

func TestRotateKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")
	_, err := LoadOrGenerate(path)
	require.NoError(t, err)

	v1, err := RotateKey(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), v1)

	v2, err := RotateKey(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(3), v2)

	ks, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Len(t, ks.Versions(), 3)
}

func TestRemoveKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")
	_, err := LoadOrGenerate(path)
	require.NoError(t, err)
	_, err = RotateKey(path)
	require.NoError(t, err)

	err = RemoveKey(path, 1)
	require.NoError(t, err)

	ks, err := LoadFromFile(path)
	require.NoError(t, err)
	assert.Len(t, ks.Versions(), 1)
	assert.Equal(t, uint32(2), ks.ActiveVersion())
}

func TestRemoveActiveVersionFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")
	_, err := LoadOrGenerate(path)
	require.NoError(t, err)

	err = RemoveKey(path, 1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot remove active key version")
}

func TestRemoveNonexistentVersionFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")
	_, err := LoadOrGenerate(path)
	require.NoError(t, err)

	err = RemoveKey(path, 99)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in ring")
}

func newTestKeystore(t *testing.T) *Keystore {
	t.Helper()
	key, err := GenerateKey()
	require.NoError(t, err)
	ks, err := New(map[uint32][keySize]byte{1: key})
	require.NoError(t, err)
	return ks
}
