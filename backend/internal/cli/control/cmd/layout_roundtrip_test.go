package cmd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

func TestProjectLayoutNode_OrdersSplitAndGridChildren(t *testing.T) {
	builder := newStateBuilder().
		splitNode("root", "", "").
		leafNode("left", "root", "a").
		leafNode("left-other", "root", "a").
		gridNode("grid", "root", "b").
		leafNode("duplicate-cell", "grid", "0,2")
	const columns = 11
	grid := builder.st.Nodes["grid"]
	grid.Rows = &leapmuxv1.LWWUint32{Value: 1}
	grid.Cols = &leapmuxv1.LWWUint32{Value: columns}
	var gridChildren, wantGrid []string
	for col := range columns {
		if col == 4 {
			wantGrid = append(wantGrid, "")
			continue
		}
		id := fmt.Sprintf("cell-%d", col)
		builder.leafNode(id, "grid", fmt.Sprintf("0,%d", col))
		wantGrid = append(wantGrid, id)
		gridChildren = append(gridChildren, id)
	}
	slices.Reverse(gridChildren)
	index := map[string][]string{
		"root": {"grid", "left-other", "left"},
		"grid": append([]string{"duplicate-cell"}, gridChildren...),
	}

	tree := projectLayoutNode(builder.st, index, "root")
	require.Len(t, tree.Children, 3)
	assert.Equal(t, "left", tree.Children[0].NodeID)
	assert.Equal(t, "left-other", tree.Children[1].NodeID)
	require.Equal(t, "grid", tree.Children[2].NodeID)
	gridNodes := tree.Children[2].Children
	require.Len(t, gridNodes, columns)
	for col, node := range gridNodes {
		assert.Equal(t, wantGrid[col], node.NodeID)
		assert.Equal(t, "leaf", node.Kind)
	}
}

func TestFirstLiveLeaf_SkipsVacantCellsInRowMajorOrder(t *testing.T) {
	state := newStateBuilder().
		gridNode("grid", "", "").
		leafNode("tenth", "grid", "0,10").
		leafNode("second", "grid", "0,2").
		leafNode("out-of-range", "grid", "0,11").st
	grid := state.Nodes["grid"]
	grid.Rows = &leapmuxv1.LWWUint32{Value: 1}
	grid.Cols = &leapmuxv1.LWWUint32{Value: 11}
	assert.Equal(t, "second", firstLiveLeaf(state, "grid"))
}

func TestBuildLayoutSetOps_WritesGridCellCoordinates(t *testing.T) {
	state := newStateBuilder().workspace("workspace", "root").leafNode("root", "", "").st
	target := &layoutNode{Kind: "grid", Rows: 2, Cols: 2, Children: []*layoutNode{
		{Kind: "leaf"}, {Kind: "leaf"}, {Kind: "leaf"}, {Kind: "leaf"},
	}}
	ops, rootID, firstLeaf, err := buildLayoutSetOps(testBootstrap(state), "workspace", target)
	require.NoError(t, err)
	require.Equal(t, "root", rootID)
	var positions []string
	for _, op := range ops {
		set := op.GetSetNodeRegister()
		if position, ok := set.GetField().(*leapmuxv1.SetNodeRegisterOp_Position); ok {
			positions = append(positions, position.Position)
			if set.GetNodeId() == firstLeaf {
				assert.Equal(t, "0,0", position.Position)
			}
		}
	}
	assert.Equal(t, []string{"0,0", "0,1", "1,0", "1,1"}, positions)
}

func TestBuildLayoutSetOps_ResetsRatiosForANewGridShape(t *testing.T) {
	state := newStateBuilder().workspace("workspace", "root").gridNode("root", "", "").st
	grid := state.Nodes["root"]
	grid.Rows, grid.Cols = &leapmuxv1.LWWUint32{Value: 2}, &leapmuxv1.LWWUint32{Value: 2}
	grid.RowRatios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.4, 0.6}}}
	grid.ColRatios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.5, 0.5}}}
	target := &layoutNode{Kind: "grid", Rows: 1, Cols: 3, Children: []*layoutNode{
		{Kind: "leaf"}, {Kind: "leaf"}, {Kind: "leaf"},
	}}
	ops, _, _, err := buildLayoutSetOps(testBootstrap(state), "workspace", target)
	require.NoError(t, err)
	var rowRatios, colRatios []float64
	for _, op := range ops {
		set := op.GetSetNodeRegister()
		if set.GetNodeId() == "root" {
			if set.GetRowRatios() != nil {
				rowRatios = set.GetRowRatios().GetValues()
			}
			if set.GetColRatios() != nil {
				colRatios = set.GetColRatios().GetValues()
			}
		}
	}
	assert.Equal(t, []float64{1}, rowRatios)
	assert.Equal(t, []float64{1.0 / 3, 1.0 / 3, 1.0 / 3}, colRatios)
}

