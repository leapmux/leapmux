package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
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

// A workspace_access grant is sufficient for read access on its own: org
// membership is NOT required, so a workspace can be shared with an external
// collaborator (cross-org share) and grant-based access survives if the
// grantee is later removed from the org.
func TestWorkspaceCanReadAllowsGrantWithoutOrgMembership(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID := id.Generate()
	otherOrgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "workspace-org"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: otherOrgID, Name: "other-org"}))
	ownerID := id.Generate()
	memberID := id.Generate()
	outsiderID := id.Generate()
	for _, user := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgID, Username: "owner", PasswordHash: "hash", DisplayName: "Owner", PasswordSet: true},
		{ID: memberID, OrgID: orgID, Username: "member", PasswordHash: "hash", DisplayName: "Member", PasswordSet: true},
		{ID: outsiderID, OrgID: otherOrgID, Username: "outsider", PasswordHash: "hash", DisplayName: "Outsider", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, user))
	}
	require.NoError(t, st.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
		OrgID: orgID, UserID: memberID, Role: leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}))
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: workspaceID, OrgID: orgID, OwnerUserID: ownerID, Title: "shared",
	}))

	// An external collaborator who was never an org member can read once granted.
	require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
		WorkspaceID: workspaceID, UserID: outsiderID,
	}))
	allowed, err := auth.WorkspaceCanRead(ctx, st, workspaceID, outsiderID)
	require.NoError(t, err)
	assert.True(t, allowed, "a non-member holding a grant must be able to read (cross-org share)")

	// A member's grant survives removal from the org.
	require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
		WorkspaceID: workspaceID, UserID: memberID,
	}))
	allowed, err = auth.WorkspaceCanRead(ctx, st, workspaceID, memberID)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, st.OrgMembers().Delete(ctx, store.DeleteOrgMemberParams{OrgID: orgID, UserID: memberID}))
	allowed, err = auth.WorkspaceCanRead(ctx, st, workspaceID, memberID)
	require.NoError(t, err)
	assert.True(t, allowed, "grant-based read access does not depend on org membership")

	// A user with neither ownership nor a grant is still denied.
	strangerID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: strangerID, OrgID: orgID, Username: "stranger", PasswordHash: "hash", DisplayName: "Stranger", PasswordSet: true,
	}))
	allowed, err = auth.WorkspaceCanRead(ctx, st, workspaceID, strangerID)
	require.NoError(t, err)
	assert.False(t, allowed, "a user without ownership or a grant must be denied")
}

