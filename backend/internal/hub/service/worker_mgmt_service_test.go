package service_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
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
	_, err := svc.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Cursor: "no-underscore-timestamp"},
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// An unparseable timestamp half is also bad client input, not a server fault.
	_, err = svc.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Cursor: "not-a-time_abc"},
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// The empty first-page cursor stays valid: no error, an empty page, and no
	// next cursor for a user with no workers.
	resp, err := svc.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetWorkers())
	assert.False(t, resp.Msg.GetPage().GetHasMore())
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
			ID:              workerID,
			AuthToken:       id.Generate(),
			RegisteredBy:    owner,
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
		}))
		ids = append(ids, workerID)
	}
	return ids
}

// TestListWorkers_CapsAnOversizedPageLimit pins the ceiling at the surface
// the defect was found on.
//
// This handler used a hand-rolled page default with NO ceiling, so
// `page.limit = 100000` returned the caller's whole worker row set in one
// response -- an unbounded read the caller chose the size of. Routing the
// page through AdminPageParams caps it and hands back a cursor instead, so
// the rest of the set costs another request.
//
// The owner is seeded with one worker MORE than the ceiling, so a missing
// cap is visible as an over-long page rather than as a full one that merely
// happens to fit.
func TestListWorkers_CapsAnOversizedPageLimit(t *testing.T) {
	t.Parallel()

	st := testutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "worker-owner")
	ownerID := userid.MustNew(owner.ID)
	seeded := seedWorkersForOwner(t, st, ownerID, service.MaxAdminPageLimit+1)

	svc := service.NewWorkerManagementService(st, workermgr.New(service.NewWorkerReachAuthorizer(st)), nil, nil, mail.NewStubSender(), mail.Renderer{}, servicetest.NewSettingsManager(t, st, nil), nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: ownerID})

	resp, err := svc.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Limit: 100000},
	}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetWorkers(), service.MaxAdminPageLimit,
		"an oversized limit is capped at the ceiling, not honoured")
	assert.True(t, resp.Msg.GetPage().GetHasMore(), "the rows past the cap are still reachable")
	require.NotEmpty(t, resp.Msg.GetPage().GetNextCursor(),
		"a capped page must say where the next one starts")

	// The cap must not LOSE rows: the cursor reaches the remainder, and the
	// two pages together are exactly what was seeded.
	next, err := svc.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Limit: 100000, Cursor: resp.Msg.GetPage().GetNextCursor()},
	}))
	require.NoError(t, err)
	assert.Len(t, next.Msg.GetWorkers(), 1)
	assert.False(t, next.Msg.GetPage().GetHasMore(), "the final page reports no more")
	assert.Empty(t, next.Msg.GetPage().GetNextCursor())

	seen := make(map[string]bool, len(seeded))
	for _, w := range resp.Msg.GetWorkers() {
		seen[w.GetId()] = true
	}
	for _, w := range next.Msg.GetWorkers() {
		seen[w.GetId()] = true
	}
	assert.Len(t, seen, len(seeded), "paging the capped listing still returns every worker")
}

// TestListWorkers_OmittedLimitReturnsRows pins the other end of the same
// normalization. store.ClampListLimit preserves a limit of 0 and the keyset
// query reads 0 as "return no rows", so a caller that simply omits `page`
// -- the proto3 default -- would get an empty page it cannot tell apart
// from an empty worker list.
func TestListWorkers_OmittedLimitReturnsRows(t *testing.T) {
	t.Parallel()

	st := testutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "worker-owner")
	ownerID := userid.MustNew(owner.ID)
	seedWorkersForOwner(t, st, ownerID, 3)

	svc := service.NewWorkerManagementService(st, workermgr.New(service.NewWorkerReachAuthorizer(st)), nil, nil, mail.NewStubSender(), mail.Renderer{}, servicetest.NewSettingsManager(t, st, nil), nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: ownerID})

	resp, err := svc.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetWorkers(), 3, "an omitted limit takes the default, never zero")
	assert.False(t, resp.Msg.GetPage().GetHasMore())
}

// TestNewWorkerManagementServiceRefusesAMissingCollaborator pins WHERE a
// wiring omission is answered: at construction, not on a request.
//
// Each of the four is reachable from a request path, and each nil used to
// take that path down with a nil pointer dereference or a wrong answer.
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
	// the constructor named rather than only that some panic happened.
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
	}, "a nil broadcaster, notifier and scope cache are all supported")
}
