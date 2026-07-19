package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

// scopeFixture seeds a user who owns one worker, and returns both ids.
type scopeFixture struct {
	st       store.Store
	userID   string
	workerID string
}

func seedScopeUser(t *testing.T, st store.Store) scopeFixture {
	t.Helper()
	ctx := context.Background()

	orgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{
		ID: orgID, Name: "org-" + id.Generate()[:6],
	}))
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "u-" + id.Generate()[:6],
	}))
	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userID,
		PublicKey:       []byte("x25519"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))
	return scopeFixture{st: st, userID: userID, workerID: workerID}
}

func delegationUser(userID, minterID string) *auth.UserInfo {
	return &auth.UserInfo{
		ID:         userID,
		Credential: auth.DelegationCredential("tok-"+minterID, "ws-1", minterID),
	}
}

// A non-delegation credential carries no worker bound at all, and must resolve
// without touching the store -- a nil store makes that mechanical.
func TestResolveDelegationWorkerScope_NonDelegationIsUnboundedAndStoreFree(t *testing.T) {
	ctx := context.Background()
	for name, cred := range map[string]auth.CredentialIdentity{
		"session": auth.SessionCredential("s1"),
		"api":     auth.APICredential("a1"),
	} {
		t.Run(name, func(t *testing.T) {
			scope, err := auth.ResolveDelegationWorkerScope(ctx, nil, &auth.UserInfo{ID: "u1", Credential: cred})
			require.NoError(t, err)
			assert.False(t, scope.IsBounded(), "a non-delegation credential must carry no worker bound")
			assert.True(t, scope.Allows("any-worker"), "an unbounded scope must allow every worker")
		})
	}

	// The zero value now FAILS CLOSED (deny-all): a scope that forgot to be resolved
	// -- a dropped error, a forgotten constructor -- denies every worker rather than
	// silently granting a delegation bearer unbounded reach. UNBOUNDED must therefore
	// be constructed explicitly via UnboundedScope, which is what the non-delegation
	// resolve path above returns.
	assert.False(t, auth.DelegationWorkerScope{}.Allows("any-worker"),
		"the zero-value scope must fail closed, not allow every worker")
	assert.False(t, auth.DelegationWorkerScope{}.Allows(""),
		"the deny-all zero value denies even an empty (cleared) target")
	assert.True(t, auth.DelegationWorkerScope{}.IsBounded(),
		"the deny-all zero value is bounded: it constrains everything")
	// The explicitly-constructed unbounded scope allows everything and is unbounded.
	assert.True(t, auth.UnboundedScope().Allows("any-worker"))
	assert.False(t, auth.UnboundedScope().IsBounded())
}

// The token's user owns the minting worker: its agent is that user's own, on that
// user's own machine, so it reaches that user's other workers.
func TestResolveDelegationWorkerScope_OwnedMinterReachesOtherWorkers(t *testing.T) {
	f := seedScopeUser(t, hubtestutil.OpenTestStore(t))

	scope, err := auth.ResolveDelegationWorkerScope(
		context.Background(), f.st, delegationUser(f.userID, f.workerID))
	require.NoError(t, err)

	assert.True(t, scope.IsBounded())
	assert.True(t, scope.Allows(f.workerID), "a token always reaches the worker that minted it")
	assert.True(t, scope.Allows("some-other-worker-of-mine"),
		"owning the minter means the bearer is the user's own agent, so their other workers are in reach")
}

// The cross-tenant chain: worker A (owned by the attacker) mints a token carrying
// the VICTIM's identity, which a shared workspace legitimately produces. That token
// must reach A and nothing else -- above all not a worker the victim owns.
func TestResolveDelegationWorkerScope_ForeignMinterReachesOnlyItself(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	attacker := seedScopeUser(t, st)
	victim := seedScopeUser(t, st)

	// The bearer authenticates as the VICTIM but was minted by the ATTACKER's worker.
	scope, err := auth.ResolveDelegationWorkerScope(
		context.Background(), st, delegationUser(victim.userID, attacker.workerID))
	require.NoError(t, err)

	assert.True(t, scope.Allows(attacker.workerID), "the minting worker itself stays reachable")
	assert.False(t, scope.Allows(victim.workerID),
		"a token minted by another user's worker must not reach the victim's own worker")
}

// Deregistering a worker is the operator's one containment action against a
// compromised one. Its outstanding tokens must stop reaching that user's OTHER
// machines, or the action is inert for the token's remaining TTL.
func TestResolveDelegationWorkerScope_NonActiveMinterLosesCrossWorkerReach(t *testing.T) {
	ctx := context.Background()

	for name, status := range map[string]leapmuxv1.WorkerStatus{
		"deregistering": leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING,
		"unspecified":   leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED,
	} {
		t.Run(name, func(t *testing.T) {
			f := seedScopeUser(t, hubtestutil.OpenTestStore(t))
			require.NoError(t, f.st.Workers().SetStatus(ctx, store.SetWorkerStatusParams{
				ID: f.workerID, Status: status,
			}))

			scope, err := auth.ResolveDelegationWorkerScope(ctx, f.st, delegationUser(f.userID, f.workerID))
			require.NoError(t, err)

			assert.False(t, scope.Allows("another-worker-of-mine"),
				"a minter that is no longer ACTIVE must not lend its tokens cross-worker reach")
			assert.True(t, scope.Allows(f.workerID),
				"the minter stays reachable by itself; the target's own ACTIVE check gates that")
		})
	}
}

