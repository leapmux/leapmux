package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/settingsregistry"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// deletingAdminCtx carries the elevated administrator DeleteUser now
// requires. It seeds its OWN account rather than reusing the doomed owner,
// because the gate re-reads the acting account immediately before the write
// and the owner is the row this verb deletes.
func deletingAdminCtx(t *testing.T, ctx context.Context, st store.Store) context.Context {
	t.Helper()
	admin := storetest.SeedUser(t, st, "deleting-admin")
	return hubtestutil.ElevatedAdminContext(t, ctx, admin.ID)
}

// notifiedUsers reports which users the broadcaster holds a pending
// workers-changed event for. The debounce map is the earliest observable
// point: an internal test can read it without waiting on a flush timer.
func notifiedUsers(b *HubEventBroadcaster) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.pending))
	for userID := range b.pending {
		out = append(out, userID)
	}
	return out
}

func newDeregisterTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))
	return st
}

// TestAdminDeregisterNotifiesTheWorkersOwner pins the address of the
// workers-changed notification. It goes to the worker's OWNER, never to
// the administrator who typed the command, which is why the handler reads
// the row before it deregisters it.
//
// The admin surface used to run NONE of the deregister effects: nothing
// told the worker to stop, its memoized delegation scope kept outstanding
// tokens reaching across workers, and no client learned the list changed.
func TestAdminDeregisterNotifiesTheWorkersOwner(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "worker-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)

	broadcaster := NewHubEventBroadcaster(channelmgr.New(0))
	svc := NewAdminWorkerService(st, NewWorkerDeregisterEffects(nil, nil, broadcaster))

	_, err := svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{owner.ID}, notifiedUsers(broadcaster),
		"the owner is notified, not the acting administrator")
}

// TestAdminDeregisterRunsNoEffectWhenTheStoreRefuses pins the ordering
// rule from the other side: the effects must not run for a worker the
// store did not deregister. SendDeregister moves worker-manager state and can
// delete a connected worker on its acknowledgement, so running it for a worker
// that is still active would act on state the hub never reached.
func TestAdminDeregisterRunsNoEffectWhenTheStoreRefuses(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "worker-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)

	broadcaster := NewHubEventBroadcaster(channelmgr.New(0))
	svc := NewAdminWorkerService(st, NewWorkerDeregisterEffects(nil, nil, broadcaster))

	// Already deregistered: ForceDeregister matches no ACTIVE row.
	_, err := svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}))
	require.NoError(t, err)
	broadcaster.mu.Lock()
	broadcaster.pending = map[string]*pendingFlush{}
	broadcaster.mu.Unlock()

	_, err = svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}))
	require.Error(t, err)
	assert.Empty(t, notifiedUsers(broadcaster), "a refused deregister must run no effect")
}

// flagRecordingRegistry wraps a real workermgr.Manager, which holds no live
// connection, and records every MarkDeregistering call.
// The MarkDeregistering call proves an effect was attempted for each worker:
// the broadcaster's debounce map is keyed by user and cannot tell one
// worker from ten. Offline workers retain no containment marker.
type flagRecordingRegistry struct {
	*workermgr.Manager
	mu     sync.Mutex
	marked []string
}

func newFlagRecordingRegistry(st store.Store) *flagRecordingRegistry {
	return &flagRecordingRegistry{Manager: workermgr.New(NewWorkerReachAuthorizer(st))}
}

func (r *flagRecordingRegistry) MarkDeregistering(workerID string) {
	r.mu.Lock()
	r.marked = append(r.marked, workerID)
	r.mu.Unlock()
	r.Manager.MarkDeregistering(workerID)
}

// toldToStop reports which of workerIDs the notifier marked deregistering.
func (r *flagRecordingRegistry) toldToStop(workerIDs []string) []string {
	return r.recorded(workerIDs, r.marked)
}

