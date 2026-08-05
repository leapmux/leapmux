package crdt_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// The FALLBACK arm's lock plan: an O(all-entities) baseline built with NO
// manager lock held, over a captured immutable generation, with
// resumeSuppressThrough closing the dual-delivery straddle in place of the
// projection hold that used to.
//
// The sibling suite for the RESUME arm is in manager_subscribe_test.go; this
// file is the FALLBACK half, including the analogue of
// TestSubscribeWithACL_NeitherExpandNorBroadcastStalledDuringScan.

// heldBaseline is the FALLBACK counterpart of the fake journal's
// listHold/listReached pair. The resume arm can block inside ListBatchesAfter;
// the fallback makes no journal call, so the seam is the builder itself.
//
// Only the `nth` baseline is held, so a test can register warm-up subscribers
// freely and then block exactly the connect under test.
type heldBaseline struct {
	reached chan struct{}
	release chan struct{}

	mu        sync.Mutex
	calls     int
	unblocked bool
}

// runHeldBaselineManager builds a manager whose Nth FALLBACK baseline blocks
// until released, and registers the release so it runs BEFORE the manager's
// Stop.
//
// The release itself is what keeps a failing assertion from hanging the
// package: a t.Fatal returns without unblocking the builder, leaving that
// goroutine parked forever, and Stop waits for the manager goroutine that is
// wedged behind it. But t.Cleanup is LIFO, so registering the release first --
// which every caller did, since it had to build the option before the manager
// -- ran it LAST, after Stop, and the hang happened anyway. Doing both here, in
// this order, is what makes "a regression in the lock plan FAILS these tests
// rather than hanging them" a property of the helper instead of a rule each
// caller has to get right.
func runHeldBaselineManager(t *testing.T, nth int) (*heldBaseline, *crdt.Manager) {
	t.Helper()
	h, opt := newHeldBaseline(nth)
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000, opt)
	t.Cleanup(h.unblock)
	return h, mgr
}

// newHeldBaseline returns the hold and the ManagerOption that installs it.
// Callers go through runHeldBaselineManager, which owns the cleanup ordering.
func newHeldBaseline(nth int) (*heldBaseline, crdt.ManagerOption) {
	h := &heldBaseline{reached: make(chan struct{}), release: make(chan struct{})}
	opt := crdt.WithMaterializerForTest(func(next crdt.BaselineBuilder) crdt.BaselineBuilder {
		return func(state *leapmuxv1.UserCrdtState, filter crdt.SubscriberFilter) *leapmuxv1.UserMaterialized {
			h.mu.Lock()
			h.calls++
			hold := h.calls == nth
			h.mu.Unlock()
			if hold {
				close(h.reached)
				<-h.release
			}
			// Delegates, so what is under test is the LOCK PLAN around the
			// builder rather than a stand-in for it.
			return next(state, filter)
		}
	})
	return h, opt
}

// waitReached blocks until the held baseline has started, or fails the test.
func (h *heldBaseline) waitReached(t *testing.T) {
	t.Helper()
	select {
	case <-h.reached:
	case <-time.After(2 * time.Second):
		t.Fatal("the fallback baseline never started")
	}
}

// unblock is idempotent so a test can release explicitly AND leave the
// cleanup's release in place as the safety net.
func (h *heldBaseline) unblock() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unblocked {
		return
	}
	h.unblocked = true
	close(h.release)
}

