package crdt

import (
	"fmt"
	"slices"
	"sort"
	"unicode/utf16"
	"unicode/utf8"

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
// caller can't see). Works against both `UserCrdtState` and
// `UserMaterialized` because both protos store their workspaces and
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
// This is the shared core for the two callers that need to know
// "which workspace does this node/tile/window root live in":
// FindRootWorkspace (external CLI / hub locate) and nodeWorkspace
// (validate). Intermediate tombstones are NOT a stop condition, and
// this function reports no liveness at all — a caller that needs it
// must check the chain itself. `project.go`'s resolveTileWorkspace
// used to be the third caller and now does its own single walk,
// precisely so it can answer placement and liveness together.
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
// Mirrors `BuildLiveChildrenIndex` (which operates on UserCrdtState)
// so callers that already have an UserMaterialized projection
// (the CLI's bootstrap snapshot) don't need to convert.
func LiveChildrenByParent(state *leapmuxv1.UserMaterialized) map[string][]string {
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
// `UserCrdtState` variant of LiveChildrenByParent — use this when
// you're walking the raw post-batch projection. Prefer
// BuildAllChildrenIndex when the caller filters tombstones itself
// and needs to include tombstoned nodes in the index.
func BuildLiveChildrenIndex(state *leapmuxv1.UserCrdtState) map[string][]string {
	return buildChildrenIndex(state, false)
}

// BuildAllChildrenIndex builds the parent→children adjacency over
// every node in `state`, INCLUDING tombstoned ones. Callers that
// project materialized state (validator, node→workspace walker) need
// the tombstoned intermediates so descendants whose direct parent was
// tombstoned still chain to the live grandparent.
func BuildAllChildrenIndex(state *leapmuxv1.UserCrdtState) map[string][]string {
	return buildChildrenIndex(state, true)
}

func buildChildrenIndex(state *leapmuxv1.UserCrdtState, includeTombstoned bool) map[string][]string {
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

// GridCellPosition returns the position register for one grid cell.
func GridCellPosition(row, col int) string {
	return fmt.Sprintf("%d,%d", row, col)
}

// compareLayoutString follows the browser's UTF-16 code unit order without
// allocating temporary rune or UTF-16 slices.
func compareLayoutString(a, b string) int {
	var pendA, pendB uint16
	for {
		var uA, uB uint16
		var okA, okB bool
		uA, a, pendA, okA = nextUTF16Unit(a, pendA)
		uB, b, pendB, okB = nextUTF16Unit(b, pendB)
		if !okA && !okB {
			return 0
		}
		if !okA {
			return -1
		}
		if !okB {
			return 1
		}
		if uA != uB {
			if uA < uB {
				return -1
			}
			return 1
		}
	}
}

func nextUTF16Unit(s string, pending uint16) (unit uint16, rest string, nextPending uint16, ok bool) {
	if pending != 0 {
		return pending, s, 0, true
	}
	if len(s) == 0 {
		return 0, "", 0, false
	}
	if s[0] < utf8.RuneSelf {
		return uint16(s[0]), s[1:], 0, true
	}
	r, size := utf8.DecodeRuneInString(s)
	if r <= 0xFFFF {
		return uint16(r), s[size:], 0, true
	}
	r1, r2 := utf16.EncodeRune(r)
	return uint16(r1), s[size:], uint16(r2), true
}

// LayoutChildIDs returns the children in display order. It takes a live
// child index from either the canonical or materialized state. The result
// includes an empty ID for each vacant grid cell.
func LayoutChildIDs(nodes map[string]*leapmuxv1.NodeRecord, index map[string][]string, parentID string) []string {
	parent := nodes[parentID]
	if parent.GetKind().GetValue() == leapmuxv1.NodeKind_NODE_KIND_GRID {
		return gridChildIDs(nodes, index[parentID], parent.GetRows().GetValue(), parent.GetCols().GetValue())
	}
	ids := slices.Clone(index[parentID])
	slices.SortFunc(ids, func(a, b string) int {
		if order := compareLayoutString(nodes[a].GetPosition().GetValue(), nodes[b].GetPosition().GetValue()); order != 0 {
			return order
		}
		return compareLayoutString(a, b)
	})
	return ids
}

func gridChildIDs(nodes map[string]*leapmuxv1.NodeRecord, children []string, rows, cols uint32) []string {
	if rows > MaxGridDimension || cols > MaxGridDimension {
		return nil
	}
	ordered := make([]string, int(rows*cols))
	for _, childID := range children {
		idx, ok := parseGridCellIndex(nodes[childID].GetPosition().GetValue(), rows, cols)
		if !ok {
			continue
		}
		if other := ordered[idx]; other == "" || compareLayoutString(childID, other) < 0 {
			ordered[idx] = childID
		}
	}
	return ordered
}

// parseGridCellIndex parses canonical GridCellPosition ("row,col") strings into
// a flat row-major slice index for a rows x cols grid. Non-canonical strings
// (leading zeros, signs, whitespace, or out-of-range coordinates) return false.
func parseGridCellIndex(pos string, rows, cols uint32) (int, bool) {
	row, rest, ok := parseCanonicalGridCoord(pos)
	if !ok || len(rest) == 0 || rest[0] != ',' || row >= rows {
		return 0, false
	}
	col, tail, ok := parseCanonicalGridCoord(rest[1:])
	if !ok || len(tail) != 0 || col >= cols {
		return 0, false
	}
	return int(row*cols + col), true
}

func parseCanonicalGridCoord(s string) (uint32, string, bool) {
	if len(s) == 0 || s[0] < '0' || s[0] > '9' {
		return 0, "", false
	}
	if s[0] == '0' {
		if len(s) > 1 && s[1] >= '0' && s[1] <= '9' {
			return 0, "", false
		}
		return 0, s[1:], true
	}
	var v uint32
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		v = v*10 + uint32(s[i]-'0')
		if v > MaxGridDimension {
			return 0, "", false
		}
		i++
	}
	return v, s[i:], true
}

// FirstLiveLayoutLeaf returns the first leaf that can receive a tab. It
// skips vacant cells and visits each node at most once.
func FirstLiveLayoutLeaf(nodes map[string]*leapmuxv1.NodeRecord, index map[string][]string, rootID string) string {
	visited := map[string]bool{}
	var walk func(string) string
	walk = func(nodeID string) string {
		if nodeID == "" || visited[nodeID] {
			return ""
		}
		visited[nodeID] = true
		node := nodes[nodeID]
		if node == nil || !HLCIsZero(node.GetTombstoneAt()) {
			return ""
		}
		if node.GetKind().GetValue() == leapmuxv1.NodeKind_NODE_KIND_LEAF {
			return nodeID
		}
		for _, childID := range LayoutChildIDs(nodes, index, nodeID) {
			if leaf := walk(childID); leaf != "" {
				return leaf
			}
		}
		return ""
	}
	return walk(rootID)
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
func DescendantsLeavesFirst(state *leapmuxv1.UserMaterialized, nodeID string) []string {
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
func TabsOnTile(state *leapmuxv1.UserMaterialized, tileID string) []TabRef {
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
func LiveTabsInSubtree(state *leapmuxv1.UserMaterialized, subtreeRoot string) []TabRef {
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
// It follows the browser's display order and skips vacant cells because
// they have no tile that can receive a tab.
func FindHeirTileID(state *leapmuxv1.UserMaterialized, closingTileID, rootNodeID string) string {
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
		siblings := LayoutChildIDs(state.GetNodes(), index, parentID)
		siblings = slices.DeleteFunc(siblings, func(id string) bool { return id == "" })
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
		if leaf := FirstLiveLayoutLeaf(state.GetNodes(), index, adj); leaf != "" {
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
func buildPathToNode(state *leapmuxv1.UserMaterialized, index map[string][]string, rootID, targetID string, out *[]string) bool {
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