func (r *flagRecordingRegistry) recorded(workerIDs, calls []string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, id := range calls {
		seen[id] = true
	}
	var out []string
	for _, id := range workerIDs {
		if seen[id] {
			out = append(out, id)
		}
	}
	return out
}

// newTestNotifier builds a notifier over reg, whose workers are all offline.
func newTestNotifier(t *testing.T, st store.Store, reg *flagRecordingRegistry) *notifier.Notifier {
	t.Helper()
	pending := workermgr.NewPendingRequests(func() time.Duration { return time.Second })
	return notifier.New(st, reg, pending, func() time.Duration { return time.Second }, nil)
}

// errWorkerReadFailed is what the fake store below reports for the one worker
// whose read must fail.
var errWorkerReadFailed = errors.New("worker read failed")

// failingWorkerReadStore serves every call from the real store except the
// notifier's read of ONE worker, which fails. It is how a case reaches the
// "this worker was not told to stop" branch: the durable change is already
// committed there.
type failingWorkerReadStore struct {
	store.Store
	failWorkerID string
}

func (s failingWorkerReadStore) Workers() store.WorkerStore {
	return failingWorkerReads{WorkerStore: s.Store.Workers(), failWorkerID: s.failWorkerID}
}

type failingWorkerReads struct {
	store.WorkerStore
	failWorkerID string
}

func (w failingWorkerReads) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	if id == w.failWorkerID {
		return nil, errWorkerReadFailed
	}
	return w.WorkerStore.GetByIDIncludeDeleted(ctx, id)
}

// TestDeleteUserTellsEveryWorkerToStop pins the user-deletion cascade. The
// transaction soft-deletes the rows, which is invisible to a running
// worker: without the effects each one keeps its Connect stream and its
// leases until it happens to reconnect.
//
// The observation is PER WORKER. The broadcaster's pending map is keyed by
// USER, so a case that reads only that map reports the same value for one
// worker and for ten -- it cannot see the cascade stopping after the first
// row, which is the regression the name promises to catch. The recording
// registry sees one MarkDeregistering call per worker, so "every worker" is
// what fails.
func TestDeleteUserTellsEveryWorkerToStop(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "doomed-owner")
	first := storetest.SeedWorker(t, st, owner.ID)
	second := storetest.SeedWorker(t, st, owner.ID)
	workerIDs := []string{first.ID, second.ID}

	broadcaster := NewHubEventBroadcaster(channelmgr.New(0))
	scopeCache := auth.NewDelegationScopeCache(st)
	reg := newFlagRecordingRegistry(st)
	svc := NewAdminUserService(AdminUserServiceDeps{
		Store:         st,
		WorkerEffects: NewWorkerDeregisterEffects(scopeCache, newTestNotifier(t, st, reg), broadcaster),
	})

	// One memoized delegation scope per worker, so the eviction branch is
	// observable per worker too.
	bearers := map[string]*auth.UserInfo{}
	for _, workerID := range workerIDs {
		bearer := &auth.UserInfo{
			ID:         userid.MustNew(owner.ID),
			Credential: auth.DelegationCredential(id.Generate(), workerID),
		}
		bearers[workerID] = bearer
		scope, err := scopeCache.Resolve(ctx, bearer)
		require.NoError(t, err)
		require.True(t, scope.Allows(otherWorkerID(workerIDs, workerID)),
			"an agent on its owner's own ACTIVE worker reaches that owner's other workers")
	}

	_, err := svc.DeleteUser(deletingAdminCtx(t, ctx, st), connectRequestForTest(&leapmuxv1.DeleteUserRequest{Id: owner.ID}))
	require.NoError(t, err)

	assert.ElementsMatch(t, workerIDs, reg.toldToStop(workerIDs),
		"EVERY worker of the deleted user is told to stop, not just the first")
	for _, workerID := range workerIDs {
		assert.False(t, reg.IsDeregistering(workerID),
			"a deleted, offline worker leaves no flag behind: there is nothing left to wait for")
	}
	assert.Equal(t, []string{owner.ID}, notifiedUsers(broadcaster),
		"the workers-changed event is addressed to the deleted user")

	for _, workerID := range workerIDs {
		scope, err := scopeCache.Resolve(ctx, bearers[workerID])
		require.NoError(t, err)
		assert.False(t, scope.Allows(otherWorkerID(workerIDs, workerID)),
			"each worker's memoized scope is evicted, so its tokens lose cross-worker reach at once")
	}
}

