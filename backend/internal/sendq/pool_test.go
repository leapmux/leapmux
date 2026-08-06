package sendq

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noEvict is the member hook for pool tests that never exercise reclaiming.
// Failing loudly rather than silently ignoring the call keeps a test that
// accidentally triggers an eviction from passing for the wrong reason.
func noEvict(tb testing.TB) func(error) bool {
	tb.Helper()
	return func(error) bool {
		tb.Error("evict called by a test that should not reclaim")
		return false
	}
}

// fillFrom charges size-byte frames until the pool refuses, returning how many
// bytes the member ended up holding -- read back from the member's own ledger,
// which is the same one the admission rule consults.
func fillFrom(m *PoolMember, size int64) int64 {
	for m.charge(size, 0) == Admitted { //nolint:revive // filling to the ceiling is the point
	}
	return m.Charged()
}

// A pool declared refuse-asker must never nominate a peer, whatever kind of
// member does the asking.
//
// The user-events pool has always behaved this way, but only because nothing
// there happened to call relieve -- an omission in each member rather than a
// rule. The first Writer given that pool would have started evicting
// subscribers whose own documentation promises they are never reclaimed.
func TestPoolRefuseAskerNeverNominatesAPeer(t *testing.T) {
	t.Parallel()

	const capacity = 8 << 20
	p := NewPool(PoolConfig{
		Capacity: capacity, MinFloor: 1 << 20, MaxFloor: 1 << 20,
		Reclaim: ReclaimRefuseAsker,
	})

	// A hog holding far more than the asker: under evict-largest this is exactly
	// the member relieve would tear down.
	hog := p.Attach(func(error) bool {
		t.Error("a refuse-asker pool must never evict a peer")
		return false
	})
	require.Equal(t, Admitted, hog.charge(4<<20, 0))

	asker := p.Attach(noEvict(t))
	require.Equal(t, Admitted, asker.charge(1<<10, 0))

	assert.Equal(t, relieveNoHog, asker.relieve(capacity, 0),
		"the asker must be told to shed itself, not handed somebody else's memory -- "+
			"and it is well inside its floor, so its own backlog is not the finding")
	assert.Equal(t, int64(0), p.Evictions(), "and nothing may be counted as evicted")
	assert.Equal(t, int64(4<<20), hog.Charged(), "the hog keeps every byte it held")
}

// The other half of that verdict. A refuse-asker pool still has to say WHY it is
// shedding the asker, and the two answers have opposite fixes: a connection past
// the working set it was guaranteed is a slow client, one inside it is an
// undersized deployment. Skipping the scan is not a reason to skip the question,
// and answering it with the same floor test the scanning path uses is what keeps
// the two paths from disagreeing.
func TestPoolRefuseAskerStillDistinguishesAHogFromPressure(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		floor    = 1 << 20
	)
	p := NewPool(PoolConfig{
		Capacity: capacity, MinFloor: floor, MaxFloor: floor,
		Reclaim: ReclaimRefuseAsker,
	})

	peer := p.Attach(noEvict(t))
	require.Equal(t, Admitted, peer.charge(4<<20, 0))

	asker := p.Attach(noEvict(t))
	require.Equal(t, Admitted, asker.charge(floor+1, 0))

	assert.Equal(t, relieveAskerAtFault, asker.relieve(capacity, 0),
		"an asker holding more than its guarantee is the hog, scan or no scan")
}

// The default is unchanged: a pool that says nothing still evicts its largest
// holder, which is what the relay and worker classes rely on.
func TestPoolDefaultsToEvictLargest(t *testing.T) {
	t.Parallel()

	const capacity = 8 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 1 << 10, MaxFloor: 1 << 10})

	evicted := make(chan struct{}, 1)
	hog := p.Attach(func(error) bool {
		evicted <- struct{}{}
		return true
	})
	require.Equal(t, Admitted, hog.charge(4<<20, 0))

	asker := p.Attach(noEvict(t))
	require.Equal(t, relieveEvicted, asker.relieve(capacity, 0))
	select {
	case <-evicted:
	default:
		t.Fatal("the default policy must nominate the largest holder")
	}
	assert.Equal(t, int64(1), p.Evictions())
}

func TestPoolLoneWriterGetsAboutHalfTheCapacity(t *testing.T) {
	t.Parallel()

	// Floors pinned tiny so the dynamic threshold, not the floor, is what the
	// assertion measures.
	const capacity = 64 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 4096, MaxFloor: 4096})
	m := p.Attach(noEvict(t))

	held := fillFrom(m, 64<<10)

	// q + size <= C - used with used == q settles at q ~ C/2. That is the
	// property that makes a lone connection generous on an idle machine
	// without ever letting it take the whole budget.
	assert.InDelta(t, capacity/2, held, float64(capacity)*0.02)
	assert.Equal(t, int64(held), p.Used())
	assert.Positive(t, int64(capacity)-p.Used(), "a reserve must remain for newcomers")
}

func TestPoolSharesFairlyAndAlwaysKeepsAReserve(t *testing.T) {
	t.Parallel()

	const (
		capacity = 64 << 20
		writers  = 7
	)
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 4096, MaxFloor: 4096})

	members := make([]*PoolMember, writers)
	for i := range members {
		members[i] = p.Attach(noEvict(t))
	}

	// Round-robin rather than one-at-a-time: filling sequentially would let the
	// first writer reach C/2 before the others ever asked, which measures
	// arrival order instead of the steady state.
	for progressed := true; progressed; {
		progressed = false
		for _, m := range members {
			if m.charge(64<<10, 0) == Admitted {
				progressed = true
			}
		}
	}

	// N backed-up writers converge on C/(N+1) each, leaving C/(N+1) free.
	expected := capacity / (writers + 1)
	for i, m := range members {
		assert.InDelta(t, expected, m.Charged(), float64(expected)*0.25,
			"writer %d should hold about its fair share", i)
	}
	assert.LessOrEqual(t, p.Used(), int64(capacity))
	assert.Positive(t, int64(capacity)-p.Used(),
		"the rule must always leave headroom for a connection that has not arrived yet")
}

