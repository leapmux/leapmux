package cmd

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// --- normalizeRatios ---

// TestNormalizeRatios_AlreadyNormalizedPassesThrough pins the no-op
// case: a slice that already sums to 1.0 must come back unchanged
// (modulo float64 ULP from the divide-by-sum round-trip). The frontend
// emits already-normalized ratios via the layout store, so this is
// the dominant path through `tile set-ratios`.
func TestNormalizeRatios_AlreadyNormalizedPassesThrough(t *testing.T) {
	out, err := normalizeRatios([]float64{0.3, 0.7})
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.InDelta(t, 0.3, out[0], 1e-12)
	assert.InDelta(t, 0.7, out[1], 1e-12)
	assert.InDelta(t, 1.0, out[0]+out[1], 1e-12)
}

// TestNormalizeRatios_RescalesArbitraryWeights covers the lenient
// input path: users can pass raw weights and get a normalized slice
// back. "1, 2, 1" should become [0.25, 0.5, 0.25].
func TestNormalizeRatios_RescalesArbitraryWeights(t *testing.T) {
	out, err := normalizeRatios([]float64{1, 2, 1})
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.InDelta(t, 0.25, out[0], 1e-12)
	assert.InDelta(t, 0.5, out[1], 1e-12)
	assert.InDelta(t, 0.25, out[2], 1e-12)
}

// TestNormalizeRatios_OutputSumsToOne is the load-bearing property
// for the wire compatibility: the server's validRatios rejects sum
// outside 1e-9 of 1.0. Test across a few input shapes so a regression
// in the rescale loop trips even when individual numerator/denominator
// pairs round to within tolerance by coincidence.
func TestNormalizeRatios_OutputSumsToOne(t *testing.T) {
	cases := [][]float64{
		{0.5, 0.5},
		{0.33, 0.33, 0.34},
		{1, 2, 3, 4},
		{1e-9, 1.0},
		{0.0, 1.0},
		{1.0, 0.0, 0.0},
	}
	for _, in := range cases {
		out, err := normalizeRatios(in)
		require.NoError(t, err, "input %v should normalize cleanly", in)
		var sum float64
		for _, v := range out {
			sum += v
		}
		assert.InDelta(t, 1.0, sum, 1e-9, "normalized %v -> %v sum=%v", in, out, sum)
	}
}