// A soft-deleted minter is a PERMANENT answer, not a store fault: GetByID filters
// deleted rows, and reporting that as a retryable internal error would invite a
// client to hammer a decision that can never change.
func TestResolveDelegationWorkerScope_DeletedMinterIsPermanentNotAFault(t *testing.T) {
	ctx := context.Background()
	f := seedScopeUser(t, hubtestutil.OpenTestStore(t))
	require.NoError(t, f.st.Workers().MarkDeleted(ctx, f.workerID))

	scope, err := auth.ResolveDelegationWorkerScope(ctx, f.st, delegationUser(f.userID, f.workerID))
	require.NoError(t, err, "a deleted minter is a definitive answer, not a store fault")
	assert.False(t, scope.Allows("another-worker-of-mine"),
		"a deleted minter lends no cross-worker reach")
}

// A delegation bearer whose minting worker cannot be loaded (a transient store
// fault) must fail CLOSED. The zero scope is UNBOUNDED (it is what a non-delegation
// credential legitimately gets), so returning it alongside the error would hand a
// caller that drops the error every worker its user owns. The deny-all scope makes
// that misuse mechanically safe: even without checking err, the scope admits nothing.
func TestResolveDelegationWorkerScope_StoreFaultFailsClosed(t *testing.T) {
	ctx := context.Background()
	f := seedScopeUser(t, hubtestutil.OpenTestStore(t))

	forcedErr := errors.New("forced worker lookup failure")
	st := workerLookupFailStore{
		Store:   f.st,
		workers: workerLookupFailWorkers{WorkerStore: f.st.Workers(), err: forcedErr},
	}

	scope, err := auth.ResolveDelegationWorkerScope(ctx, st, delegationUser(f.userID, f.workerID))
	require.ErrorIs(t, err, forcedErr)
	assert.False(t, scope.Allows(f.workerID),
		"an unscopable delegation bearer must not get an unbounded scope if its error is dropped")
	assert.False(t, scope.Allows("any-other-worker"), "a deny-all scope allows nothing")
	// IsBounded must ALSO report the deny-all scope as bounded. The sole value-consumer
	// (service.workerScopePredicate) keys off IsBounded, not Allows, and collapses an
	// unbounded scope to a nil (allow-every-worker) predicate. If IsBounded read the
	// deny-all scope as unbounded, a caller that dropped the resolve error would hand
	// a delegation bearer every worker its user owns -- the exact hole denyAll closes.
	assert.True(t, scope.IsBounded(),
		"a deny-all scope must report bounded so it cannot collapse to an unbounded predicate")
}

type workerLookupFailStore struct {
	store.Store
	workers store.WorkerStore
}

func (s workerLookupFailStore) Workers() store.WorkerStore { return s.workers }

type workerLookupFailWorkers struct {
	store.WorkerStore
	err error
}

func (s workerLookupFailWorkers) GetByID(context.Context, string) (*store.Worker, error) {
	return nil, s.err
}

// CheckDelegationWorkerScope's fast path: the two arms that need no store round trip
// must not take one. A nil store makes that mechanical -- a lookup panics.
func TestCheckDelegationWorkerScope_StoreFreeArms(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, auth.CheckDelegationWorkerScope(ctx, nil,
		&auth.UserInfo{ID: "u1", Credential: auth.SessionCredential("s1")}, "worker-target"),
		"a non-delegation credential must not be gated on the minting worker")

	require.NoError(t, auth.CheckDelegationWorkerScope(ctx, nil,
		delegationUser("u1", "worker-mint"), "worker-mint"),
		"a token must always reach the worker that minted it, without a store lookup")
}

// The single-target form and the resolved value must agree: the fast path is a
// strict subset of Allows, so a target the value form denies must be denied here too.
func TestCheckDelegationWorkerScope_AgreesWithResolvedScope(t *testing.T) {
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	attacker := seedScopeUser(t, st)
	victim := seedScopeUser(t, st)
	user := delegationUser(victim.userID, attacker.workerID)

	err := auth.CheckDelegationWorkerScope(ctx, st, user, victim.workerID)
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrDelegationWorkerOutOfScope,
		"a cross-tenant target is out of scope, not a store fault")

	scope, resolveErr := auth.ResolveDelegationWorkerScope(ctx, st, user)
	require.NoError(t, resolveErr)
	assert.False(t, scope.Allows(victim.workerID), "the value form must agree with the single-target form")
	assert.NoError(t, auth.CheckDelegationWorkerScope(ctx, st, user, attacker.workerID))
}

// DenyAllScope is the named fail-closed value every delegation-resolve error
// path returns. The zero value is the UNBOUNDED scope (empty minterID reads as
// "not a delegation bearer" and allows everything), so a future error path that
// drops the error and forgets the literal would hand a delegation bearer every
// worker -- the named constructor exists to make that mechanically harder. Pin
// it directly: it denies every target, including the empty/non-delegation shape
// the zero value would otherwise allow.
func TestDenyAllScope_DeniesEverything(t *testing.T) {
	scope := auth.DenyAllScope()
	for _, target := range []string{"worker-a", "worker-b", ""} {
		assert.False(t, scope.Allows(target),
			"a deny-all scope must not allow any target (target=%q)", target)
	}
}
