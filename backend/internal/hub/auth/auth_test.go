package auth_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func setupStore(t *testing.T) store.Store {
	return hubtestutil.OpenTestStore(t)
}

// mustBoundOrg builds the concrete-org binding the CRDT-path predicates
// require. It fails the test on an empty id rather than silently handing over a
// zero BoundOrg, so a fixture bug cannot masquerade as an authorization deny.
func mustBoundOrg(t *testing.T, orgID string) auth.BoundOrg {
	t.Helper()
	bound, ok := auth.NewBoundOrg(orgID)
	require.True(t, ok, "test fixture must supply a non-empty org id")
	return bound
}

func createTestUser(t *testing.T, st store.Store) (orgID, userID string) {
	t.Helper()
	ctx := context.Background()

	orgID = id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "test-org"}))

	hash, err := password.Hash("password123")
	require.NoError(t, err)

	userID = id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "testuser",
		PasswordHash: hash,
		DisplayName:  "Test User",
		PasswordSet:  true,
		IsAdmin:      true,
	}))

	return orgID, userID
}

func TestLogin_Success(t *testing.T) {
	st := setupStore(t)
	orgID, userID := createTestUser(t, st)
	ctx := context.Background()

	token, user, _, err := auth.Login(ctx, st, "testuser", "password123")
	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.Equal(t, userID, user.ID)
	assert.Equal(t, orgID, user.OrgID)
}

