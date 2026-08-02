package crdt_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"google.golang.org/protobuf/proto"
)

// submitNodeBatch commits a SetNodeRegister (parent_id + kind) for `nodeID`
// under `parentID` via the internal submit path (manager goroutine stays the
// sole writer). The parent_id is what makes workspaceForEntity resolve the
// node to a registered root's workspace, so the resume filter keeps the op.
// submitNodePositionBatch commits a SetNodeRegister op writing the position
// register (mutable) on `nodeID` via the internal submit path. Because
// `nodeID` resolves to a registered root's workspace via its parent chain,
// the op survives the resume visibility filter (workspaceForEntity resolves it
// to an allowed workspace). Used to advance the journal so a resume has a
// post-cursor tail that is NOT filtered out.
func submitNodePositionBatch(t *testing.T, mgr *crdt.Manager, batchID, nodeID, position string) {
	t.Helper()
	res, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{{BatchId: batchID, Ops: []*leapmuxv1.CrdtOp{{
			OpId: "op-" + batchID,
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: nodeID,
				Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: position},
			}},
		}}}},
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.NotNil(t, res[0].GetCommitted(), "position batch should commit; got %v", res[0])
}

// resolveAll is the resolve callback that admits every workspace (mirrors the
// production all-workspaces subscription, where workspace_ids is empty).
func resolveAll() (map[string]bool, error) { return map[string]bool{"w1": true}, nil }

// deltaBatchFrames extracts the OpBatch payloads from a ResumeDelta's frame
// stream, skipping materialized/removed frames. Resume tests that assert on the
// stable-visibility op tail use this to mirror the old `delta.GetBatches()`
// shape now that the delta is an ordered frame stream (materialized → batch →
// removed) rather than a flat batch list.
func deltaBatchFrames(t *testing.T, delta *leapmuxv1.ResumeDelta) []*leapmuxv1.OpBatch {
	t.Helper()
	var out []*leapmuxv1.OpBatch
	for _, f := range delta.GetFrames() {
		if b := f.GetBatch(); b != nil {
			out = append(out, b)
		}
	}
	return out
}

// TestSubscribeWithACL_AboveWatermark_ReturnsDelta is the happy path: a cursor
// strictly newer than compaction_watermark (which is zero here -- no
// compaction has run) yields a ResumeDelta whose tail is exactly the
// post-cursor committed batches, filtered to the subscriber's allowed set.
func TestSubscribeWithACL_AboveWatermark_ReturnsDelta(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")

	// First position commit. The cursor we resume from is this batch's max HLC.
	submitNodePositionBatch(t, mgr, "batch-a", "root1", "a0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor, "max HLC must be set after commits")

	// Second position commit AFTER the cursor -- this is the delta tail.
	submitNodePositionBatch(t, mgr, "batch-b", "root1", "b0")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err)
	require.Equal(t, crdt.SubscribeDelta, out.Mode(), "cursor above watermark must RESUME (delta), not fall back")
	require.NotNil(t, out.Unsub(), "resume must return an unsubscribe callback")
	defer out.Unsub()()

	delta := out.Delta()
	assert.Equal(t, epoch, delta.GetCurrentEpoch(), "delta epoch must match live state")
	// delta.max_hlc is computed from the FILTERED tail's own ops (the highest
	// canonical HLC the client actually receives), NOT read from the live
	// state. Here the tail is exactly batch-b, so the delta's max equals
	// batch-b's HLC — which happens to also be the live max (no other commit
	// landed), but the contract is "the tail's max", so assert against the tail
	// batch directly.
	batchFrames := deltaBatchFrames(t, delta)
	require.Len(t, batchFrames, 1, "only the post-cursor batch should be in the delta tail")
	tailMax := batchFrames[0].GetOps()[len(batchFrames[0].GetOps())-1].GetCanonicalHlc()
	assert.Equal(t, tailMax.GetPhysical(), delta.GetMaxHlc().GetPhysical(),
		"delta max_hlc must be the filtered tail's own max op HLC, not a separate live-state read")

	// Exactly the post-cursor batch (batch-b) should be in the tail, not
	// batch-a (which is at/below the cursor).
	require.Len(t, batchFrames, 1, "only the post-cursor batch should be in the delta tail")
	assert.Equal(t, "batch-b", batchFrames[0].GetBatchId(),
		"the at-cursor batch must be excluded; resume is strictly-after")
}

// TestSubscribeWithACL_FilterStripsDisallowedWorkspace is the security-relevant
// edge case for the delta's visibility filter: an op whose target resolves to
// a workspace the subscriber is NOT allowed to see must be stripped from the
// delta tail, exactly as filterVisibleOps strips it from a live broadcast.
// A regression here would leak cross-workspace ops through the resume path.
func TestSubscribeWithACL_FilterStripsDisallowedWorkspace(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")

	// Cursor sits between the two post-cursor commits.
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	// One op under w1 (allowed), one under w2 (NOT allowed for this subscriber).
	submitNodePositionBatch(t, mgr, "w1-batch", "root1", "w1op")
	submitNodePositionBatch(t, mgr, "w2-batch", "root2", "w2op")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Subscriber may see ONLY w1.
	resolveOnlyW1 := func() (map[string]bool, error) { return map[string]bool{"w1": true}, nil }
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveOnlyW1)
	require.NoError(t, err)
	require.NotNil(t, out.Unsub())
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, out.Mode(), "cursor above watermark must RESUME")
	delta := out.Delta()

	// Only the w1 op survives the filter; the w2 op must be stripped.
	seen := map[string]bool{}
	for _, b := range deltaBatchFrames(t, delta) {
		seen[b.GetBatchId()] = true
	}
	assert.True(t, seen["w1-batch"], "the allowed-workspace op must be in the delta tail")
	assert.False(t, seen["w2-batch"], "the disallowed-workspace op must be stripped from the delta tail")
}

