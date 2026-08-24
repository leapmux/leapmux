package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	"google.golang.org/protobuf/proto"
)

type adminUserEnv struct {
	client    leapmuxv1connect.AdminUserServiceClient
	st        store.Store
	token     string
	adminID   string
	validator *auth.TokenValidator
	// revocations records the in-process half of every revocation this
	// service performs. The store half is durable and a test can read it
	// back; this half is pure process state, so the only way to see it is
	// to hold the collaborator the service calls.
	revocations *recordingCredentialCloser
}

// recordingCredentialCloser is the CredentialChannelCloser the admin
// service's lifecycle effects run against. EVERY arm records, because every
// revoking verb on this service owes exactly one of them and the arms are
// not interchangeable: RevokeSession owes the session arm, RevokeAPIToken
// and RevokeDelegationToken owe the bearer arm carrying their OWN kind, and
// the user-wide verbs owe the user-revocation arm at the generation their
// transaction committed.
//
// An arm that records NOTHING makes the effect it stands for untestable:
// every method on CredentialLifecycleEffects is nil-safe by design, so a
// dropped call and a delivered call read alike from outside.
type recordingCredentialCloser struct {
	mu       sync.Mutex
	calls    []userRevocationCall
	sessions []string
	bearers  []auth.BearerRef
	restamps []sessionRestampCall
}

type userRevocationCall struct {
	userID     string
	generation int64
}

type sessionRestampCall struct {
	sessionID  string
	generation int64
}

func (c *recordingCredentialCloser) CloseChannelsByUserRevocation(userID string, generation int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, userRevocationCall{userID: userID, generation: generation})
	return 0
}

func (c *recordingCredentialCloser) CloseChannelsBySession(sessionID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = append(c.sessions, sessionID)
	return 0
}

func (c *recordingCredentialCloser) CloseChannelsByBearer(ref auth.BearerRef) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bearers = append(c.bearers, ref)
	return 0
}

func (c *recordingCredentialCloser) RestampSessionGeneration(sessionID string, generation int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restamps = append(c.restamps, sessionRestampCall{sessionID: sessionID, generation: generation})
}

func (c *recordingCredentialCloser) recorded() []userRevocationCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]userRevocationCall(nil), c.calls...)
}

// closedSessions reports the session ids the session arm received, in order.
func (c *recordingCredentialCloser) closedSessions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sessions...)
}

// closedBearers reports the bearer refs the bearer arm received, in order.
// The ref carries the KIND, which is the half a revoke verb can get wrong
// while still calling the effect: an API revoke that passed
// BearerKindDelegation would tear down a row in the other table.
func (c *recordingCredentialCloser) closedBearers() []auth.BearerRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]auth.BearerRef(nil), c.bearers...)
}

// restampedSessions reports the sessions a caller asked to SPARE from a
// user-wide revocation. The administrator surface must never populate this:
// its user-wide verbs run UserRevoked, not RevokeUserPreservingSession, so
// the acting session dies with every other one.
func (c *recordingCredentialCloser) restampedSessions() []sessionRestampCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sessionRestampCall(nil), c.restamps...)
}

func setupAdminUserTest(t *testing.T) *adminUserEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	hash, err := password.Hash("adminpass123")
	require.NoError(t, err)
	admin, err := service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username:     "admin",
		PasswordHash: hash,
		DisplayName:  "Admin",
		PasswordSet:  true,
		IsAdmin:      true,
	})
	require.NoError(t, err)
	session, _, _, err := auth.Login(context.Background(), st, "admin", "adminpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	opts := connect.WithInterceptors(interceptor)
	// The REAL lifecycle effects, not nil: every revoking verb on this
	// service owes an in-process teardown after its commit, and a nil
	// collaborator makes that obligation untestable (each effect method is
	// nil-safe by design, so a dropped call would look identical to a
	// delivered one).
	revocations := &recordingCredentialCloser{}
	lifecycle := auth.NewCredentialLifecycleEffects(contexts, revocations, nil)
	path, handler := leapmuxv1connect.NewAdminUserServiceHandler(service.NewAdminUserService(st, tv, lifecycle, nil, nil), opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	return &adminUserEnv{
		client:      leapmuxv1connect.NewAdminUserServiceClient(server.Client(), server.URL, connect.WithGRPC()),
		st:          st,
		token:       session,
		adminID:     admin.ID,
		validator:   tv,
		revocations: revocations,
	}
}

func TestAdminUserService_GetUser_Selector(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	_, err := env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "id or username is required")

	_, err = env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{
		Id: env.adminID, Username: "admin",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "mutually exclusive")

	_, err = env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{Id: "missing"}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{Username: "nobody"}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	resp, err := env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{Username: "admin"}, env.token))
	require.NoError(t, err)
	assert.Equal(t, env.adminID, resp.Msg.GetUser().GetId())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
}