func TestProjectLayoutNode_ExportsAReshapedGridWithValidRatios(t *testing.T) {
	state := newStateBuilder().gridNode("grid", "", "").st
	grid := state.Nodes["grid"]
	grid.Rows, grid.Cols = &leapmuxv1.LWWUint32{Value: 1}, &leapmuxv1.LWWUint32{Value: 3}
	grid.RowRatios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.4, 0.6}}}
	grid.ColRatios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.5, 0.5}}}
	tree := projectLayoutNode(state, nil, "grid")
	encoded, err := json.Marshal(tree)
	require.NoError(t, err)
	decoded, err := decodeTargetTree(strings.NewReader(string(encoded)))
	require.NoError(t, err)
	assert.Equal(t, []float64{1}, decoded.RowRatios)
	assert.Equal(t, []float64{0.375, 0.375, 0.25}, decoded.ColRatios)
}

func TestProjectLayoutNode_ExportsSplitsWithTooFewChildrenAsLeaves(t *testing.T) {
	builder := newStateBuilder().splitNode("root", "", "")
	for _, expected := range []string{"", "only-child"} {
		tree := projectLayoutNode(builder.st, crdt.LiveChildrenByParent(builder.st), "root")
		assert.Equal(t, "leaf", tree.Kind)
		assert.Equal(t, expected, tree.NodeID)
		assert.Empty(t, tree.Children)
		builder.leafNode("only-child", "root", "a")
	}
}

func TestLayoutSetReadsTheLayoutGetEnvelopeFromStdin(t *testing.T) {
	const workspaceID = "layout-round-trip"
	state := newStateBuilder().
		workspace(workspaceID, "root").
		splitNode("root", "", "").
		leafNode("left", "root", "a").
		gridNode("grid", "root", "b").
		leafNode("cell-00", "grid", "0,0").
		leafNode("cell-01", "grid", "0,1").
		leafNode("cell-10", "grid", "1,0").
		leafNode("cell-11", "grid", "1,1").st
	root, grid := state.Nodes["root"], state.Nodes["grid"]
	root.Direction = &leapmuxv1.LWWDirection{Value: leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL}
	root.Ratios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.25, 0.75}}}
	grid.Rows, grid.Cols = &leapmuxv1.LWWUint32{Value: 2}, &leapmuxv1.LWWUint32{Value: 2}
	grid.RowRatios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.4, 0.6}}}
	grid.ColRatios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.5, 0.5}}}
	hub := &recordingHub{materialized: state, onSubmitOps: tabCloseHub().onSubmitOps}
	startSpawnIPC(t, hub, nil)

	saved := runControl(t, "layout", "get", "id:"+workspaceID)
	require.Equal(t, cli.ExitOK, saved.Code, saved.Stderr)
	require.Len(t, saved.lines(), 1)
	decoded, err := decodeTargetTree(strings.NewReader(saved.Stdout))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_SPLIT, decoded.parsedKind())
	assert.Equal(t, leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL, decoded.parsedDirection())
	assert.Equal(t, []float64{0.25, 0.75}, decoded.Ratios)
	require.Len(t, decoded.Children, 2)
	assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_GRID, decoded.Children[1].parsedKind())
	assert.Equal(t, []float64{0.4, 0.6}, decoded.Children[1].RowRatios)
	require.Len(t, decoded.Children[1].Children, 4)

	result := runControlIn(t, saved.Stdout, "layout", "set", "id:"+workspaceID, "-", "--json").Data(t)
	assert.Equal(t, workspaceID, result["workspace_id"])
	assert.Equal(t, "root", result["root_node_id"])
	assert.NotEmpty(t, result["new_first_leaf_id"])
	assert.Contains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
}