// WorkspaceReadableByUsersInOrg is the batch counterpart of WorkspaceCanReadInOrg
// used by the CRDT subscriber-expansion fan-out. It must agree with the per-user
// check for every user, fail closed on a wrong org, and deny an unknown workspace.
func TestWorkspaceReadableByUsersInOrg(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID := id.Generate()
	otherOrgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "org"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: otherOrgID, Name: "other"}))
	ownerID := id.Generate()
	granteeID := id.Generate()
	strangerID := id.Generate()
	for _, user := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgID, Username: "owner", PasswordHash: "hash", DisplayName: "Owner", PasswordSet: true},
		{ID: granteeID, OrgID: otherOrgID, Username: "grantee", PasswordHash: "hash", DisplayName: "Grantee", PasswordSet: true},
		{ID: strangerID, OrgID: orgID, Username: "stranger", PasswordHash: "hash", DisplayName: "Stranger", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, user))
	}
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: workspaceID, OrgID: orgID, OwnerUserID: ownerID, Title: "ws",
	}))
	require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
		WorkspaceID: workspaceID, UserID: granteeID,
	}))

	users := []string{ownerID, granteeID, strangerID}
	readable, err := auth.WorkspaceReadableByUsersInOrg(ctx, st, orgID, workspaceID, users)
	require.NoError(t, err)
	assert.True(t, readable[ownerID], "owner reads")
	assert.True(t, readable[granteeID], "cross-org grantee reads")
	assert.False(t, readable[strangerID], "stranger without grant is denied")

	// The batch verdict must match the per-user check for every user.
	for _, userID := range users {
		single, err := auth.WorkspaceCanReadInOrg(ctx, st, orgID, workspaceID, userID)
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

// WorkspacesReadableByUser is the many-workspaces/single-user read resolver. It
// must honor owner-or-grant, apply the org binding when orgID is set, SKIP that
// binding when orgID is empty (the delegation contract), drop unknown IDs, and
// dedup the request.
func TestWorkspacesReadableByUser(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgA := id.Generate()
	orgB := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgA, Name: "a"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgB, Name: "b"}))
	ownerID := id.Generate()
	granteeID := id.Generate()
	outsiderID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgA, Username: "owner", PasswordHash: "h", DisplayName: "O", PasswordSet: true},
		{ID: granteeID, OrgID: orgA, Username: "grantee", PasswordHash: "h", DisplayName: "G", PasswordSet: true},
		{ID: outsiderID, OrgID: orgA, Username: "outsider", PasswordHash: "h", DisplayName: "X", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	// owner owns one workspace in each org; a third (also owned by owner) in orgA
	// is shared with the grantee.
	wsOwnA := id.Generate()
	wsOwnB := id.Generate()
	wsShared := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwnA, OrgID: orgA, OwnerUserID: ownerID, Title: "own-a"}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwnB, OrgID: orgB, OwnerUserID: ownerID, Title: "own-b"}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsShared, OrgID: orgA, OwnerUserID: ownerID, Title: "shared"}))
	require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{WorkspaceID: wsShared, UserID: granteeID}))

	unknown := id.Generate()
	requested := []string{wsOwnA, wsOwnB, wsShared, unknown}

	// orgA binding: owner sees its two orgA workspaces; the cross-org wsOwnB is
	// excluded and the unknown ID is dropped. Input order is preserved.
	inA, err := auth.WorkspacesReadableByUser(ctx, st, orgA, ownerID, requested)
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwnA, wsShared}, inA, "org binding excludes the cross-org workspace and unknown IDs")

	// Empty orgID skips the org binding (delegation contract): the owner now sees
	// all three owned workspaces regardless of org.
	crossOrg, err := auth.WorkspacesReadableByUser(ctx, st, "", ownerID, requested)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{wsOwnA, wsOwnB, wsShared}, crossOrg, "empty orgID resolves owner-or-grant across orgs")

	// Grant path: the grantee reads only the shared workspace, not the owner's
	// private ones.
	grantee, err := auth.WorkspacesReadableByUser(ctx, st, orgA, granteeID, requested)
	require.NoError(t, err)
	assert.Equal(t, []string{wsShared}, grantee, "explicit grant is honored; non-granted workspaces are not")

	// An outsider with neither ownership nor a grant reads nothing.
	outsider, err := auth.WorkspacesReadableByUser(ctx, st, orgA, outsiderID, requested)
	require.NoError(t, err)
	assert.Empty(t, outsider, "no ownership and no grant means no read access")

	// Duplicate requested IDs collapse to one.
	dedup, err := auth.WorkspacesReadableByUser(ctx, st, orgA, ownerID, []string{wsOwnA, wsOwnA, wsOwnA})
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwnA}, dedup, "duplicate requested IDs collapse")

	// Empty inputs short-circuit.
	none, err := auth.WorkspacesReadableByUser(ctx, st, orgA, ownerID, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// WorkspacesReadableByUser documents that the readable subset is returned in the
// input order. A user who both OWNS one workspace and holds a GRANT on another
// must get them back in the requested order, not owners-first (which the earlier
// two-pass implementation produced by appending owned IDs before granted ones).
func TestWorkspacesReadableByUserPreservesInputOrderAcrossOwnerAndGrant(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	org := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: org, Name: "o"}))
	userID := id.Generate()
	ownerID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: userID, OrgID: org, Username: "user", PasswordHash: "h", DisplayName: "U", PasswordSet: true},
		{ID: ownerID, OrgID: org, Username: "owner", PasswordHash: "h", DisplayName: "O", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	owned := id.Generate()
	granted := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: owned, OrgID: org, OwnerUserID: userID, Title: "owned"}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: granted, OrgID: org, OwnerUserID: ownerID, Title: "granted"}))
	require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{WorkspaceID: granted, UserID: userID}))

	// Request the GRANTED workspace before the OWNED one. The result must follow
	// the request order; the previous implementation emitted all owned IDs first,
	// so it would have returned [owned, granted] here.
	got, err := auth.WorkspacesReadableByUser(ctx, st, org, userID, []string{granted, owned})
	require.NoError(t, err)
	assert.Equal(t, []string{granted, owned}, got, "readable subset must preserve input order across a grant/owner mix")
}

