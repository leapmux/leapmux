package crdt_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The published-generation immutability invariant (see Manager.commitState).
//
// Everything that reads state without holding m.mu for the whole read depends
// on it: materializedFromState builds a cold subscriber's whole baseline from a
// captured generation holding NO lock, and manager_audit.go asserts the same
// property in prose. These tests are the executable half of that claim, and the
// analogue of state_clone_for_batch_test.go for the manager-level paths.

// captureGeneration returns the live generation pointer.
//
// Escaping the pointer out of WithStateRLock is deliberate and is precisely
// what is under test: if generations are immutable, an escaped pointer is a
// stable point-in-time value.
func captureGeneration(mgr *crdt.Manager) *leapmuxv1.UserCrdtState {
	var out *leapmuxv1.UserCrdtState
	mgr.WithStateRLock(func(state *leapmuxv1.UserCrdtState) { out = state })
	return out
}

// TestCommitState_PublishedGenerationIsNeverMutated runs every writer the
// manager has -- a commit, a housekeeping pass (compaction + epoch), and a test
// seed -- against a generation captured beforehand, and asserts that generation
// is byte-identical afterwards.
//
// The compaction arm is the one that used to fail: PruneTombstonesAtOrBelow
// DELETES map entries, and CloneStateForBatch shares the entity map of any kind
// a batch does not touch, so an in-place prune reached back into every older
// generation that still aliased that map.
func TestCommitState_PublishedGenerationIsNeverMutated(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	// A tombstoned tab, so the housekeeping pass below has something to prune.
	addAndTombstoneTab(t, mgr, "tA")

	gen := captureGeneration(mgr)
	require.NotNil(t, gen)
	before := proto.Clone(gen).(*leapmuxv1.UserCrdtState)
	require.NotNil(t, before.GetTabs()["tA"], "fixture: the tombstoned tab must still be present pre-prune")

	submitNodePositionBatch(t, mgr, "after-capture", "root1", "z0")
	mgr.TickHousekeeping(context.Background())
	mgr.SeedStateForTest(func(state *leapmuxv1.UserCrdtState) {
		state.Nodes["seeded"] = &leapmuxv1.NodeRecord{NodeId: "seeded"}
	})

	assert.True(t, proto.Equal(before, gen),
		"a published generation must be immutable; it changed under a commit, a compaction and a seed")
	// And the invariant has to be load-bearing, not vacuous: the manager really
	// did move on from that generation.
	live := captureGeneration(mgr)
	assert.NotSame(t, gen, live, "the manager should have published new generations")
	assert.NotNil(t, gen.GetTabs()["tA"],
		"the captured generation must still hold the record compaction pruned from the live one")
	assert.NotContains(t, live.GetTabs(), "tA",
		"fixture: compaction must actually have pruned it from the live generation")
}

// TestMaybeCompact_PublishesANewGeneration pins that compaction swaps rather
// than edits, and that the prune lands on the new generation only.
func TestMaybeCompact_PublishesANewGeneration(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	addAndTombstoneTab(t, mgr, "tA")

	before := captureGeneration(mgr)
	require.NotNil(t, before.GetTabs()["tA"])

	mgr.TickHousekeeping(context.Background())

	after := captureGeneration(mgr)
	assert.NotSame(t, before, after, "compaction must publish a new generation")
	assert.NotNil(t, before.GetTabs()["tA"], "the prune must not reach the old generation")
	assert.NotContains(t, after.GetTabs(), "tA", "the prune must land on the published generation")
	assert.False(t, crdt.HLCIsZero(after.GetCompactionWatermark()),
		"the published generation carries the new watermark")
	// Compaction publishes a generation that owns its entity MAPS outright -- it
	// does not alias the ones it superseded. That is the property the in-place
	// prune violated: aliased maps are what let a delete reach backwards into a
	// generation a reader was still walking.
	//
	// It is also sameNodeMap's negative case. Its only other use asserts the
	// helper returns TRUE, which two nil maps would satisfy just as well; this
	// pins that it can distinguish, so the aliasing assertion elsewhere is not
	// reading a constant.
	assert.False(t, sameNodeMap(before, after),
		"the published generation must own its entity maps, not the old one's")
	// ...while SHARING the records, which is the whole point of copying the maps
	// instead of deep-cloning the account. A prune adds and removes entries and
	// never writes a record field, so a full CloneState re-allocated every node,
	// tab, window and workspace through protobuf reflection on the manager
	// goroutine -- once per tick on any active account -- to delete a handful.
	survivor := before.GetNodes()["root1"]
	require.NotNil(t, survivor, "fixture: the root node must survive the prune")
	assert.Same(t, survivor, after.GetNodes()["root1"],
		"an untouched record must be shared with the superseded generation, not re-cloned")
}