// TestSubscribeWithACL_AtOrBelowWatermark_FallsBack pins the FALLBACK rule: a
// cursor at or below op_retention_watermark cannot be honored (those journal
// rows are deleted), so SubscribeWithACL returns a full UserMaterialized
// snapshot in fellBack and a nil delta -- exactly as a full-snapshot connect would (the FALLBACK path).
//
// Op-batch deletion gates on op_retention_watermark (which lags
// compaction_watermark by OpRetentionTTL), not compaction_watermark: a cursor
// above the retention floor but below the tombstone-prune line still resumes,
// because the journal still holds those batches. This test pins the FALLBACK
// half of that rule by shrinking the retention TTL to zero (so the floor meets
// the compaction watermark) and asserting a cursor at the watermark FALLBACKs.
// The RESUME-across-the-decoupled-window half is pinned by
// TestSubscribeWithACL_CursorBetweenRetentionAndCompaction_Resumes.
func TestSubscribeWithACL_AtOrBelowWatermark_FallsBack(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000, crdt.WithOpRetentionTTL(0))
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "batch-a", "root1", "a0")

	maxHLC := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Advance the compaction watermark to the current max HLC, as maybeCompact
	// would on a housekeeping tick (simulating one compaction having run).
	// TickHousekeeping is the test hook that drives maybeCompact on the manager
	// goroutine.
	mgr.TickHousekeeping(context.Background())
	wm := mgr.State().GetCompactionWatermark()
	require.False(t, crdt.HLCIsZero(wm), "compaction tick must advance the watermark")

	// A cursor equal to the watermark (== maxHLC at compaction time) must
	// FALLBACK: the post-cursor tail is gone.
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(maxHLC), epoch, resolveAll)
	require.NoError(t, err)
	require.NotNil(t, out.Unsub())
	defer out.Unsub()()

	require.Equal(t, crdt.SubscribeInitial, out.Mode(), "cursor at/below watermark must FALLBACK")
	fellBack := out.Initial()
	assert.Contains(t, fellBack.GetWorkspaces(), "w1", "fallback snapshot must contain the live workspace")
}

// TestSubscribeWithACL_CursorBetweenRetentionAndCompaction_Resumes pins the
// decoupling that op_retention_watermark introduces: a cursor that sits ABOVE
// the op_retention_watermark but AT OR BELOW the compaction_watermark must
// still RESUME, because the journal still holds every post-cursor batch (op
// batches are deleted only below op_retention_watermark, while tombstones are
// pruned at the tighter compaction_watermark). This is the whole point of the
// decoupling — multi-hour refreshes resume instead of full-snapshotting.
//
// The window between the two watermarks is created by submitting a batch,
// capturing the cursor, then submitting another batch and compacting: after
// compaction, compaction_watermark == max_hlc (>= cursor) but with a nonzero
// OpRetentionTTL the op_retention_watermark stays below the cursor, so the
// resume gate (which keys on op_retention_watermark) admits it. The default
// 24h TTL naturally produces this layout against the test's ~230s fake clock.
func TestSubscribeWithACL_CursorBetweenRetentionAndCompaction_Resumes(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000) // default OpRetentionTTL=24h
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Submit a tail batch so there is something to replay, then compact.
	submitNodePositionBatch(t, mgr, "tail-batch", "root1", "t0")
	mgr.TickHousekeeping(context.Background())

	cw := mgr.State().GetCompactionWatermark()
	rw := mgr.State().GetOpRetentionWatermark()
	require.False(t, crdt.HLCIsZero(cw), "compaction must advance compaction_watermark")
	// The default 24h TTL against a ~230s fake clock clamps the retention
	// floor to zero (max_hlc.physical - 24h < 0), so the cursor is well above
	// it — exercising the decoupled-window resume path.
	require.True(t, crdt.HLCCmp(cursor, rw) > 0, "cursor must be above op_retention_watermark")
	require.True(t, crdt.HLCCmp(cursor, cw) <= 0, "cursor must be at or below compaction_watermark (the old FALLBACK line)")

	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, out.Mode(),
		"cursor above op_retention_watermark must RESUME even when at/below compaction_watermark")
	delta := out.Delta()
	require.NotNil(t, delta)

	// The tail batch must ship in the delta — the journal still holds it.
	seenTail := false
	for _, f := range delta.GetFrames() {
		if b := f.GetBatch(); b != nil && b.GetBatchId() == "tail-batch" {
			seenTail = true
		}
	}
	assert.True(t, seenTail, "the post-cursor tail batch must ship in the resume delta across the decoupled window")
}

// TestSubscribeWithACL_StaleEpoch_FallsBack pins the epoch leg of the FALLBACK
// rule: even with a cursor above the watermark, a client whose epoch is stale
// (advances are time-based, 14d, but the rule must hold regardless) must get a
// full snapshot so the client re-syncs current_epoch for SubmitOps echo.
func TestSubscribeWithACL_StaleEpoch_FallsBack(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "batch-a", "root1", "a0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()

	// A deliberately-wrong epoch (live is 1 after bootstrap; pass 999).
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), 999, resolveAll)
	require.NoError(t, err)
	require.NotNil(t, out.Unsub())
	defer out.Unsub()()

	require.Equal(t, crdt.SubscribeInitial, out.Mode(), "stale-epoch resume must FALLBACK")
}

// TestSubscribeWithACL_NilCursor_FallsBack covers the "no resume param" path
// (a fresh connect, or a client with no persisted watermark): a nil cursor
// degrades to a full snapshot. This is the path an older client that never
// sends resume_after_hlc takes.
func TestSubscribeWithACL_NilCursor_FallsBack(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")

	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, nil, 0, resolveAll)
	require.NoError(t, err)
	require.NotNil(t, out.Unsub())
	defer out.Unsub()()

	require.Equal(t, crdt.SubscribeInitial, out.Mode(), "nil cursor must FALLBACK")
}

// TestSubscribeOutcome_DiscriminatedAccessPanicsOnWrongArm pins the type-level
// contract of the discriminated SubscribeOutcome: reading the payload of the arm
// that was NOT selected panics, so a caller that reads the wrong arm fails
// loudly instead of silently sending a nil frame. This is what the type (vs the
// prior nil-discipline tuple) buys.
func TestSubscribeOutcome_DiscriminatedAccessPanicsOnWrongArm(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")

	// FALLBACK outcome: Initial() is valid, Delta() panics.
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	fallback, err := mgr.SubscribeWithACL(context.Background(), sub, nil, 0, resolveAll)
	require.NoError(t, err)
	defer fallback.Unsub()()
	require.Equal(t, crdt.SubscribeInitial, fallback.Mode())
	require.NotNil(t, fallback.Initial(), "Initial() on a FALLBACK outcome returns the snapshot")
	// The panic message must name the arm textually (via SubscribeMode.String),
	// not render an opaque integer — the whole point of failing loudly is to
	// fail informatively too.
	assert.PanicsWithValue(t,
		"crdt: SubscribeOutcome.Delta called in SubscribeInitial mode",
		func() { _ = fallback.Delta() }, "Delta() on a FALLBACK outcome must panic with a textual mode")

	// RESUME outcome: Delta() is valid, Initial() panics.
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	sub2 := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	delta, err := mgr.SubscribeWithACL(context.Background(), sub2, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err)
	defer delta.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, delta.Mode())
	require.NotNil(t, delta.Delta(), "Delta() on a RESUME outcome returns the delta")
	assert.PanicsWithValue(t,
		"crdt: SubscribeOutcome.Initial called in SubscribeDelta mode",
		func() { _ = delta.Initial() }, "Initial() on a RESUME outcome must panic with a textual mode")
}

