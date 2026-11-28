package cmd

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
)

// runTileSplit emits the SplitTile batch (T flips LEAF -> SPLIT, two new leaf
// children created, tabs on T migrate to childA).
func runTileSplit(inv *cli.Invocation) (any, error) {
	direction := stringFlag(inv, "direction")
	dirEnum, ok := parseSplitDirection(direction)
	if !ok {
		return nil, cli.Usagef("--direction must be horizontal|vertical")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, _, tileID, err := s.openTile(inv.Arg("tile"))
	if err != nil {
		return nil, err
	}
	kind := cc.bs.State.GetNodes()[tileID].GetKind().GetValue()
	if kind != leapmuxv1.NodeKind_NODE_KIND_LEAF && kind != leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED {
		return nil, cli.Usagef("tile %s is a %s node; only leaf tiles can be split", tileID, kindLabel(kind))
	}
	childA := id.Generate()
	childB := id.Generate()
	posA := lexorank.First()
	posB := lexorank.After(posA)
	ops := []*leapmuxv1.CrdtOp{
		opSetNodeKind(cc.bs, tileID, leapmuxv1.NodeKind_NODE_KIND_SPLIT),
		opSetNodeDirection(cc.bs, tileID, dirEnum),
		opSetNodeRatios(cc.bs, tileID, []float64{0.5, 0.5}),
		opSetNodeKind(cc.bs, childA, leapmuxv1.NodeKind_NODE_KIND_LEAF),
		opSetNodeParentID(cc.bs, childA, tileID),
		opSetNodePosition(cc.bs, childA, posA),
		opSetNodeKind(cc.bs, childB, leapmuxv1.NodeKind_NODE_KIND_LEAF),
		opSetNodeParentID(cc.bs, childB, tileID),
		opSetNodePosition(cc.bs, childB, posB),
	}
	// Migrate tabs from T to childA.
	for _, t := range crdt.TabsOnTile(cc.bs.State, tileID) {
		ops = append(ops, opSetTabTileID(cc.bs, t.TabType, t.TabID, childA))
	}
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	// Output names reflect what the user gets back from the split:
	//   split_tile_id                   — the original tile, now a SPLIT
	//                                     parent node (no longer renders
	//                                     as a leaf).
	//   leaf_tile_with_existing_tabs_id — the new leaf that holds the
	//                                     original tabs (positioned
	//                                     first: left for a vertical
	//                                     divider, top for a horizontal).
	//   leaf_tile_with_empty_tabs_id    — the new empty leaf (positioned
	//                                     second: right for a vertical
	//                                     divider, bottom for a
	//                                     horizontal).
	return tileSplitResult{
		SplitTileID:                tileID,
		LeafTileWithExistingTabsID: childA,
		LeafTileWithEmptyTabsID:    childB,
	}, nil
}

// tileSplitResult is the result of `tile split`.
type tileSplitResult struct {
	SplitTileID                string `json:"split_tile_id"`
	LeafTileWithExistingTabsID string `json:"leaf_tile_with_existing_tabs_id"`
	LeafTileWithEmptyTabsID    string `json:"leaf_tile_with_empty_tabs_id"`
}

// QuietLines prints the newly created empty leaf tile ID so callers can pass
// `T=$(leapmux tile split self -q)` directly into `agent open --tile`.
func (r tileSplitResult) QuietLines() []string {
	if r.LeafTileWithEmptyTabsID == "" {
		return nil
	}
	return []string{r.LeafTileWithEmptyTabsID}
}

// runTileClose closes a tile. Behaviour depends on the target kind and the
// user's policy choice:
//
//	leaf, no tabs        → close (no flag needed).
//	leaf, has tabs       → require --with-tabs <close|move>.
//	                       close: tombstone tabs, then close.
//	                       move:  migrate tabs to the heir tile, then
//	                              close. (Heir = first leaf in the
//	                              adjacent sibling subtree, matching
//	                              the frontend's CloseTileDialog.)
//	SPLIT or GRID node   → require --recursive (and --with-tabs if
//	                       the subtree carries any live tab). With
//	                       --recursive the whole subtree is closed in
//	                       one batch; without it, the operation is
//	                       rejected with a hint pointing at the
//	                       leaf-by-leaf path (or `tile remove-grid`
//	                       for grids).
//
// Tombstoning the calling tab's tile kills its PTY, so `guardLayoutClose`
// rejects that case unless --allow-self is supplied.
func runTileClose(inv *cli.Invocation) (any, error) {
	allowSelf := boolFlag(inv, allowSelfFlag)
	allowBusy := boolFlag(inv, "allow-busy")
	withTabs := stringFlag(inv, "with-tabs")
	recursive := boolFlag(inv, "recursive")
	policy, err := parseWithTabsPolicy(withTabs)
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, _, tileID, err := s.openTile(inv.Arg("tile"))
	if err != nil {
		return nil, err
	}

	// Argument-validation phase: reject targets the command can't
	// operate on at all (grid node, grid cell, non-leaf without
	// --recursive). These checks run before any policy / safety
	// decision so the user gets the most actionable error first.
	tileRec := cc.bs.State.GetNodes()[tileID]
	kind := tileRec.GetKind().GetValue()
	isLeaf := kind == leapmuxv1.NodeKind_NODE_KIND_LEAF || kind == leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED

	// Grids have their own verb. Even with --recursive, route the user
	// to `tile remove-grid` so there's exactly one way to remove a
	// grid (close-leaves-first via `tile close --recursive` would
	// produce different visual outcomes for root vs non-root grids,
	// and `tile remove-grid --with-tabs` handles both uniformly).
	if kind == leapmuxv1.NodeKind_NODE_KIND_GRID {
		return nil, cli.Usagef("tile %s is a grid", tileID).
			WithHint(`run "leapmux tile remove-grid %s" to remove it, with --with-tabs close or move when the grid has tabs`, tileID)
	}

	if !isLeaf && !recursive {
		return nil, cli.Usagef("tile %s is a %s node, not a leaf; use `tile close --recursive` to cascade-close the subtree",
			tileID, kindLabel(kind))
	}

	// Reject closing a grid cell directly. Tombstoning a single cell
	// leaves a renderer-synthesised empty placeholder in the grid
	// (renderTree.ts `__empty_<grid>_<idx>`) that isn't addressable in
	// the CRDT — `agent open --tile` can't target it, so the slot is
	// effectively unusable. Grid shape is fixed at make-grid time;
	// shrinking it isn't supported, so closing a single cell has no
	// sensible interpretation. Users who want to clear the cell
	// should close its tabs individually; users who want to remove
	// the grid should use `tile remove-grid`.
	if parentID, ok := isGridCell(cc.bs.State, tileID); ok {
		return nil, cli.Usagef("tile %s is a cell of grid %s; closing one cell would leave a hole that nothing can fill", tileID, parentID).
			WithHint(`close the tabs on the cell with "leapmux tab close", or remove the whole grid with "leapmux tile remove-grid %s"`, parentID)
	}

	_, enclosingRootID, _ := nodeRootFromState(cc.bs.State, tileID)

	var tabsAffected []crdt.TabRef
	if recursive {
		tabsAffected = crdt.LiveTabsInSubtree(cc.bs.State, tileID)
	} else {
		tabsAffected = crdt.TabsOnTile(cc.bs.State, tileID)
	}

	// Policy-decision phase: a tile with live tabs needs an explicit
	// --with-tabs choice (mirrors the frontend's CloseTileDialog).
	// This runs BEFORE the self-target guard so a user who hasn't
	// chosen a policy yet doesn't get a `--allow-self`-shaped error for a
	// problem they haven't reached — they'd just hit the same wall
	// again after adding --allow-self.
	if len(tabsAffected) > 0 && policy == withTabsUnspecified {
		return nil, cli.Usagef("tile %s has %d live tab(s); pass --with-tabs=close to tombstone them or --with-tabs=move to migrate them to the heir tile",
			tileID, len(tabsAffected))
	}

	// Safety phase: refuse to tear down the calling tab's own PTY
	// unless --allow-self. Skipped for --with-tabs=move because that path
	// migrates the tab's tile_id before tombstoning its old tile, so
	// the tab (and its PTY) survive.
	if policy != withTabsMove {
		if err := guardLayoutClose("tile "+tileID, tabsAffected, allowSelf); err != nil {
			return nil, err
		}
		// Running work and dirty git checkouts, for the same reason and in the
		// same place. Skipped for --with-tabs=move as well: migrating a tab
		// keeps its agent, PTY, and worktree tabs alive.
		if err := guardBulkCloseTabs(cc, tabsAffected, allowBusy); err != nil {
			return nil, err
		}
	}

	var ops []*leapmuxv1.CrdtOp
	heirID := ""
	switch {
	case recursive:
		// Cascade: tombstone (or migrate-and-tombstone) the whole subtree.
		// Workspace and floating-window roots are protected by
		// root_node_protected, so closing an enclosing root flips it back to
		// LEAF instead of tombstoning it.
		keepRoot := tileID == enclosingRootID
		if policy == withTabsMove && len(tabsAffected) > 0 {
			if keepRoot {
				heirID = tileID
			} else {
				heirID = crdt.FindHeirTileID(cc.bs.State, tileID, enclosingRootID)
				if heirID == "" {
					return nil, cli.Usagef("no heir tile available for the tabs on %s; pass --with-tabs=close instead", tileID)
				}
			}
		}
		ops = buildCloseSubtreeOps(cc.bs, tileID, heirID, keepRoot)
		if keepRoot {
			ops = append(ops, opSetNodeKind(cc.bs, tileID, leapmuxv1.NodeKind_NODE_KIND_LEAF))
		}

	case policy == withTabsMove && len(tabsAffected) > 0:
		// Leaf with tabs to migrate. When the parent 2-child SPLIT collapses
		// into an ancestor leaf, buildCloseTileOpsWithHeir redirects tabs onto
		// that surviving ancestor instead of the tombstoned sibling.
		heirID = crdt.FindHeirTileID(cc.bs.State, tileID, enclosingRootID)
		if heirID == "" {
			return nil, cli.Usagef("no heir tile available for tabs on %s; pass --with-tabs=close instead", tileID)
		}
		ops = buildCloseTileOpsWithHeir(cc.bs, tileID, heirID)
		if collapse, ok := findInverseSplitCollapse(cc.bs.State, crdt.LiveChildrenByParent(cc.bs.State), tileID); ok && heirID == collapse.siblingID {
			heirID = collapse.destID
		}

	default:
		// Leaf with tabs to close (withTabsClose) or no tabs: close the tile
		// and tombstone any tabs on it once inside buildCloseTileOpsWithHeir.
		ops = buildCloseTileOpsWithHeir(cc.bs, tileID, "")
	}

	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	out := tileCloseResult{
		TileID: tileID,
	}
	switch policy {
	case withTabsClose:
		out.TabsClosed = len(tabsAffected)
	case withTabsMove:
		out.TabsMoved = len(tabsAffected)
		out.HeirTileID = heirID
	default:
		// withTabsUnspecified means the flag was absent, which handlers reject
		// when tabs exist, so the zero counts above stand.
	}
	return out, nil
}

type tileCloseResult struct {
	TileID     string `json:"tile_id"`
	TabsClosed int    `json:"tabs_closed"`
	TabsMoved  int    `json:"tabs_moved"`
	HeirTileID string `json:"heir_tile_id,omitempty"`
}

func (r tileCloseResult) QuietLines() []string { return []string{r.TileID} }

type tileMakeGridResult struct {
	GridTileID                 string   `json:"grid_tile_id"`
	Rows                       int      `json:"rows"`
	Cols                       int      `json:"cols"`
	LeafTileIDs                []string `json:"leaf_tile_ids"`
	LeafTileWithExistingTabsID string   `json:"leaf_tile_with_existing_tabs_id"`
	TabsMoved                  int      `json:"tabs_moved"`
}

func (r tileMakeGridResult) QuietLines() []string { return []string{r.GridTileID} }

type tileRemoveGridResult struct {
	GridTileID string `json:"grid_tile_id"`
	TabsClosed int    `json:"tabs_closed"`
	TabsMoved  int    `json:"tabs_moved"`
	LeafTileID string `json:"leaf_tile_id,omitempty"`
}

func (r tileRemoveGridResult) QuietLines() []string {
	if r.LeafTileID != "" {
		return []string{r.LeafTileID}
	}
	return []string{r.GridTileID}
}

// withTabsPolicy is the parsed form of `--with-tabs`. Unspecified
// means the user didn't pass the flag; handlers reject that case
// when the operation would otherwise silently destroy or move tabs.
type withTabsPolicy int

const (
	withTabsUnspecified withTabsPolicy = iota
	withTabsClose
	withTabsMove
)

// withTabsPolicyMap is the constrained --with-tabs mapping.
var withTabsPolicyMap = map[string]withTabsPolicy{
	"close": withTabsClose,
	"move":  withTabsMove,
}

// parseWithTabsPolicy maps the flag string to a withTabsPolicy.
// Empty string is allowed and decodes to unspecified — the handler
// decides whether that's an error based on the target's tab count.
func parseWithTabsPolicy(s string) (withTabsPolicy, error) {
	if s == "" {
		return withTabsUnspecified, nil
	}
	v, ok := withTabsPolicyMap[s]
	if !ok {
		return withTabsUnspecified, cli.Usagef(`--with-tabs must be "close" or "move"; got %q`, s)
	}
	return v, nil
}

// isGridCell reports whether nodeID specifies a live leaf whose parent
// is a live GRID. Returns the parent grid's id when true so the
// caller can identify it in the error message. Used by `tile close` to
// refuse closing a single grid cell (which would leave an unusable
// placeholder in the grid).
func isGridCell(state *leapmuxv1.UserMaterialized, nodeID string) (gridID string, ok bool) {
	rec := state.GetNodes()[nodeID]
	if rec == nil || !crdt.HLCIsZero(rec.GetTombstoneAt()) {
		return "", false
	}
	kind := rec.GetKind().GetValue()
	if kind != leapmuxv1.NodeKind_NODE_KIND_LEAF && kind != leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED {
		return "", false
	}
	parentID := rec.GetParentId()
	if parentID == "" {
		return "", false
	}
	parent := state.GetNodes()[parentID]
	if parent == nil || !crdt.HLCIsZero(parent.GetTombstoneAt()) {
		return "", false
	}
	if parent.GetKind().GetValue() != leapmuxv1.NodeKind_NODE_KIND_GRID {
		return "", false
	}
	return parentID, true
}

// appendMigrateTabs appends SetTabTileId and SetTabPosition ops to move each
// tab onto destTileID starting at startPos, returning the updated slice and
// the next available LexoRank position.
func appendMigrateTabs(ops []*leapmuxv1.CrdtOp, bs *CRDTBootstrap, tabs []crdt.TabRef, destTileID, startPos string) ([]*leapmuxv1.CrdtOp, string) {
	pos := startPos
	for _, t := range tabs {
		ops = append(ops,
			opSetTabTileID(bs, t.TabType, t.TabID, destTileID),
			opSetTabPosition(bs, t.TabType, t.TabID, pos),
		)
		pos = lexorank.After(pos)
	}
	return ops, pos
}

// nextPositionOnTile returns the LexoRank position strictly after the last
// live tab on tileID, or lexorank.First() when tileID has no live tabs with
// a non-empty position.
func nextPositionOnTile(state *leapmuxv1.UserMaterialized, tileID string) string {
	maxPos := ""
	for _, t := range state.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if t.GetTileId().GetValue() != tileID {
			continue
		}
		if p := t.GetPosition().GetValue(); p > maxPos {
			maxPos = p
		}
	}
	if maxPos == "" {
		return lexorank.First()
	}
	return lexorank.After(maxPos)
}