func TestPoolDynamicBranchNeverExceedsCapacity(t *testing.T) {
	t.Parallel()

	// Floors at 1 byte remove the deliberate overcommit, leaving only the
	// dynamic branch -- which is the branch that is supposed to be provably
	// self-limiting.
	const capacity = 1 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 1, MaxFloor: 1})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := p.Attach(func(error) bool { return true })
			defer m.Detach()
			for range 500 {
				if m.charge(4096, 0) == Admitted {
					assert.LessOrEqual(t, p.Used(), int64(capacity),
						"concurrent admissions must never overshoot")
					continue
				}
				// Give some back so the next round exercises a refill rather
				// than a permanently full pool.
				if m.Charged() > 0 {
					m.Release(4096)
				}
			}
		}()
	}
	wg.Wait()

	assert.Zero(t, p.Used(), "every charge must be refunded exactly once")
	assert.Zero(t, p.Members(), "every member must detach exactly once")
	assert.Zero(t, p.Overcommits(), "the dynamic branch alone must not overcommit")
}

func TestPoolGuaranteesTheFloorUnderFullPressure(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		floor    = 1 << 20
	)
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

	// Saturate with several hogs, not one: a lone writer settles at half the
	// capacity, which leaves the pool with room and would not exercise the
	// floor at all.
	hogs := make([]*PoolMember, 12)
	for i := range hogs {
		hogs[i] = p.Attach(func(error) bool { return true })
	}
	for progressed := true; progressed; {
		progressed = false
		for _, h := range hogs {
			if h.charge(64<<10, 0) == Admitted {
				progressed = true
			}
		}
	}
	require.LessOrEqual(t, int64(capacity)-p.Used(), int64(floor),
		"precondition: the dynamic threshold must have collapsed to the floor")

	// A connection that arrives to a saturated pool must still be able to hold
	// its guaranteed working set. Without this a full pool would tear down every
	// healthy connection on its very next frame.
	newcomer := p.Attach(noEvict(t))
	require.Equal(t, Admitted, newcomer.charge(64<<10, 0))
	assert.Equal(t, int64(64<<10), newcomer.Charged())

	// ...but only up to the floor. Past it the newcomer competes like anyone
	// else, or the guarantee would be an unbounded second budget. Filled for
	// real rather than asserted, so the ceiling is reached through the same
	// ledger the admission rule reads.
	for newcomer.charge(64<<10, 0) == Admitted { //nolint:revive // filling to the ceiling is the point
	}
	assert.Equal(t, int64(floor), newcomer.Charged(),
		"the guarantee is exactly the floor, no more and no less")
	assert.Equal(t, Pressure, newcomer.charge(64<<10, 0))
}

func TestPoolFloorShrinksWithTheWriterCount(t *testing.T) {
	t.Parallel()

	const capacity = 16 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 1 << 20, MaxFloor: 4 << 20})

	// One writer: capacity/1 is way past MaxFloor, so the generous end applies.
	m := p.Attach(noEvict(t))
	assert.Equal(t, int64(4<<20), p.floor())

	// Eight writers: capacity/8 == 2 MiB sits between the two clamps, so the
	// floor tracks the fair share -- and while it does, the SUM of all floors is
	// exactly Capacity. Past MinFloor it stops tracking; see
	// TestPoolFloorsOvershootWithTheMemberCount for what that costs.
	for range 7 {
		p.Attach(noEvict(t))
	}
	assert.Equal(t, int64(2<<20), p.floor())

	// Sixty-four writers: capacity/64 == 256 KiB is below MinFloor, so the
	// floor stops shrinking. Past this point floors DO overcommit, which is
	// what Overcommits() reports.
	for range 56 {
		p.Attach(noEvict(t))
	}
	assert.Equal(t, int64(1<<20), p.floor())

	m.Detach()
	assert.Equal(t, int64(63), p.Members())
}

func TestPoolPrivatePoolReproducesAFixedBudget(t *testing.T) {
	t.Parallel()

	// This is the shape newWriter builds for a Config with only MaxBytes. The
	// point of the test is that "fixed per-connection budget" needs no separate
	// code path: it is this rule with the floor pinned to the capacity.
	const maxBytes = 1 << 20
	p := NewPool(PoolConfig{Capacity: maxBytes, MinFloor: maxBytes, MaxFloor: maxBytes})
	m := p.Attach(noEvict(t))

	// The ceiling must be MaxBytes at EVERY occupancy, not just an empty queue.
	// Each round brings the member to a real occupancy first, then asks for
	// exactly the remainder -- which a fixed budget must always grant.
	for _, queued := range []int64{0, maxBytes / 4, maxBytes / 2, maxBytes - 4096} {
		if queued > 0 {
			require.Equal(t, Admitted, m.charge(queued, 0))
		}
		require.Equal(t, Admitted, m.charge(maxBytes-queued, 0),
			"a private pool must admit right up to MaxBytes at occupancy %d", queued)
		require.Equal(t, int64(maxBytes), m.Charged())
		m.Release(int64(maxBytes))
	}

	// And one byte past it is refused at every occupancy too.
	require.Equal(t, Admitted, m.charge(maxBytes, 0))
	assert.Equal(t, Pressure, m.charge(1, 0))
	m.Release(maxBytes)
	assert.Zero(t, p.Used())
}

func TestPoolChargeRejectsAnItemNoOccupancyCouldFit(t *testing.T) {
	t.Parallel()

	p := NewPool(PoolConfig{Capacity: 1 << 20, MinFloor: 4096, MaxFloor: 4096})
	m := p.Attach(noEvict(t))

	// Unfittable is about the item, not the backlog: an oversized frame can
	// never be admitted, so a caller that would otherwise park must fail fast.
	assert.Equal(t, Unfittable, m.charge((1<<20)+1, 0))
	// The control reserve is headroom the data path may not use, so it lowers
	// the largest admissible item too.
	assert.Equal(t, Unfittable, m.charge(1<<20, 4096))
	// An ordinary backlog is pressure, NOT unfittable -- confusing the two is
	// what turns a caller that should have waited into one that fails. The
	// member really holds the backlog now rather than asserting one, so the
	// verdict is reached from the same ledger production reads.
	require.Equal(t, Admitted, m.charge(1<<20, 0))
	require.Equal(t, int64(1<<20), m.Charged())
	assert.Equal(t, Pressure, m.charge(4096, 0))
}