func TestDeleteUserIncludesAWorkerRegisteredBeforeTheOwnerLock(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "delete-admission-owner")
	first := storetest.SeedWorker(t, st, owner.ID)
	addedID := id.Generate()
	wrapped := &workerBeforeOwnerLockStore{Store: st, owner: owner.ID, workerID: addedID}
	reg := newFlagRecordingRegistry(st)
	svc := NewAdminUserService(AdminUserServiceDeps{
		Store:         wrapped,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, st, reg), nil),
	})

	_, err := svc.DeleteUser(deletingAdminCtx(t, ctx, st), &leapmuxv1.DeleteUserRequest{Id: owner.ID})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{first.ID, addedID}, reg.toldToStop([]string{first.ID, addedID}))
	for _, workerID := range []string{first.ID, addedID} {
		worker, err := st.Workers().GetByIDIncludeDeleted(ctx, workerID)
		require.NoError(t, err)
		assert.NotNil(t, worker.DeletedAt)
	}
}

func TestDeleteUserIncludesAnAlreadyDeregisteringWorker(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "delete-deregistering-owner")
	active := storetest.SeedWorker(t, st, owner.ID)
	deregistering := storetest.SeedWorker(t, st, owner.ID)
	n, err := st.Workers().ForceDeregister(ctx, deregistering.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	reg := newFlagRecordingRegistry(st)
	svc := NewAdminUserService(AdminUserServiceDeps{
		Store:         st,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, st, reg), nil),
	})

	_, err = svc.DeleteUser(deletingAdminCtx(t, ctx, st), &leapmuxv1.DeleteUserRequest{Id: owner.ID})
	require.NoError(t, err)
	workerIDs := []string{active.ID, deregistering.ID}
	assert.ElementsMatch(t, workerIDs, reg.toldToStop(workerIDs))
}

func TestDeleteUserRetriesAStopInstructionThatFailedForAConnectedWorker(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "connected-delete-owner")
	active := storetest.SeedWorker(t, st, owner.ID)
	deregistering := storetest.SeedWorker(t, st, owner.ID)
	_, err := st.Workers().ForceDeregister(ctx, deregistering.ID)
	require.NoError(t, err)
	registry := workermgr.New(NewWorkerReachAuthorizer(st))
	workerIDs := []string{active.ID, deregistering.ID}
	for _, workerID := range workerIDs {
		connection, _ := workermgrtest.NewRecordedConn(t, workerID)
		_, err := registry.Register(connection)
		require.NoError(t, err)
		t.Cleanup(func() { registry.Unregister(workerID, connection) })
	}
	sender := &recordingDeregisterSender{}
	failing := failingWorkerReadStore{Store: st, failWorkerID: deregistering.ID}
	firstNotifier := notifier.New(failing, registry, sender, func() time.Duration { return time.Second }, nil)
	require.Error(t, firstNotifier.SendDeregister(ctx, deregistering.ID))
	require.Empty(t, sender.workerIDs(), "the failed read must happen before an instruction reaches the sender")
	svc := NewAdminUserService(AdminUserServiceDeps{
		Store: st,
		WorkerEffects: NewWorkerDeregisterEffects(nil,
			notifier.New(st, registry, sender, func() time.Duration { return time.Second }, nil), nil),
	})
	_, err = svc.DeleteUser(deletingAdminCtx(t, ctx, st), &leapmuxv1.DeleteUserRequest{Id: owner.ID})
	require.NoError(t, err)
	assert.ElementsMatch(t, workerIDs, sender.workerIDs())
	for _, workerID := range workerIDs {
		assert.True(t, registry.IsDeregistering(workerID), "an acknowledged connection remains contained until it unregisters")
	}
}

