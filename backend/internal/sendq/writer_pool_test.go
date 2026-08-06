package sendq

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// pooledWriter builds an undrained writer on pool so a test can drive the
// budget without racing a drain goroutine. giveUps records every teardown so
// the CAUSE can be asserted, not just the fact.
func pooledWriter(t *testing.T, pool *Pool, reserve int64) (
	*Writer[*leapmuxv1.ChannelMessage], *atomic.Pointer[GiveUpReason],
) {
	t.Helper()
	var reason atomic.Pointer[GiveUpReason]
	w := newWriter(context.Background(), Config[*leapmuxv1.ChannelMessage]{
		Write:          func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
		Size:           frameSize,
		Pool:           pool,
		ControlReserve: reserve,
		OnGiveUp:       func(r GiveUpReason, _ error) { reason.Store(&r) },
	})
	t.Cleanup(w.Close)
	return w, &reason
}

// fillWriter loads one writer to its current ceiling.
func fillWriter(w *Writer[*leapmuxv1.ChannelMessage], size int) {
	for i := 0; w.TryEnqueue(testFrameOfSize(uint64(i), size)); i++ {
	}
}

// saturate builds n writers and loads them round-robin until the pool reaches
// the admission rule's fixed point.
//
// Round-robin, not one at a time: a lone writer settles at half the capacity
// and leaves the pool with room, so filling sequentially would never reach the
// saturated state these tests are about.
//
// TryEnqueue, not Enqueue: exceeding the ceiling is precisely what makes
// Enqueue give up and discard the whole backlog, so a writer filled that way
// ends up holding nothing. TryEnqueue stops at the same ceiling and reports it.
func saturate(t *testing.T, pool *Pool, n, size int) []*Writer[*leapmuxv1.ChannelMessage] {
	t.Helper()
	writers := make([]*Writer[*leapmuxv1.ChannelMessage], n)
	for i := range writers {
		writers[i], _ = pooledWriter(t, pool, 0)
	}
	for progressed := true; progressed; {
		progressed = false
		for _, w := range writers {
			if w.TryEnqueue(testFrameOfSize(0, size)) {
				progressed = true
			}
		}
	}
	// Saturated means "at the rule's fixed point": every writer is holding as
	// much as the shared threshold currently allows it. Re-checked here rather
	// than inferred from the loop exiting, so a change to the threshold that
	// broke convergence would fail loudly instead of silently leaving the tests
	// below running against a half-full pool.
	require.False(t, writers[0].TryEnqueue(testFrameOfSize(0, size)),
		"precondition: the pool must be at its fixed point")
	require.Greater(t, pool.Used(), pool.Capacity()/2,
		"precondition: the pool must actually be full")
	return writers
}

// TestWriterPoolPressureReclaimsFromTheHogNotTheNewcomer is the property the
// whole shared budget turns on.
//
// The obvious rule -- refuse whoever asks once the pool is full -- drops
// whichever connection speaks NEXT, and since terminal output is one frame per
// PTY read that is every active connection at once. Each then reconnects and
// replays from the DB, refilling the pool: congestion collapse dressed up as
// load shedding. The connection that must go is the one actually holding the
// memory.
func TestWriterPoolPressureReclaimsFromTheHogNotTheNewcomer(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		floor    = 512 << 10
	)
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})
	hogs := saturate(t, pool, 12, 128<<10)

	healthy, healthyReason := pooledWriter(t, pool, 0)

	// A connection carrying a trickle must keep being served while the pool is
	// saturated by other people's backlogs. This is the case the naive rule
	// gets wrong: it would drop `healthy` simply for speaking next.
	for i := range 8 {
		require.NoError(t, healthy.Enqueue(testFrameOfSize(uint64(i), 4096)),
			"a near-empty connection must not be dropped for another's backlog")
	}
	require.Nil(t, healthyReason.Load(), "the newcomer must not be torn down")
	assert.Zero(t, pool.Evictions(), "serving a trickle from the floor reclaims nothing")

	// Now `healthy` legitimately needs more than its guaranteed floor. Somebody
	// has to go -- and it must be a connection actually holding memory.
	require.NoError(t, healthy.Enqueue(testFrameOfSize(99, floor)))
	assert.Nil(t, healthyReason.Load(), "the asker must survive once room was reclaimed")
	assert.Positive(t, pool.Evictions())

	closed := 0
	for _, h := range hogs {
		if h.IsClosed() {
			closed++
			assert.Zero(t, h.QueuedBytes(), "a reclaimed writer returns its whole backlog")
		}
	}
	assert.Positive(t, closed, "the memory has to come from a connection that was holding it")
}

