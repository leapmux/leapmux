package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

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
// the row before it flips the status.
//
// The admin surface used to run NONE of the deregister effects: the worker
// was never told to stop, its memoized delegation scope kept outstanding
// tokens reaching across workers, no client learned the list changed, and
// the row stuck at DEREGISTERING forever because only the notifier's
// acknowledgement path calls MarkDeleted.
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
// store did not deregister. SendDeregister persists a notification row and
// moves worker-manager state, so telling a client the list changed for a
// worker that is still active is state the caller was told it never
// reached.
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

// newTestNotifier builds a notifier over a registry that holds no live
// connection, so SendDeregister always falls through to the durable queue.
// That fallback is what makes the notifier a PER-WORKER observation point:
// each worker told to stop leaves a worker_notifications row a case can read
// back, where the broadcaster's debounce map is keyed by user and cannot
// tell one worker from ten.
func newTestNotifier(t *testing.T, st store.Store) *notifier.Notifier {
	t.Helper()
	pending := workermgr.NewPendingRequests(func() time.Duration { return time.Second })
	return notifier.New(st, workermgr.New(NewWorkerReachAuthorizer(st)), pending,
		func() time.Duration { return time.Second })
}

// deregisterNotifiedWorkers reports which of the given workers hold a queued
// deregister notification.
func deregisterNotifiedWorkers(t *testing.T, st store.Store, workerIDs []string) []string {
	t.Helper()
	var out []string
	for _, workerID := range workerIDs {
		queued, err := st.WorkerNotifications().ListPendingByWorker(context.Background(), workerID)
		require.NoError(t, err)
		for _, n := range queued {
			if n.Type == leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER {
				out = append(out, workerID)
				break
			}
		}
	}
	return out
}

// errNotificationWriteFailed is what the fake store below reports for the
// one worker whose notification write must fail.
var errNotificationWriteFailed = errors.New("notification write failed")

// failingNotificationStore serves every call from the real store except the
// notification write for ONE worker, which fails. It is how a case reaches
// the "this worker was not told to stop" branch: the deletion is already
// committed there, so the handler logs and carries on rather than reporting
// a failure that invites a retry of a deletion that cannot be repeated.
type failingNotificationStore struct {
	store.Store
	failWorkerID string
}

func (s failingNotificationStore) WorkerNotifications() store.WorkerNotificationStore {
	return failingNotifications{
		WorkerNotificationStore: s.Store.WorkerNotifications(),
		failWorkerID:            s.failWorkerID,
	}
}

type failingNotifications struct {
	store.WorkerNotificationStore
	failWorkerID string
}

func (n failingNotifications) Create(ctx context.Context, p store.CreateWorkerNotificationParams) error {
	if p.WorkerID == n.failWorkerID {
		return errNotificationWriteFailed
	}
	return n.WorkerNotificationStore.Create(ctx, p)
}

// TestDeleteUserTellsEveryWorkerToStop pins the user-deletion cascade. The
// transaction soft-deletes the rows, which is invisible to a running
// worker: without the effects each one keeps its Connect stream and its
// leases until it happens to reconnect.
//
// The observation is PER WORKER. The broadcaster's pending map is keyed by
// USER, so a case that reads only that map reports the same value for one
// worker and for ten -- it cannot see the cascade stopping after the first
// row, which is the regression the name promises to catch. The notifier
// leaves one queued row per worker, so "every worker" is what fails.
func TestDeleteUserTellsEveryWorkerToStop(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "doomed-owner")
	first := storetest.SeedWorker(t, st, owner.ID)
	second := storetest.SeedWorker(t, st, owner.ID)
	workerIDs := []string{first.ID, second.ID}

	broadcaster := NewHubEventBroadcaster(channelmgr.New(0))
	scopeCache := auth.NewDelegationScopeCache(st)
	svc := NewAdminUserService(AdminUserServiceDeps{
		Store:         st,
		WorkerEffects: NewWorkerDeregisterEffects(scopeCache, newTestNotifier(t, st), broadcaster),
	})

	// One memoized delegation scope per worker, so the eviction arm is
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

	_, err := svc.DeleteUser(ctx, connectRequestForTest(&leapmuxv1.DeleteUserRequest{Id: owner.ID}))
	require.NoError(t, err)

	assert.ElementsMatch(t, workerIDs, deregisterNotifiedWorkers(t, st, workerIDs),
		"EVERY worker of the deleted user is told to stop, not just the first")
	assert.Equal(t, []string{owner.ID}, notifiedUsers(broadcaster),
		"the workers-changed event is addressed to the deleted user")

	for _, workerID := range workerIDs {
		scope, err := scopeCache.Resolve(ctx, bearers[workerID])
		require.NoError(t, err)
		assert.False(t, scope.Allows(otherWorkerID(workerIDs, workerID)),
			"each worker's memoized scope is evicted, so its tokens lose cross-worker reach at once")
	}
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

