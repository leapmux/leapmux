package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/tunnel"
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
	ID        string `json:"id"`
	InputID   string `json:"input_id"`
	RequestID string `json:"request_id,omitempty"`
	// Message is "queued" or "delivered".
	Message string `json:"message"`
	// State is the agent's activity when the wait ended: a token of
	// AgentActivityState, or "unknown".
	State string `json:"state"`
	// TurnOutcome reports the native turn's result when the Worker proved it.
	TurnOutcome string `json:"turn_outcome,omitempty"`
	// Replies is the agent's reply text after the message, as the transcript
	// shows it: the last reply, or every reply with --all. It is empty when
	// the agent replied with no text.
	Replies []string `json:"replies"`
	// Structured holds the validated JSON value when --output-schema succeeded.
	Structured json.RawMessage `json:"structured,omitempty"`
	// SchemaAttempts counts the turns taken to satisfy --output-schema.
	SchemaAttempts int `json:"schema_attempts,omitempty"`
	// Receipt carries recovery metadata when a keyed wait ended without settling.
	Receipt *cli.RetryReceipt `json:"receipt,omitempty"`
	// statusRequestID overrides RequestID in agentReplyEnded when a corrective
	// schema retry turn is the active input that request-status should inspect.
	statusRequestID string
}

// formatSanitizedReplies sanitizes each reply for terminal output and joins
// them with blank lines.
func formatSanitizedReplies(replies []string) string {
	sanitized := make([]string, len(replies))
	for i, reply := range replies {
		sanitized[i] = cli.SanitizeTerminalText(reply)
	}
	return strings.Join(sanitized, "\n\n")
}

// RenderText prints the validated JSON when --output-schema was used, or the
// replies separated by blank lines otherwise.
func (r agentReplyResult) RenderText(w io.Writer) error {
	if len(r.Structured) > 0 {
		_, err := fmt.Fprintln(w, string(r.Structured))
		return err
	}
	if len(r.Replies) == 0 {
		return nil
	}
	_, err := fmt.Fprintln(w, formatSanitizedReplies(r.Replies))
	return err
}

// QuietLines returns the input_id for `agent send --wait -q` (falling back to
// agent ID when no input ID is set).
func (r agentReplyResult) QuietLines() []string {
	if r.InputID != "" {
		return []string{r.InputID}
	}
	if r.ID != "" {
		return []string{r.ID}
	}
	return nil
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
	replyInterrupted
	replyFailed
	replyUnproved
)

// replyVerdict is the result of one evaluation.
type replyVerdict struct {
	step        replyStep
	message     string
	replies     []string
	turnOutcome leapmuxv1.AgentInputTurnOutcome
	// paused is the pause reason of the queue that holds the message, or
	// UNSPECIFIED. A pause can end by itself, as after a planned restart, so
	// the wait goes on and only says why it waits.
	paused leapmuxv1.AgentInputQueuePauseReason
}

// replyReader reads what an evaluation needs from the Worker.
type replyReader struct {
	readQueue func(ctx context.Context, inputID string) (*leapmuxv1.ListAgentInputQueueResponse, error)
	// readRowsAfter reads every row after the seq in ascending order, each
	// with its reply text.
	readRowsAfter func(ctx context.Context, afterSeq int64, replyInputID string) ([]*leapmuxv1.AgentChatMessage, error)
	// latestSeq reads the seq of the agent's last row, or 0 when the Worker
	// cannot tell.
	latestSeq func(ctx context.Context) (int64, error)
}

// persistentWorkerChannel holds one reusable E2EE channel to workerID (or a
// direct local IPC client) across multiple RPCs, avoiding a fresh Noise_NK
// handshake on every wake-up evaluation and transcript page.
//
// It opens lazily on first use, closes on Close, and reconnects once on a hub
// E2EE transport error (never on an in-band RPC refusal or caller cancellation).
type persistentWorkerChannel struct {
	peer     control.Peer
	workerID string

	mu     sync.Mutex
	w      *workerCall
	closed bool
}

func newPersistentWorkerChannel(peer control.Peer, workerID string) *persistentWorkerChannel {
	return &persistentWorkerChannel{peer: peer, workerID: workerID}
}

