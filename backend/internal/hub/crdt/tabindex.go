package crdt

import (
	"context"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TabIndexRow is the value carried into Upsert{Owned,Rendered}.
type TabIndexRow struct {
	OrgID       string
	WorkspaceID string
	TabType     leapmuxv1.TabType
	TabID       string
	WorkerID    string
	TileID      string
	Position    string
}

// TabIndexWriter is the subset of store operations the manager uses to
// keep workspace_tab_owned + workspace_tab_rendered in sync with the
// CRDT state. The interface is narrow on purpose so tests can swap a
// fake without standing up a full store backend.
//
// All four operations are bulk: a typical commit only touches one or
// two tabs, but cross-workspace moves and projection-repair commits
// can produce dozens to thousands of upserts/deletes in a single
// diff. Per-row round-trips were the bottleneck before; the backends
// chunk the input internally to stay within their parameter limits.
type TabIndexWriter interface {
	BulkUpsertOwned(ctx context.Context, rows []TabIndexRow) error
	BulkDeleteOwned(ctx context.Context, keys []TabKey) error
	BulkUpsertRendered(ctx context.Context, rows []TabIndexRow) error
	BulkDeleteRendered(ctx context.Context, keys []TabKey) error
}

// IndexDiff is the minimal write plan for a single committed batch.
// Empty slices mean "no change".
type IndexDiff struct {
	OwnedUpserts    []TabIndexRow
	OwnedDeletes    []TabKey
	RenderedUpserts []TabIndexRow
	RenderedDeletes []TabKey
}

// TabKey identifies a tab inside a single org doc.
type TabKey struct {
	OrgID string
	TabID string
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
// owned/rendered placement and its index rows are already correct.
//
// For structural batches (anything that can rewire a tab's chain or
// change tile-is-leaf-ness for many tabs at once) we fall back to the
// full Project + Diff path because the affected subtree can span any
// number of pre-existing tabs and tight scoping would re-create most
// of the per-batch cost we're trying to avoid.
func DiffProjectionForBatch(prev, next *leapmuxv1.OrgCrdtState, batch []*leapmuxv1.OrgOp) IndexDiff {
	if batchHasStructuralOp(batch) {
		return DiffProjection(ProjectOwnership(prev), ProjectOwnership(next))
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
		nextOwned, nextRendered := projectOneTab(next, tabID, nextRoots)

		switch {
		case nextOwned == nil && prevOwned != nil:
			diff.OwnedDeletes = append(diff.OwnedDeletes, TabKey{OrgID: prevOwned.OrgID, TabID: prevOwned.TabID})
		case nextOwned != nil && (prevOwned == nil || !rowEqual(prevOwned, nextOwned)):
			diff.OwnedUpserts = append(diff.OwnedUpserts, toRow(nextOwned))
		}

		switch {
		case nextRendered == nil && prevRendered != nil:
			diff.RenderedDeletes = append(diff.RenderedDeletes, TabKey{OrgID: prevRendered.OrgID, TabID: prevRendered.TabID})
		case nextRendered != nil && (prevRendered == nil || !rowEqual(prevRendered, nextRendered)):
			diff.RenderedUpserts = append(diff.RenderedUpserts, nextRendered.toIndexRow())
		}
	}
	return diff
}

// touchedTabIDs returns the set of tab ids any Set/TombstoneTab op in
// `batch` names. Other op kinds contribute nothing — for non-structural
// batches those are the only tab projections that can change.
func touchedTabIDs(batch []*leapmuxv1.OrgOp) map[string]bool {
	out := map[string]bool{}
	for _, op := range batch {
		switch body := op.GetBody().(type) {
		case *leapmuxv1.OrgOp_SetTabRegister:
			out[body.SetTabRegister.GetTabId()] = true
		case *leapmuxv1.OrgOp_TombstoneTab:
			out[body.TombstoneTab.GetTabId()] = true
		}
	}
	return out
}

func (t *RenderedTab) toIndexRow() TabIndexRow {
	return toRow(t)
}

// DiffProjection returns the write plan to take the index from `prev`
// to `next`. The diff is computed from the projection (not the raw
// state) so projection-repair drops are reflected in
// workspace_tab_rendered.
func DiffProjection(prev, next *Projection) IndexDiff {
	prevOwned := mapTabsByID(prev.OwnedTabs)
	nextOwned := mapTabsByID(next.OwnedTabs)
	prevRendered := mapTabsByID(prev.RenderedTabs)
	nextRendered := mapTabsByID(next.RenderedTabs)

	diff := IndexDiff{}

	for id, t := range nextOwned {
		if pt, ok := prevOwned[id]; !ok || !rowEqual(pt, t) {
			diff.OwnedUpserts = append(diff.OwnedUpserts, toRow(t))
		}
	}
	for id, t := range prevOwned {
		if _, ok := nextOwned[id]; !ok {
			diff.OwnedDeletes = append(diff.OwnedDeletes, TabKey{OrgID: t.OrgID, TabID: t.TabID})
		}
	}
	for id, t := range nextRendered {
		if pt, ok := prevRendered[id]; !ok || !rowEqual(pt, t) {
			diff.RenderedUpserts = append(diff.RenderedUpserts, toRow(t))
		}
	}
	for id, t := range prevRendered {
		if _, ok := nextRendered[id]; !ok {
			diff.RenderedDeletes = append(diff.RenderedDeletes, TabKey{OrgID: t.OrgID, TabID: t.TabID})
		}
	}
	return diff
}

func toRow(t *RenderedTab) TabIndexRow {
	return TabIndexRow{
		OrgID:       t.OrgID,
		WorkspaceID: t.WorkspaceID,
		TabType:     t.TabType,
		TabID:       t.TabID,
		WorkerID:    t.WorkerID,
		TileID:      t.TileID,
		Position:    t.Position,
	}
}

func mapTabsByID(tabs []*RenderedTab) map[string]*RenderedTab {
	out := make(map[string]*RenderedTab, len(tabs))
	for _, t := range tabs {
		out[t.TabID] = t
	}
	return out
}

func rowEqual(a, b *RenderedTab) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.OrgID == b.OrgID && a.WorkspaceID == b.WorkspaceID &&
		a.TabType == b.TabType && a.TabID == b.TabID &&
		a.WorkerID == b.WorkerID && a.TileID == b.TileID &&
		a.Position == b.Position
}

// ApplyDiff writes the diff to the index views. Caller must ensure
// the writer is bound to the same DB transaction as the journal write.
// Each non-empty slice is forwarded as one bulk call so a diff with N
// owned upserts + M rendered deletes costs at most four DB
// round-trips (modulo backend-internal chunking) instead of N+M.
func ApplyDiff(ctx context.Context, w TabIndexWriter, diff IndexDiff) error {
	if len(diff.OwnedUpserts) > 0 {
		if err := w.BulkUpsertOwned(ctx, diff.OwnedUpserts); err != nil {
			return fmt.Errorf("bulk upsert owned (%d rows): %w", len(diff.OwnedUpserts), err)
		}
	}
	if len(diff.OwnedDeletes) > 0 {
		if err := w.BulkDeleteOwned(ctx, diff.OwnedDeletes); err != nil {
			return fmt.Errorf("bulk delete owned (%d keys): %w", len(diff.OwnedDeletes), err)
		}
	}
	if len(diff.RenderedUpserts) > 0 {
		if err := w.BulkUpsertRendered(ctx, diff.RenderedUpserts); err != nil {
			return fmt.Errorf("bulk upsert rendered (%d rows): %w", len(diff.RenderedUpserts), err)
		}
	}
	if len(diff.RenderedDeletes) > 0 {
		if err := w.BulkDeleteRendered(ctx, diff.RenderedDeletes); err != nil {
			return fmt.Errorf("bulk delete rendered (%d keys): %w", len(diff.RenderedDeletes), err)
		}
	}
	return nil
}
