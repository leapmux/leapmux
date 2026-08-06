// Package sendq is the bounded outbound queue every long-lived stream in this
// system writes through.
//
// Three connections own one today: the Hub's frontend websocket relay
// (internal/hub/service.relayWriter), the worker's Connect bidi stream
// (internal/worker/hub.Client), and the Hub's Connect server handler
// (internal/hub/workermgr.Conn). All face the same hazard -- a synchronous
// write under shared infrastructure turns one slow peer into a stall of every
// multiplexed producer behind it -- and all recover the same way: queue,
// drain from one owner, disconnect (or park) when the peer cannot keep up.
//
// Two drain modes share one queue:
//
//   - Goroutine-drained (New): a background goroutine owns the transport
//     Write. Used by the frontend relay and the worker's Hub client, where the
//     owner is free to spawn one and the write has no handler-lifetime
//     coupling.
//   - Handler-drained (NewUnstarted + Drain): the Connect *server* handler
//     owns every Write by selecting on Wake and calling Drain from its own
//     goroutine. A write that outlives that handler panics
//     ("Write called after Handler finished"); spawning a drain goroutine
//     would recreate that hazard whenever Unregister returned first. The
//     handler must call Close before it returns (via Fence), which latches
//     the queue so Drain cannot start a write afterward.
//
// Delivery classes (three deliberate APIs, not one "must"):
//
//   - Enqueue / EnqueueWait: data path. Over budget disconnects (Enqueue) or
//     parks (EnqueueWait). Ordered ciphertext has no resync for a hole.
//   - TryEnqueue: best-effort, never blocks, never tears down.
//   - TryEnqueueControl: reserved-budget TRY-enqueue. Data paths leave
//     ControlReserve free so a saturated bulk burst cannot starve tiny
//     control frames (acks, CLOSE, heartbeat). A false return means even
//     the reserve is gone -- callers must treat that as soft fail or reset;
//     the name is not a delivery guarantee.
//
// A per-connection budget does not bound a process: it multiplies by a
// connection count nothing limits. Writers therefore draw from a Pool, which
// bounds the total across its members and picks the largest holder when it has
// to reclaim. A Config with a plain MaxBytes and no Pool gets a private one
// sized to that number, so there is a single admission path rather than a
// pooled tier and an unpooled tier. See Pool for the rule and its bounds.
//
// A pool's membership is a blast radius as much as a budget: reclaiming can
// only ever cost a member. The Hub therefore runs one pool per CLASS of
// connection -- browser channel relays, worker streams, and user-event
// subscribers -- so a slow browser cannot take a worker down. Mixing classes
// whose failure costs differ by orders of magnitude into one budget is the
// mistake to avoid.
package sendq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures a Writer. Exactly one of MaxBytes and Pool is required;
// every other bound may be zeroed to disable it.
type Config[T any] struct {
	// Write is the ONLY caller of the underlying transport. Exactly one drain
	// owner -- the goroutine New starts, or the caller of NewUnstarted via
	// Drain -- invokes it.
	Write func(context.Context, T) error
	// Size returns the bytes charged for an item, excluding FrameOverhead.
	Size func(T) int
	// MaxBytes is a PRIVATE per-connection memory bound, for a writer that is
	// the only one of its kind in the process (the worker's single Hub client).
	// It is realised as a Pool of exactly this size with one member, so the
	// admission rule has no second implementation.
	//
	// Mutually exclusive with Pool: two budgets would be two sources of truth
	// for one question, and the resulting min() would silently make whichever
	// is smaller the real bound.
	MaxBytes int64
	// Pool is the SHARED byte budget this writer draws from, for writers whose
	// count is driven by clients (the Hub's frontend relays and worker
	// connections). Mutually exclusive with MaxBytes.
	Pool *Pool
	// ControlReserve is a private per-writer allowance that ONLY
	// TryEnqueueControl may spend, so a saturated bulk burst cannot starve
	// tiny control frames (acks, CLOSE, heartbeat). Data paths leave it free.
	// Zero disables the reserve, and control then shares the data budget.
	//
	// It is private on purpose: a false from TryEnqueueControl fences a
	// connection, and on the Hub's worker link that discards every user's
	// channels at once. Charging it against the shared pool would let one slow
	// browser tab fill the pool and fence the entire worker fleet. The bytes
	// are still counted in the pool's total -- they are real memory -- but the
	// pool can never REFUSE them.
	//
	// Must be strictly less than MaxBytes, or (with a Pool) at most the pool's
	// MinFloor, so the guaranteed working set always covers it.
	ControlReserve int64
	// FrameOverhead is charged per item so many tiny frames cost something.
	FrameOverhead int64
	// WriteTimeout bounds one Write. Zero disables the per-write watchdog.
	WriteTimeout time.Duration
	// MaxStall bounds how long queued work may sit unwritten. Zero disables
	// the wall-clock stall bound. The clock restarts on idle: this socket may
	// have no keepalive, so idle time is not stalled time.
	MaxStall time.Duration
	// OnGiveUp cancels the connection. Called at most once, when the writer
	// gives up. The reason is separate from the error so a caller can count
	// give-ups by cause without parsing a message: "this client was slow" and
	// "the Hub was out of shared queue memory" look alike in a log line and
	// have completely different fixes.
	OnGiveUp func(GiveUpReason, error)
	// OnDiscard reports frames/bytes discarded on teardown. Optional.
	OnDiscard func(frames int, bytes int64)
	// Now is a seam so tests can advance the stall clock without sleeping.
	// Nil means time.Now.
	Now func() time.Time
}

