package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

func setupCreateUserTestDB(t *testing.T) store.Store {
	return hubtestutil.OpenTestStore(t)
}

func createSimpleUser(t *testing.T, st store.Store, username, email string) *store.User {
	t.Helper()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	user, err := CreateUserWithOrg(context.Background(), st, CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		DisplayName:  username,
		Email:        email,
		PasswordSet:  true,
	})
	require.NoError(t, err)
	return user
}

func TestSetPendingEmailWithToken_RejectsAlreadyVerifiedEmail(t *testing.T) {
	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	// User A has verified email.
	createSimpleUser(t, st, "user-a", "taken@example.com")

	// User B exists without email.
	userB := createSimpleUser(t, st, "user-b", "")

	// User B tries to set pending_email to the already-verified address.
	err := setPendingEmailWithToken(ctx, st, userB.ID, "taken@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in use")

	// Verify user B's pending_email was NOT set.
	updated, err := st.Users().GetByID(ctx, userB.ID)
	require.NoError(t, err)
	assert.Empty(t, updated.PendingEmail)
}

func TestSetPendingEmailWithToken_AllowsUnclaimedEmail(t *testing.T) {
	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	user := createSimpleUser(t, st, "user-a", "")

	err := setPendingEmailWithToken(ctx, st, user.ID, "free@example.com")
	require.NoError(t, err)

	// Stub auto-verifies, so email should be promoted.
	updated, err := st.Users().GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "free@example.com", updated.Email)
}

func TestCreateUserWithOrg_ClearsCompetingPendingEmails(t *testing.T) {
	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	// User A sets pending_email.
	userA := createSimpleUser(t, st, "user-a", "")
	expiresAt := mustTime(t)
	err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    userA.ID,
		PendingEmail:          "race@example.com",
		PendingEmailToken:     id.Generate(),
		PendingEmailExpiresAt: &expiresAt,
	})
	require.NoError(t, err)

	// User B signs up with that email directly.
	hash, _ := password.Hash("testpass")
	_, err = CreateUserWithOrg(ctx, st, CreateUserParams{
		Username:     "user-b",
		PasswordHash: hash,
		DisplayName:  "User B",
		Email:        "race@example.com",
		PasswordSet:  true,
	})
	require.NoError(t, err)

	// User A's pending_email should be cleared.
	updatedA, err := st.Users().GetByID(ctx, userA.ID)
	require.NoError(t, err)
	assert.Empty(t, updatedA.PendingEmail)
}

func TestSetEmailAndClearCompeting(t *testing.T) {
	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	// User A has pending_email.
	userA := createSimpleUser(t, st, "user-a", "")
	expiresAt := mustTime(t)
	err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    userA.ID,
		PendingEmail:          "target@example.com",
		PendingEmailToken:     id.Generate(),
		PendingEmailExpiresAt: &expiresAt,
	})
	require.NoError(t, err)

	// User B gets verified email via SetEmailAndClearCompeting.
	userB := createSimpleUser(t, st, "user-b", "")
	err = SetEmailAndClearCompeting(ctx, st, userB.ID, "target@example.com", true)
	require.NoError(t, err)

	// User B has verified email.
	updatedB, err := st.Users().GetByID(ctx, userB.ID)
	require.NoError(t, err)
	assert.Equal(t, "target@example.com", updatedB.Email)
	assert.True(t, updatedB.EmailVerified)

	// User A's pending_email should be cleared.
	updatedA, err := st.Users().GetByID(ctx, userA.ID)
	require.NoError(t, err)
	assert.Empty(t, updatedA.PendingEmail)
}

func TestSetEmailAndClearCompeting_Unverified(t *testing.T) {
	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	user := createSimpleUser(t, st, "user-a", "")
	err := SetEmailAndClearCompeting(ctx, st, user.ID, "new@example.com", false)
	require.NoError(t, err)

	updated, err := st.Users().GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "new@example.com", updated.Email)
	assert.False(t, updated.EmailVerified)
}

// mustTime returns a future time for pending email expiry tests.
func mustTime(t *testing.T) time.Time {
	t.Helper()
	return time.Now().Add(24 * time.Hour).UTC()
}