// TestWriterPoolPressureEvictsTheLargestHolder pins that the victim is chosen
// by how much it holds, not by arrival order and not by who asked.
func TestWriterPoolPressureEvictsTheLargestHolder(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		floor    = 256 << 10
	)
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

	// Placed while the pool is empty so it ends up holding far more than the
	// crowd that arrives after it -- which makes the victim unambiguous.
	large, largeReason := pooledWriter(t, pool, 0)
	require.True(t, large.TryEnqueue(testFrameOfSize(1, 3<<20)))

	crowd := saturate(t, pool, 24, 32<<10)

	// More than the reserve the rule keeps free for newcomers, so the asker
	// genuinely cannot be served without reclaiming from somebody.
	asker, askerReason := pooledWriter(t, pool, 0)
	require.NoError(t, asker.Enqueue(testFrameOfSize(2, capacity/4)))

	assert.True(t, large.IsClosed(), "the largest holder is the one reclaimed")
	require.NotNil(t, largeReason.Load())
	assert.Equal(t, GiveUpPoolPressure, *largeReason.Load(),
		"a reclaimed bystander must not be reported as having blown its own budget")
	assert.Nil(t, askerReason.Load(), "the asker must survive")
	assert.Equal(t, int64(1), pool.Evictions())
	for i, w := range crowd {
		assert.False(t, w.IsClosed(), "crowd member %d held far less and must be left alone", i)
	}
}

// TestWriterPoolReportsPressureWhenNobodyIsAHog pins the honest diagnosis for
// an undersized deployment: with every connection inside the working set it was
// promised, there is nothing to reclaim and nobody to blame.
func TestWriterPoolReportsPressureWhenNobodyIsAHog(t *testing.T) {
	t.Parallel()

	// Four floors' worth of capacity, six connections: the promises no longer
	// fit, which is exactly the overcommit case.
	const floor = 1 << 20
	pool := NewPool(PoolConfig{Capacity: 4 * floor, MinFloor: floor, MaxFloor: floor})

	writers := make([]*Writer[*leapmuxv1.ChannelMessage], 6)
	reasons := make([]*atomic.Pointer[GiveUpReason], len(writers))
	for i := range writers {
		writers[i], reasons[i] = pooledWriter(t, pool, 0)
		require.NoError(t, writers[i].Enqueue(testFrameOfSize(uint64(i), floor-4096)))
	}

	// Every writer is at its floor, so the next frame anywhere cannot be
	// granted and cannot be paid for by reclaiming.
	last := writers[len(writers)-1]
	require.Error(t, last.Enqueue(testFrameOfSize(99, floor)))
	require.NotNil(t, reasons[len(reasons)-1].Load())
	assert.Equal(t, GiveUpPoolPressure, *reasons[len(reasons)-1].Load(),
		"an undersized pool must not be reported as a misbehaving client")
	assert.Positive(t, pool.Overcommits())
}

// TestWriterControlFrameSurvivesAFullPool is the guard against a fleet-wide
// fence storm.
//
// workermgr.Conn treats a refused control frame as "this peer cannot accept
// control" and fences the connection -- which on the worker link discards every
// user's channels. If the control reserve drew from the shared pool, one slow
// browser tab filling the pool would fence every worker in the fleet.
func TestWriterControlFrameSurvivesAFullPool(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		floor    = 1 << 20
		reserve  = 256 << 10
	)
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

	// The worker is holding real data of its own AND the pool is exhausted by
	// other connections -- the two conditions that together would otherwise
	// make a control frame unplaceable.
	worker, workerReason := pooledWriter(t, pool, reserve)
	require.NoError(t, worker.Enqueue(testFrameOfSize(1, 512<<10)))
	saturate(t, pool, 12, 128<<10)

	assert.True(t, worker.TryEnqueueControl(testFrameOfSize(2, 4096)),
		"a control frame must never be refused because of another connection's memory")
	assert.Nil(t, workerReason.Load(), "placing control must not tear anything down")

	// The reserve is still a bound -- on its OWN ledger, not on how many control
	// frames can be placed. Counting placements would assert the opposite of the
	// documented rule: once the reserve is spent, control competes for ordinary
	// pool budget like anything else, which is exactly what stops an absorbable
	// burst (a workspace delete tombstoning many tabs) from fencing the worker.
	// What must never grow past ControlReserve is the private ledger.
	for i := range 1000 {
		if !worker.TryEnqueueControl(testFrameOfSize(uint64(i), 4096)) {
			worker.mu.Lock()
			spent := worker.controlBytes
			worker.mu.Unlock()
			assert.Positive(t, spent, "the reserve must actually have been drawn on")
			assert.LessOrEqual(t, spent, int64(reserve),
				"the private control ledger must never exceed ControlReserve")
			return
		}
	}
	t.Fatal("control must be refused once both the reserve and the pool are exhausted")
}

