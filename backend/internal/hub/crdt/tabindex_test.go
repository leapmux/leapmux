package crdt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

func projectionWithTab(workspaceID, tabID, tileID, position string) *crdt.Projection {
	row := &crdt.RenderedTab{
		UserID:      "user-1",
		WorkspaceID: workspaceID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       tabID,
		WorkerID:    "w1",
		TileID:      tileID,
		Position:    position,
	}
	return &crdt.Projection{
		UserID:       "user-1",
		OwnedTabs:    []*crdt.RenderedTab{row},
		RenderedTabs: []*crdt.RenderedTab{row},
	}
}

// projectionWithUnrenderedTab is projectionWithTab for a tab that is owned but
// does not render: its tile is not a live leaf.
func projectionWithUnrenderedTab(workspaceID, tabID, tileID, position string) *crdt.Projection {
	p := projectionWithTab(workspaceID, tabID, tileID, position)
	p.RenderedTabs = nil
	return p
}

func TestDiffProjection_NewTab_GeneratesUpserts(t *testing.T) {
	prev := &crdt.Projection{UserID: "user-1"}
	next := projectionWithTab("ws1", "t1", "tile-A", "a")
	diff := crdt.DiffProjection(prev, next)
	require.Len(t, diff.Upserts, 1)
	assert.Empty(t, diff.Deletes)
	assert.Equal(t, "tile-A", diff.Upserts[0].TileID)
	assert.True(t, diff.Upserts[0].Rendered, "a tab in RenderedTabs is a rendered row")
}

func TestDiffProjection_OwnedOnlyNewTabIsOneUnrenderedUpsert(t *testing.T) {
	prev := &crdt.Projection{UserID: "user-1"}
	next := projectionWithUnrenderedTab("ws1", "t1", "split-1", "a")
	diff := crdt.DiffProjection(prev, next)
	require.Len(t, diff.Upserts, 1, "an owned tab is one row whether or not it renders")
	assert.Empty(t, diff.Deletes)
	assert.False(t, diff.Upserts[0].Rendered)
	assert.Equal(t, "t1", diff.Upserts[0].TabID)
}

func TestDiffProjection_RemovedTab_GeneratesDeletes(t *testing.T) {
	prev := projectionWithTab("ws1", "t1", "tile-A", "a")
	next := &crdt.Projection{UserID: "user-1"}
	diff := crdt.DiffProjection(prev, next)
	assert.Empty(t, diff.Upserts)
	require.Len(t, diff.Deletes, 1)
	assert.Equal(t, crdt.TabKey{UserID: "user-1", TabID: "t1"}, diff.Deletes[0])
}

func TestDiffProjection_MovedTab_GeneratesUpsertOnly(t *testing.T) {
	prev := projectionWithTab("ws1", "t1", "tile-A", "a")
	next := projectionWithTab("ws2", "t1", "tile-B", "a")
	diff := crdt.DiffProjection(prev, next)
	require.Len(t, diff.Upserts, 1, "moving the tab should yield an upsert, not delete+insert")
	assert.Empty(t, diff.Deletes)
	assert.Equal(t, "ws2", diff.Upserts[0].WorkspaceID)
	assert.Equal(t, "tile-B", diff.Upserts[0].TileID)
}

func TestDiffProjection_NoChange_NoOps(t *testing.T) {
	prev := projectionWithTab("ws1", "t1", "tile-A", "a")
	next := projectionWithTab("ws1", "t1", "tile-A", "a")
	diff := crdt.DiffProjection(prev, next)
	assert.Empty(t, diff.Upserts)
	assert.Empty(t, diff.Deletes)
}

