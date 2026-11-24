package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// --- agent startup ---

func newAgentStartupCommand() *cobra.Command {
	return cli.Group("startup", "Respond to a pending agent startup prompt (for example, OS Keychain confirmation)",
		cli.NewLeaf(cli.Leaf{
			Use:     "approve <agent>",
			Short:   "Proceed with a pending agent startup prompt",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux agent startup approve 87a19d5f"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("prompt-id", "", "prompt id to answer (default: the agent's current pending startup prompt)")
				addTargetWorkspaceFlag(fs)
			},
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
			Flags: func(fs *pflag.FlagSet) {
				fs.String("prompt-id", "", "prompt id to answer (default: the agent's current pending startup prompt)")
				addTargetWorkspaceFlag(fs)
			},
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
	if promptID == "" {
		info, err := s.agentInfo(got)
		if err != nil {
			return nil, err
		}
		prompt := info.GetStartupPrompt()
		if prompt == nil || prompt.GetPromptId() == "" {
			return nil, cli.Newf(cli.CodeNotFound, "agent %s has no pending startup prompt", got.ID)
		}
		promptID = prompt.GetPromptId()
	}
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure,
		&leapmuxv1.RespondAgentStartupPromptRequest{AgentId: got.ID, PromptId: promptID, Proceed: proceed}, nil); err != nil {
		return nil, err
	}
	return agentStartupResult{ID: got.ID, PromptID: promptID, Proceed: proceed}, nil
}

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
				&leapmuxv1.GetAgentSessionInfoRequest{AgentId: got.ID}, &infoResp); err == nil {
				var info map[string]any
				if json.Unmarshal([]byte(infoResp.GetSessionInfoJson()), &info) == nil {
					if offer, ok := info[contracts.SessionInfoKeyTurnRetryOffer].(map[string]any); ok {
						if turnID == "" {
							turnID, _ = offer[contracts.TurnRetryOfferFieldTurnId].(string)
						}
						if model == "" {
							model, _ = offer[contracts.TurnRetryOfferFieldModel].(string)
						}
					}
				}
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
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	msgID := strings.TrimSpace(stringFlag(inv, "message-id"))
	revID := strings.TrimSpace(stringFlag(inv, "review-id"))
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceApproveAutoReviewDenialProcedure,
		&leapmuxv1.ApproveAutoReviewDenialRequest{AgentId: got.ID, MessageId: msgID, ReviewId: revID}, nil); err != nil {
		return nil, err
	}
	return agentApproveReviewResult{ID: got.ID, MessageID: msgID, ReviewID: revID}, nil
}

// --- agent message / span / marks / export ---

func newAgentMessageCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "message <agent> <seq>",
		Short:            "Fetch a single transcript message by sequence number",
		QuietUnsupported: true,
		Args:             map[string]string{"seq": "Positive message sequence number."},
		Examples: []cli.Example{
			{Command: "leapmux agent message 87a19d5f 42"},
			{Command: "leapmux agent message self 42 --json"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("raw", false, "print raw provider JSON payload in text mode")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runAgentMessage,
	})
}

func runAgentMessage(inv *cli.Invocation) (any, error) {
	seq, err := strconv.ParseInt(strings.TrimSpace(inv.Arg("seq")), 10, 64)
	if err != nil || seq <= 0 {
		return nil, cli.Usagef("<seq> must be a positive integer")
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
	var resp leapmuxv1.GetAgentMessageResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceGetAgentMessageProcedure,
		&leapmuxv1.GetAgentMessageRequest{AgentId: got.ID, Seq: seq}, &resp); err != nil {
		return nil, err
	}
	if resp.GetMessage() == nil {
		return nil, cli.Newf(cli.CodeNotFound, "agent %s has no message at seq %d", got.ID, seq)
	}
	msg := renderAgentMessage(resp.GetMessage())
	msg.rawText = boolFlag(inv, "raw")
	msg.fullText = true
	return msg, nil
}

func newAgentSpanCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "span <agent> <span-id>",
		Short:            "Fetch all transcript messages belonging to a tool or subagent span",
		QuietUnsupported: true,
		Args:             map[string]string{"span-id": "Tool or subagent span id (for example, toolu_01...)."},
		Examples: []cli.Example{
			{Command: "leapmux agent span 87a19d5f toolu_01AbCd"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("session-id", "", "optional provider session id to disambiguate spans across sessions")
			fs.Bool("raw", false, "print raw provider JSON payloads in text mode")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runAgentSpan,
	})
}

