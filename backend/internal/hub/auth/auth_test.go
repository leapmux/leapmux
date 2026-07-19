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
)

func setupStore(t *testing.T) store.Store {
	return hubtestutil.OpenTestStore(t)
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
		ID: workspaceID, OrgID: orgID, OwnerUserID: ownerID, Title: "mine",
	}))

	allowed, err := auth.WorkspaceCanRead(ctx, st, workspaceID, ownerID)
	require.NoError(t, err)
	assert.True(t, allowed, "the owner reads their own workspace")

	allowed, err = auth.WorkspaceCanRead(ctx, st, workspaceID, sameOrgID)
	require.NoError(t, err)
	assert.False(t, allowed, "a same-org non-owner is denied")

	allowed, err = auth.WorkspaceCanRead(ctx, st, workspaceID, outsiderID)
	require.NoError(t, err)
	assert.False(t, allowed, "a user from another org is denied")

	allowed, err = auth.WorkspaceCanRead(ctx, st, "missing-workspace", ownerID)
	require.NoError(t, err)
	assert.False(t, allowed, "a missing workspace is a deny, not an error")

	// Empty inputs fail closed without a store round-trip.
	allowed, err = auth.WorkspaceCanRead(ctx, st, "", ownerID)
	require.NoError(t, err)
	assert.False(t, allowed, "an empty workspace id fails closed")
	allowed, err = auth.WorkspaceCanRead(ctx, st, workspaceID, "")
	require.NoError(t, err)
	assert.False(t, allowed, "an empty user id fails closed")
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
		ID: workspaceID, OrgID: orgID, OwnerUserID: ownerID, Title: "ws",
	}))

	// The empty user id entry pins the fail-closed guard: it must never be
	// marked readable, even though it can't match a real owner.
	users := []string{ownerID, strangerID, ""}
	readable, err := auth.WorkspaceReadableByUsersInOrg(ctx, st, orgID, workspaceID, users)
	require.NoError(t, err)
	assert.True(t, readable[ownerID], "owner reads")
	assert.False(t, readable[strangerID], "a non-owner is denied")
	assert.False(t, readable[""], "an empty user id is never readable")

	// The batch verdict must match the per-user check for every user.
	for _, userID := range users {
		single, err := auth.WorkspaceCanAccessInOrg(ctx, st, orgID, workspaceID, userID)
		require.NoError(t, err)
		assert.Equal(t, single, readable[userID], "batch must agree with per-user check for %s", userID)
	}

	// A wrong org fails closed (deny all), and an unknown workspace denies all.
	wrongOrg, err := auth.WorkspaceReadableByUsersInOrg(ctx, st, otherOrgID, workspaceID, users)
	require.NoError(t, err)
	assert.Empty(t, wrongOrg, "org cross-check must deny every user when orgID mismatches")
	missing, err := auth.WorkspaceReadableByUsersInOrg(ctx, st, orgID, id.Generate(), users)
	require.NoError(t, err)
	assert.Empty(t, missing, "an unknown workspace denies every user")

	// Empty inputs short-circuit to an empty (non-nil) result.
	empty, err := auth.WorkspaceReadableByUsersInOrg(ctx, st, orgID, workspaceID, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// WorkspacesReadableByUser is the many-workspaces/single-user read resolver.
// It must honor owner-only access, apply the org binding when orgID is set,
// SKIP that binding when orgID is empty (the delegation contract), drop
// unknown IDs, dedup the request, and preserve input order.
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
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwnA1, OrgID: orgA, OwnerUserID: ownerID, Title: "own-a1"}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwnA2, OrgID: orgA, OwnerUserID: ownerID, Title: "own-a2"}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwnB, OrgID: orgB, OwnerUserID: ownerID, Title: "own-b"}))

	unknown := id.Generate()
	// Request order deliberately interleaves the cross-org and unknown IDs.
	requested := []string{wsOwnA2, wsOwnB, wsOwnA1, unknown}

	// orgA binding: owner sees its two orgA workspaces in request order; the
	// cross-org wsOwnB is excluded and the unknown ID is dropped.
	inA, err := auth.WorkspacesReadableByUser(ctx, st, orgA, ownerID, requested)
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwnA2, wsOwnA1}, inA, "org binding excludes the cross-org workspace and unknown IDs, preserving input order")

	// Empty orgID skips the org binding (delegation contract): the owner now
	// sees all three owned workspaces regardless of org, in request order.
	crossOrg, err := auth.WorkspacesReadableByUser(ctx, st, "", ownerID, requested)
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwnA2, wsOwnB, wsOwnA1}, crossOrg, "empty orgID resolves ownership across orgs")

	// A non-owner reads nothing.
	outsider, err := auth.WorkspacesReadableByUser(ctx, st, orgA, outsiderID, requested)
	require.NoError(t, err)
	assert.Empty(t, outsider, "no ownership means no read access")

	// Duplicate requested IDs collapse to one.
	dedup, err := auth.WorkspacesReadableByUser(ctx, st, orgA, ownerID, []string{wsOwnA1, wsOwnA1, wsOwnA1})
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwnA1}, dedup, "duplicate requested IDs collapse")

	// Empty inputs short-circuit.
	none, err := auth.WorkspacesReadableByUser(ctx, st, orgA, ownerID, nil)
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
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: ws, OrgID: orgA, OwnerUserID: ownerID, Title: "ws"}))

	// Owner may access.
	can, err := auth.WorkspaceCanAccessInOrg(ctx, st, orgA, ws, ownerID)
	require.NoError(t, err)
	assert.True(t, can, "owner may access")

	// A non-owner may not.
	can, err = auth.WorkspaceCanAccessInOrg(ctx, st, orgA, ws, otherID)
	require.NoError(t, err)
	assert.False(t, can, "a non-owner is denied")

	// Org cross-check: the owner cannot access the workspace bound to another org.
	can, err = auth.WorkspaceCanAccessInOrg(ctx, st, orgB, ws, ownerID)
	require.NoError(t, err)
	assert.False(t, can, "org binding excludes a workspace homed in another org")

	// Missing workspace and empty inputs fail closed (a deny, not an error).
	can, err = auth.WorkspaceCanAccessInOrg(ctx, st, orgA, id.Generate(), ownerID)
	require.NoError(t, err)
	assert.False(t, can, "a missing workspace is a deny, not an error")
	can, err = auth.WorkspaceCanAccessInOrg(ctx, st, "", ws, ownerID)
	require.NoError(t, err)
	assert.False(t, can, "empty orgID fails closed")
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