// TestMaybeAdvanceEpoch_PublishesANewGenerationSharingEntityMaps pins both
// halves of nextStateGeneration: it publishes (so the old generation keeps its
// epoch) and it is O(1) (so the entity maps are aliased, not copied).
func TestMaybeAdvanceEpoch_PublishesANewGenerationSharingEntityMaps(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	// Drain compaction FIRST, so the tick under test does the epoch bump and
	// nothing else. maybeCompact's pre-check early-returns while max_hlc has
	// not moved past compaction_watermark, and neither the seed below nor the
	// epoch bump moves max_hlc -- so the only generation the tick publishes is
	// nextStateGeneration's, which is what this test is about. (Without this,
	// maybeCompact's full CloneState publishes right after and the assertion
	// would be reading its fresh maps.)
	mgr.TickHousekeeping(context.Background())

	before := captureGeneration(mgr)
	beforeEpoch := before.GetCurrentEpoch()
	// Age the epoch past EpochDuration so the next pass advances it. The
	// manager's injected clock only moves 1ms per call, so move the START back
	// rather than waiting.
	mgr.SeedStateForTest(func(state *leapmuxv1.UserCrdtState) {
		state.EpochStartedAt = timestamppb.New(time.UnixMilli(0).Add(-2 * crdt.EpochDuration))
	})
	seeded := captureGeneration(mgr)

	mgr.TickHousekeeping(context.Background())

	after := captureGeneration(mgr)
	require.Equal(t, beforeEpoch+1, after.GetCurrentEpoch(), "the epoch should have advanced")
	assert.Equal(t, beforeEpoch, before.GetCurrentEpoch(), "the old generation keeps its epoch")
	// The epoch bump touches no record, so the entity maps are shared with the
	// generation it superseded -- that sharing is what makes it O(1), and it is
	// also exactly why an in-place delete on one generation would corrupt
	// another (see commitState).
	assert.True(t, sameNodeMap(seeded, after), "nextStateGeneration must alias the entity maps, not copy them")
}

// TestRunOnManagerGoroutine_ReturnsAfterStopInsteadOfWedging pins the escape
// hatch shared by every caller-driven pass.
//
// Both SeedStateForTest and TickHousekeeping hand their work to the manager
// goroutine over an UNBUFFERED channel and then wait for it. Once Stop has run
// that goroutine is gone, so without the `<-m.done` arm the send has no
// receiver and the caller blocks forever -- a deadlock, not a failed call, and
// one that takes the whole test binary with it. The two used to spell this
// handshake out separately; they now share one, so a regression here wedges
// both at once and this is the only test that would say so.
//
// A post-Stop call is a no-op by design: it cannot run the pass, but it must
// return.
func TestRunOnManagerGoroutine_ReturnsAfterStopInsteadOfWedging(t *testing.T) {
	mgr, _, cancel := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	cancel()
	mgr.Stop()

	// Asserted with a deadline rather than by simply calling them: the failure
	// mode is a hang, and an ordinary call would take the package down with it
	// instead of reporting.
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		mgr.TickHousekeeping(context.Background())
		mgr.SeedStateForTest(func(state *leapmuxv1.UserCrdtState) {
			state.Nodes["after-stop"] = &leapmuxv1.NodeRecord{NodeId: "after-stop"}
		})
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("a caller-driven pass wedged after Stop; the manager goroutine is gone and nothing drains the channel")
	}

	// ...and it really was a no-op: the seed had no sole writer to run on, so
	// nothing was published. Silently dropping the work is the deliberate
	// trade -- a post-Stop write could not be ordered against anything.
	assert.NotContains(t, captureGeneration(mgr).GetNodes(), "after-stop",
		"a post-Stop seed must not publish; there is no goroutine to serialize it against")
}

