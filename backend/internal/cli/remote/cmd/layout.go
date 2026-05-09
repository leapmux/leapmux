package cmd

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
)

// Layout / tile-tree mutation commands. Every mutator follows the
// projection-driven CRDT contract:
//
//  1. Bootstrap the org via WatchOrg → OrgMaterialized.
//  2. Resolve parent_id chains from the materialized state when the op
//     batch needs them (e.g. replaceGridWithLeaf inherits the grid's
//     parent).
//  3. Mint fresh node ids for every new entity.
//  4. Submit a single OpBatch — atomic per-batch commit/reject.
//
// The in-place mutation model (T flips kind LEAF → SPLIT / GRID, two
// new children minted) matches the frontend layout-store helpers, so
// CLI-driven mutations converge with browser-driven mutations under
// the same plan-aligned op shape.

// resolveWorkspaceForLayout binds the universal entity flag set and
// runs the resolver to derive the workspace_id every layout / tile
// handler operates on. Returns the resolved Resolved struct so
// callers can also reach the consumed --tile-id without re-parsing
// flags.
//
// Callers compose their own Need: workspace-only verbs (`layout get`,
// `tile list`) pass Need{WorkspaceID: true}; tile-mutating verbs
// (`tile split`, `tile close`, `tile make-grid`, `tile remove-grid`)
// also pass TileID: true so the resolver tries to derive the tile id
// from $LEAPMUX_REMOTE_TAB_ID via LocateTab instead of erroring up
// front with "--tile-id is required".
func resolveWorkspaceForLayout(hub string, need resolve.Need, in resolve.Inputs) (resolve.Resolved, error) {
	c, err := requireClient(hub)
	if err != nil {
		return resolve.Resolved{}, err
	}
	ctx, cancel := rpcDeadline(context.Background())
	defer cancel()
	need.WorkspaceID = true
	return runResolve(ctx, c, need, in)
}

// openTileCRDTCall fuses the resolve-tile-and-bootstrap-CRDT prelude
// every tile-mutating handler runs. Returns the resolved (workspaceID,
// tileID), an open crdtCall the caller must close, or a remote.Emit*
// error envelope. On preflight failure cc is closed and the error is
// returned.
//
// Callers should `defer cc.close()` immediately after a nil error.
func openTileCRDTCall(hub string, in resolve.Inputs) (cc *crdtCall, workspaceID, tileID string, err error) {
	got, err := resolveWorkspaceForLayout(hub, resolve.Need{TileID: true}, in)
	if err != nil {
		return nil, "", "", err
	}
	cc, err = openCRDTCall(hub, got.WorkspaceID)
	if err != nil {
		return nil, "", "", err
	}
	if err := preflightTile(cc.bs.State, got.WorkspaceID, got.TileID); err != nil {
		cc.close()
		return nil, "", "", err
	}
	return cc, got.WorkspaceID, got.TileID, nil
}

// parseSplitDirection maps the user-facing direction string to the
// proto enum. "vertical" / "v" -> SPLIT_DIRECTION_VERTICAL (a
// vertical divider line `|` between two side-by-side panes);
// "horizontal" / "h" -> SPLIT_DIRECTION_HORIZONTAL (a horizontal
// divider line `-` between two stacked panes). The mapping is
// direct — both the CLI flag and the proto enum use the
// divider-line convention.
func parseSplitDirection(s string) (leapmuxv1.SplitDirection, bool) {
	switch s {
	case "horizontal", "h":
		return leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL, true
	case "vertical", "v":
		return leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL, true
	}
	return leapmuxv1.SplitDirection_SPLIT_DIRECTION_UNSPECIFIED, false
}