func (p *persistentWorkerChannel) open(ctx context.Context) (workerCall, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return workerCall{}, context.Canceled
	}
	if p.w != nil {
		w := *p.w
		p.mu.Unlock()
		return w, nil
	}
	p.mu.Unlock()

	var opened workerCall
	switch c := p.peer.(type) {
	case *control.WorkerIPCClient:
		opened = workerCall{ipc: c, workerID: p.workerID}
	case *control.HubClient:
		if p.workerID == "" {
			return workerCall{}, &codedRPCError{Code: "invalid_request", Cause: errors.New("worker_id is required")}
		}
		openCtx, cancel := rpcDeadline(ctx)
		ch, err := c.OpenE2EEChannel(openCtx, context.WithoutCancel(openCtx), p.workerID)
		cancel()
		if err != nil {
			return workerCall{}, withHubVersion(ctx, c, &codedRPCError{Code: "channel_open_failed", Cause: err})
		}
		opened = workerCall{hub: c, workerID: p.workerID, ch: ch}
	default:
		return workerCall{}, fmt.Errorf("unsupported control peer %T", p.peer)
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		if opened.ch != nil {
			opened.ch.Close()
		}
		return workerCall{}, context.Canceled
	}
	if p.w != nil {
		w := *p.w
		p.mu.Unlock()
		if opened.ch != nil {
			opened.ch.Close()
		}
		return w, nil
	}
	p.w = &opened
	p.mu.Unlock()
	return opened, nil
}

func (p *persistentWorkerChannel) reset(failed *tunnel.Channel) {
	p.mu.Lock()
	if p.w == nil || p.w.ch == nil || p.w.ch != failed {
		p.mu.Unlock()
		return
	}
	ch := p.w.ch
	p.w = nil
	p.mu.Unlock()
	ch.Close()
}

// Close closes any open hub E2EE channel held by p.
func (p *persistentWorkerChannel) Close() {
	p.mu.Lock()
	p.closed = true
	w := p.w
	p.w = nil
	p.mu.Unlock()
	if w != nil && w.ch != nil {
		w.ch.Close()
	}
}

// Call invokes a read RPC over the shared channel, reconnecting once if a hub
// E2EE channel drops at the transport layer.
func (p *persistentWorkerChannel) Call(ctx context.Context, method string, in, out proto.Message) error {
	return p.call(ctx, method, in, out, true)
}

// CallOnce invokes a mutating RPC over the shared channel without automatic
// transport retry, so the caller retains ownership of retry disposition.
func (p *persistentWorkerChannel) CallOnce(ctx context.Context, method string, in, out proto.Message) error {
	return p.call(ctx, method, in, out, false)
}

func (p *persistentWorkerChannel) call(ctx context.Context, method string, in, out proto.Message, retryOnChannelErr bool) error {
	for attempt := 0; ; attempt++ {
		w, err := p.open(ctx)
		if err != nil {
			return err
		}
		callCtx, cancel := rpcDeadline(ctx)
		err = w.Call(callCtx, method, in, out)
		cancel()
		if err == nil {
			return nil
		}
		if w.ch == nil || ctx.Err() != nil || !isChannelTransportError(err) {
			return err
		}
		p.reset(w.ch)
		if !retryOnChannelErr || attempt > 0 {
			return err
		}
	}
}

func isChannelTransportError(err error) bool {
	var coded *codedRPCError
	return errors.As(err, &coded) && coded.Code == "rpc_failed"
}

// workerReplyReader reads the queue and the transcript of agentID on
// workerID through c using one channel per call.
func workerReplyReader(c control.Peer, workerID, agentID string) replyReader {
	call := func(ctx context.Context, method string, in, out proto.Message) error {
		ctx, cancel := rpcDeadline(ctx)
		defer cancel()
		return callInnerRPC(ctx, c, workerID, method, in, out)
	}
	return buildReplyReader(call, agentID)
}

// channelReplyReader reads the queue and the transcript of agentID over a
// reusable persistentWorkerChannel.
func channelReplyReader(ch *persistentWorkerChannel, agentID string) replyReader {
	return buildReplyReader(ch.Call, agentID)
}

