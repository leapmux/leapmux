package crdt_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// moveTabBatch builds a single-op SetTabRegister(tile_id=newTile) batch that
// moves `tabID` onto `newTileID` (a root whose workspace differs from the tab's
// current one), mirroring the cross-workspace move primitive exercised in
// cross_workspace_move_test.go.
func moveTabBatch(batchID, tabID, newTileID string) *leapmuxv1.OpBatch {
	return &leapmuxv1.OpBatch{
		BatchId: batchID,
		Ops: []*leapmuxv1.CrdtOp{{OpId: "op-" + batchID, Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: newTileID},
		}}}},
	}
}

// submitBatch is a thin wrapper that submits one batch via the public path and
// asserts it committed, decluttering the resume regression tests.
func submitBatch(t *testing.T, mgr *crdt.Manager, epoch int64, batch *leapmuxv1.OpBatch) {
	t.Helper()
	res, err := mgr.Submit(context.Background(), crdt.SubmitInput{
		Epoch: epoch, PrincipalID: "user", OriginClient: "c1",
		Batches: []*leapmuxv1.OpBatch{batch},
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.NotNil(t, res[0].GetCommitted(), "batch %s should commit; got %v", batch.GetBatchId(), res[0])
}

// deltaFrameKinds returns the ordered list of oneof case names for a delta's
// frame stream, so an ordering assertion reads as a literal sequence.
func deltaFrameKinds(t *testing.T, delta *leapmuxv1.ResumeDelta) []string {
	t.Helper()
	out := make([]string, 0, len(delta.GetFrames()))
	for _, f := range delta.GetFrames() {
		switch f.GetEvent().(type) {
		case *leapmuxv1.WatchUserEvent_EntityMaterialized:
			out = append(out, "materialized")
		case *leapmuxv1.WatchUserEvent_Batch:
			out = append(out, "batch")
		case *leapmuxv1.WatchUserEvent_EntityRemoved:
			out = append(out, "removed")
		case *leapmuxv1.WatchUserEvent_BatchEnd:
			out = append(out, "end")
		default:
			out = append(out, "unknown")
		}
	}
	return out
}

// resumeAsW1Only performs a SubscribeWithACL resume admitting only w1 and
// returns the ResumeDelta (asserting the RESUME arm was taken).
func resumeAsW1Only(t *testing.T, mgr *crdt.Manager, cursor *leapmuxv1.HLC, epoch int64) *leapmuxv1.ResumeDelta {
	t.Helper()
	resolveOnlyW1 := func() (map[string]bool, error) { return map[string]bool{"w1": true}, nil }
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveOnlyW1)
	require.NoError(t, err)
	require.NotNil(t, out.Unsub())
	defer out.Unsub()()
	require.Equal(t, crdt.SubscribeDelta, out.Mode())
	return out.Delta()
}

// TestSubscribeWithACL_DeltaReplaysEntityRemovedOnCrossOut is the core #357
// regression: a tab that was visible to the subscriber at disconnect, then moved
// to a workspace the subscriber cannot see while disconnected, must arrive as an
// EntityRemoved frame on resume (so the client evicts the stale "ghost" record).
// Before the fix, the move op resolved to the disallowed destination and was
// silently dropped, leaving the ghost record indefinitely.
func TestSubscribeWithACL_DeltaReplaysEntityRemovedOnCrossOut(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 100_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a tab in w1 (the subscriber's allowed workspace).
	submitBatch(t, mgr, epoch, addTabBatch(t, "seed", "tA", "root1", "wkr", "p1"))

	// Capture the cursor here — this is the subscriber's last-seen watermark.
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)

	// While "disconnected": move the tab to w2 (disallowed for a w1-only subscriber).
	submitBatch(t, mgr, epoch, moveTabBatch("move-out", "tA", "root2"))

	delta := resumeAsW1Only(t, mgr, cursor, epoch)

	// The delta must contain a `removed` frame for the tab so the client evicts
	// the ghost record (the whole point of #357).
	var removedTabIDs []string
	var leakedMoveOps int
	for _, f := range delta.GetFrames() {
		if r := f.GetEntityRemoved(); r != nil {
			if tab := r.GetTab(); tab != nil {
				removedTabIDs = append(removedTabIDs, tab.GetTabId())
			}
		}
		if b := f.GetBatch(); b != nil {
			for _, op := range b.GetOps() {
				if _, isMove := op.GetBody().(*leapmuxv1.CrdtOp_SetTabRegister); isMove {
					leakedMoveOps++
				}
			}
		}
	}
	assert.Contains(t, removedTabIDs, "tA",
		"tab moved OUT of the allowed set during the gap must arrive as a removed frame (evicts the ghost record)")
	assert.Equal(t, 0, leakedMoveOps,
		"the move op into the disallowed workspace must NOT leak as a raw batch op (would carry destination info)")
}

// TestSubscribeWithACL_DeltaReplaysEntityMaterializedOnCrossIn is the symmetric
// case: a tab that was NOT visible to the subscriber at disconnect, then moved
// INTO the subscriber's allowed workspace while disconnected, must arrive as an
// EntityMaterialized frame carrying the full current record (not as raw move ops
// that would leak pre-state from the hidden source workspace).
func TestSubscribeWithACL_DeltaReplaysEntityMaterializedOnCrossIn(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 200_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a tab in w2 (NOT visible to a w1-only subscriber at disconnect).
	submitBatch(t, mgr, epoch, addTabBatch(t, "seed", "tA", "root2", "wkr", "p1"))

	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)

	// While "disconnected": move the tab into w1 (the subscriber's allowed workspace).
	submitBatch(t, mgr, epoch, moveTabBatch("move-in", "tA", "root1"))

	delta := resumeAsW1Only(t, mgr, cursor, epoch)

	// The delta must contain a `materialized` frame for the tab carrying its
	// full record (so the client installs it with current state, not the
	// pre-state the raw move op would carry from the hidden w2).
	var materializedTabIDs []string
	for _, f := range delta.GetFrames() {
		if m := f.GetEntityMaterialized(); m != nil {
			if tab := m.GetTab(); tab != nil {
				materializedTabIDs = append(materializedTabIDs, tab.GetTabId())
			}
		}
	}
	assert.Contains(t, materializedTabIDs, "tA",
		"tab moved INTO the allowed set during the gap must arrive as a materialized frame (full record)")
}

