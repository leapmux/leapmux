package cmd

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// runTabMove writes a new tile_id and position on a tab. The
// destination tile may live in a different workspace — cross-workspace
// moves are one CRDT op at this layer.
//
// Destination tile resolution:
//
//  1. --tile, when set.
//  2. --workspace alone → first live leaf (DFS, by position) of that
//     workspace's tile tree.
//  3. --before <tab> / --after <tab> → the reference tab's tile (and the
//     workspace that owns it).
//
// Placement within the destination tile is determined by the
// --first / --last / --before / --after flags (mutually exclusive;
// --last is the default). --before / --after take a tab, not a
// LexoRank — the rank is computed against the sibling tabs already on
// the destination tile.
//
// There is no worker-side bookkeeping to pair this with: a Worker stores no
// workspace id, so the CRDT op is the entire move -- which is also why it lands
// with the tab's Worker offline.
func runTabMove(inv *cli.Invocation) (any, error) {
	rawSpec, err := parsePositionSpec(
		boolFlag(inv, "first"),
		boolFlag(inv, "last"),
		stringFlag(inv, "before"),
		stringFlag(inv, "after"),
	)
	if err != nil {
		return nil, cli.Usagef("%s", err.Error())
	}
	specUsesRefTab := rawSpec.kind == positionBefore || rawSpec.kind == positionAfter
	hasPlacement := inv.Changed("first") || inv.Changed("last") || specUsesRefTab
	if !inv.Changed("tile") && !inv.Changed("workspace") && !hasPlacement {
		return nil, cli.Usagef("give the destination: --tile, --workspace, --first, --last, --before or --after")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	spec, err := s.resolvePositionRef(rawSpec)
	if err != nil {
		return nil, err
	}
	var destTileID, destWorkspaceID string
	if inv.Changed("tile") {
		t, err := s.resolveOne(resolve.KindTile, stringFlag(inv, "tile"))
		if err != nil {
			return nil, err
		}
		destTileID = t.ID
	}
	if inv.Changed("workspace") {
		w, err := s.resolveOne(resolve.KindWorkspace, stringFlag(inv, "workspace"))
		if err != nil {
			return nil, err
		}
		destWorkspaceID = w.ID
	}
	got, err := s.resolveTab(resolve.KindTab, inv.Arg("tab"), resolve.Need{WorkspaceID: true, Want: resolve.Wants{TileID: true, TabType: true}})
	if err != nil {
		return nil, err
	}
	if destTileID == "" && destWorkspaceID == "" && !specUsesRefTab {
		destTileID = got.TileID
	}
	tt := got.Type
	if tt == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		return nil, cli.Newf(cli.CodeNotFound, "the hub reports no type for tab %s", got.ID)
	}
	workspaceIDs := []string{destWorkspaceID}
	if destTileID != "" && destTileID != got.TileID {
		tileWorkspaceID, err := s.deps().LocateTile(s.ctx, destTileID)
		if err != nil {
			return nil, err
		}
		workspaceIDs = append(workspaceIDs, tileWorkspaceID)
	}
	if specUsesRefTab {
		ref, err := s.resolveTab(resolve.KindTab, spec.refID, resolve.Need{WorkspaceID: true})
		if err != nil {
			return nil, err
		}
		workspaceIDs = append(workspaceIDs, ref.WorkspaceID)
	}
	cc, err := s.openCRDT(append([]string{got.WorkspaceID}, workspaceIDs...)...)
	if err != nil {
		return nil, err
	}
	if err := preflightTab(cc.bs.State, got.WorkspaceID, got.ID, tt); err != nil {
		return nil, err
	}
	// Derive the destination tile from --workspace when the caller only
	// specified a workspace and the placement spec doesn't itself carry a
	// tile (--before / --after).
	if destTileID == "" && destWorkspaceID != "" && !specUsesRefTab {
		ws, ok := cc.bs.State.GetWorkspaces()[destWorkspaceID]
		if !ok || ws == nil {
			return nil, cli.Newf(cli.CodeNotFound, "no such workspace: %s", destWorkspaceID)
		}
		leaf := firstLiveLeaf(cc.bs.State, ws.GetRootNodeId())
		if leaf == "" {
			return nil, cli.Newf(cli.CodeConflict, "workspace %s has no live leaf tile", destWorkspaceID)
		}
		destTileID = leaf
	}
	resolvedTileID, resolvedPos, err := resolvePositionSpec(cc.bs.State, destTileID, got.ID, spec)
	if err != nil {
		return nil, err
	}
	// Destination tile MAY live in a different workspace (cross-
	// workspace tab move). We accept any live, non-tombstoned tile;
	// the CRDT layer's tabPlacementCheck verifies the chain is
	// well-formed post-batch.
	if err := preflightTile(cc.bs.State, "", resolvedTileID); err != nil {
		return nil, err
	}
	// When the caller supplied --workspace, enforce consistency: the
	// resolved tile must actually live in that workspace. Catches typos
	// and stale --before/--after references to tabs that have since moved
	// away from the intended workspace.
	if destWorkspaceID != "" {
		if ws := nodeWorkspaceFromState(cc.bs.State, resolvedTileID); ws != destWorkspaceID {
			return nil, cli.Usagef("tile %s does not belong to workspace %s", resolvedTileID, destWorkspaceID)
		}
	}
	ops := []*leapmuxv1.CrdtOp{
		opSetTabTileID(cc.bs, tt, got.ID, resolvedTileID),
		opSetTabPosition(cc.bs, tt, got.ID, resolvedPos),
	}
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return tabPlacement{ID: got.ID, Type: resolve.TabTypeWireName(tt), TileID: resolvedTileID, Position: resolvedPos}, nil
}

// tabPlacement is where a command put a tab.
type tabPlacement struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	TileID   string `json:"tile_id"`
	Position string `json:"position"`
}
