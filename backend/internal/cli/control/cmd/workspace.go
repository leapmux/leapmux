package cmd

import (
	"context"
	"sync/atomic"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func RunWhoami(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	fs := flagSet(cmd, &hub)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	c, err := requireClient(hub)
	if err != nil {
		return err
	}
	if c.IsLocal() {
		ipc, ipcErr := c.ControlIPCService()
		if ipcErr != nil {
			return control.EmitErrorWith("invalid_request", ipcErr)
		}
		resp, err := ipc.Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
		if err != nil {
			return control.EmitErrorWith("rpc_failed", err)
		}
		// Hand-projected so the tab_type enum lands as a string per the
		// CLI's enum-projection convention; encoding/json on the raw
		// proto message would emit ordinals.
		// No workspace_id and no scope: the bearer is scoped to the user and
		// to the minting worker, not to a workspace, and the spawn context
		// carries the stable tab id instead -- `tab get --tab-id <id>` derives
		// the workspace from it and stays correct across a tab move.
		return control.EmitData(map[string]any{
			"user_id":   resp.Msg.GetUserId(),
			"username":  resp.Msg.GetUsername(),
			"worker_id": resp.Msg.GetWorkerId(),
			"tab_id":    resp.Msg.GetTabId(),
			"tab_type":  tabTypeName(resp.Msg.GetTabType()),
		})
	}
	return control.EmitData(map[string]any{
		"hub_url":  c.HubURL,
		"user_id":  c.UserID,
		"username": c.Username,
	})
}

// RunWorkspaceList enumerates the caller's own workspaces. The hub
// takes the tenant from the session, so the command needs no entity ids
// at all -- `workspace list` is a complete invocation, from a laptop or
// from inside a worker-spawned agent.
func RunWorkspaceList(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		var resp leapmuxv1.ListWorkspacesResponse
		return hubCallUnaryEmitOn(ctx, c, "ListWorkspaces",
			&leapmuxv1.ListWorkspacesRequest{}, &resp,
			func() any { return resp.GetWorkspaces() })
	})
}

// RunWorkspaceGet looks up a single workspace. --workspace-id is the
// canonical input, but the resolver also accepts --tab-id /
// --tile-id (LocateTile / LocateTab derives the workspace).
func RunWorkspaceGet(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{WorkspaceID: true}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		var resp leapmuxv1.GetWorkspaceResponse
		return hubCallUnaryEmitOn(ctx, c, "GetWorkspace",
			&leapmuxv1.GetWorkspaceRequest{WorkspaceId: got.WorkspaceID}, &resp,
			func() any { return resp.GetWorkspace() })
	})
}

// RunWorkspaceCreate provisions a new workspace for the authenticated
// user. The hub takes the owner from the session, so the command needs
// no entity ids at all -- `workspace create --title X` is a complete
// invocation.
func RunWorkspaceCreate(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, title string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	fs.StringVar(&title, "title", "", "workspace title (required)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if title == "" {
		return control.EmitError("invalid_request", "--title is required")
	}
	return resolveAndEmit(hub, resolve.Need{}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		var resp leapmuxv1.CreateWorkspaceResponse
		return hubCallUnaryEmitOn(ctx, c, "CreateWorkspace",
			&leapmuxv1.CreateWorkspaceRequest{Title: title}, &resp,
			func() any { return map[string]string{"workspace_id": resp.GetWorkspaceId()} })
	})
}

// RunWorkspaceRename retitles a workspace. --workspace-id resolves
// from --tab-id / --tile-id when only one of those is given.
func RunWorkspaceRename(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, title string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	fs.StringVar(&title, "title", "", "new title (required)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if title == "" {
		return control.EmitError("invalid_request", "--title is required")
	}
	return resolveAndEmit(hub, resolve.Need{WorkspaceID: true}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		var resp leapmuxv1.RenameWorkspaceResponse
		return hubCallUnaryEmitOn(ctx, c, "RenameWorkspace",
			&leapmuxv1.RenameWorkspaceRequest{WorkspaceId: got.WorkspaceID, Title: title}, &resp,
			func() any { return map[string]string{"workspace_id": got.WorkspaceID} })
	})
}

