package service_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestListWorkers_RejectsMalformedCursor pins the API-boundary contract: a
// stale (pre-composite-format) or garbled opaque cursor is bad client input
// and must surface as InvalidArgument (400), not the Internal (500) that
// genuine store failures map to. The store's cursor decode wraps
// store.ErrInvalidCursor before any query runs, and ListWorkers classifies
// the store call's error via errors.Is instead of re-parsing the cursor.
func TestListWorkers_RejectsMalformedCursor(t *testing.T) {
	t.Parallel()

	st := testutil.OpenTestStore(t)
	svc := service.NewWorkerManagementService(st, workermgr.New(service.NewWorkerReachAuthorizer(st)), nil, nil, mail.NewStubSender(), mail.Renderer{}, servicetest.NewSettingsManager(t, st, nil), nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew("u1")})

	// Missing "_" delimiter -> store.ErrInvalidCursor -> InvalidArgument.
	_, err := svc.ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Cursor: "no-underscore-timestamp"},
	})
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// An unparseable timestamp half is also bad client input, not a server fault.
	_, err = svc.ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Cursor: "not-a-time_abc"},
	})
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// The empty first-page cursor stays valid: no error, an empty page, and no
	// next cursor for a user with no workers.
	resp, err := svc.ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetWorkers())
	assert.Empty(t, resp.GetPage().GetNextCursor())
}

// TestGetWorker_ReportsTimestampsAsInstants pins the worker's two instants as
// Timestamps. created_at is the stored row's creation time. last_seen_at is
// unset until the worker first connects, and set once the hub records a
// heartbeat, so a client never reads an empty string as a time.
func TestGetWorker_ReportsTimestampsAsInstants(t *testing.T) {
	t.Parallel()

	st := testutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "worker-owner")
	ownerID := userid.MustNew(owner.ID)
	workerID := seedWorkersForOwner(t, st, ownerID, 1)[0]
	row, err := st.Workers().GetOwned(context.Background(), store.GetOwnedWorkerParams{UserID: ownerID, WorkerID: workerID})
	require.NoError(t, err)

	svc := service.NewWorkerManagementService(st, workermgr.New(service.NewWorkerReachAuthorizer(st)), nil, nil, mail.NewStubSender(), mail.Renderer{}, servicetest.NewSettingsManager(t, st, nil), nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: ownerID})
	get := func() *leapmuxv1.Worker {
		t.Helper()
		resp, err := svc.GetWorker(ctx, &leapmuxv1.GetWorkerRequest{WorkerId: workerID})
		require.NoError(t, err)
		return resp.GetWorker()
	}

	fresh := get()
	require.NotNil(t, fresh.GetCreatedAt())
	assert.True(t, fresh.GetCreatedAt().AsTime().Equal(row.CreatedAt), "created_at is the stored creation time")
	assert.Nil(t, fresh.GetLastSeenAt(), "a worker that never connected has no last-seen time")

	require.NoError(t, st.Workers().UpdateLastSeen(context.Background(), workerID))
	seen := get()
	require.NotNil(t, seen.GetLastSeenAt(), "a heartbeat sets the last-seen time")
	assert.False(t, seen.GetLastSeenAt().AsTime().Before(row.CreatedAt), "the worker is seen after it was created")

	// The listing projects the same instants as the single read.
	listed, err := svc.ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{})
	require.NoError(t, err)
	require.Len(t, listed.GetWorkers(), 1)
	assert.True(t, listed.GetWorkers()[0].GetLastSeenAt().AsTime().Equal(seen.GetLastSeenAt().AsTime()))
}

// seedWorkersForOwner inserts n worker rows for one owner and returns their
// ids. It writes the row directly rather than through storetest.SeedWorker
// so a case that needs a page-sized set pays one INSERT per worker instead
// of an INSERT and a read-back.
func seedWorkersForOwner(t *testing.T, st store.Store, owner userid.UserID, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for range n {
		workerID := id.Generate()
		require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
			ID:           workerID,
			AuthToken:    id.Generate(),
			RegisteredBy: owner,
		}))
		ids = append(ids, workerID)
	}
	return ids
}

