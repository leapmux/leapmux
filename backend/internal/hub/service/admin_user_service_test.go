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
	"github.com/leapmux/leapmux/internal/hub/mail"
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
	// mail records the issuance notice IssueAPIToken sends. It is a real
	// Sender rather than nil, because "sends nothing" and "sends the wrong
	// thing" look identical through a nil one.
	mail *recordingSender
}

// awaitCredentialNotice waits for the issuance notice and returns it.
//
// The hub sends the notice DETACHED, on its own goroutine and its own
// deadline, so that an SMTP exchange never delays a mint the caller is
// blocked on. That is why this polls rather than reading straight after the
// call.
func (e *adminUserEnv) awaitCredentialNotice(t *testing.T) mail.Message {
	t.Helper()
	var got *mail.Message
	require.Eventually(t, func() bool {
		got = e.mail.last()
		return got != nil
	}, 2*time.Second, 10*time.Millisecond, "the owner must be told a credential was minted for them")
	return *got
}

// recordingCredentialCloser is the CredentialChannelCloser the admin
// service's lifecycle effects run against. EVERY path records, because every
// revoking verb on this service owes exactly one of them and the paths are
// not interchangeable: RevokeSession owes the session path, RevokeAPIToken
// and RevokeDelegationToken owe the bearer path carrying their OWN kind, and
// the user-wide verbs owe the user-revocation path at the generation their
// transaction committed.
//
// A path that records NOTHING makes the effect it stands for untestable:
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

// closedSessions reports the session ids the session path received, in order.
func (c *recordingCredentialCloser) closedSessions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sessions...)
}

// closedBearers reports the bearer refs the bearer path received, in order.
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

func setupAdminUserTestUnelevated(t *testing.T) *adminUserEnv {
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
		// VERIFIED, because the issuance notice is silent to an address
		// nobody confirmed -- an account notice to an unconfirmed address is
		// a delivery to a stranger.
		Email:         "admin@example.test",
		EmailVerified: true,
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
	sender := &recordingSender{}
	path, handler := leapmuxv1connect.NewAdminUserServiceHandler(service.NewAdminUserService(service.AdminUserServiceDeps{
		Store: st, Validator: tv, Lifecycle: lifecycle, Mail: sender,
	}), opts)
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
		mail:        sender,
	}
}

// setupAdminUserTest is the DEFAULT fixture, and its session is elevated.
//
// Four verbs on this service create durable new authority and demand an
// elevated session for it -- IssueAPIToken, CreateUser, SetUserAdmin and
// ResetPassword. Almost every test here exercises what a verb DOES rather
// than whether the gate is there, so supplying the elevation is what keeps
// those tests about their own subject. The *Unelevated builders are for the
// cases that assert the gate itself.
func setupAdminUserTest(t *testing.T) *adminUserEnv {
	t.Helper()
	env := setupAdminUserTestUnelevated(t)
	env.elevateAdminSession(t)
	return env
}

// setupAdminUserTestWithSettings is the same, with a settings manager.
func setupAdminUserTestWithSettings(t *testing.T) *adminUserEnv {
	t.Helper()
	env := setupAdminUserTestWithSettingsUnelevated(t)
	env.elevateAdminSession(t)
	return env
}

// elevateAdminSession stamps a live elevation on the environment's session.
//
// IssueAPIToken requires one, because it mints a credential that outlives
// the session that asked for it -- the same reason every /auth/cli/* consent
// leg does. A test that exercises the mint states the elevation rather than
// relying on a hole; TestAdminUserService_IssueAPITokenNeedsAnElevatedSession
// is the one that asserts the refusal.
func (e *adminUserEnv) elevateAdminSession(t *testing.T) {
	t.Helper()
	hubtestutil.ElevateSession(t, e.st, e.token, e.adminID)
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

	// Another user's address IS a conflict, and the message identifies the
	// email.
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

	// A request that specifies no field changes nothing and says so.
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
// setupAdminUserTestWithSettings is setupAdminUserTest plus the settings
// manager the service reads, with SMTP on so EmailVerificationEffective is
// true. The plain setup passes a nil manager, and every guard that asks
// "does this hub require verification" is therefore inert there.
func setupAdminUserTestWithSettingsUnelevated(t *testing.T) *adminUserEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	hash, err := password.Hash("adminpass123")
	require.NoError(t, err)
	admin, err := service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username: "admin", PasswordHash: hash, DisplayName: "Admin", PasswordSet: true, IsAdmin: true,
	})
	require.NoError(t, err)
	session, _, _, err := auth.Login(context.Background(), st, "admin", "adminpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	set := servicetest.NewSettingsManager(t, st, nil)
	seedSMTP(t, set)

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	t.Cleanup(contexts.Stop)
	adminSvc := service.NewAdminUserService(service.AdminUserServiceDeps{Store: st, Settings: set, Validator: tv, Lifecycle: auth.NewCredentialLifecycleEffects(contexts, nil, nil)})
	path, handler := leapmuxv1connect.NewAdminUserServiceHandler(adminSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	return &adminUserEnv{
		client:    leapmuxv1connect.NewAdminUserServiceClient(server.Client(), server.URL, connect.WithGRPC()),
		st:        st,
		token:     session,
		adminID:   admin.ID,
		validator: tv,
	}
}

func TestAdminUserService_CreateUserRequiresEmailWhenVerificationRequired(t *testing.T) {
	env := setupAdminUserTestWithSettings(t)
	client, session := env.client, env.token

	// No email, not verified: refused.
	_, err := client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
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

// TestAdminUserService_LoweringEmailVerifiedIsAllowedForAdmins pins the
// branch that used to refuse.
//
// email_verified records whether somebody confirmed the address, so an
// administrator's is as lowerable as anybody else's -- there is no stored
// invariant left for it to contradict. It still travels through the FENCED
// verb, in the bare form and in the paired {email, email_verified:false}
// form alike, so the account's live credentials are torn down with it.
func TestAdminUserService_LoweringEmailVerifiedIsAllowedForAdmins(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	// A genuinely confirmed administrator, so the lowering is a real
	// reduction and the fence has something to do.
	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "verifiedadmin", Password: "password123",
		Email: "verifiedadmin@example.com", EmailVerified: true, IsAdmin: true,
	}, env.token))
	require.NoError(t, err)
	target := created.Msg.GetUser().GetId()
	before, err := env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)
	require.True(t, before.EmailVerified)

	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id:            target,
		EmailVerified: proto.Bool(false),
	}, env.token))
	require.NoError(t, err)

	row, err := env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)
	assert.True(t, row.IsAdmin, "lowering the flag is not a demotion")
	assert.False(t, row.EmailVerified)
	assert.Greater(t, row.AuthGeneration, before.AuthGeneration,
		"the lowering must run through the fenced verb, which tears the account's credentials down")

	// The administrator can still use the hub: the login gate takes its own
	// exemption rather than reading a forced column.
	assert.True(t, auth.EmailVerificationFactsFromUser(row).Satisfied(true))
}