// liveTabsByTile groups all live tabs in state by tile_id in a single O(T)
// pass, sorting each tile's tabs by LexoRank position with tab_id as tiebreak.
func liveTabsByTile(state *leapmuxv1.UserMaterialized) map[string][]crdt.TabRef {
	type tabEntry struct {
		ref crdt.TabRef
		pos string
	}
	grouped := map[string][]tabEntry{}
	for _, t := range state.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		tileID := t.GetTileId().GetValue()
		if tileID == "" {
			continue
		}
		grouped[tileID] = append(grouped[tileID], tabEntry{
			ref: crdt.TabRef{TabType: t.GetTabType(), TabID: t.GetTabId()},
			pos: t.GetPosition().GetValue(),
		})
	}
	out := make(map[string][]crdt.TabRef, len(grouped))
	for tileID, entries := range grouped {
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].pos != entries[j].pos {
				return entries[i].pos < entries[j].pos
			}
			return entries[i].ref.TabID < entries[j].ref.TabID
		})
		refs := make([]crdt.TabRef, len(entries))
		for i, e := range entries {
			refs[i] = e.ref
		}
		out[tileID] = refs
	}
	return out
}

// inverseSplitCollapse describes the ancestor chain that collapses into destID
// when closing a leaf child of a 2-child SPLIT whose surviving sibling is also
// a leaf.
type inverseSplitCollapse struct {
	siblingID     string
	destID        string
	intermediates []string
}

