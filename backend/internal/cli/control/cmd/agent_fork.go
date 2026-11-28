package cmd

import (
	"context"
	"maps"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

type forkHintError struct {
	err  error
	hint string
}

var _ cli.HintProvider = (*forkHintError)(nil)

func (e *forkHintError) Error() string      { return e.err.Error() }
func (e *forkHintError) Unwrap() error      { return e.err }
func (e *forkHintError) CLIHints() []string { return []string{e.hint} }

func newAgentForkCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "fork <agent> [prompt]",
		Short:   "Fork an agent's conversation history and settings into a new agent tab",
		Mutates: true,
		Args: map[string]string{
			"prompt": `Optional first message to send to the forked agent ("-" reads stdin). Mutually exclusive with --message and --file.`,
		},
		Long: `Fork an existing agent into a new agent tab, cloning its provider session
history and Worker transcript while preserving the provider prompt cache.

Pass --fresh to clone only the agent's settings (provider, model, effort,
permission mode, provider options, and working directory) into a fresh empty
session without copying conversation history. --fresh also allows launching
the clone on another Worker with --worker.

Pass --worktree <branch> to fork the agent directly into a fresh git worktree,
or override individual settings (--model, --effort, --permission-mode, --option,
--dir, --title, --name).`,
		Next: []string{
			`leapmux agent send <id> "Try an alternative approach" --wait`,
			`leapmux agent wait <id>`,
		},
		Examples: []cli.Example{
			{Command: "leapmux agent fork 87a19d5f"},
			{Command: `leapmux agent fork 87a19d5f "Try an alternative implementation" --worktree alt-approach --base main`},
			{Command: `leapmux agent fork self --fresh --model opus --name reviewer`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("model", "", "override model id")
			fs.String("effort", "", "override reasoning effort")
			fs.String("permission-mode", "", "override permission mode")
			fs.StringArray("option", nil, "override provider option as key=value; repeat for more than one")
			fs.String("title", "", `tab title (default: "<source title> (fork)")`)
			fs.String("name", "", "unique short agent slug (lowercase letters, digits, hyphens, underscores)")
			for _, name := range []string{"model", "effort", "permission-mode", "option", "title", "name"} {
				cli.SetFlagGroup(fs, name, "Agent")
			}
			fs.Bool("fresh", false, "clone settings into a fresh empty session without copying conversation history")
			fs.StringP("message", "m", "", `queue a first message on the forked agent ("-" reads stdin)`)
			fs.String("file", "", `read the first message from this file ("-" reads stdin)`)
			fs.StringArray("attach", nil, "attach this file to the first message; repeat for more than one")
			addRequestIDFlag(fs)
			for _, name := range []string{"fresh", "message", "file", "attach", "request-id"} {
				cli.SetFlagGroup(fs, name, "Session & Input")
			}
			fs.Bool("wait", false, "wait until the forked agent settles (and print its reply when a message was sent)")
			fs.Bool("all", false, "with --wait and a message, print every reply, not only the last one")
			fs.Duration("timeout", 0, "with --wait, stop waiting after this long, such as 90s or 30m; 0 waits without a limit")
			addOutputSchemaFlags(fs)
			for _, name := range []string{"wait", "all", "timeout", outputSchemaFlag, schemaRetriesFlag} {
				cli.SetFlagGroup(fs, name, "Wait")
			}
			fs.String("dir", "", "override working directory (default: source agent's working directory)")
			cli.SetFlagGroup(fs, "dir", "Git & Directory")
			addGitSetupFlags(fs)
			addPlacementFlags(fs)
		},
		FlagComplete: mergeFlagCompletions(
			agentOptionFlagCompletions(),
			gitSetupFlagCompletions(),
			tabPositionFlagCompletions(),
		),
		Complete: completeTargets,
		Run:      runAgentFork,
	})
}