// The paired form an earlier bug shipped -- {email, email_verified:false}
// taking the UNFENCED branch -- must still take the fenced one.
func TestAdminUserService_LoweringWithAnAddressChangeStaysFenced(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "paired", Password: "password123",
		Email: "paired@example.com", EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	target := created.Msg.GetUser().GetId()
	before, err := env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)

	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id:            target,
		Email:         proto.String("moved@example.com"),
		EmailVerified: proto.Bool(false),
	}, env.token))
	require.NoError(t, err)

	row, err := env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)
	assert.Equal(t, "moved@example.com", row.Email)
	assert.False(t, row.EmailVerified)
	assert.Greater(t, row.AuthGeneration, before.AuthGeneration,
		"the paired form must not take the unfenced branch")
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

// Promotion and demotion leave email_verified ALONE, because neither
// changes whether anybody confirmed the address.
//
// Promotion used to force the flag true and demotion had to repair what the
// force left behind -- with a gap it could not close, since the row recorded
// no reason for the flag and so could not tell a confirmation from an
// invariant. Removing the force removes the repair and the gap together.
func TestAdminUserService_SetUserAdmin_LeavesEmailVerificationAlone(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "confirmed", Password: "password123",
		Email: "confirmed@example.com", EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	target := created.Msg.GetUser().GetId()

	_, err = env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Username: "confirmed", IsAdmin: true,
	}, env.token))
	require.NoError(t, err)
	row, err := env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)
	assert.True(t, row.EmailVerified, "a confirmed address stays confirmed")

	_, err = env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
		Username: "confirmed", IsAdmin: false,
	}, env.token))
	require.NoError(t, err)
	row, err = env.st.Users().GetByID(ctx, target)
	require.NoError(t, err)
	assert.True(t, row.EmailVerified,
		"demotion must not un-verify an address the user really did confirm")
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

	// The listing hides revoked tokens by default and shows them for forensics.
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

// TestAdminUserService_IssueAPITokenTTLCeiling pins the ttl ceiling. The
// handler multiplies the requested seconds by time.Second, and an int64
// with no ceiling WRAPS on that multiply: ttl_seconds = 20000000000 wraps
// to roughly 18 days, passes the `ttl <= 0` guard, and mints a bearer that
// expires 634 years before the operator asked.
func TestAdminUserService_IssueAPITokenTTLCeiling(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	// At the ceiling: accepted.
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

// TestAdminUserService_IssueAPITokenNeedsAnElevatedSession pins the gate a
// stolen administrator cookie used to bypass.
//
// The four /auth/cli/* consent legs demand an elevated session because what
// they mint outlives the session by months, and with the admin scope it
// administers the hub. This verb mints the SAME credential, with a TTL of up
// to a year, and asked for nothing: a replayed admin cookie POSTed
// {admin_scope:true, ttl_seconds:31536000} and got the pair in the response
// body without proving a password, a passkey, or an OAuth
// re-authentication.
//
// The refusal carries the elevation marker, so a browser opens the prompt
// rather than reading it as a dead session.
func TestAdminUserService_IssueAPITokenNeedsAnElevatedSession(t *testing.T) {
	env := setupAdminUserTestUnelevated(t)
	ctx := context.Background()

	_, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		Username: "admin", ClientName: "stolen-laptop", AdminScope: true, TtlSeconds: service.MaxAPITokenTTLSeconds,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "1", connectErr.Meta().Get(service.ElevationRequiredHeader),
		"the refusal must be the one a step-up prompt can clear")

	// Nothing was minted.
	page, listErr := env.st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
		PageParams: service.NormalizePageParams("", service.MaxPageLimit),
	})
	require.NoError(t, listErr)
	assert.Empty(t, page.Rows, "a refused mint must leave no row behind")

	// With a proven factor the same call succeeds. A FRESH environment,
	// because the interceptor caches the UserInfo it validated: stamping the
	// row after a refused request would be invisible until that entry
	// expired, which is the cache's job to solve and not this test's.
	elevated := setupAdminUserTest(t)
	got, err := elevated.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		Username: "admin", ClientName: "stolen-laptop", AdminScope: true, TtlSeconds: service.MaxAPITokenTTLSeconds,
	}, elevated.token))
	require.NoError(t, err)
	assert.NotEmpty(t, got.Msg.GetTokenId())
}

