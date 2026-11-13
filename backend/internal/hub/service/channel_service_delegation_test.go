package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

type bearerChannelEnv struct {
	channelClient leapmuxv1connect.ChannelServiceClient
	store         store.Store
	workerMgr     *workermgr.Manager
	channelMgr    *channelmgr.Manager
	pending       *workermgr.PendingRequests
	channelSvc    *service.ChannelService
	validator     *auth.TokenValidator
	cache         *auth.AuthContextRegistry
	server        *httptest.Server
}

// setupBearerChannelEnv stands up a hub with bearer-token interceptor
// wired in, so OpenChannel calls can be authenticated by an
// `Authorization: Bearer lmx_d<id>_<secret>` header. The delegation-narrowing
// tests need this — the cookie-only env in channel_service_test.go
// doesn't exercise the bearer path the plan added.
func setupBearerChannelEnv(t *testing.T) *bearerChannelEnv {
	t.Helper()

	st := openMemoryStore(t)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	cMgr := channelmgr.New(0)
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return settings.DefaultTimeouts.APITimeout() })

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{SessionHasher: hubtestutil.TestSecretHasher(), Store: st, TokenValidator: tv})
	t.Cleanup(sc.Stop)
	connectServer := connect.NewServer(interceptor)

	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, service.NewAuthService(servicetest.AuthServiceDeps(st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))))

	channelSvc := service.NewChannelService(st, wMgr, cMgr, pendingReqs, allowAllAuthFreshness{})
	leapmuxv1connect.RegisterChannelServiceHandler(connectServer, channelSvc)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &bearerChannelEnv{
		channelClient: leapmuxv1connect.NewChannelServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL, connecthttp.WithReadMaxBytes(0)))),
		store:         st,
		workerMgr:     wMgr,
		channelMgr:    cMgr,
		pending:       pendingReqs,
		channelSvc:    channelSvc,
		validator:     tv,
		cache:         sc,
		server:        srv,
	}
}

// seedUserWorkspaceWorker creates a user, two workspaces the user owns, and
// a Worker the user owns. It returns IDs for a delegation bearer that carries
// the user identity and minting Worker, without a workspace restriction.
func (e *bearerChannelEnv) seedUserWorkspaceWorker(t *testing.T) (userID, wsA, wsB, workerID string) {
	t.Helper()
	ctx := context.Background()

	userID = id.Generate()
	require.NoError(t, e.store.Users().Create(ctx, store.CreateUserParams{
		ID:       userID,
		Username: "delegation-user-" + id.Generate()[:6],
	}))

	wsA = id.Generate()
	require.NoError(t, e.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: wsA, OwnerUserID: userid.MustNew(userID), Title: "ws-A",
	}))
	wsB = id.Generate()
	require.NoError(t, e.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: wsB, OwnerUserID: userid.MustNew(userID), Title: "ws-B",
	}))

	workerID = id.Generate()
	require.NoError(t, e.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID:            workerID,
		AuthTokenHash: hubtestutil.TestSecretHasher().Hash(id.Generate()),
		RegisteredBy:  userid.MustNew(userID),
		PublicKeys:    storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
	}))

	return userID, wsA, wsB, workerID
}

// mintDelegation seeds a delegation_tokens row directly so the test has a known
// (token_id, secret) pair to authenticate with. It bypasses the mint endpoint's
// registrant and tab-ownership checks, which worker_delegation_handler_test.go
// exercises. This bypass permits the deliberately invalid cross-tenant fixture.
func (e *bearerChannelEnv) mintDelegation(t *testing.T, userID, workerID string) (bearer, tokenID string) {
	t.Helper()
	tokenID = id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, e.store.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            tokenID,
		UserID:        userid.MustNew(userID),
		WorkerID:      workerID,
		SecretHash:    e.validator.HashSecret(secret),
		ExpiresAt:     time.Now().Add(time.Hour),
	}))
	return auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret), tokenID
}

// openMemoryStore is local to these delegation tests so the bearer
// env doesn't depend on the cookie env's setup.
func openMemoryStore(t *testing.T) store.Store {
	t.Helper()
	return hubtestutil.OpenTestStore(t)
}