func TestPoolRelieveReclaimsFromTheLargestHolder(t *testing.T) {
	t.Parallel()

	const capacity = 8 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 64 << 10, MaxFloor: 64 << 10})

	var evicted []string
	var mu sync.Mutex
	record := func(name string) func(error) bool {
		return func(err error) bool {
			mu.Lock()
			defer mu.Unlock()
			assert.ErrorIs(t, err, ErrPoolPressure)
			evicted = append(evicted, name)
			return true
		}
	}

	small := p.Attach(record("small"))
	large := p.Attach(record("large"))
	require.Equal(t, Admitted, small.charge(512<<10, 0))
	require.Equal(t, Admitted, large.charge(4<<20, 0))

	// The asker is the SMALL holder. The naive rule -- refuse whoever spoke
	// next -- would drop it; the point of choosing a victim is that the
	// connection actually holding the memory is the one that goes.
	//
	// `want` is past anything the pool could grant, so this is real pressure
	// rather than a stale reading.
	assert.Equal(t, relieveEvicted, small.relieve(capacity, 0))
	assert.Equal(t, []string{"large"}, evicted)
	assert.Equal(t, int64(1), p.Evictions())
}

func TestPoolRelieveBlamesTheAskerWhenItIsTheLargest(t *testing.T) {
	t.Parallel()

	p := NewPool(PoolConfig{Capacity: 8 << 20, MinFloor: 64 << 10, MaxFloor: 64 << 10})
	hog := p.Attach(noEvict(t))
	bystander := p.Attach(noEvict(t))
	require.Equal(t, Admitted, hog.charge(4<<20, 0))
	require.Equal(t, Admitted, bystander.charge(64<<10, 0))

	// Nobody holds more than the asker, so there is nothing to reclaim and the
	// asker's own backlog is the finding. A bystander must not be taken down
	// for it.
	assert.Equal(t, relieveAskerAtFault, hog.relieve(8<<20, 0))
	assert.Zero(t, p.Evictions())
}

func TestPoolRelieveWillNotEvictAWriterInsideItsFloor(t *testing.T) {
	t.Parallel()

	const floor = 1 << 20
	p := NewPool(PoolConfig{Capacity: 4 << 20, MinFloor: floor, MaxFloor: floor})
	asker := p.Attach(noEvict(t))
	peer := p.Attach(noEvict(t))
	require.Equal(t, Admitted, peer.charge(floor, 0))

	// The peer holds more than the asker but no more than it was PROMISED.
	// Reclaiming from it would make the floor a lie, so the honest answer is
	// that the pool is undersized -- not that this connection misbehaved.
	assert.Equal(t, relieveNoHog, asker.relieve(4<<20, 0))
	assert.Zero(t, p.Evictions())
}

func TestPoolCountsOvercommitsWhenFloorsExceedCapacity(t *testing.T) {
	t.Parallel()

	// Four writers each guaranteed a quarter... plus a fifth. The floor clamps
	// at MinFloor and stops tracking the fair share, so the promises now add up
	// to more than the pool holds.
	const capacity = 4 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 1 << 20, MaxFloor: 1 << 20})

	members := make([]*PoolMember, 5)
	for i := range members {
		members[i] = p.Attach(func(error) bool { return true })
	}
	for _, m := range members {
		require.Equal(t, Admitted, m.charge(1<<20, 0))
	}

	assert.Positive(t, p.Overcommits(),
		"granting a floor the pool had no room for must be visible to an operator")
	assert.Greater(t, p.Used(), int64(capacity))
	// The overshoot is members x floor, NOT a second capacity: floor() clamps up
	// to MinFloor, so every member past capacity/MinFloor is still promised a
	// full floor and the sum keeps growing. Asserting "at most 2 x capacity"
	// here would have passed only because five members happens to be under the
	// eight this pool can honour.
	assert.Equal(t, int64(len(members))*(1<<20), p.Used())
}

// The floors are bounded by Capacity only while the pool can honour them all.
// Past that the guarantee is per-member and the total grows with the connection
// count -- which is the honest limit of what this type bounds, and the thing
// Overcommits() exists to report.
func TestPoolFloorsOvershootWithTheMemberCount(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		floor    = 1 << 20
		// Well past capacity/floor == 8, so the clamp is what decides.
		members = 64
	)
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

	held := make([]*PoolMember, members)
	for i := range held {
		held[i] = p.Attach(func(error) bool { return true })
		require.Equal(t, Admitted, held[i].charge(floor, 0),
			"member %d must still be granted the floor it was promised", i)
	}

	assert.Equal(t, int64(members)*floor, p.Used())
	assert.Equal(t, int64(8)*int64(capacity), p.Used(),
		"eight times the budget, from members holding nothing but their guarantee")
	assert.Positive(t, p.Overcommits(),
		"the operator's only signal that the budget is past the count it can honour")

	for _, m := range held {
		m.Release(floor)
		m.Detach()
	}
	assert.Zero(t, p.Used())
}

func TestPoolDetachIsIdempotent(t *testing.T) {
	t.Parallel()

	p := NewPool(PoolConfig{Capacity: 1 << 20})
	m := p.Attach(noEvict(t))
	require.Equal(t, Admitted, m.charge(4096, 0))

	// Production closes a relay writer twice by construction, so a
	// non-idempotent detach would drive both ledgers negative.
	m.Detach()
	m.Detach()

	assert.Zero(t, p.Used())
	assert.Zero(t, p.Members())
}

// p.members sizes every member's floor and p.byMember is the set the eviction
// scan walks. A reader that sees one without the other sizes a guarantee against
// a crowd that does not exist, and at small member counts the swing is large:
// Capacity/2 against Capacity/3.
//
// attach used to publish the member under the mutex and raise the count AFTER
// releasing it, and Detach was the mirror image, so any scan landing in that
// window did exactly that. Both mutations now happen inside the critical section
// that owns the map, so anything holding the mutex sees the two agree.
func TestPoolMemberCountAgreesWithTheMapItCounts(t *testing.T) {
	t.Parallel()

	p := NewPool(PoolConfig{Capacity: 8 << 20})

	// Churn from several goroutines, so the publication window -- if there is
	// one -- is open somewhere almost every time the observer looks.
	const churners = 8
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range churners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				p.Attach(func(error) bool { return true }).Detach()
			}
		}()
	}
	defer func() {
		close(stop)
		wg.Wait()
	}()

	// Read under the pool's own mutex, which is where both values are written:
	// whatever an eviction scan can observe, this observes.
	for range 20000 {
		p.mu.Lock()
		mapped, counted := len(p.byMember), p.members.Load()
		p.mu.Unlock()
		require.Equal(t, int64(mapped), counted,
			"the member count and the scan set must never disagree")
	}
}

