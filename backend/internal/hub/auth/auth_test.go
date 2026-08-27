package auth_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
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

func createTestUser(t *testing.T, st store.Store) string {
	t.Helper()
	ctx := context.Background()

	hash, err := password.Hash("password123")
	require.NoError(t, err)

	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:           userID,
		Username:     "testuser",
		PasswordHash: hash,
		DisplayName:  "Test User",
		PasswordSet:  true,
		IsAdmin:      true,
	}))

	return userID
}

func TestLogin_Success(t *testing.T) {
	st := setupStore(t)
	userID := createTestUser(t, st)
	ctx := context.Background()

	token, user, _, err := auth.Login(ctx, st, "testuser", "password123", auth.DefaultSessionDuration)
	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.Equal(t, userID, user.ID)
}

// Login must pass its lifetime through to the session it creates. It threads the
// value across a transaction boundary, where dropping it would leave every login
// on the default and no caller any the wiser.
func TestLogin_StampsTheGivenLifetime(t *testing.T) {
	st := setupStore(t)
	createTestUser(t, st)
	ctx := context.Background()

	const lifetime = 90 * time.Minute
	before := time.Now()
	token, _, expiresAt, err := auth.Login(ctx, st, "testuser", "password123", lifetime)
	require.NoError(t, err)
	hubtestutil.AssertSessionLifetime(t, before, lifetime, expiresAt)

	sess, err := st.Sessions().GetByID(ctx, token, time.Now().UTC())
	require.NoError(t, err)
	assert.WithinDuration(t, expiresAt, sess.ExpiresAt, time.Second,
		"the returned expiry and the stored row must be the same deadline")
}

// CreateSession states that a lifetime of zero or less falls back to the
// default. The fallback is what keeps a caller that has no configured value --
// a test, a tool, a path that forgets the argument -- from writing a session
// that expired before the response left the Hub.
func TestCreateSession_NonPositiveLifetimeFallsBackToDefault(t *testing.T) {
	st := setupStore(t)
	userID := userid.MustNew(createTestUser(t, st))
	ctx := context.Background()

	for name, lifetime := range map[string]time.Duration{
		"zero":     0,
		"negative": -time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			before := time.Now()
			_, expiresAt, err := auth.CreateSession(ctx, st, userID, lifetime)
			require.NoError(t, err)
			hubtestutil.AssertSessionLifetime(t, before, auth.DefaultSessionDuration, expiresAt)
		})
	}

	// Control: a real lifetime is used as given, so the cases above prove the
	// fallback rather than that CreateSession ignores the argument entirely.
	t.Run("positive is honoured", func(t *testing.T) {
		before := time.Now()
		_, expiresAt, err := auth.CreateSession(ctx, st, userID, time.Hour)
		require.NoError(t, err)
		hubtestutil.AssertSessionLifetime(t, before, time.Hour, expiresAt)
	})
}

