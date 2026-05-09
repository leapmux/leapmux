package crdt

import (
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TabRef pairs a tab's (TabType, TabID) — the minimum identity callers
// need to address a tab in subsequent op batches without re-walking the
// state's tab map.
type TabRef struct {
	TabType leapmuxv1.TabType
	TabID   string
}

// FindRootWorkspace walks nodeID's parent_id chain to a registered
// workspace root and returns the workspace_id, or "" when the chain
// doesn't terminate at a known root (orphan, cycle, or workspace the
// caller can't see). Works against both `OrgCrdtState` and
// `OrgMaterialized` because both protos store their workspaces and
// nodes as `map<string, WorkspaceContentsRecord>` / `map<string,
// NodeRecord>` with identical shapes.
//
// A visited set guards against malformed parent cycles; projection
// repair already breaks cycles before they reach here, but defense in
// depth is cheap.
func FindRootWorkspace(
	nodes map[string]*leapmuxv1.NodeRecord,
	workspaces map[string]*leapmuxv1.WorkspaceContentsRecord,
	nodeID string,
) string {
	if nodeID == "" {
		return ""
	}
	rootToWS := make(map[string]string, len(workspaces))
	for wsID, ws := range workspaces {
		if rid := ws.GetRootNodeId(); rid != "" {
			rootToWS[rid] = wsID
		}
	}
	return resolveParentChain(rootToWS, nodeID, func(id string) (string, bool) {
		rec, ok := nodes[id]
		if !ok || rec == nil {
			return "", false
		}
		return rec.GetParentId(), true
	})
}

// resolveParentChain walks startID through `parentOf` until it hits a
// node id present in roots and returns the mapped workspace_id, or ""
// on cycles / dead ends. parentOf returns the next id ("" terminates
// without a root) and whether the current id existed; missing nodes
// abort the walk.
//
// This is the shared core for the three callers that need to know
// "which workspace does this node/tile/window root live in":
// FindRootWorkspace (external CLI / hub locate), nodeWorkspace
// (validate), resolveTileWorkspace (project). Intermediate tombstones
// are NOT a stop condition — callers that need liveness pair the
// result with chainAlive.
func resolveParentChain(roots map[string]string, startID string, parentOf func(id string) (string, bool)) string {
	visited := map[string]bool{}
	cur := startID
	for cur != "" {
		if visited[cur] {
			return ""
		}
		visited[cur] = true
		if ws, ok := roots[cur]; ok {
			return ws
		}
		parent, ok := parentOf(cur)
		if !ok {
			return ""
		}
		cur = parent
	}
	return ""
}

// LiveChildrenByParent indexes the materialized state's live nodes by
// parent_id. The adjacency is built in a single O(N) pass so callers
// that recurse into the tree avoid the O(N²) cost of re-scanning
// state.GetNodes() at every level. Tombstoned nodes are omitted from
// both keys and values.
//
// Mirrors `BuildLiveChildrenIndex` (which operates on OrgCrdtState)
// so callers that already have an OrgMaterialized projection
// (the CLI's bootstrap snapshot) don't need to convert.
func LiveChildrenByParent(state *leapmuxv1.OrgMaterialized) map[string][]string {
	idx := map[string][]string{}
	for _, n := range state.GetNodes() {
		if !HLCIsZero(n.GetTombstoneAt()) {
			continue
		}
		parent := n.GetParentId()
		idx[parent] = append(idx[parent], n.GetNodeId())
	}
	return idx
}

// BuildLiveChildrenIndex builds the parent→children adjacency over
// every live node in `state`. Tombstoned nodes are omitted from both
// keys and values. Single O(N) pass; share the result across callers
// that recurse so each level avoids re-scanning state.GetNodes().
//
// `OrgCrdtState` variant of LiveChildrenByParent — use this when
// you're walking the raw post-batch projection. Prefer
// BuildAllChildrenIndex when the caller filters tombstones itself
// and needs to include tombstoned nodes in the index.
func BuildLiveChildrenIndex(state *leapmuxv1.OrgCrdtState) map[string][]string {
	return buildChildrenIndex(state, false)
}

// BuildAllChildrenIndex builds the parent→children adjacency over
// every node in `state`, INCLUDING tombstoned ones. Callers that
// project materialized state (validator, node→workspace walker) need
// the tombstoned intermediates so descendants whose direct parent was
// tombstoned still chain to the live grandparent.
func BuildAllChildrenIndex(state *leapmuxv1.OrgCrdtState) map[string][]string {
	return buildChildrenIndex(state, true)
}

func buildChildrenIndex(state *leapmuxv1.OrgCrdtState, includeTombstoned bool) map[string][]string {
	idx := map[string][]string{}
	for nodeID, n := range state.GetNodes() {
		if !includeTombstoned && !HLCIsZero(n.GetTombstoneAt()) {
			continue
		}
		if parent := n.GetParentId(); parent != "" {
			idx[parent] = append(idx[parent], nodeID)
		}
	}
	return idx
}

// LiveOrderedChildren returns parentID's live children sorted by their
// LexoRank position register so heir-finding sees the same left/upper
// ordering the renderer does. `index` is the already-built parent →
// children adjacency from LiveChildrenByParent.
func LiveOrderedChildren(state *leapmuxv1.OrgMaterialized, index map[string][]string, parentID string) []string {
	ids := append([]string(nil), index[parentID]...)
	sort.SliceStable(ids, func(i, j int) bool {
		a := state.GetNodes()[ids[i]].GetPosition().GetValue()
		b := state.GetNodes()[ids[j]].GetPosition().GetValue()
		return a < b
	})
	return ids
}

// DescendantsLeavesFirst enumerates every live descendant of nodeID
// (the node itself included last), ordered leaves-first. Tombstoned
// nodes are skipped; cycles are broken by the visited set. Used by
// close-subtree / remove-grid handlers to tombstone descendants before
// their ancestor so the validator's parent-before-child invariants hold.
//
// The adjacency is filtered to live nodes via LiveChildrenByParent, so
// the iterative post-order walker reused with the lifecycle's
// workspace-delete enumerator (subtreePostOrder) sees the same live
// graph and skips tombstoned subtrees without extra checks.
func DescendantsLeavesFirst(state *leapmuxv1.OrgMaterialized, nodeID string) []string {
	rec := state.GetNodes()[nodeID]
	if rec == nil || !HLCIsZero(rec.GetTombstoneAt()) {
		return nil
	}
	return subtreePostOrder(LiveChildrenByParent(state), nodeID)
}

// TabsOnTile returns every live tab whose tile_id register equals
// tileID, sorted by LexoRank position (then tab_id as a stable tiebreak
// when two tabs happen to share a rank). The sort matters because
// callers re-stamp positions with a hoisted `lexorank.First()`
// advanced once per iteration, and the proto's tab map has
// non-deterministic Go iteration order — without the sort the same
// fixture would produce different post-migration orderings between runs.
func TabsOnTile(state *leapmuxv1.OrgMaterialized, tileID string) []TabRef {
	type withPos struct {
		ref TabRef
		pos string
	}
	picked := []withPos{}
	for _, t := range state.GetTabs() {
		if t == nil || !HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if t.GetTileId().GetValue() != tileID {
			continue
		}
		picked = append(picked, withPos{
			ref: TabRef{TabType: t.GetTabType(), TabID: t.GetTabId()},
			pos: t.GetPosition().GetValue(),
		})
	}
	sort.SliceStable(picked, func(i, j int) bool {
		if picked[i].pos != picked[j].pos {
			return picked[i].pos < picked[j].pos
		}
		return picked[i].ref.TabID < picked[j].ref.TabID
	})
	out := make([]TabRef, len(picked))
	for i, p := range picked {
		out[i] = p.ref
	}
	return out
}

// LiveTabsInSubtree returns every live tab anchored to any live
// descendant of subtreeRoot (including the root itself). Used to
// decide whether a destructive operation needs the user's explicit
// --with-tabs choice.
func LiveTabsInSubtree(state *leapmuxv1.OrgMaterialized, subtreeRoot string) []TabRef {
	descendants := DescendantsLeavesFirst(state, subtreeRoot)
	seen := map[string]bool{}
	for _, id := range descendants {
		seen[id] = true
	}
	out := []TabRef{}
	for _, t := range state.GetTabs() {
		if t == nil || !HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if !seen[t.GetTileId().GetValue()] {
			continue
		}
		out = append(out, TabRef{TabType: t.GetTabType(), TabID: t.GetTabId()})
	}
	return out
}

// FindHeirTileID returns the leaf tile that should inherit
// closingTileID's tabs when the user picks "Move tabs". Walks up from
// the closing leaf to its first ancestor with a sibling subtree, then
// returns the first leaf in the left/upper-preferred adjacent sibling.
// Returns "" when no heir exists (single-leaf workspace, malformed tree).
//
// Mirrors frontend/src/stores/layout.store.ts:findHeirTileId so the
// CLI and the CloseTileDialog drop tabs in the same place.
func FindHeirTileID(state *leapmuxv1.OrgMaterialized, closingTileID, rootNodeID string) string {
	if rootNodeID == "" || closingTileID == "" {
		return ""
	}
	index := LiveChildrenByParent(state)
	path := []string{}
	if !buildPathToNode(state, index, rootNodeID, closingTileID, &path) {
		return ""
	}
	for i := len(path) - 2; i >= 0; i-- {
		parentID := path[i]
		childID := path[i+1]
		siblings := LiveOrderedChildren(state, index, parentID)
		idx := -1
		for j, s := range siblings {
			if s == childID {
				idx = j
				break
			}
		}
		if idx < 0 {
			continue
		}
		var adj string
		if idx > 0 {
			adj = siblings[idx-1]
		} else if idx+1 < len(siblings) {
			adj = siblings[idx+1]
		}
		if adj == "" {
			continue
		}
		if leaf := firstLeafID(state, index, adj); leaf != "" {
			return leaf
		}
	}
	return ""
}

// buildPathToNode collects the chain of node ids from rootID down to
// targetID (inclusive of both ends) into out. Returns true when the
// target was found. Skips tombstoned nodes. Position order doesn't
// matter for path-finding because the closing tile is identified by id,
// not slot.
func buildPathToNode(state *leapmuxv1.OrgMaterialized, index map[string][]string, rootID, targetID string, out *[]string) bool {
	rec := state.GetNodes()[rootID]
	if rec == nil || !HLCIsZero(rec.GetTombstoneAt()) {
		return false
	}
	*out = append(*out, rootID)
	if rootID == targetID {
		return true
	}
	for _, childID := range index[rootID] {
		if buildPathToNode(state, index, childID, targetID, out) {
			return true
		}
	}
	*out = (*out)[:len(*out)-1]
	return false
}

// firstLeafID walks the leftmost descent of subtreeRoot (preferring the
// first LexoRank-ordered child at each level) and returns the first
// LEAF-kinded id encountered. Used by FindHeirTileID to pick the
// destination leaf inside a sibling subtree. Returns "" when the
// subtree is malformed (no live leaves).
func firstLeafID(state *leapmuxv1.OrgMaterialized, index map[string][]string, subtreeRoot string) string {
	rec := state.GetNodes()[subtreeRoot]
	if rec == nil || !HLCIsZero(rec.GetTombstoneAt()) {
		return ""
	}
	// Tile kinds: LEAF returns itself; SPLIT/GRID descend into the
	// first ordered live child. UNSPECIFIED is treated as a leaf — in
	// practice that only happens for the workspace root that was minted
	// before kind was first written, which projection-side renders as
	// a leaf anyway.
	kind := rec.GetKind().GetValue()
	if kind == leapmuxv1.NodeKind_NODE_KIND_LEAF || kind == leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED {
		return subtreeRoot
	}
	for _, childID := range LiveOrderedChildren(state, index, subtreeRoot) {
		if leaf := firstLeafID(state, index, childID); leaf != "" {
			return leaf
		}
	}
	return ""
}