type recordingDeregisterSender struct {
	mu   sync.Mutex
	seen []string
}

func (s *recordingDeregisterSender) SendAndWait(_ context.Context, conn *workermgr.Conn, message *leapmuxv1.ConnectResponse) (*leapmuxv1.ConnectRequest, error) {
	if message.GetDeregister() == nil {
		return nil, errors.New("the worker must receive a deregistration instruction")
	}
	s.mu.Lock()
	s.seen = append(s.seen, conn.WorkerID)
	s.mu.Unlock()
	select {
	case <-conn.Done():
		return nil, errors.New("the worker connection already ended")
	default:
	}
	return &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_DeregisterAck{DeregisterAck: &leapmuxv1.DeregisterAck{}},
	}, nil
}

func (s *recordingDeregisterSender) workerIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func TestDeleteUserReplacesTheWorkerSnapshotAfterTransactionRollback(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "delete-retry-owner")
	first := storetest.SeedWorker(t, st, owner.ID)
	rolledBackID, committedID := id.Generate(), id.Generate()
	reg := newFlagRecordingRegistry(st)
	wrapped := &retryWorkerDeletionStore{
		Store: st, owner: owner.ID, rolledBackID: rolledBackID, committedID: committedID,
		beforeCommit: func() error {
			reg.mu.Lock()
			defer reg.mu.Unlock()
			if len(reg.marked) != 0 {
				return errors.New("worker effects ran before the deletion committed")
			}
			return nil
		},
	}
	svc := NewAdminUserService(AdminUserServiceDeps{
		Store:         wrapped,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, st, reg), nil),
	})

	_, err := svc.DeleteUser(deletingAdminCtx(t, ctx, st), &leapmuxv1.DeleteUserRequest{Id: owner.ID})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{first.ID, committedID}, reg.toldToStop([]string{first.ID, committedID, rolledBackID}))
	_, err = st.Workers().GetByIDIncludeDeleted(ctx, rolledBackID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

type retryWorkerDeletionStore struct {
	store.Store
	owner        string
	rolledBackID string
	committedID  string
	beforeCommit func() error
	replayed     bool
}

func (s *retryWorkerDeletionStore) RunInUserAuthTransaction(ctx context.Context, owner userid.UserID, fn func(store.Store) error) error {
	if !owner.Matches(s.owner) || s.replayed {
		return s.Store.RunInUserAuthTransaction(ctx, owner, fn)
	}
	s.replayed = true
	rollback := errors.New("roll back the first deletion attempt")
	err := s.Store.RunInUserAuthTransaction(ctx, owner, func(tx store.Store) error {
		if err := tx.Workers().Create(ctx, store.CreateWorkerParams{
			ID: s.rolledBackID, AuthToken: id.Generate(), RegisteredBy: owner,
			PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
		}); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		if err := s.beforeCommit(); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		return err
	}
	if err := s.Store.Workers().Create(ctx, store.CreateWorkerParams{
		ID: s.committedID, AuthToken: id.Generate(), RegisteredBy: owner,
		PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
	}); err != nil {
		return err
	}
	return s.Store.RunInUserAuthTransaction(ctx, owner, func(tx store.Store) error {
		if err := fn(tx); err != nil {
			return err
		}
		return s.beforeCommit()
	})
}

type workerBeforeOwnerLockStore struct {
	store.Store
	owner    string
	workerID string
	inserted bool
}

func (s *workerBeforeOwnerLockStore) RunInUserAuthTransaction(ctx context.Context, owner userid.UserID, fn func(store.Store) error) error {
	if owner.Matches(s.owner) && !s.inserted {
		if err := s.Store.Workers().Create(ctx, store.CreateWorkerParams{
			ID: s.workerID, AuthToken: id.Generate(), RegisteredBy: owner,
			PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
		}); err != nil {
			return err
		}
		s.inserted = true
	}
	return s.Store.RunInUserAuthTransaction(ctx, owner, fn)
}

// otherWorkerID returns the id in ids that is not self. The cross-worker
// reach a deregistration strips is only visible against a DIFFERENT worker,
// so each assertion needs its partner.
func otherWorkerID(ids []string, self string) string {
	for _, candidate := range ids {
		if candidate != self {
			return candidate
		}
	}
	return ""
}

// TestDeleteUserContinuesWhenOneWorkerCannotBeTold pins the branch the
// cascade takes when an effect fails. The user IS deleted and the rows ARE
// soft-deleted by then, so the handler must log the miss and continue:
// reporting a failure would invite a retry of a deletion that already
// committed, and stopping would leave the REMAINING workers running.
func TestDeleteUserContinuesWhenOneWorkerCannotBeTold(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "doomed-owner")
	for range 3 {
		storetest.SeedWorker(t, st, owner.ID)
	}

	// Read the order the cascade walks, and fail its FIRST worker. The
	// listing orders by (created_at, id), and three workers seeded in one
	// instant are ordered by their random ids -- so failing a worker chosen
	// by name would leave the "continues after a failure" property untested
	// whenever that worker happened to come last.
	order, err := liveWorkerIDs(ctx, st, userid.MustNew(owner.ID))
	require.NoError(t, err)
	require.Len(t, order, 3)

	// Only the NOTIFIER sees the failing store; the service keeps the real
	// one, so the deletion itself commits normally.
	failing := failingWorkerReadStore{Store: st, failWorkerID: order[0]}
	reg := newFlagRecordingRegistry(st)
	svc := NewAdminUserService(AdminUserServiceDeps{
		Store:         st,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, failing, reg), nil),
	})

	logs := testutil.CaptureDefaultLogger(t)
	_, err = svc.DeleteUser(deletingAdminCtx(t, ctx, st), connectRequestForTest(&leapmuxv1.DeleteUserRequest{Id: owner.ID}))
	require.NoError(t, err, "a failed effect must not fail an already-committed deletion")

	for _, workerID := range order {
		assert.False(t, reg.IsDeregistering(workerID), "offline workers retain no containment marker")
	}
	assert.Equal(t, order, reg.toldToStop(order), "the cascade reaches every worker, the failing one included")

	logged := logs.String()
	assert.Contains(t, logged, "user deleted but one worker was not told to stop")
	assert.Contains(t, logged, order[0], "the log identifies the worker that missed its notification")
	assert.Contains(t, logged, "level=WARN", "a missed notification is a warning, not a silent drop")

	deleted, err := st.Users().GetByIDIncludeDeleted(ctx, owner.ID)
	require.NoError(t, err)
	assert.NotNil(t, deleted.DeletedAt, "the deletion still committed")
}