func TestAdminUserService_CreateUser_RefusalsAndHappyPath(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	_, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Password: "password1",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "solo",
		Password: "password1",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reserved")

	_, err = env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "bob",
		Password: "short",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "at least")

	resp, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username:    "bob",
		Password:    "password1",
		DisplayName: "Bob",
		IsAdmin:     true,
	}, env.token))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.GetUser())
	assert.Equal(t, "bob", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
	assert.NotContains(t, resp.Msg.String(), "password1")

	_, err = env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "bob",
		Password: "password1",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestAdminUserService_DeleteUser_ForceAndCredentialRevocation(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "carol",
		Password: "password1",
	}, env.token))
	require.NoError(t, err)
	carolID := created.Msg.GetUser().GetId()

	worker := storetest.SeedWorker(t, env.st, carolID)
	tokenID := id.Generate()
	require.NoError(t, env.st.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     userid.MustNew(carolID),
		ClientType: "cli",
		ClientName: "test",
		SecretHash: []byte("hash"),
	}))
	_, _, _, err = auth.Login(ctx, env.st, "carol", "password1", auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = env.client.DeleteUser(ctx, authedReq(&leapmuxv1.DeleteUserRequest{
		Username: "admin",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "force")

	_, err = env.client.DeleteUser(ctx, authedReq(&leapmuxv1.DeleteUserRequest{
		Id: carolID,
	}, env.token))
	require.NoError(t, err)

	sessions, err := env.st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
		UserID:     userid.MustNew(carolID),
		PageParams: store.PageParams{Limit: 10},
	}, time.Now().UTC())
	require.NoError(t, err)
	assert.Empty(t, sessions.Rows)

	tokens, err := env.st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
		UserID:         &carolID,
		IncludeRevoked: true,
		PageParams:     store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	require.Len(t, tokens.Rows, 1)
	require.NotNil(t, tokens.Rows[0].RevokedAt)

	deletedWorker, err := env.st.Workers().GetByIDIncludeDeleted(ctx, worker.ID)
	require.NoError(t, err)
	assert.NotNil(t, deletedWorker.DeletedAt)

	// Deleting the last remaining admin currently succeeds: there is no
	// last-admin guard on this RPC. Pin that so a later guard is a
	// deliberate change, not a silent one.
	_, err = env.client.DeleteUser(ctx, authedReq(&leapmuxv1.DeleteUserRequest{
		Username: "admin",
		Force:    true,
	}, env.token))
	require.NoError(t, err)
}

func TestAdminUserService_IssueAndRevokeAPIToken(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	_, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		ClientName: "cli",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "id or username is required")

	_, err = env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId: env.adminID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "client_name is required")

	_, err = env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId:     "missing",
		ClientName: "cli",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	issued, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId:     env.adminID,
		ClientName: "cli",
		ClientType: "cli",
	}, env.token))
	require.NoError(t, err)
	assert.NotEmpty(t, issued.Msg.GetTokenId())
	assert.NotEmpty(t, issued.Msg.GetAccessToken())
	assert.NotEmpty(t, issued.Msg.GetRefreshToken())

	_, err = env.client.RevokeAPIToken(ctx, authedReq(&leapmuxv1.RevokeAPITokenRequest{
		Id: issued.Msg.GetTokenId(),
	}, env.token))
	require.NoError(t, err)

	_, err = env.client.RevokeAPIToken(ctx, authedReq(&leapmuxv1.RevokeAPITokenRequest{
		Id: issued.Msg.GetTokenId(),
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "already revoked")

	_, err = env.client.RevokeAPIToken(ctx, authedReq(&leapmuxv1.RevokeAPITokenRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// ttl <= 0 uses the default access-token lifetime rather than minting
	// an immediately-expired bearer.
	issuedDefault, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId:     env.adminID,
		ClientName: "default-ttl",
		TtlSeconds: 0,
	}, env.token))
	require.NoError(t, err)
	assert.NotEmpty(t, issuedDefault.Msg.GetAccessToken())
}

// TestAdminUserService_UpdateUser_EmailRules covers the two rules the RPC
// reproduces that no test pinned after the offline verbs were deleted.
//
// Both are subtle enough to break silently. `CheckEmailAvailable` must
// EXCLUDE the user's own row, or re-saving an address a user already holds
// fails against herself; and a collision must be reported as an EMAIL
// collision, because "already in use" against the wrong field sends the
// operator to change the wrong thing.
func TestAdminUserService_UpdateUser_EmailRules(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	create := func(username, email string) {
		_, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
			Username: username, Password: "password123", Email: email,
		}, env.token))
		require.NoError(t, err)
	}
	create("alice", "alice@example.com")
	create("bob", "bob@example.com")

	// Re-setting the address she already holds is a no-op, not a conflict.
	resp, err := env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Username: "alice", Email: ptr("alice@example.com"),
	}, env.token))
	require.NoError(t, err, "a user's own address must not collide with herself")
	assert.Equal(t, "alice@example.com", resp.Msg.GetUser().GetEmail())

	// Another user's address IS a conflict, and the message names the email.
	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Username: "alice", Email: ptr("bob@example.com"),
	}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bob@example.com",
		"the refusal must name the EMAIL, not the username")

	// The other editable fields still apply.
	resp, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Username: "alice", DisplayName: ptr("Alice A"), EmailVerified: ptrBool(true),
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "Alice A", resp.Msg.GetUser().GetDisplayName())
	assert.True(t, resp.Msg.GetUser().GetEmailVerified())

	// A request that names no field changes nothing and says so.
	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Username: "alice",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestAdminUserService_CreateUserRequiresEmailWhenVerificationRequired pins
