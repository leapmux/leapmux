package sendq

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Default pool bounds. Capacity has no default -- NewPool requires it, because
// the only honest source for it is the process's memory budget, which this
// package deliberately knows nothing about (see the Pool doc).
const (
	// DefaultMinFloor is the smallest working set a writer is guaranteed under
	// a crowded pool. It is sized off real frames, not rounded: a /ws/channel
	// frame is at most channelwire.WSReadLimit (~69 KiB) and workermgr reserves
	// DefaultControlReserve (256 KiB) for control, so 1 MiB always holds a
	// handful of data frames on top of a full control reserve.
	DefaultMinFloor int64 = 1 << 20 // 1 MiB
	// DefaultMaxFloor is the guaranteed working set when the pool is NOT
	// crowded: room for many frames of any class this package is likely to
	// serve, so an uncrowded pool refuses nothing an idle member sends.
	//
	// It is a floor OF LAST RESORT rather than a figure derived from any one
	// class. A caller that knows its own largest frame states it -- config's
	// PoolConfig raises MaxFloor to the class's own maxFrame -- and that is the
	// number that binds for every pool the Hub builds. This default binds only
	// where a class's frames are smaller than it, which is both pools that take
	// it today.
	DefaultMaxFloor int64 = 4 << 20 // 4 MiB
)

// ErrPoolPressure is the cause passed to OnGiveUp when a writer is torn down to
// reclaim shared pool bytes rather than for its own budget. Distinct from
// ErrOverBudget so an operator reading give-up logs can tell "this client was
// slow" from "the Hub was full" -- the two have different fixes, and only the
// second means the deployment is undersized.
var ErrPoolPressure = errors.New("sendq: evicted to reclaim shared queue memory")

