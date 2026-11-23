package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
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

// testCmdCtx is the dummy cmdCtx tests pass to leaf functions. The Path
// and Description fields only affect --help output, which these tests don't
// exercise — so an empty ctx is sufficient.
var testCmdCtx = cmdCtx{}

func openTestStore(t *testing.T, dir string) store.Store {
	t.Helper()
	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	st, err := storeopen.Open(context.Background(), recoverConfig(dir), ks)
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
	st, err := storeopen.OpenWithoutMigrate(context.Background(), recoverConfig(dir), ks)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// createTestUser seeds a user row through the store so all encrypted columns
// carry valid ciphertext under dir's encryption.key. The hash is a placeholder:
// every test that goes through runPasswordReset overwrites it.
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

	err := runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir})
	require.NoError(t, err)

	ks, err := keystore.LoadFromFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 2)
}

func TestCLI_RotateEncryptionKey_TwiceIncrementsVersion(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir})
	_ = runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir})

	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.Equal(t, uint32(3), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 3)
}

func TestCLI_RemoveEncryptionKey_ActiveVersionFails(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot remove active")
}

func TestCLI_RemoveEncryptionKey_OldVersion(t *testing.T) {
	dir := setupTestDataDir(t)
	_ = runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir})

	err := runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir})
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
			require.NoError(t, runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir}))
			keyPath := filepath.Join(dir, "encryption.key")
			before, err := os.ReadFile(keyPath)
			require.NoError(t, err)

			err = runRemoveEncryptionKey(testCmdCtx, []string{"--version", version, "--data-dir", dir})
			assert.Error(t, err)

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

	err = runRemoveEncryptionKey(testCmdCtx, []string{"--version", "4294967295", "--data-dir", dir})
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
	require.NoError(t, runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir}))

	// Re-encrypt.
	require.NoError(t, runReencryptSecrets(testCmdCtx, []string{"--data-dir", dir}))

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

	require.NoError(t, runReencryptSecrets(testCmdCtx, []string{"--data-dir", dir}))

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
	require.NoError(t, runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir}))

	// Removing v1 must be refused — it still encrypts the provider secret.
	err := runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still encrypts")

	// After reencrypt migrates the secret to v2, v1 is unreferenced and
	// removal succeeds.
	require.NoError(t, runReencryptSecrets(testCmdCtx, []string{"--data-dir", dir}))
	require.NoError(t, runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir}))

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
	require.NoError(t, runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir}))

	// Removing v1 must be refused — it still encrypts the hub_settings row.
	err := runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still encrypts")
	assert.Contains(t, err.Error(), "hub settings")

	// Reencrypt migrates the row; it must decrypt under v2 with the original plaintext.
	require.NoError(t, runReencryptSecrets(testCmdCtx, []string{"--data-dir", dir}))
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
	require.NoError(t, runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir}))
}

