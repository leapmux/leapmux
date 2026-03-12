package layout_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/layout"
)

// --- Helpers ---

func leaf(id string) *leapmuxv1.LayoutNode {
	return &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Leaf{
			Leaf: &leapmuxv1.LayoutLeaf{Id: id},
		},
	}
}

func split(id string, dir leapmuxv1.SplitDirection, ratios []float64, children ...*leapmuxv1.LayoutNode) *leapmuxv1.LayoutNode {
	return &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Split{
			Split: &leapmuxv1.LayoutSplit{
				Id:        id,
				Direction: dir,
				Ratios:    ratios,
				Children:  children,
			},
		},
	}
}

var (
	H = leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL
	V = leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL
)

// --- Validate tests ---

func TestValidate_SingleLeaf(t *testing.T) {
	assert.NoError(t, layout.Validate(leaf("A")))
}

func TestValidate_NilNode(t *testing.T) {
	assert.Error(t, layout.Validate(nil))
}

func TestValidate_EmptyNodeType(t *testing.T) {
	assert.Error(t, layout.Validate(&leapmuxv1.LayoutNode{}))
}

func TestValidate_LeafEmptyID(t *testing.T) {
	assert.Error(t, layout.Validate(leaf("")))
}

func TestValidate_ValidHorizontalSplit(t *testing.T) {
	n := split("s1", H, []float64{0.5, 0.5}, leaf("A"), leaf("B"))
	assert.NoError(t, layout.Validate(n))
}

func TestValidate_ValidVerticalSplit(t *testing.T) {
	n := split("s1", V, []float64{0.5, 0.5}, leaf("A"), leaf("B"))
	assert.NoError(t, layout.Validate(n))
}

func TestValidate_Valid3WaySplit(t *testing.T) {
	n := split("s1", H, []float64{0.33, 0.34, 0.33}, leaf("A"), leaf("B"), leaf("C"))
	assert.NoError(t, layout.Validate(n))
}

func TestValidate_ValidNestedDifferentDirection(t *testing.T) {
	// horizontal containing vertical — valid
	nested := split("s2", V, []float64{0.5, 0.5}, leaf("B"), leaf("C"))
	n := split("s1", H, []float64{0.33, 0.34, 0.33}, leaf("A"), nested, leaf("D"))
	assert.NoError(t, layout.Validate(n))
}

func TestValidate_TooFewChildren(t *testing.T) {
	n := &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Split{
			Split: &leapmuxv1.LayoutSplit{
				Id:        "s1",
				Direction: H,
				Ratios:    []float64{1.0},
				Children:  []*leapmuxv1.LayoutNode{leaf("A")},
			},
		},
	}
	err := layout.Validate(n)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least 2 children")
}

func TestValidate_RatioCountMismatch(t *testing.T) {
	n := split("s1", H, []float64{0.5, 0.5, 0.0}, leaf("A"), leaf("B"))
	err := layout.Validate(n)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected 2 ratios")
}

func TestValidate_RatiosDontSumTo1(t *testing.T) {
	n := split("s1", H, []float64{0.3, 0.3}, leaf("A"), leaf("B"))
	err := layout.Validate(n)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ratios sum to")
}

func TestValidate_NegativeRatio(t *testing.T) {
	n := split("s1", H, []float64{-0.5, 1.5}, leaf("A"), leaf("B"))
	err := layout.Validate(n)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be positive")
}

func TestValidate_SplitEmptyID(t *testing.T) {
	n := split("", H, []float64{0.5, 0.5}, leaf("A"), leaf("B"))
	err := layout.Validate(n)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty id")
}

func TestValidate_DeeplyNestedValid(t *testing.T) {
	// 3 levels: H -> V -> H (depth 0, 1, 2 — all within maxDepth=3)
	innermost := split("s3", H, []float64{0.5, 0.5}, leaf("C"), leaf("D"))
	middle := split("s2", V, []float64{0.5, 0.5}, leaf("B"), innermost)
	outer := split("s1", H, []float64{0.5, 0.5}, leaf("A"), middle)
	assert.NoError(t, layout.Validate(outer))
}

