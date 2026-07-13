package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

type delegationEnv struct {
	store           store.Store
	validator       *auth.TokenValidator
	cache           *auth.AuthContextRegistry
	server          *httptest.Server
	handler         *service.WorkerDelegationHandler
	userID          string
	orgID           string
	workerID        string
	workerAuthToken string
	workspaceID     string
	tabID           string
}

// setupDelegation seeds a complete (worker, workspace, workspace_tab)
// triple owned by the bootstrap admin so the mint authz check has
// something to verify.
func setupDelegation(t *testing.T) *delegationEnv {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)
	_, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	h := service.NewWorkerDelegationHandler(st, tv, auth.NewCredentialLifecycleEffects(sc, nil, nil))
	// Tests don't want production-grade backoff; shrink the
	// AddTab-race propagation window so "tab missing" cases fail fast
	// while still exercising the polling loop.
	h.MintTabPropagationTimeout = 50 * time.Millisecond
	h.MintTabPropagationStep = 5 * time.Millisecond
	h.RegisterRoutes(mux)

	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)

	workerID := id.Generate()
	workerAuthToken := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       workerAuthToken,
		RegisteredBy:    u.ID,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("test-mlkem"),
		SlhdsaPublicKey: []byte("test-slhdsa"),
	}))

	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       u.OrgID,
		OwnerUserID: u.ID,
		Title:       "ws",
	}))

	tabID := id.Generate()
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(context.Background(), store.UpsertOwnedTabParams{
		OrgID:       u.OrgID,
		WorkspaceID: workspaceID,
		WorkerID:    workerID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       tabID,
		Position:    "a",
		TileID:      "tile-1",
	}))

	return &delegationEnv{
		store:           st,
		validator:       tv,
		cache:           sc,
		server:          srv,
		handler:         h,
		userID:          u.ID,
		orgID:           u.OrgID,
		workerID:        workerID,
		workerAuthToken: workerAuthToken,
		workspaceID:     workspaceID,
		tabID:           tabID,
	}
}

func mintRequest(t *testing.T, env *delegationEnv, workerAuthToken string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/worker/delegation-tokens/mint", bytes.NewReader(buf))
	require.NoError(t, err)
	if workerAuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+workerAuthToken)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func revokeRequest(t *testing.T, env *delegationEnv, workerAuthToken string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/worker/delegation-tokens/revoke", bytes.NewReader(buf))
	require.NoError(t, err)
	if workerAuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+workerAuthToken)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestWorkerDelegation_Mint_HappyPath(t *testing.T) {
	env := setupDelegation(t)

	resp := mintRequest(t, env, env.workerAuthToken, map[string]any{
		"user_id":             env.userID,
		"workspace_id":        env.workspaceID,
		"issued_for_tab_id":   env.tabID,
		"issued_for_tab_type": int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		"agent_id":            "agent-1",
		"ttl_seconds":         60,
	})
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	access, _ := body["access_token"].(string)
	tokenID, _ := body["token_id"].(string)
	require.True(t, strings.HasPrefix(access, "lmx_"))
	require.NotEmpty(t, tokenID)

	// Validator must accept the minted bearer.
	info, err := env.validator.ValidateBearer(context.Background(), access)
	require.NoError(t, err)
	assert.Equal(t, env.userID, info.ID)

	// Persisted row carries the worker_id provenance for revocation
	// authz.
	row, err := env.store.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, env.workerID, row.WorkerID)
	assert.Equal(t, env.workspaceID, row.WorkspaceID)
}