func TestCLI_RotatePepper_RequiresYes(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runRotatePepper(testCmdCtx, []string{"--data-dir", dir})
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

	require.NoError(t, runRotatePepper(testCmdCtx, []string{"--yes", "--data-dir", dir}))

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

	err = runPasswordReset(testCmdCtx, []string{
		"--id", user.ID,
		"--password", "NewPassword2!",
		"--data-dir", dir,
	})
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
	err := runPasswordReset(testCmdCtx, []string{
		"--id", alice.ID,
		"--password", "NewPassword2!",
		"--data-dir", dir,
	})
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

	err := runPasswordReset(testCmdCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a terminal")
}

// The refusal echoes the selector the operator typed, whichever flag held it.
func TestCLI_PasswordReset_NotFound(t *testing.T) {
	for _, tc := range []struct {
		flag, value string
	}{
		{"--id", "nonexistent-id"},
		{"--username", "nobody"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			dir := setupTestDataDir(t)

			err := runPasswordReset(testCmdCtx, []string{
				tc.flag, tc.value,
				"--password", "NewPassword2!",
				"--data-dir", dir,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "user not found: "+tc.value)
		})
	}
}

func TestCLI_PasswordReset_ByUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runPasswordReset(testCmdCtx, []string{
		"--username", "alice",
		"--password", "NewPassword2!",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	st := openTestStore(t, dir)
	updated, err := st.Users().GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.NotEqual(t, "placeholder", updated.PasswordHash)
}

// Both flags are refused before any lookup, even when they name two different
// users, and neither user's password changes.
func TestCLI_PasswordReset_IdAndUsernameMutuallyExclusive(t *testing.T) {
	dir := setupTestDataDir(t)
	alice := createTestUser(t, dir, "alice")
	createTestUser(t, dir, "bob")

	err := runPasswordReset(testCmdCtx, []string{
		"--id", alice.ID,
		"--username", "bob",
		"--password", "NewPassword2!",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id and --username are mutually exclusive")

	st := openTestStore(t, dir)
	for _, username := range []string{"alice", "bob"} {
		user, err := st.Users().GetByUsername(context.Background(), username)
		require.NoError(t, err)
		assert.Equalf(t, "placeholder", user.PasswordHash, "%s's password must not change", username)
	}
}

// With neither flag, resolveUser builds no selector, and the shared rule's
// ErrNoUserSelector reaches the operator in flag words.
func TestCLI_PasswordReset_RequiresASelector(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "alice")

	err := runPasswordReset(testCmdCtx, []string{
		"--password", "NewPassword2!",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id or --username is required")
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

	require.NoError(t, runPasswordReset(testCmdCtx, []string{
		"--id", user.ID,
		"--password", "NewPassword2!",
		"--data-dir", dir,
	}))

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

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	fnErr := fn()
	require.NoError(t, w.Close())
	os.Stdout = old
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out), fnErr
}

func TestCLI_DBPath(t *testing.T) {
	dir := setupTestDataDir(t)

	out, err := captureStdout(t, func() error {
		return runDBPath(testCmdCtx, []string{"--data-dir", dir})
	})
	require.NoError(t, err)
	assert.Equal(t, recoverConfig(dir).SQLiteDBPath(), strings.TrimSpace(out))
}

func TestCLI_DBVersion(t *testing.T) {
	dir := setupTestDataDir(t)

	out, err := captureStdout(t, func() error {
		return runDBVersion(testCmdCtx, []string{"--data-dir", dir})
	})
	require.NoError(t, err)
	assert.Contains(t, out, "Current schema version:")
	assert.Contains(t, out, "Latest available version:")
}

func TestCLI_DBMigrate_AlreadyLatest(t *testing.T) {
	dir := setupTestDataDir(t)

	out, err := captureStdout(t, func() error {
		return runDBMigrate(testCmdCtx, []string{"--data-dir", dir})
	})
	require.NoError(t, err)
	assert.Contains(t, out, "Already at latest version.")
}

// ---- recover bootstrap ----

func TestCLI_BootstrapCreateAdmin(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "firstadmin",
		"--password", "AdminPassword1!",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)
	user, err := q.GetUserByUsername(context.Background(), "firstadmin")
	require.NoError(t, err)
	assert.NotZero(t, user.IsAdmin, "the bootstrapped user is always an administrator")

	// The precondition refuses a second bootstrap once any admin exists.
	err = runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "secondadmin",
		"--password", "AdminPassword1!",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control admin user create")
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
		err := runBootstrapCreateAdmin(testCmdCtx, []string{
			"--username", name,
			"--password", "AdminPassword1!",
			"--data-dir", dir,
		})
		require.Errorf(t, err, "%q normalizes to the reserved solo username", name)
		assert.Contains(t, err.Error(), "reserved username")

		// And nothing was written.
		_, q := openTestDB(t, dir)
		_, err = q.GetUserByUsername(context.Background(), "solo")
		require.Error(t, err, "the refusal must happen before the insert")
	}
}

func TestCLI_BootstrapCreateAdmin_MissingUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	err := runBootstrapCreateAdmin(testCmdCtx, []string{"--password", "AdminPassword1!", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--username is required")
}

func TestCLI_BootstrapCreateAdmin_SucceedsWhenOnlyNonAdminsExist(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "bob")

	err := runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "alice",
		"--password", "AdminPassword1!",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)
	alice, err := q.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)
	assert.NotZero(t, alice.IsAdmin)
}

func TestCLI_BootstrapCreateAdmin_DuplicateUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "alice")

	err := runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "alice",
		"--password", "AdminPassword1!",
		"--data-dir", dir,
	})
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

	err = runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "firstadmin",
		"--password", "AdminPassword1!",
		"--data-dir", dir,
	})
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
		err := runBootstrapCreateAdmin(testCmdCtx, []string{
			"--username", username,
			"--password", "AdminPassword1!",
			"--data-dir", dir,
		})
		require.Errorf(t, err, "username %q must be refused", username)
		assert.Contains(t, err.Error(), "username")
	}
}

