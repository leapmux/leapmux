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
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
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

	wMgr := workermgr.New()
	cMgr := channelmgr.New()
	pendingReqs := workermgr.NewPendingRequests(testConfig().APITimeout)

	mux := http.NewServeMux()
	interceptor, sc := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(st, testConfig(), auth.NewCredentialLifecycleEffects(nil, nil, nil), nil, mail.NewStubSender(), mail.Renderer{}), opts)
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
func (e *bearerChannelEnv) seedUserWorkspaceWorker(t *testing.T) (userID, orgID, wsA, wsB, workerID string) {
	t.Helper()
	ctx := context.Background()

	orgID = id.Generate()
	require.NoError(t, e.store.Orgs().Create(ctx, store.CreateOrgParams{
		ID: orgID, Name: "test-org",
	}))

	userID = id.Generate()
	require.NoError(t, e.store.Users().Create(ctx, store.CreateUserParams{
		ID:       userID,
		OrgID:    orgID,
		Username: "delegation-user-" + id.Generate()[:6],
	}))

	wsA = id.Generate()
	require.NoError(t, e.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: wsA, OrgID: orgID, OwnerUserID: userID, Title: "ws-A",
	}))
	wsB = id.Generate()
	require.NoError(t, e.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: wsB, OrgID: orgID, OwnerUserID: userID, Title: "ws-B",
	}))

	workerID = id.Generate()
	require.NoError(t, e.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userID,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	return userID, orgID, wsA, wsB, workerID
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
		UserID:           userID,
		WorkerID:         workerID,
		WorkspaceID:      workspaceID,
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
// ConnectResponse the hub sends so the test can inspect what
// AccessibleWorkspaceIds were actually announced.
func (e *bearerChannelEnv) captureWorker(t *testing.T, workerID string) (chan *leapmuxv1.ConnectResponse, *workermgr.Conn) {
	t.Helper()
	ch := make(chan *leapmuxv1.ConnectResponse, 8)
	conn := &workermgr.Conn{
		WorkerID: workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			ch <- msg
			return nil
		},
	}
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
// closes and returns the victim's worker id, the workspace the bearer is pinned to,
// plus a bearer minted by the ATTACKER's worker that authenticates as the VICTIM --
// what a shared-workspace tab legitimately produces. The pinned workspace is
// returned because it is readable to the token's user by design: PrepareWorkspaceAccess
// clears every workspace guard on this chain, so only the worker bound can refuse it.
func (e *bearerChannelEnv) seedCrossTenantDelegation(t *testing.T) (victimWorkerID, victimWS, bearer string) {
	t.Helper()
	ctx := context.Background()

	// The victim, with a workspace and their own worker.
	victimID, _, victimWS, _, victimWorkerID := e.seedUserWorkspaceWorker(t)

	// The attacker owns a separate worker.
	attackerID := id.Generate()
	attackerOrgID := id.Generate()
	require.NoError(t, e.store.Orgs().Create(ctx, store.CreateOrgParams{
		ID: attackerOrgID, Name: "attacker-org",
	}))
	require.NoError(t, e.store.Users().Create(ctx, store.CreateUserParams{
		ID:       attackerID,
		OrgID:    attackerOrgID,
		Username: "attacker-" + id.Generate()[:6],
	}))
	attackerWorkerID := id.Generate()
	require.NoError(t, e.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              attackerWorkerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    attackerID,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	bearer, _ = e.mintDelegation(t, victimID, attackerWorkerID, victimWS)
	return victimWorkerID, victimWS, bearer
}

func TestOpenChannel_DelegationCannotReachAnotherUsersWorker(t *testing.T) {
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
// worker" -- and answers it with the same verifyWorkerAccess call. While the minter
// bound was bolted onto OpenChannel alone, this entrypoint still handed a
// cross-tenant bearer the victim worker's key bundle, its live encryption mode, and
// (via the offline/unavailable split) an online oracle. The bound lives in
// verifyWorkerAccess so both callers -- and the next one -- inherit it.
func TestGetWorkerHandshakeParams_DelegationCannotReachAnotherUsersWorker(t *testing.T) {
	env := setupBearerChannelEnv(t)
	victimWorkerID, _, bearer := env.seedCrossTenantDelegation(t)

	req := connect.NewRequest(&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: victimWorkerID})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, err := env.channelClient.GetWorkerHandshakeParams(context.Background(), req)

	require.Error(t, err, "a token minted by another user's worker must not read the victim worker's keys")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// ...and the ordinary case still works: a token may read the handshake params of the
// worker that minted it (the common `leapmux remote` path).
func TestGetWorkerHandshakeParams_DelegationReachesItsMintingWorker(t *testing.T) {
	env := setupBearerChannelEnv(t)
	userID, _, wsA, _, workerID := env.seedUserWorkspaceWorker(t)
	bearer, _ := env.mintDelegation(t, userID, workerID, wsA)

	req := connect.NewRequest(&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, err := env.channelClient.GetWorkerHandshakeParams(context.Background(), req)

	assert.NotEqual(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"the minting worker's own params must remain readable")
}

// The same token MUST still open a channel back to the worker that minted it --
// the ordinary `leapmux remote` case, where an agent talks to its own host.
func TestOpenChannel_DelegationReachesItsMintingWorker(t *testing.T) {
	env := setupBearerChannelEnv(t)
	userID, _, wsA, _, workerID := env.seedUserWorkspaceWorker(t)

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

// A delegation bearer is issued for ws-A; the user owns both ws-A and ws-B; the
// OpenChannel call must announce **only** ws-A to the worker -- even though
// ListAccessible would report both.
func TestOpenChannel_DelegationTokenNarrowsAccessibleWorkspaces(t *testing.T) {
	env := setupBearerChannelEnv(t)
	userID, _, wsA, wsB, workerID := env.seedUserWorkspaceWorker(t)

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
	require.Error(t, err, "no fake worker response → expected timeout/cancel")

	// The hub still SENT the ChannelOpen to the worker before
	// timing out; that's the part we want to inspect.
	select {
	case msg := <-sent:
		open := msg.GetChannelOpen()
		require.NotNil(t, open)
		assert.Equal(t, []string{wsA}, open.GetAccessibleWorkspaceIds(), "delegation pin must restrict the announced set to [ws-A]")
		assert.NotContains(t, open.GetAccessibleWorkspaceIds(), wsB, "ws-B must NOT leak into the worker even though the user owns it")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected ChannelOpen to be sent to the worker")
	}
}

// TestOpenChannel_DelegationRejectsRevokedScope verifies the OpenChannel
// re-check: if the workspace the bearer is pinned to has been
// transferred away (or the grant withdrawn) since the token was
// minted, the channel must NOT open even though the bearer is still
// formally valid.
func TestOpenChannel_DelegationRejectsRevokedScope(t *testing.T) {
	env := setupBearerChannelEnv(t)
	userID, _, wsA, _, workerID := env.seedUserWorkspaceWorker(t)

	bearer, _ := env.mintDelegation(t, userID, workerID, wsA)

	// Yank the workspace out from under the user. SoftDelete makes
	// GetByID return ErrNotFound which the OpenChannel re-check
	// resolves to "no access" → CodePermissionDenied.
	_, err := env.store.Workspaces().SoftDelete(context.Background(), store.SoftDeleteWorkspaceParams{
		ID:          wsA,
		OwnerUserID: userID,
	})
	require.NoError(t, err)

	env.captureWorker(t, workerID)
	req := connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         workerID,
		HandshakePayload: []byte("hs1"),
	})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, openErr := env.channelClient.OpenChannel(context.Background(), req)
	require.Error(t, openErr)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(openErr), "delegation pin to a workspace the user no longer accesses must be rejected")
}

func TestCloseChannel_DelegationRequiresSameBearerScope(t *testing.T) {
	env := setupBearerChannelEnv(t)
	userID, orgID, wsA, _, workerID := env.seedUserWorkspaceWorker(t)
	_, tokenID := env.mintDelegation(t, userID, workerID, wsA)

	cookieChannelID := id.Generate()
	otherDelegationChannelID := id.Generate()
	scopedChannelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(cookieChannelID, workerID, userID, channelmgr.AuthInfo{}, nil)
	env.channelMgr.RegisterWithAuthInfo(otherDelegationChannelID, workerID, userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential("other-token", wsA, "worker-mint"),
	}, nil)
	env.channelMgr.RegisterWithAuthInfo(scopedChannelID, workerID, userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential(tokenID, wsA, "worker-mint"),
	}, nil)

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userID,
		OrgID:      orgID,
		Credential: auth.DelegationCredential(tokenID, wsA, "worker-mint"),
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

func TestPrepareWorkspaceAccess_DelegationUpdatesOnlyMatchingBearerChannel(t *testing.T) {
	env := setupBearerChannelEnv(t)
	userID, orgID, wsA, _, workerID := env.seedUserWorkspaceWorker(t)
	_, tokenID := env.mintDelegation(t, userID, workerID, wsA)

	cookieChannelID := id.Generate()
	scopedChannelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(cookieChannelID, workerID, userID, channelmgr.AuthInfo{}, nil)
	env.channelMgr.RegisterWithAuthInfo(scopedChannelID, workerID, userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential(tokenID, wsA, workerID),
	}, nil)

	sent := make(chan *leapmuxv1.ConnectResponse, 4)
	_, _ = env.workerMgr.Register(&workermgr.Conn{
		WorkerID: workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sent <- msg
			if msg.GetChannelAccessUpdate() != nil && msg.GetRequestId() != "" {
				env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
					RequestId: msg.GetRequestId(),
					Payload: &leapmuxv1.ConnectRequest_ChannelAccessUpdateAck{
						ChannelAccessUpdateAck: &leapmuxv1.ChannelAccessUpdateAck{},
					},
				})
			}
			return nil
		},
	})

	// The minter is the worker that actually minted this token (what the auth
	// interceptor derives from the delegation_tokens row) -- prepare-access now
	// runs the same worker bound as OpenChannel, and a made-up minter id would
	// be refused as unscopeable rather than exercising the fan-out.
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userID,
		OrgID:      orgID,
		Credential: auth.DelegationCredential(tokenID, wsA, workerID),
	})
	_, err := env.channelSvc.PrepareWorkspaceAccess(ctx, connect.NewRequest(&leapmuxv1.PrepareWorkspaceAccessRequest{
		WorkerId:    workerID,
		WorkspaceId: wsA,
	}))
	require.NoError(t, err)

	select {
	case msg := <-sent:
		update := msg.GetChannelAccessUpdate()
		require.NotNil(t, update)
		assert.Equal(t, scopedChannelID, update.GetChannelId())
		assert.Equal(t, wsA, update.GetWorkspaceId())
	case <-time.After(time.Second):
		require.Fail(t, "expected ChannelAccessUpdate for matching delegation channel")
	}
	select {
	case msg := <-sent:
		require.Failf(t, "delegation caller must not update unrestricted channel", "got %v; cookie channel %s", msg, cookieChannelID)
	default:
	}
}