// Pool bounds the TOTAL queued bytes of every member drawing from it, which is
// the number an operator actually sizes: per-connection budgets multiply by a
// connection count nothing bounds.
//
// A member is usually a Writer, but need not be: anything that queues bytes for
// one connection can attach and answer to the same rule. The Hub's
// /ws/userevents subscriber queue does, without being a Writer at all.
//
// Membership is also a blast radius. Reclaiming (below) can only ever tear down
// a member, so connections whose failure costs differ sharply do not belong in
// one pool -- give each class its own, and the cheaper failure can never be
// traded for the dearer one.
//
// # Holding is not residency
//
// Two numbers, not one. A member's HOLDING is how many bytes sit in its queue,
// and answers "how far behind is this connection?". The pool's RESIDENCY is how
// many distinct bytes this process actually holds, and answers "how close are we
// to the budget?". They coincide for a Writer, whose frames are its own, and
// they part company the moment several members queue ONE buffer by pointer --
// as every subscriber of a user does with a memoized *crdt.MarshaledEvent. The
// second holder's queue really is that much further behind, but not one byte of
// it is newly resident.
//
// So admission tests the holding (whoever is furthest behind is refused first,
// which is the right question) while the total tracks residency (so the metric
// an operator sizes from is not inflated by fan-out). See Admit.
//
// # The admission rule
//
// Each member's ceiling is recomputed from one subtraction at every enqueue:
//
//	T_i = max( floor , Capacity - used )
//
// This is the Dynamic Queue Length Threshold of Choudhury & Hahne (1998), whose
// problem -- many queues sharing one memory, no advance knowledge of how many
// are active -- is exactly this one. Three properties earn it:
//
//   - It cannot overshoot. Admitting requires q_i + size <= Capacity - used, and
//     used >= q_i, so used + size <= Capacity. No heap, no scan, no reservation.
//     (Sharing does not break the premise: a member's queue holds each buffer at
//     most once, so every byte of its holding is resident somewhere.)
//   - It always keeps a reserve for newcomers. With N backed-up members each
//     converges to Capacity/(N+1), leaving Capacity/(N+1) free, so a connection
//     is never punished merely for arriving last.
//   - It is generous exactly when the machine is idle. One backed-up connection
//     on an otherwise-quiet pool gets half of it before anyone considers
//     dropping anything, where a fixed per-connection budget would have cut it
//     off at the same small number on a 256 GiB host as on a 512 MiB container.
//
// # Why there is still a floor
//
// At full occupancy T_i would fall to zero and the NEXT enqueue on every
// healthy connection would tear it down -- a stampede that drops the innocent
// alongside the guilty. The floor is the working set no member can be denied,
// so only members actually holding memory can lose. It shrinks as the pool
// crowds (Capacity/members) rather than being a fixed per-member reservation:
// reserving bytes for connections that are not using them would refuse healthy
// connections on a machine with gigabytes free, which is the failure this whole
// mechanism exists to prevent.
//
// # Eviction picks the hog, not the next speaker
//
// The threshold alone does NOT drop the writer that caused the pressure -- it
// drops whichever writer enqueues NEXT while above the shrunken ceiling. Since
// terminal output is one frame per PTY read, "next" is milliseconds away for
// every active connection at once, so the naive rule sheds a correlated mass of
// healthy connections, each of which reconnects and replays from the DB and
// refills the pool. That is congestion collapse, not load shedding.
//
// So a pressure event nominates the pool's LARGEST CURRENT HOLDER instead. If
// that is the asker, the asker dies -- it is the genuine hog. Otherwise the
// holder is torn down and the asker retries into the bytes that frees.
//
// The scan is O(members) under the pool's mutex, and it runs from Enqueue --
// i.e. ON the enqueue path, just only on the turns that fail admission. A
// saturated pool is therefore also the moment its members pay for the scan, and
// ONE refused frame can cost several of them: Enqueue retries after every
// successful reclaim, so a frame needing more room than the largest holder was
// carrying scans again for each further victim, bounded by the member count
// because a nominated victim is never nominated twice. That is affordable
// because a refused frame is already about to cost a teardown, and unaffordable
// to make cheaper the obvious way: a cached victim would nominate a member that
// has since drained.
//
// Whether pressure may nominate a peer at all is a property of the POOL,
// declared once at construction as its ReclaimPolicy, rather than a habit each
// member has to keep. ReclaimEvictLargest tears the biggest holder down for an
// asker that would rather evict than fail; ReclaimRefuseAsker never nominates
// anyone and hands the asker its own verdict, which is the right trade where a
// member's own shed is the cheapest outcome available -- a /ws/userevents
// subscriber reconnects and delta-resumes.
//
// # What the bound is NOT
//
// Resident bytes can exceed Capacity, and the overshoot is NOT all bounded by
// Capacity. Three of the four terms are:
//
//   - one in-flight frame per member (released at pop, still resident until the
//     write returns);
//   - each writer's private ControlReserve, which the pool counts but may not
//     refuse;
//   - the guaranteed floors, WHILE members <= Capacity/MinFloor. Up to that
//     count floor() is the fair share and the floors sum to Capacity, so the
//     ceiling really is 2 x Capacity.
//
// Past that count the fourth term takes over and is unbounded: floor() clamps
// UP to MinFloor, so every further member is still promised a full MinFloor and
// the sum grows as members x MinFloor with nothing capping it. On a 64 MiB pool
// with the 1 MiB default floor that is 64 members; 1000 members holding nothing
// but their promised floors reach ~1 GiB. That is the same
// per-connection-times-unbounded-connections shape a private budget has, only
// 32x smaller per connection -- an improvement, not an elimination, and the
// honest statement of what this type does and does not bound.
//
// It is a deliberate trade rather than an oversight: a floor that decayed to
// zero would let one enqueue on every healthy connection tear it down at once,
// which is the stampede the floor exists to prevent. Bounding the total instead
// would mean bounding the MEMBER count -- admission control at connect time,
// which belongs to the layer that accepts connections, not to a byte budget.
// What this type owes an operator is the signal, and Overcommits is it: it
// counts exactly the admissions that crossed Capacity, so sustained growth
// means the deployment is past the count its budget can honour.
//
// And Capacity bounds CHARGED bytes, which is not a measurement of footprint.
// For a Writer that is Config.Size plus Config.FrameOverhead, an over-estimate.
// For a shared member it is whatever the buffer reports, which for
// crdt.MarshaledEvent is the marshaled length ALONE while the frame also
// retains the proto tree it was marshaled from -- an under-estimate, by roughly
// the size of that tree.
//
// Pool deliberately has no opinion about how big it should be. Deriving
// Capacity from cgroup limits, GOMEMLIMIT or physical memory is the embedding
// process's job; keeping it out of here is what lets this package stay free of
// OS surface and lets tests state a capacity instead of inheriting the host's.
type Pool struct {
	capacity int64
	minFloor int64
	maxFloor int64
	// reclaim decides whether pressure may nominate a peer. See ReclaimPolicy.
	reclaim ReclaimPolicy

	// used is the resident total: every distinct buffer once, whichever members
	// hold it. Maintained by atomics rather than a mutex so that nothing on the
	// enqueue path waits on a lock shared with every other connection in the
	// process. charge reads and commits it in one compare-and-swap, so a Writer's
	// admission is exact rather than approximately-right-under-races; Admit
	// cannot, because its residency is unconditional (see there).
	//
	// (#293's hazard was a process-global mutex held across a network Send.
	// This is a compare-and-swap held across a subtraction, twice per frame.)
	used atomic.Int64
	// members is the count used to size the floor. Kept separately from
	// len(byMember) so the hot path never takes mu: floor() is reached from
	// admits <- charge on every enqueue, and reading the map there would queue
	// every admission behind another connection's O(members) eviction scan.
	//
	// Moved under mu with the map it counts, though. attach used to publish the
	// member and raise the count as two separate steps, so relieve's scan could
	// run in between and size the floor from a count that disagreed with the map
	// it was scanning -- at small member counts the difference between
	// Capacity/2 and Capacity/3. Both mutations now happen inside the critical
	// section, so the counter and the map agree at every point either can be
	// observed. That removes the publication skew, not every interleaving:
	// relieve still reads p.floor() before taking mu, so a member that arrives
	// immediately after is legitimately absent from both the count it used and
	// the map it goes on to scan.
	members atomic.Int64
	// waiters counts goroutines parked in EnqueueWait across all members. The
	// freed broadcast is skipped entirely while it is zero, which is the normal
	// case: the frontend relay never parks, so without this gate every relayed
	// frame in the Hub would pay for a wake-up nobody is waiting for.
	//
	// Raised only by a caller that is genuinely about to park -- after its first
	// admission attempt has already failed -- so a single in-flight EnqueueWait
	// admitted on the fast path does not put freedMu plus a channel allocation
	// on the dequeue path of every other connection sharing the pool for the
	// length of its call.
	waiters atomic.Int64
	// overcommits counts pressure events resolved by granting a floor rather
	// than by evicting, i.e. occasions when the pool was too small to guarantee
	// every connection its minimum. Non-zero means raise Capacity.
	overcommits atomic.Int64
	// evictions counts members torn down to reclaim pool bytes.
	evictions atomic.Int64

	mu sync.Mutex
	// byMember is scanned only to choose an eviction victim. It is keyed by the
	// shared core rather than by either attach-mode wrapper, because everything
	// the scan needs -- the holding, the eviction mark, the evict callback -- is
	// the same for both and a scan that had to switch on the mode would be one
	// more place for the two contracts to drift.
	byMember map[*member]struct{}

	// freedMu guards freed alone, deliberately NOT mu. Waking parkers happens
	// on the release path -- once per dequeued frame while anyone is parked --
	// and mu is also held across relieve's O(members) victim scan and across
	// attach/Detach. Sharing one mutex made every worker's every dequeue queue
	// behind another connection's eviction scan for no reason: the two protect
	// unrelated state.
	freedMu sync.Mutex
	// freed is a generation channel closed and replaced when bytes return, so
	// any number of EnqueueWait parkers wake together. A depth-1 signal would
	// let one parker consume the wake-up another needed and sleep it until its
	// deadline -- see Writer.budgetFreed, which is the same pattern for the
	// same reason.
	freed chan struct{}
}