// TestSubscribeMode_String pins that SubscribeMode names its arms textually. The
// Delta()/Initial() panic messages format the mode with %v, so without a
// String() method a wrong-arm bug reports an opaque integer instead of the arm
// name — defeating half the purpose of the assertion-gated access.
func TestSubscribeMode_String(t *testing.T) {
	assert.Equal(t, "SubscribeDelta", crdt.SubscribeDelta.String())
	assert.Equal(t, "SubscribeInitial", crdt.SubscribeInitial.String())
}

// TestSubscribeWithACL_TailOverBudget_FallsBack pins that an over-budget tail
// makes SubscribeWithACL ship a full snapshot rather than a delta.
//
// This used to call j.ListBatchesAfter directly and assert that the FAKE
// returned the sentinel it had been hand-written to return — proving nothing
// about SubscribeWithACL, whose FALLBACK behavior the name promises. It also
// asserted the partial tail was non-empty, which contradicts the Journal
// contract ("on ErrDeltaTooLarge the returned slice is incidental and MUST be
// discarded") and would have reddened if the production path correctly started
// returning nil. Drive the real entry point and assert the observable outcome.
func TestSubscribeWithACL_TailOverBudget_FallsBack(t *testing.T) {
	mgr, j, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	submitNodePositionBatch(t, mgr, "post-cursor", "root1", "p1")

	// Force the budget verdict the real journal would reach on a huge tail.
	j.listErr = crdt.ErrDeltaTooLarge

	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, cursor, epoch, resolveAll)
	require.NoError(t, err, "an over-budget tail is recoverable, not a connect failure")
	require.Equal(t, crdt.SubscribeInitial, out.Mode(),
		"an over-budget tail must FALLBACK to a full snapshot")
	defer out.Unsub()()
	require.NotNil(t, out.Initial(), "the FALLBACK arm must carry a materialized snapshot")
}

// resumeFixture seeds the minimum shape that takes the RESUME arm -- one
// workspace, a cursor batch, and one post-cursor tail batch -- and returns the
// manager plus the cursor/epoch a reconnecting client would present.
//
// Deterministic by construction: runManager injects a clock that advances 1ms
// per call and every now() consumer reached here is driven by these submits, so
// two fixtures built with the same seed produce byte-identical deltas. The
// frame-ceiling cases below depend on that -- they measure a delta under one
// ceiling and re-run the same fixture under another -- and assert the
// reproduction explicitly rather than assuming it.
func resumeFixture(t *testing.T, opts ...crdt.ManagerOption) (*crdt.Manager, *leapmuxv1.HLC, int64) {
	t.Helper()
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000, opts...)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	submitNodePositionBatch(t, mgr, "post-cursor", "root1", "p1")
	return mgr, cursor, epoch
}

// resumeOnce runs one SubscribeWithACL against a seeded fixture and returns the
// outcome plus the subscriber's capture buffer.
func resumeOnce(t *testing.T, mgr *crdt.Manager, cursor *leapmuxv1.HLC, epoch int64) (crdt.SubscribeOutcome, *captureSubscriber) {
	t.Helper()
	cap := &captureSubscriber{}
	sub := &crdt.Subscriber{UserID: "user-1", Send: cap.send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, cursor, epoch, resolveAll)
	require.NoError(t, err)
	return out, cap
}

// TestSubscribeWithACL_BuiltDeltaOverFrameCeiling_FallsBack pins the EXACT gate
// on the built delta, which is a different bound from the scan's
// MaxResumeDeltaBytes source-row budget: that one caps the decoded journal rows
// (memory, during a streaming scan), this one caps the single WebSocket frame
// the delta ships as.
//
// Getting it wrong is not a slow path but an unrecoverable one: the client's
// socket read limit (channelwire.UserEventsReadLimit) fails the oversized frame,
// the client reconnects with the SAME cursor, the hub rebuilds the SAME frame,
// and the pair loops forever with no server-side error to attribute it to.
//
// The ceiling is injected at 1 byte rather than the delta being grown to 16 MiB:
// the gate is a size comparison, so a small ceiling exercises exactly the same
// branch for a fraction of the runtime. 1 -- not 0 -- so the FALLBACK cannot be
// passing for the degenerate reason that any delta at all exceeds it.
// MaxResumeDeltaBytes is deliberately left at its production value, so the tail
// read succeeds and the FALLBACK can ONLY have come from this gate.
func TestSubscribeWithACL_BuiltDeltaOverFrameCeiling_FallsBack(t *testing.T) {
	mgr, cursor, epoch := resumeFixture(t, crdt.WithMaxResumeDeltaFrameBytes(1))

	out, cap := resumeOnce(t, mgr, cursor, epoch)
	require.Equal(t, crdt.SubscribeInitial, out.Mode(),
		"a delta past the client's frame ceiling must FALLBACK, never ship")
	defer out.Unsub()()
	snap := out.Initial()
	require.NotNil(t, snap, "the FALLBACK arm must carry a materialized snapshot")
	require.Equal(t, "p1", snap.GetNodes()["root1"].GetPosition().GetValue(),
		"the snapshot must carry the post-cursor state the rejected delta would have replayed")

	// The gate reuses the over-budget arm's teardown (unsub + resumeScanFallback),
	// so the register→unsub→re-register churn must leave the subscriber
	// registered EXACTLY once -- not zero (torn down and never re-added) and not
	// two (torn down by the wrong handle). One later broadcast, one batch frame.
	cap.events = nil
	submitNodePositionBatch(t, mgr, "post-fallback", "root1", "p2")
	var batches int
	for _, e := range cap.snapshot() {
		if e.GetBatch() != nil {
			batches++
		}
	}
	require.Equal(t, 1, batches,
		"the frame-gate FALLBACK must leave exactly one registration (0 = torn down, 2 = double-registered)")
}