// the funnel guard: on a hub that requires email verification, an email-less
// non-admin account lands on /verify-email with no code that can ever
// arrive (the login flow flags it verification-required, but there is
// nothing to verify). Admins and explicitly-verified accounts are exempt.
func TestAdminUserService_CreateUserRequiresEmailWhenVerificationRequired(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	hash, err := password.Hash("adminpass123")
	require.NoError(t, err)
	_, err = service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username: "admin", PasswordHash: hash, DisplayName: "Admin", PasswordSet: true, IsAdmin: true,
	})
	require.NoError(t, err)
	session, _, _, err := auth.Login(context.Background(), st, "admin", "adminpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	// The settings manager the service reads: SMTP on makes
	// EmailVerificationEffective true.
	set := servicetest.NewSettingsManager(t, st, nil)
	seedSMTP(t, set)

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	t.Cleanup(contexts.Stop)
	adminSvc := service.NewAdminUserService(st, tv, auth.NewCredentialLifecycleEffects(contexts, nil, nil), nil, set)
	path, handler := leapmuxv1connect.NewAdminUserServiceHandler(adminSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAdminUserServiceClient(server.Client(), server.URL, connect.WithGRPC())

	// No email, not verified: refused.
	_, err = client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username: "nou-email", Password: "password123",
	}, session))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "email is required")

	// Explicitly verified without an email: allowed.
	resp, err := client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username: "trusted", Password: "password123", EmailVerified: true,
	}, session))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetUser().GetEmailVerified())

	// Email present: allowed and lands unverified, as the caller stated.
	resp, err = client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username: "withmail", Password: "password123", Email: "withmail@example.com",
	}, session))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetUser().GetEmailVerified())
}

// TestAdminUserService_LoweringEmailVerifiedRefusesForAdmins pins the admin
// arm of the lowering verb. Every path that grants is_admin forces
// email_verified=true with it (CreateUser, SetUserAdmin, offline
// bootstrap), so the stored flag matches the auth interceptor's runtime
// IsAdmin exemption. UpdateUser's lowering verb is the one write that
// could split them apart; it must refuse for admin targets, in both the
// bare form and the paired {email, email_verified:false} form, and leave
// the row untouched. Demotion (SetUserAdmin) is the documented path that
// reopens the verb.
func TestAdminUserService_LoweringEmailVerifiedRefusesForAdmins(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	// The acting administrator targets itself, bare form.
	_, err := env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Username:      "admin",
		EmailVerified: proto.Bool(false),
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "demote the account first")

	// The paired form an earlier bug shipped ({email, email_verified:false}
	// taking the unfenced arm) refuses the same way.
	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Username:      "admin",
		Email:         proto.String("admin@example.com"),
		EmailVerified: proto.Bool(false),
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	row, err := env.st.Users().GetByID(ctx, env.adminID)
	require.NoError(t, err)
	assert.True(t, row.IsAdmin, "the refusal must not demote")
	assert.True(t, row.EmailVerified, "an administrator's email_verified must stay true")
}

// TestAdminUserService_SetUserAdmin_FencesADemotedUser pins the credential
// fence, which is the whole point of the verb.
//
// Demoting an admin must bump the user's auth generation: the store
// escalates `is_admin true->false` into a generation-bearing revocation
// that logs the user out and fences live streams. A grant does not, because
// nothing a non-admin holds becomes unsafe when they gain privilege. Route
// the write through a different store call and the RPC still reports
// success while the demoted admin's sessions keep admin-era access.
func TestAdminUserService_SetUserAdmin_FencesADemotedUser(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "mallory", Password: "password123",
	}, env.token))
	require.NoError(t, err)
	target := created.Msg.GetUser().GetId()

	generation := func() int64 {
		u, err := env.st.Users().GetByID(ctx, target)
		require.NoError(t, err)
		return u.AuthGeneration
	}
	before := generation()

	// A GRANT does not fence: nothing the user holds becomes unsafe.
	granted, err := env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Username: "mallory", IsAdmin: true,
	}, env.token))
	require.NoError(t, err)
	assert.True(t, granted.Msg.GetUser().GetIsAdmin())
	assert.Equal(t, before, generation(), "a grant must not revoke the user's own credentials")

	// A REVOKE fences every credential minted while the user was an admin.
	revoked, err := env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Username: "mallory", IsAdmin: false,
	}, env.token))
	require.NoError(t, err)
	assert.False(t, revoked.Msg.GetUser().GetIsAdmin())
	assert.Greater(t, generation(), before,
		"a demotion must bump auth_generation so admin-era sessions and tokens die")
}

// Promotion forces email_verified=true so the stored flag matches the
// runtime IsAdmin exemption. Demotion has to repair that, or the account
// keeps a verified state it never earned: the demoted row would then pass
// the login verification gate, pass UpdateUser's clear-email guard, and
// satisfy the "no durable identity" refusal that protects a first passkey.
func TestAdminUserService_SetUserAdmin_DemotionClearsUnearnedVerification(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "noaddress", Password: "password123",
	}, env.token))
	require.NoError(t, err)
	target := created.Msg.GetUser().GetId()

	row, err := env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)
	require.Empty(t, row.Email, "precondition: the account has no address to verify")
	require.False(t, row.EmailVerified)

	_, err = env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Username: "noaddress", IsAdmin: true,
	}, env.token))
	require.NoError(t, err)
	row, err = env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)
	require.True(t, row.EmailVerified, "the admin invariant forces the flag")

	_, err = env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Username: "noaddress", IsAdmin: false,
	}, env.token))
	require.NoError(t, err)
	row, err = env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)
	assert.False(t, row.EmailVerified,
		"with no address there is nothing that could have been confirmed")
}

