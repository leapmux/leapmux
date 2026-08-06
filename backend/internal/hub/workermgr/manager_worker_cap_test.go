package workermgr

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The per-account cap on LIVE worker connections -- the term that actually
// bounds worker-pool membership.
//
// Every registered connection is a member of the worker sendq.Pool holding a
// floor the pool may not reclaim, and the pool has no count term of its own. The
// row cap in WorkerConnectorService cannot supply one: it counts ACTIVE rows,
// while the member is created per Connect stream behind a lookup that admits a
// DEREGISTERING worker too. These tests pin the bound at the place membership is
// created.

func newCappedManager(t *testing.T, limit int64) *Manager {
	t.Helper()
	m := New(DenyAllReach())
	m.SetMaxWorkersPerUser(limit)
	return m
}

// registerOwned builds a fresh conn for (workerID, owner) and registers it,
// returning everything Register reported so a refusal is as inspectable as an
// admission.
func registerOwned(t *testing.T, m *Manager, workerID, owner string) (*Conn, bool, error) {
	t.Helper()
	conn, _ := newOwnedTestConn(t, workerID, owner, nil, nil)
	replaced, err := m.Register(conn)
	return conn, replaced, err
}

// liveFor reads the per-account tally the cap is computed from. Reaching into
// the field keeps the index itself under test: an assertion that only watched
// admissions would pass just as well against a tally that never released.
func liveFor(t *testing.T, m *Manager, owner string) int {
	t.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connsByOwner[owner]
}

func TestRegister_AdmitsUpToTheLiveWorkerCapAndRefusesPastIt(t *testing.T) {
	m := newCappedManager(t, 2)

	_, _, err := registerOwned(t, m, "w1", "alice")
	require.NoError(t, err, "the first connection is inside the cap")
	_, _, err = registerOwned(t, m, "w2", "alice")
	require.NoError(t, err, "and so is the Nth")

	refused, replaced, err := registerOwned(t, m, "w3", "alice")
	require.ErrorIs(t, err, ErrTooManyWorkers, "the N+1th must be refused")
	assert.False(t, replaced)
	assert.Contains(t, err.Error(), "max_workers_per_user",
		"the operator-facing key has to be in the message, or nobody knows what to raise")
	assert.Contains(t, err.Error(), "2", "the message must name the limit it refused against")

	assert.Nil(t, m.ConnForTrustedPath("w3"), "a refused connection must never be published")
	assert.ErrorIs(t, refused.Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed,
		"a refused connection is fenced: its handler returns before installing the teardown defer")
	assert.Equal(t, 2, liveFor(t, m, "alice"), "a refusal must not consume a slot either")
}

// The cap is per account, not per Hub: one user filling their allowance cannot
// refuse another's machines.
func TestRegister_LiveWorkerCapIsPerAccount(t *testing.T) {
	m := newCappedManager(t, 1)

	_, _, err := registerOwned(t, m, "w1", "alice")
	require.NoError(t, err)
	_, _, err = registerOwned(t, m, "w2", "alice")
	require.ErrorIs(t, err, ErrTooManyWorkers)

	_, _, err = registerOwned(t, m, "w3", "bob")
	assert.NoError(t, err, "bob's first worker is inside bob's cap regardless of alice")
	assert.Equal(t, 1, liveFor(t, m, "bob"))
}

// THE DEFECT. A deregistering Worker keeps its Connect stream -- that stream is
// how the Hub tells it to tear itself down -- so it keeps its pool membership
// too. The row cap stops counting it the moment the row flips to DEREGISTERING,
// which is what let register/deregister/register cycles grow membership without
// bound. Live membership must not budge until the connection is gone.
func TestRegister_DeregisteringWorkersStillHoldTheirSlot(t *testing.T) {
	m := newCappedManager(t, 2)

	_, _, err := registerOwned(t, m, "w1", "alice")
	require.NoError(t, err)
	_, _, err = registerOwned(t, m, "w2", "alice")
	require.NoError(t, err)

	m.MarkDeregistering("w1")
	m.MarkDeregistering("w2")

	_, _, err = registerOwned(t, m, "w3", "alice")
	require.ErrorIs(t, err, ErrTooManyWorkers,
		"a worker being torn down still holds its queue floor, so it still holds its slot")
	assert.Equal(t, 2, liveFor(t, m, "alice"))

	// And the slot comes back only when the stream actually ends.
	require.True(t, m.Unregister("w1", m.ConnForTrustedPath("w1")))
	_, _, err = registerOwned(t, m, "w3", "alice")
	assert.NoError(t, err, "the disconnect, not the deregistration, is what frees the slot")
}

func TestUnregister_FreesExactlyOneSlot(t *testing.T) {
	m := newCappedManager(t, 2)

	first, _, err := registerOwned(t, m, "w1", "alice")
	require.NoError(t, err)
	_, _, err = registerOwned(t, m, "w2", "alice")
	require.NoError(t, err)

	require.True(t, m.Unregister("w1", first))
	assert.Equal(t, 1, liveFor(t, m, "alice"))

	_, _, err = registerOwned(t, m, "w3", "alice")
	require.NoError(t, err, "the freed slot must be usable")
	_, _, err = registerOwned(t, m, "w4", "alice")
	assert.ErrorIs(t, err, ErrTooManyWorkers, "exactly one slot was freed, not two")
}

// A nil conn matches the nil an absent key yields, so it must be refused before
// the equality test rather than by it: otherwise unregistering a worker that was
// never registered reports success and releases a slot nobody held.
func TestUnregister_NilConnReleasesNothing(t *testing.T) {
	m := newCappedManager(t, 1)

	assert.False(t, m.Unregister("never-registered", nil),
		"a worker that was never registered cannot be removed")

	live, _, err := registerOwned(t, m, "w1", "alice")
	require.NoError(t, err)
	assert.False(t, m.Unregister("w1", nil), "and a nil conn is never the registered one")
	assert.Equal(t, 1, liveFor(t, m, "alice"), "the live connection must keep its slot")
	assert.Same(t, live, m.ConnForTrustedPath("w1"), "nor may it unpublish the real one")
}