func TestWorkerDelegation_Mint_RejectsMissingBearer(t *testing.T) {
	env := setupDelegation(t)
	resp := mintRequest(t, env, "", map[string]any{
		"user_id":           env.userID,
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": env.tabID,
	})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestWorkerDelegation_Mint_RejectsUnknownWorkerToken(t *testing.T) {
	env := setupDelegation(t)
	resp := mintRequest(t, env, "not-a-real-worker-token", map[string]any{
		"user_id":           env.userID,
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": env.tabID,
	})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestWorkerDelegation_Mint_RejectsMissingFields(t *testing.T) {
	env := setupDelegation(t)
	resp := mintRequest(t, env, env.workerAuthToken, map[string]any{
		// user_id missing
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": env.tabID,
	})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestWorkerDelegation_Mint_RejectsTabOwnedByDifferentWorker(t *testing.T) {
	env := setupDelegation(t)

	// Seed a second worker (also owned by the same user) that is NOT
	// the host of env.tabID.
	otherWorkerID := id.Generate()
	otherAuthToken := id.Generate()
	require.NoError(t, env.store.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              otherWorkerID,
		AuthToken:       otherAuthToken,
		RegisteredBy:    env.userID,
		PublicKey:       []byte("other-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("other-mlkem"),
		SlhdsaPublicKey: []byte("other-slhdsa"),
	}))

	resp := mintRequest(t, env, otherAuthToken, map[string]any{
		"user_id":           env.userID,
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": env.tabID, // hosted by env.workerID
	})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "tab not owned by calling worker")
}

func TestWorkerDelegation_Mint_RejectsBeforeTabPropagation(t *testing.T) {
	env := setupDelegation(t)
	// Mint for a tab_id that has no row yet — emulates the AddTab race
	// resolving negatively (tab will never appear). With test-tuned
	// retry windows this resolves to 403 in ~50ms instead of waiting
	// the full production backoff.
	resp := mintRequest(t, env, env.workerAuthToken, map[string]any{
		"user_id":           env.userID,
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": "nonexistent-tab",
	})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestWorkerDelegation_Mint_RetriesUntilTabPropagates exercises the
// AddTab-race recovery path: a worker that races mint ahead of the
// hub-side workspace_tabs commit must succeed once the row appears,
// not fail with a hard 403. This is the "lazy-mint resolves the
// chicken-and-egg" guarantee the plan requires.
func TestWorkerDelegation_Mint_RetriesUntilTabPropagates(t *testing.T) {
	env := setupDelegation(t)
	// Give the polling loop room to observe at least a few retries
	// before the tab is inserted.
	env.handler.MintTabPropagationTimeout = 500 * time.Millisecond
	env.handler.MintTabPropagationStep = 10 * time.Millisecond

	// Use a tab id that does NOT exist yet — it's inserted by a
	// goroutine after a short delay, simulating AddTab landing later.
	lateTabID := id.Generate()

	go func() {
		time.Sleep(75 * time.Millisecond)
		_ = env.store.WorkspaceTabIndex().UpsertOwned(context.Background(), store.UpsertOwnedTabParams{
			OrgID:       env.orgID,
			WorkspaceID: env.workspaceID,
			WorkerID:    env.workerID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       lateTabID,
			Position:    "b",
			TileID:      "tile-2",
		})
	}()

	resp := mintRequest(t, env, env.workerAuthToken, map[string]any{
		"user_id":             env.userID,
		"workspace_id":        env.workspaceID,
		"issued_for_tab_id":   lateTabID,
		"issued_for_tab_type": int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		"agent_id":            "agent-late",
	})
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "mint must succeed after tab propagates")
}

func TestWorkerDelegation_Mint_RejectsCrossUserMint(t *testing.T) {
	env := setupDelegation(t)

	// Seed a second user with their own org and try to mint for env.workerID.
	otherUserID := hubtestutil.CreateTestUser(t, env.store, "other-user", "p")
	resp := mintRequest(t, env, env.workerAuthToken, map[string]any{
		"user_id":           otherUserID,
		"workspace_id":      env.workspaceID, // owned by admin, not other
		"issued_for_tab_id": env.tabID,
	})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestWorkerDelegation_Mint_AllowsSharedWorkspaceAccess(t *testing.T) {
	env := setupDelegation(t)

	// Share env.workspaceID with another user who can therefore have
	// delegation tokens minted for them.
	otherUserID := hubtestutil.CreateTestUser(t, env.store, "shared-user", "p")
	require.NoError(t, env.store.OrgMembers().Create(context.Background(), store.CreateOrgMemberParams{
		OrgID: env.orgID, UserID: otherUserID, Role: leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	}))
	require.NoError(t, env.store.WorkspaceAccess().Grant(context.Background(), store.GrantWorkspaceAccessParams{
		WorkspaceID: env.workspaceID,
		UserID:      otherUserID,
	}))

	resp := mintRequest(t, env, env.workerAuthToken, map[string]any{
		"user_id":           otherUserID,
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": env.tabID,
	})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// failWorkspaceGetStore forces Workspaces().GetByID to return a
// non-NotFound store error, standing in for a transient DB failure during
// the mint workspace-access check. Everything else delegates to the real store.
type failWorkspaceGetStore struct {
	store.Store
	err error
}

func (s failWorkspaceGetStore) Workspaces() store.WorkspaceStore {
	return failWorkspaceGet{WorkspaceStore: s.Store.Workspaces(), err: s.err}
}

type failWorkspaceGet struct {
	store.WorkspaceStore
	err error
}

func (w failWorkspaceGet) GetByID(context.Context, string) (*store.Workspace, error) {
	return nil, w.err
}

// TestWorkerDelegation_Mint_StoreErrorIsRetryable500 pins that a transient
// store error during the workspace-access check surfaces as a retryable 500,
// not a permanent 403 -- a freshly-spawned agent treats 403 as a hard authz
// denial (not retryable) and would fail the mint permanently on a brief DB blip.
func TestWorkerDelegation_Mint_StoreErrorIsRetryable500(t *testing.T) {
	env := setupDelegation(t)
	failing := failWorkspaceGetStore{Store: env.store, err: errors.New("transient db failure")}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	h := service.NewWorkerDelegationHandler(failing, env.validator, auth.NewCredentialLifecycleEffects(env.cache, nil, nil))
	h.MintTabPropagationTimeout = 50 * time.Millisecond
	h.MintTabPropagationStep = 5 * time.Millisecond
	h.RegisterRoutes(mux)

	buf, err := json.Marshal(map[string]any{
		"user_id":           env.userID,
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": env.tabID,
	})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/worker/delegation-tokens/mint", bytes.NewReader(buf))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+env.workerAuthToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode,
		"a transient store error during the access check must be a retryable 500, not a permanent 403")
}

func TestWorkerDelegation_Mint_BoundsTTL(t *testing.T) {
	env := setupDelegation(t)
	// Request a TTL longer than DelegationTokenTTL (1h). Handler must
	// clamp to the max.
	huge := int64((10 * time.Hour) / time.Second)
	resp := mintRequest(t, env, env.workerAuthToken, map[string]any{
		"user_id":           env.userID,
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": env.tabID,
		"ttl_seconds":       huge,
	})
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	expiresIn, _ := body["expires_in"].(float64)
	require.LessOrEqual(t, int64(expiresIn), int64(auth.DelegationTokenTTL/time.Second))
}

func TestWorkerDelegation_Revoke_HappyPath(t *testing.T) {
	env := setupDelegation(t)

	// Mint then revoke.
	mintResp := mintRequest(t, env, env.workerAuthToken, map[string]any{
		"user_id":           env.userID,
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": env.tabID,
	})
	require.Equal(t, http.StatusOK, mintResp.StatusCode)
	var mintBody map[string]any
	require.NoError(t, json.NewDecoder(mintResp.Body).Decode(&mintBody))
	_ = mintResp.Body.Close()
	tokenID := mintBody["token_id"].(string)
	access := mintBody["access_token"].(string)

	// Warm the validator cache so we can verify EvictBearer is called.
	_, err := env.validator.ValidateBearer(context.Background(), access)
	require.NoError(t, err)

	revokeResp := revokeRequest(t, env, env.workerAuthToken, map[string]any{"token_id": tokenID})
	defer func() { _ = revokeResp.Body.Close() }()
	require.Equal(t, http.StatusNoContent, revokeResp.StatusCode)

	// Row is revoked.
	row, err := env.store.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt)

	// Validation now fails — confirms the underlying row reflects the
	// revocation regardless of cache state.
	_, err = env.validator.ValidateBearer(context.Background(), access)
	assert.Error(t, err)
}

func TestWorkerDelegation_Revoke_RejectsUnauthenticatedCall(t *testing.T) {
	env := setupDelegation(t)
	resp := revokeRequest(t, env, "", map[string]any{"token_id": "t-1"})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestWorkerDelegation_Revoke_RejectsCrossWorkerRevocation(t *testing.T) {
	env := setupDelegation(t)

	// Mint via env's worker.
	mintResp := mintRequest(t, env, env.workerAuthToken, map[string]any{
		"user_id":           env.userID,
		"workspace_id":      env.workspaceID,
		"issued_for_tab_id": env.tabID,
	})
	var body map[string]any
	require.NoError(t, json.NewDecoder(mintResp.Body).Decode(&body))
	_ = mintResp.Body.Close()
	tokenID := body["token_id"].(string)

	// Seed a second worker; revocation must fail because the token was
	// minted by env.workerID, not this worker.
	otherWorkerID := id.Generate()
	otherAuthToken := id.Generate()
	require.NoError(t, env.store.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              otherWorkerID,
		AuthToken:       otherAuthToken,
		RegisteredBy:    env.userID,
		PublicKey:       []byte("other-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("other-mlkem"),
		SlhdsaPublicKey: []byte("other-slhdsa"),
	}))

	resp := revokeRequest(t, env, otherAuthToken, map[string]any{"token_id": tokenID})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Token must still be valid (not revoked by the failed call).
	row, err := env.store.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt)
}

func TestWorkerDelegation_Revoke_UnknownTokenIs404(t *testing.T) {
	env := setupDelegation(t)
	resp := revokeRequest(t, env, env.workerAuthToken, map[string]any{"token_id": "never-existed"})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestWorkerDelegation_Mint_GetMethodRejected(t *testing.T) {
	env := setupDelegation(t)
	req, _ := http.NewRequest(http.MethodGet, env.server.URL+"/worker/delegation-tokens/mint", nil)
	req.Header.Set("Authorization", "Bearer "+env.workerAuthToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestWorkerDelegation_Mint_RejectsMalformedJSON(t *testing.T) {
	env := setupDelegation(t)
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/worker/delegation-tokens/mint",
		strings.NewReader("{not json"))
	req.Header.Set("Authorization", "Bearer "+env.workerAuthToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