// A ttl_seconds picks WHICH KIND of credential this verb mints, and the two
// kinds are exclusive.
//
// Both together was the defect. The row records an EXPIRY and never the
// lifetime it was minted from, and the refresh leg rewrites that expiry with
// auth.AccessWindowFor -- the ordinary hour -- so an operator who asked for a
// year of access got one hour back the first time the credential renewed, and
// the year was unrecoverable. Nothing in the response or the admin listing
// reported the change.
func TestAdminUserService_IssueAPITokenTTLPicksTheCredentialKind(t *testing.T) {
	ctx := context.Background()

	t.Run("no ttl mints the renewing kind", func(t *testing.T) {
		env := setupAdminUserTest(t)

		got, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
			Username: "admin", ClientName: "ci-bot",
		}, env.token))
		require.NoError(t, err)
		assert.NotEmpty(t, got.Msg.GetRefreshToken(), "the ordinary credential renews itself")

		row, err := env.st.APITokens().GetByID(ctx, got.Msg.GetTokenId())
		require.NoError(t, err)
		require.NotNil(t, row.ExpiresAt)
		assert.WithinDuration(t, time.Now().UTC().Add(auth.AccessTokenTTL), *row.ExpiresAt, time.Minute)
		assert.NotNil(t, row.RefreshExpiresAt)
	})

	t.Run("a ttl mints a fixed-lifetime credential with no refresh leg", func(t *testing.T) {
		env := setupAdminUserTest(t)

		const year = int64(365 * 24 * 60 * 60)
		got, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
			Username: "admin", ClientName: "service-account", TtlSeconds: year,
		}, env.token))
		require.NoError(t, err)
		assert.Empty(t, got.Msg.GetRefreshToken(),
			"a fixed lifetime cannot survive a rotation, so there is nothing to rotate with")

		row, err := env.st.APITokens().GetByID(ctx, got.Msg.GetTokenId())
		require.NoError(t, err)
		require.NotNil(t, row.ExpiresAt)
		assert.WithinDuration(t, time.Now().UTC().Add(time.Duration(year)*time.Second), *row.ExpiresAt, time.Minute,
			"the operator gets the lifetime they configured")
		assert.Nil(t, row.RefreshExpiresAt, "no refresh leg means nothing can rewrite the expiry")
	})
}

// The owner learns about a credential minted on their account from the ADMIN
// surface, exactly as they do from a browser consent.
//
// Only the consent legs sent the notice, and this is the surface a stolen
// administrator cookie reaches -- so the one signal that does not depend on
// the owner opening Preferences was missing from the one path that most
// needed it.
func TestAdminUserService_IssueAPITokenNotifiesTheOwner(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	_, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		Username: "admin", ClientName: "ci-bot", AdminScope: true,
	}, env.token))
	require.NoError(t, err)

	msg := env.awaitCredentialNotice(t)
	assert.Contains(t, msg.Body, "ci-bot", "the notice identifies the device that asked")
}

// The CONTROL CLI's own path, which authenticates by bearer and never by
// cookie -- and is the only caller this verb actually has.
//
// A bearer takes the SAME elevation rule a session does, and proves its
// factor in a browser through /auth/cli/elevate-authorization. It used to be
// admitted with no factor at all, which made possession of the credential
// file the whole of the check for the most consequential verb on this
// surface: a stolen file minted itself fresh admin-scoped credentials.
func TestAdminUserService_IssueAPITokenAdmitsAnElevatedBearer(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	// The admin's own admin-scoped credentials, as a browser consent would
	// have minted them. TWO of them, and each serves one case only: a
	// credential that authenticates once is cached, and the elevation the
	// store writes reaches this process through an eviction the production
	// leg performs and this helper cannot.
	owner := userid.MustNew(env.adminID)
	mint := func(name string) (tokenID, bearer string) {
		t.Helper()
		tokenID = id.Generate()
		secret := auth.MintAccessSecret()
		require.NoError(t, env.st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:         tokenID,
			UserID:     owner,
			ClientType: "cli",
			ClientName: name,
			SecretHash: env.validator.HashSecret(secret),
			AdminScope: true,
		}))
		return tokenID, auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	}
	issueAs := func(bearer string) (*connect.Response[leapmuxv1.IssueAPITokenResponse], error) {
		req := connect.NewRequest(&leapmuxv1.IssueAPITokenRequest{
			Username: "admin", ClientName: "ci-bot",
		})
		req.Header().Set("Authorization", "Bearer "+bearer)
		return env.client.IssueAPIToken(ctx, req)
	}

	// Unelevated, so the pass below measures the elevation and not the
	// absence of a gate. The refusal is MARKED: the CLI runs the step-up leg
	// and retries rather than reporting an error the user cannot clear.
	_, unverified := mint("operator-laptop")
	_, err := issueAs(unverified)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "1", connectErr.Meta().Get(service.ElevationRequiredHeader))

	actorID, verified := mint("operator-laptop-verified")
	hubtestutil.ElevateAPIToken(t, env.st, actorID, env.adminID)

	got, err := issueAs(verified)
	require.NoError(t, err, "a credential that proved a factor mints as a session does")
	assert.NotEmpty(t, got.Msg.GetTokenId())

	// The credential-epoch half of the pre-mint re-read still applies to a
	// bearer: it has no session row, but it has an owner whose credentials
	// can be revoked wholesale.
	assert.NotEmpty(t, got.Msg.GetAccessToken())
}