// TestWriterControlReserveIsRefundedOnPop pins that the private control ledger
// is released as its frames drain. Leaking it would let a long-lived worker
// connection slowly lose the ability to send control -- and the symptom would
// be a fence, days later, with nothing to point at.
func TestWriterControlReserveIsRefundedOnPop(t *testing.T) {
	t.Parallel()

	const reserve = 64 << 10
	pool := NewPool(PoolConfig{Capacity: 4 << 20})
	w, _ := pooledWriter(t, pool, reserve)

	for range 8 {
		require.True(t, w.TryEnqueueControl(testFrameOfSize(1, 4096)))
	}
	require.Positive(t, w.QueuedBytes())

	for {
		if _, ok := w.PopForTest(); !ok {
			break
		}
	}
	assert.Zero(t, w.QueuedBytes())
	assert.Zero(t, pool.Used(), "draining must refund the pool as well as the writer")

	// The ledger is genuinely reset, not merely emptied: the reserve is
	// spendable again from scratch.
	for range 8 {
		assert.True(t, w.TryEnqueueControl(testFrameOfSize(2, 4096)))
	}
}

// TestWriterEnqueueWaitWakesOnAnotherWritersDrain pins that a parked caller
// tracks the SHARED budget. Waiting only on its own writer's drain would sleep
// it until its deadline while the bytes it needed were freed next door.
func TestWriterEnqueueWaitWakesOnAnotherWritersDrain(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		floor    = 64 << 10
	)
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})
	writers := saturate(t, pool, 12, 128<<10)
	keeper, waiter := writers[0], writers[1]

	// waiter is already at its share of a saturated pool, so this parks. Only
	// the pool freeing bytes can complete it -- nothing on waiter's own queue
	// will ever move.
	done := make(chan error, 1)
	go func() {
		done <- waiter.EnqueueWait(context.Background(), testFrameOfSize(99, 128<<10))
	}()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-done:
			require.NoError(t, err)
			return
		case <-deadline:
			t.Fatal("EnqueueWait never woke on another writer's drain")
		default:
		}
		if _, ok := keeper.PopForTest(); !ok {
			// keeper is empty: everything it could free has been freed, so the
			// parked caller must already have resumed.
			select {
			case err := <-done:
				require.NoError(t, err)
				return
			case <-time.After(2 * time.Second):
				t.Fatal("the pool freed bytes but the parked caller did not resume")
			}
		}
	}
}

// NewMaxBytesPoolForTest exists so a test that cares about a writer's behaviour
// rather than about sharing gets the private budget that writer would really
// have. It has to BE that budget, or every bound written against
// DefaultMaxBytes silently tests half of it.
//
// It built its pool with Capacity alone and let the floors default, so a lone
// member's ceiling was the dynamic max(Capacity-used, 4 MiB) and admission
// stopped at about Capacity/2 -- 16 MiB where newWriter's private pool, which
// pins all three bounds, grants a flat 32 MiB at every occupancy. Tests reading
// as "saturate the connection's whole budget" tripped at half of it, on a
// threshold that was still falling rather than the fixed one they described.
func TestMaxBytesPoolForTestMatchesAPrivateBudget(t *testing.T) {
	t.Parallel()

	const frame = 64 << 10
	fillTo := func(t *testing.T, cfg Config[*leapmuxv1.ChannelMessage]) int64 {
		t.Helper()
		cfg.Write = func(context.Context, *leapmuxv1.ChannelMessage) error { return nil }
		cfg.Size = frameSize
		cfg.ControlReserve = DefaultControlReserve
		w := newWriter(context.Background(), cfg)
		t.Cleanup(w.Close)
		fillWriter(w, frame)
		return w.QueuedBytes()
	}

	pool := NewMaxBytesPoolForTest()
	assert.Equal(t, DefaultMaxBytes, pool.MinFloor(),
		"the guaranteed working set is the whole budget, and it is what newWriter validates "+
			"ControlReserve against")

	pooled := fillTo(t, Config[*leapmuxv1.ChannelMessage]{Pool: pool})
	private := fillTo(t, Config[*leapmuxv1.ChannelMessage]{MaxBytes: DefaultMaxBytes})

	assert.Equal(t, DefaultMaxBytes-DefaultControlReserve, pooled,
		"a lone member must be able to queue the whole budget less the control reserve")
	assert.Equal(t, private, pooled,
		"the helper must not be a smaller budget wearing the name of the real one")
}

// A caller admitted on its first attempt never parks, so it must not register as
// a waiter.
//
// Pool.release fires the pool-global broadcast whenever the count is non-zero,
// and that broadcast takes freedMu, allocates a fresh channel and closes the old
// one -- waking every parker in the pool for bytes at most one of them can use.
// Registering before the first attempt held the count above zero for the whole
// of a call that parked for no part of it, so ONE in-flight SendWait put that
// lock and that allocation on the dequeue path of every connection sharing the
// pool.
//
// A held freedMu is the seam: a call that reaches for the pool's freed
// generation blocks on it, and a call with no business there does not.
func TestWriterEnqueueWaitSkipsTheWaiterGateWhenItNeverParks(t *testing.T) {
	t.Parallel()

	pool := NewPool(PoolConfig{Capacity: 8 << 20})
	w, reason := pooledWriter(t, pool, 0)

	pool.freedMu.Lock()
	defer pool.freedMu.Unlock()

	done := make(chan error, 1)
	go func() { done <- w.EnqueueWait(context.Background(), testFrameOfSize(1, 4096)) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("an EnqueueWait admitted on its first attempt reached the pool's freed generation, " +
			"so it had registered as a waiter it never became")
	}
	assert.Zero(t, pool.waiters.Load(), "and it must leave the count where it found it")
	assert.Equal(t, 1, w.QueuedLen(), "the frame is queued, not merely not-parked")
	assert.Nil(t, reason.Load())
}