var (
	// ErrClosed is returned by Enqueue once the writer is torn down, and by
	// Flush when the writer closed or gave up before the waited-for frames
	// reached the transport (including when Close discarded them).
	ErrClosed = errors.New("sendq: writer closed")
	// ErrOverBudget is the cause passed to OnGiveUp when Enqueue blows the
	// byte budget. Callers of Enqueue itself still see ErrClosed: a client
	// that cannot keep up is disconnected, and further enqueues must stop.
	ErrOverBudget = errors.New("sendq: queue over byte budget")
	// errConcurrentDrain is the panic value Drain raises when a second
	// goroutine tries to own the drain. Typed so recoverers can re-panic it
	// without treating a programming invariant as a transport write panic.
	errConcurrentDrain = errors.New("sendq: concurrent Drain")
	// errStalled marks the MaxStall give-up so the drain can label it without
	// matching on the message. Unexported: the give-up REASON is the caller's
	// interface to this, not the sentinel.
	errStalled = errors.New("sendq: queue stalled")
)

// IsConcurrentDrainPanic reports whether a recovered panic value is the
// intentional concurrent-Drain ownership panic.
func IsConcurrentDrainPanic(r any) bool {
	return r == errConcurrentDrain
}

// GiveUpReason classifies why a writer tore its connection down. The set is a
// complete partition of the give-up paths, so a counter labelled by it accounts
// for every disconnect rather than leaving an unexplained remainder.
type GiveUpReason int

const (
	// GiveUpOverBudget: this writer's own backlog outgrew what it could ever be
	// granted. Its peer is slow.
	GiveUpOverBudget GiveUpReason = iota
	// GiveUpPoolPressure: this writer was reclaimed because the shared pool was
	// full and it was the largest holder. The deployment may be undersized.
	GiveUpPoolPressure
	// GiveUpStall: queued work sat unwritten past MaxStall.
	GiveUpStall
	// GiveUpWriteTimeout: one write did not return within WriteTimeout.
	GiveUpWriteTimeout
	// GiveUpWriteError: the transport returned an error or panicked.
	GiveUpWriteError
)

// Label returns the stable metric-label form. Callers must use this rather than
// a literal so a renamed reason cannot silently split a time series.
func (r GiveUpReason) Label() string {
	switch r {
	case GiveUpOverBudget:
		return "over_budget"
	case GiveUpPoolPressure:
		return "pool_pressure"
	case GiveUpStall:
		return "stall"
	case GiveUpWriteTimeout:
		return "write_timeout"
	case GiveUpWriteError:
		return "write_error"
	default:
		return "unknown"
	}
}

// Default Connect-stream queue bounds shared by the Hub workermgr.Conn and the
// worker hub.Client. Keeping one definition stops the two sides of the bidi
// link from drifting on control reserve or watchdog behaviour.
//
// DefaultMaxBytes is the WORKER side's whole budget: that process holds exactly
// one Hub client, so there is no aggregate for a Pool to bound and a private
// budget says everything. The Hub side has many connections and draws from a
// Pool instead, so it uses every constant here except this one.
const (
	DefaultMaxBytes       int64 = 32 * 1024 * 1024
	DefaultControlReserve int64 = 256 * 1024
	DefaultFrameOverhead  int64 = 256
	DefaultWriteTimeout         = 30 * time.Second
)

// maxEnqueueRaces bounds how many times one Enqueue will retry after finding
// that the pool freed the bytes it needed and something else took them again.
// Large enough that a genuine one-off race always resolves, small enough that a
// caller on a shared goroutine cannot spin on it.
const maxEnqueueRaces = 32

// DrainLimits bounds one Drain turn so a handler select can yield to receives.
// Zero MaxFrames and MaxDuration means unlimited (full drain).
type DrainLimits struct {
	MaxFrames   int
	MaxDuration time.Duration
}

type queued[T any] struct {
	item T
	// size is the charge recorded at admission, NOT recomputed at release.
	// Config.Size runs once, outside the lock, and every release path refunds
	// this stored number -- which is what keeps the writer's counter and the
	// pool's total exactly in step even if Size is expensive, non-deterministic,
	// or reads a field the item's owner mutates afterwards.
	size int64
	// control marks a frame admitted against ControlReserve, so the release
	// paths know to refund the reserve rather than the data budget.
	control bool
}

// Writer is a bounded outbound queue drained by exactly one owner -- either
// the goroutine New starts, or the caller of NewUnstarted via Drain.
type Writer[T any] struct {
	cfg Config[T]
	ctx context.Context

	now func() time.Time

	watchdog      *time.Timer
	watchdogArmed atomic.Bool
	lastProgress  time.Time

	// draining is set for the duration of Drain so a concurrent second
	// drainer panics rather than racing finishWrite / writing.
	draining atomic.Bool
	// waiters counts goroutines parked in EnqueueWait on THIS writer, so the
	// drain loop can skip the budget broadcast when nobody is listening. Raised
	// only once a caller's first admission attempt has failed, so a caller that
	// never parks never costs the drain a broadcast; see EnqueueWait.
	waiters atomic.Int64

	// member is this writer's handle on the byte budget. Never nil: a Config
	// with only MaxBytes gets a private single-member Pool.
	member *PoolMember

	mu    sync.Mutex
	queue []queued[T]
	// controlBytes is the part of this writer's holding that was admitted
	// against ControlReserve. Not a mirror of the member's ledger like the
	// queued-bytes counter that used to sit here: it tracks a DIFFERENT
	// quantity -- how much of the private reserve is spent -- which the pool
	// does not know about because the pool may never refuse it.
	controlBytes int64
	closed       bool
	gaveUp       bool
	// writing is true from the moment the drain pops a frame until
	// its Write returns. Flush needs it because pop() removes the frame before
	// the write happens, so an empty queue alone does not mean the last frame
	// reached the transport.
	writing bool
	// wake carries at most one pending signal; the drain loop always
	// empties the whole queue, so more would be redundant.
	wake chan struct{}
	// budgetFreed wakes EnqueueWait when a pop frees budget. Like drained (and
	// unlike wake) it is a BROADCAST -- generation closed and replaced -- not a
	// depth-1 signal. Nothing stops two goroutines parking on one writer:
	// PendingRequests.SendAndWait runs one per in-flight worker RPC, and several
	// tabs can open a channel on the same worker at once. With a coalescing
	// send, whichever parker woke first would consume the only value, find the
	// budget still short, and re-park -- leaving the other asleep until its
	// deadline even though room had appeared for it.
	budgetFreed chan struct{}
	// drained wakes Flush each time a write completes. Unlike wake and
	// budgetFreed this is a BROADCAST -- the current generation is closed and
	// replaced (swapDrainedLocked) rather than sent to -- because Flush is
	// exported and any number of goroutines may be parked in it. Flush
	// re-checks the real condition under the mutex after every wake-up.
	drained chan struct{}
}

