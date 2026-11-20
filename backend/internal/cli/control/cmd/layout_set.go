package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
)

// RunLayoutSet replaces the workspace's main layout with a tree read
// from `--file` or `--stdin`. Implementation, per the plan's
// "wholesale rewrite under set-once / root-protection constraints":
//
//  1. Mutate the workspace root NodeRecord in place to match the
//     target's root (kind / direction / ratios / rows / cols).
//  2. Tombstone every existing non-root descendant of the root,
//     leaves-first (so ancestor tombstones never break the chain
//     before their descendants are tombstoned).
//  3. Create new nodes for the target tree with parent_id chains
//     anchored at the existing root.
//  4. Re-point every existing live tab to the first leaf of the new
//     tree via `SetTabRegister(tile_id=…)`.
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
// command gives up and emits `concurrent_modification` instead of
// the opaque batch-rejected envelope.
//
// Input JSON shape (matches `layout get`'s output):
//
//	{ "kind": "NODE_KIND_LEAF" | "NODE_KIND_SPLIT" | "NODE_KIND_GRID",
//	  "direction": "SPLIT_DIRECTION_HORIZONTAL" | "..._VERTICAL",
//	  "ratios": [0.5, 0.5],
//	  "rows": 2, "cols": 2,
//	  "row_ratios": [0.5, 0.5], "col_ratios": [0.5, 0.5],
//	  "children": [ {…}, {…} ] }
func runLayoutSet(inv *cli.Invocation) (any, error) {
	target, err := readTargetTree(inv.Arg("file"), inv.Stdin)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ws, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
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

// layoutSetMaxAttempts caps the rebuild-and-retry loop on
// TAB_PLACEMENT_INVALID rejections. One retry covers the common race
// (a single tab opens while we're computing the batch); a second
// race during the retry's own snapshot window is improbable enough
// that we surface it to the user rather than looping further.
const layoutSetMaxAttempts = 2

// submitLayoutSetWithRetry runs the bootstrap -> build -> submit
// cycle up to layoutSetMaxAttempts times. The retry trigger is the
// concurrent-modification rejection (TAB_PLACEMENT_INVALID): a tab
// that raced in between bootstrap and submit appears in the next
// snapshot, so the rebuilt batch includes a tile_id update for it.
// Every other error is fatal on the first attempt.
func (s *session) submitLayoutSetWithRetry(workspaceID string, target *layoutNode) (*layoutSetResult, error) {
	var lastErr error
	for attempt := 0; attempt < layoutSetMaxAttempts; attempt++ {
		cc, err := s.openCRDT(workspaceID)
		if err != nil {
			return nil, err
		}
		ops, rootID, firstLeaf, berr := buildLayoutSetOps(cc.bs, workspaceID, target)
		if berr != nil {
			return nil, berr
		}
		committed, reason, submitErr := cc.trySubmitOps(ops)
		if committed {
			return &layoutSetResult{
				WorkspaceID:    workspaceID,
				RootNodeID:     rootID,
				NewFirstLeafID: firstLeaf,
				OpsEmitted:     len(ops),
				Attempts:       attempt + 1,
			}, nil
		}
		lastErr = submitErr
		if reason != leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_PLACEMENT_INVALID {
			// Not a race -- a malformed batch / value-domain / auth
			// failure won't fix itself on retry. Surface immediately.
			return nil, cli.Wrap(cli.CodeConflict, submitErr, "")
		}
		// TAB_PLACEMENT_INVALID under a layout-set batch means another
		// client added a tab between our snapshot and the commit. Loop
		// and re-bootstrap.
	}
	return nil, cli.Newf(cli.CodeConflict, "another client changed the layout during each of %d attempts (last hub error: %v)", layoutSetMaxAttempts, lastErr).
		WithHint("run the same layout set again")
}

// buildLayoutSetOps assembles the full 4-step rewrite batch against
// `bs`. Returns the ops, the (unchanged) workspace root id, the first
// leaf of the new tree (where every live tab lands), and an error.
// Extracted from runLayoutSet so the retry loop can rebuild the batch
// against a fresh snapshot without duplicating the step logic.
func buildLayoutSetOps(bs *CRDTBootstrap, workspaceID string, target *layoutNode) ([]*leapmuxv1.CrdtOp, string, string, error) {
	rec := bs.State.GetWorkspaces()[workspaceID]
	if rec == nil || rec.GetRootNodeId() == "" {
		return nil, "", "", cli.Newf(cli.CodeConflict, "workspace has no root node yet")
	}
	rootID := rec.GetRootNodeId()

	ops := []*leapmuxv1.CrdtOp{}

	// Step 1: mutate the existing root to match the target's root.
	ops = appendNodeShapeOps(ops, bs, rootID, target)

	// Step 2: tombstone every existing non-root descendant (leaves-
	// first). `descendantsLeavesFirst` returns the chain leaves-first
	// followed by the node itself; drop the rootID entry so it stays.
	tombstones := crdt.DescendantsLeavesFirst(bs.State, rootID)
	for _, nid := range tombstones {
		if nid == rootID {
			continue
		}
		ops = append(ops, opTombstoneNode(bs, nid))
	}

	// Step 3: create new nodes mirroring the target's children under
	// the (kept) root.
	firstLeaf := ""
	if !target.isLeaf() {
		var creationOps []*leapmuxv1.CrdtOp
		creationOps, firstLeaf = synthesizeSubtree(bs, rootID, target.Children)
		ops = append(ops, creationOps...)
	} else {
		firstLeaf = rootID
	}

	// Step 4: migrate every existing live tab to the first leaf of the
	// new tree. Tabs were previously anchored to soon-to-be-tombstoned
	// leaves; without this they'd violate the "no orphaned live tabs"
	// invariant and the whole batch would be rejected. The retry loop
	// re-runs this step against a fresh snapshot so racing tabs are
	// picked up on the second attempt.
	for _, t := range bs.State.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		ops = append(ops, opSetTabTileID(bs, t.GetTabType(), t.GetTabId(), firstLeaf))
	}
	return ops, rootID, firstLeaf, nil
}