func runAgentSpan(inv *cli.Invocation) (any, error) {
	spanID := strings.TrimSpace(inv.Arg("span-id"))
	if spanID == "" {
		return nil, cli.Usagef("<span-id> is empty")
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
	var resp leapmuxv1.GetAgentSpanMessagesResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceGetAgentSpanMessagesProcedure,
		&leapmuxv1.GetAgentSpanMessagesRequest{
			AgentId:        got.ID,
			SpanId:         spanID,
			AgentSessionId: strings.TrimSpace(stringFlag(inv, "session-id")),
		}, &resp); err != nil {
		return nil, err
	}
	rawMode := boolFlag(inv, "raw")
	rows := make([]agentMessage, 0, len(resp.GetMessages()))
	for _, m := range resp.GetMessages() {
		msg := renderAgentMessage(m)
		msg.rawText = rawMode
		msg.fullText = true
		rows = append(rows, msg)
	}
	return agentMessageList{Messages: rows}, nil
}

func newAgentMarksCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "marks [agent]",
		Short: "List transcript jump marks (user prompts, plans, errors) and sequence bounds (default: this agent)",
		Examples: []cli.Example{
			{Command: "leapmux agent marks"},
			{Command: "leapmux agent marks 87a19d5f"},
		},
		Flags:    addTargetWorkspaceFlag,
		Complete: completeTargets,
		Run:      runAgentMarks,
	})
}

type agentMarkRow struct {
	Seq  int64  `json:"seq" table:"SEQ"`
	Type string `json:"type" table:"TYPE"`
}

type agentMarksResult struct {
	AgentID string         `json:"agent_id"`
	MinSeq  *int64         `json:"min_seq,omitempty"`
	MaxSeq  *int64         `json:"max_seq,omitempty"`
	Marks   []agentMarkRow `json:"marks"`
}

func (r agentMarksResult) QuietLines() []string {
	out := make([]string, 0, len(r.Marks))
	for _, m := range r.Marks {
		out = append(out, strconv.FormatInt(m.Seq, 10))
	}
	return out
}

func runAgentMarks(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListMessageMarksResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListMessageMarksProcedure,
		&leapmuxv1.ListMessageMarksRequest{AgentId: got.ID}, &resp); err != nil {
		return nil, err
	}
	rows := make([]agentMarkRow, 0, len(resp.GetMarks()))
	for _, m := range resp.GetMarks() {
		rows = append(rows, agentMarkRow{Seq: m.GetSeq(), Type: enumToken(m.GetType())})
	}
	return agentMarksResult{
		AgentID: got.ID,
		MinSeq:  resp.MinSeq,
		MaxSeq:  resp.MaxSeq,
		Marks:   rows,
	}, nil
}

func newAgentExportCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "export [agent]",
		Short:            "Export an agent's complete transcript as Markdown, text, or JSON (default: this agent)",
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux agent export 87a19d5f --output transcript.md"},
			{Command: "leapmux agent export self --style text"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("style", "markdown", "export style")
			cli.SetEnum(fs, "style", "markdown", "text", "json")
			fs.StringP("output", "o", "", "write exported transcript to this file instead of stdout")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runAgentExport,
	})
}

type agentExportResult struct {
	AgentID  string `json:"agent_id"`
	Messages int    `json:"messages"`
	Output   string `json:"output,omitempty"`
}

func runAgentExport(inv *cli.Invocation) (any, error) {
	style := strings.ToLower(strings.TrimSpace(stringFlag(inv, "style")))
	switch style {
	case "", "markdown", "text", "json":
	default:
		return nil, cli.Usagef("--style must be one of markdown, text, json; got %q", style)
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
	var all []*leapmuxv1.AgentChatMessage
	if err := withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		var cursor int64
		for {
			req := &leapmuxv1.ListAgentMessagesRequest{
				AgentId:          got.ID,
				Limit:            contracts.MessagePageLimit,
				IncludeReplyText: true,
			}
			if cursor == 0 {
				req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST
			} else {
				req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER
				req.CursorSeq = cursor
			}
			var resp leapmuxv1.ListAgentMessagesResponse
			if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, req, &resp); err != nil {
				return err
			}
			msgs := resp.GetMessages()
			all = append(all, msgs...)
			if !resp.GetHasMore() || len(msgs) == 0 {
				break
			}
			next := msgs[len(msgs)-1].GetSeq()
			if next <= cursor {
				break
			}
			cursor = next
		}
		return nil
	}); err != nil {
		return nil, err
	}
	rows := make([]agentMessage, 0, len(all))
	for _, m := range all {
		msg := renderAgentMessage(m)
		msg.fullText = true
		msg.markdown = style == "markdown"
		rows = append(rows, msg)
	}
	outPath := strings.TrimSpace(stringFlag(inv, "output"))
	if outPath == "" && (inv.Printer.Format == cli.FormatJSON || inv.Printer.Format == cli.FormatNDJSON) {
		return agentMessageList{Messages: rows}, nil
	}
	var buf bytes.Buffer
	switch style {
	case "json":
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(agentMessageList{Messages: rows}); err != nil {
			return nil, err
		}
	default:
		list := agentMessageList{Messages: rows, markdown: style == "markdown"}
		if err := list.RenderText(&buf); err != nil {
			return nil, err
		}
	}
	if outPath != "" {
		if err := os.WriteFile(outPath, buf.Bytes(), 0o600); err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "write --output")
		}
		return agentExportResult{AgentID: got.ID, Messages: len(rows), Output: outPath}, nil
	}
	if _, err := inv.Printer.Out.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	return nil, nil
}

