package cmd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/coder/quartz"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// The places of a message in the result of `agent send --wait`.
const (
	// messageQueued is a message that waits in the agent's input queue.
	messageQueued = "queued"
	// messageDelivered is a message that the agent received: its row is in
	// the transcript.
	messageDelivered = "delivered"
)

// agentReplyResult is the result of `agent send --wait`.
type agentReplyResult struct {
	ID      string `json:"id"`
	InputID string `json:"input_id"`
	// Message is "queued" or "delivered".
	Message string `json:"message"`
	// State is the agent's activity when the wait ended: a token of
	// AgentActivityState, or "unknown".
	State string `json:"state"`
	// Replies is the agent's reply text after the message, as the transcript
	// shows it: the last reply, or every reply with --all. It is empty when
	// the agent replied with no text.
	Replies []string `json:"replies"`
}

// RenderText prints the replies, with a blank line between two of them.
func (r agentReplyResult) RenderText(w io.Writer) error {
	if len(r.Replies) == 0 {
		return nil
	}
	_, err := fmt.Fprintln(w, strings.Join(r.Replies, "\n\n"))
	return err
}

// replyStep is where a wait for a reply stands after one evaluation.
type replyStep int

const (
	// replyPending keeps the wait going.
	replyPending replyStep = iota
	// replySettled is an agent that settled after it received the message.
	replySettled
	// replyWaitingForUser is an agent that waits for an answer to a
	// permission prompt, before or after it received the message.
	replyWaitingForUser
)

// replyVerdict is the result of one evaluation.
type replyVerdict struct {
	step    replyStep
	message string
	replies []string
	// paused is the pause reason of the queue that holds the message, or
	// UNSPECIFIED. A pause can end by itself, as after a planned restart, so
	// the wait goes on and only says why it waits.
	paused leapmuxv1.AgentInputQueuePauseReason
}

// replyReader reads what an evaluation needs from the Worker.
type replyReader struct {
	readQueue func(ctx context.Context) (*leapmuxv1.AgentInputQueueSnapshot, error)
	// readRowsAfter reads every row after the seq in ascending order, each
	// with its reply text.
	readRowsAfter func(ctx context.Context, afterSeq int64) ([]*leapmuxv1.AgentChatMessage, error)
	// latestSeq reads the seq of the agent's last row, or 0 when the Worker
	// cannot tell.
	latestSeq func(ctx context.Context) (int64, error)
	// rowSeq reads the seq of the row whose id is rowID, from the newest row
	// back, and reports false when the transcript holds no such row.
	rowSeq func(ctx context.Context, rowID string) (int64, bool, error)
}

// workerReplyReader reads the queue and the transcript of agentID on
// workerID through c.
func workerReplyReader(c *control.Client, workerID, agentID string) replyReader {
	call := func(ctx context.Context, method string, in, out proto.Message) error {
		ctx, cancel := rpcDeadline(ctx)
		defer cancel()
		return callInnerRPC(ctx, c, workerID, method, in, out)
	}
	return replyReader{
		readQueue: func(ctx context.Context) (*leapmuxv1.AgentInputQueueSnapshot, error) {
			var resp leapmuxv1.ListAgentInputQueueResponse
			if err := call(ctx, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
				&leapmuxv1.ListAgentInputQueueRequest{AgentId: agentID}, &resp); err != nil {
				return nil, err
			}
			return resp.GetSnapshot(), nil
		},
		readRowsAfter: func(ctx context.Context, afterSeq int64) ([]*leapmuxv1.AgentChatMessage, error) {
			return pageRowsAfter(afterSeq, func(req *leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
				req.AgentId, req.IncludeReplyText = agentID, true
				var resp leapmuxv1.ListAgentMessagesResponse
				if err := call(ctx, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, req, &resp); err != nil {
					return nil, err
				}
				return &resp, nil
			})
		},
		latestSeq: func(ctx context.Context) (int64, error) {
			var resp leapmuxv1.ListAgentMessagesResponse
			if err := call(ctx, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, &leapmuxv1.ListAgentMessagesRequest{
				AgentId: agentID, Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, Limit: 1,
			}, &resp); err != nil {
				return 0, err
			}
			return latestSeqOf(&resp), nil
		},
		rowSeq: func(ctx context.Context, rowID string) (int64, bool, error) {
			return findRowSeqBackward(rowID, func(req *leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
				req.AgentId = agentID
				var resp leapmuxv1.ListAgentMessagesResponse
				if err := call(ctx, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, req, &resp); err != nil {
					return nil, err
				}
				return &resp, nil
			})
		},
	}
}