// TestSubscribeWithACL_BuiltDeltaAtFrameCeiling_Ships pins the gate's boundary
// from both sides. The comparison is strictly `>`, so a delta whose proto.Size
// EQUALS the ceiling still ships; flipping it to `>=` would push every delta
// that exactly filled the budget onto a full snapshot for no reason, and the
// over-ceiling case alone cannot tell the two spellings apart.
func TestSubscribeWithACL_BuiltDeltaAtFrameCeiling_Ships(t *testing.T) {
	// Measure what this fixture's delta actually weighs, under the production
	// ceiling (16 MiB - envelope headroom), which it is nowhere near.
	measureMgr, measureCursor, measureEpoch := resumeFixture(t)
	measured, _ := resumeOnce(t, measureMgr, measureCursor, measureEpoch)
	require.Equal(t, crdt.SubscribeDelta, measured.Mode(),
		"the unconstrained fixture must RESUME, or the boundary below measures nothing")
	size := proto.Size(measured.Delta())
	measured.Unsub()()
	require.Positive(t, size, "a resume with a post-cursor tail must build a non-empty delta")

	// Exactly at the ceiling: ships.
	atMgr, atCursor, atEpoch := resumeFixture(t, crdt.WithMaxResumeDeltaFrameBytes(size))
	atOut, _ := resumeOnce(t, atMgr, atCursor, atEpoch)
	require.Equal(t, crdt.SubscribeDelta, atOut.Mode(),
		"a delta exactly AT the ceiling is within budget and must ship")
	defer atOut.Unsub()()
	require.Equal(t, size, proto.Size(atOut.Delta()),
		"the fixture must reproduce byte-identically, or the boundary this case pins is not the one it measured")

	// One byte under: the same delta FALLBACKs.
	underMgr, underCursor, underEpoch := resumeFixture(t, crdt.WithMaxResumeDeltaFrameBytes(size-1))
	underOut, _ := resumeOnce(t, underMgr, underCursor, underEpoch)
	require.Equal(t, crdt.SubscribeInitial, underOut.Mode(),
		"one byte over the ceiling is over the ceiling")
	defer underOut.Unsub()()
}

// TestMaxResumeDeltaFrameBytes_LeavesEnvelopeHeadroomUnderTheReadLimit pins the
// constant itself, which is the half of the gate no behavioural test can reach:
// the ceiling exists to keep the WHOLE WebSocket message under the limit every
// subscriber's socket enforces, and the message is the measured ResumeDelta plus
// the WatchUserEvent wrapper, the 4-byte length prefix, and the two ids
// SubscribeOutcome.Bootstrap stamps AFTER the gate has run. A ceiling raised to
// (or past) the read limit would let a delta pass the gate and still fail the
// client's read -- the exact reconnect loop the gate exists to prevent.
func TestMaxResumeDeltaFrameBytes_LeavesEnvelopeHeadroomUnderTheReadLimit(t *testing.T) {
	require.Less(t, crdt.MaxResumeDeltaFrameBytes, channelwire.UserEventsReadLimit,
		"the gate must sit strictly below the read limit it protects")

	// A maximal delta wrapped in everything the wire adds must still fit. The
	// payload is opaque bytes on a scratch frame, so this measures the ENVELOPE
	// overhead, not a realistic delta's contents.
	envelope := &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Delta{Delta: &leapmuxv1.ResumeDelta{
			SubscriberClientId: strings.Repeat("c", 128),
			UserId:             strings.Repeat("u", 128),
		}},
	}
	const lengthPrefix = 4 // channelwire.WriteFramedBytes
	require.LessOrEqual(t, crdt.MaxResumeDeltaFrameBytes+proto.Size(envelope)+lengthPrefix,
		channelwire.UserEventsReadLimit,
		"a delta at the ceiling plus its envelope must still be readable by the client")
}

// TestSubscribeWithACL_DeltaMaxHlcIsTheFilteredTailMax_NotLiveState pins the
// watermark-correctness contract (issue tracked via deep review): the delta's
// max_hlc is the max canonical HLC over the FILTERED tail's own ops, never a
// read of the live state's max. A live-state read would let a commit landing
// between the tail read and the max read inflate the watermark past ops the
// client has not received; the next resume from that inflated cursor would
// skip them. Here the tail's tail batch sits under an allowed workspace (w1)
// while a LATER op under a disallowed workspace (w2) advances the live state
// max — but is filtered out of this subscriber's tail. The delta's max_hlc
// must be the w1 op's HLC, NOT the (higher) live max the w2 op produced.
func TestSubscribeWithACL_DeltaMaxHlcIsTheFilteredTailMax_NotLiveState(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")

	// Cursor sits before both tail commits.
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)

	// The w1 op is the tail the w1-only subscriber receives; the w2 op advances
	// the live state max but is filtered out of the w1 subscriber's tail.
	submitNodePositionBatch(t, mgr, "w1-tail", "root1", "w1op")
	submitNodePositionBatch(t, mgr, "w2-later", "root2", "w2op")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	liveMax := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, liveMax)

	resolveOnlyW1 := func() (map[string]bool, error) { return map[string]bool{"w1": true}, nil }
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveOnlyW1)
	require.NoError(t, err)
	require.NotNil(t, out.Unsub())
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, out.Mode())
	delta := out.Delta()

	// Only the w1 op's CONTENT ships; the w2 batch contributes no ops.
	batchFrames := deltaBatchFrames(t, delta)
	require.Len(t, batchFrames, 1, "only the w1 (allowed) op should be in the tail")

	// max_hlc is the last SCANNED batch's boundary, not the last VISIBLE op's.
	// Every batch in the tail emits a batch_end, including one whose ops are all
	// filtered out, so a narrowly-filtered subscriber advances past traffic it
	// cannot see instead of re-scanning the same journal tail on every
	// reconnect. That is safe because an entity that later becomes visible
	// arrives as a full EntityMaterialized record, never as its historical ops.
	assert.Equal(t, liveMax.GetPhysical(), delta.GetMaxHlc().GetPhysical(),
		"max_hlc must reach the last scanned batch's boundary, even when that batch was filtered out")

	// The bound that still matters: max_hlc comes from the SCAN (capped at the
	// register-time `until`), never from a live-state read. A commit landing
	// after the scan must not be advertised -- it is delivered as a broadcast
	// frame instead, and advertising it here would let a
	// disconnect-before-broadcast skip it on the next resume.
	submitNodePositionBatch(t, mgr, "after-scan", "root1", "later")
	postScanMax := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	assert.Less(t, delta.GetMaxHlc().GetPhysical(), postScanMax.GetPhysical(),
		"delta max_hlc must not track commits that landed after the scan")
}