// New starts the drain goroutine, bound to ctx.
func New[T any](ctx context.Context, cfg Config[T]) *Writer[T] {
	w := newWriter(ctx, cfg)
	go w.run()
	return w
}

// NewUnstarted constructs a Writer without starting a drain goroutine. The
// caller owns the drain: it must call Drain from exactly one goroutine (the
// typical pattern is select{ case <-w.Wake(): w.Drain() }), and must call
// Close before that goroutine goes away so no write can outlive it.
//
// The Connect server handler uses this mode because a write against a finished
// handler panics; a background drain would recreate that hazard whenever the
// handler returned first.
func NewUnstarted[T any](ctx context.Context, cfg Config[T]) *Writer[T] {
	return newWriter(ctx, cfg)
}

// newWriter constructs a Writer without starting the drain. Tests that drive
// writeItem or the budget accounting directly use it so they do not race the
// drain goroutine on lastProgress. Production handler-drain callers use
// NewUnstarted instead.
func newWriter[T any](ctx context.Context, cfg Config[T]) *Writer[T] {
	if cfg.Write == nil || cfg.Size == nil {
		panic("sendq: Config.Write and Config.Size are required")
	}
	if cfg.ControlReserve < 0 {
		panic("sendq: Config.ControlReserve must not be negative")
	}
	pool := cfg.Pool
	switch {
	case pool != nil && cfg.MaxBytes > 0:
		panic("sendq: Config.MaxBytes and Config.Pool are mutually exclusive")
	case pool == nil && cfg.MaxBytes <= 0:
		panic("sendq: exactly one of Config.MaxBytes and Config.Pool is required")
	case pool == nil:
		// A private pool whose floor IS its capacity: floor() then pins the
		// threshold at MaxBytes for every occupancy, which is the fixed
		// per-connection budget written as the shared rule's degenerate case.
		pool = NewPool(PoolConfig{
			Capacity: cfg.MaxBytes,
			MinFloor: cfg.MaxBytes,
			MaxFloor: cfg.MaxBytes,
		})
	}

	// ONE rule for both config shapes, stated against the guaranteed working
	// set, which is the thing it is really about: the reserve must leave room
	// for data, or a writer sitting at its floor has none and every data charge
	// is refused however idle the pool is.
	//
	// The two shapes used to disagree here, and the pooled one was the loose
	// half: a private pool's floor IS its MaxBytes, so `>= MaxBytes` and
	// `>= MinFloor` are the same statement, but the pooled path only rejected
	// `> MinFloor` and so accepted a reserve exactly equal to the floor -- the
	// configuration that admits nothing. Two rules for one question is how that
	// went unnoticed; there is now one, and it is the strict one.
	if cfg.ControlReserve >= pool.MinFloor() {
		panic("sendq: Config.ControlReserve must be strictly less than the guaranteed working set " +
			"(Config.MaxBytes, or the pool's MinFloor)")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	w := &Writer[T]{
		cfg:         cfg,
		ctx:         ctx,
		now:         now,
		wake:        make(chan struct{}, 1),
		budgetFreed: make(chan struct{}),
		drained:     make(chan struct{}),
	}
	// Assigned under w.mu, which giveUp also takes. attach publishes the member
	// into the pool BEFORE returning it, so another connection's eviction scan
	// can reach this member -- and its evict closure -- while this line is still
	// executing. Taking the lock the closure will need is what makes the
	// assignment visible before any evict can dereference it, instead of relying
	// on a fresh member never being nominated because it holds nothing.
	w.mu.Lock()
	w.member = pool.Attach(func(err error) bool { return w.giveUp(GiveUpPoolPressure, err) })
	w.mu.Unlock()
	if cfg.WriteTimeout > 0 {
		// Created armed-then-stopped so writeItem only ever has to Reset it.
		// CompareAndSwap on watchdogArmed ensures only one of the write
		// completion path and the AfterFunc callback "owns" the give-up: a
		// timer that fires in the same instant Write returns cannot spuriously
		// tear down a connection that just made progress.
		//
		// Winning that CAS is necessary but not sufficient. Close tears the
		// writer down for a reason that is NOT a write timeout -- workermgr's
		// Fence, an application close, the drain goroutine's own defer -- and it
		// cannot interrupt a Write already blocked on peer flow control, so the
		// timer stays armed and fires up to WriteTimeout later. Close sets
		// closed, never gaveUp, so nothing downstream stopped that late fire
		// from reporting write_timeout for a connection already gone for
		// something else. GiveUpReason promises to partition the give-up paths;
		// a timer outliving a close breaks that promise exactly as a timer
		// outliving a panic did (see writeUnderWatchdog).
		w.watchdog = time.AfterFunc(time.Hour, func() {
			if w.watchdogArmed.CompareAndSwap(true, false) && !w.isClosed() {
				w.giveUp(GiveUpWriteTimeout,
					fmt.Errorf("write timed out after %s", cfg.WriteTimeout))
			}
		})
		w.watchdog.Stop()
	}
	return w
}

// Enqueue appends item, giving up (discard + close + OnGiveUp) when the data
// byte budget would be exceeded. The Hub's policy: a client that cannot keep
// up is disconnected, because reconnect + replay-from-DB already exist.
//
// Data paths leave ControlReserve free for TryEnqueueControl, so the ceiling
// they see is this member's admission threshold minus that reserve. For a
// pooled writer the threshold moves with the pool's occupancy; for a private
// one it is the fixed MaxBytes.
func (w *Writer[T]) Enqueue(item T) error {
	size := int64(w.cfg.Size(item)) + w.cfg.FrameOverhead

	// Each turn either queues the item, gives up, or reclaims one writer's worth
	// of pool bytes and retries.
	//
	// The EVICTED arm terminates on its own: the pool marks a nominated victim
	// ineligible before tearing it down, so the scan cannot return the same one
	// twice and the arm is bounded by the member count.
	//
	// The RACED arm is not self-limiting. It fires when the pool freed enough
	// between the failed admission and the re-test, which is the same predicate
	// read at two instants -- so on a pool oscillating around the admission
	// boundary another goroutine can keep taking the bytes back before this one
	// retries, and the loop makes no progress while burning a core. The system
	// is still making progress (somebody else is enqueueing), so this is
	// starvation rather than livelock, but the caller here can be the Hub's
	// per-worker receive goroutine holding a channel's send mutex -- so spinning
	// on it stalls every channel on that worker. Yield, and after a bounded
	// number of consecutive races take the ordinary over-budget verdict: at that
	// point this connection genuinely is not getting in.
	races := 0
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return ErrClosed
		}
		outcome := w.member.charge(size, w.cfg.ControlReserve)
		if outcome == Admitted {
			w.appendLocked(item, size, false)
			w.mu.Unlock()
			w.signalWake()
			return nil
		}
		queued, frames := w.member.Charged(), len(w.queue)
		w.mu.Unlock()

		// Reclaiming has to happen with no lock held: relieve tears down
		// ANOTHER writer, and doing that under this one's mutex would order
		// writer-then-writer between two goroutines that can each be the other's
		// victim.
		reason, cause := GiveUpOverBudget, ErrOverBudget
		if outcome == Pressure {
			want := queued + size
			switch w.member.relieve(want, w.cfg.ControlReserve) {
			case relieveEvicted:
				races = 0
				continue
			case relieveRaced:
				if races++; races <= maxEnqueueRaces {
					runtime.Gosched()
					continue
				}
				// Out of retries: report it as ordinary over-budget rather than
				// pool pressure. Nobody was evicted and nothing is structurally
				// wrong with the deployment -- this connection simply lost the
				// race for the last bytes repeatedly.
			case relieveAskerAtFault:
				// This writer holds more than anyone else, so the pool is full
				// because of IT. Same verdict a private budget would reach.
			case relieveNoHog:
				// Everyone is inside the working set they were promised. Nobody
				// misbehaved; there is simply not enough budget for this many
				// connections.
				reason, cause = GiveUpPoolPressure, ErrPoolPressure
			}
		}
		w.giveUp(reason, fmt.Errorf(
			"%w: queued %d frames / %d bytes, pool %d/%d bytes across %d members",
			cause, frames, queued,
			w.member.pool.Used(), w.member.pool.Capacity(), w.member.pool.Members()))
		return ErrClosed
	}
}