// The pre-mint authority re-read, on both of its refusal branches.
//
// Every other test on this verb exercises the ADMIT path, so the guard could
// be deleted, inverted, or turned from "refuse" into "continue" on a read
// failure and the suite would still pass. What it protects is not small: the
// gate above it answers from a CACHED UserInfo, and what the mint writes
// outlives the session by months.
func TestAdminUserService_IssueAPITokenRefusesWithdrawnAuthority(t *testing.T) {
	ctx := context.Background()

	// An administrator REVOKED the acting session. Absence alone cannot
	// separate that from the owner's own sign-out, so the distinct
	// session_revoked event is what the guard reads.
	t.Run("a revoked acting session", func(t *testing.T) {
		env := setupAdminUserTest(t)

		// One read FIRST, so the interceptor holds a validated UserInfo for
		// this cookie. Without it Revoke leaves no row, the interceptor
		// refuses the request itself, and the case passes with the handler's
		// re-read deleted -- it would measure the interceptor instead.
		_, err := env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{Id: env.adminID}, env.token))
		require.NoError(t, err)

		revoked, err := env.st.Sessions().Revoke(ctx, env.token)
		require.NoError(t, err)
		require.EqualValues(t, 1, revoked)

		_, err = env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
			Username: "admin", ClientName: "ci-bot",
		}, env.token))
		require.Error(t, err, "a mint must not land on a session an administrator took away")

		page, listErr := env.st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
			PageParams: service.NormalizePageParams("", service.MaxPageLimit),
		})
		require.NoError(t, listErr)
		assert.Empty(t, page.Rows, "a refused mint must leave no row behind")
	})

	// The owner's OWN sign-out is tolerated -- rolling back a change the user
	// legitimately started was a real regression once already -- so a plain
	// Delete must NOT refuse. This is the case a guard that keyed on row
	// absence alone would get wrong.
	t.Run("a plain sign-out is tolerated", func(t *testing.T) {
		env := setupAdminUserTest(t)

		// The same priming read, for the same reason: the interceptor must
		// serve the cached UserInfo, or the request never reaches the guard
		// this case is about.
		_, err := env.client.GetUser(ctx, authedReq(&leapmuxv1.GetUserRequest{Id: env.adminID}, env.token))
		require.NoError(t, err)

		deleted, err := env.st.Sessions().Delete(ctx, env.token)
		require.NoError(t, err)
		require.EqualValues(t, 1, deleted)

		// The interceptor still holds the validated UserInfo for its cache
		// TTL, which is exactly the staleness the guard exists to answer. A
		// plain Delete writes no session_revoked event, so the guard must
		// ADMIT the mint: rolling back a change the owner legitimately
		// started was a real regression once already.
		_, err = env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
			Username: "admin", ClientName: "ci-bot",
		}, env.token))
		require.NoError(t, err, "the owner's own sign-out must not refuse the change they started")
	})
}

// Creating DURABLE NEW AUTHORITY is one class, and all four verbs in it
// demand an elevated session.
//
// Restricting only IssueAPIToken recorded a property the hub did not have: a
// stolen admin bearer that could not renew itself past the one-year ceiling
// could instead CreateUser a fresh administrator with a password it chose,
// sign in through a browser, elevate with that password, and mint whatever it
// liked. The ceiling gained nothing while that route stayed open.
func TestAdminUserService_DurableAuthorityVerbsNeedAnElevatedSession(t *testing.T) {
	ctx := context.Background()

	for name, call := range map[string]func(*adminUserEnv, string) error{
		"CreateUser": func(env *adminUserEnv, token string) error {
			_, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
				Username: "planted", Password: "password123", IsAdmin: true,
			}, token))
			return err
		},
		"SetUserAdmin": func(env *adminUserEnv, token string) error {
			_, err := env.client.SetUserAdmin(ctx, authedReq(&leapmuxv1.SetUserAdminRequest{
				Id: env.adminID, IsAdmin: true,
			}, token))
			return err
		},
		"ResetPassword": func(env *adminUserEnv, token string) error {
			_, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
				Id: env.adminID, Password: "brand-new-adminpass",
			}, token))
			return err
		},
		"IssueAPIToken": func(env *adminUserEnv, token string) error {
			_, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
				Username: "admin", ClientName: "ci-bot",
			}, token))
			return err
		},
	} {
		t.Run(name+" refuses an un-elevated session", func(t *testing.T) {
			env := setupAdminUserTestUnelevated(t)
			err := call(env, env.token)
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, "1", connectErr.Meta().Get(service.ElevationRequiredHeader),
				"the refusal must be the one a step-up prompt can clear")
		})

		t.Run(name+" admits an elevated session", func(t *testing.T) {
			env := setupAdminUserTest(t)
			assert.NoError(t, call(env, env.token))
		})
	}

	// The THREE that need no headless path refuse a bearer outright, and the
	// refusal carries NO marker: a bearer has no row to stamp and nobody at a
	// keyboard, so a prompt would ask for a factor and refuse the retry for
	// the same reason. IssueAPIToken is the documented exception; the mint's
	// own clamp is what contains it.
	for name, call := range map[string]func(*adminUserEnv, string) error{
		"CreateUser": func(env *adminUserEnv, bearer string) error {
			_, err := env.client.CreateUser(ctx, adminBearerReq(&leapmuxv1.CreateUserRequest{
				Username: "planted-by-bearer", Password: "password123", IsAdmin: true,
			}, bearer))
			return err
		},
		"SetUserAdmin": func(env *adminUserEnv, bearer string) error {
			_, err := env.client.SetUserAdmin(ctx, adminBearerReq(&leapmuxv1.SetUserAdminRequest{
				Id: env.adminID, IsAdmin: true,
			}, bearer))
			return err
		},
		"ResetPassword": func(env *adminUserEnv, bearer string) error {
			_, err := env.client.ResetPassword(ctx, adminBearerReq(&leapmuxv1.ResetPasswordRequest{
				Id: env.adminID, Password: "brand-new-adminpass",
			}, bearer))
			return err
		},
	} {
		t.Run(name+" refuses a bearer", func(t *testing.T) {
			env := setupAdminUserTest(t)
			issued, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
				UserId: env.adminID, ClientName: "admin-cli", ClientType: "cli", AdminScope: true,
			}, env.token))
			require.NoError(t, err)

			err = call(env, issued.Msg.GetAccessToken())
			require.Error(t, err, "a bearer must not create durable new authority")
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Empty(t, connectErr.Meta().Get(service.ElevationRequiredHeader),
				"a bearer can never elevate, so the refusal must not offer a prompt")
		})
	}
}

