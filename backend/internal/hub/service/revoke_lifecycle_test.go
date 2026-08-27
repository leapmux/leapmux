package service_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// adminBearerReq wraps a message in a request that carries an API bearer
// rather than the session cookie authedReq sends. A CLI operator
// authenticates this way, and the two credential kinds take different paths
// through the interceptor's cache -- so a case about the credential that
// made the call has to be able to choose which one that is.
func adminBearerReq[T any](msg *T, bearer string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+bearer)
	return req
}

// seedRevocableUser creates one non-admin user and returns their id.
func seedRevocableUser(t *testing.T, env *adminUserEnv, username string) string {
	t.Helper()
	created, err := env.client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username: username, Password: "password1",
	}, env.token))
	require.NoError(t, err)
	return created.Msg.GetUser().GetId()
}

// TestAdminUserService_RevokeSessionClosesThatSessionsChannels pins the
// in-process path RevokeSession owes after its store delete.
//
// The durable delete reaches every hub, but only on the revocation
// watcher's next sweep, so without this effect the acting hub keeps serving
// the revoked session for that whole interval. The path is per-credential:
// the SESSION path, carrying the id the caller gave, and neither of the
// other two.
func TestAdminUserService_RevokeSessionClosesThatSessionsChannels(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	userID := seedRevocableUser(t, env, "carol")
	sessionID, _, _, err := auth.Login(ctx, env.st, "carol", "password1", auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = env.client.RevokeSession(ctx, authedReq(&leapmuxv1.RevokeSessionRequest{
		Id: sessionID,
	}, env.token))
	require.NoError(t, err)

	assert.Equal(t, []string{sessionID}, env.revocations.closedSessions(),
		"the hub closes the revoked session's channels in process")
	assert.Empty(t, env.revocations.closedBearers(), "a session revoke touches no bearer")
	assert.Empty(t, env.revocations.recorded(), "a session revoke is not a user-wide revocation")

	// The durable half, so the case still fails if the effect runs
	// for a row the store never deleted.
	sessions, err := env.st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
		UserID:     userid.MustNew(userID),
		PageParams: store.PageParams{Limit: 10},
	}, time.Now().UTC())
	require.NoError(t, err)
	assert.Empty(t, sessions.Rows)

	// The administrator's OWN event kind, not the sign-out one. A step-up
	// mutation that queued on the user-auth lock reads exactly this to
	// distinguish a revoke from a logout, and both paths delete the same row, so
	// Sessions().Delete here would look identical and silently let that
	// mutation commit. See store.RevocationEventKindSessionRevoked.
	revoked, err := env.st.RevocationEvents().SessionWasRevoked(ctx, sessionID)
	require.NoError(t, err)
	assert.True(t, revoked, "an administrator's revoke must leave its own durable record")
}

