package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

// TestOrgEventsHandler_ForeignOrgRefusedWithoutMaterializing pins the fail-closed
// org guard on the /ws/orgevents path: an authenticated user who dials with a
// foreign org_id must be refused (NotFound) BEFORE the registry materializes that
// org's manager. This is the highest-severity arm of the guard, because a
// subscriber refcount would otherwise pin the foreign manager janitor-immune for
// the socket's whole lifetime.
func TestOrgEventsHandler_ForeignOrgRefusedWithoutMaterializing(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "org-events-owner")
	user := storetest.SeedUser(t, st, orgID, "alice")
	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(user.ID),
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	_, sessionCache := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
	t.Cleanup(sessionCache.Stop)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	workspaceID := storetest.SeedWorkspace(t, st, orgID, user.ID, "Owned")
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(user.ID),
		WorkerID:         workerID,
		WorkspaceID:      workspaceID,
		IssuedForTabID:   "tab-1",
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       tv.HashSecret(secret),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))

	// The factory records every org it is asked to build. The guard must reject
	// the foreign org before the registry ever calls it.
	var factoryOrgs []string
	registry := crdt.NewRegistry(func(ctx context.Context, want string) (*crdt.Manager, error) {
		factoryOrgs = append(factoryOrgs, want)
		mgr := crdt.NewManager(want, newMemJournal(), allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	srv := httptest.NewServer(service.NewOrgEventsHandler(st, registry, sessionCache, nil, false).
		WithTokenValidator(tv))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	// Dial with an org the caller is not a member of.
	wsURL := "ws" + srv.URL[len("http"):] + "?org_id=" + url.QueryEscape("some-foreign-org")
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	require.Error(t, err, "a foreign org_id must be refused, not upgraded to a WebSocket")
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"the foreign org must be refused with NotFound before the WS upgrade")
	assert.NotContains(t, factoryOrgs, "some-foreign-org",
		"the registry must never materialize a manager for a foreign org")
}

func TestOrgEventsHandler_DelegationScopesInitialMaterialized(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "org-events-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	allowedWS := storetest.SeedWorkspace(t, st, orgID, user.ID, "Allowed")
	siblingWS := storetest.SeedWorkspace(t, st, orgID, user.ID, "Sibling")
	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(user.ID),
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	_, sessionCache := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
	t.Cleanup(sessionCache.Stop)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(user.ID),
		WorkerID:         workerID,
		WorkspaceID:      allowedWS,
		IssuedForTabID:   "tab-1",
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       tv.HashSecret(secret),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))

	j := newMemJournal()
	var mgr *crdt.Manager
	registry := crdt.NewRegistry(func(ctx context.Context, want string) (*crdt.Manager, error) {
		require.Equal(t, orgID, want)
		mgr = crdt.NewManager(orgID, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
			s.Workspaces[allowedWS] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: allowedWS, RootNodeId: "root-allowed"}
			s.Workspaces[siblingWS] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: siblingWS, RootNodeId: "root-sibling"}
			s.Nodes["root-allowed"] = &leapmuxv1.NodeRecord{NodeId: "root-allowed"}
			s.Nodes["root-sibling"] = &leapmuxv1.NodeRecord{NodeId: "root-sibling"}
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	srv := httptest.NewServer(service.NewOrgEventsHandler(st, registry, sessionCache, nil, false).
		WithTokenValidator(tv))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	wsURL := "ws" + srv.URL[len("http"):] + "?org_id=" + url.QueryEscape(orgID)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	payload, err := channelwire.ReadFramedBytes(ctx, conn)
	require.NoError(t, err)
	event := &leapmuxv1.WatchOrgEvent{}
	require.NoError(t, proto.Unmarshal(payload, event))
	initial := event.GetInitial()
	require.NotNil(t, initial)

	assert.Contains(t, initial.GetWorkspaces(), allowedWS)
	assert.NotContains(t, initial.GetWorkspaces(), siblingWS,
		"delegation bearer must not receive sibling workspace materialized state")

	// The connect-time delegation scope is immutable. Even though the
	// underlying user owns siblingWS, a later lifecycle expansion must
	// not add it to this bearer-scoped subscription.
	mgr.BroadcastWorkspaceCreated(context.Background(), siblingWS, "Sibling", "root-sibling")
	mgr.BroadcastWorkspaceCreated(context.Background(), allowedWS, "Allowed", "root-allowed")
	payload, err = channelwire.ReadFramedBytes(ctx, conn)
	require.NoError(t, err)
	event.Reset()
	require.NoError(t, proto.Unmarshal(payload, event))
	require.NotNil(t, event.GetCreated())
	assert.Equal(t, allowedWS, event.GetCreated().GetWorkspaceId(),
		"the first post-connect lifecycle event must remain inside the delegation scope")

	sessionCache.EvictBearer(auth.NewBearerRef(auth.BearerKindDelegation, tokenID))
	readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	defer cancelRead()
	_, err = channelwire.ReadFramedBytes(readCtx, conn)
	require.Error(t, err, "revoking the delegation bearer must close the org-event subscription")
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"the org-event subscription remained open after its authenticated lease was cancelled")
}
