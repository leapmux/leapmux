package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

// teardownEnv stands up a hub configured with bearer auth + the
// channelService→delegationHandler closer wiring so the
// "revoke a delegation token, watch the channel die" tests are
// faithful end-to-end.
type teardownEnv struct {
	store         store.Store
	validator     *auth.TokenValidator
	cache         *auth.AuthContextRegistry
	server        *httptest.Server
	channelMgr    *channelmgr.Manager
	channelSvc    *service.ChannelService
	workerMgr     *workermgr.Manager
	userID        string
	orgID         string
	workspaceID   string
	workerID      string
	tabID         string
	workerAuthTok string
	revokeURL     string
}

func setupTeardownEnv(t *testing.T) *teardownEnv {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	wMgr := workermgr.New()
	cMgr := channelmgr.New()
	pendingReqs := workermgr.NewPendingRequests(testConfig().APITimeout)

	mux := http.NewServeMux()
	_, sc := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
	t.Cleanup(sc.Stop)

	channelSvc := service.NewChannelService(st, wMgr, cMgr, pendingReqs, sc)

	delegationHandler := service.NewWorkerDelegationHandler(
		st, tv, auth.NewCredentialLifecycleEffects(sc, channelSvc, nil))
	delegationHandler.RegisterRoutes(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Seed user / workspace / worker / workspace_tab so the mint authz
	// check has something to verify and the delegation rows are
	// well-formed.
	ctx := context.Background()
	orgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{
		ID: orgID, Name: "td-org",
	}))
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "td-user-" + id.Generate()[:6],
	}))

	workerID := id.Generate()
	workerAuthTok := id.Generate()
	require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       workerAuthTok,
		RegisteredBy:    userID,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	wsID := id.Generate()
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: wsID, OrgID: orgID, OwnerUserID: userID, Title: "ws",
	}))
	tabID := id.Generate()
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		OrgID:       orgID,
		WorkspaceID: wsID,
		WorkerID:    workerID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       tabID,
		Position:    "a",
		TileID:      "tile-1",
	}))

	return &teardownEnv{
		store:         st,
		validator:     tv,
		cache:         sc,
		server:        srv,
		channelMgr:    cMgr,
		channelSvc:    channelSvc,
		workerMgr:     wMgr,
		userID:        userID,
		orgID:         orgID,
		workspaceID:   wsID,
		workerID:      workerID,
		tabID:         tabID,
		workerAuthTok: workerAuthTok,
		revokeURL:     srv.URL + "/worker/delegation-tokens/revoke",
	}
}

func (e *teardownEnv) seedDelegationRow(t *testing.T) (tokenID string) {
	t.Helper()
	tokenID = id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, e.store.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           e.userID,
		WorkerID:         e.workerID,
		WorkspaceID:      e.workspaceID,
		IssuedForTabID:   e.tabID,
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       e.validator.HashSecret(secret),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))
	return tokenID
}

// captureWorker registers a fake online worker that records every
// ConnectResponse the hub sends; tests assert the ChannelClose
// notification fires after a delegation revoke.
func (e *teardownEnv) captureWorker() chan *leapmuxv1.ConnectResponse {
	ch := make(chan *leapmuxv1.ConnectResponse, 16)
	_, _ = e.workerMgr.Register(&workermgr.Conn{
		WorkerID: e.workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			ch <- msg
			return nil
		},
	})
	return ch
}

// TestWorkerDelegationRevoke_TearsDownOpenChannels is the headline
// test for the plan's "channel teardown on delegation revocation"
// hardening: an open channel that was authenticated by a delegation
// bearer must be removed the moment that bearer is revoked, and the
// owning worker must receive a ChannelClose notification — same
// payload it would see for a user-initiated CloseChannel.
func TestWorkerDelegationRevoke_TearsDownOpenChannels(t *testing.T) {
	env := setupTeardownEnv(t)
	tokenID := env.seedDelegationRow(t)

	// Register a channel "as if" OpenChannel had succeeded for this
	// bearer. Going through the full OpenChannel flow would need a
	// running fake worker; the post-condition we care about is that
	// a registered bearer-keyed channel is dropped on revoke.
	channelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(channelID, env.workerID, env.userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential(tokenID, "test-workspace", "worker-mint"),
	}, nil)
	require.True(t, env.channelMgr.Exists(channelID))

	workerSent := env.captureWorker()

	// Hit the revoke endpoint as the worker that minted the bearer.
	req, err := http.NewRequest(http.MethodPost, env.revokeURL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+env.workerAuthTok)
	req.Header.Set("Content-Type", "application/json")
	req.Body = mustJSON(t, map[string]string{"token_id": tokenID})

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Channel is gone.
	assert.False(t, env.channelMgr.Exists(channelID), "channel must be torn down on bearer revoke")

	// Worker received a ChannelClose for the dropped channel.
	select {
	case msg := <-workerSent:
		cc := msg.GetChannelClose()
		require.NotNil(t, cc, "worker must receive ChannelCloseNotification")
		assert.Equal(t, channelID, cc.GetChannelId())
	case <-time.After(time.Second):
		require.Fail(t, "expected ChannelClose notification on the worker channel")
	}
}

