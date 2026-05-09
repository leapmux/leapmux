package cmd

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// RunTileSetRatios writes a fresh `ratios` slice on a SPLIT node.
// The split's tile id flows through the universal resolver, so
// `--tile-id` can be derived from `--tab-id` / env defaults like
// every other tile verb. The handler rejects non-SPLIT targets
// explicitly — `set-grid-ratios` is the GRID-node counterpart.
func RunTileSetRatios(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, ratiosCSV string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	fs.StringVar(&ratiosCSV, "ratios", "", "comma-separated ratios summing to 1.0 (required)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if ratiosCSV == "" {
		return remote.EmitError("invalid_request", "--ratios is required")
	}
	ratios, err := parseRatiosCSV(ratiosCSV)
	if err != nil {
		return remote.EmitErrorWith("invalid_request", err)
	}
	cc, _, splitID, err := openTileCRDTCall(hub, in)
	if err != nil {
		return err
	}
	defer cc.close()
	if err := preflightTileKind(cc.bs.State, splitID, leapmuxv1.NodeKind_NODE_KIND_SPLIT, "set-ratios", "SPLIT", "(use set-grid-ratios for grids)"); err != nil {
		return err
	}
	if err := validateSplitRatiosLength(cc.bs.State, splitID, ratios); err != nil {
		return err
	}
	if err := cc.submitOps([]*leapmuxv1.OrgOp{opSetNodeRatios(cc.bs, splitID, ratios)}); err != nil {
		return err
	}
	return remote.EmitData(map[string]any{"split_tile_id": splitID, "ratios": ratios})
}

// RunTileSetGridRatios writes rowRatios and/or colRatios on a GRID
// node. At least one of --row-ratios / --col-ratios must be supplied;
// passing both updates both axes in the same batch (one CRDT op per
// axis). The grid's tile id flows through the universal resolver, so
// `--tile-id` can be derived from `--tab-id` / env defaults like
// every other tile verb.
func RunTileSetGridRatios(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, rowRatiosCSV, colRatiosCSV string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	fs.StringVar(&rowRatiosCSV, "row-ratios", "", "comma-separated row ratios (at least one of --row-ratios / --col-ratios is required)")
	fs.StringVar(&colRatiosCSV, "col-ratios", "", "comma-separated column ratios (at least one of --row-ratios / --col-ratios is required)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if rowRatiosCSV == "" && colRatiosCSV == "" {
		return remote.EmitError("invalid_request", "at least one of --row-ratios / --col-ratios is required")
	}
	var rowRatios, colRatios []float64
	if rowRatiosCSV != "" {
		r, err := parseRatiosCSV(rowRatiosCSV)
		if err != nil {
			return remote.EmitErrorWith("invalid_request", fmt.Errorf("--row-ratios: %w", err))
		}
		rowRatios = r
	}
	if colRatiosCSV != "" {
		c, err := parseRatiosCSV(colRatiosCSV)
		if err != nil {
			return remote.EmitErrorWith("invalid_request", fmt.Errorf("--col-ratios: %w", err))
		}
		colRatios = c
	}
	cc, _, gridID, err := openTileCRDTCall(hub, in)
	if err != nil {
		return err
	}
	defer cc.close()
	if err := preflightTileKind(cc.bs.State, gridID, leapmuxv1.NodeKind_NODE_KIND_GRID, "set-grid-ratios", "GRID", ""); err != nil {
		return err
	}
	if err := validateGridRatiosShape(cc.bs.State, gridID, rowRatios, colRatios); err != nil {
		return err
	}
	ops := []*leapmuxv1.OrgOp{}
	if rowRatios != nil {
		ops = append(ops, opSetNodeRowRatios(cc.bs, gridID, rowRatios))
	}
	if colRatios != nil {
		ops = append(ops, opSetNodeColRatios(cc.bs, gridID, colRatios))
	}
	if err := cc.submitOps(ops); err != nil {
		return err
	}
	out := map[string]any{"grid_tile_id": gridID}
	if rowRatios != nil {
		out["row_ratios"] = rowRatios
	}
	if colRatios != nil {
		out["col_ratios"] = colRatios
	}
	return remote.EmitData(out)
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
func validateSplitRatiosLength(state *leapmuxv1.OrgMaterialized, splitID string, ratios []float64) error {
	live := crdt.LiveChildrenByParent(state)[splitID]
	if len(ratios) != len(live) {
		return remote.EmitError("invalid_request",
			fmt.Sprintf("--ratios length %d does not match SPLIT %s live child count %d",
				len(ratios), splitID, len(live)))
	}
	return nil
}

// validateGridRatiosShape rejects --row-ratios / --col-ratios whose
// length doesn't match the GRID's `rows` / `cols` register. Either
// slice may be nil (the caller skips that side of the update); only
// non-nil slices are checked. Same catch-it-early rationale as
// validateSplitRatiosLength.
func validateGridRatiosShape(state *leapmuxv1.OrgMaterialized, gridID string, rowRatios, colRatios []float64) error {
	rec := state.GetNodes()[gridID]
	rows := int(rec.GetRows().GetValue())
	cols := int(rec.GetCols().GetValue())
	if len(rowRatios) > 0 && len(rowRatios) != rows {
		return remote.EmitError("invalid_request",
			fmt.Sprintf("--row-ratios length %d does not match GRID %s rows %d",
				len(rowRatios), gridID, rows))
	}
	if len(colRatios) > 0 && len(colRatios) != cols {
		return remote.EmitError("invalid_request",
			fmt.Sprintf("--col-ratios length %d does not match GRID %s cols %d",
				len(colRatios), gridID, cols))
	}
	return nil
}
