package cmd

import (
	"context"
	"maps"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func newAgentForkCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "fork <agent>",
		Short:   "Clone an agent's provider, model, options, and working directory into a new agent tab",
		Mutates: true,
		Long: `Clone an existing agent's provider, model, effort, permission mode, provider
options, and working directory into a new agent tab.

Pass --worktree <branch> to fork the agent directly into a fresh git worktree,
or override individual settings (--model, --effort, --permission-mode, --option,
--dir, --title, --name).`,
		Next: []string{
			`leapmux agent send <id> "Try an alternative approach" --wait`,
			`leapmux agent wait <id>`,
		},
		Examples: []cli.Example{
			{Command: "leapmux agent fork 87a19d5f"},
			{Command: `leapmux agent fork 87a19d5f --worktree alt-approach --base main --message "Try an alternative implementation"`},
			{Command: `leapmux agent fork self --model opus --name reviewer`},
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
			fs.String("message", "", `queue a first message on the forked agent ("-" reads stdin)`)
			fs.String("file", "", `read the first message from this file ("-" reads stdin)`)
			fs.StringArray("attach", nil, "attach this file to the first message; repeat for more than one")
			addRequestIDFlag(fs)
			for _, name := range []string{"message", "file", "attach", "request-id"} {
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
			GitSetup:       flags.setup,
			Position:       p.Position,
			RequestID:      flags.requestID,
			RequestScope:   requestScopeAgentFork,
		})
		if openErr != nil {
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

	return finishAgentLaunch(inv, s, p, result, flags, buildAgentForkRetryCmd)
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
	spec, err := s.positionSpec()
	if err != nil {
		return placement{}, err
	}
	p := placement{
		WorkspaceID: src.WorkspaceID,
		TileID:      src.TileID,
		WorkerID:    src.WorkerID,
		Position:    spec,
	}
	hasExplicitWS := s.inv.Changed("workspace")
	hasExplicitTile := s.inv.Changed("tile")
	if !hasExplicitWS && !hasExplicitTile && (spec.kind == positionBefore || spec.kind == positionAfter) {
		refGot, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{
			TabID:       true,
			WorkspaceID: true,
			TileID:      true,
		}, resolve.Inputs{
			TabID:         spec.refID,
			ExplicitTabID: true,
		})
		if err != nil {
			return placement{}, err
		}
		p.WorkspaceID = refGot.WorkspaceID
		p.TileID = refGot.TileID
	}
	if hasExplicitWS {
		w, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
		if err != nil {
			return placement{}, err
		}
		p.WorkspaceID = w.ID
		p.TileID = ""
	}
	if hasExplicitTile {
		t, err := s.resolveOne(resolve.KindTile, stringFlag(s.inv, "tile"))
		if err != nil {
			return placement{}, err
		}
		in := resolve.Inputs{TileID: t.ID, ExplicitTileID: true}
		if hasExplicitWS {
			in.WorkspaceID, in.ExplicitWorkspaceID = p.WorkspaceID, true
		}
		got, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{WorkspaceID: true, TileID: true}, in)
		if err != nil {
			return placement{}, err
		}
		p.WorkspaceID = got.WorkspaceID
		p.TileID = got.TileID
	}
	if s.inv.Changed(workerFlag) {
		k, err := s.workerScope()
		if err != nil {
			return placement{}, err
		}
		p.WorkerID = k
	}
	return p, nil
}