// TestWorkerDelegationRevoke_LeavesOtherChannelsUntouched verifies the
// teardown is bearer-precise: a second channel held by an unrelated
// bearer (or a cookie session) must NOT be dropped.
func TestWorkerDelegationRevoke_LeavesOtherChannelsUntouched(t *testing.T) {
	env := setupTeardownEnv(t)
	revokedToken := env.seedDelegationRow(t)
	otherToken := env.seedDelegationRow(t)

	revokedCh := id.Generate()
	otherBearerCh := id.Generate()
	cookieCh := id.Generate()

	env.channelMgr.RegisterWithAuthInfo(revokedCh, env.workerID, env.userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential(revokedToken, "test-workspace", "worker-mint"),
	}, nil)
	env.channelMgr.RegisterWithAuthInfo(otherBearerCh, env.workerID, env.userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential(otherToken, "test-workspace", "worker-mint"),
	}, nil)
	env.channelMgr.RegisterWithAuthInfo(cookieCh, env.workerID, env.userID, channelmgr.AuthInfo{}, nil)

	env.captureWorker()

	req, _ := http.NewRequest(http.MethodPost, env.revokeURL, nil)
	req.Header.Set("Authorization", "Bearer "+env.workerAuthTok)
	req.Header.Set("Content-Type", "application/json")
	req.Body = mustJSON(t, map[string]string{"token_id": revokedToken})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.False(t, env.channelMgr.Exists(revokedCh))
	assert.True(t, env.channelMgr.Exists(otherBearerCh), "other bearer's channel must survive")
	assert.True(t, env.channelMgr.Exists(cookieCh), "cookie channel must survive")
}

// TestChannelService_CloseChannelsByUserRevocationNotifiesWorkers verifies
// generation-aware credential rotation closes old channels and notifies workers.
func TestChannelService_CloseChannelsByUserRevocationNotifiesWorkers(t *testing.T) {
	env := setupTeardownEnv(t)
	tokenID := env.seedDelegationRow(t)

	// Two channels for the user across two workers, plus a third
	// worker hosting a different user's channel.
	otherUserID := id.Generate()
	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID: otherUserID, OrgID: env.orgID, Username: "other-" + id.Generate()[:6],
	}))

	chA := id.Generate()
	chB := id.Generate()
	chOther := id.Generate()
	workerA := env.workerID
	workerB := id.Generate()
	require.NoError(t, env.store.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID: workerB, AuthToken: id.Generate(), RegisteredBy: env.userID,
		PublicKey: []byte("k2-32-bytes-padding-padding-okok"), MlkemPublicKey: []byte("m2"), SlhdsaPublicKey: []byte("s2"),
	}))

	env.channelMgr.RegisterWithAuthInfo(chA, workerA, env.userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential(tokenID, "test-workspace", "worker-mint"),
	}, nil)
	env.channelMgr.RegisterWithAuthInfo(chB, workerB, env.userID, channelmgr.AuthInfo{}, nil)
	env.channelMgr.RegisterWithAuthInfo(chOther, workerA, otherUserID, channelmgr.AuthInfo{}, nil)

	closesA := make(chan *leapmuxv1.ConnectResponse, 4)
	closesB := make(chan *leapmuxv1.ConnectResponse, 4)
	_, _ = env.workerMgr.Register(&workermgr.Conn{
		WorkerID: workerA,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			if msg.GetChannelClose() != nil {
				closesA <- msg
			}
			return nil
		},
	})
	_, _ = env.workerMgr.Register(&workermgr.Conn{
		WorkerID: workerB,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			if msg.GetChannelClose() != nil {
				closesB <- msg
			}
			return nil
		},
	})

	closed := env.channelSvc.CloseChannelsByUserRevocation(env.userID, 1)
	assert.Equal(t, 2, closed, "both of the user's channels should be torn down")
	assert.False(t, env.channelMgr.Exists(chA))
	assert.False(t, env.channelMgr.Exists(chB))
	assert.True(t, env.channelMgr.Exists(chOther), "other user's channel must survive")

	// Both workers must have received their respective close.
	select {
	case msg := <-closesA:
		assert.Equal(t, chA, msg.GetChannelClose().GetChannelId())
	case <-time.After(time.Second):
		require.Fail(t, "worker A did not receive close")
	}
	select {
	case msg := <-closesB:
		assert.Equal(t, chB, msg.GetChannelClose().GetChannelId())
	case <-time.After(time.Second):
		require.Fail(t, "worker B did not receive close")
	}
}

// TestChannelService_CloseChannelsByBearer_EmptyTokenIDIsNoop pins the
// safety check at the hub layer: a buggy revoke path that passes ""
// must NOT match every cookie channel.
func TestChannelService_CloseChannelsByBearer_EmptyTokenIDIsNoop(t *testing.T) {
	env := setupTeardownEnv(t)
	chCookie := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(chCookie, env.workerID, env.userID, channelmgr.AuthInfo{}, nil)
	assert.Equal(t, 0, env.channelSvc.CloseChannelsByBearer(auth.NewBearerRef(auth.BearerKindAPI, "")))
	assert.True(t, env.channelMgr.Exists(chCookie))
}

// mustJSON marshals body to a no-op-Close ReadCloser suitable for
// http.Request.Body. Centralised so the revoke tests stay focused on
// the assertions instead of plumbing.
func mustJSON(t *testing.T, body any) io.ReadCloser {
	t.Helper()
	buf, err := json.Marshal(body)
	require.NoError(t, err)
	return io.NopCloser(bytes.NewReader(buf))
}