// TestSubscribeWithACL_DeltaMaxHlcFallsBackToCursorOnEmptyTail pins the empty-tail
// arm of the max_hlc computation: when every post-cursor op is filtered out of
// the subscriber's tail, the delta's max_hlc falls back to the cursor itself
// (nothing newer-than-the-cursor was visible, so the cursor remains the honest
// high-water mark), never to the live state max.
func TestSubscribeWithACL_DeltaMaxHlcFallsBackToCursorOnEmptyTail(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")

	// Cursor under w1.
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	// The only post-cursor op is under w2 (disallowed for a w1-only subscriber),
	// so the filtered tail is empty.
	submitNodePositionBatch(t, mgr, "w2-tail", "root2", "w2op")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	resolveOnlyW1 := func() (map[string]bool, error) { return map[string]bool{"w1": true}, nil }
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveOnlyW1)
	require.NoError(t, err)
	require.NotNil(t, out.Unsub())
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, out.Mode())
	delta := out.Delta()
	assert.Empty(t, deltaBatchFrames(t, delta), "the w2-only tail must be filtered out")
	// No visible OPS, but the scanned batch still emits its boundary, so the
	// cursor advances past traffic this subscriber cannot see. Without that, a
	// narrowly-filtered client would re-scan the same growing tail on every
	// reconnect until it exceeded MaxResumeDeltaOps and was pinned to full
	// snapshots.
	scannedMax := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	assert.Equal(t, scannedMax.GetPhysical(), delta.GetMaxHlc().GetPhysical(),
		"a fully-filtered tail must still advance max_hlc to the last scanned batch's boundary")
	assert.Greater(t, delta.GetMaxHlc().GetPhysical(), cursor.GetPhysical(),
		"...and therefore strictly past the cursor the client presented")
}