// findInverseSplitCollapse inspects tileID's parent SPLIT chain using the
// prebuilt live-children adjacency and reports whether closing tileID collapses
// its parent SPLIT (and any single-child ancestor SPLITs) back into a LEAF.
func findInverseSplitCollapse(state *leapmuxv1.UserMaterialized, children map[string][]string, tileID string) (inverseSplitCollapse, bool) {
	closing := state.GetNodes()[tileID]
	if closing == nil {
		return inverseSplitCollapse{}, false
	}
	parentID := closing.GetParentId()
	if parentID == "" {
		return inverseSplitCollapse{}, false
	}
	parent := state.GetNodes()[parentID]
	if parent == nil || parent.GetKind().GetValue() != leapmuxv1.NodeKind_NODE_KIND_SPLIT || !crdt.HLCIsZero(parent.GetTombstoneAt()) {
		return inverseSplitCollapse{}, false
	}
	liveChildren := children[parentID]
	if len(liveChildren) != 2 {
		return inverseSplitCollapse{}, false
	}
	var siblingID string
	for _, childID := range liveChildren {
		if childID != tileID {
			siblingID = childID
		}
	}
	if siblingID == "" {
		return inverseSplitCollapse{}, false
	}
	// Inverse-split only fires when the sibling is itself a leaf. If the
	// sibling is a SPLIT or GRID, tombstoning it would orphan every descendant
	// and tab under it.
	sibling := state.GetNodes()[siblingID]
	sibKind := sibling.GetKind().GetValue()
	if sibKind != leapmuxv1.NodeKind_NODE_KIND_LEAF && sibKind != leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED {
		return inverseSplitCollapse{}, false
	}

	// Walk upward to collapse any chain of single-child ancestor SPLITs in the
	// same batch. Workspace and floating-window roots are safe targets for the
	// kind flip (root_node_id is set-once, while kind is a normal LWW register).
	destID := parentID
	var intermediates []string
	curNode := parent
	for {
		upID := curNode.GetParentId()
		if upID == "" {
			break
		}
		up := state.GetNodes()[upID]
		if up == nil || up.GetKind().GetValue() != leapmuxv1.NodeKind_NODE_KIND_SPLIT || !crdt.HLCIsZero(up.GetTombstoneAt()) {
			break
		}
		upLive := children[upID]
		if len(upLive) != 1 || upLive[0] != destID {
			break
		}
		intermediates = append(intermediates, destID)
		destID = upID
		curNode = up
	}

	return inverseSplitCollapse{
		siblingID:     siblingID,
		destID:        destID,
		intermediates: intermediates,
	}, true
}