// PoolConfig configures a Pool. Only Capacity is required.
//
// Sizes are int64 rather than int because a Capacity is derived from a machine's
// memory and the auto-sized default reaches 8 GiB, which does not fit an int on
// a 32-bit build -- and would wrap into a negative capacity there rather than
// failing.
type PoolConfig struct {
	// Capacity is the total resident bytes every member may hold between them.
	// Required and must be positive.
	Capacity int64
	// MinFloor is the guaranteed per-member working set under a crowded pool.
	// Zero means DefaultMinFloor.
	MinFloor int64
	// MaxFloor is the guaranteed per-member working set when the pool is not
	// crowded. Zero means DefaultMaxFloor. Clamped up to MinFloor.
	MaxFloor int64
	// Reclaim is what this pool does under pressure. Zero means
	// ReclaimEvictLargest.
	Reclaim ReclaimPolicy
}

// ReclaimPolicy is how a pool answers a member that cannot fit a frame.
//
// A property of the POOL, declared once at construction, rather than a habit
// each member has to keep. The user-events pool has always been refuse-self --
// a subscriber that cannot fit a frame sheds itself, which is cheaper than
// taking a peer down -- but only because nothing there happened to call
// relieve. That is an omission per member, not a rule: the first sendq.Writer
// given that pool would start evicting subscribers whose own documentation
// promises they are never reclaimed, and nothing would fail to say so.
type ReclaimPolicy int

const (
	// ReclaimEvictLargest tears down the pool's largest current holder to make
	// room for an asker that would otherwise fail. The right trade where a
	// member's shed is expensive and the hog is identifiable.
	ReclaimEvictLargest ReclaimPolicy = iota
	// ReclaimRefuseAsker never nominates a peer: the asker is told it does not
	// fit and decides for itself. The right trade where a member's own shed is
	// the cheapest outcome available -- a /ws/userevents subscriber reconnects
	// and delta-resumes, and before its opening frame is on the wire it is not
	// even a disconnect.
	ReclaimRefuseAsker
)

// NewPool builds a shared byte pool. Capacity must be positive: a pool that
// silently defaulted its own size would be a second, invisible source of truth
// for the number this type exists to make explicit.
func NewPool(cfg PoolConfig) *Pool {
	if cfg.Capacity <= 0 {
		panic("sendq: PoolConfig.Capacity must be positive")
	}
	minFloor := cfg.MinFloor
	if minFloor <= 0 {
		minFloor = DefaultMinFloor
	}
	maxFloor := cfg.MaxFloor
	if maxFloor <= 0 {
		maxFloor = DefaultMaxFloor
	}
	if maxFloor < minFloor {
		maxFloor = minFloor
	}
	if minFloor > cfg.Capacity {
		panic("sendq: PoolConfig.MinFloor must not exceed Capacity")
	}
	return &Pool{
		capacity: cfg.Capacity,
		minFloor: minFloor,
		maxFloor: maxFloor,
		reclaim:  cfg.Reclaim,
		byMember: make(map[*member]struct{}),
		freed:    make(chan struct{}),
	}
}

// Capacity reports the configured total.
func (p *Pool) Capacity() int64 { return p.capacity }

