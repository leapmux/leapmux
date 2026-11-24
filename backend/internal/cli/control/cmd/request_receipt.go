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
	"github.com/leapmux/leapmux/internal/util/id"
)

// retryStage identifies where in a mutating command's lifecycle a failure
// happened, so the CLI can classify whether the server may have applied it.
type retryStage int

const (
	retryStageBeforeSend retryStage = iota
	retryStageDuringSend
	retryStageDuringWait
)

const (
	dispositionNotSent        = "not_sent"
	dispositionInProgress     = "in_progress"
	dispositionOutcomeUnknown = "outcome_unknown"
)

type retryReceipt struct {
	RequestID    string
	Explicit     bool
	QueryCommand string
	RetryCommand string
}

// ensureRequestID returns the caller's explicit --request-id when provided, or
// generates a fresh ID when omitted so every retryable command carries a
// durable receipt. The second return value reports whether the caller passed
// --request-id explicitly.
func ensureRequestID(inv *cli.Invocation) (requestID string, explicit bool, err error) {
	if inv.Changed(requestIDFlag) {
		v, err := requestIDOf(inv)
		return v, true, err
	}
	return id.Generate(), false, nil
}

// classifyRetryDisposition maps (error, stage) to one of Orca-style recovery
// dispositions: not_sent, in_progress, or outcome_unknown.
func classifyRetryDisposition(err error, stage retryStage) string {
	e := cli.FromError(err)
	switch stage {
	case retryStageBeforeSend:
		return dispositionNotSent
	case retryStageDuringWait:
		return dispositionInProgress
	case retryStageDuringSend:
		switch e.Code {
		case cli.CodeUsage, cli.CodeInvalidArgument, cli.CodeNotLoggedIn, cli.CodePermissionDenied,
			cli.CodeNotFound, cli.CodeAmbiguousTarget, cli.CodeSelfTargetRefused, cli.CodeTabBusyRefused,
			cli.CodeAgentWaiting, cli.CodeVersionMismatch:
			return dispositionNotSent
		default:
			return dispositionOutcomeUnknown
		}
	default:
		return dispositionOutcomeUnknown
	}
}

// wrapRetryReceipt attaches a structured cli.RetryReceipt and actionable hints
// to err when requestID is present. Conflict errors keep their dedicated
// request-id conflict hint.
func wrapRetryReceipt(err error, stage retryStage, r retryReceipt) error {
	if err == nil || r.RequestID == "" {
		return err
	}
	e := cli.FromError(err)
	if e.Code == cli.CodeConflict {
		if r.Explicit {
			return withRequestIDHint(e, r.RequestID)
		}
		return e
	}
	disp := classifyRetryDisposition(e, stage)
	rec := &cli.RetryReceipt{
		RequestID:    r.RequestID,
		Disposition:  disp,
		QueryCommand: r.QueryCommand,
		RetryCommand: r.RetryCommand,
	}
	e = e.WithReceipt(rec)
	e = e.WithHint("disposition: %s (request_id: %s)", disp, r.RequestID)
	if r.QueryCommand != "" {
		e = e.WithHint("check status: %s", r.QueryCommand)
	}
	if r.RetryCommand != "" && disp != dispositionNotSent {
		e = e.WithHint("safe retry: %s", r.RetryCommand)
	}
	return e
}

// shellQuote quotes s for safe POSIX shell copy-paste when it contains spaces
// or shell metacharacters.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '/' || c == ':' || c == '@' {
			continue
		}
		safe = false
		break
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func buildAgentRequestStatusCmd(agentID, requestID string) string {
	return fmt.Sprintf("leapmux agent request-status %s %s", shellQuote(agentID), shellQuote(requestID))
}

func buildAgentSendRetryCmd(inv *cli.Invocation, agentID, text string, wait bool, requestID string) string {
	parts := []string{"leapmux", "agent", "send", shellQuote(agentID)}
	if inv != nil {
		for _, path := range inv.StringArray("attach") {
			parts = append(parts, "--attach", shellQuote(path))
		}
		if inv.Changed("at") {
			parts = append(parts, "--at", shellQuote(stringFlag(inv, "at")))
		}
		if boolFlag(inv, "steer") {
			parts = append(parts, "--steer")
		}
		if boolFlag(inv, "preempt") {
			parts = append(parts, "--preempt")
		}
		if boolFlag(inv, "all") {
			parts = append(parts, "--all")
		}
		if inv.Changed("timeout") {
			parts = append(parts, "--timeout", shellQuote(inv.Command.Flags().Lookup("timeout").Value.String()))
		}
	}
	if wait {
		parts = append(parts, "--wait")
	}
	parts = append(parts, "--request-id", shellQuote(requestID))
	if text != "" {
		if strings.HasPrefix(text, "-") {
			parts = append(parts, "--")
		}
		parts = append(parts, shellQuote(text))
	}
	return strings.Join(parts, " ")
}

