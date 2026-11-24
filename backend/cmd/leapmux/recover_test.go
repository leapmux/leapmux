package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func openTestStore(t *testing.T, dir string) store.Store {
	t.Helper()
	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	st, err := storeopen.Open(context.Background(), testHubConfig(dir), ks)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// openExistingTestStore opens an already-initialized Hub store without running
// migrations or re-seeding built-in oauth_clients, so rotation and key-version
// tests observe the exact ciphertext left by the command under test.
func openExistingTestStore(t *testing.T, dir string) store.Store {
	t.Helper()
	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	st, err := storeopen.OpenWithoutMigrate(context.Background(), testHubConfig(dir), ks)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// createTestUser seeds a user row through the store so all encrypted columns
// carry valid ciphertext under dir's encryption.key. The hash is a placeholder:
// every test that goes through `recover reset-password` overwrites it.
func createTestUser(t *testing.T, dir, username string) *store.User {
	t.Helper()
	st := openTestStore(t, dir)
	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:                    userID,
		Username:              username,
		PasswordHash:          "placeholder",
		DisplayName:           username,
		FirstCredentialExempt: true,
	}))
	user, err := st.Users().GetByUsername(context.Background(), username)
	require.NoError(t, err)
	return user
}

func setupTestDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Generate encryption key.
	_, err := keystore.LoadOrGenerate(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)

	// Create and migrate the DB.
	sqlDB, err := sqlite.OpenDB(filepath.Join(dir, "hub.db"), sqlitedb.Config{})
	require.NoError(t, err)
	require.NoError(t, sqlite.MigrateDB(sqlDB))
	_ = sqlDB.Close()

	return dir
}

func openTestDB(t *testing.T, dir string) (*sql.DB, *gendb.Queries) {
	t.Helper()
	sqlDB, err := sqlite.OpenDB(filepath.Join(dir, "hub.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB, gendb.New(sqlDB)
}

// seedEncryptedOAuthProvider stores one OAuth provider whose client secret
// is encrypted under the data dir's active key version.
func seedEncryptedOAuthProvider(t *testing.T, dir, secret string) {
	t.Helper()
	ctx := context.Background()
	st := openTestStore(t, dir)
	provID := id.Generate()
	require.NoError(t, st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
		ID:           provID,
		ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_GITHUB,
		Name:         "github",
		ClientID:     "c1",
		ClientSecret: []byte(secret),
		Enabled:      true,
	}))
}

// seedCaptchaTurnstileSecret stores the captcha.turnstile row with its
// secret half encrypted under the data dir's active key version.
func seedCaptchaTurnstileSecret(t *testing.T, dir string) {
	t.Helper()
	ctx := context.Background()
	st := openTestStore(t, dir)
	value := `{"site_key":"1x000AA"}`
	require.NoError(t, st.Settings().Upsert(ctx, store.UpsertSettingParams{
		Key: "captcha.turnstile", Value: &value, Secret: []byte(`{"secret_key":"captcha-secret"}`),
	}))
}

func listSessions(t *testing.T, st store.Store, userID string) []store.UserSession {
	t.Helper()
	page, err := st.Sessions().ListByUserID(context.Background(), store.ListUserSessionsParams{
		UserID:     userid.MustNew(userID),
		PageParams: store.PageParams{Limit: 1000},
	}, time.Now().UTC())
	require.NoError(t, err)
	return page.Rows
}

func createTestSession(t *testing.T, st store.Store, userID string, expiresAt time.Time) string {
	t.Helper()

	sessionID := id.Generate()
	err := st.Sessions().Create(context.Background(), store.CreateSessionParams{
		SecretHash: hubtestutil.TestSecretHasher().Hash(id.Generate()),
		ID:         sessionID,
		UserID:     userid.MustNew(userID),
		ExpiresAt:  expiresAt,
		UserAgent:  "test",
		IPAddress:  "127.0.0.1",
	})
	require.NoError(t, err)
	return sessionID
}