// EnqueueWait appends item, BLOCKING until the data budget frees, ctx ends, or
// the writer closes. The worker's handler-data policy: the producer parks and
// the upstream source throttles itself, which is real backpressure rather than
// a drop.
//
// An item that cannot fit at ANY pool occupancy -- even against an empty pool
// -- is rejected immediately rather than parked forever. That exactness
// matters: worker/hub.Client.Send parks on context.Background(), so a
// too-conservative "never fits" test would strand the goroutine for the life of
// the process. ControlReserve headroom is not available to this path.
//
// A parked caller does NOT evict anyone. Reclaiming is the data path's job,
// where the volume (and therefore the pressure) actually comes from; a
// request/response RPC waiting its turn is not evidence that some other
// connection should be torn down.
func (w *Writer[T]) EnqueueWait(ctx context.Context, item T) error {
	size := int64(w.cfg.Size(item)) + w.cfg.FrameOverhead

	// Registered only once this call is genuinely about to park -- i.e. after an
	// admission attempt has already failed. Both counters gate a BROADCAST: the
	// pool's takes freedMu, allocates a fresh channel and closes the old one,
	// waking every parker in the pool for a release at most one of them can use.
	// Registering up front made a caller admitted on the fast path -- the common
	// case, since the budget is usually there -- hold the count above zero for
	// its whole duration, so one in-flight SendWait put that lock and allocation
	// on the dequeue path of every connection sharing the pool.
	//
	// Deregistration stays in a defer so ctx cancellation and the unfittable
	// return cannot leak a registration.
	registered := false
	defer func() {
		if registered {
			w.waiters.Add(-1)
			w.member.pool.addWaiter(-1)
		}
	}()

	for {
		// Captured before the budget test so a release that lands between the
		// test and the select still wakes this call. Nil until this call is
		// registered, which is safe because an unregistered turn always takes
		// the `continue` below rather than reaching the select.
		var poolFreed <-chan struct{}
		if registered {
			poolFreed = w.member.pool.freedGen()
		}

		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return ErrClosed
		}
		outcome := w.member.charge(size, w.cfg.ControlReserve)
		if outcome == Admitted {
			w.appendLocked(item, size, false)
			w.mu.Unlock()
			w.signalWake()
			return nil
		}
		localFreed := w.budgetFreed
		w.mu.Unlock()

		if outcome == Unfittable {
			return fmt.Errorf("%w: item %d bytes exceeds the %d-byte budget",
				ErrOverBudget, size, w.member.pool.Capacity()-w.cfg.ControlReserve)
		}

		// Register and RE-TEST rather than parking on this turn. A release that
		// lands while the counters are zero skips both broadcasts, so parking on
		// a generation captured before this point could miss it and sleep to the
		// deadline. Retrying closes that window from the other side: whatever was
		// freed before the registration is visible to the next attempt, and
		// nothing freed after it can be missed, because from here on every
		// release sees a non-zero count and turns the generation over.
		if !registered {
			registered = true
			w.waiters.Add(1)
			w.member.pool.addWaiter(1)
			continue
		}

		select {
		case <-localFreed:
		case <-poolFreed:
		case <-ctx.Done():
			return ctx.Err()
		case <-w.ctx.Done():
			return ErrClosed
		}
	}
}