func (t *layoutNode) isLeaf() bool {
	return t != nil && t.parsedKind() == leapmuxv1.NodeKind_NODE_KIND_LEAF
}

func (t *layoutNode) parsedKind() leapmuxv1.NodeKind {
	if t == nil {
		return leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED
	}
	switch t.Kind {
	case "NODE_KIND_LEAF", "leaf", "LEAF":
		return leapmuxv1.NodeKind_NODE_KIND_LEAF
	case "NODE_KIND_SPLIT", "split", "SPLIT":
		return leapmuxv1.NodeKind_NODE_KIND_SPLIT
	case "NODE_KIND_GRID", "grid", "GRID":
		return leapmuxv1.NodeKind_NODE_KIND_GRID
	}
	return leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED
}

func (t *layoutNode) parsedDirection() leapmuxv1.SplitDirection {
	switch t.Direction {
	case "SPLIT_DIRECTION_HORIZONTAL", "horizontal", "h":
		return leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL
	case "SPLIT_DIRECTION_VERTICAL", "vertical", "v":
		return leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL
	}
	return leapmuxv1.SplitDirection_SPLIT_DIRECTION_UNSPECIFIED
}

// readTargetTree reads the tree from path, or from stdin when path is "" or
// "-", then validates and normalizes it.
func readTargetTree(path string, stdin io.Reader) (*layoutNode, error) {
	reader := stdin
	if path != "" && path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("read tree file: %w", err)
		}
		defer func() { _ = f.Close() }()
		reader = f
	}
	return decodeTargetTree(reader)
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
	if len(data) == 0 {
		return nil, fmt.Errorf("tree input is empty")
	}
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
		// Ratios are optional — when omitted, appendNodeShapeOps below
		// skips the SetNodeRatios op and the renderer falls back to
		// equal weights. When present, the slice must match the child
		// count exactly so the projection's per-child fraction is
		// unambiguous.
		if len(t.Ratios) > 0 {
			if len(t.Ratios) != len(t.Children) {
				return fmt.Errorf("%s: SPLIT ratios length %d must match children count %d", path, len(t.Ratios), len(t.Children))
			}
			normalized, err := normalizeRatios(t.Ratios)
			if err != nil {
				return fmt.Errorf("%s: SPLIT ratios: %w", path, err)
			}
			t.Ratios = normalized
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
		if len(t.RowRatios) > 0 {
			if uint32(len(t.RowRatios)) != t.Rows {
				return fmt.Errorf("%s: GRID row_ratios length %d must equal rows %d", path, len(t.RowRatios), t.Rows)
			}
			normalized, err := normalizeRatios(t.RowRatios)
			if err != nil {
				return fmt.Errorf("%s: GRID row_ratios: %w", path, err)
			}
			t.RowRatios = normalized
		}
		if len(t.ColRatios) > 0 {
			if uint32(len(t.ColRatios)) != t.Cols {
				return fmt.Errorf("%s: GRID col_ratios length %d must equal cols %d", path, len(t.ColRatios), t.Cols)
			}
			normalized, err := normalizeRatios(t.ColRatios)
			if err != nil {
				return fmt.Errorf("%s: GRID col_ratios: %w", path, err)
			}
			t.ColRatios = normalized
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
		if len(target.Ratios) > 0 {
			ops = append(ops, opSetNodeRatios(bs, nodeID, target.Ratios))
		}
	case leapmuxv1.NodeKind_NODE_KIND_GRID:
		if target.Rows > 0 {
			ops = append(ops, opSetNodeRows(bs, nodeID, target.Rows))
		}
		if target.Cols > 0 {
			ops = append(ops, opSetNodeCols(bs, nodeID, target.Cols))
		}
		if len(target.RowRatios) > 0 {
			ops = append(ops, opSetNodeRowRatios(bs, nodeID, target.RowRatios))
		}
		if len(target.ColRatios) > 0 {
			ops = append(ops, opSetNodeColRatios(bs, nodeID, target.ColRatios))
		}
	default:
		// LEAF has no shape registers to set, and validateTargetNode already
		// rejected UNSPECIFIED, so the kind op appended above is the whole shape.
	}
	return ops
}

// synthesizeSubtree creates fresh nodes for `children` under
// `parentID`. Returns the emitted ops plus the id of the first leaf
// encountered (depth-first, left-to-right) — that's where tabs are
// migrated to so they pass the "tile_id resolves to a live leaf"
// validator rule.
func synthesizeSubtree(bs *CRDTBootstrap, parentID string, children []*layoutNode) (ops []*leapmuxv1.CrdtOp, firstLeaf string) {
	pos := lexorank.First()
	for _, child := range children {
		childID := id.Generate()
		ops = append(ops,
			opSetNodeKind(bs, childID, child.parsedKind()),
			opSetNodeParentID(bs, childID, parentID),
			opSetNodePosition(bs, childID, pos),
		)
		pos = lexorank.After(pos)
		ops = appendNodeShapeOps(ops, bs, childID, child)
		if child.isLeaf() && firstLeaf == "" {
			firstLeaf = childID
		}
		grandOps, deeperFirst := synthesizeSubtree(bs, childID, child.Children)
		ops = append(ops, grandOps...)
		if firstLeaf == "" && deeperFirst != "" {
			firstLeaf = deeperFirst
		}
	}
	return ops, firstLeaf
}
