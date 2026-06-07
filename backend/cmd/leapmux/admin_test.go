package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/hub/storeopen"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// testAdminCtx is the dummy adminCmdCtx tests pass to leaf functions. The Path
// and Description fields only affect --help output, which these tests don't
// exercise — so an empty ctx is sufficient.
var testAdminCtx = adminCmdCtx{}

// setupTestDataDir creates a temp dir with an encryption key and migrated hub DB.
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

// openTestDB opens the test database and returns the connection and queries handle.
// The database is automatically closed when the test completes.
func openTestDB(t *testing.T, dir string) (*sql.DB, *gendb.Queries) {
	t.Helper()
	sqlDB, err := sqlite.OpenDB(filepath.Join(dir, "hub.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB, gendb.New(sqlDB)
}

// createTestUser creates a user via runUserCreate and returns the user from DB.
// Uses a fixed password "TestPassword1!" to minimize Argon2id hashing calls.
func createTestUser(t *testing.T, dir, username string) gendb.User {
	t.Helper()
	err := runUserCreate(testAdminCtx, []string{
		"--username", username,
		"--password", "TestPassword1!",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	user, err := q.GetUserByUsername(context.Background(), username)
	require.NoError(t, err)
	return user
}

// createTestSession creates a session for the given user and returns its ID.
// The expiresAt time is truncated to millisecond precision to match SQLite's
// strftime('%Y-%m-%dT%H:%M:%fZ', 'now') format which uses 3 decimal places.
func createTestSession(t *testing.T, q *gendb.Queries, userID string, expiresAt time.Time) string {
	t.Helper()

	sessionID := id.Generate()
	err := q.CreateUserSession(context.Background(), gendb.CreateUserSessionParams{
		ID:        sessionID,
		UserID:    userID,
		ExpiresAt: expiresAt.Truncate(time.Millisecond),
		UserAgent: "test",
		IpAddress: "127.0.0.1",
	})
	require.NoError(t, err)
	return sessionID
}

// createTestWorker creates a worker registered by the given user and returns its ID.
func createTestWorker(t *testing.T, q *gendb.Queries, userID string) string {
	t.Helper()

	workerID := id.Generate()
	err := q.CreateWorker(context.Background(), gendb.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userID,
		PublicKey:       []byte{},
		MlkemPublicKey:  []byte{},
		SlhdsaPublicKey: []byte{},
	})
	require.NoError(t, err)
	return workerID
}

// ---- OAuth provider tests ----

func TestCLI_AddOAuthProvider_GitHub(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runAddOAuthProvider(testAdminCtx, []string{
		"--type", "github",
		"--client-id", "test-gh-client",
		"--client-secret", "test-gh-secret",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	// Verify provider was created in DB.
	_, q := openTestDB(t, dir)

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

func TestCLI_AddOAuthProvider_OIDC_MissingTrustEmail(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runAddOAuthProvider(testAdminCtx, []string{
		"--type", "oidc",
		"--name", "My OIDC",
		"--client-id", "test-client",
		"--client-secret", "test-secret",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--trust-email is required")
}

func TestCLI_AddOAuthProvider_OIDC_MissingIssuerURL(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runAddOAuthProvider(testAdminCtx, []string{
		"--type", "oidc",
		"--name", "My OIDC",
		"--client-id", "test-client",
		"--client-secret", "test-secret",
		"--trust-email=true",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--issuer-url is required")
}

func TestCLI_AddOAuthProvider_PresetOverride(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runAddOAuthProvider(testAdminCtx, []string{
		"--type", "github",
		"--client-id", "test-client",
		"--client-secret", "test-secret",
		"--name", "Custom GitHub",
		"--scopes", "repo user",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	providers, _ := q.ListAllOAuthProviders(context.Background())
	require.Len(t, providers, 1)
	assert.Equal(t, "Custom GitHub", providers[0].Name)
	assert.Equal(t, "repo user", providers[0].Scopes)
}

func TestCLI_ListOAuthProviders(t *testing.T) {
	dir := setupTestDataDir(t)

	// Add a provider first.
	_ = runAddOAuthProvider(testAdminCtx, []string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "s1",
		"--data-dir", dir,
	})

	// List should succeed (we can't easily capture stdout, but no error = success).
	err := runListOAuthProviders(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_RemoveOAuthProvider(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runAddOAuthProvider(testAdminCtx, []string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "s1",
		"--data-dir", dir,
	})

	// Get the provider ID.
	_, q := openTestDB(t, dir)
	providers, _ := q.ListAllOAuthProviders(context.Background())
	require.Len(t, providers, 1)

	err := runRemoveOAuthProvider(testAdminCtx, []string{"--id", providers[0].ID, "--data-dir", dir})
	require.NoError(t, err)

	// Verify deleted.
	_, q = openTestDB(t, dir)
	providers, _ = q.ListAllOAuthProviders(context.Background())
	assert.Empty(t, providers)
}

func TestCLI_EnableDisableOAuthProvider(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runAddOAuthProvider(testAdminCtx, []string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "s1",
		"--data-dir", dir,
	})

	_, q := openTestDB(t, dir)
	providers, _ := q.ListAllOAuthProviders(context.Background())
	providerID := providers[0].ID

	// Disable.
	err := runSetOAuthProviderEnabled(testAdminCtx, []string{"--id", providerID, "--data-dir", dir}, false)
	require.NoError(t, err)

	_, q = openTestDB(t, dir)
	enabled, _ := q.ListEnabledOAuthProviders(context.Background())
	assert.Empty(t, enabled, "no providers should be enabled")

	// Re-enable.
	err = runSetOAuthProviderEnabled(testAdminCtx, []string{"--id", providerID, "--data-dir", dir}, true)
	require.NoError(t, err)

	_, q = openTestDB(t, dir)
	enabled, _ = q.ListEnabledOAuthProviders(context.Background())
	assert.Len(t, enabled, 1)
}

// ---- Encryption key tests ----

func TestCLI_RotateEncryptionKey(t *testing.T) {
	dir := setupTestDataDir(t)
	keyPath := filepath.Join(dir, "encryption.key")

	err := runRotateEncryptionKey(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)

	ks, err := keystore.LoadFromFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 2)
}

func TestCLI_RotateEncryptionKey_TwiceIncrementsVersion(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runRotateEncryptionKey(testAdminCtx, []string{"--data-dir", dir})
	_ = runRotateEncryptionKey(testAdminCtx, []string{"--data-dir", dir})

	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.Equal(t, uint32(3), ks.ActiveVersion())
	assert.Len(t, ks.Versions(), 3)
}

func TestCLI_RemoveEncryptionKey_ActiveVersionFails(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runRemoveEncryptionKey(testAdminCtx, []string{"--version", "1", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot remove active")
}

func TestCLI_RemoveEncryptionKey_OldVersion(t *testing.T) {
	dir := setupTestDataDir(t)
	_ = runRotateEncryptionKey(testAdminCtx, []string{"--data-dir", dir})

	err := runRemoveEncryptionKey(testAdminCtx, []string{"--version", "1", "--data-dir", dir})
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
	_ = runAddOAuthProvider(testAdminCtx, []string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "original-secret",
		"--data-dir", dir,
	})

	// Rotate key.
	_ = runRotateEncryptionKey(testAdminCtx, []string{"--data-dir", dir})

	// Re-encrypt.
	err := runReencryptSecrets(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)

	// Verify the encrypted secret can be decrypted with the new key only.
	ks, _ := keystore.LoadFromFile(keyPath)
	_, q := openTestDB(t, dir)
	providers, _ := q.ListAllOAuthProviders(context.Background())
	full, _ := q.GetOAuthProviderByID(context.Background(), providers[0].ID)

	// The ciphertext should now be version 2.
	ver, err := keystore.CiphertextVersion(full.ClientSecret)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), ver, "re-encrypted secret should be version 2")

	// Decrypting should return the original plaintext.
	aad := keystore.ProviderAAD(providers[0].ID)
	plain, err := ks.Decrypt(full.ClientSecret, aad)
	require.NoError(t, err)
	assert.Equal(t, "original-secret", string(plain))
}

func TestCLI_ReencryptSecrets_Idempotent(t *testing.T) {
	dir := setupTestDataDir(t)

	_ = runAddOAuthProvider(testAdminCtx, []string{
		"--type", "github",
		"--client-id", "c1",
		"--client-secret", "secret",
		"--data-dir", dir,
	})

	// Running reencrypt without rotation should report 0 re-encrypted.
	err := runReencryptSecrets(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_RemoveEncryptionKey_RefusesWhenReferenced(t *testing.T) {
	dir := setupTestDataDir(t)

	// Add a provider — its client secret is encrypted under key version 1.
	require.NoError(t, runAddOAuthProvider(testAdminCtx, []string{
		"--type", "github", "--client-id", "c1", "--client-secret", "s1", "--data-dir", dir,
	}))

	// Rotate to version 2; the provider secret is still encrypted under v1.
	require.NoError(t, runRotateEncryptionKey(testAdminCtx, []string{"--data-dir", dir}))

	// Removing v1 must be refused — it still encrypts the provider secret.
	err := runRemoveEncryptionKey(testAdminCtx, []string{"--version", "1", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still encrypts")

	// After reencrypt migrates the secret to v2, v1 is unreferenced and
	// removal succeeds.
	require.NoError(t, runReencryptSecrets(testAdminCtx, []string{"--data-dir", dir}))
	require.NoError(t, runRemoveEncryptionKey(testAdminCtx, []string{"--version", "1", "--data-dir", dir}))

	ks, err := keystore.LoadFromFile(filepath.Join(dir, "encryption.key"))
	require.NoError(t, err)
	assert.Len(t, ks.Versions(), 1)
}

func TestCLI_RemoveEncryptionKey_RefusesWhenTokenReferenced(t *testing.T) {
	dir := setupTestDataDir(t)
	ctx := context.Background()

	// Seed an OAuth token encrypted under key version 1.
	st, err := storeopen.Open(ctx, adminConfig(dir))
	require.NoError(t, err)
	orgID := storetest.SeedOrg(t, st, "org", true)
	user := storetest.SeedUser(t, st, orgID, "tokuser")
	prov := storetest.SeedOAuthProvider(t, st, "tokprov")
	require.NoError(t, st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
		UserID:       user.ID,
		ProviderID:   prov.ID,
		AccessToken:  []byte("a"),
		RefreshToken: []byte("r"),
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
		KeyVersion:   1,
	}))
	require.NoError(t, st.Close())

	// Rotate to version 2; the OAuth token is still on version 1.
	require.NoError(t, runRotateEncryptionKey(testAdminCtx, []string{"--data-dir", dir}))

	// Removing v1 must be refused — an OAuth token still references it. This
	// exercises the guard's CountByKeyVersion (oauth_tokens) branch.
	err = runRemoveEncryptionKey(testAdminCtx, []string{"--version", "1", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OAuth token")
}

func TestCLI_RotatePepper_RequiresYes(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runRotatePepper(testAdminCtx, []string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--yes")

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

	require.NoError(t, runRotatePepper(testAdminCtx, []string{"--yes", "--data-dir", dir}))

	after, err := keystore.LoadFromFile(keyPath)
	require.NoError(t, err)
	assert.NotEqual(t, before.Pepper(), after.Pepper(), "pepper must change")
	assert.Equal(t, before.ActiveVersion(), after.ActiveVersion(), "encryption keys must be unchanged")
	assert.Equal(t, before.Versions(), after.Versions())
}

// ---- User subcommand tests: happy paths ----

func TestCLI_UserCreate(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate(testAdminCtx, []string{
		"--username", "alice",
		"--password", "TestPassword1!",
		"--display-name", "Alice Smith",
		"--email", "alice@example.com",
		"--admin",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	user, err := q.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, "alice", user.Username)
	assert.Equal(t, "Alice Smith", user.DisplayName)
	assert.Equal(t, "alice@example.com", user.Email)
	assert.Equal(t, int64(0), user.EmailVerified, "email_verified should be 0 when --email-verified not passed")
	assert.Equal(t, int64(1), user.IsAdmin, "is_admin should be 1 when --admin passed")
	assert.Equal(t, int64(1), user.PasswordSet)
	assert.NotEmpty(t, user.PasswordHash)
	assert.NotEmpty(t, user.OrgID)
}

func TestCLI_UserCreate_MinimalFlags(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate(testAdminCtx, []string{
		"--username", "bob",
		"--password", "TestPassword1!",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	user, err := q.GetUserByUsername(context.Background(), "bob")
	require.NoError(t, err)
	assert.Equal(t, "bob", user.Username)
	assert.Equal(t, "bob", user.DisplayName, "display name defaults to username")
	assert.Equal(t, "", user.Email)
	assert.Equal(t, int64(0), user.EmailVerified)
	assert.Equal(t, int64(0), user.IsAdmin)
	assert.Equal(t, int64(1), user.PasswordSet)
}

func TestCLI_UserCreate_WithEmailVerified(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate(testAdminCtx, []string{
		"--username", "carol",
		"--password", "TestPassword1!",
		"--email", "carol@example.com",
		"--email-verified",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	user, err := q.GetUserByUsername(context.Background(), "carol")
	require.NoError(t, err)
	assert.Equal(t, "carol@example.com", user.Email)
	assert.Equal(t, int64(1), user.EmailVerified, "email_verified should be 1 when --email-verified passed")
}

func TestCLI_UserCreate_EmailWithoutVerified(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate(testAdminCtx, []string{
		"--username", "dave",
		"--password", "TestPassword1!",
		"--email", "dave@example.com",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	user, err := q.GetUserByUsername(context.Background(), "dave")
	require.NoError(t, err)
	assert.Equal(t, "dave@example.com", user.Email)
	assert.Equal(t, int64(0), user.EmailVerified, "email_verified should be 0 when --email-verified not passed")
}

func TestCLI_UserList(t *testing.T) {
	dir := setupTestDataDir(t)

	createTestUser(t, dir, "alice")
	createTestUser(t, dir, "bob")
	createTestUser(t, dir, "carol")

	err := runUserList(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserList_WithQuery(t *testing.T) {
	dir := setupTestDataDir(t)

	createTestUser(t, dir, "alice")
	createTestUser(t, dir, "bob")
	createTestUser(t, dir, "alicia")

	// Search for "ali" should match alice and alicia.
	_, q := openTestDB(t, dir)

	users, err := q.SearchUsers(context.Background(), gendb.SearchUsersParams{
		Query: sql.NullString{String: "ali", Valid: true},
		Limit: 50,
	})
	require.NoError(t, err)
	assert.Len(t, users, 2)

	// Also verify the CLI function returns no error.
	err = runUserList(testAdminCtx, []string{"--query", "ali", "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserList_WithLimitAndCursor(t *testing.T) {
	dir := setupTestDataDir(t)

	for i := 0; i < 5; i++ {
		createTestUser(t, dir, fmt.Sprintf("user%d", i))
	}

	// Verify limit works via the generated query.
	_, q := openTestDB(t, dir)

	users, err := q.ListAllUsers(context.Background(), gendb.ListAllUsersParams{
		Limit: 2,
	})
	require.NoError(t, err)
	assert.Len(t, users, 2)

	// Verify cursor-based pagination works.
	// The cursor must be passed in the same string format SQLite stores it in.
	cursor := users[len(users)-1].CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	users2, err := q.ListAllUsers(context.Background(), gendb.ListAllUsersParams{
		Cursor: cursor,
		Limit:  10,
	})
	require.NoError(t, err)
	assert.Len(t, users2, 3)

	// CLI with limit should not error.
	err = runUserList(testAdminCtx, []string{"--limit", "2", "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserList_Empty(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserList(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserGet_ByID(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserGet(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserGet_ByUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "alice")

	err := runUserGet(testAdminCtx, []string{"--username", "alice", "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserUpdate_DisplayName(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserUpdate(testAdminCtx, []string{
		"--id", user.ID,
		"--display-name", "Alice Updated",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, "Alice Updated", updated.DisplayName)
}

func TestCLI_UserUpdate_Email(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserUpdate(testAdminCtx, []string{
		"--id", user.ID,
		"--email", "newalice@example.com",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, "newalice@example.com", updated.Email)
	assert.Equal(t, int64(0), updated.EmailVerified, "email_verified should be 0 when email updated without --email-verified")
}

func TestCLI_UserUpdate_EmailVerified(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserUpdate(testAdminCtx, []string{
		"--id", user.ID,
		"--email", "alice@example.com",
		"--email-verified=true",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", updated.Email)
	assert.Equal(t, int64(1), updated.EmailVerified, "email_verified should be 1 when --email-verified=true passed")
}

func TestCLI_UserUpdate_ByUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "alice")

	err := runUserUpdate(testAdminCtx, []string{
		"--username", "alice",
		"--display-name", "Alice Via Username",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	updated, err := q.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, "Alice Via Username", updated.DisplayName)
}

func TestCLI_UserDelete_ByID(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserDelete(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	// Verify user is soft-deleted.
	_, q := openTestDB(t, dir)

	deletedUser, err := q.GetUserByIDIncludeDeleted(context.Background(), user.ID)
	require.NoError(t, err)
	assert.True(t, deletedUser.DeletedAt.Valid, "user should be soft-deleted")

	// Verify personal org is soft-deleted.
	deletedOrg, err := q.GetOrgByIDIncludeDeleted(context.Background(), user.OrgID)
	require.NoError(t, err)
	assert.True(t, deletedOrg.DeletedAt.Valid, "org should be soft-deleted")
}

func TestCLI_UserDelete_ByUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserDelete(testAdminCtx, []string{"--username", "alice", "--data-dir", dir})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	deletedUser, err := q.GetUserByIDIncludeDeleted(context.Background(), user.ID)
	require.NoError(t, err)
	assert.True(t, deletedUser.DeletedAt.Valid, "user should be soft-deleted")
}

func TestCLI_UserResetPassword(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	// Record original hash and create a session that should be deleted after reset.
	_, q := openTestDB(t, dir)
	original, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	createTestSession(t, q, user.ID, time.Now().UTC().Add(24*time.Hour))

	err = runUserResetPassword(testAdminCtx, []string{
		"--id", user.ID,
		"--password", "NewPassword2!",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	// Verify hash changed.
	_, q = openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.NotEqual(t, original.PasswordHash, updated.PasswordHash)

	// Verify sessions deleted.
	sessions, err := q.ListUserSessionsByUserID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestCLI_UserResetPassword_PreservesOtherUserSessions(t *testing.T) {
	dir := setupTestDataDir(t)
	alice := createTestUser(t, dir, "alice")
	bob := createTestUser(t, dir, "bob")

	// Create sessions for both users.
	_, q := openTestDB(t, dir)
	createTestSession(t, q, alice.ID, time.Now().UTC().Add(24*time.Hour))
	bobSessionID := createTestSession(t, q, bob.ID, time.Now().UTC().Add(24*time.Hour))

	// Reset alice's password.
	err := runUserResetPassword(testAdminCtx, []string{
		"--id", alice.ID,
		"--password", "NewPassword2!",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	// Verify bob's session is still there.
	_, q = openTestDB(t, dir)

	sessions, err := q.ListUserSessionsByUserID(context.Background(), bob.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, bobSessionID, sessions[0].ID)
}

func TestCLI_UserGrantAdmin(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserGrantAdmin(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated.IsAdmin)
}

func TestCLI_UserGrantAdmin_AlreadyAdmin(t *testing.T) {
	dir := setupTestDataDir(t)

	// Create as admin.
	err := runUserCreate(testAdminCtx, []string{
		"--username", "alice",
		"--password", "TestPassword1!",
		"--admin",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)
	user, err := q.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)

	// Grant admin again -- should not error.
	err = runUserGrantAdmin(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated.IsAdmin)
}

func TestCLI_UserRevokeAdmin(t *testing.T) {
	dir := setupTestDataDir(t)

	// Create as admin.
	err := runUserCreate(testAdminCtx, []string{
		"--username", "alice",
		"--password", "TestPassword1!",
		"--admin",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)
	user, err := q.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)

	err = runUserRevokeAdmin(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), updated.IsAdmin)
}

func TestCLI_UserRevokeAdmin_AlreadyNonAdmin(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	// Revoke admin on a non-admin user -- should not error.
	err := runUserRevokeAdmin(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), updated.IsAdmin)
}

func TestCLI_UserListSessions(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	_, q := openTestDB(t, dir)
	createTestSession(t, q, user.ID, time.Now().UTC().Add(24*time.Hour))
	createTestSession(t, q, user.ID, time.Now().UTC().Add(48*time.Hour))

	err := runUserListSessions(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)
}

// ---- User subcommand tests: edge cases ----

func TestCLI_UserCreate_MissingUsername(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate(testAdminCtx, []string{
		"--password", "TestPassword1!",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--username is required")
}

func TestCLI_UserCreate_MissingPassword(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate(testAdminCtx, []string{
		"--username", "alice",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a terminal")
}

func TestCLI_UserCreate_DuplicateUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "alice")

	err := runUserCreate(testAdminCtx, []string{
		"--username", "alice",
		"--password", "TestPassword1!",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already taken")
}

func TestCLI_UserCreate_DuplicateEmail(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate(testAdminCtx, []string{
		"--username", "alice",
		"--password", "TestPassword1!",
		"--email", "shared@example.com",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	err = runUserCreate(testAdminCtx, []string{
		"--username", "bob",
		"--password", "TestPassword1!",
		"--email", "shared@example.com",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in use")
}

func TestCLI_UserGet_NotFound(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserGet(testAdminCtx, []string{"--id", "nonexistent-id", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCLI_UserGet_NeitherIDNorUsername(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserGet(testAdminCtx, []string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id or --username is required")
}

func TestCLI_UserUpdate_NotFound(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserUpdate(testAdminCtx, []string{
		"--id", "nonexistent-id",
		"--display-name", "Ghost",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCLI_UserDelete_NotFound(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserDelete(testAdminCtx, []string{"--id", "nonexistent-id", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCLI_UserDelete_AdminRequiresForce(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate(testAdminCtx, []string{
		"--username", "adminuser",
		"--password", "TestPassword1!",
		"--admin",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	// Without --force, deleting an admin user should fail.
	err = runUserDelete(testAdminCtx, []string{"--username", "adminuser", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")

	// With --force, it should succeed.
	err = runUserDelete(testAdminCtx, []string{"--username", "adminuser", "--force", "--data-dir", dir})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)
	_, err = q.GetUserByUsername(context.Background(), "adminuser")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestCLI_UserDelete_InvisibleToLookup(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserDelete(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	// GetUserByUsername filters out soft-deleted users, so it should return ErrNoRows.
	_, err = q.GetUserByUsername(context.Background(), "alice")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// GetUserByIDIncludeDeleted returns soft-deleted users, so we can still see the row.
	deletedUser, err := q.GetUserByIDIncludeDeleted(context.Background(), user.ID)
	require.NoError(t, err)
	assert.True(t, deletedUser.DeletedAt.Valid, "user should have non-null deleted_at")
}

func TestCLI_UserDelete_WorkersMarkedDeleted(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	workerID := createTestWorker(t, q, user.ID)

	err := runUserDelete(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)

	worker, err := q.GetWorkerByIDIncludeDeleted(context.Background(), workerID)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, worker.Status)
	assert.True(t, worker.DeletedAt.Valid, "worker should have non-null deleted_at")
}

func TestCLI_UserDelete_WorkspaceSoftDeleted(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	// Create a workspace directly via the DB queries.
	_, q := openTestDB(t, dir)
	wsID := id.Generate()
	err := q.CreateWorkspace(context.Background(), gendb.CreateWorkspaceParams{
		ID:          wsID,
		OrgID:       user.OrgID,
		OwnerUserID: user.ID,
		Title:       "test workspace",
	})
	require.NoError(t, err)

	err = runUserDelete(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	// Read the workspace row directly via raw SQL since GetWorkspaceByID filters deleted ones.
	sqlDB, _ := openTestDB(t, dir)

	var isDeleted int64
	var deletedAt sql.NullTime
	err = sqlDB.QueryRowContext(context.Background(),
		"SELECT is_deleted, deleted_at FROM workspaces WHERE id = ?", wsID,
	).Scan(&isDeleted, &deletedAt)
	require.NoError(t, err)
	assert.Equal(t, int64(1), isDeleted, "workspace should have is_deleted = 1")
	assert.True(t, deletedAt.Valid, "workspace should have non-null deleted_at")
}

func TestCLI_UserResetPassword_MissingPassword(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserResetPassword(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a terminal")
}

func TestCLI_UserResetPassword_NotFound(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserResetPassword(testAdminCtx, []string{
		"--id", "nonexistent-id",
		"--password", "NewPassword2!",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ---- Session subcommand tests: happy paths ----

func TestCLI_SessionList(t *testing.T) {
	dir := setupTestDataDir(t)
	alice := createTestUser(t, dir, "alice")
	bob := createTestUser(t, dir, "bob")

	_, q := openTestDB(t, dir)
	createTestSession(t, q, alice.ID, time.Now().UTC().Add(24*time.Hour))
	createTestSession(t, q, bob.ID, time.Now().UTC().Add(24*time.Hour))

	err := runSessionList(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_SessionRevoke(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	sessionID := createTestSession(t, q, user.ID, time.Now().UTC().Add(24*time.Hour))

	err := runSessionRevoke(testAdminCtx, []string{"--id", sessionID, "--data-dir", dir})
	require.NoError(t, err)

	// Verify session is gone.
	_, q = openTestDB(t, dir)

	sessions, err := q.ListUserSessionsByUserID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestCLI_SessionRevokeUser_ByID(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	createTestSession(t, q, user.ID, time.Now().UTC().Add(24*time.Hour))
	createTestSession(t, q, user.ID, time.Now().UTC().Add(48*time.Hour))

	err := runSessionRevokeUser(testAdminCtx, []string{"--user-id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)

	sessions, err := q.ListUserSessionsByUserID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestCLI_SessionRevokeUser_ByUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	createTestSession(t, q, user.ID, time.Now().UTC().Add(24*time.Hour))

	err := runSessionRevokeUser(testAdminCtx, []string{"--username", "alice", "--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)

	sessions, err := q.ListUserSessionsByUserID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestCLI_SessionRevokeUser_PreservesOtherUsers(t *testing.T) {
	dir := setupTestDataDir(t)
	alice := createTestUser(t, dir, "alice")
	bob := createTestUser(t, dir, "bob")

	_, q := openTestDB(t, dir)
	createTestSession(t, q, alice.ID, time.Now().UTC().Add(24*time.Hour))
	bobSessionID := createTestSession(t, q, bob.ID, time.Now().UTC().Add(24*time.Hour))

	// Revoke alice's sessions.
	err := runSessionRevokeUser(testAdminCtx, []string{"--user-id", alice.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)

	// Alice's sessions should be gone.
	aliceSessions, err := q.ListUserSessionsByUserID(context.Background(), alice.ID)
	require.NoError(t, err)
	assert.Empty(t, aliceSessions)

	// Bob's session should remain.
	bobSessions, err := q.ListUserSessionsByUserID(context.Background(), bob.ID)
	require.NoError(t, err)
	require.Len(t, bobSessions, 1)
	assert.Equal(t, bobSessionID, bobSessions[0].ID)
}

func TestCLI_SessionPurgeExpired(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	// Create one expired and one active session.
	_, q := openTestDB(t, dir)
	createTestSession(t, q, user.ID, time.Now().UTC().Add(-24*time.Hour))            // expired
	activeID := createTestSession(t, q, user.ID, time.Now().UTC().Add(24*time.Hour)) // active

	err := runSessionPurgeExpired(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)

	// Only the active session should remain.
	_, q = openTestDB(t, dir)

	sessions, err := q.ListUserSessionsByUserID(context.Background(), user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, activeID, sessions[0].ID)
}

// ---- Session subcommand tests: edge cases ----

func TestCLI_SessionRevoke_MissingID(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runSessionRevoke(testAdminCtx, []string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

func TestCLI_SessionRevokeUser_NeitherIDNorUsername(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runSessionRevokeUser(testAdminCtx, []string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id or --username is required")
}

func TestCLI_SessionPurgeExpired_NoneExpired(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	// Create only active sessions.
	_, q := openTestDB(t, dir)
	createTestSession(t, q, user.ID, time.Now().UTC().Add(24*time.Hour))

	err := runSessionPurgeExpired(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)

	// Session should still be there.
	_, q = openTestDB(t, dir)

	sessions, err := q.ListUserSessionsByUserID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Len(t, sessions, 1)
}

// ---- Worker subcommand tests: happy paths ----

func TestCLI_WorkerList(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	createTestWorker(t, q, user.ID)
	createTestWorker(t, q, user.ID)

	err := runWorkerList(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_WorkerList_AllStatuses(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	w1 := createTestWorker(t, q, user.ID)
	createTestWorker(t, q, user.ID)

	// Deregister one worker so we have mixed statuses.
	err := runWorkerDeregister(testAdminCtx, []string{"--id", w1, "--data-dir", dir})
	require.NoError(t, err)

	// List all statuses should succeed.
	err = runWorkerList(testAdminCtx, []string{"--status", "all", "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_WorkerList_Empty(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerList(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_WorkerGet(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	workerID := createTestWorker(t, q, user.ID)

	err := runWorkerGet(testAdminCtx, []string{"--id", workerID, "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_WorkerDeregister(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	workerID := createTestWorker(t, q, user.ID)

	err := runWorkerDeregister(testAdminCtx, []string{"--id", workerID, "--data-dir", dir})
	require.NoError(t, err)

	// Verify worker status changed.
	_, q = openTestDB(t, dir)

	worker, err := q.GetWorkerByID(context.Background(), workerID)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, worker.Status)
}

// ---- Worker subcommand tests: edge cases ----

func TestCLI_WorkerGet_NotFound(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerGet(testAdminCtx, []string{"--id", "nonexistent-worker", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCLI_WorkerGet_MissingID(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerGet(testAdminCtx, []string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

func TestCLI_WorkerDeregister_MissingID(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerDeregister(testAdminCtx, []string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

// ---- Worker reg-key subcommand tests ----

// createTestRegKey inserts a worker_registration_keys row directly. expiresAt
// is truncated to ms to match the precision SQLite stores it at.
func createTestRegKey(t *testing.T, q *gendb.Queries, createdBy string, expiresAt time.Time) string {
	t.Helper()
	regID := id.Generate()
	err := q.CreateRegistrationKey(context.Background(), gendb.CreateRegistrationKeyParams{
		ID:        regID,
		CreatedBy: createdBy,
		ExpiresAt: expiresAt.UTC().Truncate(time.Millisecond),
	})
	require.NoError(t, err)
	return regID
}

func TestCLI_WorkerRegKeyList_Empty(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerRegKeyList(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_WorkerRegKeyList_HidesExpiredByDefault(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)

	live := createTestRegKey(t, q, user.ID, time.Now().Add(5*time.Minute))
	expired := createTestRegKey(t, q, user.ID, time.Now().Add(-1*time.Minute))

	err := runWorkerRegKeyList(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)
	got, err := q.ListRegistrationKeysAdmin(context.Background(), gendb.ListRegistrationKeysAdminParams{
		Now:   time.Now().UTC(),
		Limit: 50,
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, live, got[0].ID)
	assert.NotEqual(t, expired, got[0].ID)
}

func TestCLI_WorkerRegKeyList_IncludeExpired(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)

	createTestRegKey(t, q, user.ID, time.Now().Add(5*time.Minute))
	createTestRegKey(t, q, user.ID, time.Now().Add(-1*time.Minute))

	err := runWorkerRegKeyList(testAdminCtx, []string{"--include-expired", "--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)
	got, err := q.ListRegistrationKeysAdmin(context.Background(), gendb.ListRegistrationKeysAdminParams{
		Limit: 50,
	})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestCLI_WorkerRegKeyRevoke(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	regID := createTestRegKey(t, q, user.ID, time.Now().Add(5*time.Minute))

	err := runWorkerRegKeyRevoke(testAdminCtx, []string{"--id", regID, "--data-dir", dir})
	require.NoError(t, err)

	// Row still exists, but expires_at is in the past.
	_, q = openTestDB(t, dir)
	got, err := q.GetRegistrationKeyByID(context.Background(), regID)
	require.NoError(t, err)
	assert.True(t, got.ExpiresAt.Before(time.Now()), "revoked key should have expires_at in the past")
}

func TestCLI_WorkerRegKeyRevoke_MissingID(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerRegKeyRevoke(testAdminCtx, []string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

func TestCLI_WorkerRegKeyRevoke_NotFound(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerRegKeyRevoke(testAdminCtx, []string{"--id", "nonexistent", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCLI_WorkerRegKeyPurgeExpired(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)

	live := createTestRegKey(t, q, user.ID, time.Now().Add(5*time.Minute))
	expired := createTestRegKey(t, q, user.ID, time.Now().Add(-1*time.Minute))

	err := runWorkerRegKeyPurgeExpired(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)

	// Live row remains; expired row is gone.
	_, q = openTestDB(t, dir)
	_, err = q.GetRegistrationKeyByID(context.Background(), live)
	require.NoError(t, err, "live key should still exist after purge")

	_, err = q.GetRegistrationKeyByID(context.Background(), expired)
	assert.ErrorIs(t, err, sql.ErrNoRows, "expired key should be hard-deleted")
}

// ---- User --clear-pending-email tests ----

func TestCLI_UserUpdate_ClearPendingEmail(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	// Stage a pending email verification with a non-zero attempt counter
	// and a future expiry, then run the admin "reset" path.
	_, q := openTestDB(t, dir)
	expires := time.Now().UTC().Add(30 * time.Minute)
	require.NoError(t, q.SetPendingEmail(context.Background(), gendb.SetPendingEmailParams{
		ID:                    user.ID,
		PendingEmail:          "alice-new@example.com",
		PendingEmailToken:     "ABC123",
		PendingEmailExpiresAt: sql.NullTime{Time: expires, Valid: true},
	}))

	// PendingEmail and PendingEmailToken are plain strings in the
	// SQLite-generated model (NOT NULL columns with empty-string sentinels).
	pre, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	require.Equal(t, "alice-new@example.com", pre.PendingEmail)
	require.Equal(t, "ABC123", pre.PendingEmailToken)

	err = runUserUpdate(testAdminCtx, []string{
		"--id", user.ID,
		"--clear-pending-email",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)
	post, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, post.PendingEmail, "pending_email should be cleared")
	assert.Empty(t, post.PendingEmailToken, "pending_email_token should be cleared")
	assert.False(t, post.PendingEmailExpiresAt.Valid, "pending_email_expires_at should be NULL")
	assert.Equal(t, int64(0), post.PendingEmailAttempts)
}

func TestCLI_UserUpdate_NoFields(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserUpdate(testAdminCtx, []string{"--id", user.ID, "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fields to update")
	assert.Contains(t, err.Error(), "--clear-pending-email")
}

// ---- DB subcommand tests ----

func TestCLI_DBPath(t *testing.T) {
	dir := setupTestDataDir(t)

	// Verify no error. The printed path should match the expected DB path.
	err := runDBPath(testAdminCtx, []string{"--data-dir", dir})
	require.NoError(t, err)
}