// TryEnqueue appends item if the data budget allows and reports whether it
// did. It never blocks and never tears the connection down. The policy for
// best-effort sends issued from a shared receive goroutine.
func (w *Writer[T]) TryEnqueue(item T) bool {
	size := int64(w.cfg.Size(item)) + w.cfg.FrameOverhead

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false
	}
	// No relieve() here: this path exists precisely because its caller has
	// nothing at stake in the frame, so it must not take a connection down.
	if w.member.charge(size, w.cfg.ControlReserve) != Admitted {
		w.mu.Unlock()
		return false
	}
	w.appendLocked(item, size, false)
	w.mu.Unlock()

	w.signalWake()
	return true
}

// TryEnqueueControl appends item against this writer's PRIVATE ControlReserve.
// It is the reserved-budget try path for receive-goroutine control frames: an
// open response, access ack, or teardown CLOSE must not be starved by a
// saturated bulk burst -- on this writer or on any other.
//
// The reserve is spent on its own ledger and is never subject to the shared
// pool's threshold, only to its own size. That is what stops one slow browser
// tab from filling the pool and fencing every worker connection in the fleet:
// SendControl treats false as "this peer cannot accept control" and discards
// the connection, which on the worker link takes every user's channels with it.
// The bytes still count toward the pool's total -- they are real memory -- but
// the pool cannot refuse them.
//
// Still never blocks and never tears the connection down; a false return means
// this writer's own reserve is full, and the caller must decide soft-fail vs
// reset. It is not a delivery guarantee. With ControlReserve zero there is no
// separate ledger and this behaves exactly like TryEnqueue.
func (w *Writer[T]) TryEnqueueControl(item T) bool {
	if w.cfg.ControlReserve == 0 {
		return w.TryEnqueue(item)
	}
	size := int64(w.cfg.Size(item)) + w.cfg.FrameOverhead

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false
	}
	// The reserve first, then the shared budget -- not the reserve ALONE.
	//
	// A reserve is a floor under control traffic, not a ceiling on it. Spending
	// it first is what makes it a guarantee: those bytes are never subject to
	// the pool's threshold, so a pool full of somebody else's backlog can still
	// never fence this connection. But once it is spent, control has no less
	// claim on ordinary budget than data does -- and rather more, since the
	// caller's answer to false is to discard the connection. Refusing here while
	// the writer's own data queue sat empty and the pool had gigabytes free
	// turned an absorbable burst -- a workspace delete tombstoning many tabs, a
	// mass channel teardown -- into a fence that drops every user's channels on
	// that worker.
	switch {
	case w.controlBytes+size <= w.cfg.ControlReserve:
		w.member.chargeReserved(size)
		w.appendLocked(item, size, true)
	case w.member.charge(size, 0) == Admitted:
		// Zero reserve, not ControlReserve: charge's reserve argument means "hold
		// this much back for control", which is a DATA path's obligation. The
		// bytes already spent from the reserve are in this member's holding
		// (chargeReserved put them there), so passing ControlReserve here
		// subtracted it a second time and tightened control's ceiling below
		// data's. On a crowded pool, where the threshold collapses to MinFloor,
		// that fenced a worker with a quarter of its floor still unused -- and a
		// fence discards every user's channels on that machine.
		//
		// No relieve(): a control frame must not be the reason another
		// connection is torn down.
		w.appendLocked(item, size, false)
	default:
		w.mu.Unlock()
		return false
	}
	w.mu.Unlock()

	w.signalWake()
	return true
}

// appendLocked records item as queued. Caller holds mu and has already charged
// the budget, so this only updates the writer's own ledgers.
func (w *Writer[T]) appendLocked(item T, size int64, control bool) {
	w.queue = append(w.queue, queued[T]{item: item, size: size, control: control})
	if control {
		w.controlBytes += size
	}
}

// Close stops the writer, discards anything still queued, and wakes the
// drain goroutine so it observes the closure and returns.
//
// It signals wake rather than relying on the caller's context so that
// close alone is sufficient to reap the goroutine. Depending on an
// external cancel would leak one goroutine -- plus every queued frame it
// pins -- for any caller that owns the writer's lifetime without owning
// its context.
func (w *Writer[T]) Close() {
	w.mu.Lock()
	alreadyClosed := w.closed
	bytes, frames := w.discardQueueLocked()
	w.closed = true
	w.mu.Unlock()

	if frames > 0 && w.cfg.OnDiscard != nil {
		w.cfg.OnDiscard(frames, bytes)
	}
	if !alreadyClosed {
		w.signalWake()
		// Also nudge EnqueueWait parkers, and any Flush waiting on frames that
		// this close just discarded.
		w.signalBudgetFreed()
		w.signalDrained()
	}
	// Unconditional because detach is idempotent, and it has to be: a relay
	// connection closes its writer twice by construction -- the drain
	// goroutine's deferred Close plus the handler's own -- and Fence adds more.
	// Gating on alreadyClosed instead would leave the membership dangling for
	// any writer whose first Close lost a race.
	w.member.Detach()
}

