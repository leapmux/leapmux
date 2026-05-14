package cmd

import (
	"context"
	"flag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
)

// agentScaffoldOpts configures `withResolvedAgent`. The defaults
// match the shape every agent sub-command except `open` / `list` /
// `providers` / `close` shares: bind the universal entity flag set
// pinned to TabTypeAgent, require the resolver to find a (tab,
// worker) pair, preflight the worker, then call the body.
type agentScaffoldOpts struct {
	// setup runs BEFORE parseFlags so the caller can register
	// additional flags on the supplied FlagSet.
	setup func(fs *flag.FlagSet)
	// validate runs AFTER parseFlags but BEFORE the client is built.
	// Use it for caller-specific input gates (e.g. requiring --message
	// or --title); return the error to short-circuit before any
	// network call.
	validate func() error
	// noDeadline skips `rpcDeadline` so the body sees a non-cancelling
	// context. Streaming commands (`agent messages --follow`) use
	// this.
	noDeadline bool
	// body runs the per-command action with the fully-resolved
	// (ctx, client, workerID, agentID, workspaceID). workspaceID is
	// passed through to the local-IPC transport for delegation
	// scoping (the worker router uses it to mint the right bearer).
	body func(ctx context.Context, c *remote.Client, workerID, agentID, workspaceID string) error
}

// withResolvedAgent runs the shared scaffold for every `agent <verb>`
// command that targets an existing agent (send / interrupt / get /
// rename / messages / set / send-control-response). It binds the
// universal entity flag set pinned to TabTypeAgent, runs the resolver
// to derive (worker, agent, workspace) from whichever subset of
// --tab-id / --worker-id / --workspace-id / --tile-id / --org-id /
// --user-id the caller supplied (with LEAPMUX_REMOTE_*_ID env-var
// defaults), preflights the worker, and invokes the body.
//
// The helper does NOT cover commands whose shape diverges:
//   - `agent open` mints an agent rather than addressing one;
//   - `agent list` / `agent providers` don't take a tab id;
//   - `agent close` skips worker preflight and has its own fallback
//     CRDT-tombstone path on worker-unreachable errors.
func withResolvedAgent(rawCtx any, args []string, opts agentScaffoldOpts) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{
		HideOrg:      true,
		HideUser:     true,
		FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	})
	if opts.setup != nil {
		opts.setup(fs)
	}
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if opts.validate != nil {
		if err := opts.validate(); err != nil {
			return err
		}
	}
	// Pre-credential input check: every agent verb needs at least one
	// entity-id input (the resolver will derive the rest). Skipping
	// this would let requireClient fire first and surface a
	// `not_logged_in` envelope for users who simply forgot --tab-id,
	// masking the real "you need an agent id" hint.
	if !hasAnyEntityInput(in) {
		return remote.EmitError("invalid_request", "missing required ID(s): pass --tab-id (or any of --worker-id / --workspace-id / --tile-id / --org-id / --user-id)")
	}
	c, err := requireClient(hub)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if !opts.noDeadline {
		var cancel context.CancelFunc
		ctx, cancel = rpcDeadline(ctx)
		defer cancel()
	}
	got, err := runResolve(ctx, c, resolve.Need{TabID: true, WorkerID: true}, in)
	if err != nil {
		return err
	}
	if err := maybePreflightWorker(ctx, c, got.WorkerID); err != nil {
		return err
	}
	return opts.body(ctx, c, got.WorkerID, got.TabID, got.WorkspaceID)
}