// TestDeleteUserTellsEveryWorkerPastTheFirstPage pins liveWorkerIDs' cursor
// loop. It collects the ids BEFORE the transaction soft-deletes the rows,
// one page at a time, and a loop that took only the first page would leave
// every worker past the page ceiling running with its stream and its leases
// intact -- and say nothing.
func TestDeleteUserTellsEveryWorkerPastTheFirstPage(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "doomed-owner")

	// One worker MORE than a page holds, so a single-page collect is short
	// by exactly one rather than by nothing.
	workerIDs := make([]string, 0, MaxPageLimit+1)
	for range MaxPageLimit + 1 {
		workerID := id.Generate()
		require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
			ID:              workerID,
			AuthToken:       id.Generate(),
			RegisteredBy:    userid.MustNew(owner.ID),
			PublicKey:       []byte{},
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
		}))
		workerIDs = append(workerIDs, workerID)
	}

	reg := newFlagRecordingRegistry(st)
	svc := NewAdminUserService(AdminUserServiceDeps{
		Store:         st,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, st, reg), nil),
	})
	_, err := svc.DeleteUser(deletingAdminCtx(t, ctx, st), connectRequestForTest(&leapmuxv1.DeleteUserRequest{Id: owner.ID}))
	require.NoError(t, err)

	assert.Len(t, reg.toldToStop(workerIDs), len(workerIDs),
		"the cursor loop reaches every page, not just the first")
	for _, workerID := range workerIDs {
		assert.False(t, reg.IsDeregistering(workerID), "offline workers retain no containment marker")
	}
}