// Workspace access is owner-only: no other user -- same org or not -- may
// read someone else's workspace.
func TestWorkspaceCanReadIsOwnerOnly(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID := id.Generate()
	otherOrgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "workspace-org"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: otherOrgID, Name: "other-org"}))
	ownerID := id.Generate()
	sameOrgID := id.Generate()
	outsiderID := id.Generate()
	for _, user := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgID, Username: "owner", PasswordHash: "hash", DisplayName: "Owner", PasswordSet: true},
		{ID: sameOrgID, OrgID: orgID, Username: "member", PasswordHash: "hash", DisplayName: "Member", PasswordSet: true},
		{ID: outsiderID, OrgID: otherOrgID, Username: "outsider", PasswordHash: "hash", DisplayName: "Outsider", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, user))
	}
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: workspaceID, OrgID: orgID, OwnerUserID: userid.MustNew(ownerID), Title: "mine",
	}))

	allowed, err := auth.WorkspaceCanRead(ctx, st, auth.AnyOrg(), workspaceID, userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.True(t, allowed, "the owner reads their own workspace")

	allowed, err = auth.WorkspaceCanRead(ctx, st, auth.AnyOrg(), workspaceID, userid.MustNew(sameOrgID))
	require.NoError(t, err)
	assert.False(t, allowed, "a same-org non-owner is denied")

	allowed, err = auth.WorkspaceCanRead(ctx, st, auth.AnyOrg(), workspaceID, userid.MustNew(outsiderID))
	require.NoError(t, err)
	assert.False(t, allowed, "a user from another org is denied")

	allowed, err = auth.WorkspaceCanRead(ctx, st, auth.AnyOrg(), "missing-workspace", userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.False(t, allowed, "a missing workspace is a deny, not an error")

	// Empty inputs fail closed without a store round-trip.
	allowed, err = auth.WorkspaceCanRead(ctx, st, auth.AnyOrg(), "", userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.False(t, allowed, "an empty workspace id fails closed")
	allowed, err = auth.WorkspaceCanRead(ctx, st, auth.AnyOrg(), workspaceID, userid.UserID{})
	require.NoError(t, err)
	assert.False(t, allowed, "a zero user id fails closed")
}

// WorkspaceReadableByUsersInOrg is the batch counterpart of
// WorkspaceCanAccessInOrg used by the CRDT subscriber-expansion fan-out. It
// must agree with the per-user check for every user, fail closed on a wrong
// org, and deny an unknown workspace.
func TestWorkspaceReadableByUsersInOrg(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID := id.Generate()
	otherOrgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "org"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: otherOrgID, Name: "other"}))
	ownerID := id.Generate()
	strangerID := id.Generate()
	for _, user := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgID, Username: "owner", PasswordHash: "hash", DisplayName: "Owner", PasswordSet: true},
		{ID: strangerID, OrgID: orgID, Username: "stranger", PasswordHash: "hash", DisplayName: "Stranger", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, user))
	}
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: workspaceID, OrgID: orgID, OwnerUserID: userid.MustNew(ownerID), Title: "ws",
	}))

	// The zero UserID entry pins the fail-closed guard: it must never be
	// marked readable (and its String() key is ""), even though it can't
	// match a real owner.
	users := []userid.UserID{userid.MustNew(ownerID), userid.MustNew(strangerID), {}}
	readable, err := auth.WorkspaceReadableByUsersInOrg(ctx, st, mustBoundOrg(t, orgID), workspaceID, users)
	require.NoError(t, err)
	assert.True(t, readable[ownerID], "owner reads")
	assert.False(t, readable[strangerID], "a non-owner is denied")
	assert.False(t, readable[""], "a zero user id is never readable")

	// The batch verdict must match the per-user check for every user.
	for _, userID := range users {
		single, err := auth.WorkspaceCanAccessInOrg(ctx, st, mustBoundOrg(t, orgID), workspaceID, userID)
		require.NoError(t, err)
		assert.Equal(t, single, readable[userID.String()], "batch must agree with per-user check for %s", userID)
	}

	// A wrong org fails closed (deny all), and an unknown workspace denies all.
	wrongOrg, err := auth.WorkspaceReadableByUsersInOrg(ctx, st, mustBoundOrg(t, otherOrgID), workspaceID, users)
	require.NoError(t, err)
	assert.Empty(t, wrongOrg, "org cross-check must deny every user when orgID mismatches")
	missing, err := auth.WorkspaceReadableByUsersInOrg(ctx, st, mustBoundOrg(t, orgID), id.Generate(), users)
	require.NoError(t, err)
	assert.Empty(t, missing, "an unknown workspace denies every user")

	// Empty inputs short-circuit to an empty (non-nil) result.
	empty, err := auth.WorkspaceReadableByUsersInOrg(ctx, st, mustBoundOrg(t, orgID), workspaceID, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// WorkspacesReadableByUser is the many-workspaces/single-user read resolver.
// It must honor owner-only access, apply BindOrg when set, skip the binding
// via AnyOrg (the delegation contract), deny on a zero OrgBinding / BindOrg(""),
// drop unknown IDs, dedup the request, and preserve input order.
func TestWorkspacesReadableByUser(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgA := id.Generate()
	orgB := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgA, Name: "a"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgB, Name: "b"}))
	ownerID := id.Generate()
	outsiderID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgA, Username: "owner", PasswordHash: "h", DisplayName: "O", PasswordSet: true},
		{ID: outsiderID, OrgID: orgA, Username: "outsider", PasswordHash: "h", DisplayName: "X", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	// owner owns two workspaces in orgA and one in orgB.
	wsOwnA1 := id.Generate()
	wsOwnA2 := id.Generate()
	wsOwnB := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwnA1, OrgID: orgA, OwnerUserID: userid.MustNew(ownerID), Title: "own-a1"}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwnA2, OrgID: orgA, OwnerUserID: userid.MustNew(ownerID), Title: "own-a2"}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwnB, OrgID: orgB, OwnerUserID: userid.MustNew(ownerID), Title: "own-b"}))

	unknown := id.Generate()
	// Request order deliberately interleaves the cross-org and unknown IDs.
	requested := []string{wsOwnA2, wsOwnB, wsOwnA1, unknown}
	owner := userid.MustNew(ownerID)
	outsider := userid.MustNew(outsiderID)

	// orgA binding: owner sees its two orgA workspaces in request order; the
	// cross-org wsOwnB is excluded and the unknown ID is dropped.
	inA, err := auth.WorkspacesReadableByUser(ctx, st, auth.BindOrg(orgA), owner, requested)
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwnA2, wsOwnA1}, inA, "org binding excludes the cross-org workspace and unknown IDs, preserving input order")

	// AnyOrg skips the org binding (delegation contract): the owner now
	// sees all three owned workspaces regardless of org, in request order.
	crossOrg, err := auth.WorkspacesReadableByUser(ctx, st, auth.AnyOrg(), owner, requested)
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwnA2, wsOwnB, wsOwnA1}, crossOrg, "AnyOrg resolves ownership across orgs")

	// BindOrg("") / zero OrgBinding deny everything (fail closed).
	emptyOrg, err := auth.WorkspacesReadableByUser(ctx, st, auth.BindOrg(""), owner, requested)
	require.NoError(t, err)
	assert.Empty(t, emptyOrg, "BindOrg(\"\") fails closed")
	var zeroBinding auth.OrgBinding
	zeroOrg, err := auth.WorkspacesReadableByUser(ctx, st, zeroBinding, owner, requested)
	require.NoError(t, err)
	assert.Empty(t, zeroOrg, "zero OrgBinding fails closed")

	// A non-owner reads nothing.
	outsiderGot, err := auth.WorkspacesReadableByUser(ctx, st, auth.BindOrg(orgA), outsider, requested)
	require.NoError(t, err)
	assert.Empty(t, outsiderGot, "no ownership means no read access")

	// Zero UserID denies everything.
	zeroUser, err := auth.WorkspacesReadableByUser(ctx, st, auth.BindOrg(orgA), userid.UserID{}, requested)
	require.NoError(t, err)
	assert.Empty(t, zeroUser, "zero UserID fails closed")

	// Duplicate requested IDs collapse to one.
	dedup, err := auth.WorkspacesReadableByUser(ctx, st, auth.BindOrg(orgA), owner, []string{wsOwnA1, wsOwnA1, wsOwnA1})
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwnA1}, dedup, "duplicate requested IDs collapse")

	// Empty inputs short-circuit.
	none, err := auth.WorkspacesReadableByUser(ctx, st, auth.BindOrg(orgA), owner, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// WorkspaceCanAccessInOrg is the single owner-only predicate behind both the
// CRDT read and write gates. It applies the org cross-check and the
// missing/out-of-org deny prologue (loadWorkspaceInOrg).
func TestWorkspaceCanAccessInOrg(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgA := id.Generate()
	orgB := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgA, Name: "a"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgB, Name: "b"}))
	ownerID := id.Generate()
	otherID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgA, Username: "owner", PasswordHash: "h", DisplayName: "O", PasswordSet: true},
		{ID: otherID, OrgID: orgA, Username: "other", PasswordHash: "h", DisplayName: "G", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	ws := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: ws, OrgID: orgA, OwnerUserID: userid.MustNew(ownerID), Title: "ws"}))
	owner := userid.MustNew(ownerID)
	other := userid.MustNew(otherID)

	// Owner may access.
	can, err := auth.WorkspaceCanAccessInOrg(ctx, st, mustBoundOrg(t, orgA), ws, owner)
	require.NoError(t, err)
	assert.True(t, can, "owner may access")

	// A non-owner may not.
	can, err = auth.WorkspaceCanAccessInOrg(ctx, st, mustBoundOrg(t, orgA), ws, other)
	require.NoError(t, err)
	assert.False(t, can, "a non-owner is denied")

	// Org cross-check: the owner cannot access the workspace bound to another org.
	can, err = auth.WorkspaceCanAccessInOrg(ctx, st, mustBoundOrg(t, orgB), ws, owner)
	require.NoError(t, err)
	assert.False(t, can, "org binding excludes a workspace homed in another org")

	// Missing workspace and empty inputs fail closed (a deny, not an error).
	can, err = auth.WorkspaceCanAccessInOrg(ctx, st, mustBoundOrg(t, orgA), id.Generate(), owner)
	require.NoError(t, err)
	assert.False(t, can, "a missing workspace is a deny, not an error")
	// The empty-org deny moved to the constructor: NewBoundOrg refuses it, so a
	// caller must branch explicitly instead of relying on a silent prologue.
	// AnyOrg() cannot be converted to a BoundOrg at all, which is the point --
	// on this path it used to compile and then deny every workspace.
	_, boundOK := auth.NewBoundOrg("")
	assert.False(t, boundOK, "NewBoundOrg(\"\") must refuse")
	can, err = auth.WorkspaceCanAccessInOrg(ctx, st, auth.BoundOrg{}, ws, owner)
	require.NoError(t, err)
	assert.False(t, can, "a zero BoundOrg still fails closed")
	can, err = auth.WorkspaceCanAccessInOrg(ctx, st, mustBoundOrg(t, orgA), ws, userid.UserID{})
	require.NoError(t, err)
	assert.False(t, can, "zero UserID fails closed")
}

