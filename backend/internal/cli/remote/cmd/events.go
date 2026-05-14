package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/signal"
	"syscall"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// RunEvents subscribes to OrgCRDT.WatchOrg and emits each event as a
// single JSONL line on stdout. The first event is always
// OrgMaterialized (the bootstrap snapshot); subsequent events are
// canonical-HLC-tagged ops, presence updates, lifecycle events, or
// entity-visibility transitions.
//
// The command runs until SIGINT/SIGTERM or the stream closes. The
// universal resolver fills org_id from any of --tab-id,
// --workspace-id, --worker-id, --user-id, --tile-id, or directly
// --org-id; conflicts surface as invalid_request.
//
// It is the CLI surface that replaces the legacy
// `WatchWorkspaceEvents` command from before the CRDT migration.
func RunEvents(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideUser: true})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	c, err := requireClient(hub)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	got, err := runResolve(ctx, c, resolve.Need{OrgID: true}, in)
	if err != nil {
		return err
	}
	workspaceIDs := []string{}
	if got.WorkspaceID != "" {
		workspaceIDs = []string{got.WorkspaceID}
	}
	if c.IsLocal() {
		return runEventsLocal(ctx, c, got.OrgID, workspaceIDs)
	}
	return runEventsHub(ctx, c, got.OrgID, workspaceIDs)
}

func runEventsHub(ctx context.Context, c *remote.Client, orgID string, workspaceIDs []string) error {
	stream, err := c.OpenOrgEvents(ctx, orgID, workspaceIDs)
	if err != nil {
		return remote.EmitErrorWith("rpc_failed", err)
	}
	defer func() { _ = stream.Close() }()
	enc := json.NewEncoder(remote.Out)
	for {
		evt, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return remote.EmitErrorWith("stream_error", err)
		}
		_ = enc.Encode(eventToJSON(evt))
	}
}

