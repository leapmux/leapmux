package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
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
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestUserEventsHandler_RevokingTheBearerClosesTheSubscription is the surviving
// half of a test deleted with the workspace scope axis.
//
// That test asserted three things; two were about the per-workspace narrowing
// this commit removed, but its tail was not: it pinned that evicting a bearer
// from the session cache CLOSES the `/ws/userevents` socket it authenticated.
// That property lives in `ws_userevents.go`'s authLease binding, which is
// untouched and still load-bearing -- a revoked delegation bearer left streaming
// a user's entire CRDT is the failure it prevents.
//
// Nothing replaced it: the only other eviction-closes-the-socket test dials
// `/ws/channel`, a different endpoint, so this file's endpoint had no direct
// coverage at all.
func TestUserEventsHandler_RevokingTheBearerClosesTheSubscription(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "Allowed")
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
		IssuedForTabID:   "tab-1",
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       tv.HashSecret(secret),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))

	j := newMemJournal()
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
			s.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
				WorkspaceId: workspaceID, RootNodeId: "root-allowed",
			}
			s.Nodes["root-allowed"] = &leapmuxv1.NodeRecord{NodeId: "root-allowed"}
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
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// The subscription is live: the initial materialized state arrives, and a
	// delegation bearer sees what its user owns (no workspace narrowing any more).
	payload, err := channelwire.ReadFramedBytes(ctx, conn)
	require.NoError(t, err)
	event := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, event))
	require.NotNil(t, event.GetInitial())
	require.Contains(t, event.GetInitial().GetWorkspaces(), workspaceID)

	sessionCache.EvictBearer(auth.NewBearerRef(auth.BearerKindDelegation, tokenID))

	readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	defer cancelRead()
	_, err = channelwire.ReadFramedBytes(readCtx, conn)
	require.Error(t, err, "revoking the delegation bearer must close the user-event subscription")
	// DeadlineExceeded would mean the socket simply went quiet, which is the
	// failure: the lease was cancelled and the stream stayed open.
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"the user-event subscription remained open after its authenticated lease was cancelled")
}
