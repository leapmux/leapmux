package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestIsGridCell pins the `tile close` rejection check for grid
// cells. Returns true (and the grid id) only for a live leaf whose
// parent is a live GRID; every other configuration (leaf under a
// SPLIT, leaf without a parent, grid node itself, tombstoned cell,
// tombstoned parent) must fall through so the handler's main close
// flow handles those cases as today.
func TestIsGridCell(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root").
		splitNode("root", "", "").
		gridNode("g", "root", "a").
		leafNode("cell-00", "g", "0,0").
		leafNode("cell-01", "g", "0,1").
		splitNode("s", "root", "b").
		leafNode("sLeaf", "s", "a").
		leafNode("orphan", "", ""). // no parent
		tombstonedNode("dead-cell", "g").
		st
	// Anchor a tombstoned-parent scenario: add a separate cell whose
	// grid parent has been tombstoned. Easier to set up by hand than
	// via stateBuilder since we want the cell to survive while its
	// parent dies.
	state.GetNodes()["zombie-cell"] = &leapmuxv1.NodeRecord{
		NodeId:   "zombie-cell",
		ParentId: "dead-grid",
		Kind:     &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		Position: &leapmuxv1.LWWString{Value: "0,0"},
	}
	state.GetNodes()["dead-grid"] = &leapmuxv1.NodeRecord{
		NodeId:      "dead-grid",
		ParentId:    "root",
		Kind:        &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_GRID},
		TombstoneAt: &leapmuxv1.HLC{Physical: 1, ClientId: "x"},
	}

	cases := []struct {
		nodeID     string
		wantGridID string
		wantOK     bool
		why        string
	}{
		{"cell-00", "g", true, "live grid cell"},
		{"cell-01", "g", true, "live grid cell (second)"},
		{"sLeaf", "", false, "leaf under a SPLIT is not a grid cell"},
		{"orphan", "", false, "leaf without a parent isn't a grid cell"},
		{"g", "", false, "the grid node itself isn't a grid cell"},
		{"s", "", false, "a SPLIT node isn't a grid cell"},
		{"dead-cell", "", false, "tombstoned leaf shouldn't trigger the guard"},
		{"zombie-cell", "", false, "leaf whose grid parent is tombstoned isn't a live grid cell"},
		{"nonexistent", "", false, "unknown node id"},
	}
	for _, c := range cases {
		got, ok := isGridCell(state, c.nodeID)
		assert.Equal(t, c.wantOK, ok, "isGridCell(%q) ok: %s", c.nodeID, c.why)
		assert.Equal(t, c.wantGridID, got, "isGridCell(%q) gridID: %s", c.nodeID, c.why)
	}
}
