package cmd

import (
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
)

// RunTileSplit emits the 9-op SplitTile batch (T flips LEAF -> SPLIT,
// two new leaf children created, tabs on T migrate to childA).
func RunTileSplit(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, direction string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	fs.StringVar(&direction, "direction", "vertical", `divider orientation: "vertical" (|) puts the new pane to the right; "horizontal" (-) puts it below`)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	dirEnum, ok := parseSplitDirection(direction)
	if !ok {
		return remote.EmitError("invalid_request", "--direction must be horizontal|vertical")
	}
	cc, _, tileID, err := openTileCRDTCall(hub, in)
	if err != nil {
		return err
	}
	defer cc.close()
	childA := id.Generate()
	childB := id.Generate()
	posA := lexorank.First()
	posB := lexorank.After(posA)
	ops := []*leapmuxv1.OrgOp{
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
		return err
	}
	// Output names reflect what the user gets back from the split:
	//   split_tile_id          — the original tile, now a SPLIT
	//                                   parent node (no longer renders
	//                                   as a leaf).
	//   leaf_tile_with_existing_tabs_id — the new leaf that holds the
	//                                   original tabs (positioned
	//                                   first: left for a vertical
	//                                   divider, top for a horizontal).
	//   leaf_tile_with_empty_tabs_id  — the new empty leaf (positioned
	//                                   second: right for a vertical
	//                                   divider, bottom for a
	//                                   horizontal).
	return remote.EmitData(map[string]any{
		"split_tile_id":                   tileID,
		"leaf_tile_with_existing_tabs_id": childA,
		"leaf_tile_with_empty_tabs_id":    childB,
	})
}