func TestValidate_ExceedsMaxDepth(t *testing.T) {
	// 4 levels deep: H -> V -> H -> V — the 4th level exceeds maxDepth=3
	level3 := split("s4", V, []float64{0.5, 0.5}, leaf("D"), leaf("E"))
	level2 := split("s3", H, []float64{0.5, 0.5}, leaf("C"), level3)
	level1 := split("s2", V, []float64{0.5, 0.5}, leaf("B"), level2)
	level0 := split("s1", H, []float64{0.5, 0.5}, leaf("A"), level1)
	err := layout.Validate(level0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum nesting depth")
}

func TestValidate_NilChildInSplit(t *testing.T) {
	n := split("s1", H, []float64{0.5, 0.5}, leaf("A"), nil)
	err := layout.Validate(n)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// --- IsOptimized tests ---

func TestIsOptimized_SingleLeaf(t *testing.T) {
	assert.True(t, layout.IsOptimized(leaf("A")))
}

func TestIsOptimized_HorizontalSplitWithLeaves(t *testing.T) {
	n := split("s1", H, []float64{0.33, 0.34, 0.33}, leaf("A"), leaf("B"), leaf("C"))
	assert.True(t, layout.IsOptimized(n))
}

func TestIsOptimized_VerticalSplitWithLeaves(t *testing.T) {
	n := split("s1", V, []float64{0.33, 0.34, 0.33}, leaf("A"), leaf("B"), leaf("C"))
	assert.True(t, layout.IsOptimized(n))
}

func TestIsOptimized_SingleChildNotOptimized(t *testing.T) {
	n := &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Split{
			Split: &leapmuxv1.LayoutSplit{
				Id:        "s1",
				Direction: H,
				Ratios:    []float64{1.0},
				Children:  []*leapmuxv1.LayoutNode{leaf("A")},
			},
		},
	}
	assert.False(t, layout.IsOptimized(n))
}

func TestIsOptimized_SameDirectionHorizontalNesting(t *testing.T) {
	inner := split("s2", H, []float64{0.5, 0.5}, leaf("B"), leaf("C"))
	n := split("s1", H, []float64{0.5, 0.5}, leaf("A"), inner)
	assert.False(t, layout.IsOptimized(n))
}

func TestIsOptimized_SameDirectionVerticalNesting(t *testing.T) {
	inner := split("s2", V, []float64{0.5, 0.5}, leaf("B"), leaf("C"))
	n := split("s1", V, []float64{0.5, 0.5}, leaf("A"), inner)
	assert.False(t, layout.IsOptimized(n))
}

func TestIsOptimized_DifferentDirectionNesting(t *testing.T) {
	inner := split("s2", V, []float64{0.5, 0.5}, leaf("B"), leaf("C"))
	n := split("s1", H, []float64{0.5, 0.5}, leaf("A"), inner)
	assert.True(t, layout.IsOptimized(n))
}

func TestIsOptimized_NestedSingleChildInsideSplit(t *testing.T) {
	inner := &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Split{
			Split: &leapmuxv1.LayoutSplit{
				Id:        "s2",
				Direction: V,
				Ratios:    []float64{1.0},
				Children:  []*leapmuxv1.LayoutNode{leaf("B")},
			},
		},
	}
	n := split("s1", H, []float64{0.5, 0.5}, leaf("A"), inner)
	assert.False(t, layout.IsOptimized(n))
}

func TestIsOptimized_NilNode(t *testing.T) {
	assert.True(t, layout.IsOptimized(nil))
}

func TestIsOptimized_ComplexValid(t *testing.T) {
	nested := split("s2", V, []float64{0.5, 0.5}, leaf("B"), leaf("C"))
	n := split("s1", H, []float64{0.33, 0.34, 0.33}, leaf("A"), nested, leaf("D"))
	assert.True(t, layout.IsOptimized(n))
}

func TestIsOptimized_DeeplyNestedSameDirection(t *testing.T) {
	innermost := split("s3", H, []float64{0.5, 0.5}, leaf("C"), leaf("D"))
	middle := split("s2", H, []float64{0.5, 0.5}, leaf("B"), innermost)
	outer := split("s1", H, []float64{0.5, 0.5}, leaf("A"), middle)
	assert.False(t, layout.IsOptimized(outer))
}