func TestLogin_InvalidPassword(t *testing.T) {
	st := setupStore(t)
	createTestUser(t, st)
	ctx := context.Background()

	_, _, _, err := auth.Login(ctx, st, "testuser", "wrongpassword")
	require.Error(t, err)
}

func TestLogin_UnknownUser(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	_, _, _, err := auth.Login(ctx, st, "nonexistent", "password")
	require.Error(t, err)
}

func TestLogin_HashUnchangedAfterLogin(t *testing.T) {
	st := setupStore(t)
	createTestUser(t, st)
	ctx := context.Background()

	user, err := st.Users().GetByUsername(ctx, "testuser")
	require.NoError(t, err)
	originalHash := user.PasswordHash

	_, _, _, err = auth.Login(ctx, st, "testuser", "password123")
	require.NoError(t, err)

	user, err = st.Users().GetByUsername(ctx, "testuser")
	require.NoError(t, err)
	assert.Equal(t, originalHash, user.PasswordHash, "argon2id hash should not change after login")
}

type beforeTransactionStore struct {
	store.Store
	before func() error
}

func (s *beforeTransactionStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	if err := s.before(); err != nil {
		return err
	}
	return s.Store.RunInTransaction(ctx, fn)
}

func (s *beforeTransactionStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	if err := s.before(); err != nil {
		return err
	}
	return s.Store.RunInUserAuthTransaction(ctx, userID, fn)
}