// TestSubscribeWithACL_TailReadErrorTearsDownRegistration pins the journal-tail-
// error teardown: after registerSubscriber runs, a non-budget/non-corrupt error
// from ListBatchesAfter must undo the registration (so no subscriber leaks
// whose bootstrap was never delivered); the returned outcome's Unsub() is a
// safe no-op (the registration was already torn down before the error return).
// The teardown is load-bearing — without it, a transient DB error during resume
// would leak a registered subscriber sponging broadcasts into a dead channel
// and skew the presence refcount. ErrDeltaTooLarge takes the FALLBACK path
// instead (see TestSubscribeWithACL_OverBudgetFallbackRegistersExactlyOnce);
// a corrupt row is now skipped+surfaced, not fatal (see
// crdtJournal.scan's recoverable contract).
func TestSubscribeWithACL_TailReadErrorTearsDownRegistration(t *testing.T) {
	mgr, j, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Inject a plain (non-ErrDeltaTooLarge) error into the resume tail read.
	j.listErr = errors.New("simulated DB failure")
	// The sub's Send records into its own capture so a leaked registration
	// would be observable: a subsequent broadcast would append to cap.events.
	cap := &captureSubscriber{}
	sub := &crdt.Subscriber{UserID: "user-1", Send: cap.send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.Error(t, err, "a plain tail-read error must surface")
	// The error path tears down the registration before returning, so there is
	// no live handle to defer. SubscribeOutcome.Unsub() returns a no-op (not
	// nil) on such an outcome so a caller that defers it before checking err is
	// safe; calling it here must not double-teardown or panic.
	assert.NotPanics(t, func() { out.Unsub()() }, "Unsub on an error-path outcome must be a safe no-op")

	// The subscriber must not remain registered. Clear the injected error and
	// commit a batch (which fans a broadcast out to every registered subscriber);
	// the torn-down sub's capture must stay empty — if the teardown had leaked,
	// the broadcast would land in cap.events.
	j.listErr = nil
	submitNodePositionBatch(t, mgr, "post-error-batch", "root1", "p0")
	assert.Empty(t, cap.snapshot(), "the torn-down subscriber must not receive the post-error broadcast (registration leaked)")
}

// TestSubscribeWithACL_CorruptRowFallsBackWithoutLosingOps pins the resume-level
// half of stop-on-corrupt: a tail with one undecodable batch in the middle must
// NOT ship a partial delta.
//
// The tempting behavior — skip the bad row, ship the good tail around it — is
// silently lossy. The corrupt batch contributes no frames, but the batch AFTER
// it does, so the delta's max_hlc lands above the hole; the client adopts that
// watermark, and because the resume cursor is strictly-greater, no later resume
// ever re-requests the skipped batch. The client diverges permanently (and,
// since the #356 checkpoint, persists the divergence across refreshes).
//
// So the contract is: a corrupt row is a RECOVERABLE verdict that FALLBACKs to
// a full snapshot — always complete — and never a connect error.
func TestSubscribeWithACL_CorruptRowFallsBackWithoutLosingOps(t *testing.T) {
	mgr, j, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed two post-cursor batches and mark the FIRST corrupt. The SECOND is
	// what made skip-and-continue lossy: its frames would have advanced max_hlc
	// past the corrupt batch.
	submitNodePositionBatch(t, mgr, "corrupt-batch", "root1", "p1")
	submitNodePositionBatch(t, mgr, "good-batch", "root1", "p2")
	j.corruptTransitions = map[string]bool{"corrupt-batch": true}

	cap := &captureSubscriber{}
	sub := &crdt.Subscriber{UserID: "user-1", Send: cap.send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, cursor, epoch, resolveAll)
	require.NoError(t, err, "a corrupt row is recoverable: it must not surface a connect error")
	require.Equal(t, crdt.SubscribeInitial, out.Mode(),
		"a corrupt row must FALLBACK; shipping the good tail around it would strand the corrupt batch's ops forever")
	defer out.Unsub()()

	// The whole point of falling back: the snapshot carries BOTH post-cursor
	// batches' effects, including the one whose journal row was undecodable.
	// (A delta would have carried only "good-batch".)
	snap := out.Initial()
	require.NotNil(t, snap)
	node := snap.GetNodes()["root1"]
	require.NotNil(t, node, "the fallback snapshot must materialize the node both batches touched")
	require.Equal(t, "p2", node.GetPosition().GetValue(),
		"the snapshot reflects the latest committed position, so no committed op was lost to the corrupt row")
}

// TestSubscribeWithACL_OverBudgetFallbackRegistersExactlyOnce pins the
// register→undo→re-register churn on the ErrDeltaTooLarge FALLBACK path. After
// the over-budget tail read, SubscribeWithACL calls unsub() (tearing down the
// RESUME registration) then subscribeLocked (re-registering via the FALLBACK
// seam). The subscriber must end up registered EXACTLY ONCE: a subsequent
// broadcast must deliver exactly one frame, not two (double-registration) or
// zero (the unsub tore down the wrong handle). The over-budget path is driven
// through SubscribeWithACL itself (not the journal stub) so the full churn runs.
func TestSubscribeWithACL_OverBudgetFallbackRegistersExactlyOnce(t *testing.T) {
	mgr, j, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Force the resume tail read to hit the ErrDeltaTooLarge ceiling so
	// SubscribeWithACL takes the FALLBACK-reentry path (register → unsub →
	// subscribeLocked → re-register) rather than the plain RESUME arm.
	j.listErr = crdt.ErrDeltaTooLarge
	cap := &captureSubscriber{}
	sub := &crdt.Subscriber{UserID: "user-1", Send: cap.send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err, "ErrDeltaTooLarge must FALLBACK, not error")
	require.Equal(t, crdt.SubscribeInitial, out.Mode(), "over-budget cursor must FALLBACK")
	defer out.Unsub()()
	require.NotNil(t, out.Initial(), "FALLBACK must carry a snapshot")

	// Clear the injected ceiling and commit a batch. The broadcast fan-out
	// reaches every registered subscriber; the over-budget path must have left
	// this subscriber registered exactly once, so exactly one broadcast lands.
	j.listErr = nil
	cap.events = nil
	submitNodePositionBatch(t, mgr, "post-fallback-batch", "root1", "p0")
	// Count BATCH frames, not raw frames: one broadcast is now a batch frame
	// plus its batch_end boundary, so a raw count of 2 is a single delivery.
	// Counting the batch arm keeps this pinned on registration cardinality
	// rather than on how many frames a batch happens to fan out as.
	var batches int
	for _, e := range cap.snapshot() {
		if e.GetBatch() != nil {
			batches++
		}
	}
	require.Equal(t, 1, batches, "the FALLBACK-reentered subscriber must receive exactly one broadcast (not zero=unregistered, not two=double-registered)")
}

// TestSubscribeWithACL_NeitherExpandNorBroadcastStalledDuringScan is the
// load-bearing concurrency test for the lock-release restructure: while a
// RESUME's journal tail scan is in flight (held via the fake journal's
// listHold), NEITHER a workspace-create expand (which takes subscribeExpandMu)
// NOR a live commit broadcast to another subscriber (which takes m.projection)
// may stall behind the scan. Before the restructure both locks were held across
// the multi-page DB read, so a reconnect stalled that user's workspace lifecycle
// RPCs and every other tab's live broadcasts for the scan's duration; this test
// FAILS against that code (both operations time out) and PASSES once the RESUME
// arm releases both locks after registering.
func TestSubscribeWithACL_NeitherExpandNorBroadcastStalledDuringScan(t *testing.T) {
	mgr, j, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	// A cursor + one post-cursor tail batch so RESUME is chosen (not FALLBACK).
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Hold the resume's tail scan mid-flight, and signal when it has entered.
	listHold := make(chan struct{})
	listReached := make(chan struct{})
	j.listHold = listHold
	j.listReached = listReached

	// Start the RESUME in a goroutine. It registers, releases the locks, then
	// blocks in ListBatchesAfter on listHold.
	type res struct {
		out crdt.SubscribeOutcome
		err error
	}
	resCh := make(chan res, 1)
	resumeSub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	go func() {
		out, err := mgr.SubscribeWithACL(context.Background(), resumeSub, crdt.HLCClone(cursor), epoch, resolveAll)
		resCh <- res{out, err}
	}()

	// Wait until the resume has entered its tail scan (it is past register and
	// has released subscribeExpandMu + m.projection).
	<-listReached

	// (a) A workspace-create expand must NOT be blocked on subscribeExpandMu.
	expandDone := make(chan error, 1)
	go func() {
		expandDone <- mgr.ExpandSubscribersForWorkspace(context.Background(), "w1")
	}()
	select {
	case err := <-expandDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("ExpandSubscribersForWorkspace was stalled behind a resume's tail scan — subscribeExpandMu not released")
	}

	// (b) A live commit's broadcast to ANOTHER registered subscriber must NOT be
	// blocked on m.projection. Register a second subscriber, commit a batch, and
	// assert its broadcast lands while the resume scan is still held.
	capOther := &captureSubscriber{}
	otherSub := &crdt.Subscriber{UserID: "user-1", Send: capOther.send}
	otherOut, err := mgr.SubscribeWithACL(context.Background(), otherSub, nil, 0, resolveAll)
	require.NoError(t, err)
	defer otherOut.Unsub()()

	submitNodePositionBatch(t, mgr, "during-scan-batch", "root1", "live")
	select {
	case got := <-signalLen(capOther):
		require.GreaterOrEqual(t, got, 1, "the other subscriber must receive the live broadcast during the resume scan — m.projection not released")
	case <-time.After(time.Second):
		t.Fatal("live broadcast was stalled behind a resume's tail scan — m.projection not released")
	}

	// Release the held scan and let the RESUME complete.
	close(listHold)
	r := <-resCh
	require.NoError(t, r.err)
	defer r.out.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, r.out.Mode(), "the held resume must still complete as a delta")

	// Register-time until high-water: the during-scan commit landed AFTER the
	// resume captured MaxHlc, so it must arrive via live broadcast only — not
	// also as a frame inside the delta (dual-delivery of the same batch).
	for _, f := range r.out.Delta().GetFrames() {
		if b := f.GetBatch(); b != nil {
			assert.NotEqual(t, "during-scan-batch", b.GetBatchId(),
				"post-register commits must not appear in the resume delta (owned by live broadcast)")
		}
	}
}

// TestSubscribeOutcome_ZeroValueIsNotAReadyDelta pins the zero-value guard on
// SubscribeMode.
//
// Every error return in SubscribeWithACL / resumeFallback is
// `SubscribeOutcome{}, err`. While SubscribeDelta was the iota-zero arm, that
// value read as "delta ready" carrying a nil *ResumeDelta: today's sole caller
// checks err first, but a second caller -- or a refactor that logs Mode()
// before the err check -- would ship WatchUserEvent_Delta{Delta: nil} as the
// bootstrap frame, leaving the client "connected" against unhydrated state with
// every submit rejected STALE_EPOCH. The sibling resumeScanKind already
// reserved its zero value for exactly this; this is the same guard one type
// over.
func TestSubscribeOutcome_ZeroValueIsNotAReadyDelta(t *testing.T) {
	var zero crdt.SubscribeOutcome

	assert.NotEqual(t, crdt.SubscribeDelta, zero.Mode(),
		"the zero outcome is what every error path returns; it must not read as a ready delta")
	assert.NotEqual(t, crdt.SubscribeInitial, zero.Mode(),
		"nor as a ready snapshot")
	assert.Equal(t, "SubscribeMode(invalid)", zero.Mode().String(),
		"the invalid arm must name itself, so a log line on the error path is legible")

	// The discriminated accessors stay assertion-gated on the zero value, so a
	// caller that skips the err check fails loudly instead of sending nil.
	assert.Panics(t, func() { _ = zero.Delta() })
	assert.Panics(t, func() { _ = zero.Initial() })

	// Unsub stays safe to defer unconditionally -- that contract predates this
	// change and the guard must not break it.
	assert.NotPanics(t, func() { zero.Unsub()() })
}