// The tally must not outlive the connections it counted, or a Hub that has seen
// many accounts carries one map entry per account forever.
func TestUnregister_EmptiesTheOwnerIndex(t *testing.T) {
	m := newCappedManager(t, 2)

	first, _, err := registerOwned(t, m, "w1", "alice")
	require.NoError(t, err)
	second, _, err := registerOwned(t, m, "w2", "alice")
	require.NoError(t, err)

	require.True(t, m.Unregister("w1", first))
	require.True(t, m.Unregister("w2", second))

	m.mu.RLock()
	defer m.mu.RUnlock()
	assert.Empty(t, m.connsByOwner, "an emptied bucket is deleted, not left at zero")
}

// A reconnecting Worker REPLACES its own connection rather than adding one. If
// the replacement consumed a second slot, a Worker at the cap could never
// reconnect -- and permanently, since the predecessor's handler only lets go
// once the replacement has taken over.
func TestRegister_ReplacingAConnectionDoesNotConsumeASecondSlot(t *testing.T) {
	m := newCappedManager(t, 1)

	first, _, err := registerOwned(t, m, "w1", "alice")
	require.NoError(t, err)

	second, replaced, err := registerOwned(t, m, "w1", "alice")
	require.NoError(t, err, "a Worker reconnecting into its own slot must be admitted")
	assert.True(t, replaced)
	assert.Equal(t, 1, liveFor(t, m, "alice"), "the account still holds exactly one connection")
	assert.Same(t, second, m.ConnForTrustedPath("w1"))

	// The superseded connection's deferred cleanup still runs. It must not hand
	// the account back a slot the replacement is still occupying.
	assert.False(t, m.Unregister("w1", first),
		"a stale conn is not the registered one, so it removes nothing")
	assert.Equal(t, 1, liveFor(t, m, "alice"), "and it must release nothing either")

	_, _, err = registerOwned(t, m, "w2", "alice")
	assert.ErrorIs(t, err, ErrTooManyWorkers,
		"the reconnect leaked a slot if a second worker fits under a cap of one")
}

// A worker id reappearing under a different registrant moves its slot between
// accounts instead of being counted in one and released from the other. Nothing
// changes registered_by today; the accounting is written so that if something
// ever does, the tally stays equal to what conns actually holds.
func TestRegister_ReplacingUnderANewOwnerMovesTheSlot(t *testing.T) {
	m := newCappedManager(t, 1)

	_, _, err := registerOwned(t, m, "w1", "alice")
	require.NoError(t, err)

	_, replaced, err := registerOwned(t, m, "w1", "bob")
	require.NoError(t, err, "bob has no connections yet, so the takeover is inside bob's cap")
	assert.True(t, replaced)
	assert.Equal(t, 0, liveFor(t, m, "alice"), "alice's slot was released, not stranded")
	assert.Equal(t, 1, liveFor(t, m, "bob"))

	_, _, err = registerOwned(t, m, "w2", "alice")
	assert.NoError(t, err, "alice may use the slot she got back")
	_, _, err = registerOwned(t, m, "w3", "bob")
	assert.ErrorIs(t, err, ErrTooManyWorkers, "and bob is now at his own cap")
}

func TestRegister_ZeroAndNegativeCapsAreUnlimited(t *testing.T) {
	for name, limit := range map[string]int64{"zero": 0, "negative": -1} {
		t.Run(name, func(t *testing.T) {
			m := New(DenyAllReach())
			if limit != 0 {
				m.SetMaxWorkersPerUser(limit)
			}
			for _, workerID := range []string{"w1", "w2", "w3", "w4", "w5"} {
				_, _, err := registerOwned(t, m, workerID, "alice")
				require.NoError(t, err, "%s means unlimited, so no count refuses anything", name)
			}
			assert.Equal(t, 5, liveFor(t, m, "alice"))
		})
	}
}

// The check and the publish share one critical section, so a burst cannot slip
// past by having every racer read the same under-cap count. Run under -race:
// this is the assertion that fails if the check ever moves outside the lock.
func TestRegister_ConcurrentBurstNeverExceedsTheCap(t *testing.T) {
	const (
		limit   = 5
		racers  = 64
		account = "alice"
	)
	m := newCappedManager(t, limit)

	// Every racer gets its own worker id, so an admission can only come from a
	// free slot and never from replacing a sibling.
	conns := make([]*Conn, racers)
	for i := range conns {
		conns[i], _ = newOwnedTestConn(t, fmt.Sprintf("burst-worker-%d", i), account, nil, nil)
	}

	// One result slot per racer rather than a shared counter: each goroutine
	// writes only its own element, so the outcomes are collected without adding
	// synchronization that could mask the race under test.
	results := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(racers)
	for i := range conns {
		go func() {
			defer wg.Done()
			<-start
			_, results[i] = m.Register(conns[i])
		}()
	}
	close(start)
	wg.Wait()

	admitted := 0
	for i, err := range results {
		if err == nil {
			admitted++
			assert.Same(t, conns[i], m.ConnForTrustedPath(conns[i].WorkerID),
				"an admitted connection must be the published one")
			continue
		}
		assert.ErrorIs(t, err, ErrTooManyWorkers,
			"the only refusal an unfenced registry can produce here is the cap")
	}
	assert.Equal(t, limit, admitted, "a burst must be bounded by the cap, not by scheduling luck")
	assert.Equal(t, limit, liveFor(t, m, account))
}
