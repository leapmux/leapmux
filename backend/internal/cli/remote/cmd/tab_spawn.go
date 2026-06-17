package cmd

import (
	"context"

	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/tunnel"
)

// openAgentArgs is the resolved input to openAgentAndAddTab. All
// fields are set by the caller after env-default + tile→workspace
// derivation; the helper does not consult os.Getenv itself.
type openAgentArgs struct {
	WorkspaceID    string
	WorkerID       string
	TileID         string
	Provider       leapmuxv1.AgentProvider
	Model          string
	Effort         string
	Title          string
	PermissionMode string
	WorkingDir     string
	InitialMessage string
	// Position spec captures the caller's --first / --last /
	// --before / --after intent; resolved against the bootstrapped
	// state inside addTabToCRDTWithBootstrap.
	Position positionSpec
}

// spawnOptions builds the OpenAgent initial option map from the CLI's --model / --effort /
// --permission-mode flags, omitting empty values so the worker fills provider defaults. The
// permission mode rides in here -- applied at LAUNCH alongside model/effort (resolveProviderDefaults
// for the catalog providers, the startup permission-mode apply for ACP) -- rather than via a
// redundant post-spawn UpdateAgentSettings that would re-set the already-applied mode (and, for a
// provider that applies the mode via restart, force a spawn-time relaunch). This treats permission
// mode uniformly with every other axis, closing the last special-cased seam in the spawn path.
func spawnOptions(model, effort, permissionMode string) map[string]string {
	options := map[string]string{}
	if model != "" {
		options[optionids.Model] = model
	}
	if effort != "" {
		options[optionids.Effort] = effort
	}
	if permissionMode != "" {
		options[optionids.PermissionMode] = permissionMode
	}
	return options
}

// openAgentResult is what openAgentAndAddTab emits on success.
type openAgentResult struct {
	Agent          *leapmuxv1.AgentInfo
	TileID         string
	Position       string
	InitialMsgWarn string
}

// openAgentAndAddTab opens an agent on the worker, defaults its tile
// to the workspace's root node when none is supplied, writes the
// CRDT tab batch (tile_id + position + worker_id), and rolls the
// agent back on CRDT failure. The permission mode is seeded into the
// OpenAgent options (applied at launch); the optional initial-message
// follow-up runs AFTER the CRDT batch — its failure surfaces as a
// non-fatal warning on the result.
//
// The three round-trips that don't depend on each other — OpenAgent on
// the worker, resolveOrgID via GetWorkspace, and crdtBootstrap once
// orgID is known — are run concurrently via errgroup so wall-clock
// latency is bounded by the slowest of (OpenAgent) and
// (GetWorkspace → crdtBootstrap) rather than their sum.
func openAgentAndAddTab(ctx context.Context, c *remote.Client, args openAgentArgs) (*openAgentResult, error) {
	if args.WorkspaceID == "" || args.WorkerID == "" {
		return nil, remote.EmitError("invalid_request", "workspace_id and worker_id are required for --type=agent")
	}
	// Resolve org synchronously: both OpenAgentRequest.OrgId (so the
	// worker can inject LEAPMUX_REMOTE_ORG_ID into the spawned process)
	// and crdtBootstrap below need it. One round-trip up front lets
	// OpenAgent and crdtBootstrap then run in parallel.
	orgID, err := resolveOrgID(ctx, c, args.WorkspaceID)
	if err != nil {
		return nil, remote.EmitErrorWith("resolve_failed", err)
	}
	// Initial option selections (model / effort / permission mode), built once.
	options := spawnOptions(args.Model, args.Effort, args.PermissionMode)
	req := &leapmuxv1.OpenAgentRequest{
		OrgId:         orgID,
		WorkspaceId:   args.WorkspaceID,
		WorkerId:      args.WorkerID,
		AgentProvider: args.Provider,
		Options:       options,
		Title:         args.Title,
		WorkingDir:    args.WorkingDir,
	}

	// Phase 1: spawn the agent on the worker AND bootstrap the CRDT in
	// parallel. Org is already resolved above. Errgroup short-circuits
	// the other half on failure via ctx cancellation.
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, gctx := errgroup.WithContext(gctx)

	var resp leapmuxv1.OpenAgentResponse
	var bs *CRDTBootstrap

	g.Go(func() error {
		return callInnerRPC(gctx, c, args.WorkerID, "OpenAgent", req, &resp)
	})
	g.Go(func() error {
		var err error
		bs, err = crdtBootstrap(gctx, c, orgID, []string{args.WorkspaceID})
		if err != nil {
			return remote.EmitErrorWith("crdt_bootstrap_failed", err)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		// If OpenAgent succeeded but the bootstrap leg failed, the
		// agent is now orphan on the worker. Roll it back so the user
		// isn't left with a dangling agent record.
		if agentID := resp.GetAgent().GetId(); agentID != "" {
			_ = callInnerRPC(ctx, c, args.WorkerID, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: agentID}, nil)
		}
		return nil, err
	}

	agentID := resp.GetAgent().GetId()
	rollback := func() {
		_ = callInnerRPC(ctx, c, args.WorkerID, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: agentID}, nil)
	}
	resolvedTileID, position, err := addTabToCRDTWithBootstrap(ctx, c, bs, args.WorkspaceID, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID, args.TileID, args.Position, args.WorkerID, rollback)
	if err != nil {
		return nil, err
	}
	result := &openAgentResult{
		Agent:    resp.GetAgent(),
		TileID:   resolvedTileID,
		Position: position,
	}
	// The permission mode rode in on the OpenAgent options above, so the only remaining
	// post-spawn follow-up is the optional initial message -- an inner-RPC against the same
	// worker that just received OpenAgent.
	if args.InitialMessage != "" {
		_ = withWorkerChannel(ctx, c, args.WorkerID, func(ch *tunnel.Channel) error {
			if err := callInnerRPCOnChannelMarshal(ctx, ch, c, args.WorkerID, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
				AgentId: agentID,
				Content: args.InitialMessage,
			}, nil); err != nil {
				result.InitialMsgWarn = err.Error()
			}
			return nil
		})
	}
	return result, nil
}