// The other half of the rule: a demotion must NOT un-verify an address the
// user really did confirm, because the row records no pre-promotion value.
func TestAdminUserService_SetUserAdmin_DemotionKeepsAConfirmedAddress(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "hasaddress", Password: "password123", Email: "real@example.com",
	}, env.token))
	require.NoError(t, err)
	target := created.Msg.GetUser().GetId()
	require.NoError(t, env.st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
		ID: target, Email: "real@example.com", EmailVerified: true,
	}))

	for _, isAdmin := range []bool{true, false} {
		_, err = env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
			Username: "hasaddress", IsAdmin: isAdmin,
		}, env.token))
		require.NoError(t, err)
	}

	row, err := env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)
	assert.True(t, row.EmailVerified, "a confirmed address survives a promote/demote round trip")
}

func ptr(s string) *string { return &s }
func ptrBool(b bool) *bool { return &b }

// TestAdminUserService_TokenListings covers the listing behaviour the
// deleted CLI tests pinned: the user filter, the revoked filter, the
// soft-deleted-owner scope, and the cursor refusal.
func TestAdminUserService_TokenListings(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "alice", Password: "password123",
	}, env.token))
	require.NoError(t, err)
	alice := created.Msg.GetUser().GetId()

	issue := func(userID, name string) string {
		resp, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
			UserId: userID, ClientType: "cli", ClientName: name, TtlSeconds: 3600,
		}, env.token))
		require.NoError(t, err)
		return resp.Msg.GetTokenId()
	}
	aliceToken := issue(alice, "alice-laptop")
	issue(env.adminID, "admin-laptop")

	list := func(req *leapmuxv1.ListAPITokensRequest) []*leapmuxv1.AdminAPIToken {
		resp, err := env.client.ListAPITokens(ctx, authedReq(req, env.token))
		require.NoError(t, err)
		return resp.Msg.GetTokens()
	}

	// An OMITTED limit must return rows. The proto default is 0, and the
	// store reads 0 as "no rows", so a client that does not pass a limit
	// used to get an empty page it could not tell from an empty table.
	assert.Len(t, list(&leapmuxv1.ListAPITokensRequest{}), 2,
		"an omitted limit must take the hub's default page size")

	// The user filter scopes the listing.
	scoped := list(&leapmuxv1.ListAPITokensRequest{UserId: alice})
	require.Len(t, scoped, 1)
	assert.Equal(t, aliceToken, scoped[0].GetId())
	assert.False(t, scoped[0].GetOwnerDeleted())

	// Revoked tokens are hidden by default and visible for forensics.
	_, err = env.client.RevokeAPIToken(ctx, authedReq(&leapmuxv1.RevokeAPITokenRequest{Id: aliceToken}, env.token))
	require.NoError(t, err)
	assert.Empty(t, list(&leapmuxv1.ListAPITokensRequest{UserId: alice}),
		"a revoked token is hidden unless asked for")
	forensic := list(&leapmuxv1.ListAPITokensRequest{UserId: alice, IncludeRevoked: true})
	require.Len(t, forensic, 1)
	assert.NotNil(t, forensic[0].GetRevokedAt())

	// A SOFT-DELETED owner's tokens stay enumerable by id. That is what the
	// id filter exists for: an operator finds a deleted account's live
	// credentials and revokes them. Resolving the id among live users only
	// would answer "not found" and hide them.
	_, err = env.client.DeleteUser(ctx, authedReq(&leapmuxv1.DeleteUserRequest{Id: alice}, env.token))
	require.NoError(t, err)
	afterDelete := list(&leapmuxv1.ListAPITokensRequest{UserId: alice, IncludeRevoked: true})
	require.Len(t, afterDelete, 1, "a soft-deleted owner's tokens must stay reachable by id")
	assert.True(t, afterDelete[0].GetOwnerDeleted(), "and the row must say the owner is gone")

	// A malformed cursor is the caller's to fix, not a hub fault.
	_, err = env.client.ListAPITokens(ctx, authedReq(&leapmuxv1.ListAPITokensRequest{
		Cursor: "not-a-cursor",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
		"a stale cursor must not read as a 500")
	assert.Contains(t, err.Error(), "cursor")
}

// TestAdminUserService_ListSessions_OmittedLimitReturnsRows pins the same
// page-size guarantee on the session listing.
func TestAdminUserService_ListSessions_OmittedLimitReturnsRows(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	resp, err := env.client.ListSessions(ctx, authedReq(&leapmuxv1.ListSessionsRequest{}, env.token))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.GetSessions(),
		"the admin's own login session must appear without an explicit limit")

	_, err = env.client.ListSessions(ctx, authedReq(&leapmuxv1.ListSessionsRequest{
		Cursor: "not-a-cursor",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestAdminUserService_LoweringEmailVerifiedAlwaysFences pins the fenced
// verb. Lowering email_verified reduces the user's auth gate, so it must
// bump auth_generation and tear the user's leases and channels down.
// UpdateEmail is NOT fenced, so as one else-if the pair
// `{email, email_verified:false}` took the unfenced path and left every
// token of a now-unverified account live, while `{email_verified:false}`
// alone fenced them.
func TestAdminUserService_LoweringEmailVerifiedAlwaysFences(t *testing.T) {
	ctx := context.Background()

	generationAfter := func(t *testing.T, req *leapmuxv1.UpdateUserRequest) (int64, int64) {
		t.Helper()
		env := setupAdminUserTest(t)
		created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
			Username: "fenced", Password: "userpass123", Email: "fenced@example.com", EmailVerified: true,
		}, env.token))
		require.NoError(t, err)
		before, err := env.st.Users().GetByID(ctx, created.Msg.GetUser().GetId())
		require.NoError(t, err)

		req.Id = created.Msg.GetUser().GetId()
		_, err = env.client.UpdateUser(ctx, authedReq(req, env.token))
		require.NoError(t, err)
		after, err := env.st.Users().GetByID(ctx, created.Msg.GetUser().GetId())
		require.NoError(t, err)
		return before.AuthGeneration, after.AuthGeneration
	}

	verifiedOnly, verifiedOnlyAfter := generationAfter(t, &leapmuxv1.UpdateUserRequest{
		EmailVerified: proto.Bool(false),
	})
	require.Greater(t, verifiedOnlyAfter, verifiedOnly,
		"lowering email_verified alone must fence the user's tokens")

	withEmail, withEmailAfter := generationAfter(t, &leapmuxv1.UpdateUserRequest{
		Email:         proto.String("moved@example.com"),
		EmailVerified: proto.Bool(false),
	})
	assert.Equal(t, verifiedOnlyAfter-verifiedOnly, withEmailAfter-withEmail,
		"the same reduction beside an address change must fence it exactly the same way")
}

// TestAdminUserService_SetUserAdminRefusesSelfDemotion pins the
// self-demotion guard. UpdateAdmin runs the fenced verb, so removing your
// own administrator access logs you out at once and the auth interceptor
// then denies every Admin* procedure to the account -- recovery needs the
// offline verb, which itself refuses while any administrator remains.
func TestAdminUserService_SetUserAdminRefusesSelfDemotion(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	_, err := env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Id: env.adminID, IsAdmin: false,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "pass force to confirm")

	still, err := env.st.Users().GetByID(ctx, env.adminID)
	require.NoError(t, err)
	assert.True(t, still.IsAdmin, "the refused call must not demote")

	// force states the intent, and the demotion goes through.
	_, err = env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Id: env.adminID, IsAdmin: false, Force: true,
	}, env.token))
	require.NoError(t, err)
	demoted, err := env.st.Users().GetByID(ctx, env.adminID)
	require.NoError(t, err)
	assert.False(t, demoted.IsAdmin)
}

