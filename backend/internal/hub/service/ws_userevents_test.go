package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestUserEventsHandler_DelegationScopesInitialMaterialized(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	allowedWS := storetest.SeedWorkspace(t, st, user.ID, "Allowed")
	siblingWS := storetest.SeedWorkspace(t, st, user.ID, "Sibling")
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
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		require.Equal(t, user.ID, want.String())
		mgr = crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
			s.Workspaces[allowedWS] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: allowedWS, RootNodeId: "root-allowed"}
			s.Workspaces[siblingWS] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: siblingWS, RootNodeId: "root-sibling"}
			s.Nodes["root-allowed"] = &leapmuxv1.NodeRecord{NodeId: "root-allowed"}
			s.Nodes["root-sibling"] = &leapmuxv1.NodeRecord{NodeId: "root-sibling"}
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, sessionCache, nil, false).
		WithTokenValidator(tv))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))
	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	payload, err := channelwire.ReadFramedBytes(ctx, conn)
	require.NoError(t, err)
	event := &leapmuxv1.WatchUserEvent{}
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
	require.Error(t, err, "revoking the delegation bearer must close the user-event subscription")
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"the user-event subscription remained open after its authenticated lease was cancelled")
}