// RunTileClose closes a tile. Behaviour depends on the target kind
// and the user's policy choice:
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
// Tombstoning the calling tab's tile kills its PTY, so
// `guardTileClose` rejects that case unless --force is supplied.
func RunTileClose(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, withTabs string
	var force, recursive bool
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	fs.BoolVar(&force, "force", false, "close even if the calling tab sits on the target tile (would kill the caller's own PTY)")
	fs.StringVar(&withTabs, "with-tabs", "", `policy for live tabs on the closing tile (or subtree): "close" tombstones them; "move" migrates them to the heir tile. Required when the target has tabs.`)
	fs.BoolVar(&recursive, "recursive", false, "required to close a SPLIT or GRID tile; cascades the close to every descendant in one batch")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	policy, err := parseWithTabsPolicy(withTabs)
	if err != nil {
		return err
	}
	cc, workspaceID, tileID, err := openTileCRDTCall(hub, in)
	if err != nil {
		return err
	}
	defer cc.close()

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
		return remote.EmitError("invalid_request",
			fmt.Sprintf("tile %s is a GRID node; use `tile remove-grid --tile-id %s` (with --with-tabs=close|move when the grid has tabs) to remove it",
				tileID, tileID))
	}

	if !isLeaf && !recursive {
		return remote.EmitError("invalid_request",
			fmt.Sprintf("tile %s is a %s node, not a leaf; use `tile close --recursive` to cascade-close the subtree",
				tileID, kindLabel(kind)))
	}

	// Reject closing a grid cell directly. Tombstoning a single cell
	// leaves a renderer-synthesised empty placeholder in the grid
	// (renderTree.ts `__empty_<grid>_<idx>`) that isn't addressable in
	// the CRDT — `tab open --tile-id` can't target it, so the slot is
	// effectively unusable. Grid shape is fixed at make-grid time;
	// shrinking it isn't supported, so closing a single cell has no
	// sensible interpretation. Users who want to clear the cell
	// should close its tabs individually; users who want to remove
	// the grid should use `tile remove-grid`.
	if parentID, ok := isGridCell(cc.bs.State, tileID); ok {
		return remote.EmitError("invalid_request",
			fmt.Sprintf("tile %s is a cell of grid %s; closing a single cell leaves an unusable hole. Close the tabs on the cell with `tab close`, or use `tile remove-grid --tile-id %s` to remove the whole grid",
				tileID, parentID, parentID))
	}

	rootNodeID := cc.bs.State.GetWorkspaces()[workspaceID].GetRootNodeId()

	var tabsAffected []crdt.TabRef
	if recursive {
		tabsAffected = crdt.LiveTabsInSubtree(cc.bs.State, tileID)
	} else {
		tabsAffected = crdt.TabsOnTile(cc.bs.State, tileID)
	}

	// Policy-decision phase: a tile with live tabs needs an explicit
	// --with-tabs choice (mirrors the frontend's CloseTileDialog).
	// This runs BEFORE the self-target guard so a user who hasn't
	// chosen a policy yet doesn't get a `--force`-shaped error for a
	// problem they haven't reached — they'd just hit the same wall
	// again after adding --force.
	if len(tabsAffected) > 0 && policy == withTabsUnspecified {
		return remote.EmitError("invalid_request",
			fmt.Sprintf("tile %s has %d live tab(s); pass --with-tabs=close to tombstone them or --with-tabs=move to migrate them to the heir tile",
				tileID, len(tabsAffected)))
	}

	// Safety phase: refuse to tear down the calling tab's own PTY
	// unless --force. Skipped for --with-tabs=move because that path
	// migrates the tab's tile_id before tombstoning its old tile, so
	// the tab (and its PTY) survive.
	if policy != withTabsMove {
		if err := guardTileClose(cc.bs.State, tileID, force); err != nil {
			return err
		}
	}

	var ops []*leapmuxv1.OrgOp
	heirID := ""
	switch {
	case recursive:
		// Cascade: tombstone (or migrate-and-tombstone) the whole
		// subtree. The root passed to buildCloseSubtreeOps is the
		// SPLIT/GRID tile itself.
		if policy == withTabsMove && len(tabsAffected) > 0 {
			heirID = crdt.FindHeirTileID(cc.bs.State, tileID, rootNodeID)
			if heirID == "" {
				return remote.EmitError("invalid_request",
					fmt.Sprintf("no heir tile available for the tabs on %s; pass --with-tabs=close instead", tileID))
			}
		}
		ops = buildCloseSubtreeOps(cc.bs, tileID, heirID, false)

	case policy == withTabsClose:
		// Leaf, close all tabs first, then run the standard close
		// (which folds the inverse-split when applicable).
		for _, t := range tabsAffected {
			ops = append(ops, opTombstoneTab(cc.bs, t.TabType, t.TabID))
		}
		ops = append(ops, buildCloseTileOps(cc.bs, tileID)...)

	case policy == withTabsMove:
		// Leaf with tabs to migrate. The heir is computed against the
		// pre-close projection so the user can predict where tabs
		// land without running the command first.
		heirID = crdt.FindHeirTileID(cc.bs.State, tileID, rootNodeID)
		if heirID == "" {
			return remote.EmitError("invalid_request",
				fmt.Sprintf("no heir tile available for tabs on %s; pass --with-tabs=close instead", tileID))
		}
		heirPos := lexorank.First()
		for _, t := range tabsAffected {
			ops = append(ops, opSetTabTileID(cc.bs, t.TabType, t.TabID, heirID))
			ops = append(ops, opSetTabPosition(cc.bs, t.TabType, t.TabID, heirPos))
			heirPos = lexorank.After(heirPos)
		}
		ops = append(ops, buildCloseTileOps(cc.bs, tileID)...)

	default:
		// Leaf with no tabs and no --with-tabs choice — plain close.
		ops = buildCloseTileOps(cc.bs, tileID)
	}

	if err := cc.submitOps(ops); err != nil {
		return err
	}
	out := map[string]any{
		"tile_id":     tileID,
		"tabs_closed": 0,
		"tabs_moved":  0,
	}
	switch policy {
	case withTabsClose:
		out["tabs_closed"] = len(tabsAffected)
	case withTabsMove:
		out["tabs_moved"] = len(tabsAffected)
		out["heir_tile_id"] = heirID
	}
	return remote.EmitData(out)
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

// withTabsPolicyMap is the constrained --with-tabs mapping. Routed
// through parseEnumFlag in parseWithTabsPolicy.
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
	v, ok := parseEnumFlag(s, withTabsPolicyMap)
	if !ok {
		return withTabsUnspecified, remote.EmitError("invalid_request", `--with-tabs must be "close" or "move"`)
	}
	return v, nil
}