// TestSubscribeWithACL_DeltaMaterializedFrameCarriesBatchEraRecord is the
// load-bearing regression guard for the per-batch record snapshot: when an
// entity crosses INTO the allowed set in one tail batch and is then EDITED by a
// LATER tail batch (both within the gap), the materialized frame for the
// crossing-in batch must carry the BATCH-ERA record (its state at that batch's
// commit), NOT the current (post-edit) record. Before the snapshot was
// persisted, the resume materialized frame cloned CURRENT live state and would
// have shipped batch2's edit in batch1's frame — re-emitting newer state than
// broadcast would have shipped at batch1's commit.
func TestSubscribeWithACL_DeltaMaterializedFrameCarriesBatchEraRecord(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 210_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a tab in w2 with worker_id="wkr".
	submitBatch(t, mgr, epoch, addTabBatch(t, "seed", "tA", "root2", "wkr", "p1"))

	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)

	// Batch 1: move the tab INTO w1 (materialized for a w1-only subscriber). At
	// this batch's commit the tab's worker_id is still "wkr".
	submitBatch(t, mgr, epoch, moveTabBatch("move-in", "tA", "root1"))
	// Batch 2: edit the tab's worker_id to "wkr2" (stable-visibility — the tab
	// stays in w1, so this op ships as a batch frame, and the live record now
	// holds "wkr2").
	submitBatch(t, mgr, epoch, &leapmuxv1.OpBatch{
		BatchId: "edit",
		Ops: []*leapmuxv1.CrdtOp{{OpId: "op-edit", Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "wkr2"},
		}}}},
	})

	delta := resumeAsW1Only(t, mgr, cursor, epoch)

	// The materialized frame for tA must carry BATCH-1-era state (worker_id
	// "wkr"), proving it read the persisted snapshot, not the current live
	// record (which holds "wkr2" after batch 2). The edit arrives separately as
	// a batch frame.
	var matWorkerID string
	var sawEditBatch bool
	for _, f := range delta.GetFrames() {
		if m := f.GetEntityMaterialized(); m != nil {
			if tab := m.GetTab(); tab != nil && tab.GetTabId() == "tA" {
				matWorkerID = tab.GetWorkerId().GetValue()
			}
		}
		if b := f.GetBatch(); b != nil && b.GetBatchId() == "edit" {
			sawEditBatch = true
		}
	}
	assert.Equal(t, "wkr", matWorkerID,
		"the materialized frame for the crossing-in batch must carry BATCH-ERA state (worker_id wkr), not the current live record (wkr2) — the snapshot is per-batch, not a live clone")
	assert.True(t, sawEditBatch,
		"the in-place edit (batch 2) must arrive as a separate batch frame so the client advances to wkr2 after installing the batch-era record")
}

