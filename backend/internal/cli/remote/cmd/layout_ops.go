package cmd

import (
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
)

// op-builder helpers for the layout/tile op set. Mirror the
// layoutOps.ts shape so frontend + CLI emit the same op shapes for
// the same intents.

// envelope returns a fresh OrgOp with org id, op id, origin client,
// and client HLC populated. Callers attach `Body` directly because
// the oneof wrapper interface is package-private to the generated
// proto package.
func envelope(bs *CRDTBootstrap) *leapmuxv1.OrgOp {
	return &leapmuxv1.OrgOp{
		OrgId:          bs.OrgID,
		OpId:           id.Generate(),
		OriginClientId: bs.OriginClient,
		ClientHlc:      bs.Clock.Tick(nowMillis()),
	}
}

// newSetNodeRegisterOp allocates the OrgOp wrapper + SetNodeRegisterOp
// inner record and links them. Callers set inner.Field to the desired
// variant. The proto generator makes the Field interface
// package-private, so per-register helpers can't share a single
// "set field" parameter type; this constructor + per-register Field
// assignment is the shortest form that doesn't require reflection.
func newSetNodeRegisterOp(bs *CRDTBootstrap, nodeID string) (*leapmuxv1.OrgOp, *leapmuxv1.SetNodeRegisterOp) {
	op := envelope(bs)
	inner := &leapmuxv1.SetNodeRegisterOp{NodeId: nodeID}
	op.Body = &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: inner}
	return op, inner
}

func opSetNodeKind(bs *CRDTBootstrap, nodeID string, kind leapmuxv1.NodeKind) *leapmuxv1.OrgOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Kind{Kind: kind}
	return op
}

func opSetNodeParentID(bs *CRDTBootstrap, nodeID, parentID string) *leapmuxv1.OrgOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: parentID}
	return op
}

func opSetNodePosition(bs *CRDTBootstrap, nodeID, position string) *leapmuxv1.OrgOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Position{Position: position}
	return op
}

func opSetNodeDirection(bs *CRDTBootstrap, nodeID string, dir leapmuxv1.SplitDirection) *leapmuxv1.OrgOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Direction{Direction: dir}
	return op
}

func opSetNodeRatios(bs *CRDTBootstrap, nodeID string, ratios []float64) *leapmuxv1.OrgOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Ratios{Ratios: &leapmuxv1.DoubleList{Values: ratios}}
	return op
}

func opSetNodeRows(bs *CRDTBootstrap, nodeID string, rows uint32) *leapmuxv1.OrgOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Rows{Rows: rows}
	return op
}

func opSetNodeCols(bs *CRDTBootstrap, nodeID string, cols uint32) *leapmuxv1.OrgOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Cols{Cols: cols}
	return op
}

func opSetNodeRowRatios(bs *CRDTBootstrap, nodeID string, values []float64) *leapmuxv1.OrgOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_RowRatios{RowRatios: &leapmuxv1.DoubleList{Values: values}}
	return op
}

func opSetNodeColRatios(bs *CRDTBootstrap, nodeID string, values []float64) *leapmuxv1.OrgOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_ColRatios{ColRatios: &leapmuxv1.DoubleList{Values: values}}
	return op
}

// kindLabel returns the short user-facing label for a NodeKind so
// error messages read "SPLIT" / "GRID" instead of the proto constant.
func kindLabel(k leapmuxv1.NodeKind) string {
	switch k {
	case leapmuxv1.NodeKind_NODE_KIND_LEAF:
		return "LEAF"
	case leapmuxv1.NodeKind_NODE_KIND_SPLIT:
		return "SPLIT"
	case leapmuxv1.NodeKind_NODE_KIND_GRID:
		return "GRID"
	}
	return k.String()
}

// buildTreeJSON projects the live subtree rooted at nodeID into a
// nested JSON shape suitable for `tile list` / `layout get`.
func buildTreeJSON(state *leapmuxv1.OrgMaterialized, nodeID string) any {
	return buildTreeJSONWith(state, crdt.LiveChildrenByParent(state), nodeID)
}

func buildTreeJSONWith(state *leapmuxv1.OrgMaterialized, children map[string][]string, nodeID string) any {
	if nodeID == "" {
		return nil
	}
	rec := state.GetNodes()[nodeID]
	if rec == nil || !crdt.HLCIsZero(rec.GetTombstoneAt()) {
		return nil
	}
	kind := rec.GetKind().GetValue()
	out := map[string]any{
		"node_id": nodeID,
		"kind":    kind.String(),
	}
	switch kind {
	case leapmuxv1.NodeKind_NODE_KIND_SPLIT:
		out["direction"] = rec.GetDirection().GetValue().String()
		out["ratios"] = rec.GetRatios().GetValue().GetValues()
	case leapmuxv1.NodeKind_NODE_KIND_GRID:
		out["rows"] = rec.GetRows().GetValue()
		out["cols"] = rec.GetCols().GetValue()
		out["row_ratios"] = rec.GetRowRatios().GetValue().GetValues()
		out["col_ratios"] = rec.GetColRatios().GetValue().GetValues()
	}
	childJSON := []any{}
	for _, childID := range children[nodeID] {
		if c := buildTreeJSONWith(state, children, childID); c != nil {
			childJSON = append(childJSON, c)
		}
	}
	if len(childJSON) > 0 {
		out["children"] = childJSON
	}
	return out
}

// firstLiveLeaf walks the subtree rooted at rootNodeID and returns the
// id of the first live leaf in depth-first order, with children
// visited in (position, node_id) order so the result matches the
// frontend's render-time leaf ordering. Returns "" when rootNodeID is
// empty, tombstoned, missing, or has no reachable live leaf.
func firstLiveLeaf(state *leapmuxv1.OrgMaterialized, rootNodeID string) string {
	if rootNodeID == "" {
		return ""
	}
	children := crdt.LiveChildrenByParent(state)
	sorted := map[string]bool{}
	sortKids := func(parent string) {
		if sorted[parent] {
			return
		}
		kids := children[parent]
		sort.Slice(kids, func(i, j int) bool {
			ri, rj := state.GetNodes()[kids[i]], state.GetNodes()[kids[j]]
			pi, pj := ri.GetPosition().GetValue(), rj.GetPosition().GetValue()
			if pi != pj {
				return pi < pj
			}
			return kids[i] < kids[j]
		})
		sorted[parent] = true
	}
	visited := map[string]bool{}
	var walk func(string) string
	walk = func(nodeID string) string {
		if nodeID == "" || visited[nodeID] {
			return ""
		}
		visited[nodeID] = true
		rec := state.GetNodes()[nodeID]
		if rec == nil || !crdt.HLCIsZero(rec.GetTombstoneAt()) {
			return ""
		}
		if rec.GetKind().GetValue() == leapmuxv1.NodeKind_NODE_KIND_LEAF {
			return nodeID
		}
		sortKids(nodeID)
		for _, child := range children[nodeID] {
			if leaf := walk(child); leaf != "" {
				return leaf
			}
		}
		return ""
	}
	return walk(rootNodeID)
}
