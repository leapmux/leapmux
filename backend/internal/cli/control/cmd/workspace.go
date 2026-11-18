package cmd

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func newWorkspaceCommand() *cobra.Command {
	return cli.Group("workspace", "List, create, rename and delete workspaces",
		cli.NewLeaf(cli.Leaf{
			Use:   "list",
			Short: "List your workspaces",
			Examples: []cli.Example{
				{Command: "leapmux control workspace list"},
				{Comment: "Print only the ids.", Command: "leapmux control workspace list -q"},
			},
			Run: runWorkspaceList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [workspace]",
			Short: "Show one workspace (default: the workspace of this tab)",
			Examples: []cli.Example{
				{Command: "leapmux control workspace get"},
				{Command: `leapmux control workspace get "title:Release 1.4"`},
			},
			Complete: completeTargets,
			Run:      runWorkspaceGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "create <title>",
			Mutates: true,
			Short:   "Create a workspace",
			Examples: []cli.Example{
				{Comment: "Create a workspace and keep its id.", Command: `W=$(leapmux control workspace create "Release 1.4" -q)`},
			},
			Run: runWorkspaceCreate,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "rename <workspace> <title>",
			Short:   "Rename a workspace",
			Mutates: true,
			Examples: []cli.Example{
				{Command: `leapmux control workspace rename self "Release 1.5"`},
			},
			Complete: completeTargets,
			Run:      runWorkspaceRename,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "delete <workspace>",
			Short:   "Delete a workspace and close every tab in it",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control workspace delete 9f3kQ2aB"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool(allowSelfFlag, false, "delete the workspace even when this tab lives in it, which ends this tab's process too")
			},
			Complete: completeTargets,
			Run:      runWorkspaceDelete,
		}),
	)
}

// workspaceRow is one workspace in a result.
type workspaceRow struct {
	ID        string `json:"id" table:"ID,short"`
	Title     string `json:"title" table:"TITLE"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt string `json:"created_at,omitempty" table:"CREATED"`
}

func workspaceRowOf(ws *leapmuxv1.Workspace) workspaceRow {
	return workspaceRow{
		ID:        ws.GetId(),
		Title:     ws.GetTitle(),
		CreatedBy: ws.GetCreatedBy(),
		CreatedAt: formatTime(ws.GetCreatedAt()),
	}
}

// runWorkspaceList enumerates the caller's own workspaces. The hub takes the
// tenant from the session, so the command needs no target at all.
func runWorkspaceList(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var resp leapmuxv1.ListWorkspacesResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, &leapmuxv1.ListWorkspacesRequest{}, &resp); err != nil {
		return nil, err
	}
	rows := make([]workspaceRow, 0, len(resp.GetWorkspaces()))
	for _, ws := range resp.GetWorkspaces() {
		rows = append(rows, workspaceRowOf(ws))
	}
	return struct {
		Workspaces []workspaceRow `json:"workspaces"`
	}{rows}, nil
}

func runWorkspaceGet(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.GetWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: target.ID}, &resp); err != nil {
		return nil, err
	}
	return workspaceRowOf(resp.GetWorkspace()), nil
}

// runWorkspaceCreate provisions a new workspace for the authenticated user.
// The hub takes the owner from the session.
func runWorkspaceCreate(inv *cli.Invocation) (any, error) {
	title := strings.TrimSpace(inv.Arg("title"))
	if title == "" {
		return nil, cli.Usagef("the title is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var resp leapmuxv1.CreateWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure, &leapmuxv1.CreateWorkspaceRequest{Title: title}, &resp); err != nil {
		return nil, err
	}
	return workspaceRow{ID: resp.GetWorkspaceId(), Title: title}, nil
}

func runWorkspaceRename(inv *cli.Invocation) (any, error) {
	title := strings.TrimSpace(inv.Arg("title"))
	if title == "" {
		return nil, cli.Usagef("the title is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.RenameWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure, &leapmuxv1.RenameWorkspaceRequest{WorkspaceId: target.ID, Title: title}, &resp); err != nil {
		return nil, err
	}
	return workspaceRow{ID: target.ID, Title: title}, nil
}

// workspaceDeleteResult is the result of `workspace delete`.
type workspaceDeleteResult struct {
	ID string `json:"id"`
	// Status is "ok" when every Worker cleaned up, and "partial" when at
	// least one failed.
	Status    string          `json:"status"`
	WorkerIDs []string        `json:"worker_ids"`
	Cleanup   []workerCleanup `json:"cleanup"`
}

// workerCleanup is the outcome of one Worker's cleanup.
type workerCleanup struct {
	WorkerID string `json:"worker_id" table:"WORKER,short"`
	Status   string `json:"status" table:"STATUS"`
	Error    string `json:"error,omitempty" table:"ERROR"`
}

// runWorkspaceDelete drops the workspace row and fans out CleanupWorkspace
// to every worker that hosted a tab.
//
// The hub-side delete cascades into a worker-side CleanupWorkspace fan-out
// that kills every PTY in the workspace — so running this from a terminal
// tab inside the target workspace will sever the caller's own shell
// mid-response. `guardWorkspaceDelete` rejects that case unless
// --allow-self is supplied.
func runWorkspaceDelete(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	if err := guardWorkspaceDelete(s.ctx, s.c, target.ID, boolFlag(inv, allowSelfFlag)); err != nil {
		return nil, err
	}
	var resp leapmuxv1.DeleteWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure, &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: target.ID}, &resp); err != nil {
		return nil, err
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
	status, entries := runWorkspaceCleanupFanout(s.ctx, resp.GetWorkerTabs(), cliCleanupCaller(s.c))

	workerIDs := make([]string, 0, len(resp.GetWorkerTabs()))
	for _, wt := range resp.GetWorkerTabs() {
		workerIDs = append(workerIDs, wt.GetWorkerId())
	}
	return workspaceDeleteResult{ID: target.ID, Status: status, WorkerIDs: workerIDs, Cleanup: entries}, nil
}

// cleanupCaller is the seam workspace-delete uses to fan out a
// `CleanupWorkspace` inner-RPC per worker. The CLI binds it to the
// real Client+E2EE channel; unit tests pass a fake that exercises the
// surrounding aggregation logic without spinning up worker plumbing.
type cleanupCaller func(ctx context.Context, workerID string, tabs []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error)

// cliCleanupCaller wires runWorkspaceCleanupFanout to the production
// transport: every worker_id gets its own E2EE channel and a
// CleanupWorkspace inner-RPC.
func cliCleanupCaller(c *control.Client) cleanupCaller {
	return func(ctx context.Context, workerID string, tabs []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		req := &leapmuxv1.CleanupWorkspaceRequest{Tabs: tabs}
		var resp leapmuxv1.CleanupWorkspaceResponse
		if err := callInnerRPCBest(ctx, c, workerID, leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure, req, &resp); err != nil {
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
func runWorkspaceCleanupFanout(ctx context.Context, workerTabs []*leapmuxv1.WorkerTabs, call cleanupCaller) (string, []workerCleanup) {
	entries := make([]workerCleanup, len(workerTabs))
	var failed atomic.Bool
	var g errgroup.Group
	g.SetLimit(8)
	for i, wt := range workerTabs {
		g.Go(func() error {
			entry := workerCleanup{WorkerID: wt.GetWorkerId(), Status: "ok"}
			if _, err := call(ctx, wt.GetWorkerId(), wt.GetTabs()); err != nil {
				entry.Status = "failed"
				entry.Error = err.Error()
				failed.Store(true)
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