// WorkspaceCanWriteInOrg is owner-only (a read grant does NOT confer write) and
// shares the org cross-check + missing/out-of-org deny prologue with the read
// path (loadWorkspaceInOrg). Guards the refactor that collapsed the CRDT
// service's re-implemented prologue onto this canonical predicate.
func TestWorkspaceCanWriteInOrg(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgA := id.Generate()
	orgB := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgA, Name: "a"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgB, Name: "b"}))
	ownerID := id.Generate()
	granteeID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgA, Username: "owner", PasswordHash: "h", DisplayName: "O", PasswordSet: true},
		{ID: granteeID, OrgID: orgA, Username: "grantee", PasswordHash: "h", DisplayName: "G", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	ws := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: ws, OrgID: orgA, OwnerUserID: ownerID, Title: "ws"}))
	require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{WorkspaceID: ws, UserID: granteeID}))

	// Owner may write.
	can, err := auth.WorkspaceCanWriteInOrg(ctx, st, orgA, ws, ownerID)
	require.NoError(t, err)
	assert.True(t, can, "owner may write")

	// A read-only grantee may NOT write (shared-write is not implemented).
	can, err = auth.WorkspaceCanWriteInOrg(ctx, st, orgA, ws, granteeID)
	require.NoError(t, err)
	assert.False(t, can, "an explicit read grant does not confer write access")

	// Org cross-check: the owner cannot write the workspace bound to another org.
	can, err = auth.WorkspaceCanWriteInOrg(ctx, st, orgB, ws, ownerID)
	require.NoError(t, err)
	assert.False(t, can, "org binding excludes a workspace homed in another org")

	// Missing workspace and empty inputs fail closed (a deny, not an error).
	can, err = auth.WorkspaceCanWriteInOrg(ctx, st, orgA, id.Generate(), ownerID)
	require.NoError(t, err)
	assert.False(t, can, "a missing workspace is a deny, not an error")
	can, err = auth.WorkspaceCanWriteInOrg(ctx, st, "", ws, ownerID)
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
	sessions, listErr := st.Sessions().ListByUserID(ctx, userID)
	require.NoError(t, listErr)
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
	sessions, listErr := st.Sessions().ListByUserID(ctx, userID)
	require.NoError(t, listErr)
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

	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
	resolved, err := auth.ResolveOrgID(context.Background(), st, user, "")
	require.NoError(t, err)
	assert.Equal(t, orgID, resolved)
}

func TestResolveOrgID_MemberReturnsOrgID(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID, userID := createTestUser(t, st)

	_ = st.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   1,
	})

	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
	resolved, err := auth.ResolveOrgID(ctx, st, user, orgID)
	require.NoError(t, err)
	assert.Equal(t, orgID, resolved)
}