// discardQueueLocked drops every queued frame and refunds its charge. Caller
// holds mu.
func (w *Writer[T]) discardQueueLocked() (bytes int64, frames int) {
	bytes, frames = w.member.Charged(), len(w.queue)
	w.queue = nil
	w.controlBytes = 0
	w.member.Release(bytes)
	return bytes, frames
}

// signalNonBlocking delivers an edge on a depth-1 signal channel, coalescing
// with any edge the receiver has not consumed yet. Correct only where exactly
// one goroutine waits on ch -- a second waiter would find the value already
// taken. Only wake (consumed by the single drain owner) satisfies that;
// budgetFreed and drained are broadcasts instead. See swapDrainedLocked.
func signalNonBlocking(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (w *Writer[T]) signalWake() { signalNonBlocking(w.wake) }

// signalBudgetFreed wakes every EnqueueWait parked on this writer.
//
// Skipped while nothing is parked, which is the overwhelmingly common case --
// the frontend relay uses Enqueue and never parks -- so the drain loop does not
// pay a channel close and allocation per frame for a wake-up with no audience.
//
// The skip cannot lose a wake-up. A signaller that reads zero here has had its
// read ordered before the parker's increment, and the parker takes w.mu after
// incrementing -- after this signaller's pop released the bytes under that same
// lock -- so the parker's next admission test sees them and never reaches the
// select. Once the count is non-zero, every subsequent release turns the
// generation over instead.
func (w *Writer[T]) signalBudgetFreed() {
	if w.waiters.Load() == 0 {
		return
	}
	w.mu.Lock()
	ch := w.budgetFreed
	w.budgetFreed = make(chan struct{})
	w.mu.Unlock()
	close(ch)
}

// swapDrainedLocked installs a fresh drained channel and returns the old one
// for the caller to close once it has released the lock.
//
// drained is a BROADCAST, not a depth-1 signal: Flush is exported and nothing
// stops two goroutines from flushing the same writer. With a coalescing send,
// whichever Flush woke first would consume the only value and the other would
// park with no producer left, then fail its own context with
// DeadlineExceeded -- reporting a failed flush for a queue that drained fine.
// Closing a generation channel wakes every waiter at once, and each re-checks
// the real condition under the lock.
func (w *Writer[T]) swapDrainedLocked() chan struct{} {
	ch := w.drained
	w.drained = make(chan struct{})
	return ch
}

func (w *Writer[T]) signalDrained() {
	w.mu.Lock()
	ch := w.swapDrainedLocked()
	w.mu.Unlock()
	close(ch)
}

// Flush blocks until the queue is empty AND the in-flight write has returned,
// so every frame enqueued before the call has been handed to the transport.
//
// Returns nil only when the queue is idle (empty and not writing) while the
// writer is still open. Returns ErrClosed if the writer closed or gave up --
// including when Close discarded queued frames -- so a caller that needs
// "reached the wire" (shutdown notify) cannot mistakingly treat a discard as
// success. Returns ctx.Err / the writer's context error when the wait was cut
// short while frames were still drainable.
//
// This is what makes a graceful shutdown's last words actually leave the
// machine. Enqueue and EnqueueWait return once a frame is QUEUED, so a caller
// that broadcasts and then tears the connection down is racing the drain
// goroutine: on an idle box the drain wins and nobody notices, under load it
// loses and the frames are discarded by Close -- Flush reports ErrClosed.
func (w *Writer[T]) Flush(ctx context.Context) error {
	for {
		// Capture the current generation channel under the same lock that read
		// the state, so a drain landing between the unlock and the select still
		// closes the channel this iteration parks on.
		w.mu.Lock()
		idle := len(w.queue) == 0 && !w.writing
		tornDown := w.closed || w.gaveUp
		drained := w.drained
		w.mu.Unlock()
		if idle {
			if tornDown {
				return ErrClosed
			}
			return nil
		}
		select {
		case <-drained:
		case <-ctx.Done():
			return ctx.Err()
		case <-w.ctx.Done():
			return w.ctx.Err()
		}
	}
}

func (w *Writer[T]) pop() (queued[T], bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || len(w.queue) == 0 {
		var zero queued[T]
		return zero, false
	}
	f := w.queue[0]
	var zero queued[T]
	w.queue[0] = zero
	w.queue = w.queue[1:]
	if f.control {
		w.controlBytes -= f.size
	}
	// The charge is refunded here rather than after the write returns, so
	// backpressure relief is not coupled to write latency. The frame is still
	// resident until Write returns, so the pool undercounts by at most one
	// in-flight frame per writer -- accounted for in Pool's sizing note.
	w.member.Release(f.size)
	// The frame has left the queue but not yet the process; Flush must keep
	// waiting until finishWrite clears this.
	w.writing = true
	return f, true
}

// finishWrite marks the popped frame as no longer in flight and wakes Flush.
// Paired with the w.writing set in pop() on every path out of writeItem,
// including the error paths -- a Flush parked behind a failed write must not
// hang waiting for a drain that will never come.
func (w *Writer[T]) finishWrite() {
	w.mu.Lock()
	w.writing = false
	ch := w.swapDrainedLocked()
	w.mu.Unlock()
	close(ch)
}

func (w *Writer[T]) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// giveUp tears the writer down and reports whether it actually reclaimed pool
// bytes. The pool counts an eviction only when this says true: a writer that had
// already given up, closed, or drained to nothing frees no memory, and counting
// it inflated the eviction metric an operator sizes from.
func (w *Writer[T]) giveUp(reason GiveUpReason, err error) bool {
	w.mu.Lock()
	if w.gaveUp {
		w.mu.Unlock()
		return false
	}
	w.gaveUp = true
	var (
		frames int
		bytes  int64
	)
	if !w.closed {
		bytes, frames = w.discardQueueLocked()
		w.closed = true
	}
	onGiveUp := w.cfg.OnGiveUp
	onDiscard := w.cfg.OnDiscard
	w.mu.Unlock()
	w.signalWake()
	w.signalBudgetFreed()
	w.signalDrained()
	// Return the membership as well as the bytes. A give-up may be the last
	// thing that ever happens to a handler-drained writer whose handler is
	// already unwinding, and a leaked member would shrink every survivor's
	// guaranteed floor for the life of the process.
	w.member.Detach()
	if onDiscard != nil && frames > 0 {
		onDiscard(frames, bytes)
	}
	if onGiveUp != nil {
		onGiveUp(reason, err)
	}
	return bytes > 0
}

// GaveUp reports whether this writer was torn down because the SENDER abandoned
// it -- a write timeout, a blown byte budget, pool pressure -- rather than being
// closed in the ordinary way.
//
// It is set under the same lock, and strictly before, the flag that makes
// Enqueue and Flush start reporting ErrClosed, so anything that observes the
// closed writer also observes why it closed. That ordering is the point: the
// OnGiveUp callback fires only AFTER the queue is closed, so a caller racing it
// would otherwise see the closure with no cause attached.
func (w *Writer[T]) GaveUp() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gaveUp
}