// ...and a caller that DOES park still registers, because that count is the only
// thing that makes the release path broadcast to it at all.
func TestWriterEnqueueWaitRegistersOnceItParks(t *testing.T) {
	t.Parallel()

	const capacity = 1 << 20
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: 4096, MaxFloor: 4096})
	w, _ := pooledWriter(t, pool, 0)
	require.NoError(t, w.Enqueue(testFrameOfSize(1, capacity/2)))
	require.False(t, w.TryEnqueue(testFrameOfSize(2, capacity/2)),
		"precondition: the budget must be full, or this call would not park")

	done := make(chan error, 1)
	go func() { done <- w.EnqueueWait(context.Background(), testFrameOfSize(3, capacity/2)) }()

	require.Eventually(t, func() bool { return pool.waiters.Load() == 1 },
		5*time.Second, time.Millisecond,
		"a caller about to park must be visible to the release path before it sleeps")

	_, ok := w.PopForTest()
	require.True(t, ok)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the parked caller was never woken by the drain")
	}
	assert.Zero(t, pool.waiters.Load(), "and it deregisters on the way out")
}

// Deferring the registration must not open a lost-wake-up window. A release
// landing between the failed attempt and the park has to be either visible to
// the retry that follows the registration, or still ahead of the generation that
// retry captures -- there is no third case, and if there were, a parker would
// sleep to its deadline with the room it needed already free.
func TestWriterEnqueueWaitNeverSleepsThroughARelease(t *testing.T) {
	t.Parallel()

	const (
		capacity = 1 << 20
		frame    = 64 << 10
		parkers  = 8
		rounds   = 40
	)
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: frame, MaxFloor: frame})
	w, _ := pooledWriter(t, pool, 0)
	fillWriter(w, frame)
	require.Positive(t, w.QueuedLen(), "precondition: the writer starts at its ceiling")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for range parkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				if err := w.EnqueueWait(ctx, testFrameOfSize(1, frame)); err != nil {
					assert.NoError(t, err, "a parked caller must be woken, not time out")
					return
				}
			}
		}()
	}

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()

	// One drainer, freeing the only bytes that can complete any of them.
	for {
		select {
		case <-finished:
			return
		case <-w.Wake():
		case <-ctx.Done():
			t.Fatal("a parked EnqueueWait slept through the release that made room for it")
		}
		for {
			if _, ok := w.PopForTest(); !ok {
				break
			}
		}
	}
}

// TestWriterEnqueueWaitFailsFastOnlyOnAnImpossibleItem pins the exactness of
// the never-fits test. worker/hub.Client.Send parks on context.Background(), so
// a rule that merely APPROXIMATED "too big" would strand that goroutine for the
// life of the process -- or fail a frame that a moment's wait would have taken.
func TestWriterEnqueueWaitFailsFastOnlyOnAnImpossibleItem(t *testing.T) {
	t.Parallel()

	const capacity = 1 << 20
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: 4096, MaxFloor: 4096})
	w, _ := pooledWriter(t, pool, 0)

	// Larger than the pool could ever hold: reject immediately.
	err := w.EnqueueWait(context.Background(), testFrameOfSize(1, capacity+1))
	require.ErrorIs(t, err, ErrOverBudget)

	// Exactly what an empty pool can hold: must be accepted, not rejected by an
	// over-eager approximation.
	require.NoError(t, w.EnqueueWait(context.Background(),
		testFrameOfSize(2, capacity)))
}