// seedCrossTenantDelegation directly inserts a deliberately invalid delegation
// row that authenticates as the victim but records the attacker's Worker as
// its minter. Production issuance rejects that combination before checking tab
// ownership. The fixture verifies defense in depth in both channel entry points.
//
// It returns the victim's Worker ID and workspace, plus the synthetic bearer.
// The victim owns the target workspace and Worker, so ordinary user ownership
// checks permit that target. Delegation Worker scope must still refuse it.
func (e *bearerChannelEnv) seedCrossTenantDelegation(t *testing.T) (victimWorkerID, victimWS, bearer string) {
	t.Helper()
	ctx := context.Background()

	// The victim, with a workspace and their own worker.
	victimID, victimWS, _, victimWorkerID := e.seedUserWorkspaceWorker(t)

	// The attacker owns a separate worker.
	attackerID := id.Generate()
	require.NoError(t, e.store.Users().Create(ctx, store.CreateUserParams{
		ID:       attackerID,
		Username: "attacker-" + id.Generate()[:6],
	}))
	attackerWorkerID := id.Generate()
	require.NoError(t, e.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID:            attackerWorkerID,
		AuthTokenHash: hubtestutil.TestSecretHasher().Hash(id.Generate()),
		RegisteredBy:  userid.MustNew(attackerID),
		PublicKeys:    storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
	}))

	bearer, _ = e.mintDelegation(t, victimID, attackerWorkerID)
	return victimWorkerID, victimWS, bearer
}

func TestOpenChannel_DelegationCannotReachAnotherUsersWorker(t *testing.T) {
	t.Parallel()

	env := setupBearerChannelEnv(t)
	victimWorkerID, _, bearer := env.seedCrossTenantDelegation(t)

	req := &leapmuxv1.OpenChannelRequest{
		WorkerId:         victimWorkerID,
		HandshakePayload: []byte("hs1"),
	}
	ctx, info := connect.NewClientContext(context.Background())
	info.RequestHeader().Set("Authorization", "Bearer "+bearer)
	_, err := env.channelClient.OpenChannel(ctx, req)

	require.Error(t, err, "a token minted by another user's worker must not reach the victim's worker")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// The SAME cross-tenant token must not reach the victim's worker through
// GetWorkerHandshakeParams either.
//
// It asks the identical question OpenChannel does -- "may this principal reach this
// worker" -- and answers it with the same AuthorizeWorkerReach call. While the minter
// bound was bolted onto OpenChannel alone, this entrypoint still handed a
// cross-tenant bearer the victim worker's key bundle, its live encryption mode, and
// (via the offline/unavailable split) an online oracle. The bound lives in
// WorkerReachAuthorizer so both callers -- and the next one -- inherit it.
func TestGetWorkerHandshakeParams_DelegationCannotReachAnotherUsersWorker(t *testing.T) {
	t.Parallel()

	env := setupBearerChannelEnv(t)
	victimWorkerID, _, bearer := env.seedCrossTenantDelegation(t)

	req := &leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: victimWorkerID}
	ctx, info := connect.NewClientContext(context.Background())
	info.RequestHeader().Set("Authorization", "Bearer "+bearer)
	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, req)

	require.Error(t, err, "a token minted by another user's worker must not read the victim worker's keys")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// ...and the ordinary case still works: a token may read the handshake params of the
// worker that minted it (the common `leapmux control` path).
func TestGetWorkerHandshakeParams_DelegationReachesItsMintingWorker(t *testing.T) {
	t.Parallel()
	env := setupBearerChannelEnv(t)
	userID, _, _, workerID := env.seedUserWorkspaceWorker(t)
	bearer, _ := env.mintDelegation(t, userID, workerID)
	hello := workermgrtest.TestHello()
	conn := workermgrtest.NewConnWithHello(t, workerID, hello, func(*leapmuxv1.ConnectResponse) error { return nil })
	requireWorkerConnectionRegistration(t, env.workerMgr, conn)
	ctx, info := connect.NewClientContext(testutil.DeadlineContext(t))
	info.RequestHeader().Set("Authorization", "Bearer "+bearer)
	response, err := env.channelClient.GetWorkerHandshakeParams(ctx, &leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID})
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.True(t, proto.Equal(hello.GetKeys(), response.GetKeys()))
	assert.Equal(t, hello.GetEncryptionMode(), response.GetEncryptionMode())
}