// Wake returns the depth-1 wake channel that fires when a frame is enqueued
// (or the writer closes). Exactly one goroutine may select on it: a second
// consumer would steal the coalesced signal that signalNonBlocking depends on.
// The goroutine-drained path parks on it inside run(); a handler-drained
// caller parks on it in its own select and then calls Drain.
func (w *Writer[T]) Wake() <-chan struct{} {
	return w.wake
}

// Drain pops and writes every currently queued frame (unlimited), then
// returns. See DrainLimited for a bounded turn used by handler selects.
func (w *Writer[T]) Drain() error {
	return w.DrainLimited(DrainLimits{})
}

// DrainLimited pops and writes queued frames until the queue is empty, a write
// fails, or the limits are hit. A non-nil error means the drain gave up (write
// failure, stall, or watchdog) and the caller should stop -- the queue is
// already closed. Nil with limits hit leaves remaining frames queued and
// re-signals Wake so a handler select can run another turn after servicing
// receives.
//
// Popping one at a time is load-bearing: pop sets writing atomically with
// removal, which is the only thing that keeps Flush from returning between
// two frames of the same batch. A concurrent second Drain panics with
// errConcurrentDrain.
//
// The caller must own the drain exclusively. run() is the goroutine-drained
// implementation of that ownership; a NewUnstarted caller is the handler-
// drained one.
func (w *Writer[T]) DrainLimited(lim DrainLimits) error {
	if !w.draining.CompareAndSwap(false, true) {
		panic(errConcurrentDrain)
	}
	defer w.draining.Store(false)

	// Restart the stall clock: reaching here means the peer owed us nothing
	// until this wake-up. Idle time is not stalled time, and this socket may
	// have no keepalive to refresh the clock for us.
	w.lastProgress = w.now()
	start := w.now()
	frames := 0

	for {
		if lim.MaxFrames > 0 && frames >= lim.MaxFrames {
			w.signalWakeIfQueued()
			return nil
		}
		if lim.MaxDuration > 0 && w.now().Sub(start) >= lim.MaxDuration {
			w.signalWakeIfQueued()
			return nil
		}
		frame, ok := w.pop()
		if !ok {
			return nil
		}
		frames++
		// Wake EnqueueWait parkers as soon as the bytes leave the queue,
		// not after the (up to WriteTimeout-long) write finishes. The
		// budget is already free; delaying the signal couples backpressure
		// relief to write latency for no functional reason.
		w.signalBudgetFreed()
		if err := w.writeTurn(frame.item); err != nil {
			if w.isClosed() {
				return err
			}
			reason := GiveUpWriteError
			if errors.Is(err, errStalled) {
				reason = GiveUpStall
			}
			w.giveUp(reason, err)
			return err
		}
	}
}

// writeTurn writes one frame and always clears the in-flight flag, including
// when Write PANICS. The flag left set would park every later Flush on this
// writer until its deadline, and the deferred clear is what survives an unwind.
//
// Both drain modes catch that unwind, one layer up: a handler-drained writer
// unwinds into workermgr.SendPump.drain, a goroutine-drained one (sendq.New --
// the frontend relay and the worker's Hub client) into run's own recover. Either
// way the connection is given up and the process survives. The frame's pool
// charge is refunded at pop, so it is not leaked on that path either.
func (w *Writer[T]) writeTurn(item T) error {
	defer w.finishWrite()
	return w.writeItem(item)
}

func (w *Writer[T]) signalWakeIfQueued() {
	w.mu.Lock()
	has := !w.closed && len(w.queue) > 0
	w.mu.Unlock()
	if has {
		w.signalWake()
	}
}

// run drains this writer on its own goroutine until the context ends or the
// writer closes.
//
// It recovers a panicking transport for the same reason workermgr.SendPump.drain
// does on the handler-drained path: a Write that panics is one connection's
// problem, and letting it unwind out of here takes the whole process -- every
// other connection and user with it. Without this the two drain modes had
// opposite blast radii for the identical fault, which is not a policy anyone
// chose.
//
// errConcurrentDrain is re-panicked rather than absorbed: it reports that two
// goroutines are draining one writer, a programming invariant whose whole point
// is to be loud, and swallowing it would leave the writer being torn down by
// somebody else while this loop kept going.
func (w *Writer[T]) run() {
	defer w.Close()
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if IsConcurrentDrainPanic(r) {
			panic(r)
		}
		slog.Error("sendq: recovered from a panicking transport write; dropping the connection",
			"panic", r)
		// Same teardown a write error takes, so the owner learns about it
		// through the channel it already watches rather than a silent stop.
		w.giveUp(GiveUpWriteError, fmt.Errorf("sendq: transport write panicked: %v", r))
	}()
	w.lastProgress = w.now()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
		}

		if err := w.Drain(); err != nil {
			return
		}
		if w.isClosed() {
			return
		}
	}
}

