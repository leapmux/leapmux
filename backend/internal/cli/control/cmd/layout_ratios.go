package cmd

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

type splitRatiosResult struct {
	SplitTileID string    `json:"split_tile_id"`
	Ratios      []float64 `json:"ratios"`
}

func (r splitRatiosResult) QuietLines() []string { return []string{r.SplitTileID} }

type gridRatiosResult struct {
	GridTileID string    `json:"grid_tile_id"`
	RowRatios  []float64 `json:"row_ratios,omitempty"`
	ColRatios  []float64 `json:"col_ratios,omitempty"`
}

func (r gridRatiosResult) QuietLines() []string { return []string{r.GridTileID} }

// runTileSetRatios writes a fresh `ratios` slice on a SPLIT node. The
// <tile> argument takes every target form, as every other tile command's
// does. The handler rejects a target that is not a SPLIT explicitly —
// `set-grid-ratios` is the GRID-node counterpart.
func runTileSetRatios(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ratiosCSV := stringFlag(inv, "ratios")
	if ratiosCSV == "" {
		return nil, cli.Usagef("--ratios is required")
	}
	ratios, err := parseRatiosCSV(ratiosCSV)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	cc, _, splitID, err := s.openTile(inv.Arg("tile"))
	if err != nil {
		return nil, err
	}
	if err := preflightTileKind(cc.bs.State, splitID, leapmuxv1.NodeKind_NODE_KIND_SPLIT, "set-ratios", "SPLIT", "(use set-grid-ratios for grids)"); err != nil {
		return nil, err
	}
	if err := validateSplitRatiosLength(cc.bs.State, splitID, ratios); err != nil {
		return nil, err
	}
	if err := cc.submitOps([]*leapmuxv1.CrdtOp{opSetNodeRatios(cc.bs, splitID, ratios)}); err != nil {
		return nil, err
	}
	return splitRatiosResult{SplitTileID: splitID, Ratios: ratios}, nil
}

// runTileSetGridRatios writes rowRatios and/or colRatios on a GRID
// node. At least one of --row-ratios / --col-ratios must be supplied;
// passing both updates both axes in the same batch (one CRDT op per
// axis). The <tile> argument takes every target form, as every other
// tile command's does.
func runTileSetGridRatios(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	rowRatiosCSV := stringFlag(inv, "row-ratios")
	colRatiosCSV := stringFlag(inv, "col-ratios")
	if rowRatiosCSV == "" && colRatiosCSV == "" {
		return nil, cli.Usagef("at least one of --row-ratios / --col-ratios is required")
	}
	var rowRatios, colRatios []float64
	if rowRatiosCSV != "" {
		r, err := parseRatiosCSV(rowRatiosCSV)
		if err != nil {
			return nil, cli.Wrap(cli.CodeUsage, fmt.Errorf("--row-ratios: %w", err), "")
		}
		rowRatios = r
	}
	if colRatiosCSV != "" {
		c, err := parseRatiosCSV(colRatiosCSV)
		if err != nil {
			return nil, cli.Wrap(cli.CodeUsage, fmt.Errorf("--col-ratios: %w", err), "")
		}
		colRatios = c
	}
	cc, _, gridID, err := s.openTile(inv.Arg("tile"))
	if err != nil {
		return nil, err
	}
	if err := preflightTileKind(cc.bs.State, gridID, leapmuxv1.NodeKind_NODE_KIND_GRID, "set-grid-ratios", "GRID", ""); err != nil {
		return nil, err
	}
	if err := validateGridRatiosShape(cc.bs.State, gridID, rowRatios, colRatios); err != nil {
		return nil, err
	}
	ops := []*leapmuxv1.CrdtOp{}
	if rowRatios != nil {
		ops = append(ops, opSetNodeRowRatios(cc.bs, gridID, rowRatios))
	}
	if colRatios != nil {
		ops = append(ops, opSetNodeColRatios(cc.bs, gridID, colRatios))
	}
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return gridRatiosResult{GridTileID: gridID, RowRatios: rowRatios, ColRatios: colRatios}, nil
}