// findRowSeqBackward reads the pages of the transcript through list from the
// newest row back until it finds the row rowID, and returns its seq. It
// reports false when no page holds the row. A page that does not move before
// its cursor ends the read, so a Worker that answers the same page again
// cannot loop it forever.
func findRowSeqBackward(rowID string, list func(*leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error)) (int64, bool, error) {
	req := &leapmuxv1.ListAgentMessagesRequest{Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, Limit: contracts.MessagePageLimit}
	for {
		resp, err := list(req)
		if err != nil {
			return 0, false, err
		}
		rows := resp.GetMessages()
		if i := slices.IndexFunc(rows, func(row *leapmuxv1.AgentChatMessage) bool { return row.GetId() == rowID }); i >= 0 {
			return rows[i].GetSeq(), true, nil
		}
		if !resp.GetHasMore() || len(rows) == 0 {
			return 0, false, nil
		}
		// A page lists its rows in ascending seq, so the first row is the
		// oldest one that the read reached.
		oldest := rows[0].GetSeq()
		if req.Anchor == leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE && oldest >= req.GetCursorSeq() {
			return 0, false, nil
		}
		req = &leapmuxv1.ListAgentMessagesRequest{Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE, CursorSeq: oldest, Limit: contracts.MessagePageLimit}
	}
}

// latestSeqOf is the seq of the agent's last row that a LATEST page states:
// its latest_seq, or the seq of its last row when the Worker could not read
// the tail, or 0 for an agent with no row. A baseline of 0 only makes the
// reads page from the oldest row, so a wrong guess costs time, not a reply.
func latestSeqOf(resp *leapmuxv1.ListAgentMessagesResponse) int64 {
	if resp.LatestSeq != nil {
		return resp.GetLatestSeq()
	}
	if n := len(resp.GetMessages()); n > 0 {
		return resp.GetMessages()[n-1].GetSeq()
	}
	return 0
}

// pageRowsAfter reads every row after afterSeq through list, one page at a
// time. A seq of 0 or less reads from the oldest row, because the Worker
// needs a positive cursor for the page after it. A page that does not move
// past its cursor ends the read, so a Worker that answers the same page
// again cannot loop it forever.
func pageRowsAfter(afterSeq int64, list func(*leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error)) ([]*leapmuxv1.AgentChatMessage, error) {
	var rows []*leapmuxv1.AgentChatMessage
	cursor := afterSeq
	for {
		req := &leapmuxv1.ListAgentMessagesRequest{Limit: contracts.MessagePageLimit}
		if cursor > 0 {
			req.Anchor, req.CursorSeq = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, cursor
		} else {
			req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST
		}
		resp, err := list(req)
		if err != nil {
			return nil, err
		}
		before := cursor
		for _, row := range resp.GetMessages() {
			if row.GetSeq() > cursor {
				rows = append(rows, row)
				cursor = row.GetSeq()
			}
		}
		if !resp.GetHasMore() || cursor <= before {
			return rows, nil
		}
	}
}

// judgeQueued judges a message that still waits in the queue.
func judgeQueued(state leapmuxv1.AgentActivityState, queue *leapmuxv1.AgentInputQueueSnapshot, item *leapmuxv1.QueuedAgentInput) (replyVerdict, error) {
	if item.GetState() == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED {
		return replyVerdict{}, cli.Newf(cli.CodeInternal, "the agent did not receive the message: %s", item.GetError()).
			WithHint("the message stays in the agent's input queue: retry or remove it in LeapMux")
	}
	verdict := replyVerdict{step: replyPending, message: messageQueued, paused: queue.GetPauseReason()}
	if state == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER {
		verdict.step = replyWaitingForUser
	}
	return verdict, nil
}