// Used reports the currently resident total: each distinct buffer once,
// however many members hold it.
func (p *Pool) Used() int64 { return p.used.Load() }

// Members reports how many members are attached.
func (p *Pool) Members() int64 { return p.members.Load() }

// Evictions reports how many members have been torn down to reclaim pool bytes.
func (p *Pool) Evictions() int64 { return p.evictions.Load() }

// Overcommits reports how many times the pool granted a guaranteed floor it did
// not have room for. Sustained growth means Capacity is too small for the
// connection count and the 2 x Capacity ceiling is the one actually in force.
func (p *Pool) Overcommits() int64 { return p.overcommits.Load() }

// MinFloor reports the guaranteed working set under a crowded pool. Writers
// validate their ControlReserve against it at attach time.
func (p *Pool) MinFloor() int64 { return p.minFloor }

// admits is THE admission rule, in one place so the two charge paths cannot
// drift: a member holding `held` bytes may take `size` more when the result is
// within its threshold at the given occupancy, less whatever headroom this path
// must leave free.
//
// `used` is passed in rather than read here because the two callers need
// different instants: charge tests and commits under one compare-and-swap,
// while Admit tests against the occupancy from BEFORE its own buffer became
// resident.
func (p *Pool) admits(held, size, used, reserve int64) bool {
	return held+size <= p.thresholdAt(used)-reserve
}

// floor is the working set no attached member may be denied. It shrinks with
// the member count -- so while members <= Capacity/MinFloor the floors sum to
// Capacity -- and then stops, because MinFloor is the point below which the
// guarantee stops being worth anything. Past that count the sum grows with the
// member count; see "What the bound is NOT" on why that is the trade and what
// reports it.
func (p *Pool) floor() int64 {
	members := p.members.Load()
	if members < 1 {
		members = 1
	}
	share := p.capacity / members
	if share < p.minFloor {
		return p.minFloor
	}
	if share > p.maxFloor {
		return p.maxFloor
	}
	return share
}

// AdmitOutcome is what an admission attempt decided. It is the verdict of both
// admission paths -- the owning member's charge and the shared member's Admit --
// so a caller never has to ask a second question to tell the two failures apart.
type AdmitOutcome int

const (
	// Admitted means the bytes are charged and the caller must queue the item;
	// nothing releases them until it does.
	Admitted AdmitOutcome = iota
	// Unfittable means the item cannot fit at ANY pool occupancy, so waiting or
	// evicting would not help. A caller that cannot tell this from Pressure
	// tells its client to retry, and the client retries forever against
	// something that can never succeed.
	Unfittable
	// Pressure means the item does not fit right now because the pool is full.
	// Reclaiming bytes from the largest holder may let a retry succeed.
	Pressure
)

// String names the outcome, so a failed assertion or a log line reads as the
// verdict rather than as an integer.
func (o AdmitOutcome) String() string {
	switch o {
	case Admitted:
		return "admitted"
	case Unfittable:
		return "unfittable"
	case Pressure:
		return "pressure"
	default:
		return "unknown"
	}
}

// member is the state every pool member has, whichever way it attached: the
// ledger the eviction scan ranks it by, the callback that tears it down, and
// the one-shot removal. It is what byMember is keyed by.
//
// The two attach modes wrap it rather than sharing one struct with a mode flag,
// because their contracts genuinely differ: an owning member's holding IS its
// share of the pool's residency and Detach refunds it, while a shared member's
// is not and Detach refunding it would subtract bytes another member still
// legitimately holds. A bool plus prose left that difference to be remembered at
// each call site; two types leave it to the compiler.
type member struct {
	pool *Pool
	// charged is this member's HOLDING -- the bytes sitting in its queue. It is
	// what the eviction scan ranks members by, so it is atomic rather than
	// living under the owner's mutex. It equals this member's contribution to
	// Pool.used only when its buffers are its own; see SharedMember.
	charged atomic.Int64
	// evict tears the owning connection down and reports whether it actually
	// reclaimed anything. Installed at attach; invoked by another member's
	// goroutine with no lock held, exactly as the write watchdog's AfterFunc
	// already does.
	evict func(error) bool
	// evicting marks a member relieve has already nominated. Set under the
	// pool's mutex by the scan that chose it and never cleared: the member is on
	// its way out, and a second nomination could neither reclaim more nor be
	// counted honestly. This is what bounds the reclaim loop for a member whose
	// teardown is asynchronous.
	//
	// Its HOLDING still counts: the scan skips it for NOMINATION and reads its
	// bytes for the VERDICT, so a member on its way out cannot make the asker
	// look like the hog for memory that is already leaving.
	//
	// A plain bool, not an atomic: every read and every write is already inside
	// relieve's critical section, so an atomic on top would be a second
	// synchronisation mechanism for one field and leave a reader working out
	// which of the two was the real ordering guarantee.
	evicting bool

	detachOnce sync.Once
}

// PoolMember is one connection's handle on a Pool, for a member whose queued
// bytes are its own. Created by Attach, released exactly once by Detach.
type PoolMember struct{ *member }