func runEventsLocal(ctx context.Context, c *remote.Client, orgID string, workspaceIDs []string) error {
	enc := json.NewEncoder(remote.Out)
	err := streamWatchOrgLocal(ctx, c, orgID, workspaceIDs, func(evt *leapmuxv1.WatchOrgEvent) (bool, error) {
		_ = enc.Encode(eventToJSON(evt))
		return false, nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return remote.EmitErrorWith("stream_error", err)
	}
	return nil
}

// eventToJSON projects a WatchOrgEvent into a JSON-friendly map. The
// proto's oneof + nested registers don't json-marshal nicely as-is —
// the protojson-equivalent shape would lose readability — so we emit a
// hand-shaped envelope: `kind`, plus event-specific fields.
func eventToJSON(evt *leapmuxv1.WatchOrgEvent) map[string]any {
	switch e := evt.GetEvent().(type) {
	case *leapmuxv1.WatchOrgEvent_Initial:
		return map[string]any{
			"kind":          "materialized",
			"org_id":        e.Initial.GetOrgId(),
			"current_epoch": e.Initial.GetCurrentEpoch(),
			"max_hlc":       hlcToJSON(e.Initial.GetMaxHlc()),
			"workspaces":    workspaceMapKeys(e.Initial.GetWorkspaces()),
		}
	case *leapmuxv1.WatchOrgEvent_Batch:
		return map[string]any{
			"kind":     "batch",
			"batch_id": e.Batch.GetBatchId(),
			"ops":      opsToJSON(e.Batch.GetOps()),
		}
	case *leapmuxv1.WatchOrgEvent_EntityMaterialized:
		return map[string]any{
			"kind":   "entity_materialized",
			"at_hlc": hlcToJSON(e.EntityMaterialized.GetAtHlc()),
			"entity": entityKindSummary(e.EntityMaterialized),
		}
	case *leapmuxv1.WatchOrgEvent_EntityRemoved:
		return map[string]any{
			"kind":   "entity_removed",
			"at_hlc": hlcToJSON(e.EntityRemoved.GetAtHlc()),
			"entity": removedEntitySummary(e.EntityRemoved),
		}
	case *leapmuxv1.WatchOrgEvent_Presence:
		return map[string]any{
			"kind":             "presence",
			"workspace_id":     e.Presence.GetWorkspaceId(),
			"active_client_id": e.Presence.GetActiveClientId(),
		}
	case *leapmuxv1.WatchOrgEvent_Renamed:
		return map[string]any{
			"kind":         "workspace_renamed",
			"workspace_id": e.Renamed.GetWorkspaceId(),
			"title":        e.Renamed.GetTitle(),
		}
	case *leapmuxv1.WatchOrgEvent_Created:
		return map[string]any{
			"kind":         "workspace_created",
			"workspace_id": e.Created.GetWorkspaceId(),
			"title":        e.Created.GetTitle(),
			"root_node_id": e.Created.GetRootNodeId(),
		}
	case *leapmuxv1.WatchOrgEvent_Deleted:
		return map[string]any{
			"kind":         "workspace_deleted",
			"workspace_id": e.Deleted.GetWorkspaceId(),
		}
	}
	return map[string]any{"kind": "unknown"}
}

func hlcToJSON(h *leapmuxv1.HLC) map[string]any {
	if h == nil {
		return nil
	}
	return map[string]any{
		"physical":  h.GetPhysical(),
		"logical":   h.GetLogical(),
		"client_id": h.GetClientId(),
	}
}

func workspaceMapKeys(m map[string]*leapmuxv1.WorkspaceContentsRecord) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func opsToJSON(ops []*leapmuxv1.OrgOp) []map[string]any {
	out := make([]map[string]any, len(ops))
	for i, op := range ops {
		out[i] = map[string]any{
			"op_id":         op.GetOpId(),
			"canonical_hlc": hlcToJSON(op.GetCanonicalHlc()),
			"target":        opTargetSummary(op),
		}
	}
	return out
}

func opTargetSummary(op *leapmuxv1.OrgOp) map[string]any {
	out := crdt.OpTarget(op).ToJSON()
	out["type"] = opKindName(op)
	return out
}

// opKindName returns the snake-case proto-body discriminator emitted
// alongside the entity identifiers in `events watch` JSON output.
// Kept here (not on EntityRef) because the op shape, not the entity,
// determines the label: a SetNodeRegister and a TombstoneNode target
// the same EntityRef.
func opKindName(op *leapmuxv1.OrgOp) string {
	switch op.GetBody().(type) {
	case *leapmuxv1.OrgOp_SetNodeRegister:
		return "set_node_register"
	case *leapmuxv1.OrgOp_TombstoneNode:
		return "tombstone_node"
	case *leapmuxv1.OrgOp_SetTabRegister:
		return "set_tab_register"
	case *leapmuxv1.OrgOp_TombstoneTab:
		return "tombstone_tab"
	case *leapmuxv1.OrgOp_SetFloatingWindowRegister:
		return "set_floating_window_register"
	case *leapmuxv1.OrgOp_TombstoneFloatingWindow:
		return "tombstone_floating_window"
	case *leapmuxv1.OrgOp_SetWorkspaceRootNode:
		return "set_workspace_root_node"
	}
	return "unknown"
}

func entityKindSummary(em *leapmuxv1.EntityMaterialized) map[string]any {
	switch e := em.GetEntity().(type) {
	case *leapmuxv1.EntityMaterialized_Tab:
		return map[string]any{
			"type":     "tab",
			"tab_id":   e.Tab.GetTabId(),
			"tab_type": tabTypeName(e.Tab.GetTabType()),
		}
	case *leapmuxv1.EntityMaterialized_FloatingWindow:
		return map[string]any{"type": "floating_window", "window_id": e.FloatingWindow.GetWindowId()}
	case *leapmuxv1.EntityMaterialized_Node:
		return map[string]any{"type": "node", "node_id": e.Node.GetNodeId()}
	}
	return map[string]any{"type": "unknown"}
}

func removedEntitySummary(em *leapmuxv1.EntityRemoved) map[string]any {
	switch e := em.GetEntity().(type) {
	case *leapmuxv1.EntityRemoved_Tab:
		return map[string]any{
			"type":     "tab",
			"tab_id":   e.Tab.GetTabId(),
			"tab_type": tabTypeName(e.Tab.GetTabType()),
		}
	case *leapmuxv1.EntityRemoved_WindowId:
		return map[string]any{"type": "floating_window", "window_id": e.WindowId}
	case *leapmuxv1.EntityRemoved_NodeId:
		return map[string]any{"type": "node", "node_id": e.NodeId}
	}
	return map[string]any{"type": "unknown"}
}
