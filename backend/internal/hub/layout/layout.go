// Package layout validates and inspects workspace layout trees.
//
// Layouts are persisted as protojson and may live either in a workspace's main
// layout slot or inside a floating window. The two contexts have different
// nesting rules: main layouts cap depth at MaxMainDepth so the UI stays
// reasonable, while floating-window layouts use UnlimitedDepth because
// existing data already nests arbitrarily and we don't want to start
// rejecting it. Use Validate for main layouts and ValidateWithMaxDepth with
// UnlimitedDepth for floating-window layouts.
package layout

import (
	"fmt"
	"math"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	ratioTolerance = 0.01
	// MaxMainDepth is the maximum nesting depth allowed for the main workspace
	// layout. Splits and grids each count as one level.
	MaxMainDepth = 3
	// UnlimitedDepth disables depth checking; pass this to
	// ValidateWithMaxDepth for floating-window layouts.
	UnlimitedDepth = -1
	// MaxGridDimension caps grid rows and columns. Mirrors the frontend
	// hard cap so a malformed request can't be persisted.
	MaxGridDimension = 20
)

// Validate checks that a main-workspace LayoutNode is structurally correct
// using MaxMainDepth. A nil node is permitted because some persistence
// callers send only tab updates without touching the layout.
func Validate(node *leapmuxv1.LayoutNode) error {
	if node == nil {
		return nil
	}
	return validateNode(node, 0, MaxMainDepth)
}

// ValidateWithMaxDepth validates with a configurable max nesting depth.
// Pass UnlimitedDepth to disable the depth check (used for floating-window
// layouts to preserve compatibility with already-persisted data).
func ValidateWithMaxDepth(node *leapmuxv1.LayoutNode, maxDepth int) error {
	if node == nil {
		return nil
	}
	return validateNode(node, 0, maxDepth)
}

func validateNode(node *leapmuxv1.LayoutNode, depth, maxDepth int) error {
	if node == nil {
		return fmt.Errorf("layout node is nil")
	}

	switch n := node.Node.(type) {
	case *leapmuxv1.LayoutNode_Leaf:
		if n.Leaf == nil {
			return fmt.Errorf("leaf node is nil")
		}
		if n.Leaf.Id == "" {
			return fmt.Errorf("leaf node has empty id")
		}
		return nil

	case *leapmuxv1.LayoutNode_Split:
		if n.Split == nil {
			return fmt.Errorf("split node is nil")
		}
		s := n.Split

		if s.Id == "" {
			return fmt.Errorf("split node has empty id")
		}

		if maxDepth >= 0 && depth >= maxDepth {
			return fmt.Errorf("split %q: exceeds maximum nesting depth of %d", s.Id, maxDepth)
		}

		if len(s.Children) < 2 {
			return fmt.Errorf("split %q: must have at least 2 children, got %d", s.Id, len(s.Children))
		}

		if len(s.Ratios) != len(s.Children) {
			return fmt.Errorf("split %q: expected %d ratios, got %d", s.Id, len(s.Children), len(s.Ratios))
		}

		if err := validateRatios(s.Ratios, s.Id); err != nil {
			return err
		}

		for i, child := range s.Children {
			if err := validateNode(child, depth+1, maxDepth); err != nil {
				return fmt.Errorf("split %q child[%d]: %w", s.Id, i, err)
			}
		}

		return nil

	case *leapmuxv1.LayoutNode_Grid:
		if n.Grid == nil {
			return fmt.Errorf("grid node is nil")
		}
		g := n.Grid

		if g.Id == "" {
			return fmt.Errorf("grid node has empty id")
		}

		if maxDepth >= 0 && depth >= maxDepth {
			return fmt.Errorf("grid %q: exceeds maximum nesting depth of %d", g.Id, maxDepth)
		}

		if g.Rows < 1 || g.Rows > MaxGridDimension {
			return fmt.Errorf("grid %q: rows must be in [1, %d], got %d", g.Id, MaxGridDimension, g.Rows)
		}
		if g.Cols < 1 || g.Cols > MaxGridDimension {
			return fmt.Errorf("grid %q: cols must be in [1, %d], got %d", g.Id, MaxGridDimension, g.Cols)
		}

		expected := int(g.Rows) * int(g.Cols)
		if len(g.Cells) != expected {
			return fmt.Errorf("grid %q: expected %d cells (%d×%d), got %d", g.Id, expected, g.Rows, g.Cols, len(g.Cells))
		}

		if len(g.RowRatios) != int(g.Rows) {
			return fmt.Errorf("grid %q: expected %d row ratios, got %d", g.Id, g.Rows, len(g.RowRatios))
		}
		if len(g.ColRatios) != int(g.Cols) {
			return fmt.Errorf("grid %q: expected %d col ratios, got %d", g.Id, g.Cols, len(g.ColRatios))
		}

		if err := validateRatios(g.RowRatios, g.Id); err != nil {
			return err
		}
		if err := validateRatios(g.ColRatios, g.Id); err != nil {
			return err
		}

		for i, cell := range g.Cells {
			if err := validateNode(cell, depth+1, maxDepth); err != nil {
				return fmt.Errorf("grid %q cell[%d]: %w", g.Id, i, err)
			}
		}

		return nil

	default:
		return fmt.Errorf("layout node has no node type set")
	}
}

