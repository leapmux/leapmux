package cmd

import (
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// RunTileList prints the projected tree for a workspace. Reads
// ListTabs to enumerate live tabs and the bootstrapped OrgMaterialized
// to walk node parent chains.
func RunTileList(rawCtx any, args []string) error {
	return runLayoutCommon(rawCtx, args, false)
}

// RunLayoutGet emits the same data shape as RunTileList — the two
// commands share the same projected tree plus a list of live tabs
// indexed by their tile id, so script consumers can pick whichever
// command name matches their mental model.
func RunLayoutGet(rawCtx any, args []string) error {
	return runLayoutCommon(rawCtx, args, true)
}

// runLayoutCommon collects the workspace tree + (optionally)
// tabs-by-tile and emits the envelope. The two public verbs differ
// only in whether they include tabs_by_tile in the output.
func runLayoutCommon(rawCtx any, args []string, includeTabs bool) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	got, err := resolveWorkspaceForLayout(hub, resolve.Need{}, in)
	if err != nil {
		return err
	}
	cc, err := openCRDTCall(hub, got.WorkspaceID)
	if err != nil {
		return err
	}
	defer cc.close()
	rootNodeID := ""
	if rec := cc.bs.State.GetWorkspaces()[got.WorkspaceID]; rec != nil {
		rootNodeID = rec.GetRootNodeId()
	}
	out := map[string]any{
		"workspace_id": got.WorkspaceID,
		"root_node_id": rootNodeID,
		"tree":         buildTreeJSON(cc.bs.State, rootNodeID),
	}
	if includeTabs {
		tabsByTile := map[string][]map[string]any{}
		for _, t := range cc.bs.State.GetTabs() {
			if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
				continue
			}
			tile := t.GetTileId().GetValue()
			if tile == "" {
				continue
			}
			tabsByTile[tile] = append(tabsByTile[tile], map[string]any{
				"tab_id":   t.GetTabId(),
				"tab_type": tabTypeName(t.GetTabType()),
			})
		}
		out["tabs_by_tile"] = tabsByTile
	}
	return remote.EmitData(out)
}