// SharedMember is one connection's handle on a Pool, for a member whose queued
// buffers may be held by other members at the same time. Created by
// AttachShared, released exactly once by Detach.
//
// Its holding is NOT its share of the pool's residency, which is why it admits
// through Admit (residency and holding are separate arguments) and why its
// Detach refunds nothing. See AttachShared.
type SharedMember struct{ *member }

// Attach registers a member whose queued bytes are its own: what it holds and
// what it makes resident in the pool are the same number, so Detach can refund
// whatever it still holds. Every sendq.Writer attaches this way.
//
// evict must not block and must not re-enter the pool's mutex. It reports
// whether it actually reclaimed anything: false when the member had already
// given up or drained, so the pool does not count a teardown that freed nothing.
//
// It SHOULD refund this member's holding and Detach before it returns --
// Writer.giveUp does -- but the reclaim loop no longer depends on that. A
// nominated victim is marked ineligible under the pool's mutex before evict is
// called, so an evict that merely SIGNALS teardown (cancelling a context and
// letting another goroutine do the refund, which is what the /ws/userevents
// subscriber must do) cannot be nominated again on the next turn. Termination
// is therefore a property of the pool rather than of every member's callback
// discipline, which is what a comment alone had been asking for.
func (p *Pool) Attach(evict func(error) bool) *PoolMember {
	return &PoolMember{member: p.attach(evict)}
}

// AttachShared registers a member that queues buffers OTHER members may hold at
// the same time -- one *crdt.MarshaledEvent fanned out to every subscriber of a
// user, say.
//
// Residency for such a buffer is refcounted by the buffer itself and belongs to
// no single member: the member that brings it in need not be the one that lets
// go last. So Detach here refunds NOTHING, and the member must release every
// buffer it holds before detaching -- refunding its holding instead would
// subtract bytes the pool still legitimately holds on another member's behalf,
// and drive the total negative. That is why this returns a distinct type: the
// refund-vs-panic split is the contract, and a caller should not be able to
// reach the wrong half of it by holding the wrong variable.
//
// evict carries the same contract as Attach's, including the return value; a
// shared member is the case that cannot refund synchronously, which is exactly
// why the pool marks a nominated victim ineligible rather than trusting evict
// to have finished before it returns.
func (p *Pool) AttachShared(evict func(error) bool) *SharedMember {
	return &SharedMember{member: p.attach(evict)}
}

// attach builds the shared core and publishes it. The count is raised inside
// the same critical section that publishes the member, so no scan can size a
// floor against a count that disagrees with the map it is about to walk.
func (p *Pool) attach(evict func(error) bool) *member {
	m := &member{pool: p, evict: evict}
	p.mu.Lock()
	p.byMember[m] = struct{}{}
	p.members.Add(1)
	p.mu.Unlock()
	return m
}

// Detach removes the member and returns any bytes it still holds. A writer torn
// down mid-queue has already had its frames discarded by the time it gets here,
// so this is the residue of a teardown that raced a charge rather than the
// normal path.
//
// Idempotent: a relay connection closes its writer twice by construction (the
// drain goroutine's deferred Close plus the handler's own), and a double
// decrement would corrupt both the member count and the byte total.
func (m *PoolMember) Detach() {
	m.detach(func(held int64) {
		// Subtracted rather than swapped to zero, so the ledger loses exactly
		// what is refunded even if a charge landed after the read.
		m.charged.Add(-held)
		m.pool.release(held)
	})
}

// Detach removes the member. It refunds NOTHING: a shared member's holding is
// not its share of the pool's residency -- the buffers are refcounted and
// whoever let go last already gave the bytes back -- so the member must release
// every buffer it holds first.
//
// A non-zero holding here is therefore a bug in the member, not a number to
// absorb: silently zeroing it would leave the pool short by exactly the leaked
// frames and the only symptom would be an unexplained drift in used_bytes.
// Loud, for the same reason MarshaledEvent.Release panics on an unbalanced
// refcount -- and loud without collateral, because the pool has already let this
// member go by the time the panic fires. See member.detach.
//
// Idempotent, on the same terms as PoolMember.Detach.
func (s *SharedMember) Detach() {
	s.detach(func(held int64) {
		panic(fmt.Sprintf("sendq: SharedMember detached while still holding %d bytes", held))
	})
}

