package cmd

import (
	"context"

	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/optionids"
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

// closeOrphanAgent tears down an agent that was spawned but never got a tab,
// on the channel the spawn already holds. Best-effort: the caller is already
// returning the failure that caused the unwind.
func closeOrphanAgent(ctx context.Context, w workerCall, agentID string) {
	_ = w.CallEmit(ctx, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: agentID}, nil)
}

// openAgentAndAddTab opens an agent on the worker, defaults its tile
// to the workspace's root node when none is supplied, writes the
// CRDT tab batch (tile_id + position + worker_id), and rolls the
// agent back on CRDT failure. The permission mode is seeded into the
// OpenAgent options (applied at launch); the optional initial-message
// follow-up runs AFTER the CRDT batch — its failure surfaces as a
// non-fatal warning on the result.
//
// The two round-trips that don't depend on each other — OpenAgent on
// the worker and crdtBootstrap — are run concurrently via errgroup so
// wall-clock latency is bounded by the slower of the two rather than
// their sum.
//
// `ch` is a channel the CALLER opened to args.WorkerID and keeps open for
// the whole sequence, so OpenAgent, the optional EnqueueAgentInput, and any
// rollback CloseAgent share one Noise_NK handshake instead of one apiece.
// Pass nil on local-IPC clients, where there is no channel and each call
// routes through the per-agent socket.
//
// Only one errgroup leg touches `ch`, so nothing here relies on the
// channel being safe for concurrent calls.
func openAgentAndAddTab(ctx context.Context, c *control.Client, w workerCall, args openAgentArgs) (*openAgentResult, error) {
	if args.WorkspaceID == "" || args.WorkerID == "" {
		return nil, control.EmitError("invalid_request", "workspace_id and worker_id are required for --type=agent")
	}
	// Initial option selections (model / effort / permission mode), built once.
	options := spawnOptions(args.Model, args.Effort, args.PermissionMode)
	req := &leapmuxv1.OpenAgentRequest{
		WorkerId:      args.WorkerID,
		AgentProvider: args.Provider,
		Options:       options,
		Title:         args.Title,
		WorkingDir:    args.WorkingDir,
	}

	// Phase 1: spawn the agent on the worker AND bootstrap the CRDT in
	// parallel -- neither leg feeds the other. Errgroup short-circuits
	// the other half on failure via ctx cancellation.
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, gctx := errgroup.WithContext(gctx)

	var resp leapmuxv1.OpenAgentResponse
	var bs *CRDTBootstrap

	g.Go(func() error {
		return w.CallEmit(gctx, "OpenAgent", req, &resp)
	})
	g.Go(func() error {
		var err error
		bs, err = crdtBootstrap(gctx, c, []string{args.WorkspaceID})
		if err != nil {
			return control.EmitErrorWith("crdt_bootstrap_failed", err)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		// If OpenAgent succeeded but the bootstrap leg failed, the
		// agent is now orphan on the worker. Roll it back so the user
		// isn't left with a dangling agent record.
		if agentID := resp.GetAgent().GetId(); agentID != "" {
			closeOrphanAgent(ctx, w, agentID)
		}
		return nil, err
	}

	agentID := resp.GetAgent().GetId()
	// Both unwind paths go through the SAME helper on the SAME channel. The
	// CRDT-failure rollback below used to call callInnerRPC, which opened a
	// fresh Noise_NK channel and quietly broke the one-handshake property this
	// function's doc comment promises -- on the likelier of the two paths.
	rollback := func() { closeOrphanAgent(ctx, w, agentID) }
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
	// worker that just received OpenAgent, on the same channel it arrived over.
	if args.InitialMessage != "" {
		if err := w.Call(ctx, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
			AgentId: agentID, InputId: id.Generate(), Text: args.InitialMessage,
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		}, nil); err != nil {
			// Non-fatal: the agent is open and its tab is registered, so the
			// caller reports the spawn as a success carrying this warning.
			result.InitialMsgWarn = err.Error()
		}
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
	c *control.Client,
	workspaceID string,
	tabType leapmuxv1.TabType,
	tabID, requestedTileID string,
	spec positionSpec,
	workerID string,
	rollback func(),
) (resolvedTileID, position string, err error) {
	bs, err := crdtBootstrap(ctx, c, []string{workspaceID})
	if err != nil {
		rollback()
		return "", "", control.EmitErrorWith("crdt_bootstrap_failed", err)
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
	c *control.Client,
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
		return "", "", control.EmitError("missing_tile", "workspace has no root_node_id; pass --tile-id or wait for workspace seed to propagate")
	}
	// Position resolution: for --before / --after the helper derives
	// the destination tile from the ref tab; for --first / --last it
	// needs resolvedTileID supplied above.
	resolvedTileID, position, err = resolvePositionSpec(bs.State, resolvedTileID, "", spec)
	if err != nil {
		rollback()
		return "", "", err
	}
	ops := []*leapmuxv1.CrdtOp{
		opSetTabTileID(bs, tabType, tabID, resolvedTileID),
		opSetTabPosition(bs, tabType, tabID, position),
		opSetTabWorkerID(bs, tabType, tabID, workerID),
	}
	batchRes, err := crdtSubmitBatch(ctx, c, bs, workspaceID, crdtNewBatch(ops))
	if err != nil {
		rollback()
		return "", "", control.EmitErrorWith("crdt_submit_failed", err)
	}
	if err := crdtBatchError(batchRes); err != nil {
		rollback()
		return "", "", control.EmitErrorWith("crdt_batch_rejected", err)
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
	// Title is optional, and empty means "you pick one": the worker then takes
	// a random `Terminal <Name>` from the shared pool, the same answer it gives
	// the quick-open buttons. It reaches the same field OpenAgentRequest has, so
	// `--title` means one thing for both tab types.
	Title    string
	Position positionSpec
}

// terminalOpenRequest projects the resolved CLI arguments onto the wire
// request, the way spawnOptions does for the agent path.
//
// Cols / Rows stay at zero so the worker applies its 80x25 default
// (terminal.Open); the frontend resizes the PTY as soon as the user attaches.
func terminalOpenRequest(args openTerminalArgs) *leapmuxv1.OpenTerminalRequest {
	return &leapmuxv1.OpenTerminalRequest{
		WorkerId:      args.WorkerID,
		WorkingDir:    args.WorkingDir,
		Shell:         args.Shell,
		ShellStartDir: args.ShellStartDir,
		Title:         args.Title,
	}
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
func openTerminalAndAddTab(ctx context.Context, c *control.Client, w workerCall, args openTerminalArgs) (*openTerminalResult, error) {
	if args.WorkspaceID == "" || args.WorkerID == "" {
		return nil, control.EmitError("invalid_request", "workspace_id and worker_id are required for --type=terminal")
	}
	req := terminalOpenRequest(args)
	var resp leapmuxv1.OpenTerminalResponse
	if err := w.CallEmit(ctx, "OpenTerminal", req, &resp); err != nil {
		return nil, err
	}
	terminalID := resp.GetTerminalId()
	// On the SAME channel as the open, matching the agent path: the rollback
	// is the likelier of this function's two worker calls to actually run, and
	// paying a second Noise_NK handshake for it defeats the hoist.
	rollback := func() {
		_ = w.CallEmit(ctx, "CloseTerminal", &leapmuxv1.CloseTerminalRequest{
			TerminalId: terminalID,
		}, nil)
	}
	resolvedTileID, position, err := addTabToCRDT(ctx, c, args.WorkspaceID, leapmuxv1.TabType_TAB_TYPE_TERMINAL, terminalID, args.TileID, args.Position, args.WorkerID, rollback)
	if err != nil {
		return nil, err
	}
	return &openTerminalResult{
		TerminalID: terminalID,
		TileID:     resolvedTileID,
		Position:   position,
	}, nil
}