// signalLen returns a channel that receives cap.snapshot()'s length once a
// broadcast lands (polled briefly), so a test can select on it with a timeout.
func signalLen(cap *captureSubscriber) <-chan int {
	ch := make(chan int, 1)
	go func() {
		// The broadcast is delivered synchronously inside SubmitInternal's
		// commit, so by the time submitNodePositionBatch returns it has either
		// landed or been blocked. Poll a few times to let any buffered send
		// flush, then report.
		for i := 0; i < 20; i++ {
			if n := len(cap.snapshot()); n > 0 {
				ch <- n
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		ch <- 0
	}()
	return ch
}

// TestSubscribeWithACL_PostCursorTailShipsInDelta pins that a batch committed
// after the resume cursor (and therefore inside register-time until) appears
// in the ResumeDelta frame stream.
func TestSubscribeWithACL_PostCursorTailShipsInDelta(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	submitNodePositionBatch(t, mgr, "in-until-batch", "root1", "iu0")

	cap := &captureSubscriber{}
	sub := &crdt.Subscriber{UserID: "user-1", Send: cap.send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, out.Mode())

	inDelta := false
	for _, f := range out.Delta().GetFrames() {
		if b := f.GetBatch(); b != nil && b.GetBatchId() == "in-until-batch" {
			inDelta = true
		}
	}
	assert.True(t, inDelta, "post-cursor batch must ship in the resume delta")
}

// TestSubscribeWithACL_CompactionDuringScan_FallsBack pins that a compaction
// advancing the op_retention_watermark past the cursor during the unlocked
// journal scan must FALLBACK rather than return a shortened delta the client
// would treat as complete catch-up.
//
// Op-batch deletion (and therefore the resume gate) keys on
// op_retention_watermark, which lags compaction_watermark by OpRetentionTTL.
// This test shrinks the TTL to zero so a single compaction tick advances the
// retention floor to the compaction watermark (== max_hlc), past the resume
// cursor — exercising the post-scan re-check that invalidates a resume when
// compaction races the scan. The production 24h default would leave the
// retention floor far below the cursor and a single tick would NOT invalidate.
func TestSubscribeWithACL_CompactionDuringScan_StillResumes(t *testing.T) {
	// This used to force a post-scan FALLBACK with WithOpRetentionTTL(0), so a
	// single compaction tick pushed op_retention_watermark past the cursor while
	// the scan was held. That scenario is now UNREACHABLE, and deliberately so.
	//
	// decideResume gained a wall-clock floor (cursor.physical >= now - TTL)
	// alongside the stored-watermark floor, because the cleanup job sweeps op
	// batches by committed_at and a dormant account's stored floor stops
	// advancing. Since max_hlc <= now, any cursor the stored floor could
	// invalidate mid-scan (cursor <= max_hlc - TTL) was already below the
	// wall-clock floor (now - TTL) at decide time, so it never reaches the scan.
	//
	// The post-scan re-check therefore survives as defence-in-depth and for the
	// epoch arm, not for compaction. What is still worth pinning here is the
	// concurrency itself: a compaction tick landing mid-scan must not corrupt or
	// hang the resume.
	mgr, j, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	submitNodePositionBatch(t, mgr, "tail-batch", "root1", "t0")

	listHold := make(chan struct{})
	listReached := make(chan struct{})
	j.listHold = listHold
	j.listReached = listReached

	type res struct {
		out crdt.SubscribeOutcome
		err error
	}
	resCh := make(chan res, 1)
	resumeSub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	go func() {
		out, err := mgr.SubscribeWithACL(context.Background(), resumeSub, crdt.HLCClone(cursor), epoch, resolveAll)
		resCh <- res{out, err}
	}()

	<-listReached
	mgr.TickHousekeeping(context.Background())
	close(listHold)

	r := <-resCh
	require.NoError(t, r.err)
	defer r.out.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, r.out.Mode(),
		"a compaction tick during the scan must not invalidate a cursor inside the retention window")
	require.NotNil(t, r.out.Delta())
}

// TestDecideResume_WallClockFloorFallsBack pins the floor that makes the
// cleanup job's committed_at sweep safe.
//
// op_retention_watermark only advances while a user is committing (maybeCompact
// short-circuits once compaction_watermark reaches max_hlc), so a dormant
// account's stored floor freezes while the sweep keeps deleting its rows by
// wall clock. Gating on the stored floor alone would admit a cursor whose
// batches were already swept and ship a silently short tail.
func TestDecideResume_WallClockFloorFallsBack(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// A cursor one full retention window in the past. The stored floor is still
	// zero (no compaction has advanced it), so ONLY the wall-clock floor can
	// refuse this.
	stale := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, stale)
	stale.Physical -= int64(crdt.OpRetentionTTL / time.Millisecond)
	require.True(t, crdt.HLCIsZero(mgr.State().GetOpRetentionWatermark()),
		"precondition: the stored floor must be zero so it cannot be what refuses")

	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, stale, epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeInitial, out.Mode(),
		"a cursor older than OpRetentionTTL must FALLBACK: the sweep may already have deleted its batches")
}

// TestSubscribeWithACL_OverBudgetFallbackReResolvesFilter pins that the
// over-budget FALLBACK path re-runs resolve under subscribeExpandMu before
// re-registering, so an ACL change that landed while the RESUME sub was
// registered is reflected in the FALLBACK snapshot filter.
func TestSubscribeWithACL_OverBudgetFallbackReResolvesFilter(t *testing.T) {
	mgr, j, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	calls := 0
	resolve := func() (map[string]bool, error) {
		calls++
		if calls == 1 {
			// First resolve (RESUME decision): only w1.
			return map[string]bool{"w1": true}, nil
		}
		// Re-resolve on FALLBACK: ACL now includes w2.
		return map[string]bool{"w1": true, "w2": true}, nil
	}

	j.listErr = crdt.ErrDeltaTooLarge
	cap := &captureSubscriber{}
	sub := &crdt.Subscriber{UserID: "user-1", Send: cap.send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolve)
	require.NoError(t, err)
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeInitial, out.Mode())
	require.GreaterOrEqual(t, calls, 2, "FALLBACK must re-resolve the ACL")
	fellBack := out.Initial()
	assert.Contains(t, fellBack.GetWorkspaces(), "w1")
	assert.Contains(t, fellBack.GetWorkspaces(), "w2",
		"FALLBACK snapshot must use the re-resolved filter, not the stale RESUME filter")
}