// detach removes the member from the pool and then hands any residual holding to
// settle, which either refunds it or refuses to.
//
// The order is deliberate and every step of it is about a settle that PANICS.
// That is a reachable state rather than a theoretical one: subscriberQueue.Close
// runs from a deferred func inside an HTTP handler, and net/http recovers a
// panic per connection, so the process carries on with whatever this left
// behind.
//
//   - The holding is READ, not swapped to zero. Zeroing first would hand the
//     panic the exact outcome its own comment says it exists to prevent -- a
//     pool left short by the leaked frames, with the evidence erased.
//   - The removal and the count run BEFORE settle, and therefore on every path.
//     A panic that skipped them would leave a detached member in byMember with
//     the count still counting it: every survivor's floor (Capacity/members)
//     permanently shrunk, and the member plus its evict closure pinned for the
//     life of the process.
//   - detachOnce marks itself done from a defer, so it is done even when the
//     body panics. There is no second attempt that could finish the removal,
//     which is why the first one has to.
func (m *member) detach(settle func(held int64)) {
	m.detachOnce.Do(func() {
		p := m.pool
		held := m.charged.Load()

		p.mu.Lock()
		delete(p.byMember, m)
		p.members.Add(-1)
		p.mu.Unlock()

		// Leaving RAISES every survivor's floor, because the floor is
		// Capacity/members -- so a departure can turn a refusal into an
		// admission without one byte being freed. Anyone parked in EnqueueWait
		// has to be told, or it sleeps to its deadline while the room it needed
		// is already there: a ChannelOpen failing with DeadlineExceeded at the
		// exact moment a worker disconnected. Signalled before settle so a
		// refusal that panics still wakes them.
		if p.waiters.Load() > 0 {
			p.signalFreed()
		}
		if held != 0 {
			settle(held)
		}
	})
}

// Charged reports what this member currently holds. It is the member's own
// ledger, so a caller never has to keep a second copy in step with it.
func (m *member) Charged() int64 { return m.charged.Load() }

// Release gives back what a dequeued or dropped frame was charged, and wakes
// parked EnqueueWait callers.
//
// One number, because this member's buffers are its own: what leaves its holding
// is exactly what stops being resident. There is no second figure to pass, and
// therefore none to pass wrongly.
func (m *PoolMember) Release(size int64) { m.release(size, size) }

// Release gives back what a dequeued or dropped frame was charged: `size` leaves
// this member's holding, and `resident` stops being resident in the pool. It
// also wakes parked EnqueueWait callers.
//
// Two numbers, because a shared buffer's holding and residency genuinely part
// company, in both directions -- a frame this member drops while another still
// holds it frees no residency (resident 0), and a frame it refused but was last
// to let go of frees residency it never charged (size 0).
func (s *SharedMember) Release(size, resident int64) { s.release(size, resident) }

// charge runs the admission test for one item a Writer owns outright, so its
// holding and its residency are the same bytes and move together under one
// compare-and-swap. reserve is the headroom this path must leave free (the
// control reserve for data paths, zero for control itself).
//
// The holding is read from this member rather than passed in. It used to be an
// argument, and every caller passed a counter of its own that tracked exactly
// what m.charged already did -- one quantity, three hand-maintained copies, any
// of which could drift from the ledger the eviction scan actually ranks members
// by. The caller holds the writer's mutex, which is what makes the read stable:
// every path that moves this member's holding takes that mutex first, and the
// one foreign writer (Detach, from a peer's eviction) is only reachable after
// the same mutex has set closed, which every charge path re-checks.
//
// No other writer's lock is taken here, and the pool's mutex is not touched at
// all -- an admission decision never waits on an eviction scan.
func (m *PoolMember) charge(size, reserve int64) AdmitOutcome {
	p := m.pool
	queued := m.charged.Load()
	// Unfittable is about the ITEM, not the occupancy: an item this large would
	// be refused against an empty pool too, so no amount of waiting or
	// reclaiming can help. Testing `queued + size` here instead would misreport
	// an ordinary backlog as an impossible frame, and turn a caller that should
	// have parked into one that fails.
	if size > p.capacity-reserve {
		return Unfittable
	}
	for {
		used := p.used.Load()
		if !p.admits(queued, size, used, reserve) {
			return Pressure
		}
		if p.used.CompareAndSwap(used, used+size) {
			m.charged.Add(size)
			// Granting a floor the pool did not have room for is the one way
			// occupancy passes Capacity. Count it: it is the signal that the
			// deployment is undersized for its connection count.
			if used+size > p.capacity {
				p.overcommits.Add(1)
			}
			return Admitted
		}
	}
}