func TestLogin_RejectsOldPasswordRotatedAtTransactionBoundary(t *testing.T) {
	st := setupStore(t)
	_, userID := createTestUser(t, st)
	ctx := context.Background()
	newHash, err := password.Hash("new-password123")
	require.NoError(t, err)

	hooked := &beforeTransactionStore{
		Store: st,
		before: func() error {
			return st.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
				ID:           userID,
				PasswordHash: newHash,
			})
		},
	}

	_, _, _, err = auth.Login(ctx, hooked, "testuser", "password123")
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	sessions := storetest.ListAllSessions(t, st, userID)
	assert.Empty(t, sessions)
}

// TestLogin_AcceptsNewPasswordRotatedAtTransactionBoundary is the accept-branch
// twin of the reject test above. Because the password is verified before the
// auth transaction acquires its write lock, a rotation that commits at the
// transaction boundary makes the pre-lock verification stale. The login must
// re-verify against the committed hash inside the lock, so a caller presenting
// the NEW password still succeeds even though the pre-lock hash was the old one.
func TestLogin_AcceptsNewPasswordRotatedAtTransactionBoundary(t *testing.T) {
	st := setupStore(t)
	_, userID := createTestUser(t, st)
	ctx := context.Background()
	newHash, err := password.Hash("new-password123")
	require.NoError(t, err)

	hooked := &beforeTransactionStore{
		Store: st,
		before: func() error {
			return st.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
				ID:           userID,
				PasswordHash: newHash,
			})
		},
	}

	sessionID, user, _, err := auth.Login(ctx, hooked, "testuser", "new-password123")
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
	assert.Equal(t, userID, user.ID)
	sessions := storetest.ListAllSessions(t, st, userID)
	require.Len(t, sessions, 1)
}

func TestCredentialCreationOrdersAgainstUserRevocation(t *testing.T) {
	t.Run("credential created before revocation is invalidated", func(t *testing.T) {
		st := setupStore(t)
		_, userID := createTestUser(t, st)
		ctx := context.Background()

		sessionID, _, err := auth.CreateSession(ctx, st, userid.MustNew(userID))
		require.NoError(t, err)
		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			_, _, err := auth.RevokeAllUserCredentials(ctx, tx, userid.MustNew(userID))
			return err
		}))

		_, err = auth.ValidateToken(ctx, st, sessionID)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("credential created after revocation uses new generation", func(t *testing.T) {
		st := setupStore(t)
		_, userID := createTestUser(t, st)
		ctx := context.Background()

		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			_, _, err := auth.RevokeAllUserCredentials(ctx, tx, userid.MustNew(userID))
			return err
		}))
		sessionID, _, err := auth.CreateSession(ctx, st, userid.MustNew(userID))
		require.NoError(t, err)

		info, err := auth.ValidateToken(ctx, st, sessionID)
		require.NoError(t, err)
		user, err := st.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, user.AuthGeneration, info.UserAuthGeneration)
	})
}