func TestPoolFreedBroadcastIsSkippedWithNoWaiters(t *testing.T) {
	t.Parallel()

	p := NewPool(PoolConfig{Capacity: 1 << 20})
	m := p.Attach(noEvict(t))
	require.Equal(t, Admitted, m.charge(4096, 0))

	// No waiters: the generation must not turn over, because every relayed
	// frame in the Hub would otherwise pay for a wake-up with no audience.
	before := p.freedGen()
	m.Release(4096)
	assert.Equal(t, before, p.freedGen())

	// With a waiter registered, the same release must wake it.
	require.Equal(t, Admitted, m.charge(4096, 0))
	p.addWaiter(1)
	defer p.addWaiter(-1)
	gen := p.freedGen()
	m.Release(4096)
	select {
	case <-gen:
	default:
		t.Fatal("a release must wake a registered waiter")
	}
}

func TestNewPoolRejectsAnUnsizedCapacity(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() { NewPool(PoolConfig{}) },
		"a pool that guessed its own size would be a second source of truth for the one number this type exists to state")
	assert.Panics(t, func() { NewPool(PoolConfig{Capacity: 4096, MinFloor: 8192}) },
		"a floor larger than the capacity can never be honoured")
}

func TestPoolDefaultFloorsApply(t *testing.T) {
	t.Parallel()

	p := NewPool(PoolConfig{Capacity: 1 << 30})
	assert.Equal(t, int64(DefaultMinFloor), p.MinFloor())
	assert.Equal(t, int64(DefaultMaxFloor), p.floor(),
		"an uncrowded pool grants the generous end of the floor range")

	// A MaxFloor below MinFloor is a misconfiguration that would otherwise make
	// clamp() invert; it is raised rather than honoured.
	q := NewPool(PoolConfig{Capacity: 1 << 30, MinFloor: 2 << 20, MaxFloor: 1 << 20})
	assert.Equal(t, int64(2<<20), q.floor())
}

func TestPoolRelieveDoesNotEvictOnStalePressure(t *testing.T) {
	t.Parallel()

	const capacity = 8 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 64 << 10, MaxFloor: 64 << 10})
	asker := p.Attach(noEvict(t))
	peer := p.Attach(noEvict(t))
	require.Equal(t, Admitted, peer.charge(4<<20, 0))

	// The admission test runs under the asker's own lock and the reclaim runs
	// under none, so an ordinary drain elsewhere can free the needed bytes in
	// between. Reclaiming on that stale reading would disconnect somebody for
	// memory that is already there.
	assert.Equal(t, relieveRaced, asker.relieve(1<<20, 0))
	assert.Zero(t, p.Evictions())
}

// --- Shared buffers: holding is not residency ----------------------------
//
// A member whose queued buffers are held by other members at the same time --
// the Hub's /ws/userevents subscribers, all queuing one memoized frame by
// pointer -- must not be charged for each of them. Otherwise the pool reports
// several times the memory that exists and sheds connections on a Hub with
// room, which is the failure the whole mechanism exists to prevent.

// shared drives Admit the way a shared-buffer member does: the caller retains
// the buffer first and tells the pool how many of the bytes are NEW to it.
func TestPoolCountsOneSharedBufferOnce(t *testing.T) {
	t.Parallel()

	const size = 64 << 10
	p := NewPool(PoolConfig{Capacity: 4 << 20, MinFloor: 1 << 20, MaxFloor: 1 << 20})
	a := p.AttachShared(noEvict(t))
	b := p.AttachShared(noEvict(t))

	// a brings the buffer in; b joins an already-resident one.
	require.Equal(t, Admitted, a.Admit(size, size))
	require.Equal(t, int64(size), p.Used())
	require.Equal(t, Admitted, b.Admit(size, 0))
	assert.Equal(t, int64(size), p.Used(),
		"one buffer held twice is one buffer: a second charge is memory that does not exist")

	// Both are equally far behind, and THAT is what an eviction scan ranks.
	assert.Equal(t, int64(size), a.charged.Load())
	assert.Equal(t, int64(size), b.charged.Load())

	// Whoever lets go LAST returns the buffer, whether or not they charged it.
	// Here that is b, which passed resident=0 on the way in.
	a.Release(size, 0)
	assert.Equal(t, int64(size), p.Used(), "the buffer is still resident while b holds it")
	b.Release(size, size)
	assert.Zero(t, p.Used())
}

// The residency has to land whatever the verdict: the buffer became resident
// when the caller retained it, one step before asking. A pool that recorded it
// only on success would under-report live memory for as long as a refused
// caller took to drop the frame -- and the refused caller's refund would then
// drive the total NEGATIVE, which reads as "empty" and switches the bound off
// for every connection in the pool.
func TestPoolAdmitRecordsResidencyEvenWhenItRefuses(t *testing.T) {
	t.Parallel()

	const (
		capacity = 1 << 20
		// Chosen against the crowd below: the admission rule deliberately keeps
		// about capacity/(members+1) free, so the floor has to be a large enough
		// share of that reserve for a member already sitting on its guarantee to
		// be refused the next frame.
		floor = 64 << 10
	)
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})
	m := p.AttachShared(noEvict(t))

	// Saturate the pool from elsewhere, then take the working set this member is
	// guaranteed. Real state rather than a fictional holding: the threshold is
	// now the floor, and the member is sitting exactly on it.
	// Several hogs round-robin, not one: under the dynamic rule a lone member
	// settles at about half the capacity and leaves the pool with room.
	hogs := make([]*PoolMember, 12)
	for i := range hogs {
		hogs[i] = p.Attach(func(error) bool { return true })
	}
	for progressed := true; progressed; {
		progressed = false
		for _, h := range hogs {
			if h.charge(floor, 0) == Admitted {
				progressed = true
			}
		}
	}
	require.Less(t, capacity-p.Used(), int64(2*floor),
		"precondition: the pool must be saturated to within two floors")

	require.Equal(t, Admitted, m.Admit(floor, floor), "the guaranteed floor must still be grantable")
	require.Equal(t, int64(floor), m.Charged())
	before := p.Used()

	// One byte past the guarantee, against a pool with nothing left: certainly no.
	require.Equal(t, Pressure, m.Admit(floor, floor))
	assert.Equal(t, before+floor, p.Used(), "the refused buffer is resident all the same")
	assert.Equal(t, int64(floor), m.Charged(), "but the member is not holding it")

	// The caller drops the frame and gives back what letting go freed.
	m.Release(0, floor)
	assert.Equal(t, before, p.Used())

	m.Release(floor, floor)
	m.Detach()
	for _, h := range hogs {
		h.Release(h.Charged())
		h.Detach()
	}
	assert.Zero(t, p.Used())
}