// TestAdminUserService_CredentialGatedVerbsNeedAProvenFactor covers the other
// half of the gate on this service: the verbs that demand a recently proven
// factor from the ACTING CREDENTIAL, and admit an elevated command-line
// credential rather than refusing it.
//
// Both shipped with NO gate. DeleteUser is irreversible destruction of an
// account, every workspace it owns and every credential it holds; UpdateUser
// writes the account email, which is the address the public password-reset
// verb mails a link to -- so {email, email_verified:true} in one call handed
// over any account by the longer route while its sibling ResetPassword was
// restricted. The classification record in admin_procedures_internal_test.go
// states the decision; this observes the handler.
func TestAdminUserService_CredentialGatedVerbsNeedAProvenFactor(t *testing.T) {
	ctx := context.Background()

	// The verbs that take requireElevatedActor: an elevated bearer passes.
	for name, call := range map[string]func(*adminUserEnv, requestAuth) error{
		"DeleteUser": func(env *adminUserEnv, authorize requestAuth) error {
			_, err := env.client.DeleteUser(ctx, authorized(&leapmuxv1.DeleteUserRequest{
				Id: env.adminID, Force: true,
			}, authorize))
			return err
		},
		"UpdateUser display name": func(env *adminUserEnv, authorize requestAuth) error {
			_, err := env.client.UpdateUser(ctx, authorized(&leapmuxv1.UpdateUserRequest{
				Id: env.adminID, DisplayName: proto.String("Renamed"),
			}, authorize))
			return err
		},
	} {
		t.Run(name+" refuses an un-elevated session", func(t *testing.T) {
			env := setupAdminUserTestUnelevated(t)
			err := call(env, cookieAuth(env.token))
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, "1", connectErr.Meta().Get(service.ElevationRequiredHeader),
				"the refusal must be the one a step-up prompt can clear")
		})

		t.Run(name+" admits an elevated session", func(t *testing.T) {
			env := setupAdminUserTest(t)
			assert.NoError(t, call(env, cookieAuth(env.token)))
		})

		t.Run(name+" admits an elevated bearer", func(t *testing.T) {
			// `leapmux control admin user delete` and `... update
			// --display-name` are documented headless verbs, and neither
			// creates a new way INTO an account, so the strict session rule
			// would cost the CLI for no gain.
			env := setupAdminUserTest(t)
			issued, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
				UserId: env.adminID, ClientName: "admin-cli", ClientType: "cli", AdminScope: true,
			}, env.token))
			require.NoError(t, err)
			hubtestutil.ElevateAPIToken(t, env.st, issued.Msg.GetTokenId(), env.adminID)

			assert.NoError(t, call(env, bearerAuth(issued.Msg.GetAccessToken())))
		})
	}

	// The EMAIL fields take the strict session rule instead, because the
	// address is a recovery identity. A bearer is refused with no marker: it
	// has no row to stamp and nobody at a keyboard, so a prompt would collect a
	// factor and refuse the retry for the same reason.
	updateEmail := func(env *adminUserEnv, authorize requestAuth) error {
		_, err := env.client.UpdateUser(ctx, authorized(&leapmuxv1.UpdateUserRequest{
			Id: env.adminID, Email: proto.String("moved@example.test"), EmailVerified: proto.Bool(true),
		}, authorize))
		return err
	}

	t.Run("UpdateUser email refuses an un-elevated session", func(t *testing.T) {
		env := setupAdminUserTestUnelevated(t)
		err := updateEmail(env, cookieAuth(env.token))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

		user, err := env.st.Users().GetByID(ctx, env.adminID)
		require.NoError(t, err)
		assert.Equal(t, "admin@example.test", user.Email,
			"a refused update must leave the recovery address alone")
	})

	t.Run("UpdateUser email admits an elevated session", func(t *testing.T) {
		env := setupAdminUserTest(t)
		assert.NoError(t, updateEmail(env, cookieAuth(env.token)))
	})

	t.Run("UpdateUser email refuses a bearer", func(t *testing.T) {
		env := setupAdminUserTest(t)
		issued, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
			UserId: env.adminID, ClientName: "admin-cli", ClientType: "cli", AdminScope: true,
		}, env.token))
		require.NoError(t, err)
		hubtestutil.ElevateAPIToken(t, env.st, issued.Msg.GetTokenId(), env.adminID)

		err = updateEmail(env, bearerAuth(issued.Msg.GetAccessToken()))
		require.Error(t, err, "a bearer must not move a recovery identity")
		var connectErr *connect.Error
		require.ErrorAs(t, err, &connectErr)
		assert.Empty(t, connectErr.Meta().Get(service.ElevationRequiredHeader),
			"a bearer can never elevate, so the refusal must not offer a prompt")
	})
}