// addTabToCRDT bootstraps the CRDT for `workspaceID` and submits the
// tab-registration batch. Convenience wrapper for callers that don't
// already have a *CRDTBootstrap in hand; the parallelizable variant is
// `addTabToCRDTWithBootstrap`. `rollback` runs on any failure so the
// worker-side resource owned by the caller is torn down before the
// error surfaces. Errors are already wrapped in EmitError /
// EmitErrorWith — the caller should return them directly.
func addTabToCRDT(
	ctx context.Context,
	c *remote.Client,
	orgID, workspaceID string,
	tabType leapmuxv1.TabType,
	tabID, requestedTileID string,
	spec positionSpec,
	workerID string,
	rollback func(),
) (resolvedTileID, position string, err error) {
	bs, err := crdtBootstrap(ctx, c, orgID, []string{workspaceID})
	if err != nil {
		rollback()
		return "", "", remote.EmitErrorWith("crdt_bootstrap_failed", err)
	}
	return addTabToCRDTWithBootstrap(ctx, c, bs, workspaceID, tabType, tabID, requestedTileID, spec, workerID, rollback)
}

// addTabToCRDTWithBootstrap resolves the destination tile + LexoRank
// position from the supplied positionSpec and emits a 3-op batch
// registering the tab's tile_id + position + worker_id. Callers that
// already paid for a CRDT bootstrap — e.g. the agent / terminal open
// path that ran bootstrap in parallel with the worker RPC — use this
// variant directly to avoid a redundant bootstrap.
//
// Tile resolution priority:
//
//  1. The ref tab's tile, when the spec is --before / --after (and
//     consistent with requestedTileID if both are set).
//  2. requestedTileID, when supplied.
//  3. The workspace's root_node_id, for backwards compatibility with
//     spawns that don't pin a tile (the env-defaulted parent-tab path
//     normally pre-fills requestedTileID).
func addTabToCRDTWithBootstrap(
	ctx context.Context,
	c *remote.Client,
	bs *CRDTBootstrap,
	workspaceID string,
	tabType leapmuxv1.TabType,
	tabID, requestedTileID string,
	spec positionSpec,
	workerID string,
	rollback func(),
) (resolvedTileID, position string, err error) {
	resolvedTileID = requestedTileID
	if resolvedTileID == "" && (spec.kind == positionFirst || spec.kind == positionLast) {
		resolvedTileID = bs.State.GetWorkspaces()[workspaceID].GetRootNodeId()
	}
	if resolvedTileID == "" && spec.kind != positionBefore && spec.kind != positionAfter {
		rollback()
		return "", "", remote.EmitError("missing_tile", "workspace has no root_node_id; pass --tile-id or wait for workspace seed to propagate")
	}
	// Position resolution: for --before / --after the helper derives
	// the destination tile from the ref tab; for --first / --last it
	// needs resolvedTileID supplied above.
	resolvedTileID, position, err = resolvePositionSpec(bs.State, resolvedTileID, "", spec)
	if err != nil {
		rollback()
		return "", "", err
	}
	ops := []*leapmuxv1.OrgOp{
		opSetTabTileID(bs, tabType, tabID, resolvedTileID),
		opSetTabPosition(bs, tabType, tabID, position),
		opSetTabWorkerID(bs, tabType, tabID, workerID),
	}
	batchRes, err := crdtSubmitBatch(ctx, c, bs, workspaceID, crdtNewBatch(ops))
	if err != nil {
		rollback()
		return "", "", remote.EmitErrorWith("crdt_submit_failed", err)
	}
	if err := crdtBatchError(batchRes); err != nil {
		rollback()
		return "", "", remote.EmitErrorWith("crdt_batch_rejected", err)
	}
	return resolvedTileID, position, nil
}