// TestSubscribeWithACL_DeltaStableVisibilityOpsArriveAsBatchFrames pins that an
// in-place edit (an op on an entity that stays in the allowed workspace) still
// arrives as a batch frame — the resume path did not regress the common case
// while gaining the transition-replay frames.
func TestSubscribeWithACL_DeltaStableVisibilityOpsArriveAsBatchFrames(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 300_000)
	seedRootInternal(t, mgr, "w1", "root1")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a node under root1 (w1) and capture the cursor.
	submitNodePositionBatch(t, mgr, "seed", "root1", "initial")
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)

	// In-place position update on root1 (stays in w1 → stable visibility).
	submitNodePositionBatch(t, mgr, "tail", "root1", "updated")

	delta := resumeAsW1Only(t, mgr, cursor, epoch)

	kinds := deltaFrameKinds(t, delta)
	assert.Equal(t, []string{"batch", "end"}, kinds,
		"a stable-visibility in-place edit must arrive as exactly one batch frame (no transition frames)")
	if len(delta.GetFrames()) == 1 {
		b := delta.GetFrames()[0].GetBatch()
		require.NotNil(t, b)
		assert.Equal(t, "tail", b.GetBatchId())
	}
}

// TestSubscribeWithACL_DeltaOrdering_MaterializedBeforeBatchBeforeRemoved pins
// the per-batch frame-order contract: within a single tail batch that
// materializes one entity, ships a stable-visibility op on another, and removes
// a third, the frames must be ordered materialized → batch → removed. The client
// applies them in delta-order, so materialized-first ensures a batch op
// referencing a just-materialized entity resolves, and removed-last avoids the
// dropPendingByPredicate race (consumeEntityRemoved would otherwise drop ops
// this very batch confirms).
func TestSubscribeWithACL_DeltaOrdering_MaterializedBeforeBatchBeforeRemoved(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 400_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// tA in w2 (will move INTO w1 → materialized); tB in w1 (will move OUT to w2
	// → removed); tC in w1 (stays in w1 → its in-place edit is a stable-visibility
	// batch-frame op, so the delta actually emits all THREE frame kinds).
	submitBatch(t, mgr, epoch, addTabBatch(t, "seed-a", "tA", "root2", "wkr", "p1"))
	submitBatch(t, mgr, epoch, addTabBatch(t, "seed-b", "tB", "root1", "wkr", "p1"))
	submitBatch(t, mgr, epoch, addTabBatch(t, "seed-c", "tC", "root1", "wkr", "p1"))

	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)

	// One batch with three ops: move tA into w1 (materialized), edit tC in place
	// (stable-visibility → batch frame), and move tB out to w2 (removed).
	both := &leapmuxv1.OpBatch{
		BatchId: "both",
		Ops: []*leapmuxv1.CrdtOp{
			{OpId: "op-move-a", Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
			}}},
			{OpId: "op-edit-c", Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tC",
				Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "wkr2"},
			}}},
			{OpId: "op-move-b", Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tB",
				Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root2"},
			}}},
		},
	}
	submitBatch(t, mgr, epoch, both)

	delta := resumeAsW1Only(t, mgr, cursor, epoch)

	// The contract is the literal sequence materialized → batch → removed.
	// tC's in-place edit is stable-visibility (Pre=Post=w1, both allowed), so it
	// ships as the batch frame — without it the test would degenerate to a
	// two-kind assertion and never exercise the "batch in the middle" arm.
	kinds := deltaFrameKinds(t, delta)
	assert.Equal(t, []string{"materialized", "batch", "removed", "end"}, kinds,
		"a batch that materializes (tA into w1), edits in place (tC stable in w1), and removes (tB out to w2) must order materialized → batch → removed")
}