// TestWriterPoolIsCleanAfterEveryTeardownPath pins that no teardown leaks a
// charge or a membership. A leak here is invisible until it has silently shrunk
// every surviving connection's guaranteed floor.
func TestWriterPoolIsCleanAfterEveryTeardownPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		teardown func(*Writer[*leapmuxv1.ChannelMessage])
	}{
		{"close", func(w *Writer[*leapmuxv1.ChannelMessage]) { w.Close() }},
		{
			// Production does exactly this: the drain goroutine's deferred
			// Close plus the handler's own.
			name:     "close twice",
			teardown: func(w *Writer[*leapmuxv1.ChannelMessage]) { w.Close(); w.Close() },
		},
		{
			name: "give up",
			teardown: func(w *Writer[*leapmuxv1.ChannelMessage]) {
				w.giveUp(GiveUpStall, ErrOverBudget)
			},
		},
		{
			name: "give up then close",
			teardown: func(w *Writer[*leapmuxv1.ChannelMessage]) {
				w.giveUp(GiveUpWriteError, ErrOverBudget)
				w.Close()
			},
		},
		{
			name: "drain then close",
			teardown: func(w *Writer[*leapmuxv1.ChannelMessage]) {
				for {
					if _, ok := w.PopForTest(); !ok {
						break
					}
				}
				w.Close()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := NewPool(PoolConfig{Capacity: 4 << 20})
			w := newWriter(context.Background(), Config[*leapmuxv1.ChannelMessage]{
				Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
				Size:  frameSize, Pool: pool,
				OnGiveUp: func(GiveUpReason, error) {},
			})
			for i := range 5 {
				require.NoError(t, w.Enqueue(testFrameOfSize(uint64(i), 64<<10)))
			}
			require.Positive(t, pool.Used())

			tt.teardown(w)

			assert.Zero(t, pool.Used(), "%s must refund every charged byte", tt.name)
			assert.Zero(t, pool.Members(), "%s must return the pool slot", tt.name)
		})
	}
}

func TestNewWriterRejectsAmbiguousBudgets(t *testing.T) {
	t.Parallel()

	base := func() Config[*leapmuxv1.ChannelMessage] {
		return Config[*leapmuxv1.ChannelMessage]{
			Write: func(context.Context, *leapmuxv1.ChannelMessage) error { return nil },
			Size:  frameSize,
		}
	}

	assert.PanicsWithValue(t, "sendq: exactly one of Config.MaxBytes and Config.Pool is required",
		func() { newWriter(context.Background(), base()) },
		"an unbudgeted writer would be exactly the unbounded queue this package exists to prevent")

	assert.PanicsWithValue(t, "sendq: Config.MaxBytes and Config.Pool are mutually exclusive",
		func() {
			cfg := base()
			cfg.MaxBytes = 1 << 20
			cfg.Pool = NewPool(PoolConfig{Capacity: 1 << 20})
			newWriter(context.Background(), cfg)
		},
		"two budgets are two sources of truth for one question")

	// ONE rule for both config shapes, and it is the strict one. The two used to
	// disagree: a private pool's floor IS its MaxBytes, so the MaxBytes arm
	// rejected a reserve EQUAL to the guarantee while the pooled arm accepted
	// it -- and equal is precisely the configuration that admits no data at all
	// once a writer is sitting at its floor.
	const reserveRule = "sendq: Config.ControlReserve must be strictly less than the guaranteed " +
		"working set (Config.MaxBytes, or the pool's MinFloor)"

	assert.PanicsWithValue(t, reserveRule,
		func() {
			cfg := base()
			cfg.Pool = NewPool(PoolConfig{Capacity: 8 << 20, MinFloor: 1 << 20, MaxFloor: 1 << 20})
			cfg.ControlReserve = (1 << 20) + 1
			newWriter(context.Background(), cfg)
		},
		"a control reserve larger than the guaranteed floor could not be placed by a writer sitting at its floor")

	assert.PanicsWithValue(t, reserveRule,
		func() {
			cfg := base()
			cfg.Pool = NewPool(PoolConfig{Capacity: 8 << 20, MinFloor: 1 << 20, MaxFloor: 1 << 20})
			cfg.ControlReserve = 1 << 20
			newWriter(context.Background(), cfg)
		},
		"a reserve EQUAL to the floor leaves no room for data, which the pooled path used to accept")

	assert.PanicsWithValue(t, reserveRule,
		func() {
			cfg := base()
			cfg.MaxBytes = 1 << 20
			cfg.ControlReserve = 1 << 20
			newWriter(context.Background(), cfg)
		})
}

// TestWriterSeparatePoolsBoundBlastRadius pins the reason the Hub runs one pool
// per class of connection instead of one shared total.
//
// Reclaiming can only ever tear down a MEMBER, so pool membership is a blast
// radius as much as a budget. Browser relays and worker streams have failure
// costs that differ by orders of magnitude -- a dropped tab reconnects and
// replays from the DB, a dropped worker discards every user's channels on that
// machine and has no replay in that direction -- so a shared budget would let
// the cheap failure be traded for the dear one whenever the worker happened to
// be the largest holder.
func TestWriterSeparatePoolsBoundBlastRadius(t *testing.T) {
	t.Parallel()

	const (
		capacity = 8 << 20
		floor    = 256 << 10
	)
	relayPool := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})
	workerPool := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

	// The worker is holding more than any single relay will -- exactly the
	// shape that makes it the victim under one shared budget.
	worker, workerReason := pooledWriter(t, workerPool, 0)
	require.True(t, worker.TryEnqueue(testFrameOfSize(1, 4<<20)))

	// Now saturate the relay side and push it into reclaiming.
	saturate(t, relayPool, 24, 32<<10)
	asker, askerReason := pooledWriter(t, relayPool, 0)
	require.NoError(t, asker.Enqueue(testFrameOfSize(2, capacity/4)))

	assert.Positive(t, relayPool.Evictions(), "relay pressure must be resolved inside the relay pool")
	assert.Nil(t, askerReason.Load())

	assert.False(t, worker.IsClosed(),
		"a browser tab's backlog must never be able to take a worker connection down")
	assert.Nil(t, workerReason.Load())
	assert.Zero(t, workerPool.Evictions())
	assert.Equal(t, int64(4<<20), workerPool.Used(),
		"the worker's charge must be untouched by relay-side reclaiming")
}

