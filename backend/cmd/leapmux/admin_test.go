package main

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
)

// setupTestDataDir creates a temp dir with an encryption key and migrated hub DB.
func setupTestDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Generate encryption key.
	_, err := keystore.LoadOrGenerate(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)

	// Create and migrate the DB.
	sqlDB, err := db.Open(filepath.Join(dir, "hub.db"))
	require.NoError(t, err)
	require.NoError(t, db.Migrate(sqlDB))
	_ = sqlDB.Close()

	return dir
}

func TestCLI_AddOAuthProvider_GitHub(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runAddOAuthProvider([]string{
		"--type", "github",
		"--client-id", "test-gh-client",
		"--client-secret", "test-gh-secret",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	// Verify provider was created in DB.
	sqlDB, _ := db.Open(filepath.Join(dir, "hub.db"))
	defer func() { _ = sqlDB.Close() }()
	q := gendb.New(sqlDB)

	providers, err := q.ListAllOAuthProviders(context.Background())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	assert.Equal(t, "github", providers[0].ProviderType)
	assert.Equal(t, "GitHub", providers[0].Name)
	assert.Equal(t, "read:user user:email", providers[0].Scopes)

	// Verify client_secret is encrypted (not plaintext).
	full, err := q.GetOAuthProviderByID(context.Background(), providers[0].ID)
	require.NoError(t, err)
	assert.NotEqual(t, []byte("test-gh-secret"), full.ClientSecret, "client_secret must be encrypted")
}

func TestCLI_AddOAuthProvider_OIDC_MissingIssuerURL(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runAddOAuthProvider([]string{
		"--type", "oidc",
		"--name", "My OIDC",
		"--client-id", "test-client",
		"--client-secret", "test-secret",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--issuer-url is required")
}

func TestCLI_AddOAuthProvider_PresetOverride(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runAddOAuthProvider([]string{
		"--type", "github",
		"--client-id", "test-client",
		"--client-secret", "test-secret",
		"--name", "Custom GitHub",
		"--scopes", "repo user",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	sqlDB, _ := db.Open(filepath.Join(dir, "hub.db"))
	defer func() { _ = sqlDB.Close() }()
	q := gendb.New(sqlDB)

	providers, _ := q.ListAllOAuthProviders(context.Background())
	require.Len(t, providers, 1)
	assert.Equal(t, "Custom GitHub", providers[0].Name)
	assert.Equal(t, "repo user", providers[0].Scopes)
}

func TestCLI_ListOAuthProviders(t *testing.T) {
	dir := setupTestDataDir(t)

	// Add a provider first.
	_ = runAddOAuthProvider([]string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "s1",
		"--data-dir", dir,
	})

	// List should succeed (we can't easily capture stdout, but no error = success).
	err := runListOAuthProviders([]string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_RemoveOAuthProvider(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runAddOAuthProvider([]string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "s1",
		"--data-dir", dir,
	})

	// Get the provider ID.
	sqlDB, _ := db.Open(filepath.Join(dir, "hub.db"))
	q := gendb.New(sqlDB)
	providers, _ := q.ListAllOAuthProviders(context.Background())
	_ = sqlDB.Close()
	require.Len(t, providers, 1)

	err := runRemoveOAuthProvider([]string{"--id", providers[0].ID, "--data-dir", dir})
	require.NoError(t, err)

	// Verify deleted.
	sqlDB, _ = db.Open(filepath.Join(dir, "hub.db"))
	q = gendb.New(sqlDB)
	providers, _ = q.ListAllOAuthProviders(context.Background())
	_ = sqlDB.Close()
	assert.Empty(t, providers)
}

func TestCLI_EnableDisableOAuthProvider(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runAddOAuthProvider([]string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "s1",
		"--data-dir", dir,
	})

	sqlDB, _ := db.Open(filepath.Join(dir, "hub.db"))
	q := gendb.New(sqlDB)
	providers, _ := q.ListAllOAuthProviders(context.Background())
	_ = sqlDB.Close()
	providerID := providers[0].ID

	// Disable.
	err := runSetOAuthProviderEnabled([]string{"--id", providerID, "--data-dir", dir}, false)
	require.NoError(t, err)

	sqlDB, _ = db.Open(filepath.Join(dir, "hub.db"))
	q = gendb.New(sqlDB)
	enabled, _ := q.ListEnabledOAuthProviders(context.Background())
	_ = sqlDB.Close()
	assert.Empty(t, enabled, "no providers should be enabled")

	// Re-enable.
	err = runSetOAuthProviderEnabled([]string{"--id", providerID, "--data-dir", dir}, true)
	require.NoError(t, err)

	sqlDB, _ = db.Open(filepath.Join(dir, "hub.db"))
	q = gendb.New(sqlDB)
	enabled, _ = q.ListEnabledOAuthProviders(context.Background())
	_ = sqlDB.Close()
	assert.Len(t, enabled, 1)
}

func TestCLI_RotateEncryptionKey(t *testing.T) {
	dir := setupTestDataDir(t)
	keyPath := filepath.Join(dir, "encryption.key")

	err := runRotateEncryptionKey([]string{"--data-dir", dir})
	require.NoError(t, err)

	ks, err := keystore.LoadFromFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 2)
}

func TestCLI_RotateEncryptionKey_TwiceIncrementsVersion(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runRotateEncryptionKey([]string{"--data-dir", dir})
	_ = runRotateEncryptionKey([]string{"--data-dir", dir})

	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.Equal(t, uint32(3), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 3)
}

func TestCLI_RemoveEncryptionKey_ActiveVersionFails(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runRemoveEncryptionKey([]string{"--version", "1", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot remove active")
}

func TestCLI_RemoveEncryptionKey_OldVersion(t *testing.T) {
	dir := setupTestDataDir(t)
	_ = runRotateEncryptionKey([]string{"--data-dir", dir})

	err := runRemoveEncryptionKey([]string{"--version", "1", "--data-dir", dir})
	require.NoError(t, err)

	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.Len(t, ks.Versions(), 1)
	assert.Equal(t, uint32(2), ks.ActiveVersion())
}

func TestCLI_ReencryptSecrets(t *testing.T) {
	dir := setupTestDataDir(t)
	keyPath := filepath.Join(dir, "encryption.key")

	// Add a provider with encrypted secret.
	_ = runAddOAuthProvider([]string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "original-secret",
		"--data-dir", dir,
	})

	// Rotate key.
	_ = runRotateEncryptionKey([]string{"--data-dir", dir})

	// Re-encrypt.
	err := runReencryptSecrets([]string{"--data-dir", dir})
	require.NoError(t, err)

	// Verify the encrypted secret can be decrypted with the new key only.
	ks, _ := keystore.LoadFromFile(keyPath)
	sqlDB, _ := db.Open(filepath.Join(dir, "hub.db"))
	q := gendb.New(sqlDB)
	providers, _ := q.ListAllOAuthProviders(context.Background())
	full, _ := q.GetOAuthProviderByID(context.Background(), providers[0].ID)
	_ = sqlDB.Close()

	// The ciphertext should now be version 2.
	assert.Equal(t, uint32(2), binary.BigEndian.Uint32(full.ClientSecret[:4]), "re-encrypted secret should be version 2")

	// Decrypting should return the original plaintext.
	aad := []byte("oauth_provider:" + providers[0].ID)
	plain, err := ks.Decrypt(full.ClientSecret, aad)
	require.NoError(t, err)
	assert.Equal(t, "original-secret", string(plain))
}

func TestCLI_ReencryptSecrets_Idempotent(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runAddOAuthProvider([]string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "secret",
		"--data-dir", dir,
	})

	// Running reencrypt without rotation should report 0 re-encrypted.
	err := runReencryptSecrets([]string{"--data-dir", dir})
	require.NoError(t, err)
}
