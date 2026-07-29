package crdt

import (
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// RenderedTab is a tab that survives projection.
type RenderedTab struct {
	UserID      string
	WorkspaceID string
	TabType     leapmuxv1.TabType
	TabID       string
	WorkerID    string
	TileID      string
	Position    string
}

// OwnedTab is every non-tombstoned tab. Worker reconciliation reads
// from this set; UI reads from RenderedTabs.
type OwnedTab = RenderedTab

// Projection is the hub's view of a user's tabs.
//
// Deliberately NOT a rendered layout. The hub used to build a full render
// tree here, mirroring `frontend/src/lib/crdt/project.ts` node for node, and
// nothing ever read it -- the two consumers below want tabs, and the frontend
// renders the layout from the same CRDT state on its own. Keeping a second
// implementation of the tiling rules only gave them somewhere to drift apart,
// which is exactly what happened. Layout questions belong to the client that
// draws the layout; if the hub ever needs one, it can rebuild a tree from
// state rather than maintain one nobody reads.
//
// The two slices are different questions:
//   - OwnedTabs: every live tab. Worker reconciliation reads this.
//   - RenderedTabs: the subset whose tile_id resolves to a live LEAF reachable
//     from a registered root. This is an ELIGIBILITY predicate, not a layout
//     one -- it backs `workspace_tab_rendered`, which serves LocateTab /
//     GetTab / ListTabs, and the CLI derives a writable `--tile-id` from it.
//     So its TileID is always the tab's own tile: the raw, addressable id that
//     `validateTabPlacement` will accept.
type Projection struct {
	UserID       string
	OwnedTabs    []*OwnedTab
	RenderedTabs []*RenderedTab
}

// Project applies the deterministic repair rules and returns the projected
// tabs. The rules that survive here:
//   - tombstoned tabs are skipped
//   - tabs whose tile_id chain doesn't terminate at a registered root
//     (workspace or floating window) are dropped from both views as orphans
//   - a tab renders only when its tile is a live LEAF; one that fails this
//     stays in the owned view and leaves the rendered one
func Project(state *leapmuxv1.UserCrdtState) *Projection {
	roots := registeredRoots(state)
	owned, rendered := projectTabs(state, roots)
	return &Projection{
		UserID:       state.GetUserId(),
		OwnedTabs:    owned,
		RenderedTabs: rendered,
	}
}

// rootSet is the union of all known root node IDs (workspace roots +
// floating-window roots). The validator's root_uniqueness rule
// guarantees no overlap in well-formed state; `counts` retains the
// per-node occurrence so validateRootAssignment can reject same-batch
// duplicate registrations without re-scanning workspaces + windows.
type rootSet struct {
	// nodeID -> workspaceID. For floating-window roots, this is the
	// resolved workspace_id of the parent window. When `counts[id] > 1`
	// the winner is decided by a stated rule rather than by map order:
	// workspaces are considered before floating windows, each in
	// ascending id order, and the FIRST claim wins. See registeredRoots.
	roots  map[string]string
	counts map[string]int
	// workspaceRoots maps root node_id -> workspace_id when the root is
	// a workspace (not a floating window). Distinguishes the two root
	// kinds for rootChecks (workspace roots are protected even from
	// internal batches; window roots may be tombstoned by an internal
	// sweep). Populated only for live workspaces.
	workspaceRoots map[string]string
	// windowRoots maps root node_id -> window_id for live floating
	// windows. Same disambiguation purpose as workspaceRoots.
	windowRoots map[string]string
}

func registeredRoots(state *leapmuxv1.UserCrdtState) rootSet {
	rs := rootSet{
		roots:          map[string]string{},
		counts:         map[string]int{},
		workspaceRoots: map[string]string{},
		windowRoots:    map[string]string{},
	}
	// Ascending id order, FIRST claim wins -- on both sides.
	//
	// Two workspaces can name the same root node id. The hub's commit path
	// rejects that (a set-once register plus validateRootAssignment), but the
	// projection is total and still has to answer for speculative state, and
	// the conformance harnesses apply ops raw with no validator. Ranging a Go
	// map and letting the last writer win made the answer depend on map
	// iteration order, so the same input could resolve a tab to a different
	// workspace between runs -- and the corpus, which RECORDS this side's
	// answer, cannot certify a value that is not a function of its input.
	//
	// Insertion order is not the fix: a Go map has none, the records arrive
	// over the wire where protobuf leaves map ordering unspecified, and TS's
	// object order is therefore just decode order rather than anything two
	// clients agree on. Ordering on a value IN the data is the only property
	// both languages can share.
	wsIDs := make([]string, 0, len(state.GetWorkspaces()))
	for wsID := range state.GetWorkspaces() {
		wsIDs = append(wsIDs, wsID)
	}
	sort.Strings(wsIDs)
	for _, wsID := range wsIDs {
		id := state.GetWorkspaces()[wsID].GetRootNodeId()
		if id == "" {
			continue
		}
		// `counts` records every claim, including the losing ones --
		// validateRootAssignment needs the true occurrence count to reject a
		// same-batch duplicate registration.
		rs.counts[id]++
		if _, taken := rs.roots[id]; taken {
			continue
		}
		rs.roots[id] = wsID
		rs.workspaceRoots[id] = wsID
	}
	// Windows are considered after workspaces, so a workspace root outranks a
	// window root on collision. That direction is deliberate: a workspace root
	// is protected even from internal batches, while a window root may be
	// tombstoned by an internal sweep.
	windowIDs := make([]string, 0, len(state.GetFloatingWindows()))
	for windowID := range state.GetFloatingWindows() {
		windowIDs = append(windowIDs, windowID)
	}
	sort.Strings(windowIDs)
	for _, windowID := range windowIDs {
		fw := state.GetFloatingWindows()[windowID]
		if !HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		id := fw.GetRootNodeId()
		if id == "" {
			continue
		}
		rs.counts[id]++
		if _, taken := rs.roots[id]; taken {
			continue
		}
		rs.roots[id] = fw.GetWorkspaceId().GetValue()
		rs.windowRoots[id] = fw.GetWindowId()
	}
	return rs
}

// resolveTileWorkspace walks tile_id's parent_id chain to a registered
// root and returns (workspaceID, chainAlive). The chain is "alive" iff
// the tile itself and every ancestor up to the root are non-tombstoned.
//
// A SINGLE walk covers cycle detection and tombstone-along-the-chain
// checking, mirroring frontend/src/lib/crdt/project.ts line for line.
// The previous shape walked the same chain twice (resolveParentChain,
// then a separate chainAlive helper) and the two walks did not agree
// with the TS twin at the chain's end, in two ways the shared
// conformance fixture did not cover:
//
//   - a registered root whose NodeRecord has not materialised yet was
//     DEAD here and ALIVE there. It is alive: the root is registered,
//     the record simply has not landed, and that window is exactly
//     what the client's hold-in-place exists for.
//   - chainAlive kept walking ABOVE the registered root to parent_id
//     == "", so a missing or tombstoned node above a root killed the
//     chain here only. A registered root is by definition the top of
//     the workspace's tree; whatever sits above it is irrelevant.
//
// Both cases now have conformance cases pinning them, so the two
// implementations cannot drift back apart silently.
//
// Unlike the generic resolveParentChain helper, this one rejects
// tombstoned intermediates outright — the leaf-reachability contract
// is part of the projection / move-validation semantics.
func resolveTileWorkspace(state *leapmuxv1.UserCrdtState, tileID string, roots rootSet) (string, bool) {
	if tileID == "" {
		return "", false
	}
	visited := map[string]bool{}
	cur := tileID
	for {
		if visited[cur] {
			return "", false
		}
		visited[cur] = true
		if wsID, ok := roots.roots[cur]; ok {
			// Root reached. The chain up to here is alive (we would have
			// returned otherwise). Workspace roots are registered without
			// checking their NodeRecord, so re-read it: a tombstoned root
			// kills the chain, an absent one does not.
			rootNode := state.GetNodes()[cur]
			alive := rootNode == nil || HLCIsZero(rootNode.GetTombstoneAt())
			return wsID, alive
		}
		node := state.GetNodes()[cur]
		if node == nil || !HLCIsZero(node.GetTombstoneAt()) {
			return "", false
		}
		if node.GetParentId() == "" {
			return "", false
		}
		cur = node.GetParentId()
	}
}

// projectTabs splits the tab map into (owned, rendered). The per-tab rule
// itself lives in projectTabRow, shared with projectOneTab's incremental
// commit-path diffing.
func projectTabs(state *leapmuxv1.UserCrdtState, roots rootSet) ([]*OwnedTab, []*RenderedTab) {
	var owned []*OwnedTab
	var rendered []*RenderedTab
	// Memoize tile→(workspace, leafLive) so multi-tab leaves don't
	// re-walk identical parent chains. The hot path is structural-batch
	// reprojection, where the same handful of tile ids recur across the
	// tab map.
	type tileResolution struct {
		wsID     string
		leafLive bool
	}
	tileMemo := make(map[string]tileResolution, len(state.GetNodes()))
	resolve := func(tile string) (string, bool) {
		if res, ok := tileMemo[tile]; ok {
			return res.wsID, res.leafLive
		}
		wsID, leafLive := resolveTileWorkspace(state, tile, roots)
		tileMemo[tile] = tileResolution{wsID, leafLive}
		return wsID, leafLive
	}
	for _, t := range state.GetTabs() {
		row, renderedRow := projectTabRow(state, t, resolve)
		if row == nil {
			continue
		}
		owned = append(owned, row)
		if renderedRow != nil {
			rendered = append(rendered, renderedRow)
		}
	}
	// Stable ordering for deterministic output.
	sort.SliceStable(owned, func(i, j int) bool { return owned[i].TabID < owned[j].TabID })
	sort.SliceStable(rendered, func(i, j int) bool { return rendered[i].TabID < rendered[j].TabID })
	return owned, rendered
}

func tileIsLeaf(state *leapmuxv1.UserCrdtState, tileID string) bool {
	rec := state.GetNodes()[tileID]
	if rec == nil {
		return false
	}
	return rec.GetKind().GetValue() == leapmuxv1.NodeKind_NODE_KIND_LEAF
}

// projectOneTab returns (owned, rendered) for a single tab id in the
// given state. Either pointer is nil when the tab is absent /
// tombstoned / orphaned. Used by the op-driven diff path
// (DiffProjectionForBatch) to skip the full projectTabs scan when only
// a handful of tab ids could have transitioned between commits.
func projectOneTab(state *leapmuxv1.UserCrdtState, tabID string, roots rootSet) (*OwnedTab, *RenderedTab) {
	t, ok := state.GetTabs()[tabID]
	if !ok {
		return nil, nil
	}
	return projectTabRow(state, t, func(tile string) (string, bool) {
		return resolveTileWorkspace(state, tile, roots)
	})
}

// projectTabRow applies the per-tab projection rule to ONE record: drop it if
// tombstoned or unowned, otherwise build its row and decide whether it also
// renders. Returns (nil, nil) when the tab projects to nothing, and (row, row)
// when it renders -- rendered is a strict subset of owned by construction, so
// the two share one allocation.
//
// The full scan and the incremental commit-path diff both need EXACTLY these
// rules, and used to spell them out separately -- the tombstone check, the
// resolve, `ownershipHolds`, the seven-field literal, then `tileIsLeaf`. Two
// hand-synced copies of the ownership rule is precisely where a Go/TS
// projection disagreement hides, which is the bug class this file has already
// been bitten by. `resolve` is the only genuine difference between the callers:
// the full scan memoizes tile resolution across a whole tab map, the single-tab
// path does not.
func projectTabRow(
	state *leapmuxv1.UserCrdtState,
	t *leapmuxv1.TabRecord,
	resolve func(tile string) (string, bool),
) (*OwnedTab, *RenderedTab) {
	if !HLCIsZero(t.GetTombstoneAt()) {
		return nil, nil
	}
	tile := t.GetTileId().GetValue()
	wsID, chainLive := resolve(tile)
	if !ownershipHolds(state, wsID, chainLive) {
		return nil, nil
	}
	row := &OwnedTab{
		UserID:      state.GetUserId(),
		WorkspaceID: wsID,
		TabType:     t.GetTabType(),
		TabID:       t.GetTabId(),
		WorkerID:    t.GetWorkerId().GetValue(),
		TileID:      tile,
		Position:    t.GetPosition().GetValue(),
	}
	if tileIsLeaf(state, tile) {
		return row, row
	}
	return row, nil
}

// ownershipHolds decides whether a tab whose tile resolved to `wsID` is the
// user's tab at all. `rendered` then narrows `owned` by leaf-ness alone, which
// makes rendered a strict subset by construction.
//
// Two conditions, both of which used to be applied unevenly:
//
//   - The CHAIN must be alive. The roots map is consulted before the tombstone
//     check, so reaching a registered root short-circuits the walk even when
//     that root node is tombstoned. Liveness therefore only gated rendering,
//     and a tab on a tombstoned ROOT stayed owned while a tab on any other
//     tombstoned tile dropped from both views. Same dead tile, two answers.
//
//   - The WORKSPACE must still exist. A floating window carries its own
//     workspace_id and registers its root from the window record, so deleting
//     the WorkspaceContentsRecord left the window's tabs resolving to a
//     workspace that is no longer in the projection at all -- the client's
//     render tree already drops such a window, so those tabs were reported as
//     on-screen with nothing to draw them in.
//
// Checked here rather than in registeredRoots because that root map is shared
// with batch validation (a parentless node is legal only if it is a registered
// root) and with subscriber broadcast filtering; narrowing it would change both.
func ownershipHolds(state *leapmuxv1.UserCrdtState, wsID string, chainLive bool) bool {
	if wsID == "" || !chainLive {
		return false
	}
	_, live := state.GetWorkspaces()[wsID]
	return live
}