// TestAdminUserService_SetUserAdminAllowsDemotingSomeoneElse pins the
// guard's scope: it is about the CALLER's own access, so demoting another
// administrator needs no force.
func TestAdminUserService_SetUserAdminAllowsDemotingSomeoneElse(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	other, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "second", Password: "userpass123", IsAdmin: true,
	}, env.token))
	require.NoError(t, err)

	_, err = env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Id: other.Msg.GetUser().GetId(), IsAdmin: false,
	}, env.token))
	require.NoError(t, err)
}

// TestAdminUserService_IssueAPITokenTTLBounds pins the ttl ceiling. The
// handler multiplies the requested seconds by time.Second, and an int64
// with no ceiling WRAPS on that multiply: ttl_seconds = 20000000000 wraps
// to roughly 18 days, passes the `ttl <= 0` guard, and mints a bearer that
// expires 634 years before the operator asked.
func TestAdminUserService_IssueAPITokenTTLBounds(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	// At the bound: accepted.
	_, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId: env.adminID, ClientName: "cli", TtlSeconds: service.MaxAPITokenTTLSeconds,
	}, env.token))
	require.NoError(t, err)

	for _, secs := range []int64{
		service.MaxAPITokenTTLSeconds + 1,
		20000000000, // the value that wrapped the multiply
		-1,
	} {
		_, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
			UserId: env.adminID, ClientName: "cli", TtlSeconds: secs,
		}, env.token))
		require.Errorf(t, err, "ttl_seconds %d must be refused", secs)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Contains(t, err.Error(), "ttl_seconds must be between")
	}
}

// TestAdminUserService_IssueAPITokenTakesTheSharedSelector pins the
// selector: the token's owner is (user_id | username), the same pair every
// sibling verb takes, so an operator does not have to remember a third
// spelling for this one verb.
func TestAdminUserService_IssueAPITokenTakesTheSharedSelector(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	got, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		Username: "admin", ClientName: "cli",
	}, env.token))
	require.NoError(t, err)
	assert.NotEmpty(t, got.Msg.GetTokenId())

	_, err = env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId: env.adminID, Username: "admin", ClientName: "cli",
	}, env.token))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestAdminUserService_CreateUserConflictNamesOnlyTheSuppliedField pins
// the conflict message. The dialect layer cannot say which unique index
// fired, so the handler -- which knows what it sent -- identifies it.
// Blaming both unconditionally produced `username "bob" or email "" is
// already taken` for a create with no email at all.
func TestAdminUserService_CreateUserConflictNamesOnlyTheSuppliedField(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	_, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "admin", Password: "userpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.Contains(t, err.Error(), `username "admin" is already taken`)
	assert.NotContains(t, err.Error(), `email ""`, "an email the caller never supplied must not be blamed")
}