// TestSubscribeWithACL_FallbackStallsNothingDuringBaseline is the whole point
// of the FALLBACK rework, stated as a test.
//
// While a cold connect's baseline is in flight, NONE of the things it used to
// block may be blocked: a workspace expand (subscribeExpandMu), a commit and
// its broadcast to another subscriber (m.mu.Lock, then m.projection), or a
// Materialized() read (m.mu.RLock).
//
// The commit arm is the one that fails outright before this change: the old
// shape held m.mu.RLock across the whole walk, and commitState needs
// m.mu.Lock.
func TestSubscribeWithACL_FallbackStallsNothingDuringBaseline(t *testing.T) {
	held, mgr := runHeldBaselineManager(t, 2)
	seedRootInternal(t, mgr, "w1", "root1")

	// Warm-up subscriber (baseline #1, not held), so a commit's broadcast has
	// somewhere to land while the connect under test is stuck.
	warm := &captureSubscriber{}
	warmOut, err := mgr.SubscribeWithACL(context.Background(), &crdt.Subscriber{UserID: "user-1", Send: warm.send}, nil, 0, resolveAll)
	require.NoError(t, err)
	defer warmOut.Unsub()()
	warmBefore := len(warm.snapshot())

	// The connect under test: baseline #2, held.
	//
	// The error is carried back to the test goroutine rather than asserted here:
	// require's FailNow is runtime.Goexit, which is only defined on the test
	// goroutine. Called here it would skip the Unsub below, leaking a registered
	// subscriber and its presence refcount into assertions (a)-(d).
	heldDone := make(chan struct{})
	heldErr := make(chan error, 1)
	go func() {
		defer close(heldDone)
		out, err := mgr.SubscribeWithACL(context.Background(),
			&crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}, nil, 0, resolveAll)
		heldErr <- err
		if err == nil {
			out.Unsub()()
		}
	}()
	held.waitReached(t)

	// (a) subscribeExpandMu: a workspace-create expand must not stall.
	expandDone := make(chan error, 1)
	go func() { expandDone <- mgr.ExpandSubscribersForWorkspace(context.Background(), "w1") }()
	select {
	case err := <-expandDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ExpandSubscribersForWorkspace stalled behind a fallback baseline — subscribeExpandMu held across it")
	}

	// (b) m.mu.Lock: the commit itself. This is the arm the old shape failed.
	committed := make(chan struct{})
	go func() {
		defer close(committed)
		submitNodePositionBatch(t, mgr, "during-baseline", "root1", "live")
	}()
	select {
	case <-committed:
	case <-time.After(2 * time.Second):
		t.Fatal("a commit stalled behind a fallback baseline — m.mu held across the walk")
	}

	// (c) m.projection: and its broadcast reached the warm subscriber.
	waitFor(t, func() bool { return len(warm.snapshot()) > warmBefore },
		"a broadcast stalled behind a fallback baseline — m.projection held across the walk")

	// (d) m.mu.RLock readers are not queued behind a waiting writer either.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_ = mgr.Materialized(crdt.SubscriberFilter{})
	}()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Materialized() stalled behind a fallback baseline")
	}

	held.unblock()
	<-heldDone
	require.NoError(t, <-heldErr, "the held connect itself must succeed")
}

// TestSubscribeWithACL_FallbackExcludesAndDeliversPostBaselineCommits is the
// no-gap / no-duplicate proof in behavioural form: a batch committed while the
// baseline is in flight is NOT in the snapshot, and IS delivered live, exactly
// once.
func TestSubscribeWithACL_FallbackExcludesAndDeliversPostBaselineCommits(t *testing.T) {
	held, mgr := runHeldBaselineManager(t, 1)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "pre-baseline", "root1", "before")

	cap := &captureSubscriber{}
	done := make(chan crdt.SubscribeOutcome, 1)
	go func() {
		out, err := mgr.SubscribeWithACL(context.Background(),
			&crdt.Subscriber{UserID: "user-1", Send: cap.send}, nil, 0, resolveAll)
		require.NoError(t, err)
		done <- out
	}()
	held.waitReached(t)

	// Commits strictly after the generation the baseline captured.
	submitNodePositionBatch(t, mgr, "post-baseline", "root1", "after")
	held.unblock()
	out := <-done
	defer out.Unsub()()

	initial := out.Initial()
	require.NotNil(t, initial)
	assert.Equal(t, "before", initial.GetNodes()["root1"].GetPosition().GetValue(),
		"the baseline must reflect the generation it captured, not a commit that landed during the walk")
	waitFor(t, func() bool { return countBatchFrames(cap) == 1 },
		"the post-baseline commit must be delivered live exactly once")
	assert.Equal(t, 1, countBatchFrames(cap),
		"and not twice -- it is above the suppress gate, so exactly one copy reaches the client")
}

