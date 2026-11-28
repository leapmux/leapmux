package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
)

// runLayoutSet replaces the workspace's main layout with a tree read
// from a file or standard input. Implementation, per the plan's
// "wholesale rewrite under set-once / root-protection constraints":
//
//  1. Mutate the workspace root NodeRecord in place to match the
//     target's root (kind / direction / ratios / rows / cols).
//  2. Tombstone every existing non-root descendant of the root,
//     leaves-first (so ancestor tombstones never break the chain
//     before their descendants are tombstoned).
//  3. Create new nodes for the target tree with parent_id chains
//     anchored at the existing root.
//  4. Re-point every live tab anchored in the replaced main tree
//     (the workspace root and its descendants) to the first leaf of
//     the new tree via `SetTabRegister(tile_id=…)`. Tabs inside live
//     floating windows stay in place.
//
// The root_node_id never changes.
//
// Concurrent modification: between our bootstrap snapshot and the
// commit, another client could land a tab on a tile we're about to
// tombstone. Our op batch didn't know about it, so post-batch state
// would have a live tab pointing at a tombstoned leaf -- the hub's
// `tabPlacementCheck` rejects that with TAB_PLACEMENT_INVALID. The
// retry loop re-bootstraps the snapshot (which picks up the racing
// tab) and rebuilds the op batch so the second attempt includes a
// tile_id update for it. After `layoutSetMaxAttempts` retries, the
// command gives up and emits `conflict` instead of the opaque
// batch-rejected envelope.
//
// Input JSON shape (matches `layout get`'s output):
//
//	{ "kind": "leaf" | "split" | "grid",
//	  "direction": "horizontal" | "vertical",
//	  "ratios": [0.5, 0.5],
//	  "rows": 2, "cols": 2,
//	  "row_ratios": [0.5, 0.5], "col_ratios": [0.5, 0.5],
//	  "children": [ {…}, {…} ] }
func runLayoutSet(inv *cli.Invocation) (any, error) {
	target, err := readTargetTree(inv.Arg("file"), inv.Stdin)
	if err != nil {
		var cliErr *cli.Error
		if errors.As(err, &cliErr) {
			return nil, err
		}
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ws, err := s.resolveExistingWorkspace(inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	return s.submitLayoutSetWithRetry(ws.ID, target)
}

// layoutSetResult is the result of `layout set`.
type layoutSetResult struct {
	WorkspaceID    string `json:"workspace_id"`
	RootNodeID     string `json:"root_node_id"`
	NewFirstLeafID string `json:"new_first_leaf_id"`
	OpsEmitted     int    `json:"ops_emitted"`
	Attempts       int    `json:"attempts"`
}

func (r *layoutSetResult) QuietLines() []string {
	if r == nil || r.RootNodeID == "" {
		return nil
	}
	return []string{r.RootNodeID}
}

// layoutSetMaxAttempts caps the rebuild-and-retry loop on
// TAB_PLACEMENT_INVALID rejections. One retry covers the common race
// (a single tab opens while we're computing the batch); a second
// race during the retry's own snapshot window is improbable enough
// that we surface it to the user rather than looping further.
const layoutSetMaxAttempts = 2

// submitLayoutSetWithRetry opens a CRDT call for workspaceID and submits a
// main-tree rewrite with retry on TAB_PLACEMENT_INVALID.
func (s *session) submitLayoutSetWithRetry(workspaceID string, target *layoutNode) (*layoutSetResult, error) {
	cc, err := s.openCRDT(workspaceID)
	if err != nil {
		return nil, err
	}
	_, res, err := submitLayoutRewrite(cc, workspaceID, target, nil)
	return res, err
}

// submitLayoutRewriteWithRetry rewrites workspaceID's main layout to target
// (appending any extra ops produced for the current snapshot) and retries up to
// layoutSetMaxAttempts times on BATCH_REJECTION_TAB_PLACEMENT_INVALID.
func submitLayoutRewriteWithRetry(
	cc *crdtCall,
	workspaceID string,
	target *layoutNode,
	extra func(bs *CRDTBootstrap, firstLeaf string, tileMap map[string]string) []*leapmuxv1.CrdtOp,
) (tileMap map[string]string, firstLeaf string, err error) {
	tileMap, res, err := submitLayoutRewrite(cc, workspaceID, target, extra)
	if err != nil {
		return nil, "", err
	}
	return tileMap, res.NewFirstLeafID, nil
}

func submitLayoutRewrite(
	cc *crdtCall,
	workspaceID string,
	target *layoutNode,
	extra func(bs *CRDTBootstrap, firstLeaf string, tileMap map[string]string) []*leapmuxv1.CrdtOp,
) (map[string]string, *layoutSetResult, error) {
	var lastErr error
	for attempt := range layoutSetMaxAttempts {
		if attempt > 0 || cc.bs == nil {
			bs, err := crdtBootstrap(cc.ctx, cc.c, []string{workspaceID})
			if err != nil {
				return nil, nil, err
			}
			cc.bs = bs
		}
		ops, rootID, firstLeaf, tileMap, berr := buildLayoutSetOpsWithMap(cc.bs, workspaceID, target)
		if berr != nil {
			return nil, nil, berr
		}
		if extra != nil {
			ops = append(ops, extra(cc.bs, firstLeaf, tileMap)...)
		}
		committed, reason, submitErr := cc.trySubmitOps(ops)
		if committed {
			return tileMap, &layoutSetResult{
				WorkspaceID:    workspaceID,
				RootNodeID:     rootID,
				NewFirstLeafID: firstLeaf,
				OpsEmitted:     len(ops),
				Attempts:       attempt + 1,
			}, nil
		}
		lastErr = submitErr
		if reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_PLACEMENT_INVALID {
			// Not a race -- a malformed batch, value-domain, or auth failure
			// will not fix itself on retry. Surface it immediately.
			return nil, nil, submitErr
		}
		// TAB_PLACEMENT_INVALID under a layout-rewrite batch means another
		// client added a tab between our snapshot and the commit. Loop and
		// re-bootstrap.
	}
	return nil, nil, cli.Newf(cli.CodeConflict, "another client changed the layout during each of %d attempts (last hub error: %v)", layoutSetMaxAttempts, lastErr).
		WithHint("run the same layout set again")
}

// buildLayoutSetOps assembles the full 4-step rewrite batch against `bs`.
// Returns the ops, the (unchanged) workspace root id, the first leaf of the
// new tree (where live main-tree tabs land), and an error. Tabs anchored in
// live floating windows stay untouched.
func buildLayoutSetOps(bs *CRDTBootstrap, workspaceID string, target *layoutNode) ([]*leapmuxv1.CrdtOp, string, string, error) {
	ops, rootID, firstLeaf, _, err := buildLayoutSetOpsWithMap(bs, workspaceID, target)
	return ops, rootID, firstLeaf, err
}

// buildLayoutSetOpsWithMap is the tile-map-preserving variant of
// buildLayoutSetOps. It maps each leaf node's internal TileRef (when set) to
// the newly synthesized (or kept root) CRDT node ID.
func buildLayoutSetOpsWithMap(bs *CRDTBootstrap, workspaceID string, target *layoutNode) (ops []*leapmuxv1.CrdtOp, rootID, firstLeaf string, tileMap map[string]string, err error) {
	rec := bs.State.GetWorkspaces()[workspaceID]
	if rec == nil || rec.GetRootNodeId() == "" {
		return nil, "", "", nil, cli.Newf(cli.CodeConflict, "workspace has no root node yet")
	}
	rootID = rec.GetRootNodeId()
	tileMap = map[string]string{}
	ops = []*leapmuxv1.CrdtOp{}

	// Step 1: mutate the existing root to match the target's root.
	ops = appendNodeShapeOps(ops, bs, rootID, target)

	// Step 2: tombstone every existing non-root descendant (leaves-
	// first). `DescendantsLeavesFirst` returns the chain leaves-first
	// followed by the node itself; drop the rootID entry so it stays.
	// Record every node belonging to the replaced main tree so Step 4
	// migrates only tabs anchored there and leaves floating-window tabs
	// untouched.
	tombstones := crdt.DescendantsLeavesFirst(bs.State, rootID)
	replacedNodes := make(map[string]bool, len(tombstones)+1)
	replacedNodes[rootID] = true
	for _, nid := range tombstones {
		replacedNodes[nid] = true
		if nid == rootID {
			continue
		}
		ops = append(ops, opTombstoneNode(bs, nid))
	}

	// Step 3: create new nodes mirroring the target's children under
	// the (kept) root.
	if !target.isLeaf() {
		var creationOps []*leapmuxv1.CrdtOp
		creationOps, firstLeaf = synthesizeChildren(bs, rootID, target, tileMap)
		ops = append(ops, creationOps...)
	} else {
		firstLeaf = rootID
		if target.TileRef != "" {
			tileMap[target.TileRef] = rootID
		}
	}

	// Step 4: migrate live tabs anchored in the replaced main tree to
	// the first leaf of the new tree. Tabs inside live floating windows
	// sit on separate floating-window roots, so their tiles remain live
	// and untouched. The retry loop re-runs this step against a fresh
	// snapshot so racing main-tree tabs are picked up on the second
	// attempt.
	for _, t := range bs.State.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if !replacedNodes[t.GetTileId().GetValue()] {
			continue
		}
		ops = append(ops, opSetTabTileID(bs, t.GetTabType(), t.GetTabId(), firstLeaf))
	}
	return ops, rootID, firstLeaf, tileMap, nil
}

func (t *layoutNode) isLeaf() bool {
	return t != nil && t.parsedKind() == leapmuxv1.NodeKind_NODE_KIND_LEAF
}

func (t *layoutNode) parsedKind() leapmuxv1.NodeKind {
	if t == nil {
		return leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED
	}
	switch t.Kind {
	case "leaf":
		return leapmuxv1.NodeKind_NODE_KIND_LEAF
	case "split":
		return leapmuxv1.NodeKind_NODE_KIND_SPLIT
	case "grid":
		return leapmuxv1.NodeKind_NODE_KIND_GRID
	}
	return leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED
}

func (t *layoutNode) parsedDirection() leapmuxv1.SplitDirection {
	if t == nil {
		return leapmuxv1.SplitDirection_SPLIT_DIRECTION_UNSPECIFIED
	}
	dir, _ := parseSplitDirection(t.Direction)
	return dir
}

// readTargetTree reads the tree from path, or from stdin when path is "" or
// "-", then validates and normalizes it.
func readTargetTree(path string, stdin io.Reader) (*layoutNode, error) {
	if path == "" {
		path = "-"
	}
	data, err := readFileOrStdin(stdin, path, "tree file")
	if err != nil {
		return nil, err
	}
	return decodeTargetTreeBytes(data)
}

// decodeTargetTree decodes JSON from r into a layoutNode, then runs
// the structural + ratio validator. Kept separate from readTargetTree
// so unit tests can feed bytes directly without touching the
// filesystem.
//
// It accepts what `layout get` prints, at every level a caller might keep:
// the whole --json envelope ({"data": {"tree": ...}}), its data
// ({"tree": ...}), or the tree alone. A saved layout therefore restores
// without an extraction step.
func decodeTargetTree(r io.Reader) (*layoutNode, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read tree: %w", err)
	}
	return decodeTargetTreeBytes(data)
}

