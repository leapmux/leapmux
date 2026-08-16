package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
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
// `lmx_<id>_<secret>` Authorization header. The delegation-narrowing
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
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(servicetest.AuthServiceDeps(st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))), opts)
	mux.Handle(authPath, authHandler)

	channelSvc := service.NewChannelService(st, wMgr, cMgr, pendingReqs, allowAllAuthFreshness{})
	channelPath, channelHandler := leapmuxv1connect.NewChannelServiceHandler(channelSvc, opts)
	mux.Handle(channelPath, channelHandler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &bearerChannelEnv{
		channelClient: leapmuxv1connect.NewChannelServiceClient(srv.Client(), srv.URL),
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

// seedUserWorkspaceWorker creates a user, two workspaces both
// accessible to that user, and a worker the user owns. Returns ids
// the test will use to mint a delegation bearer scoped to ws-A.
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
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(userID),
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	return userID, wsA, wsB, workerID
}

// mintDelegation seeds a delegation_tokens row directly so the test
// has a known (token_id, secret) pair to authenticate with. Bypasses
// the worker mint endpoint because the tab/workspace plumbing for
// that handler is exercised in worker_delegation_handler_test.go.
func (e *bearerChannelEnv) mintDelegation(t *testing.T, userID, workerID, workspaceID string) (bearer, tokenID string) {
	t.Helper()
	tokenID = id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, e.store.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		WorkerID:         workerID,
		IssuedForTabID:   "tab-x",
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       e.validator.HashSecret(secret),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))
	return auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret), tokenID
}

// openMemoryStore is local to these delegation tests so the bearer
// env doesn't depend on the cookie env's setup.
func openMemoryStore(t *testing.T) store.Store {
	t.Helper()
	return hubtestutil.OpenTestStore(t)
}

// captureWorker registers a fake online worker that records every
// ConnectResponse the hub sends so the test can inspect what the hub pushed to
// it.
func (e *bearerChannelEnv) captureWorker(t *testing.T, workerID string) (chan *leapmuxv1.ConnectResponse, *workermgr.Conn) {
	t.Helper()
	ch := make(chan *leapmuxv1.ConnectResponse, 8)
	conn := workermgrtest.NewConnWithWrite(t, workerID, func(msg *leapmuxv1.ConnectResponse) error {
		ch <- msg
		return nil
	})
	_, _ = e.workerMgr.Register(conn)
	return ch, conn
}

// A delegation token minted by ATTACKER's worker must not open a channel to the
// VICTIM's own worker, even though the token legitimately carries the victim's
// identity and the victim owns that worker.
//
// This is the cross-tenant chain the worker-scope check closes. A worker mints
// tokens for whichever user its tab was spawned for, so an attacker who shares a
// workspace with a victim (sharing needs no consent) gets their worker to mint a
// bearer authenticating as the VICTIM. WorkerCanUse then waves it through to the
// victim's own worker -- the victim registered it -- and the worker sees
// sess.UserID == victim, so requireWorkerOwner passes and hands the attacker
// tunnels and files on the victim's machine.
// seedCrossTenantDelegation builds the cross-tenant chain the worker-scope check
// closes and returns the victim's worker id, a workspace of the victim's, plus a
// bearer minted by the ATTACKER's worker that authenticates as the VICTIM. The
// workspace is returned because it is readable to the token's user by design --
// a delegation bearer carries its owner's reach across every workspace they own,
// so the worker bound is the only thing left that can refuse this channel.
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
		ID:              attackerWorkerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(attackerID),
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	bearer, _ = e.mintDelegation(t, victimID, attackerWorkerID, victimWS)
	return victimWorkerID, victimWS, bearer
}

func TestOpenChannel_DelegationCannotReachAnotherUsersWorker(t *testing.T) {
	t.Parallel()

	env := setupBearerChannelEnv(t)
	victimWorkerID, _, bearer := env.seedCrossTenantDelegation(t)

	req := connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         victimWorkerID,
		HandshakePayload: []byte("hs1"),
	})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, err := env.channelClient.OpenChannel(context.Background(), req)

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

	req := connect.NewRequest(&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: victimWorkerID})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, err := env.channelClient.GetWorkerHandshakeParams(context.Background(), req)

	require.Error(t, err, "a token minted by another user's worker must not read the victim worker's keys")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// ...and the ordinary case still works: a token may read the handshake params of the
// worker that minted it (the common `leapmux control` path).
func TestGetWorkerHandshakeParams_DelegationReachesItsMintingWorker(t *testing.T) {
	t.Parallel()

	env := setupBearerChannelEnv(t)
	userID, wsA, _, workerID := env.seedUserWorkspaceWorker(t)
	bearer, _ := env.mintDelegation(t, userID, workerID, wsA)

	req := connect.NewRequest(&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, err := env.channelClient.GetWorkerHandshakeParams(context.Background(), req)

	assert.NotEqual(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"the minting worker's own params must remain readable")
}

// The same token MUST still open a channel back to the worker that minted it --
// the ordinary `leapmux control` case, where an agent talks to its own host.
func TestOpenChannel_DelegationReachesItsMintingWorker(t *testing.T) {
	t.Parallel()

	env := setupBearerChannelEnv(t)
	userID, wsA, _, workerID := env.seedUserWorkspaceWorker(t)

	bearer, _ := env.mintDelegation(t, userID, workerID, wsA)
	sent, _ := env.captureWorker(t, workerID)

	shortCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         workerID,
		HandshakePayload: []byte("hs1"),
	})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, err := env.channelClient.OpenChannel(shortCtx, req)
	require.Error(t, err, "no fake worker response → expected timeout, not a permission denial")
	assert.NotEqual(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"the minting worker must remain reachable")

	select {
	case msg := <-sent:
		require.NotNil(t, msg.GetChannelOpen(), "the open must reach the minting worker")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected ChannelOpen to be sent to the minting worker")
	}
}

func TestCloseChannel_DelegationRequiresSameBearerScope(t *testing.T) {
	t.Parallel()

	env := setupBearerChannelEnv(t)
	userID, wsA, _, workerID := env.seedUserWorkspaceWorker(t)
	_, tokenID := env.mintDelegation(t, userID, workerID, wsA)

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

	_, err := env.channelSvc.CloseChannel(ctx, connect.NewRequest(&leapmuxv1.CloseChannelRequest{ChannelId: cookieChannelID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.True(t, env.channelMgr.Exists(cookieChannelID), "delegation caller must not close unrestricted same-user channel")

	_, err = env.channelSvc.CloseChannel(ctx, connect.NewRequest(&leapmuxv1.CloseChannelRequest{ChannelId: otherDelegationChannelID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.True(t, env.channelMgr.Exists(otherDelegationChannelID), "delegation caller must not close another delegation token's channel")

	_, err = env.channelSvc.CloseChannel(ctx, connect.NewRequest(&leapmuxv1.CloseChannelRequest{ChannelId: scopedChannelID}))
	require.NoError(t, err)
	assert.False(t, env.channelMgr.Exists(scopedChannelID), "matching delegation channel must close")
}
