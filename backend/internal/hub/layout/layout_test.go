package layout_test

import (
	"fmt"
	"math"
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

func grid(id string, rows, cols uint32, rowRatios, colRatios []float64, cells ...*leapmuxv1.LayoutNode) *leapmuxv1.LayoutNode {
	return &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Grid{
			Grid: &leapmuxv1.LayoutGrid{
				Id:        id,
				Rows:      rows,
				Cols:      cols,
				RowRatios: rowRatios,
				ColRatios: colRatios,
				Cells:     cells,
			},
		},
	}
}

func equalRatios(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1.0 / float64(n)
	}
	return out
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
	// Nil layouts are accepted: callers may send only tab updates without
	// touching the layout. validateNode handles structural checks; Validate
	// short-circuits on nil to keep that path explicit.
	assert.NoError(t, layout.Validate(nil))
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

func TestValidate_NaNRatio(t *testing.T) {
	n := split("s1", H, []float64{math.NaN(), 0.5}, leaf("A"), leaf("B"))
	err := layout.Validate(n)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "finite")
}

func TestValidate_InfRatio(t *testing.T) {
	n := split("s1", H, []float64{math.Inf(1), 0.5}, leaf("A"), leaf("B"))
	err := layout.Validate(n)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "finite")
}

// --- Grid validation ---

func TestValidate_ValidGrid2x2(t *testing.T) {
	g := grid("g1", 2, 2, equalRatios(2), equalRatios(2),
		leaf("A"), leaf("B"),
		leaf("C"), leaf("D"),
	)
	assert.NoError(t, layout.Validate(g))
}

func TestValidate_ValidGrid1x3(t *testing.T) {
	g := grid("g1", 1, 3, []float64{1.0}, equalRatios(3),
		leaf("A"), leaf("B"), leaf("C"),
	)
	assert.NoError(t, layout.Validate(g))
}

func TestValidate_GridEmptyID(t *testing.T) {
	g := grid("", 2, 2, equalRatios(2), equalRatios(2),
		leaf("A"), leaf("B"), leaf("C"), leaf("D"),
	)
	err := layout.Validate(g)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty id")
}

func TestValidate_GridZeroRows(t *testing.T) {
	g := grid("g1", 0, 2, []float64{}, equalRatios(2))
	err := layout.Validate(g)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rows must be in")
}

func TestValidate_GridTooManyRows(t *testing.T) {
	g := grid("g1", 21, 1, equalRatios(21), []float64{1.0})
	cells := make([]*leapmuxv1.LayoutNode, 21)
	for i := range cells {
		cells[i] = leaf(fmt.Sprintf("C%d", i))
	}
	g.GetGrid().Cells = cells
	err := layout.Validate(g)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rows must be in")
}

func TestValidate_GridTooManyCols(t *testing.T) {
	g := grid("g1", 1, 21, []float64{1.0}, equalRatios(21))
	cells := make([]*leapmuxv1.LayoutNode, 21)
	for i := range cells {
		cells[i] = leaf(fmt.Sprintf("C%d", i))
	}
	g.GetGrid().Cells = cells
	err := layout.Validate(g)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cols must be in")
}

func TestValidate_GridCellCountMismatch(t *testing.T) {
	g := grid("g1", 2, 2, equalRatios(2), equalRatios(2),
		leaf("A"), leaf("B"), leaf("C"),
	)
	err := layout.Validate(g)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected 4 cells")
}

func TestValidate_GridRowRatioCountMismatch(t *testing.T) {
	g := grid("g1", 2, 2, []float64{1.0}, equalRatios(2),
		leaf("A"), leaf("B"), leaf("C"), leaf("D"),
	)
	err := layout.Validate(g)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected 2 row ratios")
}

func TestValidate_GridColRatioCountMismatch(t *testing.T) {
	g := grid("g1", 2, 2, equalRatios(2), []float64{1.0},
		leaf("A"), leaf("B"), leaf("C"), leaf("D"),
	)
	err := layout.Validate(g)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected 2 col ratios")
}

