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

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
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
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
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

// workersChangedRecorder stands in for the CRDT registry's
// BroadcastWorkersChanged and records each user it was called for, in call
// order, so a test sees one entry per notification rather than a set.
type workersChangedRecorder struct {
	mu    sync.Mutex
	users []string
}

func (r *workersChangedRecorder) notify(userID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users = append(r.users, userID)
}

func (r *workersChangedRecorder) notified() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.users...)
}

// reset forgets every recorded notification.
func (r *workersChangedRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users = nil
}

func newDeregisterTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{}, hubtestutil.TestKeystore(t))
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
// newTestAdminUserService supplies the required notice owner. These tests
// mint nothing, so nothing waits on it.
func newTestAdminUserService(deps AdminUserServiceDeps) *AdminUserService {
	deps.Notices = NewCredentialNotices(nil, mail.Renderer{})
	return NewAdminUserService(deps)
}

func TestAdminDeregisterNotifiesTheWorkersOwner(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "worker-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)

	recorder := &workersChangedRecorder{}
	svc := NewAdminWorkerService(st, workermgr.New(workermgr.DenyAllReach()), NewWorkerDeregisterEffects(nil, nil, recorder.notify))

	_, err := svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{owner.ID}, recorder.notified(),
		"the owner is notified once, not the acting administrator")
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

	recorder := &workersChangedRecorder{}
	svc := NewAdminWorkerService(st, workermgr.New(workermgr.DenyAllReach()), NewWorkerDeregisterEffects(nil, nil, recorder.notify))

	// Already deregistered: ForceDeregister matches no ACTIVE row.
	_, err := svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}))
	require.NoError(t, err)
	recorder.reset()

	_, err = svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}))
	require.Error(t, err)
	assert.Empty(t, recorder.notified(), "a refused deregister must run no effect")
}

// flagRecordingRegistry wraps a real workermgr.Manager, which holds no live
// connection, and records every MarkDeregistering call.
// The MarkDeregistering call proves an effect was attempted for each worker:
// the workers-changed notification is addressed to a user and cannot tell
// which worker it was for. Offline workers retain no containment marker.
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
func newTestNotifier(t *testing.T, reg *flagRecordingRegistry) *notifier.Notifier {
	t.Helper()
	pending := workermgr.NewPendingRequests(func() time.Duration { return time.Second })
	return notifier.New(reg, pending, func() time.Duration { return time.Second })
}

// TestDeleteUserTellsEveryWorkerToStop pins the user-deletion cascade. The
// transaction soft-deletes the rows, which is invisible to a running
// worker: without the effects each one keeps its Connect stream and its
// leases until it happens to reconnect.
//
// The observation is PER WORKER. The workers-changed notification carries
// only the USER, so a case that reads only it cannot tell which worker each
// call was for -- it cannot see the cascade stopping at the wrong row, which
// is the regression the name promises to catch. The recording registry sees
// one MarkDeregistering call per worker, so "every worker" is what fails.
func TestDeleteUserTellsEveryWorkerToStop(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "doomed-owner")
	first := storetest.SeedWorker(t, st, owner.ID)
	second := storetest.SeedWorker(t, st, owner.ID)
	workerIDs := []string{first.ID, second.ID}

	recorder := &workersChangedRecorder{}
	scopeCache := auth.NewDelegationScopeCache(st)
	reg := newFlagRecordingRegistry(st)
	svc := newTestAdminUserService(AdminUserServiceDeps{
		Store:         st,
		WorkerEffects: NewWorkerDeregisterEffects(scopeCache, newTestNotifier(t, reg), recorder.notify),
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

	_, err := svc.DeleteUser(deletingAdminCtx(t, ctx, st), connectRequestForTest(&leapmuxv1.DeleteUserRequest{User: SelectUserByID(owner.ID)}))
	require.NoError(t, err)

	assert.ElementsMatch(t, workerIDs, reg.toldToStop(workerIDs),
		"EVERY worker of the deleted user is told to stop, not just the first")
	for _, workerID := range workerIDs {
		assert.False(t, reg.IsDeregistering(workerID),
			"a deleted, offline worker leaves no flag behind: there is nothing left to wait for")
	}
	assert.Equal(t, []string{owner.ID, owner.ID}, recorder.notified(),
		"one workers-changed event per deregistered worker, each addressed to the deleted user")

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
	svc := newTestAdminUserService(AdminUserServiceDeps{
		Store:         wrapped,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, reg), nil),
	})

	_, err := svc.DeleteUser(deletingAdminCtx(t, ctx, st), &leapmuxv1.DeleteUserRequest{User: SelectUserByID(owner.ID)})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{first.ID, addedID}, reg.toldToStop([]string{first.ID, addedID}))
	for _, workerID := range []string{first.ID, addedID} {
		worker, err := st.Workers().GetByIDIncludeDeleted(ctx, workerID)
		require.NoError(t, err)
		assert.NotNil(t, worker.DeletedAt)
	}
}

