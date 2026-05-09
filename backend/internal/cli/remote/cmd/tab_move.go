package cmd

import (
	"context"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
)

// RunTabMove writes a new tile_id and position on a tab. The
// destination tile may live in a different workspace — cross-workspace
// moves are one CRDT op at this layer. The resolver derives the source
// workspace + tab type from --tab-id.
//
// Destination tile resolution:
//
//  1. --target-tile-id, when set.
//  2. --target-workspace-id alone → first live leaf (DFS, by position)
//     of that workspace's tile tree.
//  3. --before <tab-id> / --after <tab-id> → ref tab's tile (and the
//     workspace that owns it).
//
// Placement within the destination tile is determined by the
// --first / --last / --before / --after flags (mutually exclusive;
// --last is the default). --before / --after take a tab id, not a
// LexoRank — the rank is computed against the sibling tabs already on
// the destination tile.
//
// The caller is responsible for any worker-side bookkeeping
// (`MoveTabWorkspace` for agents/terminals, `RelocateFileTabPath` for
// FILE tabs); this command only emits the CRDT op.
func RunTabMove(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, destTileID, destWorkspaceID string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	// HideTile so the resolver doesn't auto-consume --tile-id as a
	// derivation source for the source workspace; the move target is
	// a separate input owned by --target-tile-id / --target-workspace-id.
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true, HideTile: true})
	fs.StringVar(&destTileID, "target-tile-id", "", "destination tile id (defaults derived from --target-workspace-id or --before/--after)")
	fs.StringVar(&destWorkspaceID, "target-workspace-id", "", "destination workspace id; when set without --target-tile-id, the tab lands on the workspace's first live leaf")
	pos := bindPositionFlags(fs, "land")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	spec, err := pos.Resolve()
	if err != nil {
		return remote.EmitError("invalid_request", err.Error())
	}
	specReferencesTile := spec.kind == positionBefore || spec.kind == positionAfter
	if destTileID == "" && destWorkspaceID == "" && !specReferencesTile {
		return remote.EmitError("invalid_request", "--target-tile-id, --target-workspace-id, or --before/--after is required")
	}
	c, err := requireClient(hub)
	if err != nil {
		return err
	}
	ctx, cancel := rpcDeadline(context.Background())
	defer cancel()
	got, err := runResolve(ctx, c, resolve.Need{TabID: true, WorkspaceID: true}, in)
	if err != nil {
		return err
	}
	tt := got.TabType
	if tt == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		return remote.EmitError("invalid_request", "could not determine tab type for "+got.TabID+"; pass --tab-type explicitly")
	}
	cc, err := openCRDTCall(hub, got.WorkspaceID)
	if err != nil {
		return err
	}
	defer cc.close()
	if err := preflightTab(cc.bs.State, got.WorkspaceID, got.TabID, tt); err != nil {
		return err
	}
	// Derive --target-tile-id from --target-workspace-id when the
	// caller only named a workspace and the placement spec doesn't
	// itself carry a tile (--before / --after).
	if destTileID == "" && destWorkspaceID != "" && !specReferencesTile {
		ws, ok := cc.bs.State.GetWorkspaces()[destWorkspaceID]
		if !ok || ws == nil {
			return remote.EmitError("not_found", "no such workspace: "+destWorkspaceID)
		}
		leaf := firstLiveLeaf(cc.bs.State, ws.GetRootNodeId())
		if leaf == "" {
			return remote.EmitError("not_found", "workspace "+destWorkspaceID+" has no live leaf tile")
		}
		destTileID = leaf
	}
	resolvedTileID, resolvedPos, err := resolvePositionSpec(cc.bs.State, destTileID, got.TabID, spec)
	if err != nil {
		return err
	}
	// Destination tile MAY live in a different workspace (cross-
	// workspace tab move). We accept any live, non-tombstoned tile;
	// the CRDT layer's tabPlacementCheck verifies the chain is
	// well-formed post-batch.
	if err := preflightTile(cc.bs.State, "", resolvedTileID); err != nil {
		return err
	}
	// When the caller supplied --target-workspace-id, enforce
	// consistency: the resolved tile must actually live in that
	// workspace. Catches typos and stale --before/--after references
	// to tabs that have since moved away from the intended workspace.
	if destWorkspaceID != "" {
		if ws := nodeWorkspaceFromState(cc.bs.State, resolvedTileID); ws != destWorkspaceID {
			return remote.EmitError(
				"invalid_request",
				fmt.Sprintf("tile %s does not belong to workspace %s", resolvedTileID, destWorkspaceID),
			)
		}
	}
	ops := []*leapmuxv1.OrgOp{
		opSetTabTileID(cc.bs, tt, got.TabID, resolvedTileID),
		opSetTabPosition(cc.bs, tt, got.TabID, resolvedPos),
	}
	if err := cc.submitOps(ops); err != nil {
		return err
	}
	return remote.EmitData(map[string]any{
		"tab_id":   got.TabID,
		"tab_type": tabTypeName(got.TabType),
		"tile_id":  resolvedTileID,
		"position": resolvedPos,
	})
}