func TestRevokeAllUserCredentialsEmitsOnlyGenerationEvent(t *testing.T) {
	st := setupStore(t)
	orgID, userID := createTestUser(t, st)
	ctx := context.Background()

	require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID:         id.Generate(),
		UserID:     userid.MustNew(userID),
		ClientType: "cli",
		ClientName: "test",
		SecretHash: []byte("hash"),
		Scope:      "remote:*",
	}))
	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(userID),
		PublicKey:       []byte("x25519"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: workspaceID, OrgID: orgID, OwnerUserID: userid.MustNew(userID), Title: "test",
	}))
	tabID := id.Generate()
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		OrgID: orgID, WorkspaceID: workspaceID, WorkerID: workerID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabID,
		Position: "a", TileID: "tile-1",
	}))
	require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
		ID: id.Generate(), UserID: userid.MustNew(userID), WorkerID: workerID,
		WorkspaceID: workspaceID, IssuedForTabID: tabID,
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       []byte("hash"), ExpiresAt: time.Now().Add(time.Hour),
	}))

	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		apiCount, delegationCount, err := auth.RevokeAllUserCredentials(ctx, tx, userid.MustNew(userID))
		require.Equal(t, int64(1), apiCount)
		require.Equal(t, int64(1), delegationCount)
		return err
	}))
	published, err := st.RevocationEvents().PublishPending(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), published)
	events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, store.RevocationEventKindUserTokens, events[0].Event.Kind)
	assert.Equal(t, int64(1), events[0].Event.UserAuthGeneration)
}

func TestValidateToken_Success(t *testing.T) {
	st := setupStore(t)
	createTestUser(t, st)
	ctx := context.Background()

	token, _, _, err := auth.Login(ctx, st, "testuser", "password123")
	require.NoError(t, err)

	info, err := auth.ValidateToken(ctx, st, token)
	require.NoError(t, err)
	assert.Equal(t, "testuser", info.Username)
	assert.True(t, info.IsAdmin)

	session, err := st.Sessions().GetByID(ctx, token)
	require.NoError(t, err)
	assert.True(t, info.AuthenticatedAt.Equal(session.CreatedAt.UTC()),
		"session auth basis should use the DB session creation timestamp")
}

func TestValidateToken_InvalidToken(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	_, err := auth.ValidateToken(ctx, st, "invalid-token")
	require.Error(t, err)
}

// A session joined to a blank user id must be REFUSED, not panic.
//
// This is the highest-traffic identity mint site -- every cookie-authenticated
// RPC -- and its input is store data, so a corrupt or hand-seeded row has to
// fail closed the same way the not-found branch does. Minting with MustNew
// would panic here instead, turning a denial into a torn connection that
// repeats on every retry with the same session.
func TestValidateToken_BlankUserIDIsUnauthenticatedNotPanic(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "blank-id-org"}))
	// SQLite accepts "" as a TEXT primary key, so a blank-id user row (and a
	// session referencing it) inserts cleanly -- the corrupt-data shape this
	// guards against.
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: "", OrgID: orgID, Username: "blank-id", PasswordHash: "h",
		DisplayName: "Blank", PasswordSet: true,
	}))
	token := id.Generate()
	require.NoError(t, st.Sessions().Create(ctx, store.CreateSessionParams{
		ID: token, UserID: userid.UserID{}, ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}))

	assert.NotPanics(t, func() {
		info, err := auth.ValidateToken(ctx, st, token)
		require.Error(t, err, "a blank joined user id must not authenticate")
		assert.Nil(t, info)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
			"it fails closed in the same shape as an invalid token, not as a fault")
	})
}