// Detach's refund is right for a member whose bytes are its own and WRONG for
// one holding shared buffers: the residency it appears to hold may still be
// owed to another member that has not let go. Refunding it would subtract bytes
// the pool legitimately holds.
func TestPoolDetachRefundsOnlyWhatAMemberOwnsOutright(t *testing.T) {
	t.Parallel()

	const size = 4096
	t.Run("a private member's residue is refunded", func(t *testing.T) {
		t.Parallel()
		p := NewPool(PoolConfig{Capacity: 1 << 20, MinFloor: 4096, MaxFloor: 4096})
		m := p.Attach(noEvict(t))
		require.Equal(t, Admitted, m.charge(size, 0))

		m.Detach()
		assert.Zero(t, p.Used(), "a writer torn down mid-queue must not strand its bytes")
		assert.Zero(t, m.Charged(), "and the ledger loses exactly what was refunded")
		assert.Zero(t, p.Members())
	})

	t.Run("a shared member that released everything leaves quietly", func(t *testing.T) {
		t.Parallel()
		p := NewPool(PoolConfig{Capacity: 1 << 20, MinFloor: 4096, MaxFloor: 4096})
		keeper := p.AttachShared(noEvict(t))
		leaver := p.AttachShared(noEvict(t))
		require.Equal(t, Admitted, keeper.Admit(size, size))
		require.Equal(t, Admitted, leaver.Admit(size, 0))

		// The contract: let go of every buffer first. leaver was not the last
		// holder, so it frees no residency -- keeper's copy is still resident.
		leaver.Release(size, 0)
		leaver.Detach()
		assert.Equal(t, int64(size), p.Used(),
			"the buffer keeper still holds must stay resident")
		assert.Equal(t, int64(1), p.Members())

		keeper.Release(size, size)
		assert.Zero(t, p.Used())
		keeper.Detach()
		assert.Zero(t, p.Members())
	})

	// Detaching a shared member that still holds bytes cannot be repaired: the
	// holding is not its share of residency, so there is nothing to refund, and
	// absorbing it silently would leave the pool short by exactly the leaked
	// frames with no symptom but an unexplained drift in used_bytes. Loud, for
	// the same reason MarshaledEvent.Release panics on an unbalanced refcount.
	t.Run("a shared member still holding bytes is a caller bug, not a rounding error", func(t *testing.T) {
		t.Parallel()
		p := NewPool(PoolConfig{Capacity: 1 << 20, MinFloor: 4096, MaxFloor: 4096})
		m := p.AttachShared(noEvict(t))
		require.Equal(t, Admitted, m.Admit(size, size))

		// The count is part of the message: "still holding bytes" tells an
		// operator that something leaked but not how much, and the number is the
		// difference between a single dropped frame and a systematic leak.
		assert.PanicsWithValue(t, "sendq: SharedMember detached while still holding 4096 bytes",
			m.Detach)
	})
}

// The panic above is a signal, not a demolition: net/http recovers a panic per
// connection, and subscriberQueue.Close runs from a deferred func inside a
// handler, so the process carries on with whatever that unwind left behind. What
// it left behind used to be a corrupted pool.
//
// The holding was swapped to zero BEFORE the panic -- the exact "silently
// zeroing it would leave the pool short" outcome the panic exists to prevent --
// and the unwind then skipped the map removal and the member-count decrement, so
// the departed member stayed in byMember with the count still counting it. Every
// survivor's floor is Capacity/members, so that shrinks the guarantee for every
// other connection in the process, permanently: sync.Once marks itself done from
// a defer, so no retry can ever finish the removal.
func TestPoolDetachStaysConsistentWhenItsPanicIsRecovered(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		size     = 64 << 10
	)
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 1 << 10, MaxFloor: capacity})

	survivor := p.Attach(noEvict(t))
	leaker := p.AttachShared(noEvict(t))
	require.Equal(t, Admitted, leaker.Admit(size, size))
	require.Equal(t, int64(2), p.Members())
	floorWithBoth := p.floor()

	assert.Panics(t, leaker.Detach, "a shared member leaking bytes must still be loud")

	// The pool has genuinely let the member go, so nothing it did on the way out
	// can cost the connections that stay.
	assert.Equal(t, int64(1), p.Members(), "the member count must not stay inflated")
	p.mu.Lock()
	remaining := len(p.byMember)
	p.mu.Unlock()
	assert.Equal(t, 1, remaining,
		"the departed member -- and the evict closure it pins -- must leave the scan set")
	assert.Greater(t, p.floor(), floorWithBoth,
		"a departure must raise the survivors' floor, panic or no panic")

	// And the evidence is intact: the leaked bytes are still on the member's
	// ledger and still counted resident, so an operator reading used_bytes sees
	// the leak rather than an unexplained drift.
	assert.Equal(t, int64(size), leaker.Charged(),
		"zeroing the ledger on the way out is what the panic exists to prevent")
	assert.Equal(t, int64(size), p.Used())

	// The survivor is untouched and still usable.
	require.Equal(t, Admitted, survivor.charge(size, 0))
	survivor.Release(size)
	survivor.Detach()
	assert.Zero(t, p.Members())
}