// Workspace access is owner-only: no other user may read someone else's workspace.
func TestWorkspaceCanAccessIsOwnerOnly(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	ownerID := id.Generate()
	otherID := id.Generate()
	for _, user := range []store.CreateUserParams{
		{ID: ownerID, Username: "owner", PasswordHash: "hash", DisplayName: "Owner", PasswordSet: true},
		{ID: otherID, Username: "other", PasswordHash: "hash", DisplayName: "Other", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, user))
	}
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: workspaceID, OwnerUserID: userid.MustNew(ownerID), Title: "mine",
	}))

	allowed, err := auth.WorkspaceCanAccess(ctx, st, workspaceID, userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.True(t, allowed, "the owner reads their own workspace")

	allowed, err = auth.WorkspaceCanAccess(ctx, st, workspaceID, userid.MustNew(otherID))
	require.NoError(t, err)
	assert.False(t, allowed, "a non-owner is denied")

	allowed, err = auth.WorkspaceCanAccess(ctx, st, "missing-workspace", userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.False(t, allowed, "a missing workspace is a deny, not an error")

	// Empty inputs fail closed without a store round-trip.
	allowed, err = auth.WorkspaceCanAccess(ctx, st, "", userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.False(t, allowed, "an empty workspace id fails closed")
	allowed, err = auth.WorkspaceCanAccess(ctx, st, workspaceID, userid.UserID{})
	require.NoError(t, err)
	assert.False(t, allowed, "a zero user id fails closed")
}

// WorkspaceReadableByUsers is the batch counterpart of WorkspaceCanAccess
// used by the CRDT subscriber-expansion fan-out. It must agree with the
// per-user check for every user and deny an unknown workspace.
func TestWorkspaceReadableByUsers(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	ownerID := id.Generate()
	strangerID := id.Generate()
	for _, user := range []store.CreateUserParams{
		{ID: ownerID, Username: "owner", PasswordHash: "hash", DisplayName: "Owner", PasswordSet: true},
		{ID: strangerID, Username: "stranger", PasswordHash: "hash", DisplayName: "Stranger", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, user))
	}
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: workspaceID, OwnerUserID: userid.MustNew(ownerID), Title: "ws",
	}))

	// The zero UserID entry pins the fail-closed guard: it must never be
	// marked readable (and its String() key is ""), even though it can't
	// match a real owner.
	users := []userid.UserID{userid.MustNew(ownerID), userid.MustNew(strangerID), {}}
	readable, err := auth.WorkspaceReadableByUsers(ctx, st, workspaceID, users)
	require.NoError(t, err)
	assert.True(t, readable[ownerID], "owner reads")
	assert.False(t, readable[strangerID], "a non-owner is denied")
	assert.False(t, readable[""], "a zero user id is never readable")

	// The batch verdict must match the per-user check for every user.
	for _, uid := range users {
		single, err := auth.WorkspaceCanAccess(ctx, st, workspaceID, uid)
		require.NoError(t, err)
		assert.Equal(t, single, readable[uid.String()], "batch must agree with per-user check for %s", uid)
	}

	missing, err := auth.WorkspaceReadableByUsers(ctx, st, id.Generate(), users)
	require.NoError(t, err)
	assert.Empty(t, missing, "an unknown workspace denies every user")

	// Empty inputs short-circuit to an empty (non-nil) result.
	empty, err := auth.WorkspaceReadableByUsers(ctx, st, workspaceID, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// WorkspacesReadableByUser is the many-workspaces/single-user read resolver.
// It must honor owner-only access, drop unknown IDs, dedup the request, and
// preserve input order.
func TestWorkspacesReadableByUser(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	ownerID := id.Generate()
	outsiderID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: ownerID, Username: "owner", PasswordHash: "h", DisplayName: "O", PasswordSet: true},
		{ID: outsiderID, Username: "outsider", PasswordHash: "h", DisplayName: "X", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	wsOwn1 := id.Generate()
	wsOwn2 := id.Generate()
	wsOwn3 := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwn1, OwnerUserID: userid.MustNew(ownerID), Title: "own-1"}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwn2, OwnerUserID: userid.MustNew(ownerID), Title: "own-2"}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsOwn3, OwnerUserID: userid.MustNew(ownerID), Title: "own-3"}))

	unknown := id.Generate()
	requested := []string{wsOwn2, wsOwn3, wsOwn1, unknown}
	owner := userid.MustNew(ownerID)
	outsider := userid.MustNew(outsiderID)

	got, err := auth.WorkspacesReadableByUser(ctx, st, owner, requested)
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwn2, wsOwn3, wsOwn1}, got, "owner sees owned workspaces in request order; unknown IDs drop")

	outsiderGot, err := auth.WorkspacesReadableByUser(ctx, st, outsider, requested)
	require.NoError(t, err)
	assert.Empty(t, outsiderGot, "no ownership means no read access")

	zeroUser, err := auth.WorkspacesReadableByUser(ctx, st, userid.UserID{}, requested)
	require.NoError(t, err)
	assert.Empty(t, zeroUser, "zero UserID fails closed")

	dedup, err := auth.WorkspacesReadableByUser(ctx, st, owner, []string{wsOwn1, wsOwn1, wsOwn1})
	require.NoError(t, err)
	assert.Equal(t, []string{wsOwn1}, dedup, "duplicate requested IDs collapse")

	none, err := auth.WorkspacesReadableByUser(ctx, st, owner, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// WorkspaceCanAccess is the single owner-only predicate behind both the
// CRDT read and write gates.
func TestWorkspaceCanAccess(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	ownerID := id.Generate()
	otherID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: ownerID, Username: "owner", PasswordHash: "h", DisplayName: "O", PasswordSet: true},
		{ID: otherID, Username: "other", PasswordHash: "h", DisplayName: "G", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	ws := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: ws, OwnerUserID: userid.MustNew(ownerID), Title: "ws"}))
	owner := userid.MustNew(ownerID)
	other := userid.MustNew(otherID)

	can, err := auth.WorkspaceCanAccess(ctx, st, ws, owner)
	require.NoError(t, err)
	assert.True(t, can, "owner may access")

	can, err = auth.WorkspaceCanAccess(ctx, st, ws, other)
	require.NoError(t, err)
	assert.False(t, can, "a non-owner is denied")

	can, err = auth.WorkspaceCanAccess(ctx, st, id.Generate(), owner)
	require.NoError(t, err)
	assert.False(t, can, "a missing workspace is a deny, not an error")

	can, err = auth.WorkspaceCanAccess(ctx, st, ws, userid.UserID{})
	require.NoError(t, err)
	assert.False(t, can, "zero UserID fails closed")
}

