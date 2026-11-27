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

// --- agent retry-turn ---

func newAgentRetryTurnCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "retry-turn <agent>",
		Short:   "Stop a slow turn and retry it on the offered faster model",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux agent retry-turn 87a19d5f"},
			{Command: "leapmux agent retry-turn 87a19d5f --turn-id turn-1 --model gpt-5-fast"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("turn-id", "", "turn id from the retry offer (default: auto-read from live session info)")
			fs.String("model", "", "model id from the retry offer (default: auto-read from live session info)")
			addTargetWorkspaceFlag(fs)
		},
		FlagComplete: map[string]cobra.CompletionFunc{
			"model": completeAgentOptionFlag("model"),
		},
		Complete: completeTargets,
		Run:      runAgentRetryTurn,
	})
}

type agentRetryTurnResult struct {
	ID     string `json:"id"`
	TurnID string `json:"turn_id"`
	Model  string `json:"model"`
}

func runAgentRetryTurn(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	turnID := strings.TrimSpace(stringFlag(inv, "turn-id"))
	model := strings.TrimSpace(stringFlag(inv, "model"))
	if err := withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		if turnID == "" || model == "" {
			var infoResp leapmuxv1.GetAgentSessionInfoResponse
			if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceGetAgentSessionInfoProcedure,
				&leapmuxv1.GetAgentSessionInfoRequest{AgentId: got.ID}, &infoResp); err != nil {
				return err
			}
			info, err := parseAgentSessionInfo(infoResp.GetSessionInfoJson())
			if err != nil {
				return err
			}
			offerTurnID, offerModel := extractTurnRetryOffer(info)
			if turnID == "" {
				turnID = offerTurnID
			}
			if model == "" {
				model = offerModel
			}
		}
		if turnID == "" || model == "" {
			return cli.Newf(cli.CodeNotFound, "agent %s has no active turn retry offer", got.ID).
				WithHint("pass --turn-id and --model explicitly if you already know the offer")
		}
		return w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceRetryAgentTurnProcedure,
			&leapmuxv1.RetryAgentTurnRequest{AgentId: got.ID, TurnId: turnID, Model: model}, nil)
	}); err != nil {
		return nil, err
	}
	return agentRetryTurnResult{ID: got.ID, TurnID: turnID, Model: model}, nil
}

// --- agent approve-review ---

func newAgentApproveReviewCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "approve-review <agent>",
		Short:   "Record approval for an action denied by the automatic reviewer",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux agent approve-review 87a19d5f --message-id msg-1 --review-id rev-1"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("message-id", "", "transcript message id containing the denied review")
			fs.String("review-id", "", "review id within that message")
			cli.MarkRequired(fs, "message-id")
			cli.MarkRequired(fs, "review-id")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runAgentApproveReview,
	})
}

type agentApproveReviewResult struct {
	ID        string `json:"id"`
	MessageID string `json:"message_id"`
	ReviewID  string `json:"review_id"`
}

func runAgentApproveReview(inv *cli.Invocation) (any, error) {
	msgID := strings.TrimSpace(stringFlag(inv, "message-id"))
	if msgID == "" {
		return nil, cli.Usagef("--message-id is empty")
	}
	revID := strings.TrimSpace(stringFlag(inv, "review-id"))
	if revID == "" {
		return nil, cli.Usagef("--review-id is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceApproveAutoReviewDenialProcedure,
		&leapmuxv1.ApproveAutoReviewDenialRequest{AgentId: got.ID, MessageId: msgID, ReviewId: revID}, nil); err != nil {
		return nil, err
	}
	return agentApproveReviewResult{ID: got.ID, MessageID: msgID, ReviewID: revID}, nil
}