func buildReplyReader(call func(ctx context.Context, method string, in, out proto.Message) error, agentID string) replyReader {
	return replyReader{
		readQueue: func(ctx context.Context, inputID string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
			var resp leapmuxv1.ListAgentInputQueueResponse
			if err := call(ctx, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
				&leapmuxv1.ListAgentInputQueueRequest{AgentId: agentID, ReplyInputId: inputID}, &resp); err != nil {
				return nil, err
			}
			return &resp, nil
		},
		readRowsAfter: func(ctx context.Context, afterSeq int64, replyInputID string) ([]*leapmuxv1.AgentChatMessage, error) {
			list := func(req *leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
				req.AgentId, req.IncludeReplyText = agentID, true
				req.ReplyInputId = replyInputID
				var resp leapmuxv1.ListAgentMessagesResponse
				if err := call(ctx, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, req, &resp); err != nil {
					return nil, err
				}
				return &resp, nil
			}
			if replyInputID != "" {
				return pageExactInputReply(list)
			}
			return pageRowsAfter(afterSeq, list)
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
	}
}

func pageExactInputReply(list func(*leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error)) ([]*leapmuxv1.AgentChatMessage, error) {
	var rows []*leapmuxv1.AgentChatMessage
	var cursor int64
	var canonical *bool
	for {
		resp, err := list(&leapmuxv1.ListAgentMessagesRequest{
			Anchor: leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, ReplyCursor: cursor,
			ReplyCursorCanonical: canonical,
			Limit:                contracts.MessagePageLimit,
		})
		if err != nil {
			return nil, err
		}
		respCanonical := resp.GetReplyCanonical()
		if canonical != nil && !*canonical && respCanonical {
			// The turn became canonical between pages, so the Worker restarted
			// from canonical ordinal 0 instead of reusing the pre-publication seq.
			rows = nil
			cursor = 0
		}
		canonical = &respCanonical
		rows = append(rows, resp.GetMessages()...)
		if !resp.GetHasMore() {
			return rows, nil
		}
		next := resp.GetNextReplyCursor()
		if next <= cursor || len(resp.GetMessages()) == 0 {
			return nil, cli.Newf(cli.CodeInternal, "the Worker's exact reply page did not advance")
		}
		cursor = next
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
	if next := slices.IndexFunc(rows, func(row *leapmuxv1.AgentChatMessage) bool {
		return row.GetParentSpanId() == "" && row.GetMarkType() == leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE
	}); next >= 0 {
		rows = rows[:next]
	}
	return replyTexts(rows, all)
}

func replyTexts(rows []*leapmuxv1.AgentChatMessage, all bool) []string {
	replies := []string{}
	for _, row := range rows {
		if row.GetParentSpanId() != "" {
			continue
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
	// acceptedSeq is the input sequence reported on an accepted retry.
	acceptedSeq int64
	all         bool
	// timeout ends the wait after this long. Zero waits without a limit.
	timeout time.Duration
	// onActivity, when non-nil, runs inside awaitReply for each activity event.
	onActivity func(activityEvent)
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
	replyWaitInterrupted
	replyWaitFailed
	replyWaitUnproved
)

// replyEvaluator evaluates the message of one wait.
type replyEvaluator struct {
	reader replyReader
	spec   replyWaitSpec
	// notice says why the wait goes on: a paused queue.
	notice func(format string, args ...any)
	// noticed is the pause reason that the last notice stated.
	noticed leapmuxv1.AgentInputQueuePauseReason
	// delivered records a receipt even if a later transcript read is canceled.
	delivered bool
	// exactAcceptedSeq records the Worker's exact accepted_seq when InputReply
	// is observed so a timeout fetch can read exact partial replies once.
	exactAcceptedSeq int64
	// skippedReplies reports that the last pending evaluation intentionally
	// skipped paging the transcript, so a timeout fetches partial replies once.
	skippedReplies bool
	// evalOverride, when non-nil, replaces evaluate in tests to inject synthetic verdicts.
	evalOverride func(ctx context.Context, state leapmuxv1.AgentActivityState) (replyVerdict, error)
}

// evaluate reads the queue first, and the transcript only when the message
// left the queue. The other order could read the transcript before the
// Worker accepted the message, and the queue after it: the message would
// then be in neither.
func (e *replyEvaluator) evaluate(ctx context.Context, state leapmuxv1.AgentActivityState) (replyVerdict, error) {
	if e.evalOverride != nil {
		return e.evalOverride(ctx, state)
	}
	observation, err := e.reader.readQueue(ctx, e.spec.inputID)
	if err != nil {
		return replyVerdict{}, err
	}
	if exact := observation.GetInputReply(); exact != nil {
		return e.evaluateExact(ctx, exact)
	}
	queue := observation.GetSnapshot()
	if i := slices.IndexFunc(queue.GetItems(), func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == e.spec.inputID }); i >= 0 {
		e.skippedReplies = false
		verdict, err := judgeQueued(state, queue, queue.GetItems()[i])
		if err == nil && verdict.paused != e.noticed {
			e.noticed = verdict.paused
			if verdict.paused != leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED {
				e.notice("the input queue of agent %s is paused (%s); the message waits until it resumes", e.spec.agentID, enumToken(verdict.paused))
			}
		}
		return verdict, err
	}
	// Once delivery is confirmed in the transcript on the legacy path, skip
	// re-paging all rows on intermediate wake-ups while the turn is still running.
	if e.delivered && state != leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER &&
		(queue.GetActiveTurn() || state == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING) {
		e.skippedReplies = true
		return replyVerdict{step: replyPending, message: messageDelivered}, nil
	}
	e.skippedReplies = false
	rows, err := e.reader.readRowsAfter(ctx, e.spec.baseline, "")
	if err != nil {
		return replyVerdict{}, err
	}
	verdict, err := judgeDelivered(state, queue, rows, e.spec.inputID, e.spec.all)
	if err == nil {
		e.delivered = true
	}
	return verdict, err
}

func (e *replyEvaluator) evaluateExact(ctx context.Context, reply *leapmuxv1.AgentInputTurnReply) (replyVerdict, error) {
	if reply.GetAcceptedSeq() <= 0 {
		return replyVerdict{}, cli.Newf(cli.CodeInternal, "the Worker returned an invalid input sequence")
	}
	e.delivered = true
	e.exactAcceptedSeq = reply.GetAcceptedSeq()
	outcome := reply.GetOutcome()
	verdict := replyVerdict{
		step: replyPending, message: messageDelivered, turnOutcome: outcome,
	}
	switch outcome {
	case leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED:
		if reply.GetWaitingForUser() {
			verdict.step = replyWaitingForUser
		}
	case leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED:
		verdict.step = replySettled
	case leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED:
		verdict.step = replyInterrupted
	case leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED:
		verdict.step = replyFailed
	case leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED:
		verdict.step = replyUnproved
	default:
		return replyVerdict{}, cli.Newf(cli.CodeVersionMismatch, "the Worker returned an unknown input turn outcome")
	}
	if verdict.step == replyPending {
		e.skippedReplies = true
		return verdict, nil
	}
	e.skippedReplies = false
	rows, err := e.reader.readRowsAfter(ctx, reply.GetAcceptedSeq(), e.spec.inputID)
	if err != nil {
		return replyVerdict{}, err
	}
	verdict.replies = replyTexts(rows, e.spec.all)
	return verdict, nil
}

// fetchPartialReplies reads the latest partial replies once when a wait times
// out after skipping transcript reads during pending wake-ups.
func (e *replyEvaluator) fetchPartialReplies(parentCtx context.Context) []string {
	if !e.skippedReplies || e.reader.readRowsAfter == nil {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(parentCtx, 5*time.Second)
	defer cancel()
	if e.exactAcceptedSeq > 0 {
		rows, err := e.reader.readRowsAfter(fetchCtx, e.exactAcceptedSeq, e.spec.inputID)
		if err != nil {
			return nil
		}
		return replyTexts(rows, e.spec.all)
	}
	rows, err := e.reader.readRowsAfter(fetchCtx, e.spec.baseline, "")
	if err != nil {
		return nil
	}
	at := slices.IndexFunc(rows, func(row *leapmuxv1.AgentChatMessage) bool { return row.GetId() == e.spec.inputID })
	if at < 0 {
		return nil
	}
	return repliesAfter(rows[at+1:], e.spec.all)
}

// awaitReply evaluates the message after each event of the agent until the
// agent settles after it, waits for the user, or the timeout passes. state
// is the agent's published state when the wait began.
func awaitReply(parentCtx context.Context, clock quartz.Clock, events <-chan activityEvent, evaluator *replyEvaluator, state leapmuxv1.AgentActivityState) (agentReplyResult, replyWaitOutcome, error) {
	spec := evaluator.spec
	waitCtx, stop := agentWaitContext(parentCtx, clock, spec.timeout, replyWaitTimerTag)
	defer stop()
	result := func(v replyVerdict) agentReplyResult {
		replies := v.replies
		if replies == nil {
			replies = []string{}
		}
		return agentReplyResult{
			ID: spec.agentID, InputID: spec.inputID, Message: v.message, State: agentStateToken(state),
			TurnOutcome: enumToken(v.turnOutcome), Replies: replies,
		}
	}
	last := replyVerdict{message: messageQueued}
	if spec.acceptedSeq > 0 {
		last.message = messageDelivered
		evaluator.spec.baseline = spec.acceptedSeq - 1
	}
wait:
	for waitCtx.Err() == nil {
		verdict, err := evaluator.evaluate(waitCtx, state)
		if err != nil {
			if waitCtx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errAgentWaitExpired)) {
				break
			}
			return agentReplyResult{}, 0, err
		}
		last = verdict
		switch verdict.step {
		case replySettled:
			return result(verdict), replyWaitSettled, nil
		case replyWaitingForUser:
			// The Worker can report the prompt before its activity event arrives.
			state = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
			return result(verdict), replyWaitWaiting, nil
		case replyInterrupted:
			return result(verdict), replyWaitInterrupted, nil
		case replyFailed:
			return result(verdict), replyWaitFailed, nil
		case replyUnproved:
			return result(verdict), replyWaitUnproved, nil
		case replyPending:
			// The next event of the agent wakes the next evaluation.
		default:
			return agentReplyResult{}, 0, cli.Newf(cli.CodeInternal, "unexpected reply evaluation step %d", verdict.step)
		}
		if waitCtx.Err() != nil {
			break
		}
		select {
		case <-waitCtx.Done():
			break wait
		case ev := <-events:
			if waitCtx.Err() != nil {
				break wait
			}
			if spec.onActivity != nil {
				spec.onActivity(ev)
			}
			switch ev.kind {
			case activityFailed:
				return agentReplyResult{}, 0, ev.err
			case activityClosed:
				return agentReplyResult{}, 0, cli.Newf(cli.CodeNotFound, "agent %s closed before it settled after the message", spec.agentID).
					WithExitCode(cli.ExitWaitEnded)
			case activityLevel, activityChange:
				state = ev.state
			case activityTurnEnd, activityStatusChange, activityInputChanged:
				// Recheck the queue and transcript without changing the activity.
			}
		}
	}
	cause := context.Cause(waitCtx)
	if errors.Is(cause, errAgentWaitExpired) {
		if evaluator.delivered {
			last.message = messageDelivered
		}
		if replies := evaluator.fetchPartialReplies(parentCtx); replies != nil {
			last.replies = replies
		}
		return result(last), replyWaitTimedOut, nil
	}
	return agentReplyResult{}, 0, cause
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
			case activityChange, activityTurnEnd, activityStatusChange, activityInputChanged:
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
//  2. Refuse new input to an agent that waits for an answer. An explicit
//     request ID can still match an input the Worker already recorded.
//  3. Read the last seq, send the message, and evaluate it after each event
//     of the agent.
//
// A run that repeats a request (--request-id) can find that the Worker
// already holds the message. When the message left the queue before this
// run, the Worker returns the sequence of the accepted row. The wait reads
// the replies after that row. If someone removed the message from the queue,
// the Worker queues
// nothing for the repeat, and the transcript holds no row of the message. The
// run then fails as the first run fails when someone removes the message
// while it waits: with not_found, and exit status 6.
type sendDeliveryMode int

const (
	sendDeliveryNormal sendDeliveryMode = iota
	sendDeliverySteer
	sendDeliveryPreempt
)

func applySendDeliveryModeOnChannel(ctx context.Context, ch *persistentWorkerChannel, agentID, inputID string, mode sendDeliveryMode) error {
	switch mode {
	case sendDeliverySteer:
		return ch.CallOnce(ctx, leapmuxv1connect.WorkerAgentInputServiceSteerQueuedAgentInputProcedure,
			&leapmuxv1.SteerQueuedAgentInputRequest{AgentId: agentID, InputId: inputID}, nil)
	case sendDeliveryPreempt:
		return ch.CallOnce(ctx, leapmuxv1connect.WorkerAgentInputServicePreemptQueuedAgentInputProcedure,
			&leapmuxv1.PreemptQueuedAgentInputRequest{AgentId: agentID, InputId: inputID}, nil)
	default:
		return nil
	}
}

// replyWaitSession shares one activity feed and one persistent worker channel
// across one or more send-and-wait rounds (including --output-schema retries).
type replyWaitSession struct {
	agentID  string
	ctx      context.Context
	cancel   context.CancelFunc
	stopFeed func()
	events   chan activityEvent
	ch       *persistentWorkerChannel
	reader   replyReader
	state    leapmuxv1.AgentActivityState
}

func openReplyWaitSession(s *session, t tabTarget) (*replyWaitSession, error) {
	if err := refuseSelfWait(t); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	events := make(chan activityEvent, 16)
	stopFeed := startActivityFeed(ctx, workerActivitySource(s.c, t.WorkerID), []string{t.ID}, events)
	state, err := awaitFirstLevel(ctx, events, t.ID)
	if err != nil {
		cancel()
		stopFeed()
		return nil, err
	}
	ch := newPersistentWorkerChannel(s.c, t.WorkerID)
	return &replyWaitSession{
		agentID:  t.ID,
		ctx:      ctx,
		cancel:   cancel,
		stopFeed: stopFeed,
		events:   events,
		ch:       ch,
		reader:   channelReplyReader(ch, t.ID),
		state:    state,
	}, nil
}

func (ws *replyWaitSession) Close() {
	ws.cancel()
	ws.stopFeed()
	ws.ch.Close()
}

// drainEvents consumes any buffered activity events before starting a new turn,
// keeping ws.state current and surfacing feed closure or failure early.
func (ws *replyWaitSession) drainEvents() error {
	for {
		select {
		case ev := <-ws.events:
			switch ev.kind {
			case activityFailed:
				return ev.err
			case activityClosed:
				return cli.Newf(cli.CodeNotFound, "agent %s is closed", ws.agentID)
			case activityLevel, activityChange:
				ws.state = ev.state
			case activityTurnEnd, activityStatusChange, activityInputChanged:
			}
		default:
			return nil
		}
	}
}

func (ws *replyWaitSession) sendAndWaitOne(
	s *session,
	t tabTarget,
	message userMessage,
	inputID string,
	delivery sendDeliveryMode,
	all bool,
	timeout time.Duration,
) (agentReplyResult, replyWaitOutcome, retryStage, error) {
	if err := ws.drainEvents(); err != nil {
		return agentReplyResult{}, 0, retryStageBeforeSend, err
	}
	waiting := ws.state == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
	if waiting && !s.inv.Changed("request-id") {
		return agentReplyResult{}, 0, retryStageBeforeSend, newInputWhileWaiting(t.ID)
	}
	baseline, err := ws.reader.latestSeq(ws.ctx)
	if err != nil {
		return agentReplyResult{}, 0, retryStageBeforeSend, err
	}
	request := message.request(t.ID, inputID)
	request.RequireExisting = waiting
	var ack leapmuxv1.EnqueueAgentInputResponse
	if err := ws.ch.CallOnce(ws.ctx, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, request, &ack); err != nil {
		return agentReplyResult{}, 0, retryStageDuringSend, err
	}
	if ack.GetExistingInputMissing() {
		return agentReplyResult{}, 0, retryStageBeforeSend, newInputWhileWaiting(t.ID)
	}
	if ack.GetAcceptedSeq() <= 0 && !queueHolds(ack.GetSnapshot(), inputID) {
		return agentReplyResult{}, 0, retryStageDuringWait, cli.Newf(cli.CodeNotFound, "someone removed the message %s from the input queue of agent %s before the agent received it, so this retry queued nothing", inputID, t.ID).
			WithHint("send the message again with a new --request-id").
			WithExitCode(cli.ExitWaitEnded)
	}
	if delivery != sendDeliveryNormal && ack.GetAcceptedSeq() <= 0 {
		if err := applySendDeliveryModeOnChannel(ws.ctx, ws.ch, t.ID, inputID, delivery); err != nil {
			return agentReplyResult{}, 0, retryStageDuringWait, err
		}
	}
	spin := cli.StartSpinner(s.inv.Printer, fmt.Sprintf("waiting for agent %s (%s)", cli.ShortID(t.ID), agentStateToken(ws.state)))
	defer spin.Stop()
	evaluator := &replyEvaluator{
		reader: ws.reader,
		spec: replyWaitSpec{
			agentID: t.ID, inputID: inputID, baseline: baseline, all: all, timeout: timeout,
			acceptedSeq: ack.GetAcceptedSeq(),
			onActivity: func(ev activityEvent) {
				if ev.kind == activityLevel || ev.kind == activityChange {
					ws.state = ev.state
					spin.Update(fmt.Sprintf("waiting for agent %s (%s)", cli.ShortID(t.ID), agentStateToken(ev.state)))
				}
			},
		},
		notice: func(format string, args ...any) {
			spin.Stop()
			_ = s.inv.Printer.Notice(format, args...)
		},
	}
	res, outcome, err := awaitReply(ws.ctx, quartz.NewReal(), ws.events, evaluator, ws.state)
	spin.Stop()
	return res, outcome, retryStageDuringWait, err
}

type sendAndWaitRequest struct {
	target    tabTarget
	message   userMessage
	requestID string
	inputID   string
	delivery  sendDeliveryMode
	all       bool
	timeout   time.Duration
	schema    *outputSchemaSpec
}

func sendAndWait(s *session, req sendAndWaitRequest) (agentReplyResult, replyWaitOutcome, retryStage, error) {
	if req.schema != nil {
		return sendAndWaitWithSchema(s, req)
	}
	ws, err := openReplyWaitSession(s, req.target)
	if err != nil {
		return agentReplyResult{}, 0, retryStageBeforeSend, err
	}
	defer ws.Close()
	return ws.sendAndWaitOne(s, req.target, req.message, req.inputID, req.delivery, req.all, req.timeout)
}

func newInputWhileWaiting(agentID string) error {
	return cli.Newf(cli.CodeAgentWaiting, "agent %s waits for an answer to a permission prompt, so the message would wait behind it", agentID).
		WithHint("list the prompt with \"leapmux agent requests %s\", answer it, then send the message again", agentID)
}

// enqueueAgentInput sends one EnqueueAgentInput request for the agent t,
// and returns the Worker acknowledgment.
func enqueueAgentInput(ctx context.Context, c control.Peer, t tabTarget, request *leapmuxv1.EnqueueAgentInputRequest) (*leapmuxv1.EnqueueAgentInputResponse, error) {
	ctx, cancel := rpcDeadline(ctx)
	defer cancel()
	var resp leapmuxv1.EnqueueAgentInputResponse
	if err := callInnerRPC(ctx, c, t.WorkerID, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, request, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// queueHolds reports whether queue holds the input inputID.
func queueHolds(queue *leapmuxv1.AgentInputQueueSnapshot, inputID string) bool {
	return slices.ContainsFunc(queue.GetItems(), func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == inputID })
}

// agentReplyEnded reports a wait for a reply that did not end with a settled
// agent: it prints the result, says why on stderr, and returns exit status 6.
func agentReplyEnded(p *cli.Printer, result agentReplyResult, outcome replyWaitOutcome, timeout time.Duration) error {
	disp := dispositionInProgress
	switch outcome {
	case replyWaitSettled, replyWaitWaiting, replyWaitTimedOut:
		disp = dispositionInProgress
	case replyWaitInterrupted:
		disp = "interrupted"
	case replyWaitFailed:
		disp = "failed"
	case replyWaitUnproved:
		disp = dispositionOutcomeUnknown
	}
	queryRequestID := result.RequestID
	if result.statusRequestID != "" {
		queryRequestID = result.statusRequestID
	}
	if queryRequestID != "" {
		result.Receipt = &cli.RetryReceipt{
			RequestID:    queryRequestID,
			Disposition:  disp,
			QueryCommand: buildAgentRequestStatusCmd(result.ID, queryRequestID),
		}
	}
	if err := p.Result(result); err != nil {
		return err
	}
	switch {
	case outcome == replyWaitWaiting:
		_ = p.Notice("agent %s waits for an answer to a permission prompt; list it with \"leapmux agent requests %s\"", result.ID, result.ID)
	case outcome == replyWaitInterrupted:
		_ = p.Notice("the turn for input %s was interrupted", result.InputID)
	case outcome == replyWaitFailed:
		_ = p.Notice("the turn for input %s failed", result.InputID)
	case outcome == replyWaitUnproved:
		_ = p.Notice("the Worker could not prove the outcome of the turn for input %s", result.InputID)
	case result.Message == messageQueued:
		_ = p.Notice("the wait ended after %s. The last queue snapshot for agent %s still held the message", timeout, result.ID)
	default:
		_ = p.Notice("the wait ended after %s. Agent %s received the message and the CLI did not observe it settle", timeout, result.ID)
	}
	if queryRequestID != "" {
		_ = p.Notice("disposition: %s (request_id: %s)", disp, queryRequestID)
		_ = p.Notice("check status: %s", buildAgentRequestStatusCmd(result.ID, queryRequestID))
	}
	return &cli.ExitStatus{Code: cli.ExitWaitEnded}
}