func TestMintResolvedUserID(t *testing.T) {
	t.Run("mints a populated row", func(t *testing.T) {
		uid, err := mintResolvedUserID(&store.User{ID: "u-1", Username: "alice"})
		require.NoError(t, err)
		assert.Equal(t, "u-1", uid.String())
	})

	t.Run("refuses a blank id and names the user", func(t *testing.T) {
		uid, err := mintResolvedUserID(&store.User{Username: "bob"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"bob"`,
			"the operator has only the username to go on: the error must name it")
		assert.True(t, uid.IsZero(), "a refused mint must not return a usable id")
	})
}

func TestCLI_RotateEncryptionKey(t *testing.T) {
	dir := setupTestDataDir(t)
	keyPath := filepath.Join(dir, "encryption.key")

	err := runRecover(t, "encryption-key", "rotate", "--data-dir", dir)
	require.NoError(t, err)

	ks, err := keystore.LoadFromFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 2)
}

func TestCLI_RotateEncryptionKey_TwiceIncrementsVersion(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runRecover(t, "encryption-key", "rotate", "--data-dir", dir)
	_ = runRecover(t, "encryption-key", "rotate", "--data-dir", dir)

	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.Equal(t, uint32(3), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 3)
}

func TestCLI_RemoveEncryptionKey_ActiveVersionFails(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot remove active")
}

func TestCLI_RemoveEncryptionKey_OldVersion(t *testing.T) {
	dir := setupTestDataDir(t)
	_ = runRecover(t, "encryption-key", "rotate", "--data-dir", dir)

	err := runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir)
	require.NoError(t, err)

	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.Len(t, ks.Versions(), 1)
	assert.Equal(t, uint32(2), ks.ActiveVersion())
}

func TestCLI_RemoveEncryptionKey_RefusesInvalidVersion(t *testing.T) {
	for _, version := range []string{"0", "-1", "4294967296", "4294967297", "18446744073709551615"} {
		t.Run(version, func(t *testing.T) {
			dir := setupTestDataDir(t)
			require.NoError(t, runRecover(t, "encryption-key", "rotate", "--data-dir", dir))
			keyPath := filepath.Join(dir, "encryption.key")
			before, err := os.ReadFile(keyPath)
			require.NoError(t, err)

			err = runRecover(t, "encryption-key", "remove", version, "--data-dir", dir)
			require.Error(t, err)
			assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code, "an invalid <version> is a usage error")

			after, err := os.ReadFile(keyPath)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(before, after), "an invalid version must not change the key ring")
			ks, err := keystore.LoadFromFile(keyPath)
			require.NoError(t, err)
			assert.Equal(t, []uint32{1, 2}, ks.Versions())
			assert.Equal(t, uint32(2), ks.ActiveVersion())
		})
	}
}

func TestCLI_RemoveEncryptionKey_ReportsAbsentMaximumVersion(t *testing.T) {
	dir := setupTestDataDir(t)
	keyPath := filepath.Join(dir, "encryption.key")
	before, err := os.ReadFile(keyPath)
	require.NoError(t, err)

	err = runRecover(t, "encryption-key", "remove", "4294967295", "--data-dir", dir)
	assert.ErrorContains(t, err, "key version 4294967295 not in ring")

	after, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(before, after), "an absent version must not change the key ring")
}

func TestCLI_ReencryptSecrets(t *testing.T) {
	dir := setupTestDataDir(t)
	ctx := context.Background()

	seedEncryptedOAuthProvider(t, dir, "original-secret")

	// Rotate key.
	require.NoError(t, runRecover(t, "encryption-key", "rotate", "--data-dir", dir))

	// Re-encrypt.
	require.NoError(t, runRecover(t, "encryption-key", "reencrypt", "--data-dir", dir))

	// Verify the ciphertext version is now 2 on disk and decrypts through the store.
	_, q := openTestDB(t, dir)
	providers, err := q.ListAllOAuthProviders(ctx)
	require.NoError(t, err)
	require.Len(t, providers, 1)
	full, err := q.GetOAuthProviderByID(ctx, providers[0].ID)
	require.NoError(t, err)

	ver, err := full.ClientSecret.Version()
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ver, "re-encrypted secret should be version 2")

	st := openTestStore(t, dir)
	got, err := st.OAuthProviders().GetByID(ctx, providers[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "original-secret", string(got.ClientSecret))
}

func TestCLI_ReencryptSecrets_Idempotent(t *testing.T) {
	dir := setupTestDataDir(t)
	ctx := context.Background()

	seedEncryptedOAuthProvider(t, dir, "secret")

	ksBefore, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	_, q := openTestDB(t, dir)
	providers, err := q.ListAllOAuthProviders(ctx)
	require.NoError(t, err)
	require.Len(t, providers, 1)
	fullBefore, err := q.GetOAuthProviderByID(ctx, providers[0].ID)
	require.NoError(t, err)
	before := append([]byte(nil), fullBefore.ClientSecret.Bytes()...)

	require.NoError(t, runRecover(t, "encryption-key", "reencrypt", "--data-dir", dir))

	fullAfter, err := q.GetOAuthProviderByID(ctx, providers[0].ID)
	require.NoError(t, err)
	assert.Equal(t, before, fullAfter.ClientSecret.Bytes(), "ciphertext must stay byte-identical when already at the active version")
	ver, err := fullAfter.ClientSecret.Version()
	require.NoError(t, err)
	assert.Equal(t, ksBefore.ActiveVersion(), ver)

	st := openTestStore(t, dir)
	got, err := st.OAuthProviders().GetByID(ctx, providers[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(got.ClientSecret))
}

func TestCLI_RemoveEncryptionKey_RefusesWhenReferenced(t *testing.T) {
	dir := setupTestDataDir(t)

	// Add a provider — its client secret is encrypted under key version 1.
	seedEncryptedOAuthProvider(t, dir, "s1")

	// Rotate to version 2; the provider secret is still encrypted under v1.
	require.NoError(t, runRecover(t, "encryption-key", "rotate", "--data-dir", dir))

	// Removing v1 must be refused — it still encrypts the provider secret.
	err := runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still encrypts")

	// After reencrypt migrates the secret to v2, v1 is unreferenced and
	// removal succeeds.
	require.NoError(t, runRecover(t, "encryption-key", "reencrypt", "--data-dir", dir))
	require.NoError(t, runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir))

	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.Len(t, ks.Versions(), 1)
}

func TestCLI_RemoveEncryptionKey_RefusesWhenCaptchaSecretReferenced(t *testing.T) {
	dir := setupTestDataDir(t)
	ctx := context.Background()

	// Configure turnstile: the row lands under key version 1.
	seedCaptchaTurnstileSecret(t, dir)

	// Rotate to version 2; the captcha row is still encrypted under v1.
	require.NoError(t, runRecover(t, "encryption-key", "rotate", "--data-dir", dir))

	// Removing v1 must be refused — it still encrypts the hub_settings row.
	err := runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still encrypts")
	assert.Contains(t, err.Error(), "hub settings")

	// Reencrypt migrates the row; it must decrypt under v2 with the original plaintext.
	require.NoError(t, runRecover(t, "encryption-key", "reencrypt", "--data-dir", dir))
	st := openExistingTestStore(t, dir)
	refs, err := st.KeyMaintenance().CountKeyVersionRefs(ctx, 1)
	require.NoError(t, err)
	assert.Empty(t, refs)

	row, err := st.Settings().Get(ctx, "captcha.turnstile")
	require.NoError(t, err)
	require.NoError(t, row.SecretDecryptErr)
	var ts captcha.TurnstileRow
	require.NoError(t, json.Unmarshal(row.Secret, &ts))
	assert.Equal(t, "captcha-secret", ts.SecretKey)

	// With the row migrated, v1 is unreferenced and removal succeeds.
	require.NoError(t, runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir))
}

func TestCLI_RotatePepper_RequiresYes(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runRecover(t, "encryption-key", "rotate-pepper", "--data-dir", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--yes")
	for _, effect := range []string{"every session", "Worker auth token", "API and delegation tokens", "app secrets", "registration keys", "device codes", "OAuth authorization codes"} {
		assert.Contains(t, err.Error(), effect)
	}

	// Without --yes the pepper is left untouched.
	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, ks.Pepper())
}

func TestCLI_RotatePepper_ChangesPepperLeavesKeysIntact(t *testing.T) {
	dir := setupTestDataDir(t)
	keyPath := filepath.Join(dir, "encryption.key")

	before, err := keystore.LoadFromFile(keyPath)
	require.NoError(t, err)

	require.NoError(t, runRecover(t, "encryption-key", "rotate-pepper", "--yes", "--data-dir", dir))

	after, err := keystore.LoadFromFile(keyPath)
	require.NoError(t, err)
	assert.NotEqual(t, before.Pepper(), after.Pepper(), "pepper must change")
	assert.Equal(t, before.ActiveVersion(), after.ActiveVersion(), "encryption keys must be unchanged")
	assert.Equal(t, before.Versions(), after.Versions())
}

func TestCLI_PasswordReset(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	// Record original hash and create a session that should be deleted after reset.
	st := openTestStore(t, dir)
	original, err := st.Users().GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	createTestSession(t, st, user.ID, time.Now().UTC().Add(24*time.Hour))

	err = runRecover(t, "reset-password", "id:"+user.ID, "--password", "NewPassword2!", "--data-dir", dir)
	require.NoError(t, err)

	// Verify hash changed.
	st = openTestStore(t, dir)

	updated, err := st.Users().GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.NotEqual(t, original.PasswordHash, updated.PasswordHash)

	// Verify sessions deleted.
	sessions := listSessions(t, st, user.ID)
	assert.Empty(t, sessions)
}

func TestCLI_PasswordReset_PreservesOtherUserSessions(t *testing.T) {
	dir := setupTestDataDir(t)
	alice := createTestUser(t, dir, "alice")
	bob := createTestUser(t, dir, "bob")

	// Create sessions for both users.
	st := openTestStore(t, dir)
	createTestSession(t, st, alice.ID, time.Now().UTC().Add(24*time.Hour))
	bobSessionID := createTestSession(t, st, bob.ID, time.Now().UTC().Add(24*time.Hour))

	// Reset alice's password.
	err := runRecover(t, "reset-password", "id:"+alice.ID, "--password", "NewPassword2!", "--data-dir", dir)
	require.NoError(t, err)

	// Verify bob's session is still there.
	st = openTestStore(t, dir)

	sessions := listSessions(t, st, bob.ID)
	require.Len(t, sessions, 1)
	assert.Equal(t, bobSessionID, sessions[0].ID)
}

func TestCLI_PasswordReset_MissingPassword(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runRecover(t, "reset-password", "id:"+user.ID, "--data-dir", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a terminal")
}

// The refusal is not_found, and it echoes the <user> that the operator typed,
// in either form.
func TestCLI_PasswordReset_NotFound(t *testing.T) {
	for _, user := range []string{"id:nonexistent-id", "nobody"} {
		t.Run(user, func(t *testing.T) {
			dir := setupTestDataDir(t)

			err := runRecover(t, "reset-password", user, "--password", "NewPassword2!", "--data-dir", dir)
			require.Error(t, err)
			assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
			assert.Contains(t, err.Error(), fmt.Sprintf("no user %q", user))
		})
	}
}

func TestCLI_PasswordReset_ByUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runRecover(t, "reset-password", "alice", "--password", "NewPassword2!", "--data-dir", dir)
	require.NoError(t, err)

	st := openTestStore(t, dir)
	updated, err := st.Users().GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.NotEqual(t, "placeholder", updated.PasswordHash)
}

// A second <user> is refused before any lookup, even when the two name two
// different users, and neither user's password changes.
func TestCLI_PasswordReset_RefusesASecondUser(t *testing.T) {
	dir := setupTestDataDir(t)
	alice := createTestUser(t, dir, "alice")
	createTestUser(t, dir, "bob")

	err := runRecover(t, "reset-password", "id:"+alice.ID, "bob", "--password", "NewPassword2!", "--data-dir", dir)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), `unexpected argument "bob"`)

	st := openTestStore(t, dir)
	for _, username := range []string{"alice", "bob"} {
		user, err := st.Users().GetByUsername(context.Background(), username)
		require.NoError(t, err)
		assert.Equalf(t, "placeholder", user.PasswordHash, "%s's password must not change", username)
	}
}

// A missing or empty <user> is a usage error, and the command opens no
// database: a usage error changes nothing on disk.
func TestCLI_PasswordReset_RequiresAUser(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		message string
	}{
		{"missing", nil, "missing argument <user>"},
		{"empty", []string{""}, "<user> is empty"},
		{"an id prefix alone", []string{"id:"}, `<user> "id:" has no id after the "id:" prefix`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "hub")
			args := append([]string{"reset-password"}, tc.args...)
			err := runRecover(t, append(args, "--password", "NewPassword2!", "--data-dir", dir)...)
			require.Error(t, err)
			assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
			assert.Contains(t, err.Error(), tc.message)
			assert.NoDirExists(t, dir, "a usage error must not create the data directory")
		})
	}
}

func TestCLI_PasswordReset_RevokesAPIAndDelegationTokens(t *testing.T) {
	dir := setupTestDataDir(t)
	ctx := context.Background()
	user := createTestUser(t, dir, "alice")

	st := openTestStore(t, dir)
	tokenID := id.Generate()
	require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
		Issuance:         store.APITokenIssuanceAuthorizationCode,
		ID:               tokenID,
		UserID:           userid.MustNew(user.ID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       []byte("hash"),
	}))
	before, err := st.Users().GetByID(ctx, user.ID)
	require.NoError(t, err)

	require.NoError(t, runRecover(t, "reset-password", "id:"+user.ID, "--password", "NewPassword2!", "--data-dir", dir))

	st = openTestStore(t, dir)
	tokens, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
		UserID:         &user.ID,
		IncludeRevoked: true,
		PageParams:     store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	require.Len(t, tokens.Rows, 1)
	require.NotNil(t, tokens.Rows[0].RevokedAt)
	updated, err := st.Users().GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Greater(t, updated.AuthGeneration, before.AuthGeneration,
		"a password reset must advance the user's credential epoch")
}

func TestCLI_DBPath(t *testing.T) {
	dir := setupTestDataDir(t)

	out, err := runRecoverOutput(t, "db", "path", "--data-dir", dir)
	require.NoError(t, err)
	assert.Equal(t, testHubConfig(dir).SQLiteDBPath(), strings.TrimSpace(out))
}

// db path reads the configuration only: it creates no data directory.
func TestCLI_DBPath_CreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")

	out, err := runRecoverOutput(t, "db", "path", "--data-dir", dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "hub.db"), strings.TrimSpace(out))
	assert.NoDirExists(t, dir)
}

// --config selects the database through the storage settings of the file,
// and --data-dir still replaces the data directory of the file.
func TestCLI_DBPath_ReadsTheConfigurationFile(t *testing.T) {
	root := t.TempDir()
	configFile := filepath.Join(root, "hub.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("storage:\n  sqlite:\n    path: /srv/leapmux/hub.db\n"), 0o600))

	out, err := runRecoverOutput(t, "db", "path", "--config", configFile)
	require.NoError(t, err)
	assert.Equal(t, "/srv/leapmux/hub.db", strings.TrimSpace(out))

	require.NoError(t, os.WriteFile(configFile, []byte("data_dir: from-file\n"), 0o600))
	out, err = runRecoverOutput(t, "db", "path", "--config", configFile)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "from-file", "hub.db"), strings.TrimSpace(out), "a relative data_dir follows the file")

	dataDir := t.TempDir()
	out, err = runRecoverOutput(t, "db", "path", "--config", configFile, "--data-dir", dataDir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dataDir, "hub.db"), strings.TrimSpace(out), "--data-dir replaces the data directory of the file")
}

// A --config that the operator typed must exist: the loader would skip an
// absent file without a word, and the command would then open the SQLite
// database of the data directory instead of the configured backend.
func TestCLI_RefusesAnAbsentConfigurationFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	err := runRecover(t, "db", "version", "--config", filepath.Join(t.TempDir(), "typo.yaml"), "--data-dir", dir)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "cannot read the configuration file")
	assert.NoDirExists(t, dir, "the refusal must not create the data directory")
}

// Without --config, the command reads no configuration file and no
// variable: the default file of the Hub would select the wrong database for
// the data directory of a solo or dev instance.
func TestCLI_ReadsNoConfigurationWithoutTheFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LEAPMUX_HUB_STORAGE_TYPE", "postgres")
	hubFile := filepath.Join(home, ".config", "leapmux", "hub", "hub.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(hubFile), 0o700))
	require.NoError(t, os.WriteFile(hubFile, []byte("storage:\n  sqlite:\n    path: /srv/elsewhere.db\n"), 0o600))
	dir := setupTestDataDir(t)

	out, err := runRecoverOutput(t, "db", "path", "--data-dir", dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "hub.db"), strings.TrimSpace(out))

	require.NoError(t, runRecover(t, "db", "version", "--data-dir", dir),
		"the command opens the SQLite database despite LEAPMUX_HUB_STORAGE_TYPE")
}

func TestCLI_DBVersion(t *testing.T) {
	dir := setupTestDataDir(t)

	out, err := runRecoverOutput(t, "db", "version", "--data-dir", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "Current schema version:")
	assert.Contains(t, out, "Latest available version:")
}

func TestCLI_DBMigrate_AlreadyLatest(t *testing.T) {
	dir := setupTestDataDir(t)

	out, err := runRecoverOutput(t, "db", "migrate", "--data-dir", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "Already at latest version.")
}

// --to migrates to the version it gives. The latest version is a valid
// target, and the command reports it.
func TestCLI_DBMigrate_ToTheLatestVersion(t *testing.T) {
	dir := setupTestDataDir(t)
	st := openTestStore(t, dir)
	latest := st.Migrator().LatestVersion()
	require.NoError(t, st.Close())

	out, err := runRecoverOutput(t, "db", "migrate", "--to", strconv.FormatInt(latest, 10), "--data-dir", dir)
	require.NoError(t, err)
	assert.Contains(t, out, fmt.Sprintf("Migration complete. Current version: %d", latest))
}

// A negative --to is a usage error, and the command opens no database.
func TestCLI_DBMigrate_RefusesANegativeVersion(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")

	err := runRecover(t, "db", "migrate", "--to", "-3", "--data-dir", dir)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "--to -3 is not a schema version")
	assert.NoDirExists(t, dir, "a usage error must not create the data directory")
}

// ---- recover bootstrap ----

func TestCLI_BootstrapCreateAdmin(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runRecover(t, "create-admin", "firstadmin", "--password", "AdminPassword1!", "--data-dir", dir)
	require.NoError(t, err)

	_, q := openTestDB(t, dir)
	user, err := q.GetUserByUsername(context.Background(), "firstadmin")
	require.NoError(t, err)
	assert.NotZero(t, user.IsAdmin, "the bootstrapped user is always an administrator")

	// The precondition refuses a second bootstrap once any admin exists, and
	// it points at the online command that creates every later user.
	err = runRecover(t, "create-admin", "secondadmin", "--password", "AdminPassword1!", "--data-dir", dir)
	require.Error(t, err)
	failure := cli.FromError(err)
	assert.Equal(t, cli.CodeConflict, failure.Code)
	assert.Contains(t, failure.Message, "an administrator already exists")
	assert.Contains(t, strings.Join(failure.Hints, "\n"), "leapmux admin user create --admin")
	_, err = q.GetUserByUsername(context.Background(), "secondadmin")
	require.Error(t, err, "the refusal must happen before the insert")
}

// TestCLI_BootstrapCreateAdmin_RefusesAReservedUsername pins the guard
// that every other creation path applies.
//
// This one is OFFLINE, so nothing stands behind it. A row named "solo" in
// a non-solo database becomes the synthetic local IPC identity when the same
// data directory opens with `leapmux solo`. The interceptor returns that
// identity before the email and admin checks.
func TestCLI_BootstrapCreateAdmin_RefusesAReservedUsername(t *testing.T) {
	for _, name := range []string{"solo", "SOLO", " Solo "} {
		dir := setupTestDataDir(t)
		err := runRecover(t, "create-admin", name, "--password", "AdminPassword1!", "--data-dir", dir)
		require.Errorf(t, err, "%q normalizes to the reserved solo username", name)
		assert.Contains(t, err.Error(), "reserved username")

		// And nothing was written.
		_, q := openTestDB(t, dir)
		_, err = q.GetUserByUsername(context.Background(), "solo")
		require.Error(t, err, "the refusal must happen before the insert")
	}
}

// A missing or blank <username> is a usage error, and the command opens no
// database: a usage error changes nothing on disk.
func TestCLI_BootstrapCreateAdmin_MissingUsername(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		message string
	}{
		{"missing", nil, "missing argument <username>"},
		{"blank", []string{"  "}, "<username> is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "hub")
			args := append([]string{"create-admin"}, tc.args...)
			err := runRecover(t, append(args, "--password", "AdminPassword1!", "--data-dir", dir)...)
			require.Error(t, err)
			assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
			assert.Contains(t, err.Error(), tc.message)
			assert.NoDirExists(t, dir, "a usage error must not create the data directory")
		})
	}
}

func TestCLI_BootstrapCreateAdmin_SucceedsWhenOnlyNonAdminsExist(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "bob")

	err := runRecover(t, "create-admin", "alice", "--password", "AdminPassword1!", "--data-dir", dir)
	require.NoError(t, err)

	_, q := openTestDB(t, dir)
	alice, err := q.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)
	assert.NotZero(t, alice.IsAdmin)
}

func TestCLI_BootstrapCreateAdmin_DuplicateUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "alice")

	err := runRecover(t, "create-admin", "alice", "--password", "AdminPassword1!", "--data-dir", dir)
	require.Error(t, err)
	// The bootstrap admin carries no email, so the only unique index it can
	// lose is users.username. The typed error keeps that field readable to a
	// caller, instead of only spelling it into a sentence.
	var taken *service.FieldTakenError
	require.ErrorAs(t, err, &taken)
	assert.Equal(t, "username", taken.Field)
	assert.Equal(t, "alice", taken.Value)
	assert.Equal(t, `username "alice" is already taken`, err.Error())
}

// TestCLI_BootstrapCreateAdmin_PassesAStoreFaultThrough pins the arm beside
// the conflict one: a create that fails for any OTHER reason keeps its own
// error.
//
// The conflict arm rewrites the failure into a FieldTakenError, which names
// the username as the cause. That is correct for a unique-index violation and
// wrong for everything else, so an operator whose database is damaged would
// read "username is already taken" and go looking at a row that is not the
// problem.
//
// The seam is the second write of CreateUser's one transaction: the user row
// goes in first, then sections.InitDefaults seeds the sidebar. Installing a
// BEFORE INSERT trigger on workspace_sections keeps the schema valid (so store
// startup and keyring verification pass) while forcing sections.InitDefaults to
// fail mid-transaction after the admin gate reports an empty hub.
func TestCLI_BootstrapCreateAdmin_PassesAStoreFaultThrough(t *testing.T) {
	dir := setupTestDataDir(t)
	sqlDB, _ := openTestDB(t, dir)
	_, err := sqlDB.Exec(`CREATE TRIGGER fail_workspace_sections_insert
		BEFORE INSERT ON workspace_sections
		BEGIN SELECT RAISE(ABORT, 'forced workspace_sections fault'); END`)
	require.NoError(t, err)

	err = runRecover(t, "create-admin", "firstadmin", "--password", "AdminPassword1!", "--data-dir", dir)
	require.Error(t, err)

	var taken *service.FieldTakenError
	require.NotErrorAs(t, err, &taken, "a store fault must not be reported as a taken username")
	assert.Contains(t, err.Error(), "init sections", "the cause must survive the passthrough")

	// The transaction covers both writes, so the failed seeding takes the user
	// row with it.
	_, q := openTestDB(t, dir)
	_, getErr := q.GetUserByUsername(context.Background(), "firstadmin")
	require.Error(t, getErr, "a failed create must leave no half-built user behind")
}

// ---- recover password reset ----

// The bootstrap verb validates four things before it writes, and only the
// empty-username arm was covered. Each of these is a refusal an operator
// can hit on their very first command against a new hub.
func TestCLI_BootstrapCreateAdmin_RefusesAMalformedUsername(t *testing.T) {
	// SanitizeSlug lowercases and trims first, so "UPPER" is a VALID
	// username that becomes "upper". These are the shapes it refuses.
	for _, username := range []string{"has space", "under_score", "-leading", "trailing-", "double--hyphen", strings.Repeat("a", 33)} {
		dir := setupTestDataDir(t)
		// "--" ends the flags, so a username that starts with a dash is the
		// argument and not a flag.
		err := runRecover(t, "create-admin", "--password", "AdminPassword1!", "--data-dir", dir, "--", username)
		require.Errorf(t, err, "username %q must be refused", username)
		assert.Contains(t, err.Error(), "username")
	}
}

func TestCLI_BootstrapCreateAdmin_NormalizesTheUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	require.NoError(t, runRecover(t, "create-admin", "  FirstAdmin  ", "--password", "AdminPassword1!", "--data-dir", dir))
	_, q := openTestDB(t, dir)
	user, err := q.GetUserByUsername(context.Background(), "firstadmin")
	require.NoError(t, err, "the username is lowercased and trimmed before it is stored")
	assert.Equal(t, "firstadmin", user.Username)
}

func TestCLI_BootstrapCreateAdmin_RefusesAWeakPassword(t *testing.T) {
	dir := setupTestDataDir(t)
	err := runRecover(t, "create-admin", "firstadmin", "--password", "short", "--data-dir", dir)
	require.Error(t, err)

	// The refusal must happen BEFORE the row is written.
	_, q := openTestDB(t, dir)
	_, getErr := q.GetUserByUsername(context.Background(), "firstadmin")
	require.Error(t, getErr, "a refused bootstrap must leave no user behind")
}

// A non-terminal stdin cannot be prompted, and reading a password from a
// pipe would put it somewhere a pipe can be recorded.
func TestCLI_BootstrapCreateAdmin_RefusesAMissingPasswordOffATerminal(t *testing.T) {
	dir := setupTestDataDir(t)
	err := runRecover(t, "create-admin", "firstadmin", "--data-dir", dir)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "--password is required (stdin is not a terminal)")
}

// --display-name is the flag every E2E hub bootstraps with, and nothing
// covered it: an empty one falls back to the username.
func TestCLI_BootstrapCreateAdmin_DisplayName(t *testing.T) {
	dir := setupTestDataDir(t)
	require.NoError(t, runRecover(t, "create-admin", "firstadmin", "--password", "AdminPassword1!", "--display-name", "First Admin", "--data-dir", dir))
	_, q := openTestDB(t, dir)
	user, err := q.GetUserByUsername(context.Background(), "firstadmin")
	require.NoError(t, err)
	assert.Equal(t, "First Admin", user.DisplayName)

	other := setupTestDataDir(t)
	require.NoError(t, runRecover(t, "create-admin", "nodisplayname", "--password", "AdminPassword1!", "--data-dir", other))
	_, q2 := openTestDB(t, other)
	fallback, err := q2.GetUserByUsername(context.Background(), "nodisplayname")
	require.NoError(t, err)
	assert.Equal(t, "nodisplayname", fallback.DisplayName, "an omitted display name falls back to the username")
}

// TestRecover_RefusesAnUnknownCommand pins the refusal that an operator reads
// for a command that the recover tree does not hold, at each level.
func TestRecover_RefusesAnUnknownCommand(t *testing.T) {
	o := runTestCLI(t, "recover", "zzz")
	assert.Equal(t, cli.ExitUsage, o.code)
	assert.Contains(t, o.stderr, `unknown command "zzz" for "leapmux recover"`)

	o = runTestCLI(t, "recover", "db", "zzz")
	assert.Equal(t, cli.ExitUsage, o.code)
	assert.Contains(t, o.stderr, `unknown command "zzz" for "leapmux recover db"`)

	// A unique prefix of a group resolves, and the group alone prints its
	// help.
	o = runTestCLI(t, "recover", "enc")
	assert.Equal(t, cli.ExitOK, o.code, o.stderr)
	assert.Contains(t, o.stdout, "Usage:\n  leapmux recover encryption-key <command>\n")
	assert.Contains(t, o.stdout, "rotate")
}

func TestCLI_PasswordReset_DeletesPasskeys(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "pkuser")
	ctx := context.Background()

	st := openTestStore(t, dir)
	pkID := id.Generate()
	require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
		ID: pkID, UserID: user.ID, CredentialID: []byte("cred-" + pkID),
		PublicKey: []byte("pubkey"), SignCount: 0, Transports: "[]",
		FriendlyName: "Phone", CreatedAt: time.Now().UTC(),
	}))

	require.NoError(t, runRecover(t, "reset-password", "id:"+user.ID, "--password", "NewPassword2!", "--data-dir", dir))

	st = openTestStore(t, dir)
	rows, err := st.PasskeyCredentials().ListByUser(ctx, user.ID)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestCLI_Reencrypt_MigratesPasskeyPublicKey(t *testing.T) {
	dir := setupTestDataDir(t)
	ctx := context.Background()
	keyPath := filepath.Join(dir, "encryption.key")

	st := openTestStore(t, dir)
	user := storetest.SeedUser(t, st, "pkreenc")
	pkID := id.Generate()
	plain := []byte("cose-public-key-bytes")
	require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
		ID: pkID, UserID: user.ID, CredentialID: []byte("cred-" + pkID),
		PublicKey: plain, SignCount: 0, Transports: "[]",
		FriendlyName: "Key", CreatedAt: time.Now().UTC(),
	}))

	require.NoError(t, runRecover(t, "encryption-key", "rotate", "--data-dir", dir))
	require.NoError(t, runRecover(t, "encryption-key", "reencrypt", "--data-dir", dir))

	ks2, err := keystore.LoadFromFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ks2.ActiveVersion())

	st2 := openExistingTestStore(t, dir)
	refs, err := st2.KeyMaintenance().CountKeyVersionRefs(ctx, 1)
	require.NoError(t, err)
	assert.Empty(t, refs)

	rows, err := st2.PasskeyCredentials().ListByUser(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, plain, rows[0].PublicKey)
}

func TestCLI_RemoveEncryptionKey_RefusesWhenPasskeyReferenced(t *testing.T) {
	dir := setupTestDataDir(t)
	ctx := context.Background()

	st := openTestStore(t, dir)
	user := storetest.SeedUser(t, st, "pkref")
	pkID := id.Generate()
	require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
		ID: pkID, UserID: user.ID, CredentialID: []byte("cred-" + pkID),
		PublicKey: []byte("cipher"), SignCount: 0, Transports: "[]",
		FriendlyName: "Key", CreatedAt: time.Now().UTC(),
	}))

	require.NoError(t, runRecover(t, "encryption-key", "rotate", "--data-dir", dir))
	err := runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still encrypts")
	assert.Contains(t, err.Error(), "passkey")
}

func readBuiltInClientCiphertext(t *testing.T, dir string) []byte {
	t.Helper()
	sqlDB, _ := openTestDB(t, dir)
	var raw []byte
	require.NoError(t, sqlDB.QueryRow(
		`SELECT client_name FROM oauth_clients WHERE client_id = ?`,
		oauthapp.ControlCLIClientID,
	).Scan(&raw))
	require.NotEmpty(t, raw)
	return raw
}

func TestRecoverEncryptionKey_ReencryptAndRemoveAcrossAllTables(t *testing.T) {
	dir := setupTestDataDir(t)
	ctx := context.Background()

	// Seed persistent tables under key version 1 (openTestStore also seeds
	// built-in oauth_clients under v1).
	st := openTestStore(t, dir)
	user := storetest.SeedUser(t, st, "rotuser")
	sessID := createTestSession(t, st, user.ID, time.Now().UTC().Add(time.Hour))
	wsID := storetest.SeedWorkspace(t, st, user.ID, "Encrypted Workspace")
	secID := id.Generate()
	require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
		ID:          secID,
		UserID:      userid.MustNew(user.ID),
		Name:        "Encrypted Section",
		Position:    "a0",
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}))
	prov := storetest.SeedOAuthProvider(t, st, "rotprov")
	pkID := id.Generate()
	require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
		ID: pkID, UserID: user.ID, CredentialID: []byte("cred-" + pkID),
		PublicKey: []byte("rot-pubkey"), Transports: "[]", FriendlyName: "RotKey",
		CreatedAt: time.Now().UTC(),
	}))
	settingVal := `{"site_key":"rot-site"}`
	require.NoError(t, st.Settings().Upsert(ctx, store.UpsertSettingParams{
		Key: "captcha.turnstile", Value: &settingVal, Secret: []byte(`{"secret_key":"rot-secret"}`),
	}))
	require.NoError(t, st.Close())

	beforeClientCT := readBuiltInClientCiphertext(t, dir)

	// Rotate to v2.
	require.NoError(t, runRecover(t, "encryption-key", "rotate", "--data-dir", dir))

	// remove 1 must fail listing affected tables and must not modify
	// built-in oauth_clients ciphertext (it uses OpenWithoutMigrate).
	err := runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir)
	require.Error(t, err)
	for _, want := range []string{"still encrypts", "users", "workspaces", "OAuth clients", "hub settings"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.Equal(t, beforeClientCT, readBuiltInClientCiphertext(t, dir),
		"remove must not modify built-in oauth_clients ciphertext")

	// Re-encrypt all persistent tables to v2, then remove v1.
	require.NoError(t, runRecover(t, "encryption-key", "reencrypt", "--data-dir", dir))
	require.NoError(t, runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir))

	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.Equal(t, []uint32{2}, ks.Versions())

	// Reopen without migrating or reseeding under the v2-only keystore and verify
	// every seeded record (including built-in oauth_clients) decrypts cleanly.
	st2 := openExistingTestStore(t, dir)
	gotUser, err := st2.Users().GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.Username, gotUser.Username)
	assert.Equal(t, user.PasswordHash, gotUser.PasswordHash)
	gotSess, err := st2.Sessions().GetByID(ctx, sessID, time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", gotSess.IPAddress)
	gotWs, err := st2.Workspaces().GetByID(ctx, wsID)
	require.NoError(t, err)
	assert.Equal(t, "Encrypted Workspace", gotWs.Title)
	gotSec, err := st2.WorkspaceSections().GetByID(ctx, secID)
	require.NoError(t, err)
	assert.Equal(t, "Encrypted Section", gotSec.Name)
	gotProv, err := st2.OAuthProviders().GetByID(ctx, prov.ID)
	require.NoError(t, err)
	assert.Equal(t, prov.Name, gotProv.Name)
	assert.Equal(t, prov.ClientSecret, gotProv.ClientSecret)
	gotBuiltIn, err := st2.OAuthClients().Get(ctx, oauthapp.ControlCLIClientID)
	require.NoError(t, err)
	assert.NotEmpty(t, gotBuiltIn.ClientName)
	gotPK, err := st2.PasskeyCredentials().GetByID(ctx, pkID)
	require.NoError(t, err)
	assert.Equal(t, []byte("rot-pubkey"), gotPK.PublicKey)
	gotSetting, err := st2.Settings().Get(ctx, "captcha.turnstile")
	require.NoError(t, err)
	require.NotNil(t, gotSetting.Value)
	assert.Equal(t, settingVal, *gotSetting.Value)
	assert.Equal(t, `{"secret_key":"rot-secret"}`, string(gotSetting.Secret))
}

func TestRecoverExistingCommands_FailLoudlyWhenKeyFileMissing(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "missingkeyuser")
	beforeClientCT := readBuiltInClientCiphertext(t, dir)

	keyPath := filepath.Join(dir, "encryption.key")
	require.NoError(t, os.Remove(keyPath))

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"reencrypt", func() error { return runRecover(t, "encryption-key", "reencrypt", "--data-dir", dir) }},
		{"remove", func() error { return runRecover(t, "encryption-key", "remove", "1", "--data-dir", dir) }},
		{"reset-password", func() error {
			return runRecover(t, "reset-password", "id:"+user.ID, "--password", "NewPassword2!", "--data-dir", dir)
		}},
		{"db version", func() error { return runRecover(t, "db", "version", "--data-dir", dir) }},
		{"db migrate", func() error { return runRecover(t, "db", "migrate", "--data-dir", dir) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			require.Error(t, err, "%s must fail when encryption.key is missing", tc.name)
			assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code, "%s must report not_found when encryption.key is missing", tc.name)
			assert.Contains(t, err.Error(), "no encryption key file", "%s must report missing encryption.key", tc.name)
			assert.Contains(t, err.Error(), keyPath, "%s error must include missing key path", tc.name)
			assert.NoFileExists(t, keyPath, "%s must not auto-generate encryption.key", tc.name)
			assert.Equal(t, beforeClientCT, readBuiltInClientCiphertext(t, dir),
				"%s must not modify oauth_clients", tc.name)
		})
	}
}

func TestRecoverEncryptionKey_CorruptKeyFileReportsParseErrorAndAbsentVersionFailsFast(t *testing.T) {
	t.Run("corrupt key file reports parse error instead of not found", func(t *testing.T) {
		dir := setupTestDataDir(t)
		keyPath := filepath.Join(dir, "encryption.key")
		require.NoError(t, os.WriteFile(keyPath, []byte("not-a-valid-key-line\n"), 0o600))

		for _, tc := range []struct {
			name string
			run  func() error
		}{
			{"rotate", func() error { return runRecover(t, "encryption-key", "rotate", "--data-dir", dir) }},
			{"rotate-pepper", func() error { return runRecover(t, "encryption-key", "rotate-pepper", "--data-dir", dir, "--yes") }},
		} {
			err := tc.run()
			require.Error(t, err, tc.name)
			assert.NotContains(t, err.Error(), "no encryption key file", "%s must not mask a parse error as missing file", tc.name)
			assert.NotContains(t, err.Error(), "not found", "%s must not mask a parse error as missing file", tc.name)
			assert.Contains(t, err.Error(), "load encryption key", "%s must report key load error", tc.name)
		}
	})

	t.Run("remove rejects absent key version before scanning database", func(t *testing.T) {
		dir := setupTestDataDir(t)
		err := runRecover(t, "encryption-key", "remove", "99", "--data-dir", dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not in ring")
	})

	t.Run("db migrate at latest version still reseeds missing built-in oauth_clients", func(t *testing.T) {
		dir := setupTestDataDir(t)
		// Seed built-ins via normal Open first, then delete them directly in SQL.
		initStore := openTestStore(t, dir)
		require.NoError(t, initStore.Close())

		raw, err := sqlite.OpenDB(filepath.Join(dir, "hub.db"), sqlitedb.Config{})
		require.NoError(t, err)
		res, err := raw.Exec("DELETE FROM oauth_clients WHERE registration_source = ?", int64(store.OAuthClientSourceBuiltin))
		require.NoError(t, err)
		deleted, err := res.RowsAffected()
		require.NoError(t, err)
		require.Equal(t, int64(len(oauthapp.BuiltInClientIDs())), deleted)
		require.NoError(t, raw.Close())

		require.NoError(t, runRecover(t, "db", "migrate", "--data-dir", dir))

		st := openExistingTestStore(t, dir)
		for _, clientID := range oauthapp.BuiltInClientIDs() {
			_, err := st.OAuthClients().Get(context.Background(), clientID)
			require.NoErrorf(t, err, "db migrate at latest must restore built-in client %s", clientID)
		}
	})
}