// TestSubscribeWithACL_DeltaInOutNetMaterialized proves per-batch commit
// ordering matters: an entity that crosses OUT of view in one tail batch then
// back IN in a later tail batch must end the delta VISIBLE (materialized), not
// removed. Replaying transitions per-batch in commit order is what makes this
// correct — a single current-state diff would collapse the two transitions and
// could misclassify.
func TestSubscribeWithACL_DeltaInOutNetMaterialized(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 500_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Tab starts in w1 (visible to a w1-only subscriber).
	submitBatch(t, mgr, epoch, addTabBatch(t, "seed", "tA", "root1", "wkr", "p1"))

	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)

	// Batch 1: move OUT to w2 (would be a removed transition in isolation).
	submitBatch(t, mgr, epoch, moveTabBatch("move-out", "tA", "root2"))
	// Batch 2: move back IN to w1 (materialized transition in isolation).
	submitBatch(t, mgr, epoch, moveTabBatch("move-in", "tA", "root1"))

	delta := resumeAsW1Only(t, mgr, cursor, epoch)

	// The net result across the two tail batches is "tab ends in w1 (visible)".
	// Per-batch replay emits a removed frame (batch 1) THEN a materialized frame
	// (batch 2); the client ends with the tab installed. Assert the LAST
	// transition frame for tA is materialized (the entity ends visible).
	var lastKindForTab string
	for _, f := range delta.GetFrames() {
		if m := f.GetEntityMaterialized(); m != nil {
			if tab := m.GetTab(); tab != nil && tab.GetTabId() == "tA" {
				lastKindForTab = "materialized"
			}
		}
		if r := f.GetEntityRemoved(); r != nil {
			if tab := r.GetTab(); tab != nil && tab.GetTabId() == "tA" {
				lastKindForTab = "removed"
			}
		}
	}
	assert.Equal(t, "materialized", lastKindForTab,
		"an entity that moves out then back in during the gap must end the delta visible (materialized after removed in commit order)")
}