func TestDiffProjectionSeparatesMixedTransitionsAndKeepsDeletionOwnership(t *testing.T) {
	unchanged := projectionWithTab("ws1", "unchanged", "tile-a", "a").OwnedTabs[0]
	flipped := projectionWithTab("ws1", "flipped", "tile-b", "b").OwnedTabs[0]
	removed := projectionWithTab("ws1", "removed", "tile-c", "c").OwnedTabs[0]
	added := projectionWithTab("ws2", "added", "tile-d", "d").OwnedTabs[0]
	previous := &crdt.Projection{
		UserID: "user-1", OwnedTabs: []*crdt.OwnedTab{unchanged, flipped, removed},
		RenderedTabs: []*crdt.RenderedTab{unchanged, flipped, removed},
	}
	next := &crdt.Projection{
		UserID: "user-1", OwnedTabs: []*crdt.OwnedTab{unchanged, flipped, added},
		RenderedTabs: []*crdt.RenderedTab{unchanged, added},
	}
	diff := crdt.DiffProjection(previous, next)
	assert.ElementsMatch(t, []crdt.TabIndexRow{
		{WorkspaceID: "ws1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "flipped", WorkerID: "w1", TileID: "tile-b", Position: "b", Rendered: false},
		{WorkspaceID: "ws2", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "added", WorkerID: "w1", TileID: "tile-d", Position: "d", Rendered: true},
	}, diff.Upserts)
	assert.Equal(t, []crdt.TabKey{{UserID: "user-1", TabID: "removed"}}, diff.Deletes)
	empty := crdt.DiffProjection(&crdt.Projection{}, &crdt.Projection{})
	assert.Empty(t, empty.Upserts)
	assert.Empty(t, empty.Deletes)
}

// A tab that stops (or starts) rendering keeps its owned row, so the only
// write is the flag: one upsert, never a delete.
func TestDiffProjection_RenderedFlipYieldsOneUpsert(t *testing.T) {
	rendered := projectionWithTab("ws1", "t1", "tile-A", "a")
	hidden := projectionWithUnrenderedTab("ws1", "t1", "tile-A", "a")

	for _, c := range []struct {
		name       string
		prev, next *crdt.Projection
		want       bool
	}{
		{"rendered to hidden", rendered, hidden, false},
		{"hidden to rendered", hidden, rendered, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			diff := crdt.DiffProjection(c.prev, c.next)
			require.Len(t, diff.Upserts, 1)
			assert.Empty(t, diff.Deletes)
			assert.Equal(t, c.want, diff.Upserts[0].Rendered)
			assert.Equal(t, "tile-A", diff.Upserts[0].TileID, "every other column is unchanged")
		})
	}
}

// recordingTabIndexWriter captures the bulk-call arguments ApplyDiff
// forwards. It records each call's slice as-is so the test can
// observe both batching (one call per non-empty phase) and the
// per-row contents.
type recordingTabIndexWriter struct {
	upserts [][]crdt.TabIndexRow
	deletes [][]crdt.TabKey

	// failOn names the bulk phase the writer should fail in. Empty
	// string means "never fail".
	failOn string
}

var errBulkInjected = errors.New("recording writer: injected failure")

func (w *recordingTabIndexWriter) BulkUpsert(_ context.Context, rows []crdt.TabIndexRow) error {
	if w.failOn == "upsert" {
		return errBulkInjected
	}
	w.upserts = append(w.upserts, append([]crdt.TabIndexRow(nil), rows...))
	return nil
}

func (w *recordingTabIndexWriter) BulkDelete(_ context.Context, keys []crdt.TabKey) error {
	if w.failOn == "delete" {
		return errBulkInjected
	}
	w.deletes = append(w.deletes, append([]crdt.TabKey(nil), keys...))
	return nil
}

func TestApplyDiff_OnePhasePerNonEmptySlice(t *testing.T) {
	diff := crdt.IndexDiff{
		Upserts: []crdt.TabIndexRow{
			{WorkspaceID: "ws", TabID: "t1", TileID: "tile-1", Rendered: true},
			{WorkspaceID: "ws", TabID: "t2", TileID: "tile-2"},
			{WorkspaceID: "ws", TabID: "t3", TileID: "tile-3", Rendered: true},
		},
		Deletes: []crdt.TabKey{
			{UserID: "user-1", TabID: "old-1"},
			{UserID: "user-1", TabID: "old-2"},
		},
	}
	w := &recordingTabIndexWriter{}
	require.NoError(t, crdt.ApplyDiff(context.Background(), w, diff))

	// Each non-empty phase must produce exactly one bulk call (proves
	// ApplyDiff does not loop per row).
	require.Len(t, w.upserts, 1)
	require.Len(t, w.deletes, 1)

	// The slice forwarded to the writer is the diff slice verbatim.
	assert.Equal(t, diff.Upserts, w.upserts[0])
	assert.Equal(t, diff.Deletes, w.deletes[0])
}

