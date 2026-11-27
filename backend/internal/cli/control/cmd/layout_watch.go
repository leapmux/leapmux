package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// runLayoutWatch subscribes to the user CRDT WatchUser stream and prints
// each event as one stream event. The first event is always
// UserMaterialized (the bootstrap snapshot); subsequent events are
// canonical-HLC-tagged ops, presence updates, lifecycle events, or
// entity-visibility transitions.
//
// The command runs until the process is interrupted or the stream closes.
// The stream is scoped to the authenticated session's own user, so no user
// id is resolved or sent; the workspace argument only narrows which
// workspaces the stream carries.
func runLayoutWatch(inv *cli.Invocation) (any, error) {
	s, err := openStreamSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var workspaceIDs []string
	if inv.HasArg("workspace") {
		callSession := s.bounded()
		ws, err := callSession.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
		if err == nil {
			var resp leapmuxv1.GetWorkspaceResponse
			err = hubCallUnary(callSession.ctx, callSession.c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure,
				&leapmuxv1.GetWorkspaceRequest{WorkspaceId: ws.ID}, &resp)
		}
		callSession.Close()
		if err != nil {
			return nil, err
		}
		workspaceIDs = []string{ws.ID}
	}
	session := hubUserEvents(s.c, workspaceIDs)
	err = session(s.ctx, func(evt *leapmuxv1.WatchUserEvent) error {
		return inv.Printer.Event(layoutWatchEvent{event: evt})
	})
	if err != nil && !errors.Is(err, context.Canceled) && s.ctx.Err() == nil {
		return nil, err
	}
	return nil, nil
}

// layoutWatchEvent wraps a WatchUserEvent so --json/--format ndjson emit the
// structured payload while text and TSV modes print one readable line.
type layoutWatchEvent struct {
	event *leapmuxv1.WatchUserEvent
	data  map[string]any
}

func (e layoutWatchEvent) MarshalJSON() ([]byte, error) {
	if e.event == nil && e.data != nil {
		return json.Marshal(e.data)
	}
	return json.Marshal(eventToJSON(e.event))
}

// TextValue implements cli.TextValuer so --format tsv renders the structured
// summary line instead of "-" for nested JSON fields.
func (e layoutWatchEvent) TextValue() string {
	return e.TextLine()
}

func (e layoutWatchEvent) TextLine() string {
	if e.event == nil && e.data != nil {
		return layoutWatchTextFromMap(e.data)
	}
	switch ev := e.event.GetEvent().(type) {
	case *leapmuxv1.WatchUserEvent_Initial:
		return fmt.Sprintf("materialized workspaces=%d epoch=%d", len(ev.Initial.GetWorkspaces()), ev.Initial.GetCurrentEpoch())
	case *leapmuxv1.WatchUserEvent_Batch:
		ops := ev.Batch.GetOps()
		types := make([]string, 0, len(ops))
		for _, op := range ops {
			types = append(types, opKindName(op))
		}
		if len(types) == 0 {
			return fmt.Sprintf("batch %s (0 ops)", cli.ShortID(ev.Batch.GetBatchId()))
		}
		return fmt.Sprintf("batch %s (%d ops: %s)", cli.ShortID(ev.Batch.GetBatchId()), len(types), strings.Join(types, ", "))
	case *leapmuxv1.WatchUserEvent_EntityMaterialized:
		return formatMaterializedEntityLine(ev.EntityMaterialized)
	case *leapmuxv1.WatchUserEvent_EntityRemoved:
		return formatRemovedEntityLine(ev.EntityRemoved)
	case *leapmuxv1.WatchUserEvent_Delta:
		return fmt.Sprintf("resume_delta frames=%d epoch=%d", len(ev.Delta.GetFrames()), ev.Delta.GetCurrentEpoch())
	case *leapmuxv1.WatchUserEvent_BatchEnd:
		if h := ev.BatchEnd.GetAtHlc(); h != nil {
			return fmt.Sprintf("batch_end hlc=%d:%d", h.GetPhysical(), h.GetLogical())
		}
		return "batch_end"
	case *leapmuxv1.WatchUserEvent_Created:
		return fmt.Sprintf("workspace_created %s %q", cli.ShortID(ev.Created.GetWorkspaceId()), ev.Created.GetTitle())
	case *leapmuxv1.WatchUserEvent_Renamed:
		return fmt.Sprintf("workspace_renamed %s %q", cli.ShortID(ev.Renamed.GetWorkspaceId()), ev.Renamed.GetTitle())
	case *leapmuxv1.WatchUserEvent_Deleted:
		return fmt.Sprintf("workspace_deleted %s", cli.ShortID(ev.Deleted.GetWorkspaceId()))
	case *leapmuxv1.WatchUserEvent_Presence:
		return fmt.Sprintf("presence workspace=%s client=%s", cli.ShortID(ev.Presence.GetWorkspaceId()), cli.ShortID(ev.Presence.GetActiveClientId()))
	case *leapmuxv1.WatchUserEvent_WorkersChanged:
		return "workers_changed"
	default:
		return "unknown"
	}
}

