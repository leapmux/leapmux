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
	cache         *auth.SessionCache
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

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(st, testConfig(), nil, nil, mail.NewStubSender(), mail.Renderer{}), opts)
	mux.Handle(authPath, authHandler)

	channelSvc := service.NewChannelService(st, wMgr, cMgr, pendingReqs)
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
		ID: orgID, Name: "test-org", IsPersonal: true,
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
	e.workerMgr.Register(conn)
	return ch, conn
}

// TestOpenChannel_DelegationTokenNarrowsAccessibleWorkspaces is the
// test for the plan's "delegation token caller gets only
// [workspace_id]" rule. A delegation bearer is issued for ws-A; the
// user owns both ws-A and ws-B; the OpenChannel call must announce
// **only** ws-A to the worker — even though ListAccessible would
// report both.
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

// TestOpenChannel_SessionTokenStillSeesFullAccessibleSet preserves
// the existing behaviour: cookie/session callers must keep getting
// the user's full accessible-workspace list. The narrowing only
// applies when DelegationWorkspaceID is set on the UserInfo.
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
	env.workerMgr.Register(&workermgr.Conn{
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