// judgeDelivered judges a message that left the queue. rows are the rows
// after the baseline.
//
// The Worker writes the message's row and marks the queue's turn active in
// one transaction, so "the row exists and no turn runs" proves that the
// message's turn ended. The published state then says whether the agent
// settled, because a background task can keep it working after its turn.
func judgeDelivered(state leapmuxv1.AgentActivityState, queue *leapmuxv1.AgentInputQueueSnapshot, rows []*leapmuxv1.AgentChatMessage, inputID string, all bool) (replyVerdict, error) {
	at := slices.IndexFunc(rows, func(row *leapmuxv1.AgentChatMessage) bool { return row.GetId() == inputID })
	if at < 0 {
		return replyVerdict{}, cli.Newf(cli.CodeNotFound, "the message %s left the agent's input queue before the agent received it", inputID).
			WithHint("someone removed it from the queue in LeapMux").
			WithExitCode(cli.ExitWaitEnded)
	}
	verdict := replyVerdict{step: replyPending, message: messageDelivered, replies: repliesAfter(rows[at+1:], all)}
	switch {
	case state == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER:
		verdict.step = replyWaitingForUser
	case queue.GetActiveTurn(), state == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING:
	default:
		verdict.step = replySettled
	}
	return verdict, nil
}

// repliesAfter collects the reply text of the agent's own rows, until the
// next message that a user sent. A subagent's row has a parent span, and
// its text answers its parent, not the user. The user's answer to a prompt
// is no new message, so the turn goes on past it.
func repliesAfter(rows []*leapmuxv1.AgentChatMessage, all bool) []string {
	replies := []string{}
	for _, row := range rows {
		if row.GetParentSpanId() != "" {
			continue
		}
		if row.GetMarkType() == leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE {
			break
		}
		if row.GetSource() == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT && row.ReplyText != nil {
			replies = append(replies, row.GetReplyText())
		}
	}
	if !all && len(replies) > 1 {
		return replies[len(replies)-1:]
	}
	return replies
}

// replyWaitSpec states one wait for the replies to a message.
type replyWaitSpec struct {
	agentID string
	inputID string
	// baseline is the agent's last seq before the send. The message's row
	// lands after it.
	baseline int64
	all      bool
	// timeout ends the wait after this long. Zero waits without a limit.
	timeout time.Duration
}

// replyWaitOutcome is how a wait for a reply ended.
type replyWaitOutcome int

const (
	// replyWaitSettled is an agent that settled after the message.
	replyWaitSettled replyWaitOutcome = iota + 1
	// replyWaitWaiting is an agent that waits for an answer.
	replyWaitWaiting
	// replyWaitTimedOut is a wait that its timeout ended.
	replyWaitTimedOut
)

// replyEvaluator evaluates the message of one wait.
type replyEvaluator struct {
	reader replyReader
	spec   replyWaitSpec
	// notice says why the wait goes on: a paused queue.
	notice func(format string, args ...any)
	// noticed is the pause reason that the last notice stated.
	noticed leapmuxv1.AgentInputQueuePauseReason
}

// evaluate reads the queue first, and the transcript only when the message
// left the queue. The other order could read the transcript before the
// Worker accepted the message, and the queue after it: the message would
// then be in neither.
func (e *replyEvaluator) evaluate(ctx context.Context, state leapmuxv1.AgentActivityState) (replyVerdict, error) {
	queue, err := e.reader.readQueue(ctx)
	if err != nil {
		return replyVerdict{}, err
	}
	if i := slices.IndexFunc(queue.GetItems(), func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == e.spec.inputID }); i >= 0 {
		verdict, err := judgeQueued(state, queue, queue.GetItems()[i])
		if err == nil && verdict.paused != e.noticed {
			e.noticed = verdict.paused
			if verdict.paused != leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED {
				e.notice("the input queue of agent %s is paused (%s); the message waits until it resumes", e.spec.agentID, enumToken(verdict.paused))
			}
		}
		return verdict, err
	}
	rows, err := e.reader.readRowsAfter(ctx, e.spec.baseline)
	if err != nil {
		return replyVerdict{}, err
	}
	return judgeDelivered(state, queue, rows, e.spec.inputID, e.spec.all)
}

