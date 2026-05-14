package cmd

import (
	"context"
	"errors"
	"os"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
)

// RunTabOpen creates a new tab on the given tile. Dispatches on --type:
//
//   - agent     → calls worker OpenAgent (creates a real agent record),
//     then writes the CRDT batch (tile/position/worker_id).
//   - terminal  → calls worker OpenTerminal (creates a real terminal
//     record), then writes the CRDT batch.
//   - file      → registers the path worker-side over E2EE via
//     `RegisterFileTabPath`, then writes the CRDT batch.
//
// Required-flag defaults are read from LEAPMUX_REMOTE_* env vars when
// the user runs from inside a remote-enabled agent/terminal, so the
// common "spawn another tab in the same tile" case needs zero flags.
//
// The hub never sees file paths — only the opaque `tab_id` and the
// presentation registers.
func RunTabOpen(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, tabType, filePath string
	var displayMode, fileViewMode int
	// Agent-only flags.
	var provider, model, effort, title, permissionMode, workingDir, initialMessage string
	// Terminal-only flags.
	var shell, shellStartDir string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	fs.StringVar(&tabType, "type", "", "type of the new tab to open: agent | terminal | file (required; distinct from --tab-type, which classifies the anchor --tab-id)")
	pos := bindPositionFlags(fs, "place the new tab")
	fs.StringVar(&filePath, "path", "", "absolute file path (--type=file only; registered worker-side over E2EE)")
	fs.IntVar(&displayMode, "display-mode", 0, "--type=file: initial display_mode register value")
	fs.IntVar(&fileViewMode, "file-view-mode", 0, "--type=file: initial file_view_mode register value")
	// Agent-only flags.
	fs.StringVar(&provider, "provider", os.Getenv("LEAPMUX_REMOTE_AGENT_PROVIDER"), "--type=agent: provider (defaults to $LEAPMUX_REMOTE_AGENT_PROVIDER; when unset, auto-picks the sole installed provider on the worker, or errors with the list if more than one is installed -- run 'leapmux remote agent providers' to see options)")
	fs.StringVar(&model, "model", "", "--type=agent: model")
	fs.StringVar(&effort, "effort", "", "--type=agent: effort (low/medium/high/max)")
	fs.StringVar(&title, "title", "", "--type=agent: tab title")
	fs.StringVar(&permissionMode, "permission-mode", "", "--type=agent: permission mode")
	fs.StringVar(&workingDir, "working-dir", workingDirEnv(), "--type=agent|terminal: working directory (defaults to $LEAPMUX_REMOTE_WORKING_DIR)")
	fs.StringVar(&initialMessage, "initial-message", "", "--type=agent: user message sent right after spawn")
	// Terminal-only flags.
	fs.StringVar(&shell, "shell", "", "--type=terminal: shell path (empty -> worker default)")
	fs.StringVar(&shellStartDir, "shell-start-dir", "", "--type=terminal: shell start directory (empty -> working-dir)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	tt, ok := resolve.ParseTabType(tabType)
	if !ok {
		return remote.EmitError("invalid_request", "--type must be agent|terminal|file")
	}
	if tt == leapmuxv1.TabType_TAB_TYPE_FILE && filePath == "" {
		return remote.EmitError("invalid_request", "--path is required for --type=file")
	}
	if tt != leapmuxv1.TabType_TAB_TYPE_FILE && filePath != "" {
		return remote.EmitError("invalid_request", "--path is only valid for --type=file")
	}
	spec, err := pos.Resolve()
	if err != nil {
		return remote.EmitError("invalid_request", err.Error())
	}
	c, err := requireClient(hub)
	if err != nil {
		return err
	}
	ctx, cancel := rpcDeadline(context.Background())
	defer cancel()

	// The resolver pulls workspace_id / tile_id / worker_id from any
	// sufficient subset of {--tab-id (parent), --workspace-id, --tile-id,
	// --worker-id} plus the LEAPMUX_REMOTE_TAB_ID env-var spawn anchor.
	// LocateTile fills (workspace_id, org_id) when only --tile-id is
	// given; LocateTab fills the spawning tab's full context (workspace,
	// tile, worker) when only --tab-id is given.
	got, err := runResolve(ctx, c, resolve.Need{WorkspaceID: true}, in)
	if err != nil {
		return err
	}
	workspaceID := got.WorkspaceID
	tileID := got.TileID
	workerID := got.WorkerID

	switch tt {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		if workerID == "" {
			return remote.EmitError("invalid_request", "--worker-id is required for --type=agent (or set LEAPMUX_REMOTE_WORKER_ID)")
		}
		if err := maybePreflightWorker(ctx, c, workerID); err != nil {
			return err
		}
		resolvedProvider, err := resolveProvider(ctx, c, workerID, provider, callInnerRPCBest)
		if err != nil {
			return err
		}
		result, err := openAgentAndAddTab(ctx, c, openAgentArgs{
			WorkspaceID:    workspaceID,
			WorkerID:       workerID,
			TileID:         tileID,
			Provider:       resolvedProvider,
			Model:          model,
			Effort:         effort,
			Title:          title,
			PermissionMode: permissionMode,
			WorkingDir:     workingDir,
			InitialMessage: initialMessage,
			Position:       spec,
		})
		if err != nil {
			return err
		}
		out := tabOpenEnvelope(result.Agent.GetId(), tabType, workspaceID, workerID, result.TileID, result.Position)
		if result.PermissionWarn != "" {
			out["permission_mode_warning"] = result.PermissionWarn
		}
		if result.InitialMsgWarn != "" {
			out["initial_message_warning"] = result.InitialMsgWarn
		}
		return remote.EmitData(out)

	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		if workerID == "" {
			return remote.EmitError("invalid_request", "--worker-id is required for --type=terminal (or set LEAPMUX_REMOTE_WORKER_ID)")
		}
		if err := maybePreflightWorker(ctx, c, workerID); err != nil {
			return err
		}
		result, err := openTerminalAndAddTab(ctx, c, openTerminalArgs{
			WorkspaceID:   workspaceID,
			WorkerID:      workerID,
			TileID:        tileID,
			WorkingDir:    workingDir,
			Shell:         shell,
			ShellStartDir: shellStartDir,
			Position:      spec,
		})
		if err != nil {
			return err
		}
		return remote.EmitData(tabOpenEnvelope(result.TerminalID, tabType, workspaceID, workerID, result.TileID, result.Position))

	case leapmuxv1.TabType_TAB_TYPE_FILE:
		// FILE flow: tab is created entirely inside the CLI handler
		// (no worker spawn step that would pin a tile for us). For
		// --first / --last, --tile-id is required; --before / --after
		// carry their own tile via the ref tab.
		if tileID == "" && (spec.kind == positionFirst || spec.kind == positionLast) {
			return remote.EmitError("invalid_request", "--tile-id is required for --type=file with --first/--last")
		}
		cc, err := openCRDTCall(hub, workspaceID)
		if err != nil {
			return err
		}
		defer cc.close()
		resolvedTileID, resolvedPos, err := resolvePositionSpec(cc.bs.State, tileID, "", spec)
		if err != nil {
			return err
		}
		if err := preflightTile(cc.bs.State, workspaceID, resolvedTileID); err != nil {
			return err
		}
		if workerID != "" {
			if err := preflightWorker(cc.ctx, cc.c, workerID); err != nil {
				return err
			}
		}
		tabID := id.Generate()
		if workerID == "" {
			workerID = findWorkerForWorkspaceInState(cc.bs.State, workspaceID)
			if workerID == "" {
				return remote.EmitErrorWith("worker_resolution_failed",
					errors.New("no worker hosts this workspace yet; pass --worker-id explicitly"))
			}
		}
		if err := registerFileTabPath(cc.ctx, cc.c, workerID, cc.bs.OrgID, workspaceID, tabID, filePath); err != nil {
			return remote.EmitErrorWith("worker_register_failed", err)
		}
		ops := []*leapmuxv1.OrgOp{
			opSetTabTileID(cc.bs, tt, tabID, resolvedTileID),
			opSetTabPosition(cc.bs, tt, tabID, resolvedPos),
			opSetTabWorkerID(cc.bs, tt, tabID, workerID),
		}
		if displayMode != 0 {
			ops = append(ops, opSetTabDisplayMode(cc.bs, tt, tabID, int32(displayMode)))
		}
		if fileViewMode != 0 {
			ops = append(ops, opSetTabFileViewMode(cc.bs, tt, tabID, int32(fileViewMode)))
		}
		if err := cc.submitOps(ops); err != nil {
			return err
		}
		out := tabOpenEnvelope(tabID, tabType, workspaceID, workerID, resolvedTileID, resolvedPos)
		out["path"] = filePath
		return remote.EmitData(out)
	}
	return remote.EmitError("invalid_request", "--type must be agent|terminal|file")
}

// tabOpenEnvelope is the shared shape every `tab open` response emits:
// (tab_id, tab_type, workspace_id, worker_id, tile_id, position).
// Each per-type branch then layers on its own warnings / extras
// (permission_mode_warning, initial_message_warning, path) instead of
// repeating the struct-literal in three places.
func tabOpenEnvelope(tabID, tabType, workspaceID, workerID, tileID, position string) map[string]any {
	return map[string]any{
		"tab_id":       tabID,
		"tab_type":     tabType,
		"workspace_id": workspaceID,
		"worker_id":    workerID,
		"tile_id":      tileID,
		"position":     position,
	}
}

// registerFileTabPath invokes the worker's `RegisterFileTabPath`
// inner-RPC over E2EE so the file path stays off the hub. The
// returned response is empty on success; any worker-side error is
// surfaced as a wrapped codedRPCError.
func registerFileTabPath(ctx context.Context, c *remote.Client, workerID, orgID, workspaceID, tabID, filePath string) error {
	req := &leapmuxv1.RegisterFileTabPathRequest{
		OrgId:       orgID,
		WorkspaceId: workspaceID,
		TabId:       tabID,
		FilePath:    filePath,
	}
	var resp leapmuxv1.RegisterFileTabPathResponse
	return callInnerRPCBest(ctx, c, workerID, "RegisterFileTabPath", req, &resp)
}

// findWorkerForWorkspaceInState returns the first worker_id hosting a
// live tab in the given workspace, reading from the already-bootstrapped
// OrgMaterialized state. Used by `tab open --type=file` when the caller
// doesn't pass --worker-id explicitly — a workspace that has at least
// one live tab tells us which worker owns its filesystem context.
// Returns "" when no live worker is on file.
func findWorkerForWorkspaceInState(state *leapmuxv1.OrgMaterialized, workspaceID string) string {
	for _, t := range state.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		w := t.GetWorkerId().GetValue()
		if w == "" {
			continue
		}
		ws := crdt.FindRootWorkspace(state.GetNodes(), state.GetWorkspaces(), t.GetTileId().GetValue())
		if ws == workspaceID {
			return w
		}
	}
	return ""
}