func TestCLI_BootstrapCreateAdmin_NormalizesTheUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	require.NoError(t, runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "  FirstAdmin  ",
		"--password", "AdminPassword1!",
		"--data-dir", dir,
	}))
	_, q := openTestDB(t, dir)
	user, err := q.GetUserByUsername(context.Background(), "firstadmin")
	require.NoError(t, err, "the username is lowercased and trimmed before it is stored")
	assert.Equal(t, "firstadmin", user.Username)
}

func TestCLI_BootstrapCreateAdmin_RefusesAWeakPassword(t *testing.T) {
	dir := setupTestDataDir(t)
	err := runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "firstadmin",
		"--password", "short",
		"--data-dir", dir,
	})
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
	err := runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "firstadmin",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}

// --display-name is the flag every E2E hub bootstraps with, and nothing
// covered it: an empty one falls back to the username.
func TestCLI_BootstrapCreateAdmin_DisplayName(t *testing.T) {
	dir := setupTestDataDir(t)
	require.NoError(t, runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "firstadmin",
		"--password", "AdminPassword1!",
		"--display-name", "First Admin",
		"--data-dir", dir,
	}))
	_, q := openTestDB(t, dir)
	user, err := q.GetUserByUsername(context.Background(), "firstadmin")
	require.NoError(t, err)
	assert.Equal(t, "First Admin", user.DisplayName)

	other := setupTestDataDir(t)
	require.NoError(t, runBootstrapCreateAdmin(testCmdCtx, []string{
		"--username", "nodisplayname",
		"--password", "AdminPassword1!",
		"--data-dir", other,
	}))
	_, q2 := openTestDB(t, other)
	fallback, err := q2.GetUserByUsername(context.Background(), "nodisplayname")
	require.NoError(t, err)
	assert.Equal(t, "nodisplayname", fallback.DisplayName, "an omitted display name falls back to the username")
}

// TestResolveGroupTokenTreatsSubgroupsAndCommandsAsOneNamespace pins the
// rule the two tree walkers share.
//
// Matching each category separately and preferring the subgroup was wrong
// in two ways at once: a prefix that fitted a subgroup AND a command
// dispatched the subgroup with no word to the operator, and it beat an
// EXACT command match — the one thing prefix matching must never do. No
// group in recoverTree mixes the two categories today, so the property
// needs a fixture to stay true.
func TestResolveGroupTokenTreatsSubgroupsAndCommandsAsOneNamespace(t *testing.T) {
	noop := func(cmdCtx, []string) error { return nil }
	mixed := cmdGroup{
		Subgroups: []cmdGroup{{Name: "passwords"}},
		Commands: []cmdLeaf{
			{Name: "password", Run: noop},
			{Name: "path", Run: noop},
		},
	}

	// An exact command name wins although a subgroup shares its prefix.
	m := resolveGroupToken(mixed, "password")
	assert.Equal(t, -1, m.Subgroup, "an exact command match must not be overtaken by a subgroup")
	assert.Equal(t, 0, m.Command)

	// A prefix that fits one subgroup and one command is AMBIGUOUS, and the
	// candidate list names both.
	m = resolveGroupToken(mixed, "passw")
	assert.Equal(t, -1, m.Subgroup)
	assert.Equal(t, -1, m.Command)
	assert.ElementsMatch(t, []string{"passwords", "password"}, m.Candidates)

	// A prefix unique across the union still resolves.
	m = resolveGroupToken(mixed, "pat")
	assert.Equal(t, 1, m.Command)

	// A prefix that fits nothing resolves to nothing, with no candidates.
	m = resolveGroupToken(mixed, "zzz")
	assert.Equal(t, -1, m.Subgroup)
	assert.Equal(t, -1, m.Command)
	assert.Empty(t, m.Candidates)
}