// TestDeleteUserCarriesOnWhenOneWorkerCannotBeTold pins the branch the
// cascade takes when an effect fails. The user IS deleted and the rows ARE
// soft-deleted by then, so the handler must log the miss and keep going:
// reporting a failure would invite a retry of a deletion that already
// committed, and stopping would leave the REMAINING workers running.
func TestDeleteUserCarriesOnWhenOneWorkerCannotBeTold(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "doomed-owner")
	for range 3 {
		storetest.SeedWorker(t, st, owner.ID)
	}

	// Read the order the cascade walks, and fail its FIRST worker. The
	// listing orders by (created_at, id), and three workers seeded in one
	// instant are ordered by their random ids -- so failing a worker chosen
	// by name would leave the "carries on" property untested whenever that
	// worker happened to come last.
	order, err := NewAdminUserService(AdminUserServiceDeps{Store: st}).liveWorkerIDs(ctx, userid.MustNew(owner.ID))
	require.NoError(t, err)
	require.Len(t, order, 3)

	// Only the NOTIFIER sees the failing store; the service keeps the real
	// one, so the deletion itself commits normally.
	failing := failingNotificationStore{Store: st, failWorkerID: order[0]}
	svc := NewAdminUserService(AdminUserServiceDeps{
		Store:         st,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, failing), nil),
	})

	logs := testutil.CaptureDefaultLogger(t)
	_, err = svc.DeleteUser(ctx, connectRequestForTest(&leapmuxv1.DeleteUserRequest{Id: owner.ID}))
	require.NoError(t, err, "a failed effect must not fail an already-committed deletion")

	assert.Equal(t, order[1:], deregisterNotifiedWorkers(t, st, order),
		"every worker after the failing one is still told to stop")

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

	svc := NewAdminUserService(AdminUserServiceDeps{
		Store:         st,
		WorkerEffects: NewWorkerDeregisterEffects(nil, newTestNotifier(t, st), nil),
	})
	_, err := svc.DeleteUser(ctx, connectRequestForTest(&leapmuxv1.DeleteUserRequest{Id: owner.ID}))
	require.NoError(t, err)

	assert.Len(t, deregisterNotifiedWorkers(t, st, workerIDs), len(workerIDs),
		"the cursor loop reaches every page, not just the first")
}

// connectRequestForTest wraps a message in the request envelope the
// handlers take. These cases call the handler directly rather than through
// a mounted server, because what they assert is the EFFECT the handler
// runs after the store write, not the transport.
func connectRequestForTest[T any](msg *T) *connect.Request[T] {
	return connect.NewRequest(msg)
}

// TestAdminDeregisterEvictsTheWorkersMemoizedScope pins the effect the doc
// calls the operator's containment action.
//
// A delegation token minted on a worker reaches every OTHER worker its user
// owns, and the scope that says so is memoized for a TTL. Deregistering the
// worker is the one action an operator has against a compromised one, so
// the memo must be dropped at once: without the eviction the outstanding
// tokens keep their cross-worker reach for the rest of that TTL, and the
// containment action is inert for as long as it matters.
//
// A real cache is what makes the effect observable at all. Passing nil for
// it leaves every OTHER effect assertable and this one not, because each
// arm of Apply is nil-tolerant by design.
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
// would turn one containment action into a stampede of re-resolves for
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
		"one worker's deregistration must not drop another worker's memo, which would turn every containment action into a cache-wide stampede")
}

// TestWorkerDeregisterEffectsToleratesAnAbsentCollaborator pins the
// nil-tolerance the type documents, which every case that builds a partial
// effects value depends on.
//
// Each collaborator is exercised ALONE as well as together, so a guard
// present on one arm cannot stand in for a guard missing on another. A case
// that only builds one combination proves the guards that combination
// happens to reach, not each arm.
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
		"notifier only":          NewWorkerDeregisterEffects(nil, newTestNotifier(t, st), nil),
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

	failing := failingNotificationStore{Store: st, failWorkerID: worker.ID}
	effects := NewWorkerDeregisterEffects(nil, newTestNotifier(t, failing), nil)

	err := effects.Apply(ctx, worker.ID, owner.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, errNotificationWriteFailed, "the store fault stays reachable")
	assert.Contains(t, err.Error(), "send deregister", "the wrap identifies which effect failed")
}

// TestAdminDeregisterReportsAFailedEffect pins how that error reaches the
// caller. The row IS flipped by then, so the administrator has to learn the
// worker was not told to stop -- unlike the user-deletion cascade, which
// logs instead because it cannot ask for a retry of an already-deleted user.
func TestAdminDeregisterReportsAFailedEffect(t *testing.T) {
	st := newDeregisterTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "worker-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)

	failing := failingNotificationStore{Store: st, failWorkerID: worker.ID}
	svc := NewAdminWorkerService(st, NewWorkerDeregisterEffects(nil, newTestNotifier(t, failing), nil))

	_, err := svc.DeregisterWorker(ctx, connectRequestForTest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{
		Id: worker.ID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "send deregister")
}