func (s *beforeTransactionStore) RunInUserAuthTransaction(ctx context.Context, userID string, fn func(tx store.Store) error) error {
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

		sessionID, _, err := auth.CreateSession(ctx, st, userID)
		require.NoError(t, err)
		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			_, _, err := auth.RevokeAllUserCredentials(ctx, tx, userID)
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
			_, _, err := auth.RevokeAllUserCredentials(ctx, tx, userID)
			return err
		}))
		sessionID, _, err := auth.CreateSession(ctx, st, userID)
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
		UserID:     userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: []byte("hash"),
		Scope:      "remote:*",
	}))
	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userID,
		PublicKey:       []byte("x25519"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: workspaceID, OrgID: orgID, OwnerUserID: userID, Title: "test",
	}))
	tabID := id.Generate()
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		OrgID: orgID, WorkspaceID: workspaceID, WorkerID: workerID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabID,
		Position: "a", TileID: "tile-1",
	}))
	require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
		ID: id.Generate(), UserID: userID, WorkerID: workerID,
		WorkspaceID: workspaceID, IssuedForTabID: tabID,
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       []byte("hash"), ExpiresAt: time.Now().Add(time.Hour),
	}))

	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		apiCount, delegationCount, err := auth.RevokeAllUserCredentials(ctx, tx, userID)
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

func TestContextUserRoundtrip(t *testing.T) {
	info := &auth.UserInfo{
		ID:       "user-1",
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

	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
	resolved, err := auth.ResolveOrgID(user, "")
	require.NoError(t, err)
	assert.Equal(t, orgID, resolved)
}

func TestResolveOrgID_OwnOrgReturnsOrgID(t *testing.T) {
	st := setupStore(t)
	orgID, userID := createTestUser(t, st)
	_ = st

	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
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

	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
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
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsID, OrgID: orgID, OwnerUserID: ownerID, Title: "readable"}))

	assertRead := func(checkOrgID, userID string, want bool, msg string) {
		t.Helper()
		got, err := auth.WorkspaceCanAccessInOrg(ctx, st, checkOrgID, wsID, userID)
		require.NoError(t, err)
		assert.Equal(t, want, got, msg)
	}
	assertRead(orgID, ownerID, true, "owner reads in the workspace's org")
	assertRead(orgID, strangerID, false, "a non-owner is denied")
	assertRead(otherOrgID, ownerID, false, "the org binding rejects a mismatched org")
	assertRead("", ownerID, false, "an empty org fails closed")

	missing, err := auth.WorkspaceCanAccessInOrg(ctx, st, orgID, "missing-workspace", ownerID)
	require.NoError(t, err)
	assert.False(t, missing, "a missing workspace is denied")

	// A soft-deleted workspace is unreadable even by its owner.
	_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: ownerID})
	require.NoError(t, err)
	assertRead(orgID, ownerID, false, "a soft-deleted workspace is unreadable")
}

// WorkspaceCanRead must fail closed on an empty workspaceID at its OWN boundary
// -- not one helper deeper in loadWorkspace -- so a future refactor that swaps
// loadWorkspace for a lookup without the empty-id guard cannot let an empty id
// reach IsOwner (which would then answer against whatever workspace row a cache
// or bulk path handed back). The empty userID fail-close is the same shape;
// both are mechanical here rather than dependent on a helper keeping its guard.
func TestWorkspaceCanReadFailsClosedOnEmptyIDs(t *testing.T) {
	st := setupStore(t)
	for _, tc := range []struct {
		name        string
		userID      string
		workspaceID string
	}{
		{"empty workspace id", "real-user", ""},
		{"empty user id", "", "real-ws"},
		{"both empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := auth.WorkspaceCanRead(context.Background(), st, tc.workspaceID, tc.userID)
			assert.NoError(t, err)
			assert.False(t, ok, "an empty userID or workspaceID must fail closed at the boundary")
		})
	}
}

// TestIsOwnerFailsClosed pins the exported predicate's fail-closes directly.
// IsOwner is advertised as the one owner-only rule every access check routes
// through, so a nil workspace (a store path that returned (nil, nil), or a batch
// entry that failed to load) must be a deny rather than a nil-pointer panic on
// the OwnerUserID deref, and an empty userID must never match a real owner id.
func TestIsOwnerFailsClosed(t *testing.T) {
	ws := &store.Workspace{ID: "ws1", OwnerUserID: "owner-1"}
	assert.True(t, auth.IsOwner(ws, "owner-1"), "the owner matches")
	assert.False(t, auth.IsOwner(ws, "someone-else"), "a non-owner is denied")
	assert.False(t, auth.IsOwner(ws, ""), "an empty userID never matches")
	assert.False(t, auth.IsOwner(nil, "owner-1"), "a nil workspace is a deny, not a panic")
	assert.False(t, auth.IsOwner(nil, ""), "nil workspace + empty userID is a deny")
}