// TestWriterSeparatePoolsDoNotShareCapacity pins that the budgets are genuinely
// independent in the other direction too: exhausting one must not shrink what
// the other admits, or the split would be cosmetic.
func TestWriterSeparatePoolsDoNotShareCapacity(t *testing.T) {
	t.Parallel()

	const capacity = 4 << 20
	relayPool := NewPool(PoolConfig{Capacity: capacity, MinFloor: 64 << 10, MaxFloor: 64 << 10})
	workerPool := NewPool(PoolConfig{Capacity: capacity, MinFloor: 64 << 10, MaxFloor: 64 << 10})

	saturate(t, relayPool, 16, 64<<10)
	relayUsedBefore := relayPool.Used()
	require.Positive(t, relayUsedBefore)

	// A fresh worker writer must still get the full lone-writer grant -- about
	// half its own pool -- with the relay pool full.
	worker, workerReason := pooledWriter(t, workerPool, 0)
	fillWriter(worker, 64<<10)
	assert.InDelta(t, capacity/2, worker.QueuedBytes(), float64(capacity)*0.05,
		"a saturated relay pool must not shrink what the worker pool grants")
	assert.Nil(t, workerReason.Load())
	assert.Equal(t, relayUsedBefore, relayPool.Used(),
		"filling the worker pool must not charge, or free, a single relay byte")
}

// TestGiveUpReasonLabelsPartitionTheCauses pins that every give-up path has a
// distinct, stable label.
//
// metrics.SendqGiveUpsTotal is labelled from these, and the house rule for that
// package is that the label set must be a complete partition of outcomes: an
// "unknown" leaking into the series would show a disconnect nobody can account
// for, and two reasons sharing a string would silently merge two different
// operator responses into one number.
func TestGiveUpReasonLabelsPartitionTheCauses(t *testing.T) {
	t.Parallel()

	all := []GiveUpReason{
		GiveUpOverBudget, GiveUpPoolPressure, GiveUpStall, GiveUpWriteTimeout, GiveUpWriteError,
	}
	seen := map[string]bool{}
	for _, r := range all {
		label := r.Label()
		assert.NotEqual(t, "unknown", label, "reason %d must have a label", r)
		assert.False(t, seen[label], "label %q is used by more than one reason", label)
		seen[label] = true
	}
	assert.Len(t, seen, len(all))

	// The default arm exists so an unhandled value cannot panic a scrape; it
	// must stay reachable and distinguishable.
	assert.Equal(t, "unknown", GiveUpReason(-1).Label())
}

// TestWriterControlWithoutAReserveSharesTheDataBudget pins the documented
// zero-ControlReserve behaviour: with no separate ledger, TryEnqueueControl is
// TryEnqueue. The frontend relay configures exactly this, so a control path
// that quietly admitted against an unbounded allowance would escape the pool.
func TestWriterControlWithoutAReserveSharesTheDataBudget(t *testing.T) {
	t.Parallel()

	const capacity = 1 << 20
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: 4096, MaxFloor: 4096})
	w, reason := pooledWriter(t, pool, 0)

	require.True(t, w.TryEnqueueControl(testFrameOfSize(1, 4096)))
	assert.Equal(t, int64(4096), pool.Used(),
		"a control frame with no reserve must still be charged to the pool")

	// And it is bounded by the same ceiling as data, not by a private
	// allowance: fill the writer, then a further control frame must be refused
	// rather than admitted on the strength of a reserve that does not exist.
	fillWriter(w, 64<<10)
	assert.False(t, w.TryEnqueueControl(testFrameOfSize(2, 64<<10)))
	assert.Nil(t, reason.Load(), "a refused try-enqueue must never tear the connection down")
}

// TestWriterControlReserveIsChargedToThePool pins that the private control
// ledger is still REAL memory as far as the pool is concerned. The pool may not
// veto a control frame, but it must count it -- otherwise every worker
// connection's reserve would be invisible to the bound.
func TestWriterControlReserveIsChargedToThePool(t *testing.T) {
	t.Parallel()

	const reserve = 64 << 10
	pool := NewPool(PoolConfig{Capacity: 4 << 20})
	w, _ := pooledWriter(t, pool, reserve)

	require.True(t, w.TryEnqueueControl(testFrameOfSize(1, 8192)))
	assert.Equal(t, int64(8192), pool.Used())
	assert.Equal(t, int64(8192), w.member.charged.Load())
}