func TestDeleteUserNotifiesAllConnectedWorkers(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "connected-delete-owner")
	first := storetest.SeedWorker(t, st, owner.ID)
	second := storetest.SeedWorker(t, st, owner.ID)
	registry := workermgr.New(NewWorkerReachAuthorizer(st))
	workerIDs := []string{first.ID, second.ID}
	for _, workerID := range workerIDs {
		connection := workermgrtest.NewConn(t, workerID)
		_, err := registry.Register(connection)
		require.NoError(t, err)
		t.Cleanup(func() { registry.Unregister(workerID, connection) })
	}
	sender := &recordingDeregisterSender{}
	svc := newTestAdminUserService(AdminUserServiceDeps{
		Store: st,
		WorkerEffects: NewWorkerDeregisterEffects(nil,
			notifier.New(registry, sender, func() time.Duration { return time.Second }), nil),
	})
	_, err := svc.DeleteUser(deletingAdminCtx(t, ctx, st), &leapmuxv1.DeleteUserRequest{User: SelectUserByID(owner.ID)})
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
	svc := newTestAdminUserService(AdminUserServiceDeps{
		Store:         wrapped,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, reg), nil),
	})

	_, err := svc.DeleteUser(deletingAdminCtx(t, ctx, st), &leapmuxv1.DeleteUserRequest{User: SelectUserByID(owner.ID)})
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

// Replay the complete deletion transaction. The user-auth callback must not
// retry only part of its writes.
func (s *retryWorkerDeletionStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	if s.replayed {
		return s.Store.RunInTransaction(ctx, fn)
	}
	s.replayed = true
	rollback := errors.New("roll back the first deletion attempt")
	err := s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		if err := tx.Workers().Create(ctx, store.CreateWorkerParams{
			ID: s.rolledBackID, AuthTokenHash: hubtestutil.TestSecretHasher().Hash(id.Generate()), RegisteredBy: userid.MustNew(s.owner),
			PublicKeys: storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
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
		ID: s.committedID, AuthTokenHash: hubtestutil.TestSecretHasher().Hash(id.Generate()), RegisteredBy: userid.MustNew(s.owner),
		PublicKeys: storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
	}); err != nil {
		return err
	}
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		if err := fn(tx); err != nil {
			return err
		}
		return s.beforeCommit()
	})
}

func (s *retryWorkerDeletionStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(store.Store) error) error {
	return s.RunInTransaction(ctx, func(tx store.Store) error {
		return tx.RunInUserAuthTransaction(ctx, userID, fn)
	})
}

type workerBeforeOwnerLockStore struct {
	store.Store
	owner    string
	workerID string
	inserted bool
}

// The worker commits before the deletion transaction begins. SQLite
// cannot admit a separate registration after that transaction takes its writer lock.
func (s *workerBeforeOwnerLockStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	if !s.inserted {
		if err := s.Store.Workers().Create(ctx, store.CreateWorkerParams{
			ID: s.workerID, AuthTokenHash: hubtestutil.TestSecretHasher().Hash(id.Generate()), RegisteredBy: userid.MustNew(s.owner),
			PublicKeys: storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
		}); err != nil {
			return err
		}
		s.inserted = true
	}
	return s.Store.RunInTransaction(ctx, fn)
}

func (s *workerBeforeOwnerLockStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(store.Store) error) error {
	return s.RunInTransaction(ctx, func(tx store.Store) error {
		return tx.RunInUserAuthTransaction(ctx, userID, fn)
	})
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
	workerIDs := make([]string, 0, contracts.MaxPageLimit+1)
	for range contracts.MaxPageLimit + 1 {
		workerID := id.Generate()
		require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
			ID:            workerID,
			AuthTokenHash: hubtestutil.TestSecretHasher().Hash(id.Generate()),
			RegisteredBy:  userid.MustNew(owner.ID),
			PublicKeys:    storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
		}))
		workerIDs = append(workerIDs, workerID)
	}

	reg := newFlagRecordingRegistry(st)
	svc := newTestAdminUserService(AdminUserServiceDeps{
		Store:         st,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, reg), nil),
	})
	_, err := svc.DeleteUser(deletingAdminCtx(t, ctx, st), connectRequestForTest(&leapmuxv1.DeleteUserRequest{User: SelectUserByID(owner.ID)}))
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

	svc := NewAdminWorkerService(st, workermgr.New(workermgr.DenyAllReach()), NewWorkerDeregisterEffects(scopeCache, nil, nil))
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

	svc := NewAdminWorkerService(st, workermgr.New(workermgr.DenyAllReach()), NewWorkerDeregisterEffects(scopeCache, nil, nil))
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
	assert.NotPanics(t, func() { absent.Apply(ctx, worker.ID, owner.ID) }, "a nil effects value runs nothing")

	for name, effects := range map[string]*WorkerDeregisterEffects{
		"no collaborator at all": NewWorkerDeregisterEffects(nil, nil, nil),
		"scope cache only":       NewWorkerDeregisterEffects(auth.NewDelegationScopeCache(st), nil, nil),
		"notifier only":          NewWorkerDeregisterEffects(nil, newTestNotifier(t, newFlagRecordingRegistry(st)), nil),
		"notification only":      NewWorkerDeregisterEffects(nil, nil, (&workersChangedRecorder{}).notify),
	} {
		t.Run(name, func(t *testing.T) {
			assert.NotPanics(t, func() { effects.Apply(ctx, worker.ID, owner.ID) })
		})
	}
}

// TestDeregisterImmediatelyDeletesAndRevokesBearerToken pins that both owner
// and administrator deregistration soft-delete the worker immediately, revoking
// its bearer token across all Hub endpoints with no grace window.
func TestDeregisterImmediatelyDeletesAndRevokesBearerToken(t *testing.T) {
	surfaces := map[string]func(t *testing.T, st store.Store, effects *WorkerDeregisterEffects, ownerID, workerID string) error{
		"owner": func(t *testing.T, st store.Store, effects *WorkerDeregisterEffects, ownerID, workerID string) error {
			set := settingsregistry.NewManager(st)
			require.NoError(t, set.Load(context.Background()))
			svc := NewWorkerManagementService(st, hubtestutil.TestSecretHasher(), workermgr.New(NewWorkerReachAuthorizer(st)), effects, mail.NewStubSender(), mail.Renderer{}, set)
			ownerCtx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(ownerID)})
			_, err := svc.DeregisterWorker(ownerCtx, &leapmuxv1.DeregisterWorkerRequest{WorkerId: workerID})
			return err
		},
		"admin": func(t *testing.T, st store.Store, effects *WorkerDeregisterEffects, _, workerID string) error {
			svc := NewAdminWorkerService(st, workermgr.New(workermgr.DenyAllReach()), effects)
			_, err := svc.DeregisterWorker(context.Background(), connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
				Id: workerID,
			}))
			return err
		},
	}
	for name, deregister := range surfaces {
		t.Run(name, func(t *testing.T) {
			st := newDeregisterTestStore(t)
			ctx := context.Background()
			owner := storetest.SeedUser(t, st, "immediate-revoke-"+name)
			worker, workerToken := hubtestutil.CreateTestWorker(t, st, owner.ID)

			effects := NewWorkerDeregisterEffects(nil, newTestNotifier(t, newFlagRecordingRegistry(st)), nil)
			require.NoError(t, deregister(t, st, effects, owner.ID, worker.ID))

			row, err := st.Workers().GetByIDIncludeDeleted(ctx, worker.ID)
			require.NoError(t, err)
			assert.Equal(t, leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, row.Status)
			require.NotNil(t, row.DeletedAt)

			_, err = auth.AuthenticateWorkerBearer(ctx, st, hubtestutil.TestSecretHasher(), "Bearer "+workerToken)
			assert.ErrorIs(t, err, auth.ErrWorkerDeleted, "bearer token must be refused immediately after deregister")

			repeatErr := deregister(t, st, effects, owner.ID, worker.ID)
			require.Error(t, repeatErr)
			assert.Equal(t, connect.CodeNotFound, connect.CodeOf(repeatErr), "repeating deregister on an already-deleted worker returns NotFound")
		})
	}
}