// A credential a BEARER mints cannot outlive its minter, and does not rotate.
//
// Both halves are needed. Without the inherited ceiling the child restarts the
// one-year lifetime from its own created_at; without dropping the refresh leg
// the first rotation recomputes every window from that same fresh created_at
// and un-clamps it. Together each generation is strictly shorter than the
// last, so a chain of self-issued credentials terminates at the browser
// consent that started it instead of running for ever.
func TestAdminUserService_IssueAPITokenClampsABearerMintedCredential(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	parent, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId: env.adminID, ClientName: "admin-cli", ClientType: "cli", AdminScope: true,
	}, env.token))
	require.NoError(t, err)

	// The minter has to have proved a factor, exactly as a session does. It
	// is elevated BEFORE its first request, because a credential that already
	// authenticated is cached with the deadline it had then (see
	// testutil.ElevateAPIToken).
	hubtestutil.ElevateAPIToken(t, env.st, parent.Msg.GetTokenId(), env.adminID)

	child, err := env.client.IssueAPIToken(ctx, adminBearerReq(&leapmuxv1.IssueAPITokenRequest{
		Username: "admin", ClientName: "self-issued", AdminScope: true,
	}, parent.Msg.GetAccessToken()))
	require.NoError(t, err, "the documented headless path still works")
	assert.Empty(t, child.Msg.GetRefreshToken(),
		"a bearer-minted credential does not rotate; a rotation would un-clamp its ceiling")

	parentRow, err := env.st.APITokens().GetByID(ctx, parent.Msg.GetTokenId())
	require.NoError(t, err)
	childRow, err := env.st.APITokens().GetByID(ctx, child.Msg.GetTokenId())
	require.NoError(t, err)
	require.NotNil(t, childRow.ExpiresAt)
	assert.Nil(t, childRow.RefreshExpiresAt)

	// The child dies no later than the minter's own ceiling.
	assert.False(t, childRow.ExpiresAt.After(parentRow.CreatedAt.Add(auth.AbsoluteTokenLifetime)),
		"the child inherits the minter's ceiling rather than restarting it")
}

// TestAdminUserService_CreateUserConflictIdentifiesOnlyTheSuppliedField pins
// the conflict message. The dialect layer cannot say which unique index
// fired, so the handler -- which knows what it sent -- identifies it.
// Blaming both unconditionally produced `username "bob" or email "" is
// already taken` for a create with no email at all.
func TestAdminUserService_CreateUserConflictIdentifiesOnlyTheSuppliedField(t *testing.T) {
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

// TestUserConflictErrorIdentifiesOnlyTheSuppliedFields pins the conflict
// classifier's four cases. It runs only when a unique index fires on a lost
// race, so the RPC pre-checks answer first and the cases are unreachable
// from the client side.
//
// The dialect layer reports every duplicate as one opaque conflict, so the
// caller -- which knows what it sent -- identifies it. Blaming both
// unconditionally produced `username "bob" or email "" is already taken`
// for a create with no email at all.
func TestUserConflictErrorIdentifiesOnlyTheSuppliedFields(t *testing.T) {
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
	// The response echoes the subject back whichever handle the caller sent,
	// so a caller holding only one of the two learns the other.
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
// This test asserts both halves. The DURABLE half is the store: sessions
// gone, bearer rows revoked, auth_generation advanced. The IN-PROCESS half is
// the lifecycle effect, which is the only thing that evicts this hub's own
// caches and channels before the revocation watcher's next sweep — up to two
// seconds later, during which the old credential would still be served.
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

	// Not irreversible: the caller chose the new password and logs in again
	// with it. This is what makes the missing force flag correct.
	_, _, _, err = auth.Login(ctx, env.st, "admin", "brand-new-adminpass", auth.DefaultSessionDuration)
	assert.NoError(t, err)
}

// TestAdminUserService_ResetPassword_KillsTheAccountsAPITokens is the same
// teardown contract for the OTHER credential an administrator can hold.
//
// A reset revokes every API token the account holds, and the count it reports
// includes them. What CHANGED is who may ask: setting a password without the
// old one is creating durable new authority, so it now needs an elevated
// SESSION and a bearer is refused outright -- see
// requireElevatedSessionForDurableAuthority. The refusal is pinned first,
// because it is the property a later change is most likely to lose.
func TestAdminUserService_ResetPassword_KillsTheAccountsAPITokens(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	// AdminScope, because the bearer must reach an Admin* procedure at all:
	// an ordinary CLI credential is refused there even for an administrator.
	issued, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		UserId: env.adminID, ClientName: "admin-cli", ClientType: "cli", AdminScope: true,
	}, env.token))
	require.NoError(t, err)
	bearer := issued.Msg.GetAccessToken()

	// The bearer reaches the service -- so the refusal below is this verb's
	// own rule and not the admin gate answering first.
	_, err = env.client.GetUser(ctx, adminBearerReq(&leapmuxv1.GetUserRequest{Id: env.adminID}, bearer))
	require.NoError(t, err)

	_, err = env.client.ResetPassword(ctx, adminBearerReq(&leapmuxv1.ResetPasswordRequest{
		Id: env.adminID, Password: "brand-new-adminpass",
	}, bearer))
	require.Error(t, err, "a bearer must not set a password it does not know")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// From the elevated session it lands, and it takes the bearer with it.
	resp, err := env.client.ResetPassword(ctx, authedReq(&leapmuxv1.ResetPasswordRequest{
		Id: env.adminID, Password: "brand-new-adminpass",
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.Msg.GetApiTokensRevoked(),
		"the account's token is counted among the revoked, not exempted from them")

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

// Promoting somebody does not confirm their address, so it does not raise
// the flag either. The privilege and the fact are separate.
func TestAdminUserService_SetUserAdmin_DoesNotMarkEmailVerified(t *testing.T) {
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
	assert.False(t, resp.Msg.GetUser().GetEmailVerified(),
		"nobody confirmed the address, so the promotion must not claim they did")

	row, err := env.st.Users().GetByID(ctx, created.Msg.GetUser().GetId())
	require.NoError(t, err)
	assert.False(t, row.EmailVerified)
	// The new administrator can still use the hub.
	assert.True(t, auth.EmailVerificationFactsFromUser(row).Satisfied(true))
}

// Creating an administrator does not confirm their address either: the
// operator's own EmailVerified is what lands.
func TestAdminUserService_CreateUser_AdminDoesNotForceEmailVerified(t *testing.T) {
	env := setupAdminUserTest(t)
	ctx := context.Background()

	resp, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "newadmin", Password: "password123", DisplayName: "New Admin",
		Email: "newadmin@example.com", EmailVerified: false, IsAdmin: true,
	}, env.token))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
	assert.False(t, resp.Msg.GetUser().GetEmailVerified())

	// An operator who really confirmed it says so, and that lands too.
	confirmed, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "knownadmin", Password: "password123", DisplayName: "Known Admin",
		Email: "knownadmin@example.com", EmailVerified: true, IsAdmin: true,
	}, env.token))
	require.NoError(t, err)
	assert.True(t, confirmed.Msg.GetUser().GetEmailVerified())
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