// buildCloseTileOps produces the ops to close one tile and tombstone its tabs,
// collapsing a redundant 2-child SPLIT parent when applicable.
func buildCloseTileOps(bs *CRDTBootstrap, tileID string) []*leapmuxv1.CrdtOp {
	return buildCloseTileOpsWithHeir(bs, tileID, "")
}

// buildCloseTileOpsWithHeir produces the ops to close one tile, mirroring the
// frontend's `tileOps.ts:buildCloseTileOps`. When heirID is empty, live tabs
// on tileID are tombstoned once. When heirID is non-empty, live tabs on tileID
// migrate onto heirID (or onto the surviving collapsed ancestor destID when
// inverse-split tombstones heirID == siblingID), appended strictly after any
// existing tabs on the destination tile.
//
// Callers must guarantee tileID is not a registered workspace or
// floating-window root — the validator rejects root tombstones with
// `root_node_protected`. The inverse-split itself never tombstones the parent,
// only flips its kind, so a SPLIT root that lands in the collapse path is safe.
func buildCloseTileOpsWithHeir(bs *CRDTBootstrap, tileID, heirID string) []*leapmuxv1.CrdtOp {
	state := bs.State
	children := crdt.LiveChildrenByParent(state)
	collapse, hasCollapse := findInverseSplitCollapse(state, children, tileID)
	tileTabs := crdt.TabsOnTile(state, tileID)

	ops := []*leapmuxv1.CrdtOp{}
	switch {
	case heirID == "":
		for _, t := range tileTabs {
			ops = append(ops, opTombstoneTab(bs, t.TabType, t.TabID))
		}
	case hasCollapse && heirID == collapse.siblingID:
		// Both siblingID's tabs and tileID's tabs migrate onto the surviving
		// collapsed ancestor destID; emit them together below so sibling tabs
		// keep the front positions and tileID's tabs append strictly after.
	default:
		ops, _ = appendMigrateTabs(ops, bs, tileTabs, heirID, nextPositionOnTile(state, heirID))
	}
	ops = append(ops, opTombstoneNode(bs, tileID))

	if !hasCollapse {
		return ops
	}

	sibTabs := crdt.TabsOnTile(state, collapse.siblingID)
	var nextPos string
	ops, nextPos = appendMigrateTabs(ops, bs, sibTabs, collapse.destID, lexorank.First())
	if heirID == collapse.siblingID && len(tileTabs) > 0 {
		ops, _ = appendMigrateTabs(ops, bs, tileTabs, collapse.destID, nextPos)
	}
	ops = append(ops, opTombstoneNode(bs, collapse.siblingID))
	for _, mid := range collapse.intermediates {
		ops = append(ops, opTombstoneNode(bs, mid))
	}
	ops = append(ops, opSetNodeKind(bs, collapse.destID, leapmuxv1.NodeKind_NODE_KIND_LEAF))
	return ops
}

