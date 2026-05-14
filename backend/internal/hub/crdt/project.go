package crdt

import (
	"fmt"
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// RenderTree is the in-memory projection of a single tile tree (a
// workspace's main layout, or one floating-window's inner layout).
// It mirrors the shape the frontend expects but stays in Go land.
type RenderTree struct {
	NodeID    string
	Kind      leapmuxv1.NodeKind
	Direction leapmuxv1.SplitDirection
	Ratios    []float64
	Rows      uint32
	Cols      uint32
	RowRatios []float64
	ColRatios []float64
	Children  []*RenderTree
}

// RenderedTab is a tab that survives projection.
type RenderedTab struct {
	OrgID       string
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

// WorkspaceProjection groups one workspace's projected tree, floating
// windows, and tabs.
type WorkspaceProjection struct {
	WorkspaceID     string
	MainTree        *RenderTree
	FloatingWindows []*RenderedFloatingWindow
	RenderedTabs    []*RenderedTab
}

// RenderedFloatingWindow is a window that survives projection.
type RenderedFloatingWindow struct {
	WindowID  string
	X         float64
	Y         float64
	Width     float64
	Height    float64
	Opacity   float64
	InnerTree *RenderTree
}

// Projection holds the org-wide rendered state.
type Projection struct {
	OrgID        string
	Workspaces   map[string]*WorkspaceProjection
	OwnedTabs    []*OwnedTab
	RenderedTabs []*RenderedTab
}

// Project applies the deterministic repair rules and returns the
// renderable projection. The rules are:
//   - tombstoned nodes are skipped
//   - nodes whose parent_id chain doesn't terminate at a registered
//     root (workspace or floating window) are dropped (orphans / cycles)
//   - duplicate split-child positions tie-break by (position, node_id)
//   - duplicate grid-cell positions: lower node_id wins, others dropped
//   - missing grid cells render as empty leaves
//   - bad ratio lengths are truncated/padded to 1/N at projection time
//
// Tab projection requires tile_id to resolve to a live LEAF reachable
// from a registered root. Tabs that fail this drop from the rendered
// view but remain in the owned view.
//
// Hot-path callers that only need the (owned, rendered) tab pair —
// e.g. the per-commit `DiffProjection` call — should use
// `ProjectOwnership` instead. It skips the render-tree walk, which is
// the bulk of `Project`'s cost on large workspaces.
func Project(state *leapmuxv1.OrgCrdtState) *Projection {
	out := &Projection{
		OrgID:      state.GetOrgId(),
		Workspaces: map[string]*WorkspaceProjection{},
	}

	roots := registeredRoots(state)
	idx := buildChildIndex(state)

	for wsID, wsRec := range state.GetWorkspaces() {
		out.Workspaces[wsID] = &WorkspaceProjection{
			WorkspaceID: wsID,
			MainTree:    buildTreeFromRoot(state, wsRec.GetRootNodeId(), roots, idx),
		}
	}

	for _, fw := range state.GetFloatingWindows() {
		if !HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		wsID := fw.GetWorkspaceId().GetValue()
		ws := out.Workspaces[wsID]
		if ws == nil {
			// Floating window pointing at a deleted workspace; drop.
			continue
		}
		ws.FloatingWindows = append(ws.FloatingWindows, &RenderedFloatingWindow{
			WindowID:  fw.GetWindowId(),
			X:         fw.GetX().GetValue(),
			Y:         fw.GetY().GetValue(),
			Width:     fw.GetWidth().GetValue(),
			Height:    fw.GetHeight().GetValue(),
			Opacity:   fw.GetOpacity().GetValue(),
			InnerTree: buildTreeFromRoot(state, fw.GetRootNodeId(), roots, idx),
		})
	}

	// Stable order for floating windows (window_id ascending).
	for _, ws := range out.Workspaces {
		sort.SliceStable(ws.FloatingWindows, func(i, j int) bool {
			return ws.FloatingWindows[i].WindowID < ws.FloatingWindows[j].WindowID
		})
	}

	owned, rendered := projectTabs(state, roots)
	out.OwnedTabs = owned
	out.RenderedTabs = rendered
	return out
}

// ProjectOwnership returns a Projection populated with only OrgID,
// OwnedTabs, and RenderedTabs. Per-workspace MainTree and FloatingWindow
// render trees are NOT computed — this is the hot path used by
// `commit()` to feed `DiffProjection`, which only reads the two tab
// slices. Skipping the render-tree walk drops per-batch commit cost
// from O(N + W·N) (with W = workspaces, each requiring a full subtree
// build) to O(N_tabs + chain depth per tab).
//
// Callers that want the render trees (initial bootstrap, subscriber
// add, RPC handlers) must still call Project. The Workspaces map on
// the returned Projection is empty; do not iterate it.
func ProjectOwnership(state *leapmuxv1.OrgCrdtState) *Projection {
	roots := registeredRoots(state)
	owned, rendered := projectTabs(state, roots)
	return &Projection{
		OrgID:        state.GetOrgId(),
		Workspaces:   map[string]*WorkspaceProjection{},
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
	// the value is one of the colliding workspace ids, chosen
	// non-deterministically.
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

func registeredRoots(state *leapmuxv1.OrgCrdtState) rootSet {
	rs := rootSet{
		roots:          map[string]string{},
		counts:         map[string]int{},
		workspaceRoots: map[string]string{},
		windowRoots:    map[string]string{},
	}
	for wsID, wsRec := range state.GetWorkspaces() {
		if id := wsRec.GetRootNodeId(); id != "" {
			rs.roots[id] = wsID
			rs.counts[id]++
			rs.workspaceRoots[id] = wsID
		}
	}
	for _, fw := range state.GetFloatingWindows() {
		if !HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		if id := fw.GetRootNodeId(); id != "" {
			rs.roots[id] = fw.GetWorkspaceId().GetValue()
			rs.counts[id]++
			rs.windowRoots[id] = fw.GetWindowId()
		}
	}
	return rs
}

// resolveTileWorkspace walks tile_id's parent_id chain to a registered
// root and returns (workspaceID, chainAlive). The chain is "alive" iff
// the tile itself and every ancestor up to the root are non-tombstoned.
//
// Unlike the generic resolveParentChain helper, this one rejects
// tombstoned intermediates outright — the leaf-reachability contract
// is part of the projection / move-validation semantics.
func resolveTileWorkspace(state *leapmuxv1.OrgCrdtState, tileID string, roots rootSet) (string, bool) {
	if tileID == "" {
		return "", false
	}
	ws := resolveParentChain(roots.roots, tileID, func(id string) (string, bool) {
		node := state.GetNodes()[id]
		if node == nil || !HLCIsZero(node.GetTombstoneAt()) {
			return "", false
		}
		parent := node.GetParentId()
		if parent == "" {
			return "", false
		}
		return parent, true
	})
	if ws == "" {
		return "", false
	}
	return ws, chainAlive(state, tileID)
}

// chainAlive walks from tile_id upward and returns true if every node
// (including the tile itself) is non-tombstoned.
func chainAlive(state *leapmuxv1.OrgCrdtState, tileID string) bool {
	visited := map[string]bool{}
	cur := tileID
	for cur != "" {
		if visited[cur] {
			return false
		}
		visited[cur] = true
		node := state.GetNodes()[cur]
		if node == nil {
			return false
		}
		if !HLCIsZero(node.GetTombstoneAt()) {
			return false
		}
		cur = node.GetParentId()
	}
	return true
}

// childIndex maps node_id → that node's children (live + tombstoned).
// Backed by the shared parent→children adjacency from treeops; this
// alias keeps projection-side call sites that iterate records readable
// by adding the record lookup at use time.
type childIndex map[string][]string

func buildChildIndex(state *leapmuxv1.OrgCrdtState) childIndex {
	return childIndex(BuildAllChildrenIndex(state))
}

// buildTreeFromRoot constructs a RenderTree rooted at rootID. Returns
// a placeholder leaf when the root is missing/tombstoned.
func buildTreeFromRoot(state *leapmuxv1.OrgCrdtState, rootID string, roots rootSet, idx childIndex) *RenderTree {
	if rootID == "" {
		return &RenderTree{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF}
	}
	rec := state.GetNodes()[rootID]
	if rec == nil || !HLCIsZero(rec.GetTombstoneAt()) {
		return &RenderTree{NodeID: rootID, Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF}
	}
	return buildTree(state, rec, roots, idx, map[string]bool{})
}

func buildTree(state *leapmuxv1.OrgCrdtState, rec *leapmuxv1.NodeRecord, roots rootSet, idx childIndex, seen map[string]bool) *RenderTree {
	if seen[rec.GetNodeId()] {
		return &RenderTree{NodeID: rec.GetNodeId(), Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF}
	}
	seen[rec.GetNodeId()] = true

	tree := &RenderTree{
		NodeID:    rec.GetNodeId(),
		Kind:      rec.GetKind().GetValue(),
		Direction: rec.GetDirection().GetValue(),
		Ratios:    append([]float64(nil), rec.GetRatios().GetValue().GetValues()...),
		Rows:      rec.GetRows().GetValue(),
		Cols:      rec.GetCols().GetValue(),
		RowRatios: append([]float64(nil), rec.GetRowRatios().GetValue().GetValues()...),
		ColRatios: append([]float64(nil), rec.GetColRatios().GetValue().GetValues()...),
	}

	// Live children: dereference the precomputed id adjacency to records
	// and filter tombstones at access time (the index keeps all children
	// so callers that need the tombstoned ones don't re-scan).
	siblings := idx[rec.GetNodeId()]
	children := make([]*leapmuxv1.NodeRecord, 0, len(siblings))
	for _, id := range siblings {
		n := state.GetNodes()[id]
		if n == nil || !HLCIsZero(n.GetTombstoneAt()) {
			continue
		}
		children = append(children, n)
	}

	switch tree.Kind {
	case leapmuxv1.NodeKind_NODE_KIND_SPLIT:
		// Sort by (position, node_id) for stable rendering on duplicates.
		sort.Slice(children, func(i, j int) bool {
			pi, pj := children[i].GetPosition().GetValue(), children[j].GetPosition().GetValue()
			if pi != pj {
				return pi < pj
			}
			return children[i].GetNodeId() < children[j].GetNodeId()
		})
		// SPLIT with one live child renders as just that child
		// (visual collapse). The live SPLIT node is preserved in
		// state — clients that want the underlying register cleanup
		// emit an in-place collapse batch.
		if len(children) == 1 {
			only := buildTree(state, children[0], roots, idx, seen)
			only.NodeID = tree.NodeID
			return only
		}
		for _, c := range children {
			tree.Children = append(tree.Children, buildTree(state, c, roots, idx, seen))
		}
		// Pad/truncate ratios to N children, defaulting to 1/N.
		tree.Ratios = normalizeRatios(tree.Ratios, len(tree.Children))
	case leapmuxv1.NodeKind_NODE_KIND_GRID:
		rows, cols := tree.Rows, tree.Cols
		if rows == 0 || cols == 0 {
			break
		}
		if uint32(len(tree.RowRatios)) != rows {
			tree.RowRatios = normalizeRatios(tree.RowRatios, int(rows))
		}
		if uint32(len(tree.ColRatios)) != cols {
			tree.ColRatios = normalizeRatios(tree.ColRatios, int(cols))
		}
		// Index children by their "r,c" position; lower node_id wins on duplicates.
		grid := make(map[string]*leapmuxv1.NodeRecord)
		for _, c := range children {
			pos := c.GetPosition().GetValue()
			existing, ok := grid[pos]
			if !ok || c.GetNodeId() < existing.GetNodeId() {
				grid[pos] = c
			}
		}
		tree.Children = make([]*RenderTree, 0, int(rows)*int(cols))
		for r := uint32(0); r < rows; r++ {
			for col := uint32(0); col < cols; col++ {
				key := fmt.Sprintf("%d,%d", r, col)
				if entry, ok := grid[key]; ok {
					tree.Children = append(tree.Children, buildTree(state, entry, roots, idx, seen))
				} else {
					tree.Children = append(tree.Children, &RenderTree{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF})
				}
			}
		}
	}
	return tree
}

func normalizeRatios(ratios []float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = 1.0 / float64(n)
	}
	for i := 0; i < n && i < len(ratios); i++ {
		if ratios[i] >= 0 {
			out[i] = ratios[i]
		}
	}
	return out
}

// projectTabs splits the tab map into (owned, rendered). Shared by
// Project (full projection) and ProjectOwnership (commit-path fast
// path that only needs these two slices).
func projectTabs(state *leapmuxv1.OrgCrdtState, roots rootSet) ([]*OwnedTab, []*RenderedTab) {
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
		if !HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		tile := t.GetTileId().GetValue()
		wsID, leafLive := resolve(tile)
		if wsID == "" {
			continue
		}
		row := &OwnedTab{
			OrgID:       state.GetOrgId(),
			WorkspaceID: wsID,
			TabType:     t.GetTabType(),
			TabID:       t.GetTabId(),
			WorkerID:    t.GetWorkerId().GetValue(),
			TileID:      tile,
			Position:    t.GetPosition().GetValue(),
		}
		owned = append(owned, row)
		if leafLive && tileIsLeaf(state, tile) {
			rendered = append(rendered, row)
		}
	}
	// Stable ordering for deterministic output.
	sort.SliceStable(owned, func(i, j int) bool { return owned[i].TabID < owned[j].TabID })
	sort.SliceStable(rendered, func(i, j int) bool { return rendered[i].TabID < rendered[j].TabID })
	return owned, rendered
}

func tileIsLeaf(state *leapmuxv1.OrgCrdtState, tileID string) bool {
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
func projectOneTab(state *leapmuxv1.OrgCrdtState, tabID string, roots rootSet) (*OwnedTab, *RenderedTab) {
	t, ok := state.GetTabs()[tabID]
	if !ok || !HLCIsZero(t.GetTombstoneAt()) {
		return nil, nil
	}
	tile := t.GetTileId().GetValue()
	wsID, leafLive := resolveTileWorkspace(state, tile, roots)
	if wsID == "" {
		return nil, nil
	}
	row := &OwnedTab{
		OrgID:       state.GetOrgId(),
		WorkspaceID: wsID,
		TabType:     t.GetTabType(),
		TabID:       t.GetTabId(),
		WorkerID:    t.GetWorkerId().GetValue(),
		TileID:      tile,
		Position:    t.GetPosition().GetValue(),
	}
	if leafLive && tileIsLeaf(state, tile) {
		return row, row
	}
	return row, nil
}