// TestAdminUserService_AddressChangeLowersEmailVerified pins that a new
// address arrives unverified.
//
// Carrying the old flag across marked an address nobody confirmed as
// verified. That is not cosmetic: a verified address is a valid
// self-service password-reset target, so the carry handed the account's
// recovery route to whatever address the request carried. The change must
// also FENCE, because lowering email_verified reduces the user's auth gate.
func TestAdminUserService_AddressChangeLowersEmailVerified(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "mover", Password: "userpass123", Email: "mover@example.com", EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	userID := created.Msg.GetUser().GetId()

	before, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	require.True(t, before.EmailVerified)

	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id:    userID,
		Email: proto.String("moved@example.com"),
	}, env.token))
	require.NoError(t, err)

	after, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "moved@example.com", after.Email)
	assert.False(t, after.EmailVerified,
		"an address nobody confirmed must not inherit the old address's verification")
	assert.Greater(t, after.AuthGeneration, before.AuthGeneration,
		"the implied lowering must run through the fenced verb, not the unfenced address write")
}

// TestAdminUserService_AddressChangeKeepsAnExplicitVerification lets the
// same request raise the flag, so an admin who genuinely confirmed the new
// address out of band is not forced through a second call.
func TestAdminUserService_AddressChangeKeepsAnExplicitVerification(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "explicit", Password: "userpass123", Email: "explicit@example.com", EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	userID := created.Msg.GetUser().GetId()

	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id:            userID,
		Email:         proto.String("confirmed@example.com"),
		EmailVerified: proto.Bool(true),
	}, env.token))
	require.NoError(t, err)

	after, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "confirmed@example.com", after.Email)
	assert.True(t, after.EmailVerified, "an explicit verification in the same request wins")
}

// TestAdminUserService_AdminAddressChangeIsUnverified pins that an
// administrator has NO exception, which is the recovery-route fix.
//
// email_verified records whether somebody confirmed the address. An
// administrator's brand-new address is exactly as unconfirmed as anybody
// else's, and a verified address is a valid self-service password-reset
// target -- so keeping the flag across the move handed the highest-privilege
// accounts on the hub a live reset route to whatever address the request
// carried.
//
// Nothing locks the administrator out: the login gate takes its own
// exemption (auth.EmailVerificationFacts.Satisfied), which is the same derivation
// at the right altitude.
func TestAdminUserService_AdminAddressChangeIsUnverified(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "otheradmin", Password: "userpass123", Email: "otheradmin@example.com",
		IsAdmin: true, EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	userID := created.Msg.GetUser().GetId()
	before, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	require.True(t, before.EmailVerified, "precondition: the original address was confirmed")

	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id:    userID,
		Email: proto.String("newadmin@example.com"),
	}, env.token))
	require.NoError(t, err)

	after, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "newadmin@example.com", after.Email)
	assert.False(t, after.EmailVerified, "nobody confirmed the new address, administrator or not")
	// And the administrator can still use the hub, which is what the forced
	// column used to provide.
	assert.True(t, auth.EmailVerificationFactsFromUser(after).Satisfied(true))
}

// TestAdminUserService_ClearingEmailKeepsVerified pins the exclusion that
// stops this rule from stranding an account.
//
// Lowering the flag on a CLEARED address would mint exactly the state the
// guard in UpdateUser refuses: an email-less unverified account on a hub
// that requires verification, which lands on /verify-email with no code
// that can ever arrive. There is no new address for anybody to confirm, so
// there is nothing to distrust.
func TestAdminUserService_ClearingEmailKeepsVerified(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "clearer", Password: "userpass123", Email: "clearer@example.com", EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	userID := created.Msg.GetUser().GetId()

	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id:    userID,
		Email: proto.String(""),
	}, env.token))
	require.NoError(t, err)

	after, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, after.Email)
	assert.True(t, after.EmailVerified,
		"clearing an address must not strand the account behind a verification it cannot reach")
}