// buildCloseSubtreeOps walks tileID's descendants leaves-first and
// emits ops that either tombstone or migrate every tab to migrateTo
// (empty string -> tombstone). Descendant nodes are always
// tombstoned. The root node (tileID itself) is tombstoned by default
// — set keepRoot=true to leave its NodeRecord alive, which the
// workspace-root case needs (`root_node_id` is set-once, so callers
// flip the root's kind to LEAF instead of tombstoning it). Mirrors
// the frontend's `tileOps.ts:buildCloseSubtreeOps` so cascade closes
// converge with the UI's removeGrid / floating-window-disposal flows.
func buildCloseSubtreeOps(bs *CRDTBootstrap, tileID, migrateTo string, keepRoot bool) []*leapmuxv1.CrdtOp {
	state := bs.State
	descendants := crdt.DescendantsLeavesFirst(state, tileID)
	tabsByTile := liveTabsByTile(state)
	ops := []*leapmuxv1.CrdtOp{}
	migratedPos := lexorank.First()
	if migrateTo != "" {
		migratedPos = nextPositionOnTile(state, migrateTo)
	}
	emitTabOps := func(tabs []crdt.TabRef) {
		if migrateTo != "" {
			ops, migratedPos = appendMigrateTabs(ops, bs, tabs, migrateTo, migratedPos)
			return
		}
		for _, t := range tabs {
			ops = append(ops, opTombstoneTab(bs, t.TabType, t.TabID))
		}
	}
	for _, nid := range descendants {
		if nid == tileID {
			continue
		}
		emitTabOps(tabsByTile[nid])
		ops = append(ops, opTombstoneNode(bs, nid))
	}
	emitTabOps(tabsByTile[tileID])
	if !keepRoot {
		ops = append(ops, opTombstoneNode(bs, tileID))
	}
	return ops
}

// runTileMakeGrid converts a leaf tile into a rows*cols grid. Tabs
// on the leaf are migrated to cell[0,0] (the top-left cell) so the
// user never loses work to a layout restructure. This mirrors the
// frontend, which silently migrates without prompting — there's no
// MakeGridDialog because there's nothing destructive to confirm. If
// a user wants a fresh empty grid, they can close the tabs first
// with `tab close` and then run make-grid.
//
// Make-grid only operates on a leaf — flipping an existing SPLIT or
// GRID would orphan its subtree under a new GRID parent and confuse
// the projection. Use `tile close --recursive` (for splits) or
// `tile remove-grid` (for grids) first if the user wants to start
// fresh.
func runTileMakeGrid(inv *cli.Invocation) (any, error) {
	maxDim := int(crdt.MaxGridDimension)
	rows, err := inv.Command.Flags().GetInt("rows")
	if err != nil {
		return nil, err
	}
	cols, err := inv.Command.Flags().GetInt("cols")
	if err != nil {
		return nil, err
	}
	if rows < 1 || rows > maxDim || cols < 1 || cols > maxDim {
		return nil, cli.Usagef("--rows and --cols must be in [1, %d]", maxDim)
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, _, tileID, err := s.openTile(inv.Arg("tile"))
	if err != nil {
		return nil, err
	}

	kind := cc.bs.State.GetNodes()[tileID].GetKind().GetValue()
	if kind != leapmuxv1.NodeKind_NODE_KIND_LEAF && kind != leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED {
		return nil, cli.Usagef("tile %s is a %s node; only leaf tiles can be converted to a grid", tileID, kindLabel(kind))
	}

	tabsOnLeaf := crdt.TabsOnTile(cc.bs.State, tileID)
	rowRatios := crdt.EqualRatios(rows)
	colRatios := crdt.EqualRatios(cols)
	ops := []*leapmuxv1.CrdtOp{
		opSetNodeKind(cc.bs, tileID, leapmuxv1.NodeKind_NODE_KIND_GRID),
		opSetNodeRows(cc.bs, tileID, uint32(rows)),
		opSetNodeCols(cc.bs, tileID, uint32(cols)),
		opSetNodeRowRatios(cc.bs, tileID, rowRatios),
		opSetNodeColRatios(cc.bs, tileID, colRatios),
	}
	cellTileIDs := make([]string, 0, rows*cols)
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			cellID := id.Generate()
			cellTileIDs = append(cellTileIDs, cellID)
			ops = append(ops,
				opSetNodeKind(cc.bs, cellID, leapmuxv1.NodeKind_NODE_KIND_LEAF),
				opSetNodeParentID(cc.bs, cellID, tileID),
				opSetNodePosition(cc.bs, cellID, crdt.GridCellPosition(r, c)),
			)
		}
	}
	dest := cellTileIDs[0]
	ops, _ = appendMigrateTabs(ops, cc.bs, tabsOnLeaf, dest, lexorank.First())

	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}

	return tileMakeGridResult{
		GridTileID:                 tileID,
		Rows:                       rows,
		Cols:                       cols,
		LeafTileIDs:                cellTileIDs,
		LeafTileWithExistingTabsID: dest,
		TabsMoved:                  len(tabsOnLeaf),
	}, nil
}

