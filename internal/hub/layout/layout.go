package layout

import (
	"fmt"
	"math"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	ratioTolerance = 0.01
	maxDepth       = 3
)

// Validate checks that a LayoutNode is structurally correct:
//   - All splits have at least 2 children
//   - Ratios length matches children count and sums to ~1.0
//   - Nesting depth does not exceed maxDepth
//   - All leaf IDs are non-empty
func Validate(node *leapmuxv1.LayoutNode) error {
	return validateNode(node, 0)
}

func validateNode(node *leapmuxv1.LayoutNode, depth int) error {
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

		if depth >= maxDepth {
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
			if err := validateNode(child, depth+1); err != nil {
				return fmt.Errorf("split %q child[%d]: %w", s.Id, i, err)
			}
		}

		return nil

	default:
		return fmt.Errorf("layout node has no node type set")
	}
}

func validateRatios(ratios []float64, splitID string) error {
	sum := 0.0
	for i, r := range ratios {
		if r <= 0 {
			return fmt.Errorf("split %q: ratio[%d] must be positive, got %f", splitID, i, r)
		}
		sum += r
	}
	if math.Abs(sum-1.0) > ratioTolerance {
		return fmt.Errorf("split %q: ratios sum to %f, expected ~1.0", splitID, sum)
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
	}
}

// IsOptimized checks that a layout is in canonical form:
//   - No single-child splits (should be unwrapped to the child)
//   - No same-direction nesting that could be flattened
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

	default:
		return true
	}
}