// TestOpenChannel_SessionTokenStillSeesFullAccessibleSet preserves
// the existing behaviour: cookie/session callers must keep getting
// the user's full accessible-workspace list. The narrowing only
// applies when the UserInfo has a workspace-scoped delegation credential.
func TestOpenChannel_SessionTokenStillSeesFullAccessibleSet(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	wsA := id.Generate()
	require.NoError(t, env.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: wsA, OrgID: adminUser.OrgID, OwnerUserID: adminUser.ID, Title: "ws-A",
	}))
	wsB := id.Generate()
	require.NoError(t, env.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: wsB, OrgID: adminUser.OrgID, OwnerUserID: adminUser.ID, Title: "ws-B",
	}))

	workerID := env.createWorkerWithKey(t, token, []byte("k"))

	sent := make(chan *leapmuxv1.ConnectResponse, 1)
	_, _ = env.workerMgr.Register(&workermgr.Conn{
		WorkerID: workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sent <- msg
			return nil
		},
	})

	shortCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_, _ = env.channelClient.OpenChannel(shortCtx, authedReq(&leapmuxv1.OpenChannelRequest{
		WorkerId:         workerID,
		HandshakePayload: []byte("hs"),
	}, token))

	select {
	case msg := <-sent:
		open := msg.GetChannelOpen()
		require.NotNil(t, open)
		assert.Contains(t, open.GetAccessibleWorkspaceIds(), wsA)
		assert.Contains(t, open.GetAccessibleWorkspaceIds(), wsB, "session callers must continue to see all accessible workspaces")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected ChannelOpen to be sent to the worker")
	}
}