// TestSubscribeWithACL_DeltaMaxHlcAdvancesOnTransitionFrameAtHlc pins that the
// delta's max_hlc is advanced by a transition frame's at_hlc, not only by
// visible batch-op canonical HLCs. The transition frame (materialized/removed)
// carries the batch's last canonical HLC as its at_hlc; when a tail batch emits
// ONLY transition frames (every stable-visibility op is filtered out but an
// entity crossed the visibility boundary), the client must still adopt that
// at_hlc as its watermark so the next resume is strictly-after this batch.
// Before #357 the resume path could not emit transition frames, so this case
// was impossible; now it is the load-bearing max_hlc source for a
// transition-only tail batch.
func TestSubscribeWithACL_DeltaMaxHlcAdvancesOnTransitionFrameAtHlc(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 600_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Tab starts in w1 (visible to a w1-only subscriber).
	submitBatch(t, mgr, epoch, addTabBatch(t, "seed", "tA", "root1", "wkr", "p1"))

	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)

	// Move the tab OUT to w2. The move op's entity resolves to w2 (disallowed
	// for a w1-only subscriber), so the batch frame is filtered out — the ONLY
	// frame this tail batch emits for the subscriber is a `removed` frame whose
	// at_hlc is the batch's last canonical HLC. The delta's max_hlc must be
	// that at_hlc, not the cursor (nothing newer-than-the-cursor would otherwise
	// be visible).
	submitBatch(t, mgr, epoch, moveTabBatch("move-out", "tA", "root2"))
	moveResult := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, moveResult)
	// Sanity: the move actually advanced the live max past the cursor.
	require.True(t, crdt.HLCCmp(moveResult, cursor) > 0, "the move must have advanced the live max")

	delta := resumeAsW1Only(t, mgr, cursor, epoch)

	// The delta must contain exactly one removed frame (no batch frame: the
	// move op was filtered out).
	var frameKinds []string
	for _, f := range delta.GetFrames() {
		switch f.GetEvent().(type) {
		case *leapmuxv1.WatchUserEvent_EntityRemoved:
			frameKinds = append(frameKinds, "removed")
		case *leapmuxv1.WatchUserEvent_Batch:
			frameKinds = append(frameKinds, "batch")
		case *leapmuxv1.WatchUserEvent_EntityMaterialized:
			frameKinds = append(frameKinds, "materialized")
		}
	}
	assert.Equal(t, []string{"removed"}, frameKinds,
		"a tail batch whose only visible effect is a cross-out must emit exactly one removed frame")

	// The delta's max_hlc must be the removed frame's at_hlc (the batch's last
	// canonical HLC), which is strictly greater than the cursor. If the resume
	// path failed to advance max_hlc from a transition frame's at_hlc, the
	// watermark would be stuck at the cursor and the next resume would replay
	// this batch.
	assert.True(t, crdt.HLCCmp(delta.GetMaxHlc(), cursor) > 0,
		"delta max_hlc must advance past the cursor on a transition-only tail batch (driven by the removed frame's at_hlc)")
	assert.Equal(t, moveResult.GetPhysical(), delta.GetMaxHlc().GetPhysical(),
		"delta max_hlc must equal the batch's last canonical HLC (the removed frame's at_hlc)")
}

// TestSubscribeWithACL_WorkspaceDeletedDuringGap_EmitsRemoval closes the last
// #357 ghost class.
//
// #357's fix made a resume replay the same pre/post visibility rule a live
// broadcast applies -- but only for entity MOVES. A workspace DELETED during the
// gap slipped through, because the replay evaluated both sides of every
// transition against the ACL as it stands NOW: the workspace is gone from the
// allowed set, its tombstone batch is pinned {Pre: wsID, Post: wsID}, so both
// sides read invisible. No ops shipped, no EntityRemoved was emitted, and the
// client kept that workspace's nodes and tabs forever -- and, since the
// cross-refresh checkpoint landed, wrote them to disk so a reload kept them too.
// The live path never had the bug only because contractSubscribersForWorkspace
// runs AFTER the tombstone broadcast.
//
// The delta must now evaluate the PRE side against the cursor-era ACL, so the
// deletion arrives as a becoming-hidden transition the client can act on.
func TestSubscribeWithACL_WorkspaceDeletedDuringGap_EmitsRemoval(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "doomed", "doomed-root")

	// The client's cursor is minted while BOTH workspaces are visible, so its
	// confirmedState holds the doomed workspace's entities.
	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// The workspace is deleted while the client is offline: its record is
	// tombstoned out of state and it leaves the ACL.
	tombstoneWorkspaceInternal(t, mgr, "doomed", "doomed-root")

	// Reconnect. resolve() reflects the post-delete ACL, exactly as
	// ListAccessibleWorkspaces would (it filters is_deleted = 0).
	resolveSurviving := func() (map[string]bool, error) { return map[string]bool{"w1": true}, nil }
	sub := &crdt.Subscriber{UserID: "user-1", Send: (&captureSubscriber{}).send}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, cursor, epoch, resolveSurviving)
	require.NoError(t, err)
	require.Equal(t, crdt.SubscribeDelta, out.Mode(), "the cursor is inside the window; this must resume")
	defer out.Unsub()()

	// The client must be told to drop the deleted workspace's contents. Before
	// the fix the delta carried no frame naming them at all.
	var removedNodes []string
	for _, f := range out.Delta().GetFrames() {
		if er := f.GetEntityRemoved(); er != nil {
			if n := er.GetNodeId(); n != "" {
				removedNodes = append(removedNodes, n)
			}
		}
	}
	assert.Contains(t, removedNodes, "doomed-root",
		"a workspace deleted during the gap must arrive as a becoming-hidden transition, or its entities ghost forever")
}