func TestResolveOrgID_NonMemberReturnsNotFound(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID, userID := createTestUser(t, st)

	otherOrgID := id.Generate()
	_ = st.Orgs().Create(ctx, store.CreateOrgParams{ID: otherOrgID, Name: "other-org"})

	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
	_, err := auth.ResolveOrgID(ctx, st, user, otherOrgID)
	require.Error(t, err)

	connectErr, ok := err.(*connect.Error)
	require.True(t, ok, "expected *connect.Error")
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

// erroringOrgMemberStore wraps a store so OrgMembers().IsMember returns a
// transient error, exercising ResolveOrgID's store-error path.
type erroringOrgMemberStore struct {
	store.Store
	err error
}

func (s erroringOrgMemberStore) OrgMembers() store.OrgMemberStore {
	return erroringOrgMembers{OrgMemberStore: s.Store.OrgMembers(), err: s.err}
}

type erroringOrgMembers struct {
	store.OrgMemberStore
	err error
}

func (e erroringOrgMembers) IsMember(context.Context, store.IsOrgMemberParams) (bool, error) {
	return false, e.err
}

// TestResolveOrgID_TransientStoreErrorReturnsInternal verifies a transient
// membership-check failure surfaces as a retryable CodeInternal rather than an
// uncoded error the caller relays as CodeUnknown -- a client treats Unknown as a
// permanent failure instead of retrying.
func TestResolveOrgID_TransientStoreErrorReturnsInternal(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID, userID := createTestUser(t, st)

	otherOrgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: otherOrgID, Name: "other-org"}))

	boom := errors.New("db unavailable")
	hooked := erroringOrgMemberStore{Store: st, err: boom}
	user := &auth.UserInfo{ID: userID, OrgID: orgID, Username: "testuser"}
	_, err := auth.ResolveOrgID(ctx, hooked, user, otherOrgID)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err),
		"transient membership-check failure must be retryable Internal, not Unknown")
	assert.ErrorIs(t, err, boom)
}

// WorkspaceCanReadInOrg is WorkspaceCanRead bound to a concrete org: it grants
// owner-or-grant read only when the workspace lives in the given org and is not
// soft-deleted, so a stale CRDT subscriber can't see a workspace re-homed to
// another org (or a deleted one). It shares LoadedWorkspaceCanRead with the RPC
// read paths, so the owner-or-grant rule has a single source of truth.
func TestWorkspaceCanReadInOrgEnforcesOrgBindingAndDeletion(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID := id.Generate()
	otherOrgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "canread-org"}))
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: otherOrgID, Name: "canread-other-org"}))
	ownerID := id.Generate()
	granteeID := id.Generate()
	strangerID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: ownerID, OrgID: orgID, Username: "cr-owner", PasswordHash: "hash", DisplayName: "Owner", PasswordSet: true},
		{ID: granteeID, OrgID: otherOrgID, Username: "cr-grantee", PasswordHash: "hash", DisplayName: "Grantee", PasswordSet: true},
		{ID: strangerID, OrgID: orgID, Username: "cr-stranger", PasswordHash: "hash", DisplayName: "Stranger", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	wsID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsID, OrgID: orgID, OwnerUserID: ownerID, Title: "readable"}))
	require.NoError(t, st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{WorkspaceID: wsID, UserID: granteeID}))

	assertRead := func(checkOrgID, userID string, want bool, msg string) {
		t.Helper()
		got, err := auth.WorkspaceCanReadInOrg(ctx, st, checkOrgID, wsID, userID)
		require.NoError(t, err)
		assert.Equal(t, want, got, msg)
	}
	assertRead(orgID, ownerID, true, "owner reads in the workspace's org")
	assertRead(orgID, granteeID, true, "a cross-org grantee reads in the workspace's org")
	assertRead(orgID, strangerID, false, "no ownership or grant is denied")
	assertRead(otherOrgID, ownerID, false, "the org binding rejects a mismatched org")
	assertRead("", ownerID, false, "an empty org fails closed")

	missing, err := auth.WorkspaceCanReadInOrg(ctx, st, orgID, "missing-workspace", ownerID)
	require.NoError(t, err)
	assert.False(t, missing, "a missing workspace is denied")

	// A soft-deleted workspace is unreadable even by its owner.
	_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: ownerID})
	require.NoError(t, err)
	assertRead(orgID, ownerID, false, "a soft-deleted workspace is unreadable")
}
