package cmd

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

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
	return map[string]any{"split_tile_id": splitID, "ratios": ratios}, nil
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
	out := map[string]any{"grid_tile_id": gridID}
	if rowRatios != nil {
		out["row_ratios"] = rowRatios
	}
	if colRatios != nil {
		out["col_ratios"] = colRatios
	}
	return out, nil
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