// TestSeedStateForTest_PublishesANewGeneration covers the started branch; the
// pre-Start branch is exercised by every runManager call, which seeds through
// Bootstrap before Start.
func TestSeedStateForTest_PublishesANewGeneration(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")

	before := captureGeneration(mgr)
	mgr.SeedStateForTest(func(state *leapmuxv1.UserCrdtState) {
		state.Nodes["seeded"] = &leapmuxv1.NodeRecord{NodeId: "seeded"}
	})
	after := captureGeneration(mgr)

	assert.NotSame(t, before, after, "a seed must publish a new generation")
	assert.Nil(t, before.GetNodes()["seeded"], "the seed must not reach back into the old generation")
	assert.NotNil(t, after.GetNodes()["seeded"], "the seed must land on the published generation")
}

// addAndTombstoneTab commits a tab under root1 and then tombstones it in a
// SEPARATE batch (so the tombstone carries a later canonical HLC), leaving a
// record that the next compaction pass will prune.
func addAndTombstoneTab(t *testing.T, mgr *crdt.Manager, tabID string) {
	t.Helper()
	require.NoError(t, submitTombstonedTab(t, t.Context(), mgr, tabID))
}

// submitTombstonedTab is addAndTombstoneTab's assertion-free core, returning the
// error instead of asserting it.
//
// It exists so a SPAWNED goroutine can drive the same seed: require's FailNow is
// runtime.Goexit, which is only defined on the test goroutine, so calling it off
// one silently skips the rest of that goroutine instead of failing the test.
// (t.Helper() is safe anywhere; it is only the assertion that is not.)
func submitTombstonedTab(t *testing.T, ctx context.Context, mgr *crdt.Manager, tabID string) error {
	t.Helper()
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	if _, err := mgr.Submit(ctx, crdt.SubmitInput{
		Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{addTabBatch(t, "add-"+tabID, tabID, "root1", "wkr", "p1")},
	}); err != nil {
		return err
	}
	_, err := mgr.Submit(ctx, crdt.SubmitInput{
		Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{{
			BatchId: "tomb-" + tabID,
			Ops: []*leapmuxv1.CrdtOp{{
				OpId: "op-tomb-" + tabID,
				Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{
					TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
					TabId:   tabID,
				}},
			}},
		}},
	})
	return err
}

// sameNodeMap reports whether two generations share one Nodes map object.
//
// Go cannot compare maps with ==, so this compares the identity of the map
// headers they point at. The obvious alternative -- write a probe key through
// one and look for it in the other -- is NOT usable here: at least one argument
// is always the manager's LIVE generation, so the probe would be an
// unsynchronized write to a map the manager goroutine reads (housekeeping, a
// queued submit, a concurrent baseline walk). That is a data race by
// construction, and Go's map detector can turn it into a hard "concurrent map
// read and map write" panic -- an intermittent failure in the very suite that
// exists to prove generations are safe to share.
func sameNodeMap(a, b *leapmuxv1.UserCrdtState) bool {
	return reflect.ValueOf(a.GetNodes()).Pointer() == reflect.ValueOf(b.GetNodes()).Pointer()
}
