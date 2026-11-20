package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

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
