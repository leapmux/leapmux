package cmd

import (
	"context"
	"sync/atomic"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
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
		resp, err := c.RemoteIPCService().Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
		if err != nil {
			return remote.EmitErrorWith("rpc_failed", err)
		}
		// Hand-projected so the tab_type enum lands as a string per the
		// CLI's enum-projection convention; encoding/json on the raw
		// proto message would emit ordinals.
		out := map[string]any{
			"user_id":      resp.Msg.GetUserId(),
			"username":     resp.Msg.GetUsername(),
			"org_id":       resp.Msg.GetOrgId(),
			"workspace_id": resp.Msg.GetWorkspaceId(),
			"worker_id":    resp.Msg.GetWorkerId(),
			"tab_id":       resp.Msg.GetTabId(),
			"tab_type":     tabTypeName(resp.Msg.GetTabType()),
		}
		if scope := resp.Msg.GetScope(); scope != nil {
			out["scope"] = map[string]any{"workspace_ids": scope.GetWorkspaceIds()}
		}
		return remote.EmitData(out)
	}
	return remote.EmitData(map[string]any{
		"hub_url":  c.HubURL,
		"user_id":  c.UserID,
		"username": c.Username,
	})
}

// RunWorkspaceList enumerates accessible workspaces. The resolver
// can derive --org-id from any of --tab-id / --workspace-id /
// --worker-id / --user-id so scripts running inside a worker-spawned
// agent don't have to know their org id explicitly.
//
// With --all-orgs it instead lists every workspace the caller can read
// (owner OR explicit grant) across every org -- including workspaces
// owned by an org the caller is not a member of (cross-org
// collaboration). That listing is not org-scoped, so no --org-id
// resolution is needed; each row carries its own org_id (and
// created_by), so a script can filter to shares or by org.
func RunWorkspaceList(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var allOrgs bool
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	fs.BoolVar(&allOrgs, "all-orgs", false, "list every workspace you can access across all orgs (owner or shared) instead of just your current org's")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if allOrgs {
		return resolveAndEmit(hub, resolve.Need{}, in, func(ctx context.Context, c *remote.Client, _ resolve.Resolved) error {
			var resp leapmuxv1.ListAllAccessibleWorkspacesResponse
			return hubCallUnaryEmitOn(ctx, c, "ListAllAccessibleWorkspaces", "",
				&leapmuxv1.ListAllAccessibleWorkspacesRequest{}, &resp,
				func() any { return resp.GetWorkspaces() })
		})
	}
	return resolveAndEmit(hub, resolve.Need{}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		var resp leapmuxv1.ListWorkspacesResponse
		return hubCallUnaryEmitOn(ctx, c, "ListWorkspaces", "",
			&leapmuxv1.ListWorkspacesRequest{OrgId: got.OrgID}, &resp,
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
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{WorkspaceID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		var resp leapmuxv1.GetWorkspaceResponse
		return hubCallUnaryEmitOn(ctx, c, "GetWorkspace", got.WorkspaceID,
			&leapmuxv1.GetWorkspaceRequest{WorkspaceId: got.WorkspaceID}, &resp,
			func() any { return resp.GetWorkspace() })
	})
}

// RunWorkspaceCreate provisions a new workspace under an org. The
// resolver derives --org-id from --tab-id / --workspace-id /
// --worker-id / --user-id when not supplied directly.
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
		return remote.EmitError("invalid_request", "--title is required")
	}
	return resolveAndEmit(hub, resolve.Need{OrgID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		var resp leapmuxv1.CreateWorkspaceResponse
		return hubCallUnaryEmitOn(ctx, c, "CreateWorkspace", "",
			&leapmuxv1.CreateWorkspaceRequest{OrgId: got.OrgID, Title: title}, &resp,
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
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	fs.StringVar(&title, "title", "", "new title (required)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if title == "" {
		return remote.EmitError("invalid_request", "--title is required")
	}
	return resolveAndEmit(hub, resolve.Need{WorkspaceID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		var resp leapmuxv1.RenameWorkspaceResponse
		return hubCallUnaryEmitOn(ctx, c, "RenameWorkspace", got.WorkspaceID,
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
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	fs.BoolVar(&force, "force", false, "delete even if the calling tab lives in the target workspace (would kill the caller's own PTY)")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{WorkspaceID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		if err := guardWorkspaceDelete(ctx, c, got.WorkspaceID, force); err != nil {
			return err
		}
		var resp leapmuxv1.DeleteWorkspaceResponse
		if err := hubCallUnary(ctx, c, "DeleteWorkspace", got.WorkspaceID, &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: got.WorkspaceID}, &resp); err != nil {
			return remote.EmitErrorWith(classifyHubError(err), err)
		}

		// Mirror the frontend's two-step delete: hub drops the workspace
		// row (returning every worker that hosted a tab in it), then we
		// fan out CleanupWorkspace over E2EE so each worker tears down
		// its agents/terminals/worktrees. The fan-out logic is in
		// runWorkspaceCleanupFanout so it can be exercised by unit tests
		// without standing up a real E2EE worker harness.
		status, entries := runWorkspaceCleanupFanout(ctx, got.WorkspaceID, resp.GetWorkerIds(), cliCleanupCaller(c))

		return remote.EmitData(map[string]any{
			"workspace_id": got.WorkspaceID,
			"worker_ids":   resp.GetWorkerIds(),
			"status":       status,
			"cleanup":      entries,
		})
	})
}

// cleanupCaller is the seam workspace-delete uses to fan out a
// `CleanupWorkspace` inner-RPC per worker. The CLI binds it to the
// real Client+E2EE channel; unit tests pass a fake that exercises the
// surrounding aggregation logic without spinning up worker plumbing.
type cleanupCaller func(ctx context.Context, workerID, workspaceID string) (*leapmuxv1.CleanupWorkspaceResponse, error)

// cliCleanupCaller wires runWorkspaceCleanupFanout to the production
// transport: every worker_id gets its own E2EE channel and a
// CleanupWorkspace inner-RPC.
func cliCleanupCaller(c *remote.Client) cleanupCaller {
	return func(ctx context.Context, workerID, workspaceID string) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		req := &leapmuxv1.CleanupWorkspaceRequest{WorkspaceId: workspaceID}
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
// Failures DO NOT short-circuit: the user needs per-worker visibility
// so they can decide whether to retry only the failures or rerun the
// whole delete. errgroup.Group (no context cancellation) is used so
// one worker's failure doesn't cancel the others' in-flight calls.
func runWorkspaceCleanupFanout(ctx context.Context, workspaceID string, workerIDs []string, call cleanupCaller) (string, []map[string]any) {
	entries := make([]map[string]any, len(workerIDs))
	var failed atomic.Bool
	var g errgroup.Group
	g.SetLimit(8)
	for i, wid := range workerIDs {
		g.Go(func() error {
			entry := map[string]any{"worker_id": wid}
			resp, err := call(ctx, wid, workspaceID)
			if err != nil {
				entry["status"] = "failed"
				entry["error"] = err.Error()
				failed.Store(true)
			} else {
				entry["status"] = "ok"
				if wts := resp.GetWorktrees(); len(wts) > 0 {
					entry["worktrees"] = wts
				}
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