// runTileResize adjusts one tile's ratio inside its parent SPLIT node, scaling
// its siblings proportionally so all ratios remain positive and sum to 1.
func runTileResize(inv *cli.Invocation) (any, error) {
	ratio, err := floatFlag(inv, "ratio")
	if err != nil {
		return nil, err
	}
	if ratio <= 0 || ratio >= 1 {
		return nil, cli.Usagef("--ratio must be strictly between 0 and 1; got %v", ratio)
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, _, tileID, err := s.openTile(inv.Arg("tile"))
	if err != nil {
		return nil, err
	}
	node := cc.bs.State.GetNodes()[tileID]
	parentID := node.GetParentId()
	if parentID == "" {
		return nil, cli.Newf(cli.CodeConflict, "tile %s is the workspace root and has no parent split to resize", tileID)
	}
	parent := cc.bs.State.GetNodes()[parentID]
	if parent == nil || !crdt.HLCIsZero(parent.GetTombstoneAt()) || parent.GetKind().GetValue() != leapmuxv1.NodeKind_NODE_KIND_SPLIT {
		return nil, cli.Newf(cli.CodeConflict, "tile %s is not a child of a split container", tileID).
			WithHint(`for grid cells, use "leapmux tile set-grid-ratios <grid> --row-ratios ... --col-ratios ..."`)
	}
	childrenByParent := crdt.LiveChildrenByParent(cc.bs.State)
	children := crdt.LayoutChildIDs(cc.bs.State.GetNodes(), childrenByParent, parentID)
	idx := slices.Index(children, tileID)
	if idx < 0 || len(children) < 2 {
		return nil, cli.Newf(cli.CodeConflict, "parent split %s has fewer than 2 live children", parentID)
	}
	cur := parent.GetRatios().GetValue().GetValues()
	ratios := make([]float64, len(children))
	if len(cur) == len(children) {
		copy(ratios, cur)
	} else {
		eq := 1.0 / float64(len(children))
		for i := range ratios {
			ratios[i] = eq
		}
	}
	otherSum := 0.0
	for i, v := range ratios {
		if i != idx && v > 0 {
			otherSum += v
		}
	}
	rem := 1.0 - ratio
	ratios[idx] = ratio
	if otherSum <= 0 {
		share := rem / float64(len(ratios)-1)
		for i := range ratios {
			if i != idx {
				ratios[i] = share
			}
		}
	} else {
		for i := range ratios {
			if i != idx {
				ratios[i] = ratios[i] * (rem / otherSum)
			}
		}
	}
	if err := cc.submitOps([]*leapmuxv1.CrdtOp{opSetNodeRatios(cc.bs, parentID, ratios)}); err != nil {
		return nil, err
	}
	return splitRatiosResult{SplitTileID: parentID, Ratios: ratios}, nil
}

type tileSwapResult struct {
	TileA       string `json:"tile_a"`
	TileB       string `json:"tile_b"`
	WorkspaceID string `json:"workspace_id"`
}

func (r tileSwapResult) QuietLines() []string { return []string{r.TileA, r.TileB} }

// runTileSwap swaps the positions (and parents when different) of two tiles
// in the same workspace in one atomic CRDT batch.
func runTileSwap(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, wsA, tileA, err := s.openTile(inv.Arg("tile-a"))
	if err != nil {
		return nil, err
	}
	targetB, err := s.resolveOne(resolve.KindTile, inv.Arg("tile-b"))
	if err != nil {
		return nil, err
	}
	tileB := targetB.ID
	if tileA == tileB {
		return nil, cli.Usagef("cannot swap tile %s with itself", tileA)
	}
	if err := preflightTile(cc.bs.State, wsA, tileB); err != nil {
		return nil, err
	}
	nodeA := cc.bs.State.GetNodes()[tileA]
	nodeB := cc.bs.State.GetNodes()[tileB]
	parentA := nodeA.GetParentId()
	parentB := nodeB.GetParentId()
	if parentA == "" || parentB == "" {
		return nil, cli.Newf(cli.CodeConflict, "cannot swap a workspace root tile")
	}
	if isTileAncestor(cc.bs.State, tileA, tileB) || isTileAncestor(cc.bs.State, tileB, tileA) {
		return nil, cli.Newf(cli.CodeConflict, "cannot swap a tile with one of its descendants")
	}
	posA := nodeA.GetPosition().GetValue()
	posB := nodeB.GetPosition().GetValue()
	ops := []*leapmuxv1.CrdtOp{
		opSetNodePosition(cc.bs, tileA, posB),
		opSetNodePosition(cc.bs, tileB, posA),
	}
	if parentA != parentB {
		ops = append(ops,
			opSetNodeParentID(cc.bs, tileA, parentB),
			opSetNodeParentID(cc.bs, tileB, parentA),
		)
	}
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return tileSwapResult{TileA: tileA, TileB: tileB, WorkspaceID: wsA}, nil
}

func isTileAncestor(state *leapmuxv1.UserMaterialized, ancestor, descendant string) bool {
	seen := map[string]bool{}
	cur := descendant
	for cur != "" && !seen[cur] {
		seen[cur] = true
		n := state.GetNodes()[cur]
		if n == nil {
			return false
		}
		p := n.GetParentId()
		if p == ancestor {
			return true
		}
		cur = p
	}
	return false
}

func completeSwapTiles(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeTargetsOfKind(cmd, resolve.KindTile, toComplete, true)
}

// parseRatiosCSV parses a comma-separated float list and normalizes
// it so the resulting slice sums to exactly 1.0 (within float64 ULP),
// matching the server's `validRatios` 1e-9 tolerance. Lenient on
// input: users can pass any positive weights ("1,2,1" -> [0.25, 0.5,
// 0.25]) or already-normalized values ("0.5, 0.5" -> [0.5, 0.5]).
// Rejects empty lists, malformed numbers, negative weights, NaN/Inf,
// and all-zero inputs — every one of those would either drop on the
// wire as a value-domain rejection or produce a meaningless layout.
func parseRatiosCSV(s string) ([]float64, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("ratios cannot be empty")
	}
	parts := strings.Split(s, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number: %w", strings.TrimSpace(p), err)
		}
		out = append(out, v)
	}
	return normalizeRatios(out)
}