// TestSubscribeWithACL_FallbackSuppressesPreBaselineBroadcasts is the other
// direction: a batch that committed BEFORE the captured generation is already
// folded into the baseline, so re-delivering it would replay an
// entity_materialized / entity_removed the client applies wholesale onto newer
// state. The suppress gate must drop it.
func TestSubscribeWithACL_FallbackSuppressesPreBaselineBroadcasts(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "pre-baseline", "root1", "before")

	cap := &captureSubscriber{}
	out, err := mgr.SubscribeWithACL(context.Background(),
		&crdt.Subscriber{UserID: "user-1", Send: cap.send}, nil, 0, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	require.Equal(t, "before", out.Initial().GetNodes()["root1"].GetPosition().GetValue())

	// Nothing has been broadcast to this subscriber: everything it can see is
	// in the baseline it just received.
	assert.Equal(t, 0, countBatchFrames(cap))

	// A commit after registration is above the gate and IS delivered.
	submitNodePositionBatch(t, mgr, "after", "root1", "later")
	waitFor(t, func() bool { return countBatchFrames(cap) == 1 },
		"a commit after the baseline must still be delivered")
}

// TestSubscribeWithACL_ColdFallbackDoesNotRebaseline pins the enum: a first
// connect was never registered, so there is nothing parked and firing the
// discard would be meaningless. A transposed fallbackCold/fallbackRebaseline
// would still pass every other test in this file.
func TestSubscribeWithACL_ColdFallbackDoesNotRebaseline(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")

	rebaselines := 0
	out, err := mgr.SubscribeWithACL(context.Background(), &crdt.Subscriber{
		UserID:       "user-1",
		Send:         (&captureSubscriber{}).send,
		OnRebaseline: func() { rebaselines++ },
	}, nil, 0, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	assert.Equal(t, 0, rebaselines, "a cold fallback has nothing parked to discard")
}

// TestSubscribeWithACL_ParkOverflowDuringBaselineRetriesThenSucceeds covers the
// hazard the lock-free baseline introduces: the subscriber is registered while
// the walk runs, so live frames park and can overflow the transport's buffer.
//
// The first attempt reports an overflow; the ladder must rebaseline (which
// discards the buffer and clears the flag) and return a complete snapshot
// rather than shipping one alongside a hole.
func TestSubscribeWithACL_ParkOverflowDuringBaselineRetriesThenSucceeds(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")

	var (
		mu          sync.Mutex
		overflowed  = true
		rebaselines int
	)
	sub := &crdt.Subscriber{
		UserID: "user-1",
		Send:   (&captureSubscriber{}).send,
		Overflowed: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return overflowed
		},
		OnRebaseline: func() {
			mu.Lock()
			defer mu.Unlock()
			rebaselines++
			// The real queue clears the flag here (see
			// subscriberQueue.Rebaseline); a fake that did not would be
			// asserting against a transport that does not exist.
			overflowed = false
		},
	}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, nil, 0, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()

	require.Equal(t, crdt.SubscribeInitial, out.Mode())
	assert.Equal(t, crdt.SubscribeReasonParkOverflow, out.Reason(),
		"the overflow is the operative cause of this snapshot, not whatever sent us here first")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, rebaselines, "one retry should have been enough")
	assert.NotNil(t, out.Initial().GetNodes()["root1"], "the retried baseline must still be complete")
}