func TestContextUserRoundtrip(t *testing.T) {
	info := &auth.UserInfo{
		ID:       userid.MustNew("user-1"),
		OrgID:    "org-1",
		Username: "alice",
		IsAdmin:  true,
	}

	ctx := auth.WithUser(context.Background(), info)
	got := auth.GetUser(ctx)
	require.NotNil(t, got)
	assert.Equal(t, info.ID, got.ID)
}

func TestMustGetUser_NoUser(t *testing.T) {
	_, err := auth.MustGetUser(context.Background())
	require.Error(t, err)
}

func TestResolveOrgID_EmptyReturnsPersonalOrg(t *testing.T) {
	st := setupStore(t)
	orgID, userID := createTestUser(t, st)
	_ = st

	user := &auth.UserInfo{ID: userid.MustNew(userID), OrgID: orgID, Username: "testuser"}
	resolved, err := auth.ResolveOrgID(user, "")
	require.NoError(t, err)
	assert.Equal(t, orgID, resolved)
}

func TestResolveOrgID_OwnOrgReturnsOrgID(t *testing.T) {
	st := setupStore(t)
	orgID, userID := createTestUser(t, st)
	_ = st

	user := &auth.UserInfo{ID: userid.MustNew(userID), OrgID: orgID, Username: "testuser"}
	resolved, err := auth.ResolveOrgID(user, orgID)
	require.NoError(t, err)
	assert.Equal(t, orgID, resolved)
}

func TestResolveOrgID_ForeignOrgReturnsNotFound(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID, userID := createTestUser(t, st)

	otherOrgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: otherOrgID, Name: "other-org"}))

	user := &auth.UserInfo{ID: userid.MustNew(userID), OrgID: orgID, Username: "testuser"}
	_, err := auth.ResolveOrgID(user, otherOrgID)
	require.Error(t, err)

	connectErr, ok := err.(*connect.Error)
	require.True(t, ok, "expected *connect.Error")
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

// WorkspaceCanAccessInOrg is WorkspaceCanRead bound to a concrete org: it
// grants owner-only access when the workspace lives in the given org and is
// not soft-deleted, so a stale CRDT subscriber can't see a workspace re-homed
// to another org (or a deleted one).
func TestWorkspaceCanAccessInOrgEnforcesOrgBindingAndDeletion(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID := id.Generate()
	otherOrgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "canread-org"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: otherOrgID, Name: "canread-other-org"}))
	ownerID := id.Generate()
	strangerID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgID, Username: "cr-owner", PasswordHash: "hash", DisplayName: "Owner", PasswordSet: true},
		{ID: strangerID, OrgID: orgID, Username: "cr-stranger", PasswordHash: "hash", DisplayName: "Stranger", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	wsID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsID, OrgID: orgID, OwnerUserID: userid.MustNew(ownerID), Title: "readable"}))

	assertRead := func(bound auth.BoundOrg, userID userid.UserID, want bool, msg string) {
		t.Helper()
		got, err := auth.WorkspaceCanAccessInOrg(ctx, st, bound, wsID, userID)
		require.NoError(t, err)
		assert.Equal(t, want, got, msg)
	}
	assertRead(mustBoundOrg(t, orgID), userid.MustNew(ownerID), true, "owner reads in the workspace's org")
	assertRead(mustBoundOrg(t, orgID), userid.MustNew(strangerID), false, "a non-owner is denied")
	assertRead(mustBoundOrg(t, otherOrgID), userid.MustNew(ownerID), false, "the org binding rejects a mismatched org")
	// An empty org can no longer REACH this predicate: NewBoundOrg refuses it,
	// so the deny is now a call-site branch rather than a silent prologue.
	_, ok := auth.NewBoundOrg("")
	assert.False(t, ok, "NewBoundOrg(\"\") must refuse, so callers deny explicitly")
	assertRead(auth.BoundOrg{}, userid.MustNew(ownerID), false, "a zero BoundOrg still fails closed")
	assertRead(mustBoundOrg(t, orgID), userid.UserID{}, false, "zero UserID fails closed")

	missing, err := auth.WorkspaceCanAccessInOrg(ctx, st, mustBoundOrg(t, orgID), "missing-workspace", userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.False(t, missing, "a missing workspace is denied")

	// A soft-deleted workspace is unreadable even by its owner.
	_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: userid.MustNew(ownerID)})
	require.NoError(t, err)
	assertRead(mustBoundOrg(t, orgID), userid.MustNew(ownerID), false, "a soft-deleted workspace is unreadable")
}

