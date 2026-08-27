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
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type adminWorkerEnv struct {
	client leapmuxv1connect.AdminWorkerServiceClient
	st     store.Store
	token  string
	userID string
}

func setupAdminWorkerTest(t *testing.T) *adminWorkerEnv {
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
	})
	require.NoError(t, err)
	session, _, _, err := auth.Login(context.Background(), st, "admin", "adminpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	mux := http.NewServeMux()
	// A real effects value over a real delegation-scope cache; the notifier
	// and the broadcaster need a worker manager and a channel manager this
	// harness does not build, and Apply skips the ones it was not given.
	effects := service.NewWorkerDeregisterEffects(auth.NewDelegationScopeCache(st), nil, nil)
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	opts := connect.WithInterceptors(interceptor)
	path, handler := leapmuxv1connect.NewAdminWorkerServiceHandler(service.NewAdminWorkerService(st, effects), opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	return &adminWorkerEnv{
		client: leapmuxv1connect.NewAdminWorkerServiceClient(server.Client(), server.URL, connect.WithGRPC()),
		st:     st,
		token:  session,
		userID: admin.ID,
	}
}

func TestAdminWorkerService_DeregisterAndGet(t *testing.T) {
	env := setupAdminWorkerTest(t)
	ctx := context.Background()

	_, err := env.client.GetWorker(ctx, authedReq(&leapmuxv1.AdminWorkerServiceGetWorkerRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "id is required")

	_, err = env.client.DeregisterWorker(ctx, authedReq(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = env.client.DeregisterWorker(ctx, authedReq(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: "missing",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "not found or not active")

	_, err = env.client.ListWorkers(ctx, authedReq(&leapmuxv1.AdminWorkerServiceListWorkersRequest{
		UserId:   env.userID,
		Username: "admin",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "mutually exclusive")

	worker := storetest.SeedWorker(t, env.st, env.userID)

	got, err := env.client.GetWorker(ctx, authedReq(&leapmuxv1.AdminWorkerServiceGetWorkerRequest{
		Id: worker.ID,
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, worker.ID, got.Msg.GetWorker().GetId())
	assert.Equal(t, "admin", got.Msg.GetWorker().GetOwnerUsername())

	_, err = env.client.DeregisterWorker(ctx, authedReq(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}, env.token))
	require.NoError(t, err)

	got, err = env.client.GetWorker(ctx, authedReq(&leapmuxv1.AdminWorkerServiceGetWorkerRequest{
		Id: worker.ID,
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, got.Msg.GetWorker().GetStatus())
	assert.Equal(t, "admin", got.Msg.GetWorker().GetOwnerUsername())

	_, err = env.client.DeregisterWorker(ctx, authedReq(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// expiredRegistrationKeys is the backlog the drain case seeds. It is larger
// than the purge query's own batch (LIMIT 1000 in every dialect), so the
// handler MUST take more than one pass to clear it. A correct loop and a
// loop that stops after the first short page drain a one-batch backlog
// identically, so such a backlog cannot pin the drain at all.
//
// The number is coupled to that LIMIT, and it is the weaker half of the
// coverage on purpose: it is what exercises the REAL query, while
// TestPurgeExpiredRegistrationKeysDrainsUntilAPassDeletesNothing pins the
// stop rule itself through a seam and needs no copy of the batch size.
const expiredRegistrationKeys = 1001

// TestAdminWorkerService_PurgeExpiredRegistrationKeysDrainsEveryBatch pins
// the drain against the real query. The loop used to break on a page
// shorter than a hardcoded 1000, a second copy of the query's own LIMIT:
// change the SQL and the purge silently stops after one page, leaving a
// backlog nothing reports. The shared helper ends on a pass that deletes
// NOTHING, which needs no copy of that number.
func TestAdminWorkerService_PurgeExpiredRegistrationKeysDrainsEveryBatch(t *testing.T) {
	env := setupAdminWorkerTest(t)
	ctx := context.Background()

	expired := time.Now().UTC().Add(-time.Hour)
	for range expiredRegistrationKeys {
		require.NoError(t, env.st.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
			ID:        id.Generate(),
			CreatedBy: userid.MustNew(env.userID),
			ExpiresAt: expired,
		}))
	}
	// A live key the purge must leave alone: the drain is not "delete every
	// registration key until none is left".
	liveKey := id.Generate()
	require.NoError(t, env.st.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID:        liveKey,
		CreatedBy: userid.MustNew(env.userID),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))

	got, err := env.client.PurgeExpiredRegistrationKeys(ctx,
		authedReq(&leapmuxv1.PurgeExpiredRegistrationKeysRequest{}, env.token))
	require.NoError(t, err)
	assert.Equal(t, int64(expiredRegistrationKeys), got.Msg.GetPurged(),
		"a backlog larger than one batch is cleared in one call")

	// The backlog is gone from the table, not merely counted as gone.
	remaining, err := env.client.ListRegistrationKeys(ctx, authedReq(&leapmuxv1.ListRegistrationKeysRequest{
		IncludeExpired: true,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, remaining.Msg.GetKeys(), 1)
	assert.Equal(t, liveKey, remaining.Msg.GetKeys()[0].GetId(), "an unexpired key survives the purge")

	// Idempotent: a second call finds nothing and reports zero.
	got, err = env.client.PurgeExpiredRegistrationKeys(ctx,
		authedReq(&leapmuxv1.PurgeExpiredRegistrationKeysRequest{}, env.token))
	require.NoError(t, err)
	assert.Zero(t, got.Msg.GetPurged())
}

// TestAdminWorkerService_GetWorkerReportsADeletedOwner pins the case
// GetAdmin exists for.
//
// GetWorker used to read the worker row and then the owner row separately,
// and rebuilding the owner projection in Go got all three fields wrong for
// a soft-deleted owner: it reported the deleted account's username where
// the listing reports "", it left owner_deleted false on every row, and it
// turned a real store fault into "no owner". A LIVE owner reads the same
// under either implementation, so only a deleted one tells them apart.
//
// This test asserts both surfaces TOGETHER, because the value of one query
// is that the single-row read and the listing cannot disagree.
func TestAdminWorkerService_GetWorkerReportsADeletedOwner(t *testing.T) {
	env := setupAdminWorkerTest(t)
	ctx := context.Background()

	victim := storetest.SeedUser(t, env.st, "victim")
	worker := storetest.SeedWorker(t, env.st, victim.ID)

	live, err := env.client.GetWorker(ctx, authedReq(&leapmuxv1.AdminWorkerServiceGetWorkerRequest{
		Id: worker.ID,
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "victim", live.Msg.GetWorker().GetOwnerUsername())
	assert.False(t, live.Msg.GetWorker().GetOwnerDeleted())

	require.NoError(t, env.st.Users().Delete(ctx, victim.ID))

	deleted, err := env.client.GetWorker(ctx, authedReq(&leapmuxv1.AdminWorkerServiceGetWorkerRequest{
		Id: worker.ID,
	}, env.token))
	require.NoError(t, err, "the worker row outlives its owner and stays inspectable")
	assert.True(t, deleted.Msg.GetWorker().GetOwnerDeleted(),
		"a soft-deleted owner is reported as deleted")
	assert.Empty(t, deleted.Msg.GetWorker().GetOwnerUsername(),
		"a soft-deleted owner's username is freed, so it is not a handle the surface may print")
	assert.Equal(t, victim.ID, deleted.Msg.GetWorker().GetRegisteredBy(),
		"the owner id stays, which is the only stable handle left")

	listed, err := env.client.ListWorkers(ctx, authedReq(&leapmuxv1.AdminWorkerServiceListWorkersRequest{
		UserId: victim.ID,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetWorkers(), 1)
	assert.Equal(t, deleted.Msg.GetWorker().GetOwnerDeleted(), listed.Msg.GetWorkers()[0].GetOwnerDeleted(),
		"the single-row read and the listing report one projection")
	assert.Equal(t, deleted.Msg.GetWorker().GetOwnerUsername(), listed.Msg.GetWorkers()[0].GetOwnerUsername())
}

// TestAdminWorkerService_ListWorkersCapsAnOversizedPageLimit pins the
// ceiling on the cross-user listing. It is the widest read on the admin
// surface -- every worker of every user -- so a caller-chosen page size
// with no cap is the one that costs the most.
func TestAdminWorkerService_ListWorkersCapsAnOversizedPageLimit(t *testing.T) {
	env := setupAdminWorkerTest(t)
	ctx := context.Background()

	owner := userid.MustNew(env.userID)
	for range service.MaxPageLimit + 1 {
		require.NoError(t, env.st.Workers().Create(ctx, store.CreateWorkerParams{
			ID:              id.Generate(),
			AuthToken:       id.Generate(),
			RegisteredBy:    owner,
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
		}))
	}

	page, err := env.client.ListWorkers(ctx, authedReq(&leapmuxv1.AdminWorkerServiceListWorkersRequest{
		Limit: 100000,
	}, env.token))
	require.NoError(t, err)
	assert.Len(t, page.Msg.GetWorkers(), service.MaxPageLimit,
		"an oversized limit is capped, not honoured")
	require.NotEmpty(t, page.Msg.GetNextCursor(), "a capped page says where the next one starts")

	next, err := env.client.ListWorkers(ctx, authedReq(&leapmuxv1.AdminWorkerServiceListWorkersRequest{
		Limit: 100000, Cursor: page.Msg.GetNextCursor(),
	}, env.token))
	require.NoError(t, err)
	assert.Len(t, next.Msg.GetWorkers(), 1)
	assert.Empty(t, next.Msg.GetNextCursor())
}