func TestLogin_InvalidPassword(t *testing.T) {
	st := setupStore(t)
	createTestUser(t, st)
	ctx := context.Background()

	_, _, _, err := auth.Login(ctx, st, "testuser", "wrongpassword", auth.DefaultSessionDuration)
	require.Error(t, err)
}

func TestLogin_UnknownUser(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	_, _, _, err := auth.Login(ctx, st, "nonexistent", "password", auth.DefaultSessionDuration)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestLogin_PasswordNotSet_SameAsUnknownUser(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:           userID,
		Username:     "pkonly",
		PasswordHash: password.PlaceholderHash,
		DisplayName:  "PK Only",
		PasswordSet:  false,
		IsAdmin:      false,
	}))

	_, _, _, err := auth.Login(ctx, st, "pkonly", "any-password", auth.DefaultSessionDuration)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "invalid credentials")
}

func TestLogin_HashUnchangedAfterLogin(t *testing.T) {
	st := setupStore(t)
	createTestUser(t, st)
	ctx := context.Background()

	user, err := st.Users().GetByUsername(ctx, "testuser")
	require.NoError(t, err)
	originalHash := user.PasswordHash

	_, _, _, err = auth.Login(ctx, st, "testuser", "password123", auth.DefaultSessionDuration)
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
	userID := createTestUser(t, st)
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

	_, _, _, err = auth.Login(ctx, hooked, "testuser", "password123", auth.DefaultSessionDuration)
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
	userID := createTestUser(t, st)
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

	sessionID, user, _, err := auth.Login(ctx, hooked, "testuser", "new-password123", auth.DefaultSessionDuration)
	require.NoError(t, err)
	assert.NotEmpty(t, sessionID)
	assert.Equal(t, userID, user.ID)
	sessions := storetest.ListAllSessions(t, st, userID)
	require.Len(t, sessions, 1)
}