func appendSharedOpenRetryFlags(inv *cli.Invocation, parts []string, stringFlags []string) []string {
	for _, flagName := range stringFlags {
		if inv.Changed(flagName) {
			parts = append(parts, "--"+flagName, shellQuote(stringFlag(inv, flagName)))
		}
	}
	for _, opt := range inv.StringArray("option") {
		parts = append(parts, "--option", shellQuote(opt))
	}
	for _, path := range inv.StringArray("attach") {
		parts = append(parts, "--attach", shellQuote(path))
	}
	for _, boolName := range []string{"first", "last", "focus", "no-focus", "wait", "all"} {
		if inv.Changed(boolName) && boolFlag(inv, boolName) {
			parts = append(parts, "--"+boolName)
		}
	}
	if inv.Changed("timeout") {
		parts = append(parts, "--timeout", shellQuote(inv.Command.Flags().Lookup("timeout").Value.String()))
	}
	return parts
}

func buildAgentOpenRetryCmd(inv *cli.Invocation, requestID string) string {
	parts := []string{"leapmux", "agent", "open"}
	parts = appendSharedOpenRetryFlags(inv, parts, []string{
		"provider", "model", "effort", "permission-mode", "title", "name",
		"message", "file", "resume", "dir", "worktree", "use-worktree",
		"checkout", "new-branch", "base", "workspace", "tile", "worker",
		"before", "after",
	})
	parts = append(parts, "--request-id", shellQuote(requestID))
	return strings.Join(parts, " ")
}

func buildAgentForkRetryCmd(inv *cli.Invocation, requestID string) string {
	parts := []string{"leapmux", "agent", "fork", shellQuote(inv.Arg("agent"))}
	parts = appendSharedOpenRetryFlags(inv, parts, []string{
		"model", "effort", "permission-mode", "title", "name",
		"message", "file", "dir", "worktree", "use-worktree",
		"checkout", "new-branch", "base", "workspace", "tile", "worker",
		"before", "after",
	})
	parts = append(parts, "--request-id", shellQuote(requestID))
	return strings.Join(parts, " ")
}

func buildTerminalRunRetryCmd(inv *cli.Invocation, terminalID, command string, wait bool, requestID string) string {
	parts := []string{"leapmux", "terminal", "run", shellQuote(terminalID)}
	if wait {
		parts = append(parts, "--wait")
	}
	if inv != nil {
		if boolFlag(inv, "exit") {
			parts = append(parts, "--exit")
		}
		if inv.Changed("text") {
			parts = append(parts, "--text", shellQuote(stringFlag(inv, "text")))
		}
		if inv.Changed("regex") {
			parts = append(parts, "--regex", shellQuote(stringFlag(inv, "regex")))
		}
		if boolFlag(inv, "scrollback") {
			parts = append(parts, "--scrollback")
		}
		if inv.Changed("timeout") {
			parts = append(parts, "--timeout", shellQuote(inv.Command.Flags().Lookup("timeout").Value.String()))
		}
	}
	parts = append(parts, "--request-id", shellQuote(requestID))
	if strings.HasPrefix(command, "-") {
		parts = append(parts, "--")
	}
	parts = append(parts, shellQuote(command))
	return strings.Join(parts, " ")
}

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
		if _, err := fmt.Fprintf(w, "accepted_seq:%d\n", r.AcceptedSeq); err != nil {
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
		sanitized := make([]string, len(r.Replies))
		for i, reply := range r.Replies {
			sanitized[i] = cli.SanitizeTerminalText(reply)
		}
		if _, err := fmt.Fprintf(w, "\n%s\n", strings.Join(sanitized, "\n\n")); err != nil {
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
	var allRows []*leapmuxv1.AgentChatMessage
	if err := withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
			&leapmuxv1.ListAgentInputQueueRequest{AgentId: got.ID, ReplyInputId: inputID}, &queueResp); err != nil {
			return err
		}
		var replyCursor int64
		for {
			req := &leapmuxv1.ListAgentMessagesRequest{
				AgentId:          got.ID,
				Anchor:           leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST,
				ReplyInputId:     inputID,
				ReplyCursor:      replyCursor,
				IncludeReplyText: true,
				Limit:            200,
			}
			var page leapmuxv1.ListAgentMessagesResponse
			if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, req, &page); err != nil {
				return err
			}
			batch := page.GetMessages()
			allRows = append(allRows, batch...)
			nextCursor := page.GetNextReplyCursor()
			if !page.GetHasMore() || nextCursor <= replyCursor || len(batch) == 0 {
				break
			}
			replyCursor = nextCursor
		}
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

	// Check if still in queue.
	for _, item := range queueResp.GetSnapshot().GetItems() {
		if item.GetId() != inputID {
			continue
		}
		res.QueueState = enumToken(item.GetState())
		res.QueueError = item.GetError()
		switch item.GetState() {
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
		res.Replies = repliesAfter(allRows, allReplies)
		if res.Status == "not_found" {
			res.Status = "completed"
		}
	}

	return res, nil
}