// TestOpenChannel_SessionTokenAnnouncesOnlyOwnedWorkspaces pins the owner-only
// announce: a workspace owned by ANOTHER user -- in the caller's org or any
// other -- must never appear in the accessible-workspace set announced to the
// worker on channel open.
func TestOpenChannel_SessionTokenAnnouncesOnlyOwnedWorkspaces(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	// A workspace in the caller's own org (baseline).
	wsHome := id.Generate()
	require.NoError(t, env.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: wsHome, OrgID: adminUser.OrgID, OwnerUserID: adminUser.ID, Title: "ws-home",
	}))

	// A workspace owned by a different user (their own personal org) -- never
	// announced to this caller.
	orgB := id.Generate()
	require.NoError(t, env.store.Orgs().Create(ctx, store.CreateOrgParams{ID: orgB, Name: "org-b"}))
	ownerB := id.Generate()
	require.NoError(t, env.store.Users().Create(ctx, store.CreateUserParams{
		ID: ownerB, OrgID: orgB, Username: "owner-b", DisplayName: "Owner B",
	}))
	wsForeign := id.Generate()
	require.NoError(t, env.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: wsForeign, OrgID: orgB, OwnerUserID: ownerB, Title: "ws-foreign",
	}))

	workerID := env.createWorkerWithKey(t, token, []byte("k"))
	sent := make(chan *leapmuxv1.ConnectResponse, 1)
	_, _ = env.workerMgr.Register(&workermgr.Conn{
		WorkerID: workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sent <- msg
			return nil
		},
	})

	shortCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_, _ = env.channelClient.OpenChannel(shortCtx, authedReq(&leapmuxv1.OpenChannelRequest{
		WorkerId:         workerID,
		HandshakePayload: []byte("hs"),
	}, token))

	select {
	case msg := <-sent:
		open := msg.GetChannelOpen()
		require.NotNil(t, open)
		assert.Contains(t, open.GetAccessibleWorkspaceIds(), wsHome, "the caller's own workspace must be announced")
		assert.NotContains(t, open.GetAccessibleWorkspaceIds(), wsForeign, "another user's workspace must never be announced")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected ChannelOpen to be sent to the worker")
	}
}