// Admit asks whether this member may take `size` more bytes on top of what it
// already holds, and records `resident` bytes as newly resident in the pool
// EITHER WAY.
//
// It answers with the same three-way verdict charge does, and for the same
// reason: a caller that cannot tell "too big for this pool at any occupancy"
// from "full right now" tells its client to retry, and the client retries
// forever against something that can never succeed.
//
// Like charge, the holding is this member's own ledger rather than a number the
// caller keeps alongside it; the caller's mutex is what makes the read stable.
//
// The residency is not conditional on the verdict, and that is the point: the
// buffer became resident when the caller retained it, one step before this
// call, and it stays resident until the caller lets go. A pool that pretended
// otherwise on a refusal would under-report live memory for exactly as long as
// the refused caller took to drop the frame. A refused caller drops it and
// calls Release with whatever THAT frees -- which need not be what it passed
// here, because another member may have taken the last reference in between.
//
// `resident` equals `size` for a buffer this member alone holds and is zero for
// one another member already brought in. Only an AttachShared member ever
// passes them differently.
//
// Unlike charge there is no compare-and-swap: the residency has to be recorded
// whatever the answer, so there is nothing to roll back and nothing to retry.
// The record and the reading it is judged against are still ONE atomic step --
// Add returns the post-add total, and subtracting this call's own contribution
// back off it yields the occupancy immediately before it, serialized against
// every other admitter. A plain Load followed by an Add would let two
// goroutines read the same occupancy and both say yes, and that overshoot was
// bounded only by how many happened to be admitting at once -- unbounded in
// users, at up to one whole frame each.
func (s *SharedMember) Admit(size, resident int64) AdmitOutcome {
	p := s.pool
	held := s.charged.Load()
	// The occupancy BEFORE this buffer's residency landed: the rule tests what
	// the pool held without the item, and counting it on both sides would charge
	// the same bytes twice and refuse frames that fit.
	//
	// Recorded before the unfittable test, not after, because the residency is
	// unconditional whatever the verdict: the caller's refusal path refunds
	// whatever letting go of the frame reports, so an early return that skipped
	// this Add would subtract bytes that were never added and drive the total
	// negative -- a total that reads as "empty" and switches the bound off for
	// every member of the pool.
	used := p.used.Add(resident) - resident
	if size > p.capacity {
		return Unfittable
	}
	if !p.admits(held, size, used, 0) {
		return Pressure
	}
	s.charged.Add(size)
	// Counted AFTER the verdict, exactly as charge does it, because the metric
	// means "the pool granted a floor it did not have room for" -- a refusal
	// grants nothing. Counting before the test made every refused bootstrap in
	// a reconnect storm -- the shed this mechanism exists to perform -- look
	// like the deployment outgrowing its budget, which is the one thing the
	// operator docs tell the reader to answer by RAISING the budget. Worse, one
	// fanned-out frame refused by N subscribers counted N times. An Unfittable
	// return grants nothing either, and returns above without reaching here.
	if resident != 0 && used+resident > p.capacity {
		p.overcommits.Add(1)
	}
	return Admitted
}

// thresholdAt is the ceiling a writer may fill to at the given occupancy: the
// dynamic share, never less than the guaranteed working set.
func (p *Pool) thresholdAt(used int64) int64 {
	dynamic := p.capacity - used
	if floor := p.floor(); floor > dynamic {
		return floor
	}
	return dynamic
}

// chargeReserved records bytes the pool may not refuse: a control frame already
// admitted against its writer's private ControlReserve. The memory is real, so
// it counts toward the total and shrinks what data paths may take -- the pool
// simply has no veto over it.
func (m *PoolMember) chargeReserved(size int64) {
	p := m.pool
	m.charged.Add(size)
	if p.used.Add(size) > p.capacity {
		p.overcommits.Add(1)
	}
}

// release gives back what a dequeued or dropped frame was charged: `size`
// leaves this member's holding, and `resident` stops being resident in the pool.
// It also wakes parked EnqueueWait callers.
//
// Unexported, and reached through each wrapper's own Release, because the two
// arguments mean the same thing only for an owning member. Promoting this pair
// onto PoolMember would put the divergence the two types exist to prevent back
// within reach of a call site -- the compiler cannot object to Release(size, 0)
// on a member whose buffers are its own, and nothing else would notice the
// residency it silently kept.
func (m *member) release(size, resident int64) {
	if size != 0 {
		m.charged.Add(-size)
	}
	if resident != 0 {
		m.pool.release(resident)
	}
}

func (p *Pool) release(size int64) {
	p.used.Add(-size)
	// Skipped entirely in the common case: relay writers, which produce nearly
	// all of the Hub's frames, use Enqueue and never park.
	if p.waiters.Load() > 0 {
		p.signalFreed()
	}
}

// freedGen returns the current generation channel. A parker must capture it
// BEFORE re-testing the budget, or a release landing between the test and the
// select would be missed.
func (p *Pool) freedGen() <-chan struct{} {
	p.freedMu.Lock()
	defer p.freedMu.Unlock()
	return p.freed
}

func (p *Pool) signalFreed() {
	p.freedMu.Lock()
	ch := p.freed
	p.freed = make(chan struct{})
	p.freedMu.Unlock()
	close(ch)
}

func (p *Pool) addWaiter(delta int64) { p.waiters.Add(delta) }

// relieveOutcome is what a reclaim attempt found.
type relieveOutcome int

const (
	// relieveEvicted: a bigger holder was torn down; retrying is worth it.
	relieveEvicted relieveOutcome = iota
	// relieveRaced: the bytes are already on their way back -- the pool freed
	// enough on its own between the failed admission and this call, or a victim
	// nominated by an earlier turn has not finished refunding yet. Retry, and
	// reclaim from nobody.
	relieveRaced
	// relieveAskerAtFault: the asker's own backlog is the thing at fault. On a
	// pool that scans, nobody holds more than it; on one that never nominates a
	// peer there is nothing to scan, so the test is against the guarantee
	// instead -- it holds more than the floor it was promised.
	relieveAskerAtFault
	// relieveNoHog: the pool is full but every member is inside its guaranteed
	// floor, so there is nothing to reclaim that was not promised. The
	// deployment is too small for its connection count.
	relieveNoHog
)