// TestSubscribeWithACL_ParkOverflowLadderTerminates is the liveness half: a
// transport that reports an overflow forever must not spin. The ladder is
// bounded, and its last rung holds m.projection -- where no broadcast can
// interleave, so no overflow is possible.
func TestSubscribeWithACL_ParkOverflowLadderTerminates(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")

	var (
		mu          sync.Mutex
		rebaselines int
	)
	sub := &crdt.Subscriber{
		UserID:     "user-1",
		Send:       (&captureSubscriber{}).send,
		Overflowed: func() bool { return true },
		OnRebaseline: func() {
			mu.Lock()
			defer mu.Unlock()
			rebaselines++
		},
	}

	// The error travels back to the test goroutine rather than being asserted in
	// the spawned one: require's FailNow is runtime.Goexit, defined only on the
	// test goroutine. Called here it would skip the `done` send, so a plain
	// subscribe error would surface as the 5s "ladder did not terminate"
	// timeout below -- reporting a liveness bug for what is really an error
	// return, and hiding the actual cause.
	type ladderResult struct {
		out crdt.SubscribeOutcome
		err error
	}
	done := make(chan ladderResult, 1)
	go func() {
		out, err := mgr.SubscribeWithACL(context.Background(), sub, nil, 0, resolveAll)
		done <- ladderResult{out: out, err: err}
	}()
	select {
	case res := <-done:
		require.NoError(t, res.err)
		defer res.out.Unsub()()
		assert.Equal(t, crdt.SubscribeInitial, res.out.Mode())
		assert.NotNil(t, res.out.Initial().GetNodes()["root1"],
			"the locked last rung must still produce a complete baseline")
	case <-time.After(5 * time.Second):
		t.Fatal("the park-overflow ladder did not terminate")
	}
	mu.Lock()
	defer mu.Unlock()
	// The FIRST attempt enters cold (nothing parked, so no discard); each
	// subsequent lock-free attempt rebaselines, and so does the locked rung.
	// That is (attempts - 1) + 1 = attempts discards for a cold entry.
	assert.Equal(t, crdt.FallbackLockFreeAttemptsForTest, rebaselines,
		"the ladder must be bounded, not open-ended")
}

// TestSubscribeWithACL_LockedRungReleasesTheLifecycleLock pins the one lock the
// ladder's TERMINAL rung must not hold across its walk.
//
// That rung holds m.projection on purpose -- it is what makes a broadcast (and
// therefore an overflow) impossible while the baseline is taken, which is what
// makes the ladder finite. subscribeExpandMu is a different matter: it covers
// only the resolve->register TOCTOU, and the lifecycle RPCs it serializes take
// it WITHOUT ever taking m.projection. Holding it across an O(all-entities)
// walk therefore stalls this user's workspace create/delete for the duration
// and buys nothing -- and the escalated rung reached that walk under exactly
// the sustained load where a stalled lifecycle RPC hurts most.
//
// The lock-free rungs are covered by
// TestSubscribeWithACL_FallbackStallsNothingDuringBaseline; this is the arm
// that hand-rolled its own locking and so did not inherit that guarantee.
func TestSubscribeWithACL_LockedRungReleasesTheLifecycleLock(t *testing.T) {
	// Builds 1..N are the lock-free attempts; the NEXT one is the escalated
	// rung, which is the only build under test here.
	held, mgr := runHeldBaselineManager(t, crdt.FallbackLockFreeAttemptsForTest+1)
	seedRootInternal(t, mgr, "w1", "root1")

	// Permanently overflowed, so every lock-free attempt gives up and the
	// ladder runs all the way to the locked rung.
	sub := &crdt.Subscriber{
		UserID:       "user-1",
		Send:         (&captureSubscriber{}).send,
		Overflowed:   func() bool { return true },
		OnRebaseline: func() {},
	}
	connectErr := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		out, err := mgr.SubscribeWithACL(context.Background(), sub, nil, 0, resolveAll)
		connectErr <- err
		if err == nil {
			out.Unsub()()
		}
	}()
	held.waitReached(t)

	expandDone := make(chan error, 1)
	go func() { expandDone <- mgr.ExpandSubscribersForWorkspace(context.Background(), "w1") }()
	select {
	case err := <-expandDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ExpandSubscribersForWorkspace stalled behind the LOCKED rung's baseline — subscribeExpandMu held across the walk")
	}

	held.unblock()
	<-done
	require.NoError(t, <-connectErr, "the escalated connect itself must still succeed")
}