// Admit answers with the three-way verdict charge already computes, because the
// two failures need opposite handling: "the pool is full right now" is worth
// waiting or reclaiming for, "too big for this pool at any occupancy" never is.
//
// Collapsed to a bool, the shared path's callers had to ask the pool a second
// question to tell them apart -- and a caller that does not ask tells its client
// to retry, which produced a client rebuilding the same oversized snapshot every
// few seconds forever while the user watched an app that never finished loading.
func TestPoolAdmitSeparatesAnUnfittableFrameFromPressure(t *testing.T) {
	t.Parallel()

	const (
		capacity = 1 << 20
		floor    = 64 << 10
	)
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})
	m := p.AttachShared(noEvict(t))

	// Larger than the whole pool, asked against an EMPTY one: no occupancy
	// admits it, so this is not the pool being full.
	assert.Equal(t, Unfittable, m.Admit(capacity+1, capacity+1))
	// The residency landed all the same. The caller retained the buffer one step
	// before asking and refunds whatever letting go of it reports, so an early
	// return that skipped the record would subtract bytes that were never added
	// and drive the total negative -- a total that reads as "empty" and switches
	// the bound off for every member of the pool.
	assert.Equal(t, int64(capacity)+1, p.Used(),
		"the refused buffer is resident until its holder drops it")
	assert.Zero(t, m.Charged(), "but an unfittable frame is not held")
	assert.Zero(t, p.Overcommits(),
		"and it granted no floor, so it must not read as the budget being outgrown")

	m.Release(0, capacity+1)
	require.Zero(t, p.Used())

	// A frame that fits the pool but not the moment is Pressure -- the verdict
	// that says waiting or reclaiming could still help.
	hog := p.AttachShared(noEvict(t))
	require.Equal(t, Admitted, hog.Admit(capacity, capacity))
	assert.Equal(t, Pressure, m.Admit(floor+1, floor+1),
		"one byte past the guaranteed floor on a full pool is pressure, not an impossible frame")
	m.Release(0, floor+1)

	// ...and the very same frame is admitted once the room is there, which is
	// what makes the two verdicts different advice rather than different words.
	hog.Release(capacity, capacity)
	assert.Equal(t, Admitted, m.Admit(floor+1, floor+1))

	m.Release(floor+1, floor+1)
	m.Detach()
	hog.Detach()
	assert.Zero(t, p.Used())
}

// Every outcome names itself, so a failed assertion or a log line reads as the
// verdict rather than as an integer nobody can decode without the source.
func TestAdmitOutcomeNamesEveryVerdict(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, o := range []AdmitOutcome{Admitted, Unfittable, Pressure} {
		name := o.String()
		assert.NotEqual(t, "unknown", name, "outcome %d must have a name", int(o))
		assert.False(t, seen[name], "name %q is used by more than one outcome", name)
		seen[name] = true
	}
	assert.Len(t, seen, 3)
	assert.Equal(t, "unknown", AdmitOutcome(-1).String(),
		"the default arm must stay reachable so an unhandled value cannot read as a real verdict")
}

// Admission has to give the same verdict however the bytes are charged, or the
// two paths are two policies wearing one name.
func TestPoolAdmitAndChargeAgreeOnTheThreshold(t *testing.T) {
	t.Parallel()

	const capacity = 1 << 20
	// The verdict, not merely admitted-or-not: the two paths now answer with the
	// same three-way outcome, so a frame too big for the pool has to read as
	// Unfittable on both. Collapsing that to a bool is what used to force the
	// shared path's callers to ask a second question -- and a caller that cannot
	// tell "never fits" from "full right now" tells its client to retry forever.
	for _, tt := range []struct {
		name       string
		held, size int64
		want       AdmitOutcome
	}{
		{"empty queue, small frame", 0, 4096, Admitted},
		{"at half the capacity", capacity / 2, 4096, Pressure},
		{"just under half", capacity/2 - 8192, 4096, Admitted},
		{"a frame larger than the whole pool", 0, capacity + 1, Unfittable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// One member per pool and resident == held, so the two paths are
			// looking at identical state and any difference is the rule itself.
			viaCharge := NewPool(PoolConfig{Capacity: capacity, MinFloor: 4096, MaxFloor: 4096})
			cm := viaCharge.Attach(noEvict(t))
			if tt.held > 0 {
				require.Equal(t, Admitted, cm.charge(tt.held, 0))
			}

			viaAdmit := NewPool(PoolConfig{Capacity: capacity, MinFloor: 4096, MaxFloor: 4096})
			am := viaAdmit.AttachShared(noEvict(t))
			if tt.held > 0 {
				require.Equal(t, Admitted, am.Admit(tt.held, tt.held))
			}

			assert.Equal(t, tt.want, cm.charge(tt.size, 0),
				"charge disagreed with the expected verdict")
			assert.Equal(t, tt.want, am.Admit(tt.size, tt.size),
				"Admit disagreed with charge")
		})
	}
}

// The shared path has no compare-and-swap -- the residency is unconditional, so
// there is nothing to roll back and nothing to retry. What must still hold is
// that every recorded byte is recoverable: charges and refunds racing across
// members have to leave the total at exactly zero, not merely near it.
func TestPoolSharedChargesBalanceUnderRace(t *testing.T) {
	t.Parallel()

	const (
		size    = 4096
		members = 8
		rounds  = 200
	)
	p := NewPool(PoolConfig{Capacity: 1 << 20, MinFloor: 64 << 10, MaxFloor: 64 << 10})
	handles := make([]*SharedMember, members)
	for i := range handles {
		handles[i] = p.AttachShared(func(error) bool { return true })
	}

	var wg sync.WaitGroup
	for range rounds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// One "buffer" per round, brought in by the first member to take it
			// and released by whichever member happens to be last -- the
			// interleaving the manager's fan-out produces.
			var holders sync.WaitGroup
			var refs atomic.Int64
			for _, m := range handles {
				holders.Add(1)
				go func() {
					defer holders.Done()
					resident := int64(0)
					if refs.Add(1) == 1 {
						resident = size
					}
					admitted := m.Admit(size, resident) == Admitted
					freed := int64(0)
					if refs.Add(-1) == 0 {
						freed = size
					}
					if admitted {
						m.Release(size, freed)
					} else {
						m.Release(0, freed)
					}
				}()
			}
			holders.Wait()
		}()
	}
	wg.Wait()

	assert.Zero(t, p.Used(), "every byte recorded resident must be recoverable")
	for _, m := range handles {
		assert.Zero(t, m.charged.Load(), "and every member must end holding nothing")
		m.Detach()
	}
	assert.Zero(t, p.Members())
}