// TestRecoverRefusalWordingIsTheOneAnOperatorSees pins the refusal at the
// layer that actually prints it.
//
// The validating walk runs FIRST and reports "handled" on every failure,
// so the dispatcher's own refusal never reaches a terminal. Both now build
// the message through unresolvedTokenError, so the spelling a test pins is
// the spelling an operator reads.
func TestRecoverRefusalWordingIsTheOneAnOperatorSees(t *testing.T) {
	run := func(args ...string) (int, bool, string) {
		var stdout, stderr strings.Builder
		code, handled := handleRecoverArgs(args, &stdout, &stderr)
		return code, handled, stderr.String()
	}

	code, handled, out := run("zzz")
	assert.Equal(t, 1, code)
	assert.True(t, handled, "an unresolved token is handled by the walk, never by the dispatcher")
	assert.Contains(t, out, "unknown recover group: zzz")

	_, _, out = run("db", "zzz")
	assert.Contains(t, out, "unknown recover db command: zzz")

	// No two real subgroups share a first letter, so the ambiguity path has
	// no input in this tree; the fixture test above covers that branch.
	// A unique prefix still resolves.
	code, handled, _ = run("boot")
	assert.Equal(t, 1, code, "the group still needs a command")
	assert.True(t, handled)

	// And the dispatcher, reached directly, says exactly the same thing.
	err := runRecover([]string{"zzz"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown recover group: zzz")
	err = runRecover([]string{"db", "zzz"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown recover db command: zzz")
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

	require.NoError(t, runPasswordReset(testCmdCtx, []string{
		"--id", user.ID,
		"--password", "NewPassword2!",
		"--data-dir", dir,
	}))

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

	require.NoError(t, runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir}))
	require.NoError(t, runReencryptSecrets(testCmdCtx, []string{"--data-dir", dir}))

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

	require.NoError(t, runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir}))
	err := runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir})
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
	require.NoError(t, runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir}))

	// remove --version 1 must fail listing affected tables and must not modify
	// built-in oauth_clients ciphertext (it uses OpenWithoutMigrate).
	err := runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir})
	require.Error(t, err)
	for _, want := range []string{"still encrypts", "users", "workspaces", "OAuth clients", "hub settings"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.Equal(t, beforeClientCT, readBuiltInClientCiphertext(t, dir),
		"remove must not modify built-in oauth_clients ciphertext")

	// Re-encrypt all persistent tables to v2, then remove v1.
	require.NoError(t, runReencryptSecrets(testCmdCtx, []string{"--data-dir", dir}))
	require.NoError(t, runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir}))

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
		{"reencrypt", func() error { return runReencryptSecrets(testCmdCtx, []string{"--data-dir", dir}) }},
		{"remove", func() error { return runRemoveEncryptionKey(testCmdCtx, []string{"--version", "1", "--data-dir", dir}) }},
		{"password reset", func() error {
			return runPasswordReset(testCmdCtx, []string{"--id", user.ID, "--password", "NewPassword2!", "--data-dir", dir})
		}},
		{"db version", func() error { return runDBVersion(testCmdCtx, []string{"--data-dir", dir}) }},
		{"db migrate", func() error { return runDBMigrate(testCmdCtx, []string{"--data-dir", dir}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			require.Error(t, err, "%s must fail when encryption.key is missing", tc.name)
			assert.Contains(t, err.Error(), "encryption key file not found", "%s must report missing encryption.key", tc.name)
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
			{"rotate", func() error { return runRotateEncryptionKey(testCmdCtx, []string{"--data-dir", dir}) }},
			{"rotate-pepper", func() error { return runRotatePepper(testCmdCtx, []string{"--data-dir", dir, "--yes"}) }},
		} {
			err := tc.run()
			require.Error(t, err, tc.name)
			assert.NotContains(t, err.Error(), "not found", "%s must not mask a parse error as missing file", tc.name)
			assert.Contains(t, err.Error(), "load encryption key", "%s must report key load error", tc.name)
		}
	})

	t.Run("remove rejects absent key version before scanning database", func(t *testing.T) {
		dir := setupTestDataDir(t)
		err := runRemoveEncryptionKey(testCmdCtx, []string{"--version", "99", "--data-dir", dir})
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

		require.NoError(t, runDBMigrate(testCmdCtx, []string{"--data-dir", dir}))

		st := openExistingTestStore(t, dir)
		for _, clientID := range oauthapp.BuiltInClientIDs() {
			_, err := st.OAuthClients().Get(context.Background(), clientID)
			require.NoErrorf(t, err, "db migrate at latest must restore built-in client %s", clientID)
		}
	})
}