// TestPrepareWorkspaceAccess_DelegationCannotWidenScope guards the
// other side of the narrowing: PrepareWorkspaceAccess must not let a
// delegation-pinned bearer add a *different* workspace to its
// accessible set after channel-open. Otherwise the narrowing in
// OpenChannel is just a speed-bump.
func TestPrepareWorkspaceAccess_DelegationCannotWidenScope(t *testing.T) {
	env := setupBearerChannelEnv(t)
	userID, _, wsA, wsB, workerID := env.seedUserWorkspaceWorker(t)

	bearer, _ := env.mintDelegation(t, userID, workerID, wsA)

	// The handshake itself succeeds (we don't care about the
	// fake-worker response here — we just need the request to reach
	// the handler).
	env.captureWorker(t, workerID)

	req := connect.NewRequest(&leapmuxv1.PrepareWorkspaceAccessRequest{
		WorkerId:    workerID,
		WorkspaceId: wsB, // pinned to A; B is forbidden
	})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, err := env.channelClient.PrepareWorkspaceAccess(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestPrepareWorkspaceAccess_DelegationCannotReachAnotherUsersWorker is the
// PrepareWorkspaceAccess arm of the minter bound. It names a worker_id and is on
// the delegation allowlist, so a bearer minted by the ATTACKER's worker used to
// walk straight past the workspace guards (its pin is legitimately the victim's
// workspace) into an unfiltered workermgr lookup. It must be refused exactly
// where OpenChannel and GetWorkerHandshakeParams refuse it -- before the worker
// registry is touched -- and refused identically whether the victim's worker is
// online or offline, so the Unavailable/OK split cannot be read as a liveness
// oracle for a worker the caller cannot reach.
func TestPrepareWorkspaceAccess_DelegationCannotReachAnotherUsersWorker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		online bool
	}{
		{"victim worker offline", false},
		{"victim worker online", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupBearerChannelEnv(t)
			victimWorkerID, victimWS, bearer := env.seedCrossTenantDelegation(t)

			if tc.online {
				env.captureWorker(t, victimWorkerID)
			}

			req := connect.NewRequest(&leapmuxv1.PrepareWorkspaceAccessRequest{
				WorkerId:    victimWorkerID,
				WorkspaceId: victimWS,
			})
			req.Header().Set("Authorization", "Bearer "+bearer)
			_, err := env.channelClient.PrepareWorkspaceAccess(context.Background(), req)

			require.Error(t, err, "a token minted by another user's worker must not reach the victim's worker")
			assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
				"refusal must not depend on whether the unreachable worker is online")
		})
	}
}