// openTerminalArgs mirrors openAgentArgs for terminal spawns. Shell
// and ShellStartDir are terminal-only. The PTY's initial dimensions
// are not exposed at the CLI: the worker defaults to 80x25 and the
// frontend immediately resizes once the user attaches, so any caller-
// supplied value would be overwritten in milliseconds.
type openTerminalArgs struct {
	WorkspaceID   string
	WorkerID      string
	TileID        string
	WorkingDir    string
	Shell         string
	ShellStartDir string
	Position      positionSpec
}

// openTerminalResult is what openTerminalAndAddTab emits on success.
type openTerminalResult struct {
	TerminalID string
	TileID     string
	Position   string
}

// openTerminalAndAddTab opens a terminal on the worker, defaults its
// tile to the workspace's root node when none is supplied, writes the
// CRDT tab batch, and rolls the terminal back on CRDT failure.
func openTerminalAndAddTab(ctx context.Context, c *remote.Client, args openTerminalArgs) (*openTerminalResult, error) {
	if args.WorkspaceID == "" || args.WorkerID == "" {
		return nil, remote.EmitError("invalid_request", "workspace_id and worker_id are required for --type=terminal")
	}
	orgID, err := resolveOrgID(ctx, c, args.WorkspaceID)
	if err != nil {
		return nil, remote.EmitErrorWith("resolve_failed", err)
	}
	// Cols / Rows left at zero so the worker applies its 80x25 default
	// (terminal.Open). The frontend resizes the PTY as soon as the
	// user attaches.
	req := &leapmuxv1.OpenTerminalRequest{
		OrgId:         orgID,
		WorkspaceId:   args.WorkspaceID,
		WorkerId:      args.WorkerID,
		WorkingDir:    args.WorkingDir,
		Shell:         args.Shell,
		ShellStartDir: args.ShellStartDir,
	}
	var resp leapmuxv1.OpenTerminalResponse
	if err := callInnerRPC(ctx, c, args.WorkerID, "OpenTerminal", req, &resp); err != nil {
		return nil, err
	}
	terminalID := resp.GetTerminalId()
	rollback := func() {
		_ = callInnerRPC(ctx, c, args.WorkerID, "CloseTerminal", &leapmuxv1.CloseTerminalRequest{
			OrgId:       orgID,
			WorkspaceId: args.WorkspaceID,
			TerminalId:  terminalID,
		}, nil)
	}
	resolvedTileID, position, err := addTabToCRDT(ctx, c, orgID, args.WorkspaceID, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID, args.TileID, args.Position, args.WorkerID, rollback)
	if err != nil {
		return nil, err
	}
	return &openTerminalResult{
		TerminalID: terminalID,
		TileID:     resolvedTileID,
		Position:   position,
	}, nil
}