func TestApplyDiff_EmptyPhaseMakesNoCall(t *testing.T) {
	w := &recordingTabIndexWriter{}
	require.NoError(t, crdt.ApplyDiff(context.Background(), w, crdt.IndexDiff{
		Upserts: []crdt.TabIndexRow{{TabID: "t1"}},
	}))
	assert.Len(t, w.upserts, 1)
	assert.Empty(t, w.deletes, "an empty delete slice must not reach the writer")
}

func TestApplyDiff_EmptyDiff_NoCalls(t *testing.T) {
	w := &recordingTabIndexWriter{}
	require.NoError(t, crdt.ApplyDiff(context.Background(), w, crdt.IndexDiff{}))
	assert.Empty(t, w.upserts)
	assert.Empty(t, w.deletes)
}

func TestApplyDiff_PropagatesPhaseError(t *testing.T) {
	diff := crdt.IndexDiff{
		Upserts: []crdt.TabIndexRow{{TabID: "t1"}},
		Deletes: []crdt.TabKey{{UserID: "user-1", TabID: "old"}},
	}
	for _, c := range []struct {
		phase, message string
	}{
		{"upsert", "bulk upsert tabs (1 rows)"},
		{"delete", "bulk delete tabs (1 keys)"},
	} {
		t.Run(c.phase, func(t *testing.T) {
			w := &recordingTabIndexWriter{failOn: c.phase}
			err := crdt.ApplyDiff(context.Background(), w, diff)
			require.ErrorIs(t, err, errBulkInjected)
			// The error message must name the failed phase and the affected
			// row count so operators can correlate logs with diff size.
			assert.Contains(t, err.Error(), c.message)
		})
	}
}

// seedSplitRootWithLeafChild returns a workspace whose root1 is a SPLIT with
// one live LEAF child, childA, and tab tA placed on childA.
func seedSplitRootWithLeafChild(t *testing.T) *leapmuxv1.UserCrdtState {
	t.Helper()
	state := seedWorkspaceWithRoot("w1", "root1")
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
	}, hlcAt(2, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "childA",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
	}, hlcAt(2, 1, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "childA",
		Field:  &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: "root1"},
	}, hlcAt(2, 2, "a")))
	placeTab(t, state, "tA", "childA", 3)
	return state
}

// A Kind flip is a structural op, so the batch takes the full Project + Diff
// path. The tab's tile stops being a leaf, so the tab keeps its owned row and
// stops rendering: one upsert with Rendered false, and no delete.
func TestDiffProjectionForBatch_KindFlipToSplitUnrendersInOneUpsert(t *testing.T) {
	state := seedWorkspaceWithRoot("w1", "root1")
	placeTab(t, state, "tA", "root1", 2)
	prev := crdt.CloneState(state)
	require.Len(t, crdt.Project(prev).RenderedTabs, 1, "precondition: the tab renders on the leaf root")

	batch := []*leapmuxv1.CrdtOp{stamped(&leapmuxv1.SetNodeRegisterOp{
		NodeId: "root1",
		Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_SPLIT},
	}, hlcAt(3, 0, "a"))}
	next := crdt.CloneState(state)
	for _, op := range batch {
		crdt.Apply(next, op)
	}

	diff := crdt.DiffProjectionForBatch(prev, next, batch)
	require.Len(t, diff.Upserts, 1)
	assert.Empty(t, diff.Deletes, "an owned tab that stops rendering is not deleted")
	assert.Equal(t, "tA", diff.Upserts[0].TabID)
	assert.False(t, diff.Upserts[0].Rendered)
	assert.Equal(t, crdt.DiffProjection(crdt.Project(prev), crdt.Project(next)), diff,
		"the batch diff must equal the full projection diff")
}