// TestUserConflictErrorNamesOnlyTheSuppliedFields pins the conflict
// classifier's four arms. It runs only when a unique index fires on a lost
// race, so the RPC pre-checks answer first and the arms are unreachable
// from the client side.
//
// The dialect layer reports every duplicate as one opaque conflict, so the
// caller -- which knows what it sent -- identifies it. Blaming both
// unconditionally produced `username "bob" or email "" is already taken`
// for a create with no email at all.
func TestUserConflictErrorNamesOnlyTheSuppliedFields(t *testing.T) {
	conflict := store.ErrConflict

	both := service.UserConflictErrorForTest(conflict, "bob", "bob@example.com")
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(both))
	assert.Contains(t, both.Error(), `username "bob"`)
	assert.Contains(t, both.Error(), `email "bob@example.com"`)

	usernameOnly := service.UserConflictErrorForTest(conflict, "bob", "")
	assert.Contains(t, usernameOnly.Error(), `username "bob" is already taken`)
	assert.NotContains(t, usernameOnly.Error(), "email")

	emailOnly := service.UserConflictErrorForTest(conflict, "", "bob@example.com")
	assert.Contains(t, emailOnly.Error(), `email "bob@example.com" is already in use`)
	assert.NotContains(t, emailOnly.Error(), "username")

	neither := service.UserConflictErrorForTest(conflict, "", "")
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(neither))
	assert.Contains(t, neither.Error(), "conflicting user record")

	// A non-conflict store fault stays internal: it is not the caller's
	// field to correct.
	other := service.UserConflictErrorForTest(errors.New("disk on fire"), "bob", "")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(other))
}

// resetPasswordEnv seeds one non-admin user who holds every credential kind
// a reset must destroy: a live session, an API token, and a delegation
// token bound to a worker.
type resetPasswordEnv struct {
	*adminUserEnv
	userID           string
	username         string
	oldPassword      string
	session          string
	apiTokenID       string
	delegationID     string
	beforeGeneration int64
}

func setupResetPasswordTest(t *testing.T) *resetPasswordEnv {
	t.Helper()
	env := setupAdminUserTest(t)
	ctx := context.Background()

	const username = "carol"
	const oldPassword = "password1"
	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: username, Password: oldPassword,
	}, env.token))
	require.NoError(t, err)
	userID := created.Msg.GetUser().GetId()

	session, _, _, err := auth.Login(ctx, env.st, username, oldPassword, auth.DefaultSessionDuration)
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
	delegationID := id.Generate()
	require.NoError(t, env.st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
		ID:               delegationID,
		UserID:           userid.MustNew(userID),
		WorkerID:         worker.ID,
		IssuedForTabID:   "tab-1",
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       []byte("hash"),
	}))

	before, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)

	return &resetPasswordEnv{
		adminUserEnv:     env,
		userID:           userID,
		username:         username,
		oldPassword:      oldPassword,
		session:          session,
		apiTokenID:       apiTokenID,
		delegationID:     delegationID,
		beforeGeneration: before.AuthGeneration,
	}
}

// TestAdminUserService_ResetPassword_Selector covers both halves of the
// shared selector and the two refusals it owes, plus the lookup failure.
//
// Each refusal must also leave the password ALONE: a verb that refuses the
// request after it wrote the hash would be worse than one that never ran.
func TestAdminUserService_ResetPassword_Selector(t *testing.T) {
	env := setupResetPasswordTest(t)
	ctx := context.Background()

	_, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Password: "newpassword1",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "id or username is required")

	_, err = env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Id: env.userID, Username: env.username, Password: "newpassword1",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "mutually exclusive")

	_, err = env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Username: "nobody", Password: "newpassword1",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, _, _, err = auth.Login(ctx, env.st, env.username, env.oldPassword, auth.DefaultSessionDuration)
	assert.NoError(t, err, "a refused reset must not have touched the password")
}

// TestAdminUserService_ResetPassword_ByIDAndByUsername pins that BOTH
// halves of the selector reach the same write: the old password stops
// working and the new one starts.
func TestAdminUserService_ResetPassword_ByIDAndByUsername(t *testing.T) {
	env := setupResetPasswordTest(t)
	ctx := context.Background()

	byID, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Id: env.userID, Password: "by-id-password",
	}, env.token))
	require.NoError(t, err)
	// The subject is echoed back whichever handle the caller sent, so a
	// caller holding only one of the two learns the other.
	assert.Equal(t, env.userID, byID.Msg.GetUserId())
	assert.Equal(t, env.username, byID.Msg.GetUsername())

	_, _, _, err = auth.Login(ctx, env.st, env.username, env.oldPassword, auth.DefaultSessionDuration)
	assert.Error(t, err, "the old password must stop working")
	_, _, _, err = auth.Login(ctx, env.st, env.username, "by-id-password", auth.DefaultSessionDuration)
	require.NoError(t, err, "the new password must work")

	byUsername, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Username: env.username, Password: "by-username-password",
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, env.userID, byUsername.Msg.GetUserId())

	_, _, _, err = auth.Login(ctx, env.st, env.username, "by-id-password", auth.DefaultSessionDuration)
	assert.Error(t, err, "the previous password must stop working too")
	_, _, _, err = auth.Login(ctx, env.st, env.username, "by-username-password", auth.DefaultSessionDuration)
	assert.NoError(t, err)
}