// relieve reclaims pool bytes by tearing down the largest current holder.
//
// The asker is a candidate for its own nomination: when it holds more than
// anyone else it IS the hog, and reporting that is what routes it into its own
// give-up rather than punishing a bystander. A writer holding no more than the
// guaranteed floor is never a victim -- taking a connection down for memory it
// was promised would make the guarantee a lie, and at that point the honest
// answer is that the pool is undersized.
//
// The victim is chosen under the pool's mutex and torn down after it is
// released. Holding it across evict would invert the pool-then-writer order
// against the charge path's writer-then-pool, and evict is caller code.
func (m *PoolMember) relieve(want, reserve int64) relieveOutcome {
	p := m.pool
	self := m.member

	// This pool does not reclaim from peers, so there is nothing to scan and
	// nobody to nominate: the asker's own shed is the answer. Declared at
	// construction rather than left to each member to remember, so a member type
	// added later cannot start evicting peers that were promised they never
	// would be.
	if p.reclaim == ReclaimRefuseAsker {
		// Which verdict, though: this pool never nominates a peer, but the asker
		// still has to be told WHY it is being shed, and the two answers have
		// opposite fixes. The same line the scanning path draws below -- is the
		// asker inside the working set it was guaranteed? -- answers it without
		// a scan, so the two paths agree instead of one of them asserting.
		if m.charged.Load() > p.floor() {
			return relieveAskerAtFault
		}
		return relieveNoHog
	}

	// Re-test before reclaiming. This runs with no lock held -- deliberately,
	// so that tearing a victim down cannot order two members' mutexes against
	// each other -- which leaves a window in which an ordinary drain elsewhere
	// frees the bytes the asker needed. Evicting on that stale reading would
	// disconnect somebody for memory that is already available, which is the
	// exact outcome this whole mechanism exists to avoid.
	if want <= p.thresholdAt(p.used.Load())-reserve {
		return relieveRaced
	}

	mine := m.charged.Load()
	floor := p.floor()

	p.mu.Lock()
	var (
		victim    *member
		most      int64
		unwinding int64
	)
	for other := range p.byMember {
		if other == self {
			continue
		}
		held := other.charged.Load()
		// Already nominated by an earlier turn and still unwinding. Skipping it
		// is what bounds this loop at O(members) turns even when evict only
		// signals teardown: without it the same victim stays top of the scan
		// and the asker re-evicts it until that goroutine happens to finish.
		//
		// Skipped for NOMINATION, not for the verdict: its bytes are still real
		// and still coming back, so the largest of them is what tells the asker
		// to wait rather than to blame itself.
		if other.evicting {
			if held > unwinding {
				unwinding = held
			}
			continue
		}
		if held > most {
			most, victim = held, other
		}
	}
	// The verdict is reached under the same lock that chose the victim, because
	// marking it ineligible is only correct once we know it will actually be
	// evicted: marking a member the guards below then spare would exempt it from
	// every future scan without anything ever reclaiming from it.
	outcome := relieveEvicted
	switch {
	// Sole member: there is nobody else to blame or to reclaim from, so the
	// asker's own backlog is the finding. This is also the private-pool case --
	// a writer configured with a plain MaxBytes -- where "the budget is full"
	// has only ever meant "this connection filled it".
	case victim == nil:
		outcome = relieveAskerAtFault
	// Nobody exceeded what they were promised, so there is no hog to find --
	// whoever is biggest is still inside their guarantee. The asker is checked
	// too, because with every connection sitting at its floor the two are tied
	// and a tie must not read as "the asker is the hog".
	case most <= mine:
		if mine <= floor {
			outcome = relieveNoHog
		} else {
			outcome = relieveAskerAtFault
		}
	case most <= floor:
		outcome = relieveNoHog
	default:
		victim.evicting = true
	}
	p.mu.Unlock()

	if outcome != relieveEvicted {
		// Nothing is being reclaimed on this turn -- but a victim nominated by an
		// EARLIER one is still unwinding, and its bytes are on their way back.
		// Any verdict here would be a statement about the asker ("your backlog is
		// at fault", "the deployment is too small") reached from a pool the scan
		// cannot fully see, and it would shed a connection -- possibly one holding
		// nothing at all -- for memory that is already leaving. Retrying is the
		// honest answer, and Enqueue bounds it with the same race counter, so a
		// teardown that never lands costs a few yields rather than a spin.
		//
		// Deliberately NOT applied when a victim WAS chosen: waiting on a reclaim
		// already in flight instead of taking the reclaim in hand would let one
		// stuck teardown switch reclaiming off for the whole pool.
		if unwinding > 0 {
			return relieveRaced
		}
		return outcome
	}
	// Counted only if the teardown actually reclaimed something. The victim is
	// chosen under the mutex and torn down after it is released -- holding it
	// across evict would invert the pool-then-writer order against the charge
	// path's writer-then-pool, and evict is caller code -- so in that window the
	// victim may have closed, given up, or simply drained to zero on its own. It
	// then frees nothing, and counting it anyway inflated the very number the
	// operator docs describe as "connections disconnected to reclaim memory".
	if victim.evict(ErrPoolPressure) {
		p.evictions.Add(1)
	}
	return relieveEvicted
}