// connectRequestForTest wraps a message in the request envelope the
// handlers take. These cases call the handler directly rather than through
// a mounted server, because what they assert is the EFFECT the handler
// runs after the store write, not the transport.
func connectRequestForTest[T any](msg *T) *T {
	return msg
}

// TestAdminDeregisterEvictsTheWorkersMemoizedScope pins the effect the doc
// calls the operator's containment action.
//
// A delegation token minted on a worker reaches every OTHER worker its user
// owns, and the cache memoizes the scope that says so for a TTL.
// Deregistering the worker is the one action an operator has against a
// compromised one, so the memo must be dropped at once: without the eviction
// the outstanding tokens keep their cross-worker reach for the rest of that
// TTL, and the containment action is inert for as long as it matters.
//
// A real cache is what makes the effect observable at all. Passing nil for
// it leaves every OTHER effect assertable and this one not, because each
// branch of Apply is nil-tolerant by design.
func TestAdminDeregisterEvictsTheWorkersMemoizedScope(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "worker-owner")
	minter := storetest.SeedWorker(t, st, owner.ID)
	sibling := storetest.SeedWorker(t, st, owner.ID)

	scopeCache := auth.NewDelegationScopeCache(st)
	bearer := &auth.UserInfo{
		ID:         userid.MustNew(owner.ID),
		Credential: auth.DelegationCredential(id.Generate(), minter.ID),
	}
	// Memoize the pre-deregistration scope. Without this the case would
	// pass on an empty cache and prove nothing.
	scope, err := scopeCache.Resolve(ctx, bearer)
	require.NoError(t, err)
	require.True(t, scope.Allows(sibling.ID),
		"an ACTIVE minter its user owns lends cross-worker reach")

	svc := NewAdminWorkerService(st, NewWorkerDeregisterEffects(scopeCache, nil, nil))
	_, err = svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: minter.ID,
	}))
	require.NoError(t, err)

	after, err := scopeCache.Resolve(ctx, bearer)
	require.NoError(t, err)
	assert.False(t, after.Allows(sibling.ID),
		"the deregistered worker's tokens lose cross-worker reach on the next resolve, not a TTL later")
	assert.True(t, after.Allows(minter.ID),
		"the token still talks back to the host it runs on")
}

// TestAdminDeregisterEvictsOnlyTheDeregisteredWorker pins the eviction's
// scope. It is keyed by minting worker, so deregistering one worker must
// not drop the memo of another -- a sweep that cleared the whole cache
// would turn one containment action into a burst of re-resolves for
// every delegation bearer the hub serves.
func TestAdminDeregisterEvictsOnlyTheDeregisteredWorker(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "worker-owner")
	doomed := storetest.SeedWorker(t, st, owner.ID)
	survivor := storetest.SeedWorker(t, st, owner.ID)

	scopeCache := auth.NewDelegationScopeCache(st)
	survivorBearer := &auth.UserInfo{
		ID:         userid.MustNew(owner.ID),
		Credential: auth.DelegationCredential(id.Generate(), survivor.ID),
	}
	scope, err := scopeCache.Resolve(ctx, survivorBearer)
	require.NoError(t, err)
	require.True(t, scope.Allows(doomed.ID))

	svc := NewAdminWorkerService(st, NewWorkerDeregisterEffects(scopeCache, nil, nil))
	_, err = svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: doomed.ID,
	}))
	require.NoError(t, err)

	// Retire the survivor's row BEHIND the cache, so its memo and its row
	// now disagree. A re-resolve reports the narrowed scope; a surviving
	// memo reports the old one. Without this step both a targeted eviction
	// and a cache-wide flush answer alike, because the survivor's row still
	// grants what its memo holds -- so the case would assert nothing.
	n, err := st.Workers().ForceDeregister(ctx, survivor.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	after, err := scopeCache.Resolve(ctx, survivorBearer)
	require.NoError(t, err)
	assert.True(t, after.Allows(doomed.ID),
		"one worker's deregistration must not drop another worker's memo, which would turn every containment action into a cache-wide burst of re-resolves")
}

