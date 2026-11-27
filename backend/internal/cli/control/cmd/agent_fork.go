package cmd

import (
	"context"
	"maps"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
	if err := validateFocusFlags(inv); err != nil {
		return nil, err
	}
	wait := boolFlag(inv, "wait")
	if !wait {
		for _, name := range []string{"all", "timeout", outputSchemaFlag, schemaRetriesFlag} {
			if inv.Changed(name) {
				return nil, cli.Usagef("--%s needs --wait", name)
			}
		}
	}
	timeout, err := waitTimeoutFlag(inv)
	if err != nil {
		return nil, err
	}
	title := stringFlag(inv, "title")
	slug := ""
	if inv.Changed("name") {
		if inv.Changed("title") {
			return nil, cli.Usagef("give --name or --title, not both")
		}
		slug = strings.TrimSpace(stringFlag(inv, "name"))
		if err := validateAgentSlug(slug); err != nil {
			return nil, err
		}
		title = slug
	}
	overrideOpts, err := buildAgentSetOptions(
		stringFlag(inv, "model"),
		stringFlag(inv, "effort"),
		stringFlag(inv, "permission-mode"),
		inv.StringArray("option"),
	)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	msg, err := readAgentOpenMessage(inv)
	if err != nil {
		return nil, err
	}
	if wait && boolFlag(inv, "all") && msg == nil {
		return nil, cli.Usagef("--all needs --message, --file, or --attach with --wait")
	}
	promptUsedStdin := (inv.Changed("message") && stringFlag(inv, "message") == "-") || (inv.Changed("file") && stringFlag(inv, "file") == "-")
	schemaSpec, err := readOutputSchemaSpec(inv, wait, msg != nil, promptUsedStdin)
	if err != nil {
		return nil, err
	}
	setup, err := gitSetup(inv)
	if err != nil {
		return nil, err
	}
	requestID, explicitRequestID, err := ensureRequestID(inv)
	if err != nil {
		return nil, err
	}

	open := openSession
	if wait {
		open = openStreamSession
	}
	s, err := open(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	timed := s
	if wait {
		timed = s.bounded()
		defer timed.Close()
	}

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
	if slug != "" {
		if err := timed.ensureUniqueAgentSlug(slug, requestScopedID(requestID, requestScopeAgentFork, p.WorkerID)); err != nil {
			return nil, err
		}
	}
	if err := maybePreflightWorker(timed.ctx, s.c, srcTarget.WorkerID); err != nil {
		return nil, err
	}
	if p.WorkerID != srcTarget.WorkerID {
		if err := maybePreflightWorker(timed.ctx, s.c, p.WorkerID); err != nil {
			return nil, err
		}
	}

	resolveLaunchInputs := func(info *leapmuxv1.AgentInfo) (string, map[string]string, string, error) {
		launchTitle := title
		if launchTitle == "" && info.GetTitle() != "" {
			launchTitle = info.GetTitle() + " (fork)"
		}
		opts := clonedAgentOptions(info.GetOptionGroups())
		maps.Copy(opts, overrideOpts)

		var dir string
		if selector := setup.GetUseWorktree(); selector != nil {
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

	var atomicInitialMessage *string
	postOpenMsg := false
	if msg != nil {
		if len(msg.attachments) == 0 && !wait {
			text := msg.text
			atomicInitialMessage = &text
		} else {
			postOpenMsg = true
		}
	}

	focus := shouldFocusNewTab(inv)
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
			InitialMessage: atomicInitialMessage,
			GitSetup:       setup,
			Position:       p.Position,
			RequestID:      requestID,
			RequestScope:   requestScopeAgentFork,
		})
		if openErr != nil {
			return openErr
		}
		opened = true
		if focus {
			_ = w.Call(timed.ctx, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, &leapmuxv1.RevealTabRequest{TabId: result.Agent.GetId()}, nil)
		}
		if postOpenMsg && !wait {
			inputID := requestScopedID(requestID, requestScopeAgentSend, result.Agent.GetId())
			if err := w.Call(timed.ctx, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, msg.request(result.Agent.GetId(), inputID), nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if !startedOpen {
			return nil, err
		}
		if opened {
			return nil, wrapRetryReceipt(err, retryStageDuringSend, retryReceipt{
				RequestID:      requestID,
				Explicit:       explicitRequestID,
				CreatedAgentID: result.Agent.GetId(),
				QueryCommand:   buildAgentRequestStatusCmd(result.Agent.GetId(), requestID),
				RetryCommand:   buildAgentForkRetryCmd(inv, requestID),
			})
		}
		return nil, wrapRetryReceipt(err, retryStageDuringSend, retryReceipt{
			RequestID:    requestID,
			Explicit:     explicitRequestID,
			RetryCommand: buildAgentForkRetryCmd(inv, requestID),
		})
	}

	agentTarget := tabTarget{
		ID:          result.Agent.GetId(),
		Type:        leapmuxv1.TabType_TAB_TYPE_AGENT,
		WorkerID:    p.WorkerID,
		WorkspaceID: p.WorkspaceID,
		TileID:      result.TileID,
	}
	if postOpenMsg && wait {
		inputID := requestScopedID(requestID, requestScopeAgentSend, agentTarget.ID)
		replyRes, outcome, msgStage, wErr := sendAndWait(s, sendAndWaitRequest{
			target:    agentTarget,
			message:   *msg,
			requestID: requestID,
			inputID:   inputID,
			delivery:  sendDeliveryNormal,
			all:       boolFlag(inv, "all"),
			timeout:   timeout,
			schema:    schemaSpec,
		})
		if wErr != nil {
			return nil, wrapRetryReceipt(wErr, msgStage, retryReceipt{
				RequestID:      requestID,
				Explicit:       explicitRequestID,
				CreatedAgentID: agentTarget.ID,
				QueryCommand:   buildAgentRequestStatusCmd(agentTarget.ID, requestID),
				RetryCommand:   buildAgentForkRetryCmd(inv, requestID),
			})
		}
		replyRes.RequestID = requestID
		if outcome == replyWaitSettled {
			return replyRes, nil
		}
		return nil, agentReplyEnded(inv.Printer, replyRes, outcome, timeout)
	} else if !postOpenMsg && wait {
		return waitNewAgentSettled(inv, s, agentTarget, timeout, openedTab{
			ID:          result.Agent.GetId(),
			Type:        "agent",
			Title:       result.Agent.GetTitle(),
			WorkspaceID: p.WorkspaceID,
			WorkerID:    p.WorkerID,
			TileID:      result.TileID,
			Position:    result.Position,
			RequestID:   requestID,
		})
	}

	return openedTab{
		ID:          result.Agent.GetId(),
		Type:        "agent",
		Title:       result.Agent.GetTitle(),
		WorkspaceID: p.WorkspaceID,
		WorkerID:    p.WorkerID,
		TileID:      result.TileID,
		Position:    result.Position,
		RequestID:   requestID,
	}, nil
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
	if s.inv.Changed("workspace") {
		w, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
		if err != nil {
			return placement{}, err
		}
		p.WorkspaceID = w.ID
		p.TileID = ""
	}
	if s.inv.Changed("tile") {
		t, err := s.resolveOne(resolve.KindTile, stringFlag(s.inv, "tile"))
		if err != nil {
			return placement{}, err
		}
		in := resolve.Inputs{TileID: t.ID, ExplicitTileID: true}
		if s.inv.Changed("workspace") {
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