// --- agent recoveries & recover ---

func newAgentRecoveriesCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "recoveries",
		Short: "List agents whose input queue is paused waiting for process recovery approval",
		Examples: []cli.Example{
			{Command: "leapmux agent recoveries"},
			{Command: "leapmux agent recoveries --provider claude"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("provider", "", "filter recovery targets by agent provider")
			addWorkerFlag(fs)
		},
		Run: runAgentRecoveries,
	})
}

type agentRecoveryRow struct {
	AgentID          string `json:"agent_id" table:"AGENT,short"`
	Title            string `json:"title" table:"TITLE"`
	Provider         string `json:"provider" table:"PROVIDER"`
	SessionID        string `json:"session_id,omitempty" table:"SESSION,short"`
	OutputGeneration int64  `json:"output_generation"`
	ProcessInstance  string `json:"process_instance_id"`
}

type agentRecoveryList struct {
	Targets []agentRecoveryRow `json:"targets"`
}

func (l agentRecoveryList) QuietLines() []string {
	out := make([]string, 0, len(l.Targets))
	for _, t := range l.Targets {
		out = append(out, t.AgentID)
	}
	return out
}

func runAgentRecoveries(inv *cli.Invocation) (any, error) {
	var provider leapmuxv1.AgentProvider
	if raw := strings.TrimSpace(stringFlag(inv, "provider")); raw != "" {
		p, ok := agentlabels.ParseProvider(raw)
		if !ok {
			return nil, cli.Usagef("unknown --provider %q", raw)
		}
		provider = p
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListAgentInputRecoveriesResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerAgentInputServiceListAgentInputRecoveriesProcedure,
		&leapmuxv1.ListAgentInputRecoveriesRequest{AgentProvider: provider}, &resp); err != nil {
		return nil, err
	}
	rows := make([]agentRecoveryRow, 0, len(resp.GetTargets()))
	for _, t := range resp.GetTargets() {
		rec := t.GetRecovery()
		rows = append(rows, agentRecoveryRow{
			AgentID:          t.GetAgentId(),
			Title:            t.GetTitle(),
			Provider:         agentlabels.CLIAlias(rec.GetAgentProvider()),
			SessionID:        rec.GetAgentSessionId(),
			OutputGeneration: rec.GetOutputGeneration(),
			ProcessInstance:  rec.GetProcessInstanceId(),
		})
	}
	return agentRecoveryList{Targets: rows}, nil
}

func newAgentRecoverCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "recover <agent>",
		Short:   "Approve process recovery for an agent paused for login or process recovery",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux agent recover 87a19d5f"},
		},
		Flags:    addTargetWorkspaceFlag,
		Complete: completeTargets,
		Run:      runAgentRecover,
	})
}

func runAgentRecover(inv *cli.Invocation) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		rec := queue.GetProcessRecovery()
		if rec == nil {
			return nil, cli.Newf(cli.CodeNotFound, "agent %s has no pending process recovery", q.agent.ID)
		}
		var resp leapmuxv1.RecoverAgentInputProcessResponse
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceRecoverAgentInputProcessProcedure,
			&leapmuxv1.RecoverAgentInputProcessRequest{AgentId: q.agent.ID, Recovery: rec}, &resp); err != nil {
			return nil, err
		}
		next := resp.GetSnapshot()
		reason := pauseReasonOf(next)
		return agentQueueState{ID: q.agent.ID, Paused: reason != "", PauseReason: reason}, nil
	})
}
