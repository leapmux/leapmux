package cmd

import (
	"context"
	"os"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// Self-target guards block destructive `leapmux remote` commands from
// silently tearing down the very tab the user is running them in. The
// only spawn-time anchor the worker injects is LEAPMUX_REMOTE_TAB_ID
// (workspace / tile env vars are intentionally omitted — they go stale
// on cross-workspace move and tile drag; see
// internal/worker/remoteipc/router_test.go's `forbidden` list). The
// guards therefore key off the tab anchor and look up its current
// workspace / tile at call time.
//
// Each guard pairs with a per-command `--force` flag that bypasses the
// check, mirroring the existing `layout set --force` ergonomics.

// callingTabID returns the LEAPMUX_REMOTE_TAB_ID spawn anchor, or ""
// when the CLI was not invoked from inside a remote tab. Trimming
// guards against trailing newlines from shell substitutions.
func callingTabID() string {
	return strings.TrimSpace(os.Getenv("LEAPMUX_REMOTE_TAB_ID"))
}

// locateCallingTab resolves the calling tab anchor to its current
// (workspace_id, tile_id) via LocateTab. An empty anchor or a
// LocateTab failure (e.g. the anchor is stale because the tab was
// closed since spawn) returns ("", "") with no error — guards must
// not block real operations on un-resolvable anchors, only on ones
// that currently coincide with the destructive target.
func locateCallingTab(ctx context.Context, c *remote.Client) (workspaceID, tileID string) {
	self := callingTabID()
	if self == "" {
		return "", ""
	}
	req := &leapmuxv1.LocateTabRequest{
		TabType: leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED,
		TabId:   self,
	}
	var resp leapmuxv1.LocateTabResponse
	if err := hubCallUnary(ctx, c, "LocateTab", "", req, &resp); err != nil {
		return "", ""
	}
	t := resp.GetTab()
	return t.GetWorkspaceId(), t.GetTileId()
}

// callingTileFromState reads the calling tab's tile id directly from
// a CRDT state snapshot the handler has already loaded for an
// unrelated reason (e.g. preflightTile). Returns "" when no anchor
// is set, the tab is absent from the snapshot, or the tab record is
// tombstoned. Same fail-open semantics as locateCallingTab.
func callingTileFromState(state *leapmuxv1.OrgMaterialized) string {
	self := callingTabID()
	if self == "" || state == nil {
		return ""
	}
	rec := state.GetTabs()[self]
	if rec == nil || !crdt.HLCIsZero(rec.GetTombstoneAt()) {
		return ""
	}
	return rec.GetTileId().GetValue()
}

// errSelfTargetRefused builds the canonical error envelope returned
// when a guard fires. `self_target_refused` is a distinct code so
// scripts can pattern-match and decide whether to re-run with
// --force, instead of having to grep an `invalid_request` blob.
func errSelfTargetRefused(detail string) error {
	return remote.EmitError(
		"self_target_refused",
		detail+"; pass --force to override",
	)
}

// guardWorkspaceDelete blocks `workspace delete` when the calling
// tab lives inside the target workspace and --force was not passed.
// Returns nil to proceed, or an emitted error envelope to abort.
func guardWorkspaceDelete(ctx context.Context, c *remote.Client, targetWorkspaceID string, force bool) error {
	if force || targetWorkspaceID == "" {
		return nil
	}
	selfWS, _ := locateCallingTab(ctx, c)
	return rejectIfSelfWorkspace(selfWS, targetWorkspaceID)
}

// rejectIfSelfWorkspace is the pure-decision core of
// guardWorkspaceDelete: given the already-resolved self workspace id
// and the target, return the error envelope when they coincide
// (nil otherwise). Split out so the comparison + envelope is unit-
// testable without standing up a hub client.
func rejectIfSelfWorkspace(selfWS, targetWS string) error {
	if selfWS == "" || targetWS == "" || selfWS != targetWS {
		return nil
	}
	return errSelfTargetRefused(
		"refusing to delete workspace " + targetWS +
			" because the calling tab lives in it",
	)
}

// guardTabClose blocks `tab close` when the target tab is the calling
// tab itself and --force was not passed. Pure env-var comparison —
// no RPC.
func guardTabClose(targetTabID string, force bool) error {
	if force || targetTabID == "" {
		return nil
	}
	if callingTabID() != targetTabID {
		return nil
	}
	return errSelfTargetRefused(
		"refusing to close the calling tab " + targetTabID,
	)
}

// guardTileClose blocks `tile close` when the calling tab sits on
// the target tile and --force was not passed. Uses CRDT state the
// handler has already loaded.
func guardTileClose(state *leapmuxv1.OrgMaterialized, targetTileID string, force bool) error {
	if force || targetTileID == "" {
		return nil
	}
	selfTile := callingTileFromState(state)
	if selfTile == "" || selfTile != targetTileID {
		return nil
	}
	return errSelfTargetRefused(
		"refusing to close tile " + targetTileID +
			" because the calling tab sits on it",
	)
}

// guardTileRemoveGrid blocks `tile remove-grid` when the calling
// tab's tile is the grid itself or any of its descendants. The
// caller passes the already-computed descendants list (which
// `descendantsLeavesFirst` includes the grid node in) so the guard
// doesn't redo the walk.
func guardTileRemoveGrid(state *leapmuxv1.OrgMaterialized, doomedTileIDs []string, force bool) error {
	if force || len(doomedTileIDs) == 0 {
		return nil
	}
	selfTile := callingTileFromState(state)
	if selfTile == "" {
		return nil
	}
	for _, d := range doomedTileIDs {
		if d == selfTile {
			return errSelfTargetRefused(
				"refusing to remove grid because the calling tab sits inside it (tile " + selfTile + ")",
			)
		}
	}
	return nil
}