// tombstoneWorkspaceInternal deletes a workspace the way the lifecycle path
// does: the subtree's nodes are tombstoned and the WorkspaceContentsRecord entry
// is dropped, in one atomic batch (both are hub-only ops, hence SubmitInternal).
func tombstoneWorkspaceInternal(t *testing.T, mgr *crdt.Manager, workspaceID, rootID string) {
	t.Helper()
	res, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{{BatchId: "tombstone-" + workspaceID, Ops: []*leapmuxv1.CrdtOp{
			{
				OpId: "tombstone-node-" + rootID,
				Body: &leapmuxv1.CrdtOp_TombstoneNode{TombstoneNode: &leapmuxv1.TombstoneNodeOp{NodeId: rootID}},
			},
			{
				OpId: "tombstone-ws-" + workspaceID,
				Body: &leapmuxv1.CrdtOp_TombstoneWorkspace{TombstoneWorkspace: &leapmuxv1.TombstoneWorkspaceOp{WorkspaceId: workspaceID}},
			},
		}}},
	})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.NotNil(t, res[0].GetCommitted(), "workspace tombstone should commit; got %v", res[0])
}

// TestSubscribeWithACL_NarrowedWorkspaceIsNotTreatedAsDeparted is the guard on
// the cursor-era widening: it must fire for workspaces that DEPARTED, and only
// those.
//
// The pre-side widening asks "was this visible when the cursor was minted?" and
// answers it from state, because a workspace leaves an owner's set by exactly
// one route -- deletion, which tombstones its record. A workspace the subscriber
// merely narrowed away with workspace_ids is still very much alive in state, so
// it must NOT be treated as departed: doing so would emit EntityRemoved for
// entities the client never subscribed to, and (worse) would make ops in a
// still-live workspace read as a becoming-hidden transition.
func TestSubscribeWithACL_NarrowedWorkspaceIsNotTreatedAsDeparted(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")

	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Post-cursor traffic in w2, which is alive but outside this subscription.
	submitNodePositionBatch(t, mgr, "w2-batch", "root2", "w2op")

	resolveOnlyW1 := func() (map[string]bool, error) { return map[string]bool{"w1": true}, nil }
	sub := &crdt.Subscriber{
		UserID:                "user-1",
		Send:                  (&captureSubscriber{}).send,
		RequestedWorkspaceIDs: map[string]bool{"w1": true},
	}
	out, err := mgr.SubscribeWithACL(context.Background(), sub, cursor, epoch, resolveOnlyW1)
	require.NoError(t, err)
	require.Equal(t, crdt.SubscribeDelta, out.Mode())
	defer out.Unsub()()

	for _, f := range out.Delta().GetFrames() {
		assert.Nil(t, f.GetEntityRemoved(),
			"a live workspace outside the subscription is narrowed away, not departed; it must not produce evictions")
	}
}