// runTileRemoveGrid removes a grid. The behaviour depends on --with-tabs:
//
//	close (default when no tabs in the subtree)
//	     Tombstone every descendant + the grid node. For a root grid
//	     the kind is flipped from GRID to LEAF instead of tombstoning
//	     the root (the validator rejects `root_node_protected`); the
//	     workspace ends up as a bare root LEAF.
//
//	move
//	     Mirror the frontend's `replaceGridWithLeaf`: tombstone every
//	     cell in the subtree and migrate every tab onto a fresh leaf
//	     created in the grid's slot (inheriting its parent_id +
//	     position). The grid node itself is tombstoned.
//	     Root grid: flip the root's kind back to LEAF in place and
//	     migrate every tab onto it.
//
// Required when the grid's subtree has at least one live tab, so we
// never silently destroy or relocate tabs.
//
// Tombstoning a descendant tile kills the PTYs on it, so `guardLayoutClose`
// rejects the operation when the calling tab sits anywhere inside the doomed
// subtree unless --allow-self is supplied.
func runTileRemoveGrid(inv *cli.Invocation) (any, error) {
	allowSelf := boolFlag(inv, allowSelfFlag)
	allowBusy := boolFlag(inv, "allow-busy")
	withTabs := stringFlag(inv, "with-tabs")
	policy, err := parseWithTabsPolicy(withTabs)
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, _, gridID, err := s.openTile(inv.Arg("tile"))
	if err != nil {
		return nil, err
	}
	// Argument-validation phase: target must be a GRID node.
	if err := preflightTileKind(cc.bs.State, gridID, leapmuxv1.NodeKind_NODE_KIND_GRID, "remove-grid", "GRID", ""); err != nil {
		return nil, err
	}
	gridRec := cc.bs.State.GetNodes()[gridID]
	tabsInSubtree := crdt.LiveTabsInSubtree(cc.bs.State, gridID)

	// Policy-decision phase: a grid carrying live tabs needs an
	// explicit --with-tabs choice (mirrors the frontend's
	// CloseGridDialog). This runs BEFORE the self-target guard so a
	// user who hasn't picked a policy yet doesn't get a
	// `--allow-self`-shaped error for a problem they haven't reached —
	// they'd just hit the same wall again after adding --allow-self.
	if len(tabsInSubtree) > 0 && policy == withTabsUnspecified {
		return nil, cli.Usagef("grid %s has %d live tab(s); pass --with-tabs=close to tombstone them or --with-tabs=move to collapse the grid back to a single tile with the tabs preserved",
			gridID, len(tabsInSubtree))
	}

	// Safety phase: refuse to tear down the calling tab's own PTY
	// unless --allow-self. Skipped for --with-tabs=move because that path
	// migrates every tab in the subtree onto the replacement leaf
	// (or the root for a root grid), so the tab and its PTY survive.
	if policy != withTabsMove {
		if err := guardLayoutClose("grid "+gridID, tabsInSubtree, allowSelf); err != nil {
			return nil, err
		}
		if err := guardBulkCloseTabs(cc, tabsInSubtree, allowBusy); err != nil {
			return nil, err
		}
	}

	parentID := gridRec.GetParentId()
	isRoot := parentID == ""
	ops := []*leapmuxv1.CrdtOp{}
	out := tileRemoveGridResult{
		GridTileID: gridID,
	}

	switch policy {
	case withTabsMove:
		if isRoot {
			// Root grid: keep the root NodeRecord alive (root_node_id
			// is set-once) and flip its kind back to LEAF; tabs land
			// on the root tile itself.
			ops = append(ops, buildCloseSubtreeOps(cc.bs, gridID, gridID, true)...)
			ops = append(ops, opSetNodeKind(cc.bs, gridID, leapmuxv1.NodeKind_NODE_KIND_LEAF))
			out.LeafTileID = gridID
		} else {
			// Non-root grid: mint a fresh leaf in the grid's slot
			// (inherits parent_id + position), migrate all tabs onto
			// it, tombstone the grid + descendants.
			newLeaf := id.Generate()
			ops = append(ops, buildCloseSubtreeOps(cc.bs, gridID, newLeaf, false)...)
			pos := gridRec.GetPosition().GetValue()
			if pos == "" {
				pos = lexorank.First()
			}
			ops = append(ops,
				opSetNodeKind(cc.bs, newLeaf, leapmuxv1.NodeKind_NODE_KIND_LEAF),
				opSetNodeParentID(cc.bs, newLeaf, parentID),
				opSetNodePosition(cc.bs, newLeaf, pos),
			)
			out.LeafTileID = newLeaf
		}
		out.TabsMoved = len(tabsInSubtree)

	default:
		// withTabsClose, or no tabs to worry about.
		ops = append(ops, buildCloseSubtreeOps(cc.bs, gridID, "", isRoot)...)
		if isRoot {
			// Root: flip the (still-alive) NodeRecord's kind to LEAF.
			ops = append(ops, opSetNodeKind(cc.bs, gridID, leapmuxv1.NodeKind_NODE_KIND_LEAF))
		}
		out.TabsClosed = len(tabsInSubtree)
	}

	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return out, nil
}