// isGridCell reports whether nodeID names a live leaf whose parent
// is a live GRID. Returns the parent grid's id when true so the
// caller can name it in the error message. Used by `tile close` to
// refuse closing a single grid cell (which would leave an unusable
// placeholder in the grid).
func isGridCell(state *leapmuxv1.OrgMaterialized, nodeID string) (gridID string, ok bool) {
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

// buildCloseTileOps produces the ops to close one tile, mirroring the
// frontend's `tileOps.ts:buildCloseTileOps`. The undo-split logic is
// the load-bearing part: when the closing tile's parent is a SPLIT
// with exactly two live children, leaving the parent with one live
// child triggers the projection's single-child collapse — the
// rendered tree re-keys to the parent's id, but the surviving
// sibling's tabs still reference the (now hidden) sibling id, so
// they orphan visually. The fix is to migrate the sibling's tabs to
// the parent, tombstone the sibling, and flip the parent's kind back
// to LEAF in the same batch, so the rendered tree's tile id matches
// the tabs' tile_id.
//
// Callers must guarantee tileID is not a registered workspace /
// floating-window root — the validator rejects root tombstones with
// `root_node_protected` and would roll the whole batch back. The
// inverse-split itself never tombstones the parent, only flips its
// kind, so a SPLIT root that lands in the collapse path is safe.
func buildCloseTileOps(bs *CRDTBootstrap, tileID string) []*leapmuxv1.OrgOp {
	state := bs.State
	ops := []*leapmuxv1.OrgOp{}
	for _, t := range crdt.TabsOnTile(state, tileID) {
		ops = append(ops, opTombstoneTab(bs, t.TabType, t.TabID))
	}
	ops = append(ops, opTombstoneNode(bs, tileID))

	closing := state.GetNodes()[tileID]
	if closing == nil {
		return ops
	}
	parentID := closing.GetParentId()
	if parentID == "" {
		return ops
	}
	parent := state.GetNodes()[parentID]
	if parent == nil || parent.GetKind().GetValue() != leapmuxv1.NodeKind_NODE_KIND_SPLIT || !crdt.HLCIsZero(parent.GetTombstoneAt()) {
		return ops
	}
	// Live children of the parent, including tileID itself (the
	// caller hasn't applied the tombstone yet — we're still building
	// ops). The inverse-split only fires when the SPLIT has exactly
	// two live children and the closing tile is one of them, so after
	// the batch lands the parent would otherwise be left with one
	// live child.
	liveChildren := make([]string, 0, 2)
	for _, n := range state.GetNodes() {
		if n.GetParentId() != parentID {
			continue
		}
		if !crdt.HLCIsZero(n.GetTombstoneAt()) {
			continue
		}
		liveChildren = append(liveChildren, n.GetNodeId())
	}
	if len(liveChildren) != 2 {
		return ops
	}
	var siblingID string
	for _, id := range liveChildren {
		if id != tileID {
			siblingID = id
		}
	}
	if siblingID == "" {
		return ops
	}
	// Inverse-split only fires when the sibling is itself a leaf. If
	// the sibling is a SPLIT or GRID, tombstoning it would orphan
	// every descendant + every tab under those descendants — the
	// validator then rejects the batch with
	// BATCH_REJECTION_TAB_PLACEMENT_INVALID because the surviving
	// tabs reference a now-dead tile chain.
	//
	// For the non-leaf-sibling case we rely on `project.ts:buildTree`
	// (and its Go mirror): a SPLIT with a single live child collapses
	// in the rendered tree, with the surviving sub-tree's root
	// re-keyed to the parent's id. Tabs on the sub-tree's leaves keep
	// their own tile_id and render correctly, so no rewiring is
	// needed in the op batch.
	sibling := state.GetNodes()[siblingID]
	sibKind := sibling.GetKind().GetValue()
	if sibKind != leapmuxv1.NodeKind_NODE_KIND_LEAF && sibKind != leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED {
		return ops
	}

	// The "natural" undo-split target is parentID — flip it to LEAF
	// and migrate the sibling's tabs there. But if parentID is itself
	// already the only live child of an enclosing SPLIT, the
	// projection's single-child collapse will re-key that SPLIT to
	// the ancestor's id. Migrating tabs to parentID then strands them
	// on a node that doesn't appear in the rendered tree (tabs sit
	// on parentID, the rendered leaf advertises the ancestor's id,
	// the renderer queries tabs[ancestor] and finds none).
	//
	// Walk upward to find the topmost SPLIT in the single-child chain
	// and collapse the whole chain in one batch: tabs go to that
	// ancestor, every intermediate SPLIT is tombstoned, and the
	// topmost ancestor flips to LEAF. The walk terminates at any
	// non-SPLIT, tombstoned, or multi-child ancestor — those don't
	// collapse in projection so the rendered leaf would already match
	// the migration destination there. Workspace / floating-window
	// roots are safe targets for the kind flip (set-once root_node_id,
	// but kind is a normal LWW register).
	destID := parentID
	intermediates := []string{}
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
		upLive := make([]string, 0, 2)
		for _, n := range state.GetNodes() {
			if n.GetParentId() != upID {
				continue
			}
			if !crdt.HLCIsZero(n.GetTombstoneAt()) {
				continue
			}
			upLive = append(upLive, n.GetNodeId())
		}
		if len(upLive) != 1 || upLive[0] != destID {
			break
		}
		intermediates = append(intermediates, destID)
		destID = upID
		curNode = up
	}

	sibTabs := crdt.TabsOnTile(state, siblingID)
	sibPos := lexorank.First()
	for _, t := range sibTabs {
		ops = append(ops, opSetTabTileID(bs, t.TabType, t.TabID, destID))
		ops = append(ops, opSetTabPosition(bs, t.TabType, t.TabID, sibPos))
		sibPos = lexorank.After(sibPos)
	}
	ops = append(ops, opTombstoneNode(bs, siblingID))
	for _, id := range intermediates {
		ops = append(ops, opTombstoneNode(bs, id))
	}
	ops = append(ops, opSetNodeKind(bs, destID, leapmuxv1.NodeKind_NODE_KIND_LEAF))
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
func buildCloseSubtreeOps(bs *CRDTBootstrap, tileID, migrateTo string, keepRoot bool) []*leapmuxv1.OrgOp {
	state := bs.State
	descendants := crdt.DescendantsLeavesFirst(state, tileID)
	ops := []*leapmuxv1.OrgOp{}
	migratedPos := lexorank.First()
	emitTabOps := func(tabs []crdt.TabRef) {
		for _, t := range tabs {
			if migrateTo != "" {
				ops = append(ops, opSetTabTileID(bs, t.TabType, t.TabID, migrateTo))
				ops = append(ops, opSetTabPosition(bs, t.TabType, t.TabID, migratedPos))
				migratedPos = lexorank.After(migratedPos)
			} else {
				ops = append(ops, opTombstoneTab(bs, t.TabType, t.TabID))
			}
		}
	}
	for _, id := range descendants {
		if id == tileID {
			continue
		}
		emitTabOps(crdt.TabsOnTile(state, id))
		ops = append(ops, opTombstoneNode(bs, id))
	}
	emitTabOps(crdt.TabsOnTile(state, tileID))
	if !keepRoot {
		ops = append(ops, opTombstoneNode(bs, tileID))
	}
	return ops
}

// RunTileMakeGrid converts a leaf tile into a rows*cols grid. Tabs
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
func RunTileMakeGrid(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var rows, cols int
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	maxDim := int(crdt.MaxGridDimension)
	fs.IntVar(&rows, "rows", 0, fmt.Sprintf("grid rows (1-%d, required)", maxDim))
	fs.IntVar(&cols, "cols", 0, fmt.Sprintf("grid cols (1-%d, required)", maxDim))
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if rows < 1 || rows > maxDim || cols < 1 || cols > maxDim {
		return remote.EmitError("invalid_request", fmt.Sprintf("--rows and --cols must be in [1, %d]", maxDim))
	}
	cc, _, tileID, err := openTileCRDTCall(hub, in)
	if err != nil {
		return err
	}
	defer cc.close()

	kind := cc.bs.State.GetNodes()[tileID].GetKind().GetValue()
	if kind != leapmuxv1.NodeKind_NODE_KIND_LEAF && kind != leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED {
		return remote.EmitError("invalid_request",
			fmt.Sprintf("tile %s is a %s node; only leaf tiles can be converted to a grid", tileID, kindLabel(kind)))
	}

	tabsOnLeaf := crdt.TabsOnTile(cc.bs.State, tileID)
	rowRatios := crdt.EqualRatios(rows)
	colRatios := crdt.EqualRatios(cols)
	ops := []*leapmuxv1.OrgOp{
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
				opSetNodePosition(cc.bs, cellID, fmt.Sprintf("%d,%d", r, c)),
			)
		}
	}
	dest := cellTileIDs[0]
	destPos := lexorank.First()
	for _, t := range tabsOnLeaf {
		ops = append(ops, opSetTabTileID(cc.bs, t.TabType, t.TabID, dest))
		ops = append(ops, opSetTabPosition(cc.bs, t.TabType, t.TabID, destPos))
		destPos = lexorank.After(destPos)
	}

	if err := cc.submitOps(ops); err != nil {
		return err
	}

	return remote.EmitData(map[string]any{
		"grid_tile_id":                    tileID,
		"rows":                            rows,
		"cols":                            cols,
		"leaf_tile_ids":                   cellTileIDs,
		"leaf_tile_with_existing_tabs_id": dest,
		"tabs_moved":                      len(tabsOnLeaf),
	})
}