// TestSubscribeWithACL_FallbackReasonsAreDistinguishable pins that each
// non-resumable verdict reports its OWN reason. Without it the metric would be
// a single undifferentiated "initial" bucket, which is what it was before --
// and "why is this deployment full-snapshotting?" would stay unanswerable.
func TestSubscribeWithACL_FallbackReasonsAreDistinguishable(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "b1", "root1", "p1")
	snap := mgr.Materialized(crdt.SubscriberFilter{})
	cursor, epoch := snap.GetMaxHlc(), snap.GetCurrentEpoch()

	t.Run("no cursor", func(t *testing.T) {
		assert.Equal(t, crdt.SubscribeReasonNoCursor, subscribeOnce(t, mgr, nil, 0).Reason())
	})
	t.Run("stale epoch", func(t *testing.T) {
		assert.Equal(t, crdt.SubscribeReasonStaleEpoch,
			subscribeOnce(t, mgr, crdt.HLCClone(cursor), epoch+1).Reason())
	})
	t.Run("below the retention floor", func(t *testing.T) {
		// Needs its OWN manager with a zero retention TTL. On the shared one
		// the wall-clock floor is (now - 24h), which is negative for a fixture
		// clock seeded at 230s and therefore clamps to zero -- so every
		// non-zero cursor resumes and this case could never fire.
		zeroTTL, _, _ := runManager(t, "user-1", allowAll{}, 230_000, crdt.WithOpRetentionTTL(0))
		seedRootInternal(t, zeroTTL, "w1", "root1")
		submitNodePositionBatch(t, zeroTTL, "b1", "root1", "p1")
		snap := zeroTTL.Materialized(crdt.SubscriberFilter{})
		assert.Equal(t, crdt.SubscribeReasonBelowRetentionFloor,
			subscribeOnce(t, zeroTTL, &leapmuxv1.HLC{Physical: 1}, snap.GetCurrentEpoch()).Reason())
	})
	t.Run("resumed", func(t *testing.T) {
		submitNodePositionBatch(t, mgr, "tail", "root1", "p2")
		out := subscribeOnce(t, mgr, crdt.HLCClone(cursor), epoch)
		require.Equal(t, crdt.SubscribeDelta, out.Mode())
		assert.Equal(t, crdt.SubscribeReasonResumed, out.Reason())
	})
}

func subscribeOnce(t *testing.T, mgr *crdt.Manager, cursor *leapmuxv1.HLC, epoch int64) crdt.SubscribeOutcome {
	t.Helper()
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, cursor, epoch, resolveAll)
	require.NoError(t, err)
	t.Cleanup(out.Unsub())
	return out
}

// countBatchFrames counts the `batch` frames a capture subscriber received.
func countBatchFrames(c *captureSubscriber) int {
	n := 0
	for _, evt := range c.snapshot() {
		if evt.GetBatch() != nil {
			n++
		}
	}
	return n
}

// waitFor polls `cond` until it holds, failing with `msg` after two seconds.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

// TestFallbackBaselineRacesCompaction is the test that PROVES the
// copy-on-write change in A1 was necessary, and it only means anything under
// -race.
//
// Cold connects build their baselines from captured generations holding no
// lock, while housekeeping compacts. Before published generations were made
// immutable, compaction's PruneTombstonesAtOrBelow deleted entries from
// m.state's maps IN PLACE -- and CloneStateForBatch shares the entity map of
// any kind a batch does not touch, so that delete reached back into every
// older generation a baseline might still be walking. The detector fires on
// `delete(state.GetNodes(), id)` versus the walk's range.
func TestFallbackBaselineRacesCompaction(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// A FRESH tombstone every pass, not a fixed seed set.
	//
	// Seeding four tombstones once and then churning position ops gave the
	// detector a single burst of deletes to catch: PruneTombstonesAtOrBelow
	// removes all four on the first compaction, and SetNodeRegister ops create
	// no more, so every later pass pruned zero entries and issued no `delete` at
	// all. The window this test exists to hit was one early moment rather than
	// the whole run. Tombstoning a new tab each iteration keeps a delete racing
	// the baseline walks for the full 200ms.
	//
	// Tombstoning also advances max_hlc, which is what keeps maybeCompact's
	// pre-check from short-circuiting.
	churnErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := submitTombstonedTab(t, context.Background(), mgr, "churn-"+strconv.Itoa(i)); err != nil {
				select {
				case churnErr <- err:
				default:
				}
				return
			}
			mgr.TickHousekeeping(context.Background())
		}
	}()

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
				out, err := mgr.SubscribeWithACL(context.Background(), sub, nil, 0, resolveAll)
				if err != nil {
					continue
				}
				out.Unsub()()
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
	select {
	case err := <-churnErr:
		// Without this the churn goroutine could die on its first submit and the
		// test would still pass, having raced nothing at all.
		t.Fatalf("the compaction churn stopped early, so nothing was raced: %v", err)
	default:
	}
}
