package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func newAgentStartupCommand() *cobra.Command {
	addFlags := func(fs *pflag.FlagSet) {
		fs.String("prompt-id", "", "prompt id to answer (default: the agent's current pending startup prompt)")
		addTargetWorkspaceFlag(fs)
	}
	return cli.Group("startup", "Respond to a pending agent startup prompt (for example, OS Keychain confirmation)",
		cli.NewLeaf(cli.Leaf{
			Use:     "approve <agent>",
			Short:   "Proceed with a pending agent startup prompt",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux agent startup approve 87a19d5f"},
			},
			Flags:    addFlags,
			Complete: completeTargets,
			Run: func(inv *cli.Invocation) (any, error) {
				return runAgentStartupRespond(inv, true)
			},
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "cancel <agent>",
			Short:       "Cancel a pending agent startup prompt",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: "leapmux agent startup cancel 87a19d5f"},
			},
			Flags:    addFlags,
			Complete: completeTargets,
			Run: func(inv *cli.Invocation) (any, error) {
				return runAgentStartupRespond(inv, false)
			},
		}),
	)
}

type agentStartupResult struct {
	ID       string `json:"id"`
	PromptID string `json:"prompt_id"`
	Proceed  bool   `json:"proceed"`
}

func runAgentStartupRespond(inv *cli.Invocation, proceed bool) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	promptID := strings.TrimSpace(stringFlag(inv, "prompt-id"))
	if err := withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		if promptID == "" {
			var listResp leapmuxv1.ListAgentsResponse
			if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
				&leapmuxv1.ListAgentsRequest{TabIds: []string{got.ID}}, &listResp); err != nil {
				return err
			}
			var info *leapmuxv1.AgentInfo
			for _, a := range listResp.GetAgents() {
				if a.GetId() == got.ID {
					info = a
					break
				}
			}
			if info == nil {
				return cli.Newf(cli.CodeNotFound, "the worker has no agent %s", got.ID)
			}
			prompt := info.GetStartupPrompt()
			if prompt == nil || prompt.GetPromptId() == "" {
				return cli.Newf(cli.CodeNotFound, "agent %s has no pending startup prompt", got.ID)
			}
			promptID = prompt.GetPromptId()
		}
		return w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure,
			&leapmuxv1.RespondAgentStartupPromptRequest{AgentId: got.ID, PromptId: promptID, Proceed: proceed}, nil)
	}); err != nil {
		return nil, err
	}
	return agentStartupResult{ID: got.ID, PromptID: promptID, Proceed: proceed}, nil
}