func validateRatios(ratios []float64, nodeID string) error {
	sum := 0.0
	for i, r := range ratios {
		if math.IsNaN(r) || math.IsInf(r, 0) {
			return fmt.Errorf("node %q: ratio[%d] must be finite, got %f", nodeID, i, r)
		}
		if r <= 0 {
			return fmt.Errorf("node %q: ratio[%d] must be positive, got %f", nodeID, i, r)
		}
		sum += r
	}
	if math.Abs(sum-1.0) > ratioTolerance {
		return fmt.Errorf("node %q: ratios sum to %f, expected ~1.0", nodeID, sum)
	}
	return nil
}

// CollectLeafIDs returns the set of all leaf (tile) IDs in the layout tree.
func CollectLeafIDs(node *leapmuxv1.LayoutNode) map[string]struct{} {
	ids := make(map[string]struct{})
	collectLeafIDs(node, ids)
	return ids
}

func collectLeafIDs(node *leapmuxv1.LayoutNode, ids map[string]struct{}) {
	if node == nil {
		return
	}
	switch n := node.Node.(type) {
	case *leapmuxv1.LayoutNode_Leaf:
		if n.Leaf != nil {
			ids[n.Leaf.Id] = struct{}{}
		}
	case *leapmuxv1.LayoutNode_Split:
		if n.Split != nil {
			for _, child := range n.Split.Children {
				collectLeafIDs(child, ids)
			}
		}
	case *leapmuxv1.LayoutNode_Grid:
		if n.Grid != nil {
			for _, cell := range n.Grid.Cells {
				collectLeafIDs(cell, ids)
			}
		}
	}
}

// IsOptimized checks that a layout is in canonical form:
//   - No single-child splits (should be unwrapped to the child)
//   - No same-direction nesting that could be flattened
//
// Grids are always considered structurally canonical themselves; their cells
// are recursively checked.
func IsOptimized(node *leapmuxv1.LayoutNode) bool {
	if node == nil {
		return true
	}

	switch n := node.Node.(type) {
	case *leapmuxv1.LayoutNode_Leaf:
		return true

	case *leapmuxv1.LayoutNode_Split:
		if n.Split == nil {
			return true
		}
		s := n.Split

		// A single-child split should be unwrapped.
		if len(s.Children) < 2 {
			return false
		}

		for _, child := range s.Children {
			if !IsOptimized(child) {
				return false
			}

			// Check for same-direction nesting.
			if childSplit := child.GetSplit(); childSplit != nil {
				if childSplit.Direction == s.Direction {
					return false
				}
			}
		}

		return true

	case *leapmuxv1.LayoutNode_Grid:
		if n.Grid == nil {
			return true
		}
		for _, cell := range n.Grid.Cells {
			if !IsOptimized(cell) {
				return false
			}
		}
		return true

	default:
		return true
	}
}