// awaitReply evaluates the message after each event of the agent until the
// agent settles after it, waits for the user, or the timeout passes. state
// is the agent's published state when the wait began.
func awaitReply(ctx context.Context, clock quartz.Clock, events <-chan activityEvent, evaluator *replyEvaluator, state leapmuxv1.AgentActivityState) (agentReplyResult, replyWaitOutcome, error) {
	spec := evaluator.spec
	result := func(v replyVerdict) agentReplyResult {
		replies := v.replies
		if replies == nil {
			replies = []string{}
		}
		return agentReplyResult{ID: spec.agentID, InputID: spec.inputID, Message: v.message, State: agentStateToken(state), Replies: replies}
	}
	var deadline <-chan time.Time
	if spec.timeout > 0 {
		timer := clock.NewTimer(spec.timeout, replyWaitTimerTag)
		defer timer.Stop(replyWaitTimerTag)
		deadline = timer.C
	}
	for {
		verdict, err := evaluator.evaluate(ctx, state)
		if err != nil {
			return agentReplyResult{}, 0, err
		}
		switch verdict.step {
		case replySettled:
			return result(verdict), replyWaitSettled, nil
		case replyWaitingForUser:
			return result(verdict), replyWaitWaiting, nil
		case replyPending:
			// The next event of the agent wakes the next evaluation.
		}
		for wake := false; !wake; {
			select {
			case <-ctx.Done():
				return agentReplyResult{}, 0, context.Cause(ctx)
			case <-deadline:
				// The verdict of the last evaluation states where the message
				// stood. A fresh read could fail on a Worker that the timeout
				// was about.
				return result(verdict), replyWaitTimedOut, nil
			case ev := <-events:
				switch ev.kind {
				case activityFailed:
					return agentReplyResult{}, 0, ev.err
				case activityClosed:
					return agentReplyResult{}, 0, cli.Newf(cli.CodeNotFound, "agent %s closed before it settled after the message", spec.agentID).
						WithExitCode(cli.ExitWaitEnded)
				case activityLevel, activityChange:
					state = ev.state
				case activityTurnEnd, activityStatusChange:
					// The state stays, but the turn or the process changed,
					// so the queue and the transcript can say more now.
				}
				wake = true
			}
		}
	}
}

// replyWaitTimerTag identifies the timeout of a wait for a reply to a test
// clock.
const replyWaitTimerTag = "agent-send-wait-timeout"

// agentStateToken is the token of state, or "unknown" when the Worker states
// none.
func agentStateToken(state leapmuxv1.AgentActivityState) string {
	if token := enumToken(state); token != "" {
		return token
	}
	return agentStateUnknown
}

// awaitFirstLevel reads events until the first level of agentID: the
// state after the Worker registered the watch. A change before it is older
// than the level, so the level replaces it.
func awaitFirstLevel(ctx context.Context, events <-chan activityEvent, agentID string) (leapmuxv1.AgentActivityState, error) {
	for {
		select {
		case <-ctx.Done():
			return 0, context.Cause(ctx)
		case ev := <-events:
			switch ev.kind {
			case activityFailed:
				return 0, ev.err
			case activityClosed:
				return 0, cli.Newf(cli.CodeNotFound, "agent %s is closed", agentID)
			case activityLevel:
				return ev.state, nil
			case activityChange, activityTurnEnd, activityStatusChange:
				// The level read began after this event, so the level
				// replaces it.
			}
		}
	}
}