// The same token MUST still open a channel back to the worker that minted it --
// the ordinary `leapmux control` case, where an agent talks to its own host.
func TestOpenChannel_DelegationReachesItsMintingWorker(t *testing.T) {
	t.Parallel()
	env := setupBearerChannelEnv(t)
	userID, _, _, workerID := env.seedUserWorkspaceWorker(t)
	bearer, tokenID := env.mintDelegation(t, userID, workerID)
	ctx, info := connect.NewClientContext(testutil.DeadlineContext(t))
	info.RequestHeader().Set("Authorization", "Bearer "+bearer)
	sent := make(chan *leapmuxv1.ConnectResponse, 4)
	completed := make(chan bool, 1)
	conn := workermgrtest.NewConnWithWrite(t, workerID, func(msg *leapmuxv1.ConnectResponse) error {
		sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			completed <- env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
					ChannelId: open.GetChannelId(), HandshakePayload: []byte("worker-handshake"), MaxMessageSize: open.GetMaxMessageSize(),
				}},
			})
		}
		return nil
	})
	requireWorkerConnectionRegistration(t, env.workerMgr, conn)
	response, err := env.channelClient.OpenChannel(ctx, &leapmuxv1.OpenChannelRequest{WorkerId: workerID, HandshakePayload: []byte("hs1")})
	require.NoError(t, err)
	require.NotNil(t, response)
	select {
	case ok := <-completed:
		require.True(t, ok)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var msg *leapmuxv1.ConnectResponse
	select {
	case msg = <-sent:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	open := msg.GetChannelOpen()
	require.NotNil(t, open)
	assert.NotEmpty(t, msg.GetRequestId())
	assert.NotEmpty(t, open.GetChannelId())
	assert.Equal(t, userID, open.GetUserId())
	assert.Equal(t, []byte("hs1"), open.GetHandshakePayload())
	resolved, err := env.validator.ValidateBearer(ctx, bearer)
	require.NoError(t, err)
	assert.ElementsMatch(t, authscope.ScopesToWire(resolved.Scopes), open.GetGrantedScopes())
	assert.EqualValues(t, env.channelMgr.MaxMessageSize(), open.GetMaxMessageSize())
	assert.Equal(t, open.GetChannelId(), response.GetChannelId())
	assert.Equal(t, userID, response.GetUserId())
	assert.Equal(t, []byte("worker-handshake"), response.GetHandshakePayload())
	assert.Equal(t, open.GetMaxMessageSize(), response.GetMaxMessageSize())
	channel, exists := env.channelMgr.GetChannelInfo(response.GetChannelId())
	require.True(t, exists)
	assert.Equal(t, workerID, channel.WorkerID)
	assert.Equal(t, userID, channel.UserID)
	assert.Equal(t, auth.DelegationCredential(tokenID, workerID), channel.AuthInfo.Credential)
	_, err = env.channelClient.CloseChannel(ctx, &leapmuxv1.CloseChannelRequest{ChannelId: response.GetChannelId()})
	require.NoError(t, err)
	assert.False(t, env.channelMgr.Exists(response.GetChannelId()))
	select {
	case closeFrame := <-sent:
		require.NotNil(t, closeFrame.GetChannelClose())
		assert.Equal(t, response.GetChannelId(), closeFrame.GetChannelClose().GetChannelId())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestCloseChannel_DelegationRequiresSameBearerScope(t *testing.T) {
	t.Parallel()

	env := setupBearerChannelEnv(t)
	userID, _, _, workerID := env.seedUserWorkspaceWorker(t)
	_, tokenID := env.mintDelegation(t, userID, workerID)

	cookieChannelID := id.Generate()
	otherDelegationChannelID := id.Generate()
	scopedChannelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(cookieChannelID, workerID, userID, channelmgr.AuthInfo{}, nil)
	env.channelMgr.RegisterWithAuthInfo(otherDelegationChannelID, workerID, userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential("other-token", "worker-mint"),
	}, nil)
	env.channelMgr.RegisterWithAuthInfo(scopedChannelID, workerID, userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential(tokenID, "worker-mint"),
	}, nil)

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userid.MustNew(userID),
		Credential: auth.DelegationCredential(tokenID, "worker-mint"),
	})

	_, err := env.channelSvc.CloseChannel(ctx, &leapmuxv1.CloseChannelRequest{ChannelId: cookieChannelID})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.True(t, env.channelMgr.Exists(cookieChannelID), "delegation caller must not close unrestricted same-user channel")

	_, err = env.channelSvc.CloseChannel(ctx, &leapmuxv1.CloseChannelRequest{ChannelId: otherDelegationChannelID})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.True(t, env.channelMgr.Exists(otherDelegationChannelID), "delegation caller must not close another delegation token's channel")

	_, err = env.channelSvc.CloseChannel(ctx, &leapmuxv1.CloseChannelRequest{ChannelId: scopedChannelID})
	require.NoError(t, err)
	assert.False(t, env.channelMgr.Exists(scopedChannelID), "matching delegation channel must close")
}