// A retile is not a structural op, so the batch takes the per-tab path.
// Moving the tab from a leaf onto the SPLIT above it keeps the tab owned (its
// chain still reaches the root) and stops it rendering.
func TestDiffProjectionForBatch_RetileToNonLeafUnrendersInOneUpsert(t *testing.T) {
	state := seedSplitRootWithLeafChild(t)
	prev := crdt.CloneState(state)
	require.Len(t, crdt.Project(prev).RenderedTabs, 1, "precondition: the tab renders on the leaf child")

	batch := []*leapmuxv1.CrdtOp{stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(4, 0, "a"))}
	next := crdt.CloneState(state)
	for _, op := range batch {
		crdt.Apply(next, op)
	}

	diff := crdt.DiffProjectionForBatch(prev, next, batch)
	require.Len(t, diff.Upserts, 1)
	assert.Empty(t, diff.Deletes)
	assert.Equal(t, "root1", diff.Upserts[0].TileID)
	assert.False(t, diff.Upserts[0].Rendered)
	assert.Equal(t, crdt.DiffProjection(crdt.Project(prev), crdt.Project(next)), diff,
		"the per-tab path must agree with the full projection diff")
}

// TestDiffProjection_ReviveBatchReAddsTabRow pins the commit hot-path
// behavior: a revive + re-placement batch on a CLOSED tab must re-upsert the
// tab's workspace_tab_owned row. `touchedTabIDs` includes ReviveTab, so
// DiffProjectionForBatch re-projects the revived tab id; since the tab goes
// from tombstoned (no row) to live-on-a-leaf (a rendered row), the diff emits
// one upsert with Rendered set rather than leaving the row absent.
//
// The batch shape mirrors emitReviveTab (frontend/src/stores/tabOps.ts):
// revive + SetTabTile + SetTabPosition + SetTabWorker in one batch. The revive
// clears the tombstone; the companion Set ops repopulate the registers the
// post-batch completeness check demands.
func TestDiffProjection_ReviveBatchReAddsTabRow(t *testing.T) {
	// 1. Seed a workspace with a leaf root, place a tab on it, then close it.
	state := seedWorkspaceWithRoot("w1", "root1")
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
		Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
	}, hlcAt(10, 0, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
		Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "wkr1"},
	}, hlcAt(10, 1, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.SetTabRegisterOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
		Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "p1"},
	}, hlcAt(10, 2, "a")))
	crdt.Apply(state, stamped(&leapmuxv1.TombstoneTabOp{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
	}, hlcAt(20, 0, "a")))
	require.False(t, crdt.HLCIsZero(state.GetTabs()["tA"].GetTombstoneAt()),
		"precondition: the tab must be tombstoned (closed) before the revive")

	// 2. Snapshot prev (the published generation), then apply the revive +
	// re-placement batch to a working copy -- mirrors the manager's
	// commit path (CloneStateForBatch → Apply each op → DiffProjectionForBatch).
	prev := crdt.CloneState(state)
	working := crdt.CloneState(state)

	batch := []*leapmuxv1.CrdtOp{
		stamped(&leapmuxv1.ReviveTabOp{
			Tab: &leapmuxv1.TabRef{TabId: "tA", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
		}, hlcAt(30, 0, "a")),
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: "root1"},
		}, hlcAt(30, 1, "a")),
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_Position{Position: "p2"},
		}, hlcAt(30, 2, "a")),
		stamped(&leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tA",
			Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "wkr2"},
		}, hlcAt(30, 3, "a")),
	}
	for _, op := range batch {
		crdt.Apply(working, op)
	}
	require.True(t, crdt.HLCIsZero(working.GetTabs()["tA"].GetTombstoneAt()),
		"postcondition: the revive cleared the tombstone")

	// 3. Compute the projection diff the journal/manager would write.
	diff := crdt.DiffProjectionForBatch(prev, working, batch)

	// 4. The revived tab's row must be re-added, and it renders.
	require.Len(t, diff.Upserts, 1,
		"revived tab must re-upsert into workspace_tab_owned")
	assert.Equal(t, "tA", diff.Upserts[0].TabID)
	assert.Equal(t, "root1", diff.Upserts[0].TileID,
		"the row carries the re-placed tile_id")
	assert.Equal(t, "wkr2", diff.Upserts[0].WorkerID,
		"the row carries the re-placed worker_id")
	assert.True(t, diff.Upserts[0].Rendered,
		"the revived tab sits on a live leaf, so its row renders")

	// A revive re-materializes a live row, so nothing is deleted.
	assert.Empty(t, diff.Deletes, "revive adds a row, it does not delete one")
}