// TestWorkerDeregisterEffectsToleratesAnAbsentCollaborator pins the
// nil-tolerance the type documents, which every case that builds a partial
// effects value depends on.
//
// This exercises each collaborator ALONE as well as together, so a guard
// present on one branch cannot stand in for a guard missing on another. A
// case that only builds one combination proves the guards that combination
// happens to reach, not each branch.
func TestWorkerDeregisterEffectsToleratesAnAbsentCollaborator(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "worker-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)

	// A nil VALUE, which is what an admin service built without effects
	// holds. Apply is a pointer method, so this is a nil-receiver call.
	var absent *WorkerDeregisterEffects
	assert.NoError(t, absent.Apply(ctx, worker.ID, owner.ID), "a nil effects value runs nothing")

	for name, effects := range map[string]*WorkerDeregisterEffects{
		"no collaborator at all": NewWorkerDeregisterEffects(nil, nil, nil),
		"scope cache only":       NewWorkerDeregisterEffects(auth.NewDelegationScopeCache(st), nil, nil),
		"notifier only":          NewWorkerDeregisterEffects(nil, newTestNotifier(t, st, newFlagRecordingRegistry(st)), nil),
		"broadcaster only":       NewWorkerDeregisterEffects(nil, nil, NewHubEventBroadcaster(channelmgr.New(0))),
	} {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, effects.Apply(ctx, worker.ID, owner.ID))
		})
	}
}

// TestWorkerDeregisterEffectsWrapsTheNotifierFault pins the one error Apply
// can report. The wrap identifies WHICH of the three effects failed -- the
// other two return nothing -- and it must keep the store fault reachable
// through errors.Is, so a caller can still classify it.
func TestWorkerDeregisterEffectsWrapsTheNotifierFault(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "worker-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)

	failing := failingWorkerReadStore{Store: st, failWorkerID: worker.ID}
	effects := NewWorkerDeregisterEffects(nil, newTestNotifier(t, failing, newFlagRecordingRegistry(st)), nil)

	err := effects.Apply(ctx, worker.ID, owner.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, errWorkerReadFailed, "the store fault stays reachable")
	assert.Contains(t, err.Error(), "send deregister", "the wrap identifies which effect failed")
}

// TestAdminDeregisterReportsAFailedEffect pins how that error reaches the
// caller. The row IS deregistering by then, so the administrator has to learn
// that the worker was not told to stop now -- unlike the user-deletion cascade,
// which logs instead because it cannot ask for a retry of an already-deleted
// user. TestAdminDeregisterRecoversWithoutTheNotifier shows the persisted
// intent finishing the deregistration anyway.
func TestAdminDeregisterReportsAFailedEffect(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "worker-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)

	failing := failingWorkerReadStore{Store: st, failWorkerID: worker.ID}
	svc := NewAdminWorkerService(st, NewWorkerDeregisterEffects(nil, newTestNotifier(t, failing, newFlagRecordingRegistry(st)), nil))

	_, err := svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "send deregister")
}

// ackingSender acknowledges every DEREGISTER the way a real worker does.
type ackingSender struct{}

func (ackingSender) SendAndWait(context.Context, *workermgr.Conn, *leapmuxv1.ConnectResponse) (*leapmuxv1.ConnectRequest, error) {
	return &leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_DeregisterAck{DeregisterAck: &leapmuxv1.DeregisterAck{}}}, nil
}