// TestAdminUserService_ResetPassword_RefusesAPasswordThePolicyRefuses pins
// that the RPC runs the SAME validator the browser mirrors, so a password
// the form would refuse cannot arrive through the CLI instead.
func TestAdminUserService_ResetPassword_RefusesAPasswordThePolicyRefuses(t *testing.T) {
	env := setupResetPasswordTest(t)
	ctx := context.Background()

	for name, password := range map[string]string{
		"empty":     "",
		"too short": "short",
		"too long":  strings.Repeat("a", 129),
		"non-ASCII": "pässwörd-that-is-long-enough",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
				Id: env.userID, Password: password,
			}, env.token))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Contains(t, err.Error(), "password must")
		})
	}

	_, _, _, err := auth.Login(ctx, env.st, env.username, env.oldPassword, auth.DefaultSessionDuration)
	assert.NoError(t, err, "a refused password must not have replaced the stored one")
	assert.Empty(t, env.revocations.recorded(),
		"a refused reset must not tear a credential down either")
}

// TestAdminUserService_ResetPassword_TearsDownEveryCredential is the test
// that matters: a reset exists because someone else may know the old
// password, so every credential that password authenticated must die.
//
// Both halves are asserted. The DURABLE half is the store: sessions gone,
// bearer rows revoked, auth_generation advanced. The IN-PROCESS half is the
// lifecycle effect, which is the only thing that evicts this hub's own
// caches and channels before the revocation watcher's next sweep — up to
// two seconds later, during which the old credential would still be served.
func TestAdminUserService_ResetPassword_TearsDownEveryCredential(t *testing.T) {
	env := setupResetPasswordTest(t)
	ctx := context.Background()

	resp, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Id: env.userID, Password: "brand-new-password",
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.Msg.GetApiTokensRevoked())
	assert.Equal(t, int64(1), resp.Msg.GetDelegationTokensRevoked())

	sessions, err := env.st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
		UserID:     userid.MustNew(env.userID),
		PageParams: store.PageParams{Limit: 10},
	}, time.Now().UTC())
	require.NoError(t, err)
	assert.Empty(t, sessions.Rows, "every session of the user must be deleted")
	_, err = auth.ValidateToken(ctx, env.st, env.session)
	assert.Error(t, err, "the user's session token must stop validating")

	apiTokens, err := env.st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
		UserID:         &env.userID,
		IncludeRevoked: true,
		PageParams:     store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	require.Len(t, apiTokens.Rows, 1)
	assert.NotNil(t, apiTokens.Rows[0].RevokedAt, "the user's API token must be revoked")

	delegationTokens, err := env.st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
		UserID:         &env.userID,
		IncludeRevoked: true,
		PageParams:     store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	require.Len(t, delegationTokens.Rows, 1)
	assert.NotNil(t, delegationTokens.Rows[0].RevokedAt, "the user's delegation token must be revoked")

	after, err := env.st.Users().GetByID(ctx, env.userID)
	require.NoError(t, err)
	assert.Greater(t, after.AuthGeneration, env.beforeGeneration,
		"the reset must advance the user's auth epoch")

	// The in-process half, at exactly the epoch the transaction committed.
	// A second read after the commit would look the same on a quiet hub and
	// drift under a concurrent revocation, which is why the handler captures
	// the generation inside the transaction.
	require.Equal(t, []userRevocationCall{{userID: env.userID, generation: after.AuthGeneration}},
		env.revocations.recorded())
}

// TestAdminUserService_ResetPassword_SetsAPasswordForAnOAuthOnlyUser covers
// the account with no password at all: `password_set` must flip, or the
// user still cannot log in with what the administrator just handed them.
func TestAdminUserService_ResetPassword_SetsAPasswordForAnOAuthOnlyUser(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	oauthUser, err := service.CreateUser(ctx, env.st, service.CreateUserParams{
		Username:    "dave",
		DisplayName: "Dave",
		PasswordSet: false,
	})
	require.NoError(t, err)

	resp, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Username: "dave", Password: "first-password",
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, oauthUser.ID, resp.Msg.GetUserId())

	updated, err := env.st.Users().GetByID(ctx, oauthUser.ID)
	require.NoError(t, err)
	assert.True(t, updated.PasswordSet, "the reset must mark the account as having a password")
	_, _, _, err = auth.Login(ctx, env.st, "dave", "first-password", auth.DefaultSessionDuration)
	assert.NoError(t, err)
}