func (w *Writer[T]) writeItem(item T) error {
	if w.cfg.MaxStall > 0 {
		if stalled := w.now().Sub(w.lastProgress); stalled > w.cfg.MaxStall {
			return fmt.Errorf("%w: client made no progress for %s (limit %s)",
				errStalled, stalled.Round(time.Millisecond), w.cfg.MaxStall)
		}
	}

	if w.watchdog == nil {
		if err := w.cfg.Write(w.ctx, item); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		w.lastProgress = w.now()
		return nil
	}

	err, disarmed := w.writeUnderWatchdog(item)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if !disarmed {
		// The AfterFunc won the race, so it owns the teardown: it either called
		// giveUp, or found the writer already closed and left it to Close.
		// Either way this writer is finished and the frame's fate is decided
		// elsewhere.
		return ErrClosed
	}
	w.lastProgress = w.now()
	return nil
}

// writeUnderWatchdog runs one transport Write with the per-write watchdog armed,
// and reports whether THIS goroutine disarmed it. False means the AfterFunc won
// the race and has already given the writer up.
//
// One reusable timer rather than a context+timer per frame. Timer.Stop cannot
// retract a callback already running, so the watchdogArmed CAS -- not the Stop
// -- is what keeps a watchdog firing as this write returns from cancelling a
// connection that just made progress.
//
// The disarm runs from a defer so it also happens when Write PANICS. On the
// goroutine-drained path a timer left armed across the unwind was harmless by
// defer ordering -- run's recover reaches giveUp first, so the late fire no-ops
// on the gaveUp guard -- but the handler-drained path recovers by calling
// workermgr's Fence, which closes the queue WITHOUT setting gaveUp and never
// calls giveUp. Up to WriteTimeout later the watchdog then fired a give-up
// labelled write_timeout for a connection that had died of a panic, while the
// panic itself was counted as nothing. GiveUpReason promises to partition the
// give-up paths; a timer surviving an unwind breaks that promise.
func (w *Writer[T]) writeUnderWatchdog(item T) (err error, disarmed bool) {
	w.watchdogArmed.Store(true)
	w.watchdog.Reset(w.cfg.WriteTimeout)
	defer func() {
		disarmed = w.watchdogArmed.CompareAndSwap(true, false)
		w.watchdog.Stop()
	}()
	err = w.cfg.Write(w.ctx, item)
	// Naked, because disarmed is the deferred disarm's to set -- on this path
	// and on the panicking one alike. Naming a value here would be one the
	// caller never sees.
	return
}

// QueuedBytes reports the current charged byte count. For tests.
//
// Reads the pool member's ledger, which IS this writer's holding -- there is no
// second counter to disagree with it.
func (w *Writer[T]) QueuedBytes() int64 { return w.member.Charged() }

// QueuedLen reports the number of queued items. For tests.
func (w *Writer[T]) QueuedLen() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue)
}

// IsClosed reports whether Close (or an over-budget kill) has run. For tests.
func (w *Writer[T]) IsClosed() bool {
	return w.isClosed()
}

// NewMaxBytesPoolForTest builds the private-budget pool a test wants when it
// cares about a writer's behaviour rather than about sharing: one member, the
// whole DefaultMaxBytes to itself at every occupancy.
//
// All three bounds are pinned, exactly as newWriter pins them for a Config with
// a plain MaxBytes, and that is what makes the sentence above true. Leaving the
// floors to default handed a lone member max(Capacity-used, DefaultMaxFloor),
// which settles at about Capacity/2: the helper admitted 16 MiB where the writer
// it stands in for admits 32, so bounds written against DefaultMaxBytes tripped
// at half of it and tests read as exercising a saturated budget while only ever
// reaching the dynamic branch. It also reported a 1 MiB MinFloor, which is what
// newWriter validates ControlReserve against.
//
// Here rather than copied into each test package because three of them had the
// same two-line helper under two different names, each with its own copy of the
// same comment. No testing.TB parameter: nothing in it can fail a test, and
// taking one would drag the testing package into a production import graph.
func NewMaxBytesPoolForTest() *Pool {
	return NewPool(PoolConfig{
		Capacity: DefaultMaxBytes,
		MinFloor: DefaultMaxBytes,
		MaxFloor: DefaultMaxBytes,
	})
}

// PopForTest removes and returns the head item without writing it. For tests
// that drive the budget accounting without a live drain.
//
// finishWrite is what pairs with pop()'s `writing = true`, and it is required
// here too even though nothing is written: the frame is out of the queue and
// out of the process, so leaving the flag set would park every later Flush on
// this writer until its context expired.
func (w *Writer[T]) PopForTest() (T, bool) {
	f, ok := w.pop()
	if ok {
		w.signalBudgetFreed()
		w.finishWrite()
	}
	return f.item, ok
}

// SetNowForTest replaces the stall clock. For tests; call before any drain
// activity that reads it, or while the drain is idle.
func (w *Writer[T]) SetNowForTest(now func() time.Time) {
	w.now = now
}

// SetLastProgressForTest plants the stall clock. For tests of the stall check.
func (w *Writer[T]) SetLastProgressForTest(t time.Time) {
	w.lastProgress = t
}

// WriteItemForTest runs the stall+write path for one item without going
// through the queue. For tests of the stall bound in isolation.
func (w *Writer[T]) WriteItemForTest(item T) error {
	return w.writeItem(item)
}