// Leaving RAISES every survivor's floor -- the floor is Capacity/members -- so a
// departure can turn a refusal into an admission without one byte being freed.
//
// The member that leaves here holds NOTHING, which is the normal case and the
// one that used to go unnoticed: Writer.Close discards and refunds its queue
// before detaching, so Detach reached its refund branch with zero and returned
// silently. A parked EnqueueWait then slept to its deadline while the room it
// needed was already there -- a ChannelOpen failing with DeadlineExceeded at the
// exact moment a worker disconnected.
func TestPoolDetachWakesParkersItMadeRoomFor(t *testing.T) {
	t.Parallel()

	const capacity = 12 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 1 << 20, MaxFloor: capacity})

	hog := p.Attach(noEvict(t))
	waiter := p.Attach(noEvict(t))
	idle := p.Attach(noEvict(t))

	// Fill the pool so the dynamic branch is gone and only the floor decides.
	require.Equal(t, Admitted, hog.charge(capacity, 0))
	require.Equal(t, int64(capacity), p.Used())

	// Three members: the floor is capacity/3 (4 MiB). Five megabytes is above
	// that and below the two-member floor of capacity/2 (6 MiB), so it is
	// exactly the frame whose verdict the departure flips.
	const frame = 5 << 20
	require.Equal(t, int64(capacity/3), p.floor())
	require.False(t, p.admits(0, frame, p.Used(), 0),
		"precondition: this frame does NOT fit while three members are attached")

	// Register and capture the generation the way EnqueueWait does.
	p.addWaiter(1)
	defer p.addWaiter(-1)
	gen := p.freedGen()

	// idle holds nothing, so this frees not one byte -- it only changes the
	// member count. That alone is what has to wake the parker.
	require.Zero(t, idle.charged.Load(), "the point of this test is a detach that refunds nothing")
	idle.Detach()

	select {
	case <-gen:
	default:
		t.Fatal("a detach that raised the surviving members' floor must wake parkers")
	}
	assert.Equal(t, int64(capacity/2), p.floor())
	assert.True(t, p.admits(0, frame, p.Used(), 0),
		"and the frame that was refused must now fit")

	hog.Release(capacity)
	hog.Detach()
	waiter.Detach()
}

// Admit records residency and reads the occupancy it judges against in ONE
// atomic step, which is what makes the rule self-limiting under concurrency:
// admitting requires held+size <= Capacity-used, and used >= held, so the
// admitted total can never pass Capacity.
//
// With a plain Load followed by an Add, concurrent admitters all read the same
// pre-add occupancy and every one of them said yes. The overshoot was bounded
// only by how many happened to be admitting at once -- unbounded in users, at up
// to a whole frame each, and on the user-events pool a frame is up to 16 MiB.
func TestPoolConcurrentAdmitsCannotOvershootCapacity(t *testing.T) {
	t.Parallel()

	// Four frames' worth of capacity against many more admitters, so the losers
	// vastly outnumber the winners and any interleaving shows up as an
	// over-admission. Floors pinned to a byte so the floor can never be what
	// admits somebody.
	const (
		frame     = 1 << 20
		capacity  = 4 * frame
		admitters = 64
		rounds    = 300
	)

	for round := range rounds {
		p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 1, MaxFloor: 1})
		members := make([]*SharedMember, admitters)
		for i := range members {
			members[i] = p.AttachShared(noEvict(t))
		}

		var (
			ready   sync.WaitGroup
			done    sync.WaitGroup
			granted atomic.Int64
		)
		release := make(chan struct{})
		ready.Add(admitters)
		for _, m := range members {
			done.Add(1)
			go func() {
				defer done.Done()
				// Park every goroutine on one channel close rather than a
				// countdown, so they are released together instead of trickling
				// out in the order they happened to arrive.
				ready.Done()
				<-release
				// Each brings in its OWN buffer, so every admission is a full
				// frame of new residency -- the case a stale reading is costly.
				if m.Admit(frame, frame) == Admitted {
					granted.Add(1)
				} else {
					m.Release(0, frame)
				}
			}()
		}
		ready.Wait()
		close(release)
		done.Wait()

		require.LessOrEqual(t, granted.Load()*frame, int64(capacity),
			"round %d: the admitted total must never pass Capacity (%d admitted)",
			round, granted.Load())
		require.Equal(t, granted.Load()*frame, p.Used(),
			"round %d: and every refused admission must have given its residency back", round)

		for _, m := range members {
			if m.charged.Load() != 0 {
				m.Release(frame, frame)
			}
			m.Detach()
		}
		require.Zero(t, p.Used(), "round %d", round)
	}
}

// MaxFloor is what makes "an uncrowded pool never refuses one largest frame"
// true, and it has to be sized against the CLASS's frame rather than sendq's
// generic default.
//
// The Hub builds each pool from its class's own budget for exactly this reason.
// Built with the 4 MiB default instead, a merely-full user-events pool refused
// every 16 MiB bootstrap outright -- and only large accounts were locked out,
// while small ones connected fine on the same Hub, with no eviction there to
// clear the condition.
func TestPoolMaxFloorAdmitsOneLargestFrameOfItsClass(t *testing.T) {
	t.Parallel()

	// The state is built explicitly rather than by filling to the rule's fixed
	// point: what this test is about is the FLOOR, and the floor is
	// capacity/members, so a test that saturated by adding members would move
	// the very number it means to hold still.
	//
	// Two members and a hog holding all but 8 MiB. The dynamic branch can grant
	// 8 MiB, the frame needs 16, so the verdict rests entirely on the floor --
	// which is capacity/2 clamped to MaxFloor, i.e. 4 MiB by default and 16 MiB
	// when the class sized it.
	const (
		capacity = 64 << 20
		frame    = 16 << 20
		hogHolds = capacity - 8<<20
	)

	fill := func(t *testing.T, p *Pool) *PoolMember {
		t.Helper()
		hog := p.Attach(func(error) bool { return true })
		require.Equal(t, Admitted, hog.charge(hogHolds, 0))
		require.Less(t, capacity-p.Used(), int64(frame),
			"precondition: the dynamic branch alone must not be able to grant the frame")
		return hog
	}

	t.Run("sendq's generic default refuses it", func(t *testing.T) {
		t.Parallel()
		p := NewPool(PoolConfig{Capacity: capacity})
		hog := fill(t, p)

		m := p.AttachShared(noEvict(t))
		require.Equal(t, int64(DefaultMaxFloor), p.floor(), "the default floor is what is under test")
		assert.Equal(t, Pressure, m.Admit(frame, frame),
			"a 4 MiB floor cannot grant a 16 MiB frame, whatever the total capacity")

		m.Release(0, frame)
		m.Detach()
		hog.Release(hogHolds)
		hog.Detach()
		assert.Zero(t, p.Used())
	})

	t.Run("a floor sized to the class admits it", func(t *testing.T) {
		t.Parallel()
		p := NewPool(PoolConfig{Capacity: capacity, MaxFloor: frame})
		hog := fill(t, p)

		m := p.AttachShared(noEvict(t))
		require.Equal(t, int64(frame), p.floor())
		assert.Equal(t, Admitted, m.Admit(frame, frame),
			"an otherwise-idle member must be able to place one legitimate frame")

		m.Release(frame, frame)
		m.Detach()
		hog.Release(hogHolds)
		hog.Detach()
		assert.Zero(t, p.Used())
	})
}

