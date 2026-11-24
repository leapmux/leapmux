package cmd

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
)

// op-builder helpers for the layout/tile op set. Mirror the
// layoutOps.ts shape so frontend + CLI emit the same op shapes for
// the same intents.

// envelope returns a fresh CrdtOp with op id, origin client,
// and client HLC populated. Callers attach `Body` directly because
// the oneof wrapper interface is package-private to the generated
// proto package.
func envelope(bs *CRDTBootstrap) *leapmuxv1.CrdtOp {
	return &leapmuxv1.CrdtOp{
		OpId:           id.Generate(),
		OriginClientId: bs.OriginClient,
		ClientHlc:      bs.Clock.Tick(nowMillis()),
	}
}

// newSetNodeRegisterOp allocates the CrdtOp wrapper + SetNodeRegisterOp
// inner record and links them. Callers set inner.Field to the desired
// variant. The proto generator makes the Field interface
// package-private, so per-register helpers can't share a single
// "set field" parameter type; this constructor + per-register Field
// assignment is the shortest form that doesn't require reflection.
func newSetNodeRegisterOp(bs *CRDTBootstrap, nodeID string) (*leapmuxv1.CrdtOp, *leapmuxv1.SetNodeRegisterOp) {
	op := envelope(bs)
	inner := &leapmuxv1.SetNodeRegisterOp{NodeId: nodeID}
	op.Body = &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: inner}
	return op, inner
}

func opSetNodeKind(bs *CRDTBootstrap, nodeID string, kind leapmuxv1.NodeKind) *leapmuxv1.CrdtOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Kind{Kind: kind}
	return op
}

func opSetNodeParentID(bs *CRDTBootstrap, nodeID, parentID string) *leapmuxv1.CrdtOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_ParentId{ParentId: parentID}
	return op
}

func opSetNodePosition(bs *CRDTBootstrap, nodeID, position string) *leapmuxv1.CrdtOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Position{Position: position}
	return op
}

func opSetNodeDirection(bs *CRDTBootstrap, nodeID string, dir leapmuxv1.SplitDirection) *leapmuxv1.CrdtOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Direction{Direction: dir}
	return op
}

func opSetNodeRatios(bs *CRDTBootstrap, nodeID string, ratios []float64) *leapmuxv1.CrdtOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Ratios{Ratios: &leapmuxv1.DoubleList{Values: ratios}}
	return op
}

func opSetNodeRows(bs *CRDTBootstrap, nodeID string, rows uint32) *leapmuxv1.CrdtOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Rows{Rows: rows}
	return op
}

func opSetNodeCols(bs *CRDTBootstrap, nodeID string, cols uint32) *leapmuxv1.CrdtOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_Cols{Cols: cols}
	return op
}

func opSetNodeRowRatios(bs *CRDTBootstrap, nodeID string, values []float64) *leapmuxv1.CrdtOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_RowRatios{RowRatios: &leapmuxv1.DoubleList{Values: values}}
	return op
}

func opSetNodeColRatios(bs *CRDTBootstrap, nodeID string, values []float64) *leapmuxv1.CrdtOp {
	op, inner := newSetNodeRegisterOp(bs, nodeID)
	inner.Field = &leapmuxv1.SetNodeRegisterOp_ColRatios{ColRatios: &leapmuxv1.DoubleList{Values: values}}
	return op
}

// projectLayoutNode reads the live subtree in display order.
func projectLayoutNode(state *leapmuxv1.UserMaterialized, children map[string][]string, nodeID string) *layoutNode {
	if nodeID == "" {
		return nil
	}
	record := state.GetNodes()[nodeID]
	if record == nil || !crdt.HLCIsZero(record.GetTombstoneAt()) {
		return nil
	}
	kind := record.GetKind().GetValue()
	node := &layoutNode{NodeID: nodeID, Kind: enumToken(kind)}
	switch kind {
	case leapmuxv1.NodeKind_NODE_KIND_SPLIT:
		node.Direction = enumToken(record.GetDirection().GetValue())
		node.Ratios = record.GetRatios().GetValue().GetValues()
	case leapmuxv1.NodeKind_NODE_KIND_GRID:
		node.Rows = record.GetRows().GetValue()
		node.Cols = record.GetCols().GetValue()
		node.RowRatios = record.GetRowRatios().GetValue().GetValues()
		node.ColRatios = record.GetColRatios().GetValue().GetValues()
	case leapmuxv1.NodeKind_NODE_KIND_LEAF, leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED:
		// These nodes carry no container fields.
	}
	for _, childID := range crdt.LayoutChildIDs(state.GetNodes(), children, nodeID) {
		child := projectLayoutNode(state, children, childID)
		if child == nil && kind == leapmuxv1.NodeKind_NODE_KIND_GRID {
			child = &layoutNode{Kind: enumToken(leapmuxv1.NodeKind_NODE_KIND_LEAF)}
		}
		if child != nil {
			node.Children = append(node.Children, child)
		}
	}
	switch kind {
	case leapmuxv1.NodeKind_NODE_KIND_SPLIT:
		switch len(node.Children) {
		case 0:
			return &layoutNode{Kind: enumToken(leapmuxv1.NodeKind_NODE_KIND_LEAF)}
		case 1:
			return node.Children[0]
		}
		node.Ratios = ratiosForLayoutSize(node.Ratios, len(node.Children))
	case leapmuxv1.NodeKind_NODE_KIND_GRID:
		if len(node.Children) > 0 {
			node.RowRatios = ratiosForLayoutSize(node.RowRatios, int(node.Rows))
			node.ColRatios = ratiosForLayoutSize(node.ColRatios, int(node.Cols))
		}
	case leapmuxv1.NodeKind_NODE_KIND_LEAF, leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED:
	}
	return node
}

// firstLiveLeaf walks the subtree rooted at rootNodeID and returns the
// id of the first live leaf in depth-first display order. It returns ""
// when the root is absent, tombstoned, or has no reachable live leaf.
func firstLiveLeaf(state *leapmuxv1.UserMaterialized, rootNodeID string) string {
	return crdt.FirstLiveLayoutLeaf(state.GetNodes(), crdt.LiveChildrenByParent(state), rootNodeID)
}