type tileSwapResult struct {
	TileA       string `json:"tile_a"`
	TileB       string `json:"tile_b"`
	WorkspaceID string `json:"workspace_id"`
	ReissuedIDs bool   `json:"reissued_ids"`
}

func (r tileSwapResult) QuietLines() []string { return []string{r.TileA, r.TileB} }

// runTileSwap swaps two tiles in the same workspace in one atomic CRDT batch.
// Sibling tiles swap Position registers in place (ReissuedIDs=false). Tiles
// under different parents cannot rewrite parent_id (set-once in Hub CRDT), so
// each live subtree is cloned into the other's slot with fresh node IDs, live
// tabs migrate onto the cloned leaves preserving their existing positions, and
// the old subtrees are tombstoned leaves-first (ReissuedIDs=true).
func runTileSwap(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, wsA, tileA, err := s.openTile(inv.Arg("tile-a"))
	if err != nil {
		return nil, err
	}
	tileB, err := s.resolveSecondTile(cc.bs.State, wsA, inv.Arg("tile-b"))
	if err != nil {
		return nil, err
	}
	if tileA == tileB {
		return nil, cli.Usagef("cannot swap tile %s with itself", tileA)
	}

	var lastErr error
	for attempt := range layoutSetMaxAttempts {
		if attempt > 0 {
			bs, berr := crdtBootstrap(cc.ctx, cc.c, []string{wsA})
			if berr != nil {
				return nil, berr
			}
			cc.bs = bs
		}
		if err := preflightTile(cc.bs.State, wsA, tileA); err != nil {
			return nil, err
		}
		if err := preflightTile(cc.bs.State, wsA, tileB); err != nil {
			return nil, err
		}
		nodeA := cc.bs.State.GetNodes()[tileA]
		nodeB := cc.bs.State.GetNodes()[tileB]
		parentA := nodeA.GetParentId()
		parentB := nodeB.GetParentId()
		if parentA == "" || parentB == "" {
			return nil, cli.Newf(cli.CodeConflict, "cannot swap a workspace root tile")
		}
		if isTileAncestor(cc.bs.State, tileA, tileB) || isTileAncestor(cc.bs.State, tileB, tileA) {
			return nil, cli.Newf(cli.CodeConflict, "cannot swap a tile with one of its descendants")
		}
		posA := nodeA.GetPosition().GetValue()
		posB := nodeB.GetPosition().GetValue()
		if parentA == parentB {
			ops := []*leapmuxv1.CrdtOp{
				opSetNodePosition(cc.bs, tileA, posB),
				opSetNodePosition(cc.bs, tileB, posA),
			}
			if err := cc.submitOps(ops); err != nil {
				return nil, err
			}
			return tileSwapResult{
				TileA:       tileA,
				TileB:       tileB,
				WorkspaceID: wsA,
				ReissuedIDs: false,
			}, nil
		}

		ops, newTileA, newTileB := buildCrossParentTileSwapOps(cc.bs, tileA, tileB, parentA, posA, parentB, posB)
		committed, reason, submitErr := cc.trySubmitOps(ops)
		if committed {
			return tileSwapResult{
				TileA:       newTileA,
				TileB:       newTileB,
				WorkspaceID: wsA,
				ReissuedIDs: true,
			}, nil
		}
		lastErr = submitErr
		if reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_PLACEMENT_INVALID {
			return nil, submitErr
		}
	}
	return nil, cli.Newf(cli.CodeConflict, "another client changed the layout during each of %d attempts (last hub error: %v)", layoutSetMaxAttempts, lastErr).
		WithHint("run the same tile swap again")
}

// buildCrossParentTileSwapOps builds one atomic CRDT batch that clones tileA
// into (parentB, posB), clones tileB into (parentA, posA), migrates every live
// tab on cloned leaves while preserving each tab's existing position, and
// tombstones both old subtrees leaves-first.
func buildCrossParentTileSwapOps(
	bs *CRDTBootstrap,
	tileA, tileB, parentA, posA, parentB, posB string,
) (ops []*leapmuxv1.CrdtOp, newTileA, newTileB string) {
	childrenByParent := crdt.LiveChildrenByParent(bs.State)
	tabsByTile := liveTabsByTile(bs.State)

	newTileA, opsA := cloneLiveSubtreeOps(bs, tileA, parentB, posB, childrenByParent, tabsByTile)
	newTileB, opsB := cloneLiveSubtreeOps(bs, tileB, parentA, posA, childrenByParent, tabsByTile)
	ops = make([]*leapmuxv1.CrdtOp, 0, len(opsA)+len(opsB)+2)
	ops = append(ops, opsA...)
	ops = append(ops, opsB...)

	for _, nid := range crdt.DescendantsLeavesFirst(bs.State, tileA) {
		ops = append(ops, opTombstoneNode(bs, nid))
	}
	for _, nid := range crdt.DescendantsLeavesFirst(bs.State, tileB) {
		ops = append(ops, opTombstoneNode(bs, nid))
	}
	return ops, newTileA, newTileB
}