func formatMaterializedEntityLine(em *leapmuxv1.EntityMaterialized) string {
	switch ent := em.GetEntity().(type) {
	case *leapmuxv1.EntityMaterialized_Tab:
		return formatEntityLine("entity_materialized", "tab", ent.Tab.GetTabId())
	case *leapmuxv1.EntityMaterialized_FloatingWindow:
		return formatEntityLine("entity_materialized", "floating_window", ent.FloatingWindow.GetWindowId())
	case *leapmuxv1.EntityMaterialized_Node:
		return formatEntityLine("entity_materialized", "node", ent.Node.GetNodeId())
	default:
		return "entity_materialized unknown"
	}
}

func formatRemovedEntityLine(er *leapmuxv1.EntityRemoved) string {
	switch ent := er.GetEntity().(type) {
	case *leapmuxv1.EntityRemoved_Tab:
		return formatEntityLine("entity_removed", "tab", ent.Tab.GetTabId())
	case *leapmuxv1.EntityRemoved_WindowId:
		return formatEntityLine("entity_removed", "floating_window", ent.WindowId)
	case *leapmuxv1.EntityRemoved_NodeId:
		return formatEntityLine("entity_removed", "node", ent.NodeId)
	default:
		return "entity_removed unknown"
	}
}

func formatEntityLine(kind, entityType, id string) string {
	if id == "" {
		return fmt.Sprintf("%s %s", kind, entityType)
	}
	return fmt.Sprintf("%s %s %s", kind, entityType, cli.ShortID(id))
}

func mapStringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func layoutWatchTextFromMap(data map[string]any) string {
	kind := mapStringField(data, "kind")
	switch kind {
	case "materialized":
		workspaces, _ := data["workspaces"].([]string)
		epoch, _ := data["current_epoch"].(uint64)
		return fmt.Sprintf("materialized workspaces=%d epoch=%d", len(workspaces), epoch)
	case "batch":
		ops, _ := data["ops"].([]map[string]any)
		var types []string
		for _, op := range ops {
			if target, ok := op["target"].(map[string]any); ok {
				if t := mapStringField(target, "type"); t != "" {
					types = append(types, t)
				}
			}
		}
		batchID := cli.ShortID(mapStringField(data, "batch_id"))
		if len(types) == 0 {
			return fmt.Sprintf("batch %s (0 ops)", batchID)
		}
		return fmt.Sprintf("batch %s (%d ops: %s)", batchID, len(ops), strings.Join(types, ", "))
	case "entity_materialized", "entity_removed":
		ent, _ := data["entity"].(map[string]any)
		entType := mapStringField(ent, "type")
		for _, key := range []string{"tab_id", "window_id", "node_id"} {
			if id := mapStringField(ent, key); id != "" {
				return fmt.Sprintf("%s %s %s", kind, entType, cli.ShortID(id))
			}
		}
		return fmt.Sprintf("%s %s", kind, entType)
	case "resume_delta":
		frames, _ := data["frames"].([]map[string]any)
		epoch, _ := data["current_epoch"].(uint64)
		return fmt.Sprintf("resume_delta frames=%d epoch=%d", len(frames), epoch)
	case "batch_end":
		if hlc, ok := data["at_hlc"].(map[string]any); ok && hlc != nil {
			phys, _ := hlc["physical"].(int64)
			logi, _ := hlc["logical"].(uint32)
			return fmt.Sprintf("batch_end hlc=%d:%d", phys, logi)
		}
		return "batch_end"
	case "workspace_created", "workspace_renamed":
		return fmt.Sprintf("%s %s %q", kind, cli.ShortID(mapStringField(data, "workspace_id")), mapStringField(data, "title"))
	case "workspace_deleted":
		return fmt.Sprintf("%s %s", kind, cli.ShortID(mapStringField(data, "workspace_id")))
	case "presence":
		return fmt.Sprintf("presence workspace=%s client=%s", cli.ShortID(mapStringField(data, "workspace_id")), cli.ShortID(mapStringField(data, "active_client_id")))
	case "":
		return "unknown"
	default:
		return kind
	}
}

