package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/util/id"
)

func setupCreateUserTestDB(t *testing.T) (*sql.DB, *gendb.Queries) {
	t.Helper()
	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(sqlDB))
	return sqlDB, gendb.New(sqlDB)
}

func createSimpleUser(t *testing.T, sqlDB *sql.DB, q *gendb.Queries, username, email string) *gendb.User {
	t.Helper()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	user, err := CreateUserWithOrg(context.Background(), sqlDB, q, CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		DisplayName:  username,
		Email:        email,
		PasswordSet:  1,
	})
	require.NoError(t, err)
	return user
}

func TestSetPendingEmailWithToken_RejectsAlreadyVerifiedEmail(t *testing.T) {
	sqlDB, q := setupCreateUserTestDB(t)
	ctx := context.Background()

	// User A has verified email.
	createSimpleUser(t, sqlDB, q, "user-a", "taken@example.com")

	// User B exists without email.
	userB := createSimpleUser(t, sqlDB, q, "user-b", "")

	// User B tries to set pending_email to the already-verified address.
	err := setPendingEmailWithToken(ctx, q, userB.ID, "taken@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in use")

	// Verify user B's pending_email was NOT set.
	updated, err := q.GetUserByID(ctx, userB.ID)
	require.NoError(t, err)
	assert.Empty(t, updated.PendingEmail)
}

func TestSetPendingEmailWithToken_AllowsUnclaimedEmail(t *testing.T) {
	sqlDB, q := setupCreateUserTestDB(t)
	ctx := context.Background()

	user := createSimpleUser(t, sqlDB, q, "user-a", "")

	err := setPendingEmailWithToken(ctx, q, user.ID, "free@example.com")
	require.NoError(t, err)

	// Stub auto-verifies, so email should be promoted.
	updated, err := q.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "free@example.com", updated.Email)
}

func TestCreateUserWithOrg_ClearsCompetingPendingEmails(t *testing.T) {
	sqlDB, q := setupCreateUserTestDB(t)
	ctx := context.Background()

	// User A sets pending_email.
	userA := createSimpleUser(t, sqlDB, q, "user-a", "")
	err := q.SetPendingEmail(ctx, gendb.SetPendingEmailParams{
		PendingEmail:      "race@example.com",
		PendingEmailToken: id.Generate(),
		ID:                userA.ID,
	})
	require.NoError(t, err)

	// User B signs up with that email directly.
	hash, _ := password.Hash("testpass")
	_, err = CreateUserWithOrg(ctx, sqlDB, q, CreateUserParams{
		Username:     "user-b",
		PasswordHash: hash,
		DisplayName:  "User B",
		Email:        "race@example.com",
		PasswordSet:  1,
	})
	require.NoError(t, err)

	// User A's pending_email should be cleared.
	updatedA, err := q.GetUserByID(ctx, userA.ID)
	require.NoError(t, err)
	assert.Empty(t, updatedA.PendingEmail)
}

func TestSetEmailAndClearCompeting(t *testing.T) {
	sqlDB, q := setupCreateUserTestDB(t)
	ctx := context.Background()

	// User A has pending_email.
	userA := createSimpleUser(t, sqlDB, q, "user-a", "")
	err := q.SetPendingEmail(ctx, gendb.SetPendingEmailParams{
		PendingEmail:      "target@example.com",
		PendingEmailToken: id.Generate(),
		ID:                userA.ID,
	})
	require.NoError(t, err)

	// User B gets verified email via SetEmailAndClearCompeting.
	userB := createSimpleUser(t, sqlDB, q, "user-b", "")
	err = SetEmailAndClearCompeting(ctx, q, userB.ID, "target@example.com", 1)
	require.NoError(t, err)

	// User B has verified email.
	updatedB, err := q.GetUserByID(ctx, userB.ID)
	require.NoError(t, err)
	assert.Equal(t, "target@example.com", updatedB.Email)
	assert.Equal(t, int64(1), updatedB.EmailVerified)

	// User A's pending_email should be cleared.
	updatedA, err := q.GetUserByID(ctx, userA.ID)
	require.NoError(t, err)
	assert.Empty(t, updatedA.PendingEmail)
}

func TestSetEmailAndClearCompeting_Unverified(t *testing.T) {
	sqlDB, q := setupCreateUserTestDB(t)
	ctx := context.Background()

	user := createSimpleUser(t, sqlDB, q, "user-a", "")
	err := SetEmailAndClearCompeting(ctx, q, user.ID, "new@example.com", 0)
	require.NoError(t, err)

	updated, err := q.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "new@example.com", updated.Email)
	assert.Equal(t, int64(0), updated.EmailVerified)
}