// Overcommits means "the pool granted a floor it did not have room for", and a
// refusal grants nothing.
//
// Counting it before the admission test made every refused frame look like the
// deployment outgrowing its budget -- which is the one thing the operator docs
// tell the reader to answer by RAISING the budget, so a Hub shedding exactly as
// designed advised the opposite of what it needed. Worse on the shared path: one
// fanned-out frame refused by N subscribers counted N times, because each
// refusal releases and the next member's Retain is again a 0->1 transition.
func TestPoolAdmitCountsOvercommitsOnlyWhenItGrants(t *testing.T) {
	t.Parallel()

	const (
		capacity = 1 << 20
		floor    = 64 << 10
	)

	t.Run("a refusal that would cross capacity counts nothing", func(t *testing.T) {
		t.Parallel()
		p := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

		hog := p.AttachShared(noEvict(t))
		require.Equal(t, Admitted, hog.Admit(900<<10, 900<<10))
		require.Zero(t, p.Overcommits(), "filling the pool inside its capacity is not an overcommit")

		// used(900K) + resident(300K) crosses capacity, but the threshold --
		// max(capacity-used, floor) = 124K -- refuses it. Nothing was granted.
		newcomer := p.AttachShared(noEvict(t))
		require.Equal(t, Pressure, newcomer.Admit(300<<10, 300<<10), "precondition: this must be refused")
		assert.Zero(t, p.Overcommits(),
			"a refused frame granted no floor, so it must not read as the budget being outgrown")
	})

	t.Run("an admission that crosses capacity still counts", func(t *testing.T) {
		t.Parallel()
		p := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

		hog := p.AttachShared(noEvict(t))
		require.Equal(t, Admitted, hog.Admit(990<<10, 990<<10))

		// Inside the newcomer's guaranteed floor, so it is admitted -- and the
		// pool had no room for that guarantee. THAT is the overcommit.
		newcomer := p.AttachShared(noEvict(t))
		require.Equal(t, Admitted, newcomer.Admit(40<<10, 40<<10))
		assert.Equal(t, int64(1), p.Overcommits(),
			"granting a floor past capacity is exactly what this counter is for")
	})
}

// Evictions is what an operator reads as "connections disconnected to reclaim
// memory", so it must count teardowns that actually reclaimed.
//
// The victim is chosen under the pool mutex and torn down after it is released,
// and in that window it may have closed, given up, or simply drained to zero on
// its own -- in which case its evict frees nothing.
func TestPoolCountsOnlyEvictionsThatReclaimed(t *testing.T) {
	t.Parallel()

	const capacity = 8 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 1 << 10, MaxFloor: 1 << 10})

	// A victim that reports it reclaimed nothing -- the shape of a member that
	// had already given up by the time the scan reached it.
	calls := 0
	victim := p.Attach(func(error) bool {
		calls++
		return false
	})
	require.Equal(t, Admitted, victim.charge(4<<20, 0))

	asker := p.Attach(noEvict(t))
	assert.Equal(t, relieveEvicted, asker.relieve(capacity, 0))
	assert.Equal(t, 1, calls, "the victim must still be nominated")
	assert.Zero(t, p.Evictions(), "but a teardown that freed nothing is not a reclaim")
}

// The reclaim loop terminates because a nominated victim cannot be nominated
// again -- a property of the POOL, not of every member's callback discipline.
//
// It used to depend on evict refunding and detaching before it returned, which
// the /ws/userevents subscriber structurally cannot do: its teardown is a
// context cancel, and the refund lands later on the handler goroutine. Under the
// old rule that victim stayed top of the scan and the asker re-evicted it every
// turn, burning a core until the other goroutine happened to finish.
func TestPoolDoesNotRenominateAVictimStillUnwinding(t *testing.T) {
	t.Parallel()

	const capacity = 8 << 20
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: 1 << 10, MaxFloor: 1 << 10})

	// Signals teardown and returns, exactly like the subscriber queue: it keeps
	// holding its bytes, because nothing has refunded them yet.
	nominations := 0
	slow := p.Attach(func(error) bool {
		nominations++
		return true
	})
	require.Equal(t, Admitted, slow.charge(4<<20, 0))

	asker := p.Attach(noEvict(t))
	require.Equal(t, relieveEvicted, asker.relieve(capacity, 0))
	require.Equal(t, 1, nominations)

	// Second turn: the victim still holds every byte, so a scan that ranked by
	// holding alone would pick it again.
	require.Equal(t, int64(4<<20), slow.Charged(), "precondition: the victim has not refunded yet")
	require.Zero(t, asker.Charged(), "precondition: the asker holds nothing at all")
	assert.Equal(t, relieveRaced, asker.relieve(capacity, 0),
		"a victim already unwinding must not be nominated a second time -- and an asker "+
			"holding nothing must be told to retry, never that it is the hog")
	assert.Equal(t, 1, nominations, "and its evict must not be called again")
}

// The deferral above must never displace a reclaim that IS available. Skipping
// the verdict while a victim unwinds is right; skipping the eviction too would
// let one stuck teardown switch reclaiming off for the whole pool, which is a
// worse failure than the one it avoids.
func TestPoolStillReclaimsWhileAnEarlierTeardownUnwinds(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		floor    = 1 << 20
	)
	p := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

	// Signals teardown and never refunds, so it stays marked for the whole test.
	stuck := p.Attach(func(error) bool { return true })
	require.Equal(t, Admitted, stuck.charge(4<<20, 0))

	runnerUpEvicts := 0
	runnerUp := p.Attach(func(error) bool {
		runnerUpEvicts++
		return true
	})
	require.Equal(t, Admitted, runnerUp.charge(2<<20, 0))

	asker := p.Attach(noEvict(t))
	require.Equal(t, relieveEvicted, asker.relieve(capacity, 0), "the biggest holder goes first")
	require.Zero(t, runnerUpEvicts, "and it is not the runner-up")

	assert.Equal(t, relieveEvicted, asker.relieve(capacity, 0),
		"a peer above its floor is still reclaimable while an earlier victim unwinds")
	assert.Equal(t, 1, runnerUpEvicts, "and it is torn down exactly once")
}
