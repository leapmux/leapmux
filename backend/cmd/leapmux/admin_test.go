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
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/util/id"
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

// openTestDB opens the test database and returns the connection and queries handle.
// The database is automatically closed when the test completes.
func openTestDB(t *testing.T, dir string) (*sql.DB, *gendb.Queries) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(dir, "hub.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB, gendb.New(sqlDB)
}

// createTestUser creates a user via runUserCreate and returns the user from DB.
// Uses a fixed password "TestPassword1!" to minimize Argon2id hashing calls.
func createTestUser(t *testing.T, dir, username string) gendb.User {
	t.Helper()
	err := runUserCreate([]string{
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

	err := runAddOAuthProvider([]string{
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

	err := runAddOAuthProvider([]string{
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

	err := runAddOAuthProvider([]string{
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

	err := runAddOAuthProvider([]string{
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
	_, q := openTestDB(t, dir)
	providers, _ := q.ListAllOAuthProviders(context.Background())
	require.Len(t, providers, 1)

	err := runRemoveOAuthProvider([]string{"--id", providers[0].ID, "--data-dir", dir})
	require.NoError(t, err)

	// Verify deleted.
	_, q = openTestDB(t, dir)
	providers, _ = q.ListAllOAuthProviders(context.Background())
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

	_, q := openTestDB(t, dir)
	providers, _ := q.ListAllOAuthProviders(context.Background())
	providerID := providers[0].ID

	// Disable.
	err := runSetOAuthProviderEnabled([]string{"--id", providerID, "--data-dir", dir}, false)
	require.NoError(t, err)

	_, q = openTestDB(t, dir)
	enabled, _ := q.ListEnabledOAuthProviders(context.Background())
	assert.Empty(t, enabled, "no providers should be enabled")

	// Re-enable.
	err = runSetOAuthProviderEnabled([]string{"--id", providerID, "--data-dir", dir}, true)
	require.NoError(t, err)

	_, q = openTestDB(t, dir)
	enabled, _ = q.ListEnabledOAuthProviders(context.Background())
	assert.Len(t, enabled, 1)
}

// ---- Encryption key tests ----

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

// ---- User subcommand tests: happy paths ----

func TestCLI_UserCreate(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate([]string{
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

	err := runUserCreate([]string{
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

	err := runUserCreate([]string{
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

	err := runUserCreate([]string{
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

	err := runUserList([]string{"--data-dir", dir})
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
		Query:  sql.NullString{String: "ali", Valid: true},
		Limit:  50,
		Offset: 0,
	})
	require.NoError(t, err)
	assert.Len(t, users, 2)

	// Also verify the CLI function returns no error.
	err = runUserList([]string{"--query", "ali", "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserList_WithLimitOffset(t *testing.T) {
	dir := setupTestDataDir(t)

	for i := 0; i < 5; i++ {
		createTestUser(t, dir, fmt.Sprintf("user%d", i))
	}

	// Verify limit works.
	_, q := openTestDB(t, dir)

	users, err := q.ListAllUsers(context.Background(), gendb.ListAllUsersParams{
		Limit:  2,
		Offset: 0,
	})
	require.NoError(t, err)
	assert.Len(t, users, 2)

	// Verify offset works.
	users, err = q.ListAllUsers(context.Background(), gendb.ListAllUsersParams{
		Limit:  10,
		Offset: 3,
	})
	require.NoError(t, err)
	assert.Len(t, users, 2)

	// CLI with limit/offset should not error.
	err = runUserList([]string{"--limit", "2", "--offset", "1", "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserList_Empty(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserList([]string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserGet_ByID(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserGet([]string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserGet_ByUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "alice")

	err := runUserGet([]string{"--username", "alice", "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_UserUpdate_DisplayName(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserUpdate([]string{
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

	err := runUserUpdate([]string{
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

	err := runUserUpdate([]string{
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

	err := runUserUpdate([]string{
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

	err := runUserDelete([]string{"--id", user.ID, "--data-dir", dir})
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

	err := runUserDelete([]string{"--username", "alice", "--data-dir", dir})
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

	err = runUserResetPassword([]string{
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
	err := runUserResetPassword([]string{
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

	err := runUserGrantAdmin([]string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated.IsAdmin)
}

func TestCLI_UserGrantAdmin_AlreadyAdmin(t *testing.T) {
	dir := setupTestDataDir(t)

	// Create as admin.
	err := runUserCreate([]string{
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
	err = runUserGrantAdmin([]string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)

	updated, err := q.GetUserByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated.IsAdmin)
}

func TestCLI_UserRevokeAdmin(t *testing.T) {
	dir := setupTestDataDir(t)

	// Create as admin.
	err := runUserCreate([]string{
		"--username", "alice",
		"--password", "TestPassword1!",
		"--admin",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)
	user, err := q.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)

	err = runUserRevokeAdmin([]string{"--id", user.ID, "--data-dir", dir})
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
	err := runUserRevokeAdmin([]string{"--id", user.ID, "--data-dir", dir})
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

	err := runUserListSessions([]string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)
}

// ---- User subcommand tests: edge cases ----

func TestCLI_UserCreate_MissingUsername(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate([]string{
		"--password", "TestPassword1!",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--username is required")
}

func TestCLI_UserCreate_MissingPassword(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate([]string{
		"--username", "alice",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a terminal")
}

func TestCLI_UserCreate_DuplicateUsername(t *testing.T) {
	dir := setupTestDataDir(t)
	createTestUser(t, dir, "alice")

	err := runUserCreate([]string{
		"--username", "alice",
		"--password", "TestPassword1!",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already taken")
}

func TestCLI_UserCreate_DuplicateEmail(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate([]string{
		"--username", "alice",
		"--password", "TestPassword1!",
		"--email", "shared@example.com",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	err = runUserCreate([]string{
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

	err := runUserGet([]string{"--id", "nonexistent-id", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCLI_UserGet_NeitherIDNorUsername(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserGet([]string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id or --username is required")
}

func TestCLI_UserUpdate_NotFound(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserUpdate([]string{
		"--id", "nonexistent-id",
		"--display-name", "Ghost",
		"--data-dir", dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCLI_UserDelete_NotFound(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserDelete([]string{"--id", "nonexistent-id", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCLI_UserDelete_AdminRequiresForce(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserCreate([]string{
		"--username", "adminuser",
		"--password", "TestPassword1!",
		"--admin",
		"--data-dir", dir,
	})
	require.NoError(t, err)

	// Without --force, deleting an admin user should fail.
	err = runUserDelete([]string{"--username", "adminuser", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")

	// With --force, it should succeed.
	err = runUserDelete([]string{"--username", "adminuser", "--force", "--data-dir", dir})
	require.NoError(t, err)

	_, q := openTestDB(t, dir)
	_, err = q.GetUserByUsername(context.Background(), "adminuser")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestCLI_UserDelete_InvisibleToLookup(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	err := runUserDelete([]string{"--id", user.ID, "--data-dir", dir})
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

	err := runUserDelete([]string{"--id", user.ID, "--data-dir", dir})
	require.NoError(t, err)

	_, q = openTestDB(t, dir)

	worker, err := q.GetWorkerByID(context.Background(), workerID)
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

	err = runUserDelete([]string{"--id", user.ID, "--data-dir", dir})
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

	err := runUserResetPassword([]string{"--id", user.ID, "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a terminal")
}

func TestCLI_UserResetPassword_NotFound(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runUserResetPassword([]string{
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

	err := runSessionList([]string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_SessionRevoke(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	sessionID := createTestSession(t, q, user.ID, time.Now().UTC().Add(24*time.Hour))

	err := runSessionRevoke([]string{"--id", sessionID, "--data-dir", dir})
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

	err := runSessionRevokeUser([]string{"--user-id", user.ID, "--data-dir", dir})
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

	err := runSessionRevokeUser([]string{"--username", "alice", "--data-dir", dir})
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
	err := runSessionRevokeUser([]string{"--user-id", alice.ID, "--data-dir", dir})
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

	err := runSessionPurgeExpired([]string{"--data-dir", dir})
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

	err := runSessionRevoke([]string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

func TestCLI_SessionRevokeUser_NeitherIDNorUsername(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runSessionRevokeUser([]string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id or --username is required")
}

func TestCLI_SessionPurgeExpired_NoneExpired(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")

	// Create only active sessions.
	_, q := openTestDB(t, dir)
	createTestSession(t, q, user.ID, time.Now().UTC().Add(24*time.Hour))

	err := runSessionPurgeExpired([]string{"--data-dir", dir})
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

	err := runWorkerList([]string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_WorkerList_AllStatuses(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	w1 := createTestWorker(t, q, user.ID)
	createTestWorker(t, q, user.ID)

	// Deregister one worker so we have mixed statuses.
	err := runWorkerDeregister([]string{"--id", w1, "--data-dir", dir})
	require.NoError(t, err)

	// List all statuses should succeed.
	err = runWorkerList([]string{"--status", "all", "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_WorkerList_Empty(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerList([]string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_WorkerGet(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	workerID := createTestWorker(t, q, user.ID)

	err := runWorkerGet([]string{"--id", workerID, "--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_WorkerDeregister(t *testing.T) {
	dir := setupTestDataDir(t)
	user := createTestUser(t, dir, "alice")
	_, q := openTestDB(t, dir)
	workerID := createTestWorker(t, q, user.ID)

	err := runWorkerDeregister([]string{"--id", workerID, "--data-dir", dir})
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

	err := runWorkerGet([]string{"--id", "nonexistent-worker", "--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCLI_WorkerGet_MissingID(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerGet([]string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

func TestCLI_WorkerDeregister_MissingID(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runWorkerDeregister([]string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id is required")
}

// ---- DB subcommand tests ----

func TestCLI_DBPath(t *testing.T) {
	dir := setupTestDataDir(t)

	// Verify no error. The printed path should match the expected DB path.
	err := runDBPath([]string{"--data-dir", dir})
	require.NoError(t, err)
}

func TestCLI_DBBackup(t *testing.T) {
	dir := setupTestDataDir(t)
	backupPath := filepath.Join(dir, "backup.db")

	err := runDBBackup([]string{"--output", backupPath, "--data-dir", dir})
	require.NoError(t, err)

	// Verify the backup file is a valid SQLite database by opening it.
	backupDB, err := db.Open(backupPath)
	require.NoError(t, err)
	defer func() { _ = backupDB.Close() }()

	// Verify we can query it.
	q := gendb.New(backupDB)
	_, err = q.ListAllUsers(context.Background(), gendb.ListAllUsersParams{
		Limit:  10,
		Offset: 0,
	})
	require.NoError(t, err)
}

func TestCLI_DBBackup_MissingOutput(t *testing.T) {
	dir := setupTestDataDir(t)

	err := runDBBackup([]string{"--data-dir", dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--output is required")
}