func TestValidate_GridRatiosDontSumTo1(t *testing.T) {
	g := grid("g1", 2, 2, []float64{0.3, 0.3}, equalRatios(2),
		leaf("A"), leaf("B"), leaf("C"), leaf("D"),
	)
	err := layout.Validate(g)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ratios sum to")
}

func TestValidate_GridDepthCountsAsOneLevel(t *testing.T) {
	// outer split (depth 0) -> grid (depth 1) -> split (depth 2) -> split (depth 3) — exceeds 3
	deepest := split("s3", V, []float64{0.5, 0.5}, leaf("D"), leaf("E"))
	mid := split("s2", H, []float64{0.5, 0.5}, leaf("C"), deepest)
	g := grid("g1", 1, 2, []float64{1.0}, equalRatios(2), mid, leaf("B"))
	outer := split("s1", H, []float64{0.5, 0.5}, leaf("A"), g)
	err := layout.Validate(outer)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum nesting depth")
}

func TestValidate_GridNestedInsideGrid(t *testing.T) {
	innerGrid := grid("g2", 1, 2, []float64{1.0}, equalRatios(2), leaf("X"), leaf("Y"))
	outer := grid("g1", 2, 2, equalRatios(2), equalRatios(2),
		innerGrid, leaf("B"),
		leaf("C"), leaf("D"),
	)
	assert.NoError(t, layout.Validate(outer))
}

func TestValidateWithMaxDepth_UnlimitedDepth(t *testing.T) {
	// Floating windows pass UnlimitedDepth; deeply nested layouts must validate.
	level5 := split("s5", H, []float64{0.5, 0.5}, leaf("F"), leaf("G"))
	level4 := split("s4", V, []float64{0.5, 0.5}, leaf("E"), level5)
	level3 := split("s3", H, []float64{0.5, 0.5}, leaf("D"), level4)
	level2 := split("s2", V, []float64{0.5, 0.5}, leaf("C"), level3)
	level1 := split("s1", H, []float64{0.5, 0.5}, leaf("B"), level2)
	root := split("s0", V, []float64{0.5, 0.5}, leaf("A"), level1)
	assert.NoError(t, layout.ValidateWithMaxDepth(root, layout.UnlimitedDepth))
	// And rejected by the main Validate.
	assert.Error(t, layout.Validate(root))
}

func TestValidateWithMaxDepth_NilNode(t *testing.T) {
	assert.NoError(t, layout.ValidateWithMaxDepth(nil, layout.UnlimitedDepth))
	assert.NoError(t, layout.ValidateWithMaxDepth(nil, 3))
}

// --- CollectLeafIDs grid traversal ---

func TestCollectLeafIDs_Grid(t *testing.T) {
	g := grid("g1", 2, 2, equalRatios(2), equalRatios(2),
		leaf("A"), leaf("B"),
		split("s1", H, []float64{0.5, 0.5}, leaf("C"), leaf("D")),
		grid("g2", 1, 2, []float64{1.0}, equalRatios(2), leaf("E"), leaf("F")),
	)
	ids := layout.CollectLeafIDs(g)
	assert.Len(t, ids, 6)
	for _, id := range []string{"A", "B", "C", "D", "E", "F"} {
		_, ok := ids[id]
		assert.True(t, ok, "expected leaf %q to be collected", id)
	}
}

// --- IsOptimized grid cases ---

func TestIsOptimized_GridWithCanonicalCells(t *testing.T) {
	g := grid("g1", 2, 2, equalRatios(2), equalRatios(2),
		leaf("A"), leaf("B"), leaf("C"), leaf("D"),
	)
	assert.True(t, layout.IsOptimized(g))
}

func TestIsOptimized_GridWithSameDirNestingInsideCell(t *testing.T) {
	// A grid cell containing same-direction nesting is not optimized.
	innerSplit := split("s2", H, []float64{0.5, 0.5}, leaf("Y"), leaf("Z"))
	cell := split("s1", H, []float64{0.5, 0.5}, leaf("X"), innerSplit)
	g := grid("g1", 1, 2, []float64{1.0}, equalRatios(2), cell, leaf("B"))
	assert.False(t, layout.IsOptimized(g))
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
