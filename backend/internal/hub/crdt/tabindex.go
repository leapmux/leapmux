package crdt

import (
	"context"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TabIndexRow is the value carried into BulkUpsert: one row of the derived tab
// index, which holds every owned tab and marks the ones that render.
//
// It deliberately carries NO owner. Every writer stamps the committing tenant
// itself (see service.txTabIndexWriter, which mints it once per transaction),
// so a per-row owner could only ever agree with that one or contradict it. The
// DELETE side is different and does keep an owner -- see TabKey.
//
// TabIndexRow must stay comparable: the diff detects a changed row with ==.
type TabIndexRow struct {
	WorkspaceID string
	TabType     leapmuxv1.TabType
	TabID       string
	WorkerID    string
	TileID      string
	Position    string
	// Rendered is true when the tab's tile is a live leaf reachable from a
	// registered root, that is, when Project lists the tab in RenderedTabs.
	Rendered bool
}

// TabIndexWriter is the subset of store operations the manager uses to
// keep workspace_tab_owned in sync with the CRDT state. The interface is
// narrow on purpose so tests can swap a fake without standing up a full store
// backend.
//
// Both operations are bulk: a typical commit only touches one or
// two tabs, but cross-workspace moves and projection-repair commits
// can produce dozens to thousands of upserts/deletes in a single
// diff. Per-row round-trips were the bottleneck before; the backends
// chunk the input internally to stay within their parameter limits.
type TabIndexWriter interface {
	BulkUpsert(ctx context.Context, rows []TabIndexRow) error
	BulkDelete(ctx context.Context, keys []TabKey) error
}

// IndexDiff is the minimal write plan for a single committed batch.
// Empty slices mean "no change". Upserts and Deletes never name the same tab
// id, so the order in which ApplyDiff writes them does not matter.
type IndexDiff struct {
	Upserts []TabIndexRow
	Deletes []TabKey
}

// TabKey identifies a tab inside a single user doc.
type TabKey struct {
	UserID string
	TabID  string
}

// DiffProjectionForBatch is the commit hot-path entry point: it
// returns the same IndexDiff DiffProjection would, but skips the
// per-tab chain walks for tabs the batch cannot possibly transition.
//
// The op classification mirrors the validator's tabPlacementCheck
// restriction (see batchHasStructuralOp): batches that don't tombstone
// nodes, flip a node Kind, register a workspace/floating-window root,
// or move a floating window across workspaces can only affect the tabs
// they explicitly name via Set/TombstoneTab ops. For those batches we
// re-project just the touched tabs; every other tab kept its prior
// placement and its index row is already correct.
//
// For structural batches (anything that can rewire a tab's chain or
// change tile-is-leaf-ness for many tabs at once) we fall back to the
// full Project + Diff path because the affected subtree can span any
// number of pre-existing tabs and tight scoping would re-create most
// of the per-batch cost we're trying to avoid.
func DiffProjectionForBatch(prev, next *leapmuxv1.UserCrdtState, batch []*leapmuxv1.CrdtOp) IndexDiff {
	if batchHasStructuralOp(batch) {
		return DiffProjection(Project(prev), Project(next))
	}
	touched := touchedTabIDs(batch)
	if len(touched) == 0 {
		return IndexDiff{}
	}
	prevRoots := registeredRoots(prev)
	nextRoots := registeredRoots(next)

	diff := IndexDiff{}
	for tabID := range touched {
		prevOwned, prevRendered := projectOneTab(prev, tabID, prevRoots)
		prevRow, prevOK := indexRowOf(prevOwned, prevRendered)
		nextRow, nextOK := indexRowOf(projectOneTab(next, tabID, nextRoots))

		switch {
		case !nextOK && prevOK:
			diff.Deletes = append(diff.Deletes, TabKey{UserID: prevOwned.UserID, TabID: prevOwned.TabID})
		case nextOK && (!prevOK || prevRow != nextRow):
			diff.Upserts = append(diff.Upserts, nextRow)
		}
	}
	return diff
}

// touchedTabIDs returns the set of tab ids any Set/TombstoneTab op in
// `batch` names. Other op kinds contribute nothing — for non-structural
// batches those are the only tab projections that can change.
func touchedTabIDs(batch []*leapmuxv1.CrdtOp) map[string]bool {
	out := map[string]bool{}
	for _, op := range batch {
		switch body := op.GetBody().(type) {
		case *leapmuxv1.CrdtOp_SetTabRegister:
			out[body.SetTabRegister.GetTabId()] = true
		case *leapmuxv1.CrdtOp_TombstoneTab:
			out[body.TombstoneTab.GetTabId()] = true
		case *leapmuxv1.CrdtOp_ReviveTab:
			// A revive re-materializes the tab into the index; include it so
			// its workspace_tab_owned row is re-upserted.
			out[body.ReviveTab.GetTab().GetTabId()] = true
		}
	}
	return out
}

// indexRowOf builds the index row of one projected tab. owned is nil when the
// tab projects to nothing, and then there is no row; rendered is non-nil
// exactly when the tab renders.
func indexRowOf(owned *OwnedTab, rendered *RenderedTab) (TabIndexRow, bool) {
	if owned == nil {
		return TabIndexRow{}, false
	}
	return indexRow(owned, rendered != nil), true
}

// indexRow is the one place that copies an owned tab's fields into its index
// row; rendered states whether the tab renders.
func indexRow(owned *OwnedTab, rendered bool) TabIndexRow {
	return TabIndexRow{
		WorkspaceID: owned.WorkspaceID,
		TabType:     owned.TabType,
		TabID:       owned.TabID,
		WorkerID:    owned.WorkerID,
		TileID:      owned.TileID,
		Position:    owned.Position,
		Rendered:    rendered,
	}
}

// DiffProjection returns the write plan to take the index from `prev`
// to `next`. The diff is computed from the projection (not the raw
// state), so projection-repair drops are reflected in the Rendered flag.
func DiffProjection(prev, next *Projection) IndexDiff {
	prevRows := indexRowsOf(prev)
	nextRows := indexRowsOf(next)

	diff := IndexDiff{}
	for id, entry := range nextRows {
		row := indexRow(entry.tab, entry.rendered)
		if previous, ok := prevRows[id]; !ok || indexRow(previous.tab, previous.rendered) != row {
			diff.Upserts = append(diff.Upserts, row)
		}
	}
	for id, previous := range prevRows {
		if _, ok := nextRows[id]; !ok {
			diff.Deletes = append(diff.Deletes, TabKey{UserID: previous.tab.UserID, TabID: id})
		}
	}
	return diff
}

// tabIndexEntry keeps an owned tab and its rendering flag together. The tab
// supplies both its comparable index row and the owner needed for deletion.
type tabIndexEntry struct {
	tab      *OwnedTab
	rendered bool
}

// indexRowsOf indexes the projection's owned tabs without copying their fields.
// Rendering membership comes from the same projection's RenderedTabs.
func indexRowsOf(p *Projection) map[string]tabIndexEntry {
	rendered := make(map[string]bool, len(p.RenderedTabs))
	for _, t := range p.RenderedTabs {
		rendered[t.TabID] = true
	}
	rows := make(map[string]tabIndexEntry, len(p.OwnedTabs))
	for _, t := range p.OwnedTabs {
		rows[t.TabID] = tabIndexEntry{tab: t, rendered: rendered[t.TabID]}
	}
	return rows
}

// ApplyDiff writes the diff to the tab index. Caller must ensure
// the writer is bound to the same DB transaction as the journal write.
// Each non-empty slice is forwarded as one bulk call, so a diff costs at
// most two DB round-trips (modulo backend-internal chunking) instead of one
// per row.
func ApplyDiff(ctx context.Context, w TabIndexWriter, diff IndexDiff) error {
	if len(diff.Upserts) > 0 {
		if err := w.BulkUpsert(ctx, diff.Upserts); err != nil {
			return fmt.Errorf("bulk upsert tabs (%d rows): %w", len(diff.Upserts), err)
		}
	}
	if len(diff.Deletes) > 0 {
		if err := w.BulkDelete(ctx, diff.Deletes); err != nil {
			return fmt.Errorf("bulk delete tabs (%d keys): %w", len(diff.Deletes), err)
		}
	}
	return nil
}