func TestCredentialCreationOrdersAgainstUserRevocation(t *testing.T) {
	t.Run("credential created before revocation is invalidated", func(t *testing.T) {
		st := setupStore(t)
		userID := createTestUser(t, st)
		ctx := context.Background()

		sessionID, _, err := auth.CreateSession(ctx, st, userid.MustNew(userID), auth.DefaultSessionDuration)
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
		userID := createTestUser(t, st)
		ctx := context.Background()

		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			_, _, err := auth.RevokeAllUserCredentials(ctx, tx, userid.MustNew(userID))
			return err
		}))
		sessionID, _, err := auth.CreateSession(ctx, st, userid.MustNew(userID), auth.DefaultSessionDuration)
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
	userID := createTestUser(t, st)
	ctx := context.Background()

	require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID:               id.Generate(),
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       []byte("hash"),
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
		ID: workspaceID, OwnerUserID: userid.MustNew(userID), Title: "test",
	}))
	tabID := id.Generate()
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		UserID: userid.MustNew(userID), WorkspaceID: workspaceID, WorkerID: workerID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabID,
		Position: "a", TileID: "tile-1",
	}))
	require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            id.Generate(), UserID: userid.MustNew(userID), WorkerID: workerID, IssuedForTabID: tabID,
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

	token, _, _, err := auth.Login(ctx, st, "testuser", "password123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	info, err := auth.ValidateToken(ctx, st, token)
	require.NoError(t, err)
	assert.Equal(t, "testuser", info.Username)
	assert.True(t, info.IsAdmin)

	session, err := st.Sessions().GetByID(ctx, token, time.Now().UTC())
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
		ID:       userid.MustNew("user-1"),
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

// WorkspaceCanAccess denies soft-deleted workspaces even to their owner.
func TestWorkspaceCanAccessEnforcesDeletion(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	ownerID := id.Generate()
	strangerID := id.Generate()
	for _, u := range []store.CreateUserParams{
		{ID: ownerID, Username: "cr-owner", PasswordHash: "hash", DisplayName: "Owner", PasswordSet: true},
		{ID: strangerID, Username: "cr-stranger", PasswordHash: "hash", DisplayName: "Stranger", PasswordSet: true},
	} {
		require.NoError(t, st.Users().Create(ctx, u))
	}
	wsID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{ID: wsID, OwnerUserID: userid.MustNew(ownerID), Title: "readable"}))

	got, err := auth.WorkspaceCanAccess(ctx, st, wsID, userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.True(t, got, "owner reads")

	got, err = auth.WorkspaceCanAccess(ctx, st, wsID, userid.MustNew(strangerID))
	require.NoError(t, err)
	assert.False(t, got, "a non-owner is denied")

	got, err = auth.WorkspaceCanAccess(ctx, st, wsID, userid.UserID{})
	require.NoError(t, err)
	assert.False(t, got, "zero UserID fails closed")

	missing, err := auth.WorkspaceCanAccess(ctx, st, "missing-workspace", userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.False(t, missing, "a missing workspace is denied")

	_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: userid.MustNew(ownerID)})
	require.NoError(t, err)
	got, err = auth.WorkspaceCanAccess(ctx, st, wsID, userid.MustNew(ownerID))
	require.NoError(t, err)
	assert.False(t, got, "a soft-deleted workspace is unreadable")
}