// normalizeRatios validates and rescales `values` so the resulting
// slice sums to exactly 1.0. Returns an error when:
//   - the slice is empty,
//   - any value is NaN, +/-Inf, or negative,
//   - every value is zero (no positive weight to normalize against).
//
// The output is always a fresh slice — the input is not mutated.
func normalizeRatios(values []float64) ([]float64, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("ratios cannot be empty")
	}
	var sum float64
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("ratio %v must be a finite number", v)
		}
		if v < 0 {
			return nil, fmt.Errorf("ratio %v must be non-negative", v)
		}
		sum += v
	}
	if sum <= 0 {
		return nil, fmt.Errorf("ratios must include at least one positive value")
	}
	out := make([]float64, len(values))
	for i, v := range values {
		out[i] = v / sum
	}
	return out, nil
}

// ratiosForLayoutSize fills missing weights the way the browser does and
// normalizes the result for import. A size change can discard every
// positive weight, in which case the remaining cells take equal weights.
func ratiosForLayoutSize(values []float64, count int) []float64 {
	if len(values) == count {
		return values
	}
	resized := crdt.EqualRatios(count)
	copy(resized, values)
	normalized, err := normalizeRatios(resized)
	if err != nil {
		return crdt.EqualRatios(count)
	}
	return normalized
}

// validateSplitRatiosLength rejects --ratios whose length doesn't
// match the SPLIT's live child count. Catches the mistake on the
// client so the user gets a "ratios length N must match SPLIT child
// count M" error instead of an opaque BATCH_REJECTION_* from the
// server (or worse: a successful write with a ratios slice the
// renderer doesn't know how to consume).
func validateSplitRatiosLength(state *leapmuxv1.UserMaterialized, splitID string, ratios []float64) error {
	live := crdt.LiveChildrenByParent(state)[splitID]
	if len(ratios) != len(live) {
		return cli.Usagef("--ratios length %d does not match SPLIT %s live child count %d",
			len(ratios), splitID, len(live))
	}
	return nil
}

// validateGridRatiosShape rejects --row-ratios / --col-ratios whose
// length doesn't match the GRID's `rows` / `cols` register. Either
// slice may be nil (the caller skips that side of the update); only
// non-nil slices are checked. Same catch-it-early rationale as
// validateSplitRatiosLength.
func validateGridRatiosShape(state *leapmuxv1.UserMaterialized, gridID string, rowRatios, colRatios []float64) error {
	rec := state.GetNodes()[gridID]
	rows := int(rec.GetRows().GetValue())
	cols := int(rec.GetCols().GetValue())
	if len(rowRatios) > 0 && len(rowRatios) != rows {
		return cli.Usagef("--row-ratios length %d does not match GRID %s rows %d",
			len(rowRatios), gridID, rows)
	}
	if len(colRatios) > 0 && len(colRatios) != cols {
		return cli.Usagef("--col-ratios length %d does not match GRID %s cols %d",
			len(colRatios), gridID, cols)
	}
	return nil
}
