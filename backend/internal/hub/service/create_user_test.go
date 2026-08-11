package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/sections"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

func setupCreateUserTestDB(t *testing.T) store.Store {
	return hubtestutil.OpenTestStore(t)
}

func createSimpleUser(t *testing.T, st store.Store, username, email string) *store.User {
	t.Helper()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	user, err := CreateUser(context.Background(), st, CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		DisplayName:  username,
		Email:        email,
		PasswordSet:  true,
	})
	require.NoError(t, err)
	return user
}

// TestCreateUser_CreatesOneUserRowAndNoWorkspaces pins the user-owned model:
// CreateUser inserts exactly one users row and does not invent a WORKSPACE as a
// side effect of signup.
//
// It does write the default sidebar sections, in the same transaction, which is
// the one owned entity signup is allowed to create -- nothing backfills them
// later, so a user without them would have an empty sidebar forever.
// TestCreateUser_SeedsTheDefaultSections covers that half.
func TestCreateUser_CreatesOneUserRowAndNoWorkspaces(t *testing.T) {
	t.Parallel()

	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	before, err := st.Users().Count(ctx)
	require.NoError(t, err)

	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	user, err := CreateUser(ctx, st, CreateUserParams{
		Username:     "solo-user",
		PasswordHash: hash,
		DisplayName:  "Solo",
		Email:        "solo@example.com",
		PasswordSet:  true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, user.ID)

	got, err := st.Users().GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "solo-user", got.Username)
	assert.Equal(t, "Solo", got.DisplayName)
	assert.Equal(t, "solo@example.com", got.Email)

	after, err := st.Users().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, before+1, after, "CreateUser must insert exactly one users row")

	workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
		UserID: userid.MustNew(user.ID),
	})
	require.NoError(t, err)
	assert.Empty(t, workspaces, "CreateUser must not create workspaces as a signup side effect")
}

// The default sections must land with the user row, because no read path
// creates them any more. A user without them shows an empty sidebar forever.
func TestCreateUser_SeedsTheDefaultSections(t *testing.T) {
	t.Parallel()

	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	user := createSimpleUser(t, st, "seeded-user", "seeded@example.com")

	got, err := st.WorkspaceSections().ListByUserID(ctx, userid.MustNew(user.ID))
	require.NoError(t, err)
	assert.Len(t, got, sections.Count, "signup seeds every default section")
}

// The sections go in the SAME transaction as the user row, so a failure after
// the user insert must leave NEITHER behind. A duplicate username fails inside
// that transaction, which is the reachable way to test it.
func TestCreateUser_FailedSignupLeavesNoSections(t *testing.T) {
	t.Parallel()

	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	first := createSimpleUser(t, st, "taken", "first@example.com")

	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	_, err = CreateUser(ctx, st, CreateUserParams{
		Username:     "taken",
		PasswordHash: hash,
		DisplayName:  "Second",
		PasswordSet:  true,
	})
	require.Error(t, err, "a duplicate username must fail")

	// The winner keeps exactly one set; the loser wrote none. A per-user count
	// is what proves the rollback: a global count would also pass if the failed
	// attempt had left an orphan set under a different user id.
	got, err := st.WorkspaceSections().ListByUserID(ctx, userid.MustNew(first.ID))
	require.NoError(t, err)
	assert.Len(t, got, sections.Count, "the successful signup keeps its own set")

	users, err := st.Users().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), users, "the failed signup rolled its user row back")
}

// TestCreateUser_DuplicateUsernameErrorIsNotDoublePrefixed pins the shape of the
// error a duplicate signup surfaces. The store already returns an
// ErrConflict-chain error whose text begins "conflict: ", and every signup path
// in auth_service.go hands the result to the client as CodeInternal -- so
// wrapping it in a second "conflict: " layer was directly user-visible as
// "create user: conflict: conflict: UNIQUE constraint failed: users.username".
// The ErrorIs assertion is the control: collapsing the wrap must not cost
// callers their ability to classify the failure.
func TestCreateUser_DuplicateUsernameErrorIsNotDoublePrefixed(t *testing.T) {
	t.Parallel()

	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	createSimpleUser(t, st, "dupe-user", "first@example.com")

	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	_, err = CreateUser(ctx, st, CreateUserParams{
		Username:     "dupe-user",
		PasswordHash: hash,
		DisplayName:  "Dupe",
		Email:        "second@example.com",
		PasswordSet:  true,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, store.ErrConflict, "a duplicate username must still classify as a conflict")
	assert.NotContains(t, err.Error(), "conflict: conflict:",
		"the store's error already carries the conflict prefix; wrapping adds a second one")
}

func TestSetPendingEmailWithToken_RejectsAlreadyVerifiedEmail(t *testing.T) {
	t.Parallel()

	st := setupCreateUserTestDB(t)
	ctx := context.Background()
	sender := mail.NewStubSender()

	// User A has verified email.
	createSimpleUser(t, st, "user-a", "taken@example.com")

	// User B exists without email.
	userB := createSimpleUser(t, st, "user-b", "")

	// User B tries to set pending_email to the already-verified address.
	err := issuePendingEmailVerificationOrRollback(ctx, st, sender, mail.Renderer{HubURL: "https://hub.example.test"}, userB.ID, "taken@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in use")

	// Verify user B's pending_email was NOT set.
	updated, err := st.Users().GetByID(ctx, userB.ID)
	require.NoError(t, err)
	assert.Empty(t, updated.PendingEmail)
}

func TestSetPendingEmailWithToken_StoresPendingForUnclaimedEmail(t *testing.T) {
	t.Parallel()

	st := setupCreateUserTestDB(t)
	ctx := context.Background()
	sender := mail.NewStubSender()

	user := createSimpleUser(t, st, "user-a", "")

	err := issuePendingEmailVerificationOrRollback(ctx, st, sender, mail.Renderer{HubURL: "https://hub.example.test"}, user.ID, "free@example.com")
	require.NoError(t, err)

	// The row stays pending until the user submits a code via UserService.VerifyEmail.
	updated, err := st.Users().GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Empty(t, updated.Email)
	assert.Equal(t, "free@example.com", updated.PendingEmail)
	assert.Equal(t, verifycode.Length, len(updated.PendingEmailToken))
	assert.Zero(t, updated.PendingEmailAttempts)
}

func TestCreateUser_ClearsCompetingPendingEmails(t *testing.T) {
	t.Parallel()

	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	// User A sets pending_email.
	userA := createSimpleUser(t, st, "user-a", "")
	expiresAt := mustTime(t)
	err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    userA.ID,
		PendingEmail:          "race@example.com",
		PendingEmailToken:     verifycode.Generate(),
		PendingEmailExpiresAt: &expiresAt,
	})
	require.NoError(t, err)

	// User B signs up with that email directly.
	hash, _ := password.Hash("testpass")
	_, err = CreateUser(ctx, st, CreateUserParams{
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
	t.Parallel()

	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	// User A has pending_email.
	userA := createSimpleUser(t, st, "user-a", "")
	expiresAt := mustTime(t)
	err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    userA.ID,
		PendingEmail:          "target@example.com",
		PendingEmailToken:     verifycode.Generate(),
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
	t.Parallel()

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