// TestPoolChargeReservedCountsAnOvercommit pins that control frames placed past
// a full pool are reported. They are the one admission the pool cannot refuse,
// so they are also the one that can push occupancy past capacity without any
// other signal that it happened.
func TestPoolChargeReservedCountsAnOvercommit(t *testing.T) {
	t.Parallel()

	const capacity = 1 << 20
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: 4096, MaxFloor: 4096})
	m := pool.Attach(func(error) bool { return true })
	require.Equal(t, Admitted, m.charge(capacity, 0))
	require.Zero(t, pool.Overcommits())

	m.chargeReserved(4096)
	assert.Equal(t, int64(capacity)+4096, pool.Used())
	assert.Equal(t, int64(1), pool.Overcommits(),
		"a control frame placed past capacity is exactly what this counter exists to surface")
}

// A reserve is a FLOOR under control traffic, not a ceiling on it.
//
// TryEnqueueControl spends the private reserve first -- those bytes are never
// subject to the pool's threshold, so a pool full of somebody else's backlog
// can never fence this connection -- and then competes for ordinary budget like
// any other frame. Checking the reserve ALONE narrowed the control ceiling from
// the whole budget to 256 KiB, and a refusal is not a soft failure: workermgr's
// SendControl fences the worker, discarding every user's channels on it.
func TestWriterControlSpillsPastTheReserveIntoTheBudget(t *testing.T) {
	t.Parallel()

	const (
		frame    = 64 << 10
		reserve  = 4 * frame
		capacity = 64 << 20
	)
	pool := NewPool(PoolConfig{Capacity: capacity})
	var written atomic.Int64
	w := NewUnstarted(t.Context(), Config[int]{
		Write:          func(context.Context, int) error { written.Add(1); return nil },
		Size:           func(int) int { return frame },
		Pool:           pool,
		ControlReserve: reserve,
	})
	t.Cleanup(w.Close)

	// Fill the private reserve exactly.
	for i := range 4 {
		require.True(t, w.TryEnqueueControl(i), "control frame %d must fit the reserve", i)
	}
	require.Equal(t, int64(reserve), w.controlBytes, "the reserve is now spent")

	// The next control frame has no reserve left, but the writer's data queue is
	// empty and the pool has 64 MiB free. Refusing here is what used to fence a
	// healthy worker on an absorbable burst.
	require.True(t, w.TryEnqueueControl(99),
		"control must fall back to the shared budget once its own reserve is spent")
	assert.Equal(t, int64(reserve), w.controlBytes,
		"and the spill is charged as data, so the reserve ledger does not go past its bound")
	assert.Equal(t, int64(5*frame), w.QueuedBytes())

	// Every frame still refunds exactly once, whichever ledger admitted it.
	require.NoError(t, w.Drain())
	assert.Equal(t, int64(5), written.Load())
	assert.Zero(t, w.QueuedBytes())
	assert.Zero(t, w.controlBytes)
	w.Close()
	assert.Zero(t, pool.Used(), "the spilled frame must return to the pool, not the reserve")
}

// ...and the guarantee the reserve exists for still holds: when the POOL is the
// thing that is full, the reserve is untouchable and control still gets through.
func TestWriterControlReserveSurvivesAFullPoolAfterTheSpillPath(t *testing.T) {
	t.Parallel()

	const (
		frame    = 64 << 10
		reserve  = 4 * frame
		capacity = 2 << 20
		// Strictly ABOVE the reserve, which the config now requires: a floor
		// equal to the reserve leaves a writer sitting at its guarantee with no
		// room for data at all, so every data charge is refused however idle the
		// pool is. Production has the same shape -- a 1 MiB default floor
		// against a 256 KiB default reserve.
		floor = reserve + frame
	)
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

	// Saturate with SEVERAL hogs, not one: under the dynamic rule a lone member
	// settles at about half the capacity, which leaves the pool with room and
	// would not exercise the reserve at all.
	hogs := make([]*PoolMember, 8)
	held := make([]int64, len(hogs))
	for i := range hogs {
		hogs[i] = pool.Attach(func(error) bool { return true })
	}
	for progressed := true; progressed; {
		progressed = false
		for i, h := range hogs {
			if h.charge(frame, 0) == Admitted {
				held[i] += frame
				progressed = true
			}
		}
	}
	t.Cleanup(func() {
		for i, h := range hogs {
			h.Release(held[i])
			h.Detach()
		}
	})
	require.LessOrEqual(t, capacity-pool.Used(), int64(floor),
		"precondition: the dynamic threshold must have collapsed to the floor")

	w := NewUnstarted(t.Context(), Config[int]{
		Write:          func(context.Context, int) error { return nil },
		Size:           func(int) int { return frame },
		Pool:           pool,
		ControlReserve: reserve,
	})
	t.Cleanup(w.Close)

	// The floor guarantees this writer a working set above its reserve, so it
	// may place data up to it -- that IS the guarantee, not a leak. Consume it
	// first; only then is the data path genuinely exhausted.
	require.True(t, w.TryEnqueue(1),
		"the guaranteed working set must admit data even on a pool somebody else filled")
	require.False(t, w.TryEnqueue(2), "precondition: the shared budget is exhausted")
	// Control is not, because the reserve is private and the pool has no veto.
	assert.True(t, w.TryEnqueueControl(3),
		"a pool filled by another connection must never be able to fence this one")
}