// eventToJSON projects a WatchUserEvent into a JSON-friendly map. The
// proto's oneof + nested registers don't json-marshal nicely as-is —
// the protojson-equivalent shape would lose readability — so we emit a
// hand-shaped envelope: `kind`, plus event-specific fields.
func eventToJSON(evt *leapmuxv1.WatchUserEvent) map[string]any {
	switch e := evt.GetEvent().(type) {
	case *leapmuxv1.WatchUserEvent_Initial:
		return map[string]any{
			"kind":          "materialized",
			"user_id":       e.Initial.GetUserId(),
			"current_epoch": e.Initial.GetCurrentEpoch(),
			"max_hlc":       hlcToJSON(e.Initial.GetMaxHlc()),
			"workspaces":    workspaceMapKeys(e.Initial.GetWorkspaces()),
		}
	case *leapmuxv1.WatchUserEvent_Batch:
		return map[string]any{
			"kind":     "batch",
			"batch_id": e.Batch.GetBatchId(),
			"ops":      opsToJSON(e.Batch.GetOps()),
		}
	case *leapmuxv1.WatchUserEvent_EntityMaterialized:
		return map[string]any{
			"kind":   "entity_materialized",
			"at_hlc": hlcToJSON(e.EntityMaterialized.GetAtHlc()),
			"entity": entityKindSummary(e.EntityMaterialized),
		}
	case *leapmuxv1.WatchUserEvent_EntityRemoved:
		return map[string]any{
			"kind":   "entity_removed",
			"at_hlc": hlcToJSON(e.EntityRemoved.GetAtHlc()),
			"entity": removedEntitySummary(e.EntityRemoved),
		}
	case *leapmuxv1.WatchUserEvent_Delta:
		// A ResumeDelta carries an ordered list of the same WatchUserEvent
		// frames that live Send uses. `layout watch` connects with a nil cursor
		// (always a full snapshot), but handles this branch so a future
		// resume-aware watch surfaces the frames instead of printing "unknown".
		frames := make([]map[string]any, 0, len(e.Delta.GetFrames()))
		for _, frame := range e.Delta.GetFrames() {
			frames = append(frames, eventToJSON(frame))
		}
		return map[string]any{
			"kind":          "resume_delta",
			"current_epoch": e.Delta.GetCurrentEpoch(),
			"max_hlc":       hlcToJSON(e.Delta.GetMaxHlc()),
			"frames":        frames,
		}
	case *leapmuxv1.WatchUserEvent_BatchEnd:
		// Emitted after EVERY committed batch on the live path, not just on
		// resume, so leaving this unhandled put one {"kind":"unknown"} line in
		// the stream per user edit. at_hlc is the batch boundary a resume-aware
		// consumer advances its cursor on.
		return map[string]any{
			"kind":   "batch_end",
			"at_hlc": hlcToJSON(e.BatchEnd.GetAtHlc()),
		}
	case *leapmuxv1.WatchUserEvent_Presence:
		return map[string]any{
			"kind":             "presence",
			"workspace_id":     e.Presence.GetWorkspaceId(),
			"active_client_id": e.Presence.GetActiveClientId(),
		}
	case *leapmuxv1.WatchUserEvent_Renamed:
		return map[string]any{
			"kind":         "workspace_renamed",
			"workspace_id": e.Renamed.GetWorkspaceId(),
			"title":        e.Renamed.GetTitle(),
		}
	case *leapmuxv1.WatchUserEvent_Created:
		return map[string]any{
			"kind":         "workspace_created",
			"workspace_id": e.Created.GetWorkspaceId(),
			"title":        e.Created.GetTitle(),
			"root_node_id": e.Created.GetRootNodeId(),
		}
	case *leapmuxv1.WatchUserEvent_Deleted:
		return map[string]any{
			"kind":         "workspace_deleted",
			"workspace_id": e.Deleted.GetWorkspaceId(),
		}
	case *leapmuxv1.WatchUserEvent_WorkersChanged:
		return map[string]any{"kind": "workers_changed"}
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

// workspaceMapKeys lists the workspace ids of m. Go randomizes the iteration
// order of a map, so the keys are sorted: the JSONL line goes to a script that
// compares two snapshots, and an unsorted list makes every account with two or
// more workspaces report a difference that does not exist.
func workspaceMapKeys(m map[string]*leapmuxv1.WorkspaceContentsRecord) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func opsToJSON(ops []*leapmuxv1.CrdtOp) []map[string]any {
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

func opTargetSummary(op *leapmuxv1.CrdtOp) map[string]any {
	target := crdt.OpTarget(op)
	out := target.ToJSON()
	if target.Kind == crdt.EntityKindTab {
		// The token removedEntitySummary and entityKindSummary print, so one
		// tab reads the same in every event of the stream.
		out["tab_type"] = tabTypeName(target.TabType)
	}
	out["type"] = opKindName(op)
	return out
}

// opKindName returns the snake-case proto-body discriminator emitted
// alongside the entity identifiers in `layout watch` output.
// Kept here (not on EntityRef) because the op shape, not the entity,
// determines the label: a SetNodeRegister and a TombstoneNode target
// the same EntityRef.
func opKindName(op *leapmuxv1.CrdtOp) string {
	switch op.GetBody().(type) {
	case *leapmuxv1.CrdtOp_SetNodeRegister:
		return "set_node_register"
	case *leapmuxv1.CrdtOp_TombstoneNode:
		return "tombstone_node"
	case *leapmuxv1.CrdtOp_SetTabRegister:
		return "set_tab_register"
	case *leapmuxv1.CrdtOp_TombstoneTab:
		return "tombstone_tab"
	case *leapmuxv1.CrdtOp_ReviveTab:
		return "revive_tab"
	case *leapmuxv1.CrdtOp_SetFloatingWindowRegister:
		return "set_floating_window_register"
	case *leapmuxv1.CrdtOp_TombstoneFloatingWindow:
		return "tombstone_floating_window"
	case *leapmuxv1.CrdtOp_CreateWorkspace:
		return "create_workspace"
	case *leapmuxv1.CrdtOp_TombstoneWorkspace:
		return "tombstone_workspace"
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
