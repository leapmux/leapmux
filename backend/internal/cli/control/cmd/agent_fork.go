package cmd

import (
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
	bounded := s
	if wait {
		bounded = s.bounded()
		defer bounded.Close()
	}

	srcTarget, err := bounded.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true, WorkspaceID: true, TileID: true})
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(bounded.ctx, s.c, srcTarget.WorkerID); err != nil {
		return nil, err
	}
	srcInfo, err := bounded.agentInfo(srcTarget)
	if err != nil {
		return nil, err
	}

	// Default placement lands beside the source agent in the same workspace/tile/worker
	// unless overridden explicitly by placement flags.
	p, err := bounded.placementForFork(srcTarget)
	if err != nil {
		return nil, err
	}
	if slug != "" {
		if err := bounded.ensureUniqueAgentSlug(slug, requestScopedID(requestID, requestScopeAgentOpen, p.WorkerID)); err != nil {
			return nil, err
		}
	}

	if title == "" && srcInfo.GetTitle() != "" {
		title = srcInfo.GetTitle() + " (fork)"
	}

	opts := clonedAgentOptions(srcInfo.GetOptionGroups())
	maps.Copy(opts, overrideOpts)

	dir := srcInfo.GetWorkingDir()
	if inv.Changed("dir") {
		dir = stringFlag(inv, "dir")
	}
	if selector := setup.GetUseWorktree(); selector != nil {
		if inv.Changed("dir") {
			dir = stringFlag(inv, "dir")
		} else if selector.GetBranch() == "" {
			dir = ""
		}
	}

	if p.WorkerID != srcTarget.WorkerID {
		if err := maybePreflightWorker(bounded.ctx, s.c, p.WorkerID); err != nil {
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
	var result *openAgentResult
	err = withWorkerChannel(bounded.ctx, s.c, p.WorkerID, func(w workerCall) error {
		result, err = openAgentAndAddTab(bounded.ctx, s.c, w, openAgentArgs{
			WorkspaceID:    p.WorkspaceID,
			WorkerID:       p.WorkerID,
			TileID:         p.TileID,
			Provider:       srcInfo.GetAgentProvider(),
			Title:          title,
			Options:        opts,
			WorkingDir:     dir,
			InitialMessage: atomicInitialMessage,
			GitSetup:       setup,
			Position:       p.Position,
			RequestID:      requestID,
		})
		if err != nil {
			return err
		}
		if focus {
			_ = w.Call(bounded.ctx, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, &leapmuxv1.RevealTabRequest{TabId: result.Agent.GetId()}, nil)
		}
		return nil
	})
	if err != nil {
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
	if postOpenMsg {
		inputID := requestScopedID(requestID, requestScopeAgentSend, agentTarget.ID)
		if !wait {
			if _, err := enqueueAgentInput(bounded.ctx, s.c, agentTarget, msg.request(agentTarget.ID, inputID)); err != nil {
				return nil, wrapRetryReceipt(err, retryStageDuringSend, retryReceipt{
					RequestID:    requestID,
					Explicit:     explicitRequestID,
					QueryCommand: buildAgentRequestStatusCmd(agentTarget.ID, requestID),
					RetryCommand: buildAgentForkRetryCmd(inv, requestID),
				})
			}
		} else {
			var (
				replyRes agentReplyResult
				outcome  replyWaitOutcome
				wErr     error
			)
			if schemaSpec != nil {
				replyRes, outcome, _, wErr = sendAndWaitWithSchema(inv, s, agentTarget, *msg, requestID, inputID, sendDeliveryNormal, boolFlag(inv, "all"), timeout, schemaSpec)
			} else {
				replyRes, outcome, wErr = sendAndWait(s, agentTarget, *msg, inputID, boolFlag(inv, "all"), timeout)
			}
			if wErr != nil {
				return nil, wrapRetryReceipt(wErr, retryStageDuringWait, retryReceipt{
					RequestID:    requestID,
					Explicit:     explicitRequestID,
					QueryCommand: buildAgentRequestStatusCmd(agentTarget.ID, requestID),
					RetryCommand: buildAgentForkRetryCmd(inv, requestID),
				})
			}
			replyRes.RequestID = requestID
			if outcome == replyWaitSettled {
				return replyRes, nil
			}
			return nil, agentReplyEnded(inv.Printer, replyRes, outcome, timeout)
		}
	} else if wait {
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
		p.TileID = t.ID
		if !s.inv.Changed("workspace") {
			got, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{WorkspaceID: true, TileID: true}, resolve.Inputs{TileID: t.ID, ExplicitTileID: true})
			if err != nil {
				return placement{}, err
			}
			p.WorkspaceID = got.WorkspaceID
		}
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