// TestAdminUserService_ResetPassword_SelfResetEndsTheActingSession pins the
// TOTAL self-reset the method doc promises: an administrator who resets
// their OWN password loses every session they hold, the cookie that made
// the call included.
//
// Every other reset case targets a different user (carol, dave), so a
// regression that spared the caller -- the natural "do not lock the
// operator out" change -- passes all of them. The durable half alone does
// not prove it either: the interceptor memoizes a validated session for
// sessionCacheTTL, so the deleted row keeps authenticating for that whole
// window unless the handler ALSO applies the in-process UserRevoked effect.
// The case warms that cache first, which leaves the effect as the only
// thing that can end the credential.
func TestAdminUserService_ResetPassword_SelfResetEndsTheActingSession(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	// Warm the interceptor's session cache with the acting cookie.
	_, err := env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{Id: env.adminID}, env.token))
	require.NoError(t, err)

	before, err := env.st.Users().GetByID(ctx, env.adminID)
	require.NoError(t, err)

	_, err = env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Id: env.adminID, Password: "brand-new-adminpass",
	}, env.token))
	require.NoError(t, err)

	after, err := env.st.Users().GetByID(ctx, env.adminID)
	require.NoError(t, err)
	assert.Greater(t, after.AuthGeneration, before.AuthGeneration,
		"a self-reset advances the caller's own auth epoch")

	// The in-process half fired for the CALLER's own user, at the epoch the
	// transaction committed rather than at whatever a post-commit re-read
	// happens to find.
	assert.Equal(t, []userRevocationCall{{userID: env.adminID, generation: after.AuthGeneration}},
		env.revocations.recorded())
	assert.Empty(t, env.revocations.restampedSessions(),
		"the administrator surface spares no session, the acting one included")

	sessions, err := env.st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
		UserID:     userid.MustNew(env.adminID),
		PageParams: store.PageParams{Limit: 10},
	}, time.Now().UTC())
	require.NoError(t, err)
	assert.Empty(t, sessions.Rows, "the caller's own session row is deleted too")

	_, err = env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{Id: env.adminID}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"the cookie that made the call stops authenticating at once")

	// Not a one-way door: the caller chose the new password and logs in
	// again with it. This is what makes the missing force flag correct.
	_, _, _, err = auth.Login(ctx, env.st, "admin", "brand-new-adminpass", auth.DefaultSessionDuration)
	assert.NoError(t, err)
}

// TestAdminUserService_ResetPassword_SelfResetKillsTheActingAPIToken is the
// same contract for the OTHER credential an administrator can hold. A CLI
// operator authenticates with an API bearer, and the doc says a self-reset
// kills that bearer as well -- so the verb ends the very credential that
// carried it, and the count it reports includes that token.
func TestAdminUserService_ResetPassword_SelfResetKillsTheActingAPIToken(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	issued, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId: env.adminID, ClientName: "admin-cli", ClientType: "cli",
	}, env.token))
	require.NoError(t, err)
	bearer := issued.Msg.GetAccessToken()

	// Warm the interceptor's bearer cache with the acting credential, so the
	// revoked row alone cannot be what ends it inside the cache window.
	_, err = env.client.GetUser(ctx, adminBearerReq(&leapmuxv1.GetUserRequest{Id: env.adminID}, bearer))
	require.NoError(t, err)

	resp, err := env.client.ResetPassword(ctx, adminBearerReq(&leapmuxv1.ResetPasswordRequest{
		Id: env.adminID, Password: "brand-new-adminpass",
	}, bearer))
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.Msg.GetApiTokensRevoked(),
		"the acting token is counted among the revoked, not exempted from them")

	tokens, err := env.st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
		UserID:         &env.adminID,
		IncludeRevoked: true,
		PageParams:     store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	require.Len(t, tokens.Rows, 1)
	assert.Equal(t, issued.Msg.GetTokenId(), tokens.Rows[0].ID)
	assert.NotNil(t, tokens.Rows[0].RevokedAt, "the bearer that made the call is revoked")

	_, err = env.client.GetUser(ctx, adminBearerReq(&leapmuxv1.GetUserRequest{Id: env.adminID}, bearer))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"the bearer that made the call stops authenticating at once")
}

func TestAdminUserService_SetUserAdmin_MarksEmailVerified(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "promotee", Password: "password123", DisplayName: "Promotee",
		Email: "promotee@example.com", EmailVerified: false,
	}, env.token))
	require.NoError(t, err)
	assert.False(t, created.Msg.GetUser().GetEmailVerified())

	resp, err := env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Id: created.Msg.GetUser().GetId(), IsAdmin: true,
	}, env.token))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
	assert.True(t, resp.Msg.GetUser().GetEmailVerified())

	row, err := env.st.Users().GetByID(ctx, created.Msg.GetUser().GetId())
	require.NoError(t, err)
	assert.True(t, row.EmailVerified)
}

func TestAdminUserService_CreateUser_AdminForcesEmailVerified(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	resp, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "newadmin", Password: "password123", DisplayName: "New Admin",
		Email: "newadmin@example.com", EmailVerified: false, IsAdmin: true,
	}, env.token))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
	assert.True(t, resp.Msg.GetUser().GetEmailVerified())
}

func TestAdminUserService_ResetPassword_DeletesPasskeys(t *testing.T) {
	env := setupResetPasswordTest(t)
	ctx := context.Background()

	pkID := id.Generate()
	require.NoError(t, env.st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
		ID: pkID, UserID: env.userID, CredentialID: []byte("cred-" + pkID),
		PublicKey: []byte("pubkey"), SignCount: 0, Transports: "[]",
		FriendlyName: "Phone", KeyVersion: 1, CreatedAt: time.Now().UTC(),
	}))

	_, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Id: env.userID, Password: "brand-new-password",
	}, env.token))
	require.NoError(t, err)

	rows, err := env.st.PasskeyCredentials().ListByUser(ctx, env.userID)
	require.NoError(t, err)
	assert.Empty(t, rows)
}