// WorkspaceCanAccess must fail closed on an empty workspaceID at its OWN boundary
// -- not one helper deeper in loadWorkspace -- so a future refactor that swaps
// loadWorkspace for a lookup without the empty-id guard cannot let an empty id
// reach IsOwner (which would then answer against whatever workspace row a cache
// or bulk path handed back). The zero UserID fail-close is the same shape;
// both are mechanical here rather than dependent on a helper keeping its guard.
func TestWorkspaceCanAccessFailsClosedOnEmptyIDs(t *testing.T) {
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
			ok, err := auth.WorkspaceCanAccess(context.Background(), st, tc.workspaceID, tc.userID)
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

// blankIDUserStore hands Login a users row whose id is blank.
//
// No store can produce that row any more: CreateUserParams.Validate refuses a
// blank id and every owner-keyed table REFERENCES users(id). But the guard the
// tests below pin is not a guard against the store -- it is a guard against
// store DATA, which types.go's own doc calls out as still reachable through raw
// SQL ("it does not make the shape unrepresentable in the database"). Injecting
// the row directly is what lets the guard be exercised without reintroducing a
// raw-insert seam into the store's test helper.
type blankIDUserStore struct {
	store.Store
	users store.UserStore
}

func (s blankIDUserStore) Users() store.UserStore { return s.users }

type blankIDUsers struct {
	store.UserStore
	row *store.User
}

func (s blankIDUsers) GetByUsername(context.Context, string) (*store.User, error) {
	return s.row, nil
}

// TestLogin_BlankUserIDRowIsRefusedNotPanicked pins the OUTCOME of handing
// Login a blank-id users row: no session, no panic, bad-credentials.
//
// The password supplied here is CORRECT, so a wrong-password path cannot be
// what produces the refusal -- only a blank-id guard can.
//
// Deliberately NOT a pin on any single guard. Login refuses this row three
// times over: the mint at the top, the re-mint from the locked row inside
// RunInUserAuthTransaction, and CreateSession's own zero refusal (which
// zeroUserIDDenyFuncs pins separately). Verified by overlay: removing the first
// mint leaves this test green, because the re-mint catches it. So this asserts
// that no combination of those layers leaks a session, and a reader must not
// treat it as coverage for the top guard specifically.
func TestLogin_BlankUserIDRowIsRefusedNotPanicked(t *testing.T) {
	st := setupStore(t)
	hash, err := password.Hash("correct-horse-battery")
	require.NoError(t, err)

	wrapped := blankIDUserStore{
		Store: st,
		users: blankIDUsers{UserStore: st.Users(), row: &store.User{
			ID: "", Username: "blank", PasswordHash: hash, PasswordSet: true,
		}},
	}

	require.NotPanics(t, func() {
		sessionID, user, _, loginErr := auth.Login(context.Background(), wrapped, "blank", "correct-horse-battery", auth.DefaultSessionDuration)
		require.Error(t, loginErr)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(loginErr),
			"a blank-id row is refused as bad credentials, not surfaced as an internal error")
		assert.Empty(t, sessionID, "no session id is handed back for a blank-id row")
		assert.Nil(t, user, "no user is handed back for a blank-id row")
	})
}

// blankIDSessionStore hands ValidateToken a joined session row whose user_id is
// blank. Same rationale as blankIDUserStore above: the guard defends against
// store data, so the row has to be injected rather than seeded.
type blankIDSessionStore struct {
	store.Store
	sessions store.SessionStore
}

func (s blankIDSessionStore) Sessions() store.SessionStore { return s.sessions }

type blankIDSessions struct {
	store.SessionStore
	row *store.SessionWithUser
}

func (s blankIDSessions) ValidateWithUser(context.Context, string, time.Time) (*store.SessionWithUser, error) {
	return s.row, nil
}

// TestValidateToken_BlankUserIDIsUnauthenticatedNotPanic pins the mint guard on
// the highest-traffic identity path in the hub -- every cookie-authenticated
// RPC lands here. The session row is otherwise entirely valid (unexpired,
// joined username present), so only the blank joined user_id can be producing
// the refusal: swap the guard for a MustNew and this panics instead of failing.
func TestValidateToken_BlankUserIDIsUnauthenticatedNotPanic(t *testing.T) {
	st := setupStore(t)
	wrapped := blankIDSessionStore{
		Store: st,
		sessions: blankIDSessions{SessionStore: st.Sessions(), row: &store.SessionWithUser{
			UserID:    "",
			Username:  "blank",
			CreatedAt: time.Now().Add(-time.Minute),
			ExpiresAt: time.Now().Add(time.Hour),
		}},
	}

	require.NotPanics(t, func() {
		info, err := auth.ValidateToken(context.Background(), wrapped, "any-token")
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
			"a blank joined user_id fails closed the same way an expired session does")
		assert.Nil(t, info)
	})
}