// TestNormalizeRatios_RejectsEmpty pins the empty-input error: a
// length-zero ratios slice has no meaning for SPLIT/GRID and would
// produce a divide-by-zero if we tried to rescale anyway.
func TestNormalizeRatios_RejectsEmpty(t *testing.T) {
	_, err := normalizeRatios(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

// TestNormalizeRatios_RejectsNegative covers the negative-value path.
// The renderer treats negatives as "shrink-to-zero with a remainder
// debt" which is incoherent; the server rejects them via validRatios.
// We catch them earlier with a more useful message.
func TestNormalizeRatios_RejectsNegative(t *testing.T) {
	_, err := normalizeRatios([]float64{0.5, -0.5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-negative")
}

// TestNormalizeRatios_RejectsNaN covers the NaN path. NaN propagates
// through arithmetic so a single NaN poisons the entire normalized
// slice; the server also rejects it. Catch early.
func TestNormalizeRatios_RejectsNaN(t *testing.T) {
	_, err := normalizeRatios([]float64{math.NaN(), 0.5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finite")
}

// TestNormalizeRatios_RejectsInfinity covers ±Inf. Same rationale as
// NaN — arithmetic poisons the slice, server rejects.
func TestNormalizeRatios_RejectsInfinity(t *testing.T) {
	for _, v := range []float64{math.Inf(1), math.Inf(-1)} {
		_, err := normalizeRatios([]float64{v, 0.5})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "finite")
	}
}

// TestNormalizeRatios_RejectsAllZeros covers the degenerate input.
// Zero/zero is NaN; the user almost certainly didn't mean this, and
// dividing by sum=0 would produce NaN downstream. Surface the error
// with a hint that at least one positive weight is required.
func TestNormalizeRatios_RejectsAllZeros(t *testing.T) {
	_, err := normalizeRatios([]float64{0, 0, 0})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive")
}

// --- parseRatiosCSV ---

// TestParseRatiosCSV_TrimsSpacesAndNormalizes is the canonical happy
// path: copy-pasted CSV with spaces, raw weights, gets normalized.
func TestParseRatiosCSV_TrimsSpacesAndNormalizes(t *testing.T) {
	out, err := parseRatiosCSV(" 1 , 3 ")
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.InDelta(t, 0.25, out[0], 1e-12)
	assert.InDelta(t, 0.75, out[1], 1e-12)
}

// TestParseRatiosCSV_RejectsBlankString catches the "empty flag" case:
// `--ratios ""` shouldn't pass parsing as a single zero-value or
// crash the strconv loop on an empty token.
func TestParseRatiosCSV_RejectsBlankString(t *testing.T) {
	for _, in := range []string{"", "   ", "\t"} {
		_, err := parseRatiosCSV(in)
		require.Error(t, err, "input %q must be rejected", in)
	}
}

// TestParseRatiosCSV_RejectsMalformedNumber pins the strconv error
// path: a token that won't parse as a float must surface the offending
// token in the error so the user can fix the right one without diffing.
func TestParseRatiosCSV_RejectsMalformedNumber(t *testing.T) {
	_, err := parseRatiosCSV("0.5, abc, 0.5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "abc")
}

// --- validateAndNormalizeTree ---

// TestValidateTree_LeafHappyPath is the smallest valid tree: a bare
// leaf. layout set turns this into "root becomes a single LEAF and
// every tab migrates onto it" — a uniform reset.
func TestValidateTree_LeafHappyPath(t *testing.T) {
	tr := &targetNode{Kind: "leaf"}
	require.NoError(t, validateAndNormalizeTree(tr, "root"))
}

// TestValidateTree_SplitHappyPath covers a balanced two-child SPLIT
// with both direction and ratios specified. Verifies normalize-in-place
// on the user's ratios so the wire payload is sum=1.0.
func TestValidateTree_SplitHappyPath(t *testing.T) {
	tr := &targetNode{
		Kind:      "split",
		Direction: "vertical",
		Ratios:    []float64{1, 1},
		Children: []*targetNode{
			{Kind: "leaf"},
			{Kind: "leaf"},
		},
	}
	require.NoError(t, validateAndNormalizeTree(tr, "root"))
	require.Len(t, tr.Ratios, 2)
	assert.InDelta(t, 0.5, tr.Ratios[0], 1e-12)
	assert.InDelta(t, 0.5, tr.Ratios[1], 1e-12)
}

// TestValidateTree_SplitOmittedRatiosAccepted covers the "user didn't
// specify ratios" path. The renderer falls back to equal weights, so
// the validator must allow ratios to be empty.
func TestValidateTree_SplitOmittedRatiosAccepted(t *testing.T) {
	tr := &targetNode{
		Kind:      "split",
		Direction: "horizontal",
		Children: []*targetNode{
			{Kind: "leaf"},
			{Kind: "leaf"},
		},
	}
	require.NoError(t, validateAndNormalizeTree(tr, "root"))
	assert.Empty(t, tr.Ratios)
}

// TestValidateTree_GridHappyPath covers a 2x3 grid with explicit
// row and column ratios. 6 children required (rows*cols).
func TestValidateTree_GridHappyPath(t *testing.T) {
	tr := &targetNode{
		Kind:      "grid",
		Rows:      2,
		Cols:      3,
		RowRatios: []float64{1, 1},
		ColRatios: []float64{1, 1, 1},
		Children: []*targetNode{
			{Kind: "leaf"}, {Kind: "leaf"}, {Kind: "leaf"},
			{Kind: "leaf"}, {Kind: "leaf"}, {Kind: "leaf"},
		},
	}
	require.NoError(t, validateAndNormalizeTree(tr, "root"))
	require.Len(t, tr.RowRatios, 2)
	assert.InDelta(t, 0.5, tr.RowRatios[0], 1e-12)
	require.Len(t, tr.ColRatios, 3)
	assert.InDelta(t, 1.0/3, tr.ColRatios[0], 1e-12)
}

// TestValidateTree_NestedHappyPath pins the recursive case: SPLIT
// containing a GRID containing leaves. Validator must walk children
// regardless of depth and report path-anchored errors when something
// breaks (covered separately below).
func TestValidateTree_NestedHappyPath(t *testing.T) {
	tr := &targetNode{
		Kind:      "split",
		Direction: "vertical",
		Children: []*targetNode{
			{Kind: "leaf"},
			{
				Kind: "grid",
				Rows: 1, Cols: 2,
				Children: []*targetNode{
					{Kind: "leaf"},
					{Kind: "leaf"},
				},
			},
		},
	}
	require.NoError(t, validateAndNormalizeTree(tr, "root"))
}

// TestValidateTree_RejectsUnknownKind catches the basic typo path
// (e.g. "splt" or empty string at the root).
func TestValidateTree_RejectsUnknownKind(t *testing.T) {
	tr := &targetNode{Kind: "splt"}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized kind")
}

// TestValidateTree_RejectsNullChild covers the case where someone
// emits `"children": [null, {...}]` from a buggy generator. We catch
// the nil so downstream walkers don't dereference it.
func TestValidateTree_RejectsNullChild(t *testing.T) {
	tr := &targetNode{
		Kind:      "split",
		Direction: "vertical",
		Children: []*targetNode{
			nil,
			{Kind: "leaf"},
		},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "children[0]")
	assert.Contains(t, err.Error(), "null")
}

// TestValidateTree_RejectsLeafWithChildren is the structural rule
// for LEAF: a LEAF projects as a tile and has no descendants. If we
// accept children on it the projection would either drop them (silent
// data loss) or trip the projection's "leaf has children" guard.
func TestValidateTree_RejectsLeafWithChildren(t *testing.T) {
	tr := &targetNode{
		Kind:     "leaf",
		Children: []*targetNode{{Kind: "leaf"}},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LEAF nodes cannot have children")
}

// TestValidateTree_RejectsLeafWithDirection / RejectsLeafWithRatios
// etc. — LEAF rejects every SPLIT/GRID-only field. Catching these
// early protects against trees where a typo turned a SPLIT into a
// LEAF but kept its decorators.
func TestValidateTree_RejectsLeafWithDecorators(t *testing.T) {
	cases := map[string]*targetNode{
		"direction":  {Kind: "leaf", Direction: "vertical"},
		"ratios":     {Kind: "leaf", Ratios: []float64{0.5, 0.5}},
		"rows":       {Kind: "leaf", Rows: 1},
		"cols":       {Kind: "leaf", Cols: 1},
		"row_ratios": {Kind: "leaf", RowRatios: []float64{1}},
		"col_ratios": {Kind: "leaf", ColRatios: []float64{1}},
	}
	for label, tr := range cases {
		err := validateAndNormalizeTree(tr, "root")
		require.Error(t, err, "LEAF + %s must be rejected", label)
	}
}

// TestValidateTree_RejectsSplitWithOneChild covers the SPLIT cardinality
// rule. The frontend's tile-split implementation always produces
// exactly 2 children at creation; layout set allows any N >= 2 so a
// scripted layout can pre-bake 3+ panes, but it must not allow 0 or 1.
func TestValidateTree_RejectsSplitWithOneChild(t *testing.T) {
	tr := &targetNode{
		Kind:      "split",
		Direction: "vertical",
		Children:  []*targetNode{{Kind: "leaf"}},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SPLIT requires at least 2 children")
}

// TestValidateTree_RejectsSplitWithNoChildren catches the zero case
// specifically — the more-than-one-child guard's "got 0" branch.
func TestValidateTree_RejectsSplitWithNoChildren(t *testing.T) {
	tr := &targetNode{Kind: "split", Direction: "vertical"}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "got 0")
}

// TestValidateTree_RejectsSplitWithoutDirection. The renderer can't
// pick an axis without direction; default-empty would write
// UNSPECIFIED into the SPLIT and confuse downstream consumers.
func TestValidateTree_RejectsSplitWithoutDirection(t *testing.T) {
	tr := &targetNode{
		Kind: "split",
		Children: []*targetNode{
			{Kind: "leaf"}, {Kind: "leaf"},
		},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "direction")
}

// TestValidateTree_RejectsSplitWithGridFields catches mixed-up trees:
// a SPLIT with rows/cols/row_ratios/col_ratios is a sign the author
// confused the two node kinds; surface it with a hint.
func TestValidateTree_RejectsSplitWithGridFields(t *testing.T) {
	cases := map[string]*targetNode{
		"rows": {
			Kind: "split", Direction: "vertical", Rows: 2,
			Children: []*targetNode{{Kind: "leaf"}, {Kind: "leaf"}},
		},
		"cols": {
			Kind: "split", Direction: "vertical", Cols: 2,
			Children: []*targetNode{{Kind: "leaf"}, {Kind: "leaf"}},
		},
		"row_ratios": {
			Kind: "split", Direction: "vertical",
			RowRatios: []float64{1, 1},
			Children:  []*targetNode{{Kind: "leaf"}, {Kind: "leaf"}},
		},
		"col_ratios": {
			Kind: "split", Direction: "vertical",
			ColRatios: []float64{1, 1},
			Children:  []*targetNode{{Kind: "leaf"}, {Kind: "leaf"}},
		},
	}
	for label, tr := range cases {
		err := validateAndNormalizeTree(tr, "root")
		require.Error(t, err, "SPLIT + %s must be rejected", label)
	}
}

// TestValidateTree_RejectsSplitRatiosLengthMismatch. The renderer
// needs a per-child fraction; len(ratios) != len(children) is
// undefined behavior.
func TestValidateTree_RejectsSplitRatiosLengthMismatch(t *testing.T) {
	tr := &targetNode{
		Kind:      "split",
		Direction: "vertical",
		Ratios:    []float64{0.3, 0.3, 0.4},
		Children: []*targetNode{
			{Kind: "leaf"}, {Kind: "leaf"},
		},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "length 3 must match children count 2")
}

// TestValidateTree_RejectsGridZeroDimensions. Both rows and cols
// must be >= 1; without children there's nothing to project.
func TestValidateTree_RejectsGridZeroDimensions(t *testing.T) {
	tr := &targetNode{Kind: "grid", Rows: 0, Cols: 2}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GRID requires rows>=1")
}

// TestValidateTree_RejectsGridOverCap pins the MaxGridDimension
// boundary. Server enforces 20; CLI must reject earlier so the user
// gets a useful message instead of BATCH_REJECTION_VALUE_DOMAIN.
func TestValidateTree_RejectsGridOverCap(t *testing.T) {
	tr := &targetNode{Kind: "grid", Rows: 21, Cols: 1}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "capped at 20")
}

// TestValidateTree_RejectsGridWrongCellCount catches the most common
// hand-authored mistake: rows=2 cols=2 means 4 cells, not 3 or 5.
func TestValidateTree_RejectsGridWrongCellCount(t *testing.T) {
	tr := &targetNode{
		Kind: "grid", Rows: 2, Cols: 2,
		Children: []*targetNode{
			{Kind: "leaf"}, {Kind: "leaf"}, {Kind: "leaf"},
		},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires 4 cells (got 3)")
}

// TestValidateTree_RejectsGridWithSplitFields. Mirror of the SPLIT
// rejection: GRID + direction / ratios is a sign of confusion.
func TestValidateTree_RejectsGridWithSplitFields(t *testing.T) {
	cases := map[string]*targetNode{
		"direction": {
			Kind: "grid", Rows: 1, Cols: 1, Direction: "vertical",
			Children: []*targetNode{{Kind: "leaf"}},
		},
		"ratios": {
			Kind: "grid", Rows: 1, Cols: 1, Ratios: []float64{1.0},
			Children: []*targetNode{{Kind: "leaf"}},
		},
	}
	for label, tr := range cases {
		err := validateAndNormalizeTree(tr, "root")
		require.Error(t, err, "GRID + %s must be rejected", label)
	}
}

// TestValidateTree_RejectsGridRowRatiosLengthMismatch covers the
// row-axis ratio length rule.
func TestValidateTree_RejectsGridRowRatiosLengthMismatch(t *testing.T) {
	tr := &targetNode{
		Kind: "grid", Rows: 2, Cols: 2,
		RowRatios: []float64{0.3, 0.3, 0.4},
		Children: []*targetNode{
			{Kind: "leaf"}, {Kind: "leaf"},
			{Kind: "leaf"}, {Kind: "leaf"},
		},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "row_ratios length 3 must equal rows 2")
}

// TestValidateTree_RejectsGridColRatiosLengthMismatch is the
// column-axis counterpart.
func TestValidateTree_RejectsGridColRatiosLengthMismatch(t *testing.T) {
	tr := &targetNode{
		Kind: "grid", Rows: 2, Cols: 2,
		ColRatios: []float64{0.5},
		Children: []*targetNode{
			{Kind: "leaf"}, {Kind: "leaf"},
			{Kind: "leaf"}, {Kind: "leaf"},
		},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "col_ratios length 1 must equal cols 2")
}

// TestValidateTree_NestedErrorIncludesPath pins the dotted-path
// breadcrumb. When a 4-level-deep node is malformed the user must
// be able to find it without bisecting the JSON.
func TestValidateTree_NestedErrorIncludesPath(t *testing.T) {
	tr := &targetNode{
		Kind:      "split",
		Direction: "vertical",
		Children: []*targetNode{
			{Kind: "leaf"},
			{
				Kind:      "split",
				Direction: "horizontal",
				Children: []*targetNode{
					{Kind: "leaf"},
					{Kind: "leaf", Direction: "vertical"}, // <- bad
				},
			},
		},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "root.children[1].children[1]")
}

// TestValidateTree_NestedRatiosRejectionBubbles confirms that ratio
// validation runs deep too — a SPLIT three levels in with malformed
// ratios should still be flagged with its path.
func TestValidateTree_NestedRatiosRejectionBubbles(t *testing.T) {
	tr := &targetNode{
		Kind:      "split",
		Direction: "vertical",
		Children: []*targetNode{
			{Kind: "leaf"},
			{
				Kind:      "split",
				Direction: "horizontal",
				Ratios:    []float64{0, 0}, // can't normalize
				Children: []*targetNode{
					{Kind: "leaf"}, {Kind: "leaf"},
				},
			},
		},
	}
	err := validateAndNormalizeTree(tr, "root")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "root.children[1]")
	assert.Contains(t, err.Error(), "positive")
}

// --- decodeTargetTree ---

// TestDecodeTargetTree_RejectsEmptyInput covers the empty-stdin path:
// `... | leapmux remote layout set --stdin --force` with no piped
// data should fail before we try to JSON-decode an empty buffer.
func TestDecodeTargetTree_RejectsEmptyInput(t *testing.T) {
	_, err := decodeTargetTree(strings.NewReader(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

// TestDecodeTargetTree_RejectsMalformedJSON catches the obvious
// failure mode and surfaces a JSON-shaped error.
func TestDecodeTargetTree_RejectsMalformedJSON(t *testing.T) {
	_, err := decodeTargetTree(strings.NewReader("not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode tree JSON")
}

// TestDecodeTargetTree_HappyPath end-to-ends the JSON → validate →
// normalize pipeline. Verifies the parsed tree comes back with
// normalized ratios in place.
func TestDecodeTargetTree_HappyPath(t *testing.T) {
	src := `{
	  "kind": "split",
	  "direction": "vertical",
	  "ratios": [1, 3],
	  "children": [
	    {"kind": "leaf"},
	    {"kind": "leaf"}
	  ]
	}`
	tr, err := decodeTargetTree(strings.NewReader(src))
	require.NoError(t, err)
	require.Len(t, tr.Ratios, 2)
	assert.InDelta(t, 0.25, tr.Ratios[0], 1e-12)
	assert.InDelta(t, 0.75, tr.Ratios[1], 1e-12)
}

// TestDecodeTargetTree_RejectsInvalidTree runs the integration:
// JSON parses cleanly but the validator catches the structural
// problem. Confirms the validator's error message reaches the caller.
func TestDecodeTargetTree_RejectsInvalidTree(t *testing.T) {
	src := `{"kind":"split","direction":"vertical","children":[{"kind":"leaf"}]}`
	_, err := decodeTargetTree(strings.NewReader(src))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SPLIT requires at least 2 children")
}

// --- validateSplitRatiosLength ---

// TestValidateSplitRatiosLength_HappyPath pins the success path:
// ratios length matches live child count, no error.
func TestValidateSplitRatiosLength_HappyPath(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		splitNode("T", "", "").
		leafNode("a", "T", "a").
		leafNode("b", "T", "b").
		st
	assert.NoError(t, validateSplitRatiosLength(state, "T", []float64{0.5, 0.5}))
}

// TestValidateSplitRatiosLength_RejectsTooShort / TooLong pin the
// off-by-one cases. The error message names both numbers so the user
// can fix the right end without diffing the layout.
func TestValidateSplitRatiosLength_RejectsMismatch(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		splitNode("T", "", "").
		leafNode("a", "T", "a").
		leafNode("b", "T", "b").
		leafNode("c", "T", "c").
		st
	code, msg := captureEmit(t, func() error {
		return validateSplitRatiosLength(state, "T", []float64{0.5, 0.5})
	})
	assert.Equal(t, "invalid_request", code)
	assert.Contains(t, msg, "length 2")
	assert.Contains(t, msg, "child count 3")
}

// TestValidateSplitRatiosLength_IgnoresTombstonedChildren ensures
// the count is over LIVE children only. A tombstoned former child
// must not count against the ratios length, otherwise users with
// historical tombstones in the state can't update ratios at all.
func TestValidateSplitRatiosLength_IgnoresTombstonedChildren(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		splitNode("T", "", "").
		leafNode("a", "T", "a").
		leafNode("b", "T", "b").
		tombstonedNode("dead", "T").
		st
	assert.NoError(t, validateSplitRatiosLength(state, "T", []float64{0.5, 0.5}),
		"tombstoned children must not count toward the live count")
}

// --- validateGridRatiosShape ---

// TestValidateGridRatiosShape_HappyPath pins the success path for
// both axes at once.
func TestValidateGridRatiosShape_HappyPath(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "G").
		st
	state.GetNodes()["G"] = &leapmuxv1.NodeRecord{
		NodeId: "G",
		Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_GRID},
		Rows:   &leapmuxv1.LWWUint32{Value: 2},
		Cols:   &leapmuxv1.LWWUint32{Value: 3},
	}
	assert.NoError(t, validateGridRatiosShape(state, "G",
		[]float64{0.5, 0.5},
		[]float64{0.3, 0.3, 0.4},
	))
}

// TestValidateGridRatiosShape_RejectsRowMismatch covers the row axis.
func TestValidateGridRatiosShape_RejectsRowMismatch(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "G").st
	state.GetNodes()["G"] = &leapmuxv1.NodeRecord{
		NodeId: "G",
		Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_GRID},
		Rows:   &leapmuxv1.LWWUint32{Value: 2},
		Cols:   &leapmuxv1.LWWUint32{Value: 2},
	}
	code, msg := captureEmit(t, func() error {
		return validateGridRatiosShape(state, "G", []float64{0.5}, nil)
	})
	assert.Equal(t, "invalid_request", code)
	assert.Contains(t, msg, "--row-ratios length 1")
	assert.Contains(t, msg, "rows 2")
}

// TestValidateGridRatiosShape_RejectsColMismatch covers the col axis.
func TestValidateGridRatiosShape_RejectsColMismatch(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "G").st
	state.GetNodes()["G"] = &leapmuxv1.NodeRecord{
		NodeId: "G",
		Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_GRID},
		Rows:   &leapmuxv1.LWWUint32{Value: 2},
		Cols:   &leapmuxv1.LWWUint32{Value: 3},
	}
	code, msg := captureEmit(t, func() error {
		return validateGridRatiosShape(state, "G", nil, []float64{0.5, 0.5})
	})
	assert.Equal(t, "invalid_request", code)
	assert.Contains(t, msg, "--col-ratios length 2")
	assert.Contains(t, msg, "cols 3")
}

// --- buildLayoutSetOps ---

// TestBuildLayoutSetOps_LeafTarget pins the leaf-only rewrite shape:
// existing non-root nodes are tombstoned, the root's Kind register
// is set to LEAF, and every live tab repoints to the root (since
// firstLeaf == root when the target is a bare leaf). This is the
// "reset to a blank workspace" path.
func TestBuildLayoutSetOps_LeafTarget(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "R").
		splitNode("R", "", "").
		leafNode("a", "R", "a").
		leafNode("b", "R", "b").
		tab("tab-1", "a", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("tab-2", "b", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	target := &targetNode{Kind: "leaf"}

	bs := testBootstrap(state)
	ops, rootID, firstLeaf, err := buildLayoutSetOps(bs, "ws-1", target)
	require.NoError(t, err)
	assert.Equal(t, "R", rootID)
	assert.Equal(t, "R", firstLeaf, "leaf target -> tabs land on the (kept) root")

	cases := opCases(ops)
	// Root flipped to LEAF in place.
	assert.Contains(t, cases, "setNodeKind:R=NODE_KIND_LEAF")
	// Both existing children tombstoned.
	assert.Contains(t, cases, "tombstoneNode:a")
	assert.Contains(t, cases, "tombstoneNode:b")
	// Root must NOT be tombstoned -- set-once root contract.
	assert.NotContains(t, cases, "tombstoneNode:R", "workspace root must stay alive")
	// Every live tab repointed to firstLeaf (= root).
	assert.Contains(t, cases, "setTabTileId:tab-1=R")
	assert.Contains(t, cases, "setTabTileId:tab-2=R")
}

// TestBuildLayoutSetOps_SplitTarget pins the synthesize-subtree shape.
// The root must flip to SPLIT, two new leaves are minted with
// parent_id=root + sibling positions, and every live tab repoints
// to firstLeaf -- the depth-first first leaf, which is the first
// synthesized child (not the root).
func TestBuildLayoutSetOps_SplitTarget(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "R").
		leafNode("R", "", "").
		tab("tab-1", "R", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	target := &targetNode{
		Kind:      "split",
		Direction: "vertical",
		Children: []*targetNode{
			{Kind: "leaf"},
			{Kind: "leaf"},
		},
	}

	bs := testBootstrap(state)
	ops, rootID, firstLeaf, err := buildLayoutSetOps(bs, "ws-1", target)
	require.NoError(t, err)
	assert.Equal(t, "R", rootID)
	assert.NotEqual(t, "R", firstLeaf, "split target -> firstLeaf is a synthesized child, not the root")
	assert.NotEmpty(t, firstLeaf)

	cases := opCases(ops)
	assert.Contains(t, cases, "setNodeKind:R=NODE_KIND_SPLIT")
	assert.Contains(t, cases, "setTabTileId:tab-1="+firstLeaf,
		"live tabs must repoint to the new tree's first leaf so the placement invariant holds")
}

// TestBuildLayoutSetOps_RejectsMissingRoot covers the degenerate case
// where the resolved workspace has no root node yet. Without this
// guard the next step would try to tombstone descendants of an empty
// id and the batch would land in the validator as a no-op.
func TestBuildLayoutSetOps_RejectsMissingRoot(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "").st
	bs := testBootstrap(state)
	code, msg := captureEmit(t, func() error {
		_, _, _, err := buildLayoutSetOps(bs, "ws-1", &targetNode{Kind: "leaf"})
		return err
	})
	assert.Equal(t, "invalid_state", code)
	assert.Contains(t, msg, "no root node")
}

// TestBuildLayoutSetOps_SkipsTombstonedTabs confirms Step 4 doesn't
// emit setTabTileId ops for tabs that are already tombstoned. A
// historical tombstone would otherwise turn into a write against a
// dead tab, which the validator rejects.
func TestBuildLayoutSetOps_SkipsTombstonedTabs(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "R").
		leafNode("R", "", "").
		tab("live", "R", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tombstonedTab("dead", "R", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	bs := testBootstrap(state)
	ops, _, _, err := buildLayoutSetOps(bs, "ws-1", &targetNode{Kind: "leaf"})
	require.NoError(t, err)
	cases := opCases(ops)
	assert.Contains(t, cases, "setTabTileId:live=R")
	for _, c := range cases {
		assert.NotContains(t, c, "setTabTileId:dead=", "tombstoned tabs must be skipped")
	}
}

// TestValidateGridRatiosShape_NilSlicesSkipChecks confirms that
// passing nil for either side is treated as "don't update that axis"
// — the partial-update path is the dominant set-grid-ratios call,
// since users typically tweak one axis at a time.
func TestValidateGridRatiosShape_NilSlicesSkipChecks(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "G").st
	state.GetNodes()["G"] = &leapmuxv1.NodeRecord{
		NodeId: "G",
		Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_GRID},
		Rows:   &leapmuxv1.LWWUint32{Value: 2},
		Cols:   &leapmuxv1.LWWUint32{Value: 2},
	}
	assert.NoError(t, validateGridRatiosShape(state, "G", nil, nil))
	assert.NoError(t, validateGridRatiosShape(state, "G", []float64{0.5, 0.5}, nil))
	assert.NoError(t, validateGridRatiosShape(state, "G", nil, []float64{0.5, 0.5}))
}
