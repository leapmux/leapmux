package service

import (
	"context"
	"errors"
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

// failingSectionsStore fails every WorkspaceSections().Create, leaving every
// other operation to the real store. It exists to fail INSIDE the transaction
// but AFTER the user row is written, which is the only shape that exercises the
// rollback: a duplicate username aborts at the first statement, so nothing has
// been written for the transaction to undo.
type failingSectionsStore struct {
	store.Store
}

func (s failingSectionsStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(failingSectionsStore{Store: tx})
	})
}

func (s failingSectionsStore) WorkspaceSections() store.WorkspaceSectionStore {
	return failingSectionStore{WorkspaceSectionStore: s.Store.WorkspaceSections()}
}

type failingSectionStore struct {
	store.WorkspaceSectionStore
}

func (failingSectionStore) Create(context.Context, store.CreateWorkspaceSectionParams) error {
	return errors.New("injected section failure")
}

// A failure while seeding the sections must roll the USER ROW back with them:
// the two writes share one transaction precisely so a user can never exist
// without a sidebar.
func TestCreateUser_SectionFailureRollsBackTheUserRow(t *testing.T) {
	t.Parallel()

	st := setupCreateUserTestDB(t)
	ctx := context.Background()

	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	_, err = CreateUser(ctx, failingSectionsStore{Store: st}, CreateUserParams{
		Username:     "rollback",
		PasswordHash: hash,
		DisplayName:  "Rollback",
		PasswordSet:  true,
	})
	require.Error(t, err, "the injected section failure must surface")

	users, err := st.Users().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), users, "the user row rolled back with the sections")
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
	err := issuePendingEmailVerificationOrFail(ctx, st, sender, mail.Renderer{BaseURL: func() string { return "https://hub.example.test" }}, userB.ID, "taken@example.com")
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

	err := issuePendingEmailVerificationOrFail(ctx, st, sender, mail.Renderer{BaseURL: func() string { return "https://hub.example.test" }}, user.ID, "free@example.com")
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
	_, err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    userA.ID,
		PendingEmail:          "race@example.com",
		PendingEmailToken:     verifycode.Generate(),
		PendingEmailExpiresAt: &expiresAt,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
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
	_, err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                    userA.ID,
		PendingEmail:          "target@example.com",
		PendingEmailToken:     verifycode.Generate(),
		PendingEmailExpiresAt: &expiresAt,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
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

// Every account-creation invariant lives in createUserInTx, so every
// sign-up flavor gets it. These pin the two that used to sit in CreateUser
// alone, which the OAuth and passkey flavors called past.
func TestCreateUserInTx_EnforcesTheAdminInvariants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// An admin is always email_verified in the database, whatever the
	// caller passed: the stored flag is what keeps the auth interceptor's
	// runtime IsAdmin exemption honest.
	t.Run("an admin is always email_verified", func(t *testing.T) {
		t.Parallel()
		st := setupCreateUserTestDB(t)
		user, code, err := createUserInTx(ctx, st, createUserTxParams{
			username: "forced-admin", displayName: "Forced", isAdmin: true,
			emailVerified: false, passwordHash: "hash", passwordSet: true,
		})
		require.NoError(t, err)
		assert.True(t, user.EmailVerified, "the caller's false must not survive")
		assert.Empty(t, code, "no pending row, so no code")
	})

	// An admin never waits behind a pending verification row: the address
	// moves to the email column instead. Committing an admin with email=''
	// and email_verified=1 leaves the hub's only administrator with no
	// address to reset a password to, and nothing prompts them for one.
	t.Run("an admin's pending address is promoted into the email column", func(t *testing.T) {
		t.Parallel()
		st := setupCreateUserTestDB(t)
		user, code, err := createUserInTx(ctx, st, createUserTxParams{
			username: "promoted-admin", displayName: "Promoted", isAdmin: true,
			email: "", pendingEmail: "admin@example.com",
			passwordHash: "hash", passwordSet: true,
		})
		require.NoError(t, err)
		assert.Equal(t, "admin@example.com", user.Email)
		assert.True(t, user.EmailVerified)
		assert.Empty(t, user.PendingEmail, "there is nothing left to verify")
		// The returned code is what the caller keys its send on. An empty
		// one means "no pending row was written", and the passkey first-user
		// branch mailed a BLANK code -- and rolled the hub's only admin back
		// when that pointless send failed -- until it read this instead of
		// its own pre-call intent.
		assert.Empty(t, code, "no pending row was written, so there is nothing to mail")
	})

	// A non-admin keeps the pending row, and the code comes back for the
	// caller to deliver.
	t.Run("a non-admin keeps its pending row and returns the code", func(t *testing.T) {
		t.Parallel()
		st := setupCreateUserTestDB(t)
		user, code, err := createUserInTx(ctx, st, createUserTxParams{
			username: "pending-user", displayName: "Pending",
			pendingEmail: "user@example.com", passwordHash: "hash", passwordSet: true,
		})
		require.NoError(t, err)
		assert.Empty(t, user.Email)
		assert.False(t, user.EmailVerified)
		assert.Equal(t, "user@example.com", user.PendingEmail)
		assert.Len(t, code, verifycode.Length, "the caller mails this code")
	})

	// The claim clears every other account's pending target for the same
	// address, inside the same transaction. Without it the loser is pinned
	// on /verify-email with a dead row for an address it can never take.
	t.Run("claiming an address clears a competing pending row", func(t *testing.T) {
		t.Parallel()
		st := setupCreateUserTestDB(t)
		loser, _, err := createUserInTx(ctx, st, createUserTxParams{
			username: "loser", displayName: "Loser",
			pendingEmail: "contested@example.com", passwordHash: "hash", passwordSet: true,
		})
		require.NoError(t, err)
		require.Equal(t, "contested@example.com", loser.PendingEmail)

		_, _, err = createUserInTx(ctx, st, createUserTxParams{
			username: "winner", displayName: "Winner",
			email: "contested@example.com", emailVerified: true,
			passwordHash: "hash", passwordSet: true,
		})
		require.NoError(t, err)

		after, err := st.Users().GetByID(ctx, loser.ID)
		require.NoError(t, err)
		assert.Empty(t, after.PendingEmail,
			"a claimed address must not stay pending for anyone else")
	})
}