// RunTileRemoveGrid removes a grid. The behaviour depends on
// --with-tabs:
//
//	close (default when no tabs in the subtree)
//	     Tombstone every descendant + the grid node. For a root grid
//	     the kind is flipped from GRID to LEAF instead of tombstoning
//	     the root (the validator rejects `root_node_protected`); the
//	     workspace ends up as a bare root LEAF.
//
//	move
//	     Mirror the frontend's `replaceGridWithLeaf`: tombstone every
//	     cell + every tab in the subtree, but migrate the tabs onto a
//	     fresh leaf created in the grid's slot (inheriting its
//	     parent_id + position). The grid node itself is tombstoned.
//	     Root grid: flip the root's kind back to LEAF in place and
//	     migrate every tab onto it.
//
// Required when the grid's subtree has at least one live tab, so we
// never silently destroy or relocate tabs.
//
// Tombstoning a descendant tile kills the PTYs on it, so
// `guardTileRemoveGrid` rejects the operation when the calling tab
// sits anywhere inside the doomed subtree unless --force is supplied.
func RunTileRemoveGrid(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, withTabs string
	var force bool
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	fs.BoolVar(&force, "force", false, "remove even if the calling tab sits inside the target grid (would kill the caller's own PTY)")
	fs.StringVar(&withTabs, "with-tabs", "", `policy for live tabs in the grid: "close" tombstones them; "move" collapses the grid to a single leaf (in the grid's old slot) and migrates the tabs onto it. Required when the grid has tabs.`)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	policy, err := parseWithTabsPolicy(withTabs)
	if err != nil {
		return err
	}
	cc, _, gridID, err := openTileCRDTCall(hub, in)
	if err != nil {
		return err
	}
	defer cc.close()
	// Argument-validation phase: target must be a GRID node.
	if err := preflightTileKind(cc.bs.State, gridID, leapmuxv1.NodeKind_NODE_KIND_GRID, "remove-grid", "GRID", ""); err != nil {
		return err
	}
	gridRec := cc.bs.State.GetNodes()[gridID]
	descendants := crdt.DescendantsLeavesFirst(cc.bs.State, gridID)
	tabsInSubtree := crdt.LiveTabsInSubtree(cc.bs.State, gridID)

	// Policy-decision phase: a grid carrying live tabs needs an
	// explicit --with-tabs choice (mirrors the frontend's
	// CloseGridDialog). This runs BEFORE the self-target guard so a
	// user who hasn't picked a policy yet doesn't get a
	// `--force`-shaped error for a problem they haven't reached —
	// they'd just hit the same wall again after adding --force.
	if len(tabsInSubtree) > 0 && policy == withTabsUnspecified {
		return remote.EmitError("invalid_request",
			fmt.Sprintf("grid %s has %d live tab(s); pass --with-tabs=close to tombstone them or --with-tabs=move to collapse the grid back to a single tile with the tabs preserved",
				gridID, len(tabsInSubtree)))
	}

	// Safety phase: refuse to tear down the calling tab's own PTY
	// unless --force. Skipped for --with-tabs=move because that path
	// migrates every tab in the subtree onto the replacement leaf
	// (or the root for a root grid), so the tab and its PTY survive.
	if policy != withTabsMove {
		if err := guardTileRemoveGrid(cc.bs.State, descendants, force); err != nil {
			return err
		}
	}

	parentID := gridRec.GetParentId()
	isRoot := parentID == ""
	ops := []*leapmuxv1.OrgOp{}
	out := map[string]any{
		"grid_tile_id": gridID,
		"tabs_closed":  0,
		"tabs_moved":   0,
	}

	switch policy {
	case withTabsMove:
		if isRoot {
			// Root grid: keep the root NodeRecord alive (root_node_id
			// is set-once) and flip its kind back to LEAF; tabs land
			// on the root tile itself.
			ops = append(ops, buildCloseSubtreeOps(cc.bs, gridID, gridID, true)...)
			ops = append(ops, opSetNodeKind(cc.bs, gridID, leapmuxv1.NodeKind_NODE_KIND_LEAF))
			out["leaf_tile_id"] = gridID
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
			out["leaf_tile_id"] = newLeaf
		}
		out["tabs_moved"] = len(tabsInSubtree)

	default:
		// withTabsClose, or no tabs to worry about.
		ops = append(ops, buildCloseSubtreeOps(cc.bs, gridID, "", isRoot)...)
		if isRoot {
			// Root: flip the (still-alive) NodeRecord's kind to LEAF.
			ops = append(ops, opSetNodeKind(cc.bs, gridID, leapmuxv1.NodeKind_NODE_KIND_LEAF))
		}
		out["tabs_closed"] = len(tabsInSubtree)
	}

	if err := cc.submitOps(ops); err != nil {
		return err
	}
	return remote.EmitData(out)
}