// TestListWorkers_CapsAnOversizedPageLimit pins the ceiling at the surface
// the defect was found on.
//
// This handler used a hand-written page default with NO ceiling, so
// `page.limit = 100000` returned the caller's whole worker row set in one
// response -- an uncapped read the caller chose the size of. Routing the
// page through NormalizePageParams caps it and returns a cursor instead, so
// the rest of the set costs another request.
//
// The test seeds the owner with one worker MORE than the ceiling, so a missing
// cap is visible as an over-long page rather than as a full one that merely
// happens to fit.
func TestListWorkers_CapsAnOversizedPageLimit(t *testing.T) {
	t.Parallel()

	st := testutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "worker-owner")
	ownerID := userid.MustNew(owner.ID)
	seeded := seedWorkersForOwner(t, st, ownerID, service.MaxPageLimit+1)

	svc := service.NewWorkerManagementService(st, workermgr.New(service.NewWorkerReachAuthorizer(st)), nil, nil, mail.NewStubSender(), mail.Renderer{}, servicetest.NewSettingsManager(t, st, nil), nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: ownerID})

	resp, err := svc.ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Limit: 100000},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetWorkers(), service.MaxPageLimit,
		"an oversized limit is capped at the ceiling, not honoured")
	require.NotEmpty(t, resp.GetPage().GetNextCursor(),
		"a capped page must say where the next one starts, so the rows past the cap stay reachable")

	// The cap must not LOSE rows: the cursor reaches the remainder, and the
	// two pages together are exactly what the test seeded.
	next, err := svc.ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Limit: 100000, Cursor: resp.GetPage().GetNextCursor()},
	})
	require.NoError(t, err)
	assert.Len(t, next.GetWorkers(), 1)
	assert.Empty(t, next.GetPage().GetNextCursor(), "the final page reports no more")

	seen := make(map[string]bool, len(seeded))
	for _, w := range resp.GetWorkers() {
		seen[w.GetId()] = true
	}
	for _, w := range next.GetWorkers() {
		seen[w.GetId()] = true
	}
	assert.Len(t, seen, len(seeded), "paging the capped listing still returns every worker")
}

// TestListWorkers_OmittedLimitReturnsRows pins the other end of the same
// normalization. store.ClampListLimit preserves a limit of 0 and the keyset
// query reads 0 as "return no rows", so a caller that simply omits `page`
// -- the proto3 default -- would get an empty page it cannot distinguish
// from an empty worker list.
func TestListWorkers_OmittedLimitReturnsRows(t *testing.T) {
	t.Parallel()

	st := testutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "worker-owner")
	ownerID := userid.MustNew(owner.ID)
	seedWorkersForOwner(t, st, ownerID, 3)

	svc := service.NewWorkerManagementService(st, workermgr.New(service.NewWorkerReachAuthorizer(st)), nil, nil, mail.NewStubSender(), mail.Renderer{}, servicetest.NewSettingsManager(t, st, nil), nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: ownerID})

	resp, err := svc.ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.GetWorkers(), 3, "an omitted limit takes the default, never zero")
	assert.Empty(t, resp.GetPage().GetNextCursor())
}

// TestNewWorkerManagementServiceRefusesAMissingCollaborator pins WHERE the
// hub answers a wiring omission: at construction, not on a request.
//
// Each of the four is reachable from a request path, and each nil used to
// break that path with a nil pointer dereference or a wrong answer.
// A nil store, a nil worker registry (workerToProto asks it for every row's
// online bit, so ListWorkers and GetWorker panic on the FIRST row they
// return), a nil settings manager (EmailRegistrationInstructions reads the
// SMTP gate from its snapshot), and a nil mail sender (the send that gate
// admits). The hub's own wiring passes all four, so the panic is a
// programming error a caller must never make -- and it now stops the
// startup that made it.
func TestNewWorkerManagementServiceRefusesAMissingCollaborator(t *testing.T) {
	t.Parallel()

	st := testutil.OpenTestStore(t)
	mgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	set := servicetest.NewSettingsManager(t, st, nil)
	sender := mail.NewStubSender()

	// want is the EXACT panic message, so each case pins WHICH collaborator
	// the constructor identifies rather than only that some panic happened.
	for name, tc := range map[string]struct {
		build func()
		want  string
	}{
		"no store": {func() {
			service.NewWorkerManagementService(nil, mgr, nil, nil, sender, mail.Renderer{}, set, nil)
		}, "service: NewWorkerManagementService requires a store"},
		"no worker registry": {func() {
			service.NewWorkerManagementService(st, nil, nil, nil, sender, mail.Renderer{}, set, nil)
		}, "service: NewWorkerManagementService requires a worker registry"},
		"no settings manager": {func() {
			service.NewWorkerManagementService(st, mgr, nil, nil, sender, mail.Renderer{}, nil, nil)
		}, "service: NewWorkerManagementService requires a settings manager"},
		"no mail sender": {func() {
			service.NewWorkerManagementService(st, mgr, nil, nil, nil, mail.Renderer{}, set, nil)
		}, "service: NewWorkerManagementService requires a mail sender (use mail.NewDisabledSender)"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.PanicsWithValue(t, tc.want, tc.build,
				"a required collaborator is refused at construction, not on a request")
		})
	}

	// The effect collaborators stay optional: a test that exercises only the
	// store half passes nil for both, the same rule WorkerDeregisterEffects
	// and auth.CredentialLifecycleEffects hold. So does the scope cache,
	// which the constructor replaces with a private one.
	assert.NotPanics(t, func() {
		service.NewWorkerManagementService(st, mgr, nil, nil, sender, mail.Renderer{}, set, nil)
	}, "the constructor supports a nil workers-changed notification, notifier and scope cache")
}