// TestAdminUserService_RewritingTheSameAddressKeepsVerified pins the other
// exclusion. A rewrite that differs only in letter case is the same
// confirmed address, and NormalizeEmail is the folding the write path uses,
// so the confirmation still holds.
func TestAdminUserService_RewritingTheSameAddressKeepsVerified(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "samesame", Password: "userpass123", Email: "samesame@example.com", EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	userID := created.Msg.GetUser().GetId()

	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id:    userID,
		Email: proto.String("SameSame@Example.com"),
	}, env.token))
	require.NoError(t, err)

	after, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "samesame@example.com", after.Email)
	assert.True(t, after.EmailVerified,
		"a case-only rewrite is the same address, so its confirmation stands")
}

// TestAdminUserService_SettingAFirstEmailIsUnverified covers the branch
// where the account had no address at all.
//
// The account starts VERIFIED with no address, which is the state
// SetUserAdmin's demotion repair exists for and which CreateUser accepts
// directly. Starting it unverified would assert nothing: the flag would be
// false before and after, so the test passed with the whole rule reverted.
// An address arriving for the first time is unconfirmed,
// exactly like one that replaces another, so it must not land verified --
// and a verified address is a valid self-service password-reset target.
func TestAdminUserService_SettingAFirstEmailIsUnverified(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "noemail", Password: "userpass123", EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	userID := created.Msg.GetUser().GetId()

	before, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	require.Empty(t, before.Email)
	require.True(t, before.EmailVerified, "precondition: the flag is raised before the address arrives")

	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id:    userID,
		Email: proto.String("first@example.com"),
	}, env.token))
	require.NoError(t, err)

	after, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "first@example.com", after.Email)
	assert.False(t, after.EmailVerified,
		"a first address nobody confirmed must not inherit the raised flag")
	assert.Greater(t, after.AuthGeneration, before.AuthGeneration,
		"the implied lowering must run through the fenced verb, not the unfenced address write")
}

// TestAdminUserService_ClearingEmailAndLoweringVerifiedTogetherIsRefused
// pins the guard against the value the SAME request resolves.
//
// The guard reads whether the account will be able to sign in AFTER this
// edit, and it read the flag ALREADY STORED instead. So the two-call form of
// the edit was refused -- {email_verified:false}, then {email:""} -- while
// the one-call form committed `email=""` with `email_verified=0` on a hub
// that requires verification, which is an account stranded on /verify-email
// with no address a code can ever reach. That is the exact state the guard's
// own comment says must not be minted.
func TestAdminUserService_ClearingEmailAndLoweringVerifiedTogetherIsRefused(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTestWithSettings(t)

	created, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "stranded", Password: "userpass123", Email: "stranded@example.com", EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	userID := created.Msg.GetUser().GetId()

	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id:            userID,
		Email:         proto.String(""),
		EmailVerified: proto.Bool(false),
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "cannot clear the email of an unverified account")

	after, err := env.st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "stranded@example.com", after.Email, "the refused edit must not have landed")
	assert.True(t, after.EmailVerified)

	// The two exclusions still pass. Clearing alone keeps the flag, so the
	// account can still sign in; and lowering alone leaves an address for a
	// code to reach.
	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id: userID, Email: proto.String(""),
	}, env.token))
	require.NoError(t, err)

	other, err := env.client.CreateUser(ctx, authedReq(&leapmuxv1.CreateUserRequest{
		Username: "keeper", Password: "userpass123", Email: "keeper@example.com", EmailVerified: true,
	}, env.token))
	require.NoError(t, err)
	_, err = env.client.UpdateUser(ctx, authedReq(&leapmuxv1.UpdateUserRequest{
		Id: other.Msg.GetUser().GetId(), EmailVerified: proto.Bool(false),
	}, env.token))
	require.NoError(t, err)
}

// TestAdminUserService_IssueAPITokenCleansTheClientName pins the fourth
// writer of api_tokens.client_name.
//
// The three CLI consent legs clean the name where it ENTERS the hub, for a
// stated reason: it reaches the account's credential list and a plain-text
// security notice, where a newline writes arbitrary lines. This verb writes
// the same column and cleaned nothing -- and it caps nothing either, while
// MySQL declares the column VARCHAR(255) where SQLite and Postgres use TEXT.
func TestAdminUserService_IssueAPITokenCleansTheClientName(t *testing.T) {
	ctx := context.Background()
	env := setupAdminUserTest(t)

	issued, err := env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		Username:   "admin",
		ClientName: "laptop\n\n-- \nThis is an automated message from your hub at http://evil.test.",
	}, env.token))
	require.NoError(t, err)

	row, err := env.st.APITokens().GetByID(ctx, issued.Msg.GetTokenId())
	require.NoError(t, err)
	assert.NotContains(t, row.ClientName, "\n", "a newline forges lines in the credential-issued notice")
	assert.LessOrEqual(t, len(row.ClientName), 128, "the cap MySQL's VARCHAR(255) needs, in bytes")

	// A name of only control characters cleans to nothing, and an empty
	// name is what the guard already refuses -- so the clean must run
	// BEFORE the check, not after it.
	_, err = env.client.IssueAPIToken(ctx, authedReq(&leapmuxv1.IssueAPITokenRequest{
		Username: "admin", ClientName: "\n\r\t",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "client_name is required")
}