func decodeTargetTreeBytes(data []byte) (*layoutNode, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("tree input is empty")
	}
	var err error
	data, err = unwrapLayoutTree(data)
	if err != nil {
		return nil, err
	}
	var t layoutNode
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("decode tree JSON: %w", err)
	}
	if err := validateAndNormalizeTree(&t, "root"); err != nil {
		return nil, err
	}
	return &t, nil
}

// unwrapLayoutTree returns the tree inside an envelope or a `layout get`
// result, and the input itself when it is a tree.
func unwrapLayoutTree(data []byte) ([]byte, error) {
	for range 2 {
		var probe struct {
			Kind *string         `json:"kind"`
			Data json.RawMessage `json:"data"`
			Tree json.RawMessage `json:"tree"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			return nil, fmt.Errorf("decode tree JSON: %w", err)
		}
		switch {
		case probe.Kind != nil:
			return data, nil
		case len(probe.Tree) > 0:
			return probe.Tree, nil
		case len(probe.Data) > 0:
			data = probe.Data
		default:
			return data, nil
		}
	}
	return data, nil
}

// validateOptionalRatios checks and normalizes an optional ratio slice in
// place when non-empty.
func validateOptionalRatios(ratios *[]float64, expected int, path, label, countPhrase string) error {
	if len(*ratios) == 0 {
		return nil
	}
	if len(*ratios) != expected {
		return fmt.Errorf("%s: %s length %d %s %d", path, label, len(*ratios), countPhrase, expected)
	}
	normalized, err := normalizeRatios(*ratios)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", path, label, err)
	}
	*ratios = normalized
	return nil
}

// validateAndNormalizeTree walks the parsed tree and rejects anything
// the CRDT validator would later reject as a batch (with a far less
// helpful generic BATCH_REJECTION_* error). Validates kind, structural
// constraints per kind, ratio shape vs child count, rows/cols caps,
// and that ratios are finite + non-negative. Normalizes every ratio
// list in place so the emitted ops carry sum-exactly-1.0 values that
// pass the server's 1e-9 tolerance regardless of the user's input
// precision.
//
// path is a dotted breadcrumb ("root", "root.children[2]") so the
// error message tells the user exactly which subtree is wrong.
func validateAndNormalizeTree(t *layoutNode, path string) error {
	if t == nil {
		return fmt.Errorf("%s: node is null", path)
	}
	kind := t.parsedKind()
	if kind == leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED {
		return fmt.Errorf("%s: unrecognized kind %q (want one of leaf/split/grid)", path, t.Kind)
	}
	switch kind {
	case leapmuxv1.NodeKind_NODE_KIND_LEAF:
		if len(t.Children) > 0 {
			return fmt.Errorf("%s: LEAF nodes cannot have children (got %d)", path, len(t.Children))
		}
		if t.Direction != "" {
			return fmt.Errorf("%s: LEAF nodes cannot specify direction", path)
		}
		if len(t.Ratios) > 0 {
			return fmt.Errorf("%s: LEAF nodes cannot specify ratios", path)
		}
		if t.Rows != 0 || t.Cols != 0 {
			return fmt.Errorf("%s: LEAF nodes cannot specify rows/cols", path)
		}
		if len(t.RowRatios) > 0 || len(t.ColRatios) > 0 {
			return fmt.Errorf("%s: LEAF nodes cannot specify row_ratios/col_ratios", path)
		}
	case leapmuxv1.NodeKind_NODE_KIND_SPLIT:
		if len(t.Children) < 2 {
			return fmt.Errorf("%s: SPLIT requires at least 2 children (got %d)", path, len(t.Children))
		}
		if t.parsedDirection() == leapmuxv1.SplitDirection_SPLIT_DIRECTION_UNSPECIFIED {
			return fmt.Errorf("%s: SPLIT requires direction=horizontal|vertical (got %q)", path, t.Direction)
		}
		if t.Rows != 0 || t.Cols != 0 {
			return fmt.Errorf("%s: SPLIT nodes cannot specify rows/cols (those are for GRID)", path)
		}
		if len(t.RowRatios) > 0 || len(t.ColRatios) > 0 {
			return fmt.Errorf("%s: SPLIT nodes cannot specify row_ratios/col_ratios (those are for GRID)", path)
		}
		// The writer uses equal weights when the input omits ratios.
		// Explicit ratios must have one value per child.
		if err := validateOptionalRatios(&t.Ratios, len(t.Children), path, "SPLIT ratios", "must match children count"); err != nil {
			return err
		}
	case leapmuxv1.NodeKind_NODE_KIND_GRID:
		if t.Rows == 0 || t.Cols == 0 {
			return fmt.Errorf("%s: GRID requires rows>=1 and cols>=1 (got rows=%d cols=%d)", path, t.Rows, t.Cols)
		}
		if t.Rows > crdt.MaxGridDimension || t.Cols > crdt.MaxGridDimension {
			return fmt.Errorf("%s: GRID rows/cols capped at %d (got rows=%d cols=%d)", path, crdt.MaxGridDimension, t.Rows, t.Cols)
		}
		if t.Direction != "" {
			return fmt.Errorf("%s: GRID nodes cannot specify direction (that's for SPLIT)", path)
		}
		if len(t.Ratios) > 0 {
			return fmt.Errorf("%s: GRID nodes cannot specify ratios; use row_ratios + col_ratios", path)
		}
		expected := int(t.Rows) * int(t.Cols)
		if len(t.Children) != expected {
			return fmt.Errorf("%s: GRID with rows=%d cols=%d requires %d cells (got %d)", path, t.Rows, t.Cols, expected, len(t.Children))
		}
		if err := validateOptionalRatios(&t.RowRatios, int(t.Rows), path, "GRID row_ratios", "must equal rows"); err != nil {
			return err
		}
		if err := validateOptionalRatios(&t.ColRatios, int(t.Cols), path, "GRID col_ratios", "must equal cols"); err != nil {
			return err
		}
	default:
		// UNSPECIFIED is rejected by the guard above this switch, so no other
		// kind reaches here.
	}
	for i, child := range t.Children {
		if err := validateAndNormalizeTree(child, fmt.Sprintf("%s.children[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

// appendNodeShapeOps emits the register writes needed to make
// `nodeID`'s shape match `target`'s kind+attributes. The kind switch
// rewrites in place even when the previous kind differed.
func appendNodeShapeOps(ops []*leapmuxv1.CrdtOp, bs *CRDTBootstrap, nodeID string, target *layoutNode) []*leapmuxv1.CrdtOp {
	kind := target.parsedKind()
	ops = append(ops, opSetNodeKind(bs, nodeID, kind))
	switch kind {
	case leapmuxv1.NodeKind_NODE_KIND_SPLIT:
		if dir := target.parsedDirection(); dir != leapmuxv1.SplitDirection_SPLIT_DIRECTION_UNSPECIFIED {
			ops = append(ops, opSetNodeDirection(bs, nodeID, dir))
		}
		ops = append(ops, opSetNodeRatios(bs, nodeID, ratiosForLayoutSize(target.Ratios, len(target.Children))))
	case leapmuxv1.NodeKind_NODE_KIND_GRID:
		ops = append(ops,
			opSetNodeRows(bs, nodeID, target.Rows),
			opSetNodeCols(bs, nodeID, target.Cols),
			opSetNodeRowRatios(bs, nodeID, ratiosForLayoutSize(target.RowRatios, int(target.Rows))),
			opSetNodeColRatios(bs, nodeID, ratiosForLayoutSize(target.ColRatios, int(target.Cols))),
		)
	default:
		// LEAF has no shape registers to set, and validateAndNormalizeTree
		// already rejected UNSPECIFIED, so the kind op appended above is the
		// whole shape.
	}
	return ops
}

// synthesizeSubtree creates a new CRDT node for target under parentID at pos,
// recursively synthesizes target's children, and returns the emitted ops plus
// the ID of the first leaf encountered in depth-first display order. When
// tileMap is non-nil and target is a leaf with a non-empty TileRef, it records
// tileMap[target.TileRef] = nodeID.
func synthesizeSubtree(bs *CRDTBootstrap, target *layoutNode, parentID, pos string, tileMap map[string]string) ([]*leapmuxv1.CrdtOp, string) {
	nodeID := id.Generate()
	ops := make([]*leapmuxv1.CrdtOp, 0, 4)
	if parentID != "" {
		ops = append(ops, opSetNodeParentID(bs, nodeID, parentID))
	}
	if pos != "" {
		ops = append(ops, opSetNodePosition(bs, nodeID, pos))
	}
	ops = appendNodeShapeOps(ops, bs, nodeID, target)
	if target.isLeaf() {
		if tileMap != nil && target.TileRef != "" {
			tileMap[target.TileRef] = nodeID
		}
		return ops, nodeID
	}
	childOps, firstLeaf := synthesizeChildren(bs, nodeID, target, tileMap)
	ops = append(ops, childOps...)
	return ops, firstLeaf
}

// synthesizeChildren synthesizes each child of parent under parentID in
// display order (grid coordinates for GRID, LexoRank for SPLIT) and returns
// the emitted ops plus the first leaf encountered.
func synthesizeChildren(bs *CRDTBootstrap, parentID string, parent *layoutNode, tileMap map[string]string) (ops []*leapmuxv1.CrdtOp, firstLeaf string) {
	rank := lexorank.First()
	grid := parent.parsedKind() == leapmuxv1.NodeKind_NODE_KIND_GRID
	for index, child := range parent.Children {
		position := rank
		if grid {
			cols := int(parent.Cols)
			position = crdt.GridCellPosition(index/cols, index%cols)
		} else {
			rank = lexorank.After(rank)
		}
		childOps, childFirst := synthesizeSubtree(bs, child, parentID, position, tileMap)
		ops = append(ops, childOps...)
		if firstLeaf == "" && childFirst != "" {
			firstLeaf = childFirst
		}
	}
	return ops, firstLeaf
}