// sendAndWait sends a message to the agent t and waits until the agent
// settles after it, as `agent send --wait` does:
//
//  1. Watch the agent, and read its state once the Worker registered the
//     watch, so no later change is lost.
//  2. Refuse to send to an agent that waits for an answer: the message would
//     wait behind the prompt.
//  3. Read the last seq, send the message, and evaluate it after each event
//     of the agent.
//
// A run that repeats a request (--request-id) can find that the Worker
// already holds the message. When the message left the queue before this
// run, its row is older than the last seq, so the wait reads the replies
// after that row instead: the run prints the reply of the first run. When
// someone removed the message from the queue instead, the Worker queues
// nothing for the repeat, and the transcript holds no row of the message. The
// run then fails as the first run fails when someone removes the message
// while it waits: with not_found, and exit status 6.
func sendAndWait(s *session, t tabTarget, message userMessage, inputID string, all bool, timeout time.Duration) (agentReplyResult, replyWaitOutcome, error) {
	if err := refuseSelfWait(t); err != nil {
		return agentReplyResult{}, 0, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	events := make(chan activityEvent, 16)
	startActivityFeed(ctx, workerActivitySource(s.c, t.WorkerID), []string{t.ID}, events)
	state, err := awaitFirstLevel(ctx, events, t.ID)
	if err != nil {
		return agentReplyResult{}, 0, err
	}
	if state == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER {
		return agentReplyResult{}, 0, cli.Newf(cli.CodeAgentWaiting, "agent %s waits for an answer to a permission prompt, so the message would wait behind it", t.ID).
			WithHint("list the prompt with \"leapmux control agent requests %s\", answer it, then send the message again", t.ID)
	}
	reader := workerReplyReader(s.c, t.WorkerID, t.ID)
	baseline, err := reader.latestSeq(ctx)
	if err != nil {
		return agentReplyResult{}, 0, err
	}
	queue, err := enqueueUserMessage(ctx, s.c, t, inputID, message)
	if err != nil {
		return agentReplyResult{}, 0, err
	}
	if !queueHolds(queue, inputID) {
		// The Worker answers a new input with the queue that holds it, so
		// only a repeated request reaches here.
		seq, found, err := reader.rowSeq(ctx, inputID)
		if err != nil {
			return agentReplyResult{}, 0, err
		}
		if !found {
			// The Worker writes the transcript row in the transaction that
			// takes the message out of the queue, so a message that left the
			// queue without a row is one that someone removed.
			return agentReplyResult{}, 0, cli.Newf(cli.CodeNotFound, "someone removed the message %s from the input queue of agent %s before the agent received it, so this retry queued nothing", inputID, t.ID).
				WithHint("send the message again with a new --request-id").
				WithExitCode(cli.ExitWaitEnded)
		}
		baseline = seq - 1
	}
	evaluator := &replyEvaluator{
		reader: reader,
		spec:   replyWaitSpec{agentID: t.ID, inputID: inputID, baseline: baseline, all: all, timeout: timeout},
		notice: s.inv.Printer.Notice,
	}
	return awaitReply(ctx, quartz.NewReal(), events, evaluator, state)
}

// enqueueUserMessage puts message in the input queue of the agent t as the
// input inputID, and returns the queue that the Worker answers with.
func enqueueUserMessage(ctx context.Context, c *control.Client, t tabTarget, inputID string, message userMessage) (*leapmuxv1.AgentInputQueueSnapshot, error) {
	return enqueueAgentInput(ctx, c, t, message.request(t.ID, inputID))
}

// enqueueAgentInput sends one EnqueueAgentInput request for the agent t,
// and returns the queue that the Worker answers with.
func enqueueAgentInput(ctx context.Context, c *control.Client, t tabTarget, request *leapmuxv1.EnqueueAgentInputRequest) (*leapmuxv1.AgentInputQueueSnapshot, error) {
	ctx, cancel := rpcDeadline(ctx)
	defer cancel()
	var resp leapmuxv1.EnqueueAgentInputResponse
	if err := callInnerRPC(ctx, c, t.WorkerID, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, request, &resp); err != nil {
		return nil, err
	}
	return resp.GetSnapshot(), nil
}

// queueHolds reports whether queue holds the input inputID.
func queueHolds(queue *leapmuxv1.AgentInputQueueSnapshot, inputID string) bool {
	return slices.ContainsFunc(queue.GetItems(), func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == inputID })
}

// agentReplyEnded reports a wait for a reply that did not end with a settled
// agent: it prints the result, says why on stderr, and returns exit status 6.
func agentReplyEnded(p *cli.Printer, result agentReplyResult, outcome replyWaitOutcome, timeout time.Duration) error {
	if err := p.Result(result); err != nil {
		return err
	}
	switch {
	case outcome == replyWaitWaiting:
		p.Notice("agent %s waits for an answer to a permission prompt; list it with \"leapmux control agent requests %s\"", result.ID, result.ID)
	case result.Message == messageQueued:
		p.Notice("the wait ended after %s; the message is still in the input queue of agent %s", timeout, result.ID)
	default:
		p.Notice("the wait ended after %s; agent %s is still working on the message", timeout, result.ID)
	}
	return &cli.ExitStatus{Code: cli.ExitWaitEnded}
}
