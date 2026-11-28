package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func newAgentRequestStatusCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "request-status <agent> <request-id>",
		Short: "Query the durable delivery and reply status of a keyed agent send request",
		Long: `Inspect the durable Worker state of a previous "agent send" by its --request-id
(whether explicit or auto-generated).

Reports whether the request is not_found, queued, dispatching, waiting_for_user,
in_progress, completed, interrupted, failed, or unproved, together with any
replies recorded for that input.`,
		Examples: []cli.Example{
			{Command: "leapmux agent request-status 87a19d5f release-2026-11-19"},
			{Command: "leapmux agent request-status 87a19d5f 550e8400-e29b-41d4-a716-446655440000 --json"},
		},
		Flags: func(fs *pflag.FlagSet) {
			addTargetWorkspaceFlag(fs)
			fs.Bool("all", false, "include every reply message for this request, not only the last one")
		},
		Complete: completeTargets,
		Run:      runAgentRequestStatus,
	})
}

type agentRequestStatusResult struct {
	AgentID        string   `json:"agent_id"`
	RequestID      string   `json:"request_id"`
	InputID        string   `json:"input_id"`
	Status         string   `json:"status"`
	TurnOutcome    string   `json:"turn_outcome,omitempty"`
	WaitingForUser bool     `json:"waiting_for_user,omitempty"`
	AcceptedSeq    int64    `json:"accepted_seq,omitempty"`
	QueueState     string   `json:"queue_state,omitempty"`
	QueueError     string   `json:"queue_error,omitempty"`
	Replies        []string `json:"replies,omitempty"`
}

func (r agentRequestStatusResult) QuietLines() []string {
	return []string{r.Status}
}

func (r agentRequestStatusResult) RenderText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "status:      %s\nagent_id:    %s\nrequest_id:  %s\ninput_id:    %s\n",
		cli.SanitizeSingleLine(r.Status),
		cli.SanitizeSingleLine(r.AgentID),
		cli.SanitizeSingleLine(r.RequestID),
		cli.SanitizeSingleLine(r.InputID),
	); err != nil {
		return err
	}
	if r.TurnOutcome != "" {
		if _, err := fmt.Fprintf(w, "outcome:     %s\n", cli.SanitizeSingleLine(r.TurnOutcome)); err != nil {
			return err
		}
	}
	if r.AcceptedSeq > 0 {
		if _, err := fmt.Fprintf(w, "accepted_seq: %d\n", r.AcceptedSeq); err != nil {
			return err
		}
	}
	if r.WaitingForUser {
		if _, err := fmt.Fprintln(w, "waiting:     waiting_for_user"); err != nil {
			return err
		}
	}
	if r.QueueError != "" {
		if _, err := fmt.Fprintf(w, "queue_error: %s\n", cli.SanitizeSingleLine(r.QueueError)); err != nil {
			return err
		}
	}
	if len(r.Replies) > 0 {
		if _, err := fmt.Fprintf(w, "\n%s\n", formatSanitizedReplies(r.Replies)); err != nil {
			return err
		}
	}
	return nil
}

func runAgentRequestStatus(inv *cli.Invocation) (any, error) {
	requestID := strings.TrimSpace(inv.Arg("request-id"))
	if requestID == "" {
		return nil, cli.Usagef("request-id is empty")
	}
	allReplies := boolFlag(inv, "all")
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	inputID := requestScopedID(requestID, requestScopeAgentSend, got.ID)

	var queueResp leapmuxv1.ListAgentInputQueueResponse
	var queuedItem *leapmuxv1.QueuedAgentInput
	var allRows []*leapmuxv1.AgentChatMessage
	if err := withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
			&leapmuxv1.ListAgentInputQueueRequest{AgentId: got.ID, ReplyInputId: inputID}, &queueResp); err != nil {
			return err
		}
		for _, item := range queueResp.GetSnapshot().GetItems() {
			if item.GetId() == inputID {
				queuedItem = item
				return nil
			}
		}
		rows, err := pageExactInputReply(func(req *leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
			req.AgentId = got.ID
			req.ReplyInputId = inputID
			req.IncludeReplyText = true
			var page leapmuxv1.ListAgentMessagesResponse
			if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, req, &page); err != nil {
				return nil, err
			}
			return &page, nil
		})
		if err != nil {
			return err
		}
		allRows = rows
		return nil
	}); err != nil {
		return nil, err
	}

	res := agentRequestStatusResult{
		AgentID:   got.ID,
		RequestID: requestID,
		InputID:   inputID,
		Status:    "not_found",
	}

	if queuedItem != nil {
		res.QueueState = enumToken(queuedItem.GetState())
		res.QueueError = queuedItem.GetError()
		switch queuedItem.GetState() {
		case leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED:
			res.Status = "failed"
		case leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING:
			res.Status = "dispatching"
		case leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO:
			res.Status = "in_progress"
		case leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN:
			res.Status = "outcome_unknown"
		default:
			res.Status = "queued"
		}
		return res, nil
	}

	if ir := queueResp.GetInputReply(); ir != nil {
		res.AcceptedSeq = ir.GetAcceptedSeq()
		res.WaitingForUser = ir.GetWaitingForUser()
		if ir.GetOutcome() != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED {
			res.TurnOutcome = enumToken(ir.GetOutcome())
		}
		switch ir.GetOutcome() {
		case leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED:
			res.Status = "completed"
		case leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED:
			res.Status = "interrupted"
		case leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED:
			res.Status = "failed"
		case leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED:
			res.Status = "unproved"
		default:
			switch {
			case ir.GetWaitingForUser():
				res.Status = "waiting_for_user"
			case ir.GetAcceptedSeq() > 0:
				res.Status = "in_progress"
			}
		}
	}

	if len(allRows) > 0 {
		res.Replies = replyTexts(allRows, allReplies)
		if res.Status == "not_found" {
			res.Status = "completed"
		}
	}

	return res, nil
}