// TestSubscribeWithACL_FallbackFiresOnRebaseline pins the seam that keeps a
// RESUME->FALLBACK from resurrecting stale entity records.
//
// During the resume scan the subscriber is registered, so live broadcasts queue
// in the transport. If the scan then gives up, the snapshot is computed at a
// LATER point than those queued frames -- and the client applies
// entity_materialized / entity_removed WHOLESALE, with no HLC compare (unlike
// batch ops, which go through a strictly-greater LWW guard). Writing the queued
// frames after the snapshot would therefore reinstate older records permanently.
// The manager calls OnRebaseline under its projection lock at exactly the
// re-register point so the transport can drop precisely the superseded frames.
func TestSubscribeWithACL_FallbackFiresOnRebaseline(t *testing.T) {
	mgr, j, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	submitNodePositionBatch(t, mgr, "tail", "root1", "t0")

	// Force the FALLBACK arm.
	j.listErr = crdt.ErrDeltaTooLarge

	rebaselines := 0
	sub := &crdt.Subscriber{
		UserID:       "user-1",
		Send:         (&captureSubscriber{}).send,
		OnRebaseline: func() { rebaselines++ },
	}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, cursor, epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeInitial, out.Mode())
	assert.Equal(t, 1, rebaselines,
		"the FALLBACK re-entry must tell the transport to discard frames queued during the scan")
}

// TestSubscribeWithACL_ResumeDoesNotFireOnRebaseline is the other half: a resume
// that SUCCEEDS never re-registers, so nothing was superseded and the transport
// must keep everything it queued during the scan. Firing the hook there would
// silently drop live frames that are strictly newer than the delta.
func TestSubscribeWithACL_ResumeDoesNotFireOnRebaseline(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	submitNodePositionBatch(t, mgr, "tail", "root1", "t0")

	rebaselines := 0
	sub := &crdt.Subscriber{
		UserID:       "user-1",
		Send:         (&captureSubscriber{}).send,
		OnRebaseline: func() { rebaselines++ },
	}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, cursor, epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, out.Mode())
	assert.Zero(t, rebaselines, "a successful resume supersedes nothing and must not discard queued frames")
}

// A subscriber whose transport dropped a frame during the resume scan must get
// a SNAPSHOT, not a delta with a hole in it — and not a torn-down connection.
//
// Tearing down was the previous answer, and it sent the client back with the
// SAME cursor to rebuild the SAME multi-page scan, under the sustained
// broadcast load that caused the overflow in the first place. The loop is
// bounded (a widening gap eventually trips MaxResumeDeltaOps and FALLBACKs
// anyway), but every round is a wasted journal scan on an already-stressed hub.
// FALLBACK converts it into exactly one snapshot.
func TestSubscribeWithACL_TransportOverflowDuringScanFallsBack(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	// A tail the resume would otherwise ship, so a delta is genuinely available
	// and FALLBACK is a decision rather than the only option.
	submitNodePositionBatch(t, mgr, "tail-batch", "root1", "t0")

	overflowed := true
	sub := &crdt.Subscriber{
		UserID:     "user-1",
		Send:       (&captureSubscriber{}).send,
		Overflowed: func() bool { return overflowed },
	}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	assert.Equal(t, crdt.SubscribeInitial, out.Mode(),
		"a transport that dropped a frame mid-scan must get a complete snapshot")
	// Not asserting out.Delta() is nil: SubscribeOutcome PANICS on a Delta()
	// read in any mode but SubscribeDelta, which is the stronger guarantee.
}

// The control: the SAME setup without an overflow resumes. Without it the case
// above would pass even if resume were broken outright.
func TestSubscribeWithACL_NoOverflowStillResumes(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	submitNodePositionBatch(t, mgr, "tail-batch", "root1", "t0")

	sub := &crdt.Subscriber{
		UserID:     "user-1",
		Send:       (&captureSubscriber{}).send,
		Overflowed: func() bool { return false },
	}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	assert.Equal(t, crdt.SubscribeDelta, out.Mode())
}

// A nil Overflowed callback is the ordinary case — every non-websocket caller,
// and every test that does not care. It must read as "no overflow" rather than
// panicking or refusing to resume.
func TestSubscribeWithACL_NilOverflowCallbackResumes(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	submitNodePositionBatch(t, mgr, "tail-batch", "root1", "t0")

	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	assert.Equal(t, crdt.SubscribeDelta, out.Mode())
}

// decideResume gates on the GREATER of two floors, and this pins why both are
// needed rather than one.
//
// The persisted op_retention_watermark is authoritative while a user is active,
// but it only advances while they are COMMITTING: maybeCompact short-circuits
// once compaction_watermark reaches max_hlc, so a dormant account's stored
// value freezes at (last activity - TTL) while the cross-user cleanup sweep
// keeps deleting its rows on wall-clock time. Gating on the stored value alone
// would admit a cursor whose batches the sweep had already removed, and
// ListBatchesAfter reports deleted-below rows as an ordinary SHORT tail, not an
// error — so the client would adopt a max_hlc covering ops it never received.
//
// A zero TTL puts the wall-clock cutoff at "now", above any cursor, so it is
// the binding floor and the resume must be refused even though the stored
// watermark alone would admit it.
func TestSubscribeWithACL_WallClockFloorRefusesACursorTheStoredWatermarkWouldAdmit(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000, crdt.WithOpRetentionTTL(0))
	seedRootInternal(t, mgr, "w1", "root1")
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	submitNodePositionBatch(t, mgr, "tail-batch", "root1", "t0")

	// The STORED watermark alone would admit this cursor...
	require.True(t, crdt.HLCCmp(cursor, mgr.State().GetOpRetentionWatermark()) > 0,
		"precondition: the cursor is above the persisted op_retention_watermark")

	// ...but the wall-clock cutoff (TTL=0 => cutoff is now) is above it, so the
	// effective floor refuses it.
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveAll)
	require.NoError(t, err)
	defer out.Unsub()()
	assert.Equal(t, crdt.SubscribeInitial, out.Mode(),
		"a cursor below the wall-clock retention cutoff must FALLBACK, whatever the stored watermark says")
}