// RunWorkspaceDelete drops the workspace row and fans out
// CleanupWorkspace to every worker that hosted a tab. --workspace-id
// can be derived from --tab-id / --tile-id via the resolver.
//
// The hub-side delete cascades into a worker-side CleanupWorkspace
// fan-out that kills every PTY in the workspace — so running this
// from a terminal tab inside the target workspace will sever the
// caller's own shell mid-response. `guardWorkspaceDelete` rejects
// that case unless --force is supplied.
func RunWorkspaceDelete(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var force bool
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	fs.BoolVar(&force, "force", false, "delete even if the calling tab lives in the target workspace (would kill the caller's own PTY)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{WorkspaceID: true}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		if err := guardWorkspaceDelete(ctx, c, got.WorkspaceID, force); err != nil {
			return err
		}
		var resp leapmuxv1.DeleteWorkspaceResponse
		if err := hubCallUnary(ctx, c, "DeleteWorkspace", &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: got.WorkspaceID}, &resp); err != nil {
			return control.EmitErrorWith(classifyHubError(err), err)
		}

		// Mirror the frontend's two-step delete: the hub drops the workspace
		// row and returns each hosting worker WITH the tabs it must tear down
		// (read inside the delete transaction), then we fan out
		// CleanupWorkspace over E2EE. The fan-out logic is in
		// runWorkspaceCleanupFanout so it can be exercised by unit tests
		// without standing up a real E2EE worker harness.
		//
		// No pre-delete ListTabs read any more: this command used to resolve
		// its own list beforehand and swallow that read's error to nil, which
		// silently degraded the whole fan-out to "close nothing".
		status, entries := runWorkspaceCleanupFanout(ctx, resp.GetWorkerTabs(), cliCleanupCaller(c))

		workerIDs := make([]string, 0, len(resp.GetWorkerTabs()))
		for _, wt := range resp.GetWorkerTabs() {
			workerIDs = append(workerIDs, wt.GetWorkerId())
		}
		return control.EmitData(map[string]any{
			"workspace_id": got.WorkspaceID,
			"worker_ids":   workerIDs,
			"status":       status,
			"cleanup":      entries,
		})
	})
}

// cleanupCaller is the seam workspace-delete uses to fan out a
// `CleanupWorkspace` inner-RPC per worker. The CLI binds it to the
// real Client+E2EE channel; unit tests pass a fake that exercises the
// surrounding aggregation logic without spinning up worker plumbing.
type cleanupCaller func(ctx context.Context, workerID string, tabs []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error)

// cliCleanupCaller wires runWorkspaceCleanupFanout to the production
// transport: every worker_id gets its own E2EE channel and a
// CleanupWorkspace inner-RPC.
func cliCleanupCaller(c *control.Client) cleanupCaller {
	return func(ctx context.Context, workerID string, tabs []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		req := &leapmuxv1.CleanupWorkspaceRequest{Tabs: tabs}
		var resp leapmuxv1.CleanupWorkspaceResponse
		if err := callInnerRPCBest(ctx, c, workerID, "CleanupWorkspace", req, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}
}

// runWorkspaceCleanupFanout invokes call per worker in parallel and
// assembles the per-worker status entries the CLI emits, preserving the
// input workerIDs order. Returns ("ok", …) when every call succeeds,
// ("partial", …) when at least one fails, and ("ok", []) when there
// are no workers (the workspace had no tabs — the hub-side delete is
// the only step).
//
// workerTabs pairs each worker with the tabs it hosts, as the hub read them
// inside the delete transaction -- so a worker in this list always arrives with
// its own tab list rather than depending on a separate, racier read.
//
// Failures DO NOT short-circuit: the user needs per-worker visibility
// so they can decide whether to retry only the failures or rerun the
// whole delete. errgroup.Group (no context cancellation) is used so
// one worker's failure doesn't cancel the others' in-flight calls.
func runWorkspaceCleanupFanout(ctx context.Context, workerTabs []*leapmuxv1.WorkerTabs, call cleanupCaller) (string, []map[string]any) {
	entries := make([]map[string]any, len(workerTabs))
	var failed atomic.Bool
	var g errgroup.Group
	g.SetLimit(8)
	for i, wt := range workerTabs {
		g.Go(func() error {
			entry := map[string]any{"worker_id": wt.GetWorkerId()}
			_, err := call(ctx, wt.GetWorkerId(), wt.GetTabs())
			if err != nil {
				entry["status"] = "failed"
				entry["error"] = err.Error()
				failed.Store(true)
			} else {
				entry["status"] = "ok"
			}
			entries[i] = entry
			return nil
		})
	}
	_ = g.Wait()
	status := "ok"
	if failed.Load() {
		status = "partial"
	}
	return status, entries
}