// TestAdminUserService_RevokeSessionRefusalRunsNoEffect pins the other side
// of the ordering: revokeByID returns BEFORE the lifecycle call for an
// empty id and for a row the store did not delete, so a refused revoke must
// tear nothing down.
func TestAdminUserService_RevokeSessionRefusalRunsNoEffect(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	_, err := env.client.RevokeSession(ctx, authedReq(&leapmuxv1.RevokeSessionRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = env.client.RevokeSession(ctx, authedReq(&leapmuxv1.RevokeSessionRequest{
		Id: "no-such-session",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	assert.Empty(t, env.revocations.closedSessions(), "a refused revoke closes nothing")
}

// TestAdminUserService_RevokeAPITokenClosesThatBearer pins the API path.
//
// The KIND is the half that a call can get wrong while still looking
// delivered: the bearer ref is table-qualified, so an API revoke that
// passed BearerKindDelegation would evict a row in the other table and
// leave the revoked one live in this hub's cache.
func TestAdminUserService_RevokeAPITokenClosesThatBearer(t *testing.T) {
	env := setupAdminUserTest(t)
	env.elevateAdminSession(t)
	ctx := context.Background()

	userID := seedRevocableUser(t, env, "carol")
	issued, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId: userID, ClientName: "carols-laptop", ClientType: "cli",
	}, env.token))
	require.NoError(t, err)

	_, err = env.client.RevokeAPIToken(ctx, authedReq(&leapmuxv1.RevokeAPITokenRequest{
		Id: issued.Msg.GetTokenId(),
	}, env.token))
	require.NoError(t, err)

	assert.Equal(t, []auth.BearerRef{auth.NewBearerRef(auth.BearerKindAPI, issued.Msg.GetTokenId())},
		env.revocations.closedBearers(),
		"the API path fires for the API row the caller gave")
	assert.Empty(t, env.revocations.closedSessions(), "a bearer revoke touches no session")
	assert.Empty(t, env.revocations.recorded(), "a bearer revoke is not a user-wide revocation")

	// A refused second revoke adds nothing: the row is already revoked, so
	// the store reports zero rows and the effect must not run again.
	_, err = env.client.RevokeAPIToken(ctx, authedReq(&leapmuxv1.RevokeAPITokenRequest{
		Id: issued.Msg.GetTokenId(),
	}, env.token))
	require.Error(t, err)
	assert.Len(t, env.revocations.closedBearers(), 1, "an already-revoked row runs no second effect")
}

// TestAdminUserService_RevokeDelegationTokenClosesThatBearer pins the
// delegation path, and that it carries its OWN kind rather than the API kind
// its sibling verb passes.
func TestAdminUserService_RevokeDelegationTokenClosesThatBearer(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	userID := seedRevocableUser(t, env, "carol")
	worker := storetest.SeedWorker(t, env.st, userID)
	tokenID := id.Generate()
	require.NoError(t, env.st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		WorkerID:         worker.ID,
		IssuedForTabID:   "tab-1",
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       []byte("hash"),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))

	_, err := env.client.RevokeDelegationToken(ctx, authedReq(
		&leapmuxv1.AdminUserServiceRevokeDelegationTokenRequest{Id: tokenID}, env.token))
	require.NoError(t, err)

	assert.Equal(t, []auth.BearerRef{auth.NewBearerRef(auth.BearerKindDelegation, tokenID)},
		env.revocations.closedBearers(),
		"the delegation path fires with the delegation kind, not the API kind")
	assert.Empty(t, env.revocations.closedSessions())
	assert.Empty(t, env.revocations.recorded())

	tokens, err := env.st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
		UserID:         &userID,
		IncludeRevoked: true,
		PageParams:     store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	require.Len(t, tokens.Rows, 1)
	assert.NotNil(t, tokens.Rows[0].RevokedAt)
}

// TestAdminUserService_RevokeUserSessionsReportsTheCommittedGeneration pins
// the user-wide path of the OTHER verb that runs revokeEveryUserCredential.
//
// The epoch must be the one the transaction committed, read inside it. A
// post-commit re-read looks identical on a quiet hub and drifts under a
// concurrent revocation, which would evict at an epoch this commit never
// produced. ResetPassword is the only verb that pinned this before.
func TestAdminUserService_RevokeUserSessionsReportsTheCommittedGeneration(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	userID := seedRevocableUser(t, env, "carol")
	_, _, _, err := auth.Login(ctx, env.st, "carol", "password1", auth.DefaultSessionDuration)
	require.NoError(t, err)

	apiTokenID := id.Generate()
	require.NoError(t, env.st.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID:         apiTokenID,
		UserID:     userid.MustNew(userID),
		ClientType: "cli",
		ClientName: "carols-laptop",
		SecretHash: []byte("hash"),
	}))
	worker := storetest.SeedWorker(t, env.st, userID)
	require.NoError(t, env.st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
		ID:               id.Generate(),
		UserID:           userid.MustNew(userID),
		WorkerID:         worker.ID,
		IssuedForTabID:   "tab-1",
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       []byte("hash"),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))

	before, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)

	resp, err := env.client.RevokeUserSessions(ctx, authedReq(&leapmuxv1.RevokeUserSessionsRequest{
		Id: userID,
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.Msg.GetApiTokensRevoked())
	assert.Equal(t, int64(1), resp.Msg.GetDelegationTokensRevoked())

	after, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	assert.Greater(t, after.AuthGeneration, before.AuthGeneration)

	require.Equal(t, []userRevocationCall{{userID: userID, generation: after.AuthGeneration}},
		env.revocations.recorded())
	assert.Empty(t, env.revocations.restampedSessions(),
		"the administrator surface spares no session")
	// The user-wide path covers every credential at once, so neither
	// per-credential path runs beside it.
	assert.Empty(t, env.revocations.closedSessions())
	assert.Empty(t, env.revocations.closedBearers())
}