// A panicking transport is ONE connection's problem. The handler-drained path
// has always recovered (workermgr.SendPump.drain); the goroutine-drained path
// used to unwind out of run() and take the whole process -- every other
// connection and user with it. Two drain modes, opposite blast radii, identical
// fault.
func TestWriterGoroutineDrainSurvivesAPanickingTransport(t *testing.T) {
	t.Parallel()

	pool := NewPool(PoolConfig{Capacity: 1 << 20})
	gaveUp := make(chan GiveUpReason, 1)
	w := New(t.Context(), Config[int]{
		Write: func(context.Context, int) error { panic("transport exploded") },
		Size:  func(int) int { return 64 },
		Pool:  pool,
		OnGiveUp: func(reason GiveUpReason, _ error) {
			select {
			case gaveUp <- reason:
			default:
			}
		},
	})

	require.NoError(t, w.Enqueue(1))

	select {
	case reason := <-gaveUp:
		assert.Equal(t, GiveUpWriteError, reason,
			"a panicking write must be reported as a write failure, not left silent")
	case <-time.After(5 * time.Second):
		t.Fatal("the owner was never told the connection died")
	}

	// And the connection is genuinely torn down rather than limping on, with its
	// budget returned to the pool.
	assert.Eventually(t, func() bool { return pool.Members() == 0 },
		5*time.Second, 10*time.Millisecond, "the writer must leave the pool")
	assert.Zero(t, pool.Used(), "and strand none of its bytes")
	assert.False(t, w.TryEnqueue(2), "a given-up writer must not accept more work")
}

// The control reserve is a floor under control traffic, not a ceiling on it:
// once it is spent, control competes for ordinary budget like anything else.
//
// The spill used to pass ControlReserve as charge's reserve argument, which
// subtracts it a SECOND time -- the bytes already spent from the reserve are in
// the member's holding, because chargeReserved put them there. On a crowded pool,
// where the threshold has collapsed to the floor, that refused a control frame
// with a quarter of the floor still unused. A refusal there fences the worker,
// which discards every user's channels on that machine.
func TestWriterControlSpillIsNotChargedTheReserveTwice(t *testing.T) {
	t.Parallel()

	const (
		unit     = 64 << 10
		reserve  = 4 * unit  // 256 KiB
		floor    = 12 * unit // 768 KiB -- three reserves, so the two rules differ
		capacity = 8 << 20
		// Fits under the correct rule (held+size <= floor) and not under the
		// doubled one (held+size <= floor-reserve).
		spill = 6 * unit // 384 KiB
	)
	pool := NewPool(PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})

	// Crowd the pool so this writer's threshold really is the floor; on an empty
	// pool the dynamic branch grants so much that neither rule binds.
	var ballast []*SharedMember
	t.Cleanup(func() {
		for _, m := range ballast {
			held := m.Charged()
			m.Release(held, held)
			m.Detach()
		}
	})
	for pool.Used() < capacity {
		m := pool.AttachShared(func(error) bool { return true })
		ballast = append(ballast, m)
		if m.Admit(1<<20, 1<<20) != Admitted {
			break
		}
	}
	require.GreaterOrEqual(t, pool.Used(), int64(capacity),
		"precondition: the dynamic threshold must have collapsed to the floor")

	w := NewUnstarted(t.Context(), Config[int]{
		Write:          func(context.Context, int) error { return nil },
		Size:           func(n int) int { return n },
		Pool:           pool,
		ControlReserve: reserve,
	})
	t.Cleanup(w.Close)

	// Spend the private reserve exactly.
	require.True(t, w.TryEnqueueControl(reserve), "the reserve must take a frame its own size")
	require.Equal(t, int64(reserve), w.controlBytes, "precondition: the reserve is spent")

	// Now the spill. The writer holds `reserve`; the floor guarantees it `floor`,
	// so `reserve + spill = 640 KiB` fits inside 768 KiB. Subtracting the reserve
	// again would compare it against 512 KiB and refuse.
	assert.True(t, w.TryEnqueueControl(spill),
		"control that fits the guaranteed working set must not be refused: a false here fences the worker")
	assert.Equal(t, int64(reserve), w.controlBytes,
		"and the spill is charged as data, so the reserve ledger does not grow past its bound")
}