func clonedAgentOptions(groups []*leapmuxv1.AvailableOptionGroup) map[string]string {
	opts := map[string]string{}
	for _, g := range groups {
		if g == nil || g.GetId() == "" || g.GetCurrentValue() == "" {
			continue
		}
		// Include non-mutable runtime options too: a forked/exported agent is
		// launched fresh and accepts launch-time options (model, effort, etc.).
		opts[g.GetId()] = g.GetCurrentValue()
	}
	return opts
}

func runAgentFork(inv *cli.Invocation) (any, error) {
	flags, err := parseAgentLaunchFlags(inv)
	if err != nil {
		return nil, err
	}

	s, timed, closeAll, err := openWaitableSession(inv, flags.wait)
	if err != nil {
		return nil, err
	}
	defer closeAll()

	srcTarget, err := timed.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true, WorkspaceID: true, TileID: true})
	if err != nil {
		return nil, err
	}

	// Default placement lands beside the source agent in the same workspace/tile/worker
	// unless overridden explicitly by placement flags.
	p, err := timed.placementForFork(srcTarget)
	if err != nil {
		return nil, err
	}

	fresh := boolFlag(inv, "fresh")
	if !fresh && p.WorkerID != srcTarget.WorkerID {
		return nil, cli.Usagef("cannot fork native conversation history across Workers (pass --fresh to clone settings on another Worker)")
	}

	g, gctx := errgroup.WithContext(timed.ctx)
	if flags.slug != "" {
		excludeID := requestScopedID(flags.requestID, requestScopeAgentFork, p.WorkerID)
		g.Go(func() error {
			return timed.ensureUniqueAgentSlugContext(gctx, flags.slug, excludeID)
		})
	}
	g.Go(func() error {
		return maybePreflightWorker(gctx, s.c, srcTarget.WorkerID)
	})
	if p.WorkerID != srcTarget.WorkerID {
		g.Go(func() error {
			return maybePreflightWorker(gctx, s.c, p.WorkerID)
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	resolveLaunchInputs := func(info *leapmuxv1.AgentInfo) (string, map[string]string, string, error) {
		launchTitle := flags.title
		if launchTitle == "" && info.GetTitle() != "" {
			launchTitle = info.GetTitle() + " (fork)"
		}
		opts := clonedAgentOptions(info.GetOptionGroups())
		maps.Copy(opts, flags.opts)

		var dir string
		if selector := flags.setup.GetUseWorktree(); selector != nil {
			if !inv.Changed("dir") && selector.GetBranch() != "" && p.WorkerID == srcTarget.WorkerID && strings.TrimSpace(info.GetWorkingDir()) != "" {
				dir = info.GetWorkingDir()
			} else {
				var dirErr error
				dir, dirErr = timed.worktreeRepoDir(p.WorkerID, selector)
				if dirErr != nil {
					return "", nil, "", dirErr
				}
			}
		} else if inv.Changed("dir") {
			dir = stringFlag(inv, "dir")
		} else {
			dir = info.GetWorkingDir()
		}
		return launchTitle, opts, dir, nil
	}

	var (
		srcInfo     *leapmuxv1.AgentInfo
		launchTitle string
		opts        map[string]string
		dir         string
	)
	if p.WorkerID != srcTarget.WorkerID {
		srcInfo, err = timed.agentInfo(srcTarget)
		if err != nil {
			return nil, err
		}
		launchTitle, opts, dir, err = resolveLaunchInputs(srcInfo)
		if err != nil {
			return nil, err
		}
	}

	var (
		result      *openAgentResult
		startedOpen bool
		opened      bool
	)
	err = withWorkerChannel(timed.ctx, s.c, p.WorkerID, func(w workerCall) error {
		if srcInfo == nil {
			var infoErr error
			srcInfo, infoErr = agentInfoOnChannel(timed.ctx, w, srcTarget.ID)
			if infoErr != nil {
				return infoErr
			}
			var inputErr error
			launchTitle, opts, dir, inputErr = resolveLaunchInputs(srcInfo)
			if inputErr != nil {
				return inputErr
			}
		}
		startedOpen = true
		var forkSource *leapmuxv1.AgentForkSource
		if !fresh {
			forkSource = &leapmuxv1.AgentForkSource{SourceAgentId: srcTarget.ID}
		}
		var openErr error
		result, openErr = openAgentAndAddTab(timed.ctx, s.c, w, openAgentArgs{
			WorkspaceID:    p.WorkspaceID,
			WorkerID:       p.WorkerID,
			TileID:         p.TileID,
			Provider:       srcInfo.GetAgentProvider(),
			Title:          launchTitle,
			Options:        opts,
			WorkingDir:     dir,
			InitialMessage: flags.atomicInitialMsg,
			ForkSource:     forkSource,
			GitSetup:       flags.setup,
			Position:       p.Position,
			RequestID:      flags.requestID,
			RequestScope:   requestScopeAgentFork,
		})
		if openErr != nil {
			if !fresh && connect.CodeOf(openErr) == connect.CodeFailedPrecondition {
				return &forkHintError{
					err:  openErr,
					hint: "wait for the source agent to finish or pass --fresh to clone settings without history",
				}
			}
			return openErr
		}
		opened = true
		revealTabBestEffort(timed.ctx, w, flags.focus, result.Agent.GetId())
		if flags.postOpenMsg && !flags.wait {
			inputID := requestScopedID(flags.requestID, requestScopeAgentSend, result.Agent.GetId())
			if err := w.Call(timed.ctx, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, flags.msg.request(result.Agent.GetId(), inputID), nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if !startedOpen {
			return nil, err
		}
		receipt := retryReceipt{
			RequestID:    flags.requestID,
			Explicit:     flags.explicitRequestID,
			RetryCommand: buildAgentForkRetryCmd(inv, flags.requestID),
		}
		if opened {
			receipt.CreatedAgentID = result.Agent.GetId()
			receipt.QueryCommand = buildAgentRequestStatusCmd(result.Agent.GetId(), flags.requestID)
		}
		return nil, wrapRetryReceipt(err, retryStageDuringSend, receipt)
	}

	return finishAgentLaunch(inv, s, p, result, flags, agentLaunchMeta{
		verb:          "forked",
		buildRetryCmd: buildAgentForkRetryCmd,
	})
}

func agentInfoOnChannel(ctx context.Context, w workerCall, agentID string) (*leapmuxv1.AgentInfo, error) {
	var resp leapmuxv1.ListAgentsResponse
	if err := w.Call(ctx, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{TabIds: []string{agentID}}, &resp); err != nil {
		return nil, err
	}
	for _, a := range resp.GetAgents() {
		if a.GetId() == agentID {
			return a, nil
		}
	}
	return nil, cli.Newf(cli.CodeNotFound, "the worker has no agent %s", agentID)
}

func (s *session) placementForFork(src tabTarget) (placement, error) {
	exp, err := s.resolveExplicitPlacement()
	if err != nil {
		return placement{}, err
	}
	p := placement{
		WorkspaceID: src.WorkspaceID,
		TileID:      src.TileID,
		WorkerID:    src.WorkerID,
		Position:    exp.Position,
	}
	if exp.Inputs.ExplicitWorkerID {
		p.WorkerID = exp.Inputs.WorkerID
	}
	switch {
	case exp.HasRefTarget:
		p.WorkspaceID = exp.Inputs.WorkspaceID
		p.TileID = exp.Inputs.TileID
	case exp.Inputs.ExplicitTileID:
		// Validate --tile against --workspace when both are set and derive the
		// workspace when only --tile is set.
		got, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{WorkspaceID: true, TileID: true}, resolve.Inputs{
			WorkspaceID:         exp.Inputs.WorkspaceID,
			ExplicitWorkspaceID: exp.Inputs.ExplicitWorkspaceID,
			TileID:              exp.Inputs.TileID,
			ExplicitTileID:      true,
		})
		if err != nil {
			return placement{}, err
		}
		p.WorkspaceID = got.WorkspaceID
		p.TileID = got.TileID
	case exp.Inputs.ExplicitWorkspaceID:
		p.WorkspaceID = exp.Inputs.WorkspaceID
		p.TileID = ""
	}
	return p, nil
}