// materializedTabIDs collects the tab ids delivered as EntityMaterialized
// frames, so the "did the client actually receive this record" assertions read
// directly.
func materializedTabIDs(delta *leapmuxv1.ResumeDelta) []string {
	var out []string
	for _, f := range delta.GetFrames() {
		if em := f.GetEntityMaterialized(); em != nil {
			if tab := em.GetTab(); tab != nil {
				out = append(out, tab.GetTabId())
			}
		}
	}
	return out
}

// TestSubscribeWithACL_EntityBornInGapCreatedWorkspace_Materializes covers the
// case the workspace-level cursor-era widening gets wrong.
//
// `departed` answers "could the subscriber see workspace W when its cursor was
// minted?" by checking that W is referenced as a Pre, is not allowed now, and is
// gone from state. A workspace created AND deleted entirely inside the gap
// satisfies all three -- it is absent for the same reason a deleted one is --
// so the pre side reads VISIBLE for a workspace the client demonstrably never
// saw. An entity born in that workspace and then moved somewhere visible then
// classifies as stably-visible and ships as raw ops, landing on a record the
// client does not have: applySetTabRegister lazy-creates a partial TabRecord
// carrying only the fields those ops happened to touch. That is exactly the
// class EntityMaterialized exists to prevent (#357).
//
// The per-entity pre side fixes it: the tab's FIRST sighting is its creation,
// whose post side is invisible, so the client is recorded as not holding it and
// the later escape reads as becoming-visible.
func TestSubscribeWithACL_EntityBornInGapCreatedWorkspace_Materializes(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 240_000)
	seedRootInternal(t, mgr, "w1", "root1")

	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// --- everything below happens while the client is offline ---
	// A workspace is created, a tab is born inside it, the tab escapes into the
	// subscriber's visible workspace, and the scratch workspace is deleted.
	seedRootInternal(t, mgr, "ephemeral", "eph-root")
	submitBatch(t, mgr, epoch, addTabBatch(t, "born-hidden", "tEph", "eph-root", "wkr", "p1"))
	submitBatch(t, mgr, epoch, moveTabBatch("escape", "tEph", "root1"))
	tombstoneWorkspaceInternal(t, mgr, "ephemeral", "eph-root")

	delta := resumeAsW1Only(t, mgr, cursor, epoch)

	assert.Contains(t, materializedTabIDs(delta), "tEph",
		"a tab the client never held must arrive as a full record, not as raw ops onto a record it lacks")
}

// TestSubscribeWithACL_EntityBornInGapDepartedWorkspace_Materializes is the
// sibling of the case above, and shows the defect is not really about when the
// workspace was born.
//
// Here the workspace pre-dates the cursor and IS genuinely departed (deleted
// during the gap), so the workspace-level answer "the client could see it at
// cursor time" is correct. It is still the wrong answer for this ENTITY: the tab
// was created inside that workspace AFTER the cursor, so the client never held
// it either. The pre side is a per-entity question, and a per-workspace
// approximation gets it wrong from both directions.
func TestSubscribeWithACL_EntityBornInGapDepartedWorkspace_Materializes(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 250_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "doomed", "doomed-root")

	submitNodePositionBatch(t, mgr, "cursor-batch", "root1", "c0")
	cursor := crdt.HLCClone(mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc())
	require.NotNil(t, cursor)
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// --- offline ---
	submitBatch(t, mgr, epoch, addTabBatch(t, "born-doomed", "tDoomed", "doomed-root", "wkr", "p1"))
	submitBatch(t, mgr, epoch, moveTabBatch("escape", "tDoomed", "root1"))
	tombstoneWorkspaceInternal(t, mgr, "doomed", "doomed-root")

	delta := resumeAsW1Only(t, mgr, cursor, epoch)

	assert.Contains(t, materializedTabIDs(delta), "tDoomed",
		"a tab born inside a departed workspace during the gap was never held by the client either")
}