// assertDeregistrationRecovers finishes a deregistration whose notifier failed,
// both ways the persisted intent allows: the worker's next connect, and the
// cleanup sweep after the grace period.
func assertDeregistrationRecovers(t *testing.T, newStore func(t *testing.T) store.Store, deregister func(t *testing.T, st store.Store, effects *WorkerDeregisterEffects, ownerID, workerID string) error) {
	t.Helper()
	for _, recovery := range []string{"next connect", "cleanup sweep"} {
		t.Run(recovery, func(t *testing.T) {
			st := newStore(t)
			ctx := context.Background()
			owner := storetest.SeedUser(t, st, "recover-owner")
			worker := storetest.SeedWorker(t, st, owner.ID)

			failing := failingWorkerReadStore{Store: st, failWorkerID: worker.ID}
			effects := NewWorkerDeregisterEffects(nil, newTestNotifier(t, failing, newFlagRecordingRegistry(st)), nil)
			err := deregister(t, st, effects, owner.ID, worker.ID)
			require.Error(t, err, "the RPC reports the failed effect")
			assert.Contains(t, err.Error(), "send deregister")

			row, err := st.Workers().GetByAuthToken(ctx, worker.AuthToken)
			require.NoError(t, err, "a deregistering worker can still connect")
			require.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, row.Status, "the intent is persisted")
			require.NotNil(t, row.DeregisteringAt)

			switch recovery {
			case "next connect":
				reg := newFlagRecordingRegistry(st)
				connection, _ := workermgrtest.NewRecordedConn(t, worker.ID)
				_, err := reg.Register(connection)
				require.NoError(t, err)
				t.Cleanup(func() { reg.Unregister(worker.ID, connection) })
				restarted := notifier.New(st, reg, ackingSender{}, func() time.Duration { return time.Second }, nil)
				restarted.DeliverOnConnect(ctx, row, connection)
			case "cleanup sweep":
				n, err := st.Cleanup().CompleteStaleDeregistrations(ctx, row.DeregisteringAt.Add(time.Minute))
				require.NoError(t, err)
				require.Equal(t, int64(1), n)
			}
			deleted, err := st.Workers().GetByIDIncludeDeleted(ctx, worker.ID)
			require.NoError(t, err)
			assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, deleted.Status)
		})
	}
}

// TestOwnerDeregisterRecoversWithoutTheNotifier pins that the owner's
// deregister commits a recoverable state even when telling the worker fails:
// deregistering_at is the authority, not the notifier.
func TestOwnerDeregisterRecoversWithoutTheNotifier(t *testing.T) {
	assertDeregistrationRecovers(t, newDeregisterTestStore,
		func(t *testing.T, st store.Store, effects *WorkerDeregisterEffects, ownerID, workerID string) error {
			key := [32]byte{}
			ks, err := keystore.New(map[uint32][32]byte{1: key})
			require.NoError(t, err)
			set := settingsregistry.NewManager(st, ks)
			require.NoError(t, set.Load(context.Background()))
			svc := NewWorkerManagementService(st, workermgr.New(NewWorkerReachAuthorizer(st)), nil, nil, mail.NewStubSender(), mail.Renderer{}, set, nil)
			svc.deregisterEffects = effects
			ownerCtx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(ownerID)})
			_, err = svc.DeregisterWorker(ownerCtx, &leapmuxv1.DeregisterWorkerRequest{WorkerId: workerID})
			return err
		})
}

// TestAdminDeregisterRecoversWithoutTheNotifier is the administrator's twin.
func TestAdminDeregisterRecoversWithoutTheNotifier(t *testing.T) {
	assertDeregistrationRecovers(t, newDeregisterTestStore,
		func(t *testing.T, st store.Store, effects *WorkerDeregisterEffects, _, workerID string) error {
			svc := NewAdminWorkerService(st, effects)
			_, err := svc.DeregisterWorker(context.Background(), connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
				Id: workerID,
			}))
			return err
		})
}