// WorkspaceCanRead must fail closed on an empty workspaceID at its OWN boundary
// -- not one helper deeper in loadWorkspace -- so a future refactor that swaps
// loadWorkspace for a lookup without the empty-id guard cannot let an empty id
// reach IsOwner (which would then answer against whatever workspace row a cache
// or bulk path handed back). The zero UserID fail-close is the same shape;
// both are mechanical here rather than dependent on a helper keeping its guard.
func TestWorkspaceCanReadFailsClosedOnEmptyIDs(t *testing.T) {
	st := setupStore(t)
	for _, tc := range []struct {
		name        string
		userID      userid.UserID
		workspaceID string
	}{
		{"empty workspace id", userid.MustNew("real-user"), ""},
		{"zero user id", userid.UserID{}, "real-ws"},
		{"both empty/zero", userid.UserID{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := auth.WorkspaceCanRead(context.Background(), st, auth.AnyOrg(), tc.workspaceID, tc.userID)
			assert.NoError(t, err)
			assert.False(t, ok, "an empty userID or workspaceID must fail closed at the boundary")
		})
	}
}

// TestIsOwnerFailsClosed pins the exported predicate's fail-closes directly.
// IsOwner is advertised as the one owner-only rule every access check routes
// through, so a nil workspace (a store path that returned (nil, nil), or a batch
// entry that failed to load) must be a deny rather than a nil-pointer panic on
// the OwnerUserID deref, and a zero UserID must never match a real owner id.
func TestIsOwnerFailsClosed(t *testing.T) {
	ws := &store.Workspace{ID: "ws1", OwnerUserID: "owner-1"}
	assert.True(t, auth.IsOwner(ws, userid.MustNew("owner-1")), "the owner matches")
	assert.False(t, auth.IsOwner(ws, userid.MustNew("someone-else")), "a non-owner is denied")
	assert.False(t, auth.IsOwner(ws, userid.UserID{}), "a zero UserID never matches")
	assert.False(t, auth.IsOwner(nil, userid.MustNew("owner-1")), "a nil workspace is a deny, not a panic")
	assert.False(t, auth.IsOwner(nil, userid.UserID{}), "nil workspace + zero UserID is a deny")
}

// A blank-id user row must be REFUSED at login, not panicked on.
//
// lockedUser.ID is a column, and MustNew's contract ("the caller already knows
// this is non-empty") holds for a literal but never for stored data. A panic
// here fires inside RunInUserAuthTransaction on the credential path, so the
// client sees a torn connection on every retry instead of the same clean
// Unauthenticated every other bad-credential case returns -- and the
// transaction unwinds through a panic rather than a rollback. SQLite accepts ""
// as a TEXT primary key, so the row below inserts cleanly.
func TestLogin_BlankUserIDRowIsRefusedNotPanicked(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	orgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "blank-id-org"}))
	hash, err := password.Hash("password123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: "", OrgID: orgID, Username: "blankid",
		PasswordHash: hash, DisplayName: "Blank", PasswordSet: true,
	}))

	// The password is CORRECT, so this reaches the session mint -- the deny
	// must come from the blank id, not from a failed credential check.
	require.NotPanics(t, func() {
		token, user, _, loginErr := auth.Login(ctx, st, "blankid", "password123")
		assert.Error(t, loginErr, "a user row that names no user must not authenticate")
		assert.Empty(t, token, "no session token may be issued")
		assert.Nil(t, user)
	})

	// And no session row was written for the blank id, which would otherwise
	// authenticate as blank on every later request.
	//
	// Counted through ListAllActive, NOT ListByUserID: the latter routes the
	// caller id through store.OwnerFilter and short-circuits to an empty page
	// for a zero UserID BEFORE touching SQL, so asserting on it passes whether
	// or not the row exists. That is the assertion this replaced -- deleting
	// Login's mint guard left it green, which is exactly the fail-open it was
	// written to catch.
	all, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
		PageParams: store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	assert.Empty(t, all.Rows, "a refused login must leave no session behind")
}