// cloneLiveSubtreeOps copies the raw live CRDT subtree rooted at srcID under
// newParentID at newPos with fresh node IDs, preserving exact container
// registers and child positions verbatim, and migrates every live tab on cloned
// leaves via SetTabRegister(tile_id) without overwriting tab positions.
func cloneLiveSubtreeOps(
	bs *CRDTBootstrap,
	srcID, newParentID, newPos string,
	childrenByParent map[string][]string,
	tabsByTile map[string][]crdt.TabRef,
) (newID string, ops []*leapmuxv1.CrdtOp) {
	rec := bs.State.GetNodes()[srcID]
	newID = id.Generate()
	kind := rec.GetKind().GetValue()
	if kind == leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED {
		kind = leapmuxv1.NodeKind_NODE_KIND_LEAF
	}

	ops = append(ops,
		opSetNodeKind(bs, newID, kind),
		opSetNodeParentID(bs, newID, newParentID),
	)
	if newPos != "" {
		ops = append(ops, opSetNodePosition(bs, newID, newPos))
	}

	switch kind {
	case leapmuxv1.NodeKind_NODE_KIND_SPLIT:
		if dir := rec.GetDirection().GetValue(); dir != leapmuxv1.SplitDirection_SPLIT_DIRECTION_UNSPECIFIED {
			ops = append(ops, opSetNodeDirection(bs, newID, dir))
		}
		if ratios := rec.GetRatios().GetValue().GetValues(); len(ratios) > 0 {
			ops = append(ops, opSetNodeRatios(bs, newID, append([]float64(nil), ratios...)))
		}
	case leapmuxv1.NodeKind_NODE_KIND_GRID:
		if rows := rec.GetRows().GetValue(); rows > 0 {
			ops = append(ops, opSetNodeRows(bs, newID, rows))
		}
		if cols := rec.GetCols().GetValue(); cols > 0 {
			ops = append(ops, opSetNodeCols(bs, newID, cols))
		}
		if rowRatios := rec.GetRowRatios().GetValue().GetValues(); len(rowRatios) > 0 {
			ops = append(ops, opSetNodeRowRatios(bs, newID, append([]float64(nil), rowRatios...)))
		}
		if colRatios := rec.GetColRatios().GetValue().GetValues(); len(colRatios) > 0 {
			ops = append(ops, opSetNodeColRatios(bs, newID, append([]float64(nil), colRatios...)))
		}
	case leapmuxv1.NodeKind_NODE_KIND_LEAF:
		for _, t := range tabsByTile[srcID] {
			ops = append(ops, opSetTabTileID(bs, t.TabType, t.TabID, newID))
		}
	case leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED:
		// Normalized to NODE_KIND_LEAF above.
	}

	childIDs := append([]string(nil), childrenByParent[srcID]...)
	sort.SliceStable(childIDs, func(i, j int) bool {
		posI := bs.State.GetNodes()[childIDs[i]].GetPosition().GetValue()
		posJ := bs.State.GetNodes()[childIDs[j]].GetPosition().GetValue()
		if posI != posJ {
			return posI < posJ
		}
		return childIDs[i] < childIDs[j]
	})
	for _, childID := range childIDs {
		childRec := bs.State.GetNodes()[childID]
		if childRec == nil || !crdt.HLCIsZero(childRec.GetTombstoneAt()) {
			continue
		}
		_, childOps := cloneLiveSubtreeOps(bs, childID, newID, childRec.GetPosition().GetValue(), childrenByParent, tabsByTile)
		ops = append(ops, childOps...)
	}
	return newID, ops
}

// resolveSecondTile resolves raw against the already-bootstrapped workspace
// state when raw is an explicit ID or a tile prefix in wsID, falling back to
// s.resolveOne for "self", cross-workspace IDs, or short/invalid inputs.
func (s *session) resolveSecondTile(state *leapmuxv1.UserMaterialized, wsID, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if exact := resolve.ExplicitID(resolve.KindTile, trimmed); exact != "" {
		return exact, nil
	}
	if trimmed != "" &&
		trimmed != resolve.SelfTarget &&
		!strings.HasPrefix(trimmed, "id:") &&
		!strings.HasPrefix(trimmed, "title:") &&
		!strings.HasPrefix(trimmed, "name:") &&
		len(trimmed) >= resolve.MinPrefixLength {
		tileID, ok, err := lookupTileInState(state, wsID, trimmed)
		if err != nil {
			return "", err
		}
		if ok {
			return tileID, nil
		}
	}
	target, err := s.resolveOne(resolve.KindTile, raw)
	if err != nil {
		return "", err
	}
	return target.ID, nil
}

func isTileAncestor(state *leapmuxv1.UserMaterialized, ancestor, descendant string) bool {
	seen := map[string]bool{}
	cur := descendant
	for cur != "" && !seen[cur] {
		seen[cur] = true
		n := state.GetNodes()[cur]
		if n == nil {
			return false
		}
		p := n.GetParentId()
		if p == ancestor {
			return true
		}
		cur = p
	}
	return false
}

func completeSwapTiles(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeTargetsOfKind(cmd, resolve.KindTile, toComplete, true)
}
