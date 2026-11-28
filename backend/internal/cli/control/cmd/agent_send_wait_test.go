package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func row(seq int64, id string, source leapmuxv1.MessageSource) *leapmuxv1.AgentChatMessage {
	return &leapmuxv1.AgentChatMessage{Seq: seq, Id: id, Source: source}
}

func userMessageRow(seq int64, id string) *leapmuxv1.AgentChatMessage {
	r := row(seq, id, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER)
	r.MarkType = leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE
	return r
}

func replyRow(seq int64, text string) *leapmuxv1.AgentChatMessage {
	r := row(seq, fmt.Sprintf("reply-%d", seq), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	r.ReplyText = &text
	return r
}

func TestPageRowsAfter(t *testing.T) {
	t.Run("reads from the oldest row without a positive cursor", func(t *testing.T) {
		var asked []*leapmuxv1.ListAgentMessagesRequest
		rows, err := pageRowsAfter(0, func(req *leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
			asked = append(asked, req)
			if len(asked) == 1 {
				return &leapmuxv1.ListAgentMessagesResponse{Messages: []*leapmuxv1.AgentChatMessage{replyRow(1, "a"), replyRow(2, "b")}, HasMore: true}, nil
			}
			return &leapmuxv1.ListAgentMessagesResponse{Messages: []*leapmuxv1.AgentChatMessage{replyRow(3, "c")}}, nil
		})
		require.NoError(t, err)
		require.Len(t, asked, 2)
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, asked[0].GetAnchor())
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, asked[1].GetAnchor())
		assert.Equal(t, int64(2), asked[1].GetCursorSeq())
		assert.EqualValues(t, contracts.MessagePageLimit, asked[0].GetLimit())
		assert.Equal(t, []int64{1, 2, 3}, seqsOf(rows))
	})
	t.Run("pages after a positive cursor and drops a row at or before it", func(t *testing.T) {
		rows, err := pageRowsAfter(7, func(req *leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
			assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, req.GetAnchor())
			assert.Equal(t, int64(7), req.GetCursorSeq())
			return &leapmuxv1.ListAgentMessagesResponse{Messages: []*leapmuxv1.AgentChatMessage{replyRow(7, "old"), replyRow(8, "new")}}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, []int64{8}, seqsOf(rows))
	})
	t.Run("ends at a page that does not move past its cursor", func(t *testing.T) {
		calls := 0
		rows, err := pageRowsAfter(5, func(*leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
			calls++
			return &leapmuxv1.ListAgentMessagesResponse{Messages: []*leapmuxv1.AgentChatMessage{replyRow(5, "same")}, HasMore: true}, nil
		})
		require.NoError(t, err)
		assert.Empty(t, rows)
		assert.Equal(t, 1, calls)
	})
	t.Run("returns the failure of a page", func(t *testing.T) {
		refused := errors.New("the worker refused")
		_, err := pageRowsAfter(0, func(*leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
			return nil, refused
		})
		assert.ErrorIs(t, err, refused)
	})
}

func TestLatestSeqOf(t *testing.T) {
	latest := int64(42)
	assert.Equal(t, int64(42), latestSeqOf(&leapmuxv1.ListAgentMessagesResponse{LatestSeq: &latest, Messages: []*leapmuxv1.AgentChatMessage{replyRow(40, "a")}}),
		"latest_seq states the tail, past a row that the page left out")
	zero := int64(0)
	assert.Equal(t, int64(0), latestSeqOf(&leapmuxv1.ListAgentMessagesResponse{LatestSeq: &zero}), "an agent with no row")
	assert.Equal(t, int64(7), latestSeqOf(&leapmuxv1.ListAgentMessagesResponse{Messages: []*leapmuxv1.AgentChatMessage{replyRow(6, "a"), replyRow(7, "b")}}),
		"a Worker that cannot read the tail still lists the page")
	assert.Equal(t, int64(0), latestSeqOf(&leapmuxv1.ListAgentMessagesResponse{}))
}

func seqsOf(rows []*leapmuxv1.AgentChatMessage) []int64 {
	seqs := make([]int64, 0, len(rows))
	for _, r := range rows {
		seqs = append(seqs, r.GetSeq())
	}
	return seqs
}

func TestJudgeQueued(t *testing.T) {
	queued := &leapmuxv1.QueuedAgentInput{Id: "in-1", State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}
	running := &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: true}

	verdict, err := judgeQueued(working, running, queued)
	require.NoError(t, err)
	assert.Equal(t, replyVerdict{step: replyPending, message: messageQueued}, verdict)

	verdict, err = judgeQueued(waiting, running, queued)
	require.NoError(t, err)
	assert.Equal(t, replyWaitingForUser, verdict.step, "the agent waits for the user before it reads the queue")
	assert.Equal(t, messageQueued, verdict.message)

	paused := &leapmuxv1.AgentInputQueueSnapshot{PauseReason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED}
	verdict, err = judgeQueued(idle, paused, queued)
	require.NoError(t, err)
	assert.Equal(t, replyPending, verdict.step, "a planned restart pauses the queue too, and the pause ends by itself")
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, verdict.paused)

	steer := &leapmuxv1.QueuedAgentInput{Id: "in-1", State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO}
	verdict, err = judgeQueued(working, running, steer)
	require.NoError(t, err)
	assert.Equal(t, replyPending, verdict.step, "a steer waits for its echo before its row lands")

	failed := &leapmuxv1.QueuedAgentInput{Id: "in-1", State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED, Error: "the provider refused the input"}
	_, err = judgeQueued(idle, paused, failed)
	e := cli.FromError(err)
	require.NotNil(t, e)
	assert.Equal(t, cli.CodeInternal, e.Code)
	assert.Contains(t, e.Message, "the provider refused the input")
	assert.NotEmpty(t, e.Hints)
}

func TestJudgeDelivered(t *testing.T) {
	rows := []*leapmuxv1.AgentChatMessage{
		replyRow(10, "an earlier reply"),
		userMessageRow(11, "in-1"),
		replyRow(12, "First."),
		replyRow(13, "Second."),
	}
	idleQueue := &leapmuxv1.AgentInputQueueSnapshot{}
	for name, tc := range map[string]struct {
		state leapmuxv1.AgentActivityState
		queue *leapmuxv1.AgentInputQueueSnapshot
		want  replyStep
	}{
		"settled":                       {idle, idleQueue, replySettled},
		"a turn still runs":             {idle, &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: true}, replyPending},
		"a background task still works": {working, idleQueue, replyPending},
		"the agent waits for the user":  {waiting, &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: true}, replyWaitingForUser},
		"an older worker states none":   {leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED, idleQueue, replySettled},
	} {
		t.Run(name, func(t *testing.T) {
			verdict, err := judgeDelivered(tc.state, tc.queue, rows, "in-1", false)
			require.NoError(t, err)
			assert.Equal(t, tc.want, verdict.step)
			assert.Equal(t, messageDelivered, verdict.message)
			assert.Equal(t, []string{"Second."}, verdict.replies, "the replies so far come with every verdict")
		})
	}

	verdict, err := judgeDelivered(idle, idleQueue, rows, "in-1", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"First.", "Second."}, verdict.replies)

	_, err = judgeDelivered(idle, idleQueue, rows, "in-2", false)
	e := cli.FromError(err)
	require.NotNil(t, e)
	assert.Equal(t, cli.CodeNotFound, e.Code)
	assert.Equal(t, cli.ExitWaitEnded, e.ExitCode(), "the message was removed, so the wait ended without its condition")
}

func TestRepliesAfter(t *testing.T) {
	subagent := replyRow(3, "the subagent's answer")
	subagent.ParentSpanId = "span-task"
	answer := row(4, "answer", leapmuxv1.MessageSource_MESSAGE_SOURCE_USER)
	answer.MarkType = leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE
	noText := row(5, "tool-call", leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	nestedUser := userMessageRow(7, "nested")
	nestedUser.ParentSpanId = "span-task"
	rows := []*leapmuxv1.AgentChatMessage{
		replyRow(2, "Looking."),
		subagent,
		answer,
		noText,
		replyRow(6, "Done."),
		nestedUser,
		replyRow(8, "Really done."),
		userMessageRow(9, "in-2"),
		replyRow(10, "Another message's reply."),
	}
	assert.Equal(t, []string{"Looking.", "Done.", "Really done."}, repliesAfter(rows, true),
		"a subagent's row is skipped, and the answer to a prompt does not end the turn")
	assert.Equal(t, []string{"Really done."}, repliesAfter(rows, false))

	none := repliesAfter([]*leapmuxv1.AgentChatMessage{noText}, false)
	assert.NotNil(t, none, "no reply is an empty list, not null, in --json")
	assert.Empty(t, none)
	assert.Empty(t, repliesAfter(nil, true))
}

// fakeReplyWorker answers the reads of a replyEvaluator from its fields and
// records each read. mu guards every field, because a test changes the
// fields while awaitReply reads them on its goroutine.
type fakeReplyWorker struct {
	mu       sync.Mutex
	queue    *leapmuxv1.AgentInputQueueSnapshot
	rows     []*leapmuxv1.AgentChatMessage
	queueErr error
	rowsErr  error
	reads    []string
}

func (w *fakeReplyWorker) reader() replyReader {
	return replyReader{
		readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.reads = append(w.reads, "queue")
			return &leapmuxv1.ListAgentInputQueueResponse{Snapshot: w.queue}, w.queueErr
		},
		readRowsAfter: func(_ context.Context, afterSeq int64, _ string) ([]*leapmuxv1.AgentChatMessage, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.reads = append(w.reads, fmt.Sprintf("rows after %d", afterSeq))
			return w.rows, w.rowsErr
		},
		latestSeq: func(context.Context) (int64, error) { return 0, nil },
	}
}

// deliver states that the Worker accepted the message: the queue no longer
// holds it, and rows are the transcript after the baseline.
func (w *fakeReplyWorker) deliver(queue *leapmuxv1.AgentInputQueueSnapshot, rows ...*leapmuxv1.AgentChatMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.queue, w.rows = queue, rows
}

func queuedSnapshot(pause leapmuxv1.AgentInputQueuePauseReason) *leapmuxv1.AgentInputQueueSnapshot {
	return &leapmuxv1.AgentInputQueueSnapshot{
		PauseReason: pause,
		Items:       []*leapmuxv1.QueuedAgentInput{{Id: "in-1", State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}},
	}
}

func TestReplyEvaluator_ReadsTheTranscriptOnlyAfterTheMessageLeftTheQueue(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	w := &fakeReplyWorker{queue: queuedSnapshot(0)}
	var notices []string
	e := &replyEvaluator{reader: w.reader(), spec: replyWaitSpec{agentID: "a-1", inputID: "in-1", baseline: 9},
		notice: func(format string, args ...any) { notices = append(notices, fmt.Sprintf(format, args...)) }}

	verdict, err := e.evaluate(ctx, working)
	require.NoError(t, err)
	assert.Equal(t, replyPending, verdict.step)
	assert.Equal(t, []string{"queue"}, w.reads)

	w.deliver(&leapmuxv1.AgentInputQueueSnapshot{}, userMessageRow(10, "in-1"), replyRow(11, "Hi."))
	verdict, err = e.evaluate(ctx, idle)
	require.NoError(t, err)
	assert.Equal(t, replySettled, verdict.step)
	assert.Equal(t, []string{"Hi."}, verdict.replies)
	assert.Equal(t, []string{"queue", "queue", "rows after 9"}, w.reads, "the queue first, then the rows after the baseline")
	assert.Empty(t, notices)
}

func TestReplyEvaluator_SaysOnceWhyTheMessageWaitsInAPausedQueue(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	w := &fakeReplyWorker{queue: queuedSnapshot(leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)}
	var notices []string
	e := &replyEvaluator{reader: w.reader(), spec: replyWaitSpec{agentID: "a-1", inputID: "in-1"},
		notice: func(format string, args ...any) { notices = append(notices, fmt.Sprintf(format, args...)) }}

	for range 2 {
		verdict, err := e.evaluate(ctx, idle)
		require.NoError(t, err)
		assert.Equal(t, replyPending, verdict.step)
	}
	assert.Equal(t, []string{"the input queue of agent a-1 is paused (manual); the message waits until it resumes"}, notices)

	w.deliver(queuedSnapshot(0))
	_, err := e.evaluate(ctx, idle)
	require.NoError(t, err)
	w.deliver(queuedSnapshot(leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED))
	_, err = e.evaluate(ctx, idle)
	require.NoError(t, err)
	assert.Len(t, notices, 2, "a new pause after a resume says why again")
	assert.Contains(t, notices[1], "(interrupted)")
}

func TestReplyEvaluator_ReturnsTheFailureOfARead(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	refused := errors.New("the worker refused")
	w := &fakeReplyWorker{queueErr: refused}
	e := &replyEvaluator{reader: w.reader(), spec: replyWaitSpec{inputID: "in-1"}}
	_, err := e.evaluate(ctx, idle)
	assert.ErrorIs(t, err, refused)

	w = &fakeReplyWorker{queue: &leapmuxv1.AgentInputQueueSnapshot{}, rowsErr: refused}
	e = &replyEvaluator{reader: w.reader(), spec: replyWaitSpec{inputID: "in-1"}}
	_, err = e.evaluate(ctx, idle)
	assert.ErrorIs(t, err, refused)
}

// replyWaitRun runs awaitReply on a goroutine and returns its outcome.
type replyWaitRun struct {
	result  agentReplyResult
	outcome replyWaitOutcome
	err     error
}

func TestAwaitReply_SettlesAfterTheMessage(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	w := &fakeReplyWorker{queue: queuedSnapshot(0)}
	e := &replyEvaluator{reader: w.reader(), spec: replyWaitSpec{agentID: "a-1", inputID: "in-1"}, notice: func(string, ...any) {}}
	events := make(chan activityEvent)
	done := make(chan replyWaitRun, 1)
	go func() {
		result, outcome, err := awaitReply(ctx, testutil.NewQuartzMock(t), events, e, idle)
		done <- replyWaitRun{result, outcome, err}
	}()

	// The agent works on an earlier turn, and the message still waits.
	events <- change("a-1", working)
	events <- activityEvent{kind: activityTurnEnd, agentID: "a-1"}
	// The Worker accepted the message, and its turn ended. A status change
	// wakes an evaluation, but the published state still says working.
	w.deliver(&leapmuxv1.AgentInputQueueSnapshot{}, userMessageRow(4, "in-1"), replyRow(5, "All tests pass."))
	events <- activityEvent{kind: activityStatusChange, agentID: "a-1"}
	// The settle ends the wait.
	events <- change("a-1", idle)

	got := receive(t, ctx, done)
	require.NoError(t, got.err)
	assert.Equal(t, replyWaitSettled, got.outcome)
	assert.Equal(t, agentReplyResult{ID: "a-1", InputID: "in-1", Message: messageDelivered, State: "idle", Replies: []string{"All tests pass."}}, got.result)
}

func TestAwaitReply_EndsWhenTheAgentWaitsForTheUser(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	w := &fakeReplyWorker{queue: &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: true},
		rows: []*leapmuxv1.AgentChatMessage{userMessageRow(4, "in-1"), replyRow(5, "May I run the tests?")}}
	e := &replyEvaluator{reader: w.reader(), spec: replyWaitSpec{agentID: "a-1", inputID: "in-1"}, notice: func(string, ...any) {}}
	result, outcome, err := awaitReply(ctx, testutil.NewQuartzMock(t), feedEvents(change("a-1", waiting)), e, working)
	require.NoError(t, err)
	assert.Equal(t, replyWaitWaiting, outcome)
	assert.Equal(t, agentReplyResult{ID: "a-1", InputID: "in-1", Message: messageDelivered, State: "waiting_for_user", Replies: []string{"May I run the tests?"}}, result)
}

func TestAwaitReply_DeadlineCancelsAnInFlightQueueRead(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	readStarted := make(chan context.Context, 1)
	releaseRead := make(chan struct{})
	reads := 0
	reader := replyReader{readQueue: func(ctx context.Context, _ string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
		reads++
		if reads == 1 {
			return &leapmuxv1.ListAgentInputQueueResponse{Snapshot: queuedSnapshot(0)}, nil
		}
		readStarted <- ctx
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-releaseRead:
			return &leapmuxv1.ListAgentInputQueueResponse{Snapshot: queuedSnapshot(0)}, nil
		}
	}}
	e := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "a-1", inputID: "in-1", timeout: time.Minute}}
	events := feedEvents(activityEvent{kind: activityStatusChange, agentID: "a-1"})
	done := make(chan replyWaitRun, 1)
	go func() {
		result, outcome, err := awaitReply(ctx, clock, events, e, working)
		done <- replyWaitRun{result: result, outcome: outcome, err: err}
	}()
	readCtx := receive(t, ctx, readStarted)
	clock.Advance(time.Minute).MustWait(ctx)
	assert.ErrorIs(t, readCtx.Err(), context.Canceled)
	close(releaseRead)
	got := receive(t, ctx, done)
	require.NoError(t, got.err)
	assert.Equal(t, replyWaitTimedOut, got.outcome)
	assert.Equal(t, agentReplyResult{ID: "a-1", InputID: "in-1", State: "working", Message: messageQueued, Replies: []string{}}, got.result)
}

func TestAwaitReply_EndsWhenTheTimeoutPasses(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().AfterFunc(replyWaitTimerTag)
	defer newTimer.Close()
	w := &fakeReplyWorker{queue: queuedSnapshot(0)}
	baseReader := w.reader()
	evaluated := make(chan struct{}, 1)
	reader := baseReader
	reader.readQueue = func(ctx context.Context, inputID string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
		resp, err := baseReader.readQueue(ctx, inputID)
		select {
		case evaluated <- struct{}{}:
		default:
		}
		return resp, err
	}
	e := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "a-1", inputID: "in-1", timeout: time.Minute}, notice: func(string, ...any) {}}
	done := make(chan replyWaitRun, 1)
	go func() {
		result, outcome, err := awaitReply(ctx, clock, make(chan activityEvent), e, working)
		done <- replyWaitRun{result, outcome, err}
	}()
	assert.Equal(t, time.Minute, testutil.WaitForTimer(t, ctx, newTimer))
	receive(t, ctx, evaluated)
	clock.Advance(time.Minute).MustWait(ctx)

	got := receive(t, ctx, done)
	require.NoError(t, got.err)
	assert.Equal(t, replyWaitTimedOut, got.outcome)
	assert.Equal(t, agentReplyResult{ID: "a-1", InputID: "in-1", Message: messageQueued, State: "working", Replies: []string{}}, got.result)
}

func TestAwaitReply_DeadlineCancelsTheAcceptedReplyRead(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	readStarted := make(chan int64, 1)
	reader := replyReader{
		readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
			return &leapmuxv1.ListAgentInputQueueResponse{}, nil
		},
		readRowsAfter: func(ctx context.Context, afterSeq int64, _ string) ([]*leapmuxv1.AgentChatMessage, error) {
			readStarted <- afterSeq
			<-ctx.Done()
			return nil, context.Cause(ctx)
		},
	}
	verdicts := make(chan replyWaitRun, 1)
	go func() {
		e := &replyEvaluator{reader: reader, spec: replyWaitSpec{
			agentID: "a-1", inputID: "in-1", timeout: time.Minute, acceptedSeq: 42,
		}}
		result, outcome, err := awaitReply(ctx, clock, make(chan activityEvent), e, working)
		verdicts <- replyWaitRun{result: result, outcome: outcome, err: err}
	}()
	assert.EqualValues(t, 41, receive(t, ctx, readStarted))
	clock.Advance(time.Minute).MustWait(ctx)
	got := receive(t, ctx, verdicts)
	require.NoError(t, got.err)
	assert.Equal(t, replyWaitTimedOut, got.outcome)
	assert.Equal(t, agentReplyResult{ID: "a-1", InputID: "in-1", Message: messageDelivered, State: "working", Replies: []string{}}, got.result)
}

func TestReplyEvaluatorUsesTheTrackedTurnState(t *testing.T) {
	for name, test := range map[string]struct {
		outcome      leapmuxv1.AgentInputTurnOutcome
		waiting      bool
		want         replyStep
		wantRowReads int
		wantReplies  []string
	}{
		"pending on an idle tab": {want: replyPending, wantRowReads: 0, wantReplies: nil},
		"own prompt":             {waiting: true, want: replyWaitingForUser, wantRowReads: 1, wantReplies: []string{"Partial reply"}},
		"completed":              {outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, want: replySettled, wantRowReads: 1, wantReplies: []string{"Partial reply"}},
		"interrupted":            {outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, want: replyInterrupted, wantRowReads: 1, wantReplies: []string{"Partial reply"}},
		"failed":                 {outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, want: replyFailed, wantRowReads: 1, wantReplies: []string{"Partial reply"}},
		"unproved":               {outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, want: replyUnproved, wantRowReads: 1, wantReplies: []string{"Partial reply"}},
	} {
		t.Run(name, func(t *testing.T) {
			rowReads := 0
			reader := replyReader{
				readQueue: func(_ context.Context, inputID string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
					assert.Equal(t, "input", inputID)
					return &leapmuxv1.ListAgentInputQueueResponse{
						InputReply: &leapmuxv1.AgentInputTurnReply{AcceptedSeq: 10, Outcome: test.outcome, WaitingForUser: test.waiting},
					}, nil
				},
				readRowsAfter: func(_ context.Context, afterSeq int64, inputID string) ([]*leapmuxv1.AgentChatMessage, error) {
					rowReads++
					assert.EqualValues(t, 10, afterSeq)
					assert.Equal(t, "input", inputID)
					return []*leapmuxv1.AgentChatMessage{replyRow(11, "Partial reply")}, nil
				},
			}
			e := &replyEvaluator{reader: reader, spec: replyWaitSpec{inputID: "input"}}
			verdict, err := e.evaluate(t.Context(), idle)
			require.NoError(t, err)
			assert.Equal(t, test.want, verdict.step)
			assert.Equal(t, messageDelivered, verdict.message)
			assert.Equal(t, test.wantRowReads, rowReads)
			assert.Equal(t, test.wantReplies, verdict.replies)
		})
	}
}

func TestAwaitReplyWakesOnTheTrackedInputsCompletion(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	started := make(chan struct{}, 1)
	completed := make(chan struct{})
	events := make(chan activityEvent, 1)
	reader := replyReader{
		readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
			reply := &leapmuxv1.AgentInputTurnReply{AcceptedSeq: 10}
			select {
			case <-completed:
				reply.Outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
			default:
				started <- struct{}{}
			}
			return &leapmuxv1.ListAgentInputQueueResponse{InputReply: reply}, nil
		},
		readRowsAfter: func(context.Context, int64, string) ([]*leapmuxv1.AgentChatMessage, error) {
			return []*leapmuxv1.AgentChatMessage{replyRow(11, "The submitted reply.")}, nil
		},
	}
	evaluator := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "agent", inputID: "input"}}
	verdicts := make(chan replyWaitRun, 1)
	go func() {
		result, outcome, err := awaitReply(ctx, testutil.NewQuartzMock(t), events, evaluator, working)
		verdicts <- replyWaitRun{result: result, outcome: outcome, err: err}
	}()
	receive(t, ctx, started)
	close(completed)
	events <- activityEvent{kind: activityInputChanged, agentID: "agent"}
	got := receive(t, ctx, verdicts)
	require.NoError(t, got.err)
	assert.Equal(t, replyWaitSettled, got.outcome)
	assert.Equal(t, "working", got.result.State)
	assert.Equal(t, "completed", got.result.TurnOutcome)
	assert.Equal(t, []string{"The submitted reply."}, got.result.Replies)
}

func TestAwaitReplyReportsTheExactPromptBeforeTheActivityChange(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	reader := replyReader{
		readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
			return &leapmuxv1.ListAgentInputQueueResponse{InputReply: &leapmuxv1.AgentInputTurnReply{
				AcceptedSeq: 10, WaitingForUser: true,
			}}, nil
		},
		readRowsAfter: func(context.Context, int64, string) ([]*leapmuxv1.AgentChatMessage, error) {
			return nil, nil
		},
	}
	evaluator := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "agent", inputID: "input"}}
	// The Worker stored the prompt, but the activity watch has not delivered the change.
	result, outcome, err := awaitReply(ctx, testutil.NewQuartzMock(t), make(chan activityEvent), evaluator, working)
	require.NoError(t, err)
	assert.Equal(t, replyWaitWaiting, outcome)
	assert.Equal(t, messageDelivered, result.Message)
	assert.Equal(t, "waiting_for_user", result.State)
}

func TestAwaitReply_EndsWhenTheAgentCloses(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	w := &fakeReplyWorker{queue: queuedSnapshot(0)}
	e := &replyEvaluator{reader: w.reader(), spec: replyWaitSpec{agentID: "a-1", inputID: "in-1"}, notice: func(string, ...any) {}}
	_, _, err := awaitReply(ctx, testutil.NewQuartzMock(t), feedEvents(activityEvent{kind: activityClosed, agentID: "a-1"}), e, working)
	ce := cli.FromError(err)
	require.NotNil(t, ce)
	assert.Equal(t, cli.CodeNotFound, ce.Code)
	assert.Equal(t, cli.ExitWaitEnded, ce.ExitCode())
}

func TestAwaitReply_ReturnsTheFailureOfTheFeedOrTheContext(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	refused := errors.New("the worker refused")
	w := &fakeReplyWorker{queue: queuedSnapshot(0)}
	e := &replyEvaluator{reader: w.reader(), spec: replyWaitSpec{agentID: "a-1", inputID: "in-1"}, notice: func(string, ...any) {}}
	_, _, err := awaitReply(ctx, testutil.NewQuartzMock(t), feedEvents(activityEvent{kind: activityFailed, err: refused}), e, working)
	assert.ErrorIs(t, err, refused)

	canceled, cancel := context.WithCancelCause(ctx)
	cancel(cli.ErrInterrupted)
	_, _, err = awaitReply(canceled, testutil.NewQuartzMock(t), make(chan activityEvent), e, working)
	assert.ErrorIs(t, err, cli.ErrInterrupted)
}

func TestAwaitFirstLevel(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	state, err := awaitFirstLevel(ctx, feedEvents(change("a-1", idle), activityEvent{kind: activityTurnEnd, agentID: "a-1"}, level("a-1", working)), "a-1")
	require.NoError(t, err)
	assert.Equal(t, working, state, "the level replaces the older change before it")

	_, err = awaitFirstLevel(ctx, feedEvents(activityEvent{kind: activityClosed, agentID: "a-1"}), "a-1")
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	refused := errors.New("the worker refused")
	_, err = awaitFirstLevel(ctx, feedEvents(activityEvent{kind: activityFailed, err: refused}), "a-1")
	assert.ErrorIs(t, err, refused)
}

func TestAgentReplyResult_RenderText(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, agentReplyResult{Replies: []string{}}.RenderText(&out))
	assert.Empty(t, out.String(), "no reply prints nothing")

	out.Reset()
	require.NoError(t, agentReplyResult{Replies: []string{"First.", "Second."}}.RenderText(&out))
	assert.Equal(t, "First.\n\nSecond.\n", out.String())
}

func TestAgentReplyEnded(t *testing.T) {
	for name, tc := range map[string]struct {
		result  agentReplyResult
		outcome replyWaitOutcome
		notice  string
	}{
		"waiting":          {agentReplyResult{ID: "a-1", Message: messageDelivered, State: "waiting_for_user"}, replyWaitWaiting, "agent a-1 waits for an answer to a permission prompt; list it with \"leapmux agent requests a-1\"\n"},
		"timeout, queued":  {agentReplyResult{ID: "a-1", Message: messageQueued, State: "working"}, replyWaitTimedOut, "the wait ended after 2m0s. The last queue snapshot for agent a-1 still held the message\n"},
		"timeout, working": {agentReplyResult{ID: "a-1", Message: messageDelivered, State: "working"}, replyWaitTimedOut, "the wait ended after 2m0s. Agent a-1 received the message and the CLI did not observe it settle\n"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			p := &cli.Printer{Out: &out, Err: &errOut, Format: cli.FormatJSON}
			tc.result.Replies = []string{}
			err := agentReplyEnded(p, tc.result, tc.outcome, 2*time.Minute)
			var status *cli.ExitStatus
			require.ErrorAs(t, err, &status)
			assert.Equal(t, cli.ExitWaitEnded, status.Code)
			assert.Contains(t, out.String(), `"message":"`+tc.result.Message+`"`, "the result prints, so a script reads where the message is")
			assert.Equal(t, tc.notice, errOut.String())
		})
	}
}

func TestPageExactInputReply_MidPagingCanonicalTransitionRestartsCleanly(t *testing.T) {
	t.Parallel()
	call := 0
	rows, err := pageExactInputReply(func(req *leapmuxv1.ListAgentMessagesRequest) (*leapmuxv1.ListAgentMessagesResponse, error) {
		call++
		switch call {
		case 1:
			assert.Nil(t, req.ReplyCursorCanonical)
			assert.Zero(t, req.GetReplyCursor())
			return &leapmuxv1.ListAgentMessagesResponse{
				Messages:        []*leapmuxv1.AgentChatMessage{{Id: "pre-1", Seq: 50}},
				HasMore:         true,
				NextReplyCursor: 50,
				ReplyCanonical:  false,
			}, nil
		case 2:
			require.NotNil(t, req.ReplyCursorCanonical)
			assert.False(t, *req.ReplyCursorCanonical)
			assert.Equal(t, int64(50), req.GetReplyCursor())
			// Turn became canonical between page 1 and page 2: Worker restarted at ordinal 0.
			return &leapmuxv1.ListAgentMessagesResponse{
				Messages:        []*leapmuxv1.AgentChatMessage{{Id: "canon-1", Seq: 50}},
				HasMore:         true,
				NextReplyCursor: 1,
				ReplyCanonical:  true,
			}, nil
		case 3:
			require.NotNil(t, req.ReplyCursorCanonical)
			assert.True(t, *req.ReplyCursorCanonical)
			assert.Equal(t, int64(1), req.GetReplyCursor())
			return &leapmuxv1.ListAgentMessagesResponse{
				Messages:       []*leapmuxv1.AgentChatMessage{{Id: "canon-2", Seq: 51}},
				HasMore:        false,
				ReplyCanonical: true,
			}, nil
		default:
			t.Fatalf("unexpected page call %d", call)
			return nil, nil
		}
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "canon-1", rows[0].GetId())
	assert.Equal(t, "canon-2", rows[1].GetId())
}

func TestAwaitReply_PreservesSettledVerdictAndRealErrorsWhenTimeoutFiresSimultaneously(t *testing.T) {
	t.Run("settled verdict returned as timeout expires is preserved", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		reader := replyReader{
			readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
				// Expire the wait context before returning a settled outcome.
				clock.Advance(time.Minute).MustWait(ctx)
				return &leapmuxv1.ListAgentInputQueueResponse{
					InputReply: &leapmuxv1.AgentInputTurnReply{
						AcceptedSeq: 10,
						Outcome:     leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
					},
				}, nil
			},
			readRowsAfter: func(context.Context, int64, string) ([]*leapmuxv1.AgentChatMessage, error) {
				return []*leapmuxv1.AgentChatMessage{replyRow(11, "Finished just in time.")}, nil
			},
		}
		e := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "a-1", inputID: "in-1", timeout: time.Minute}}
		result, outcome, err := awaitReply(ctx, clock, make(chan activityEvent), e, working)
		require.NoError(t, err)
		assert.Equal(t, replyWaitSettled, outcome)
		assert.Equal(t, []string{"Finished just in time."}, result.Replies)
	})

	t.Run("non-context error returned as timeout expires is not discarded", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		removedErr := cli.Newf(cli.CodeNotFound, "removed from queue")
		reader := replyReader{
			readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
				clock.Advance(time.Minute).MustWait(ctx)
				return nil, removedErr
			},
		}
		e := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "a-1", inputID: "in-1", timeout: time.Minute}}
		_, _, err := awaitReply(ctx, clock, make(chan activityEvent), e, working)
		assert.ErrorIs(t, err, removedErr)
	})
}

func TestAwaitReply_SkipsTranscriptReadsWhilePending(t *testing.T) {
	t.Run("exact path skips pending events and reads once on completion", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		var mu sync.Mutex
		outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
		rowReads := 0
		queueReads := make(chan struct{}, 8)
		reader := replyReader{
			readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
				mu.Lock()
				cur := outcome
				mu.Unlock()
				queueReads <- struct{}{}
				return &leapmuxv1.ListAgentInputQueueResponse{
					InputReply: &leapmuxv1.AgentInputTurnReply{AcceptedSeq: 10, Outcome: cur},
				}, nil
			},
			readRowsAfter: func(context.Context, int64, string) ([]*leapmuxv1.AgentChatMessage, error) {
				mu.Lock()
				rowReads++
				mu.Unlock()
				return []*leapmuxv1.AgentChatMessage{replyRow(11, "Final exact reply.")}, nil
			},
		}
		events := make(chan activityEvent, 4)
		e := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "a-1", inputID: "in-1"}}
		done := make(chan replyWaitRun, 1)
		go func() {
			res, out, err := awaitReply(ctx, testutil.NewQuartzMock(t), events, e, working)
			done <- replyWaitRun{res, out, err}
		}()

		receive(t, ctx, queueReads)
		events <- activityEvent{kind: activityInputChanged, agentID: "a-1"}
		receive(t, ctx, queueReads)
		events <- activityEvent{kind: activityStatusChange, agentID: "a-1"}
		receive(t, ctx, queueReads)

		mu.Lock()
		assert.Equal(t, 0, rowReads, "readRowsAfter must not run during pending exact evaluations")
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
		mu.Unlock()

		events <- activityEvent{kind: activityInputChanged, agentID: "a-1"}
		got := receive(t, ctx, done)
		require.NoError(t, got.err)
		assert.Equal(t, replyWaitSettled, got.outcome)
		assert.Equal(t, []string{"Final exact reply."}, got.result.Replies)
		mu.Lock()
		assert.Equal(t, 1, rowReads, "readRowsAfter should run exactly once when completed")
		mu.Unlock()
	})

	t.Run("exact path fetches partial replies once on timeout", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		newTimer := clock.Trap().AfterFunc(replyWaitTimerTag)
		defer newTimer.Close()
		var mu sync.Mutex
		rowReads := 0
		queueReads := make(chan struct{}, 4)
		reader := replyReader{
			readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
				queueReads <- struct{}{}
				return &leapmuxv1.ListAgentInputQueueResponse{
					InputReply: &leapmuxv1.AgentInputTurnReply{AcceptedSeq: 10},
				}, nil
			},
			readRowsAfter: func(context.Context, int64, string) ([]*leapmuxv1.AgentChatMessage, error) {
				mu.Lock()
				rowReads++
				mu.Unlock()
				return []*leapmuxv1.AgentChatMessage{replyRow(11, "Partial exact reply.")}, nil
			},
		}
		events := make(chan activityEvent, 4)
		e := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "a-1", inputID: "in-1", timeout: time.Minute}}
		done := make(chan replyWaitRun, 1)
		go func() {
			res, out, err := awaitReply(ctx, clock, events, e, working)
			done <- replyWaitRun{res, out, err}
		}()

		assert.Equal(t, time.Minute, testutil.WaitForTimer(t, ctx, newTimer))
		receive(t, ctx, queueReads)
		events <- activityEvent{kind: activityInputChanged, agentID: "a-1"}
		receive(t, ctx, queueReads)
		mu.Lock()
		assert.Equal(t, 0, rowReads)
		mu.Unlock()

		clock.Advance(time.Minute).MustWait(ctx)
		got := receive(t, ctx, done)
		require.NoError(t, got.err)
		assert.Equal(t, replyWaitTimedOut, got.outcome)
		assert.Equal(t, messageDelivered, got.result.Message)
		assert.Equal(t, []string{"Partial exact reply."}, got.result.Replies)
		mu.Lock()
		assert.Equal(t, 1, rowReads, "timeout should fetch partial replies once")
		mu.Unlock()
	})

	t.Run("legacy path skips intermediate transcript reads after confirming delivery", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		var mu sync.Mutex
		activeTurn := true
		rowReads := 0
		queueReads := make(chan struct{}, 8)
		reader := replyReader{
			readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
				mu.Lock()
				at := activeTurn
				mu.Unlock()
				queueReads <- struct{}{}
				return &leapmuxv1.ListAgentInputQueueResponse{
					Snapshot: &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: at},
				}, nil
			},
			readRowsAfter: func(context.Context, int64, string) ([]*leapmuxv1.AgentChatMessage, error) {
				mu.Lock()
				rowReads++
				mu.Unlock()
				return []*leapmuxv1.AgentChatMessage{userMessageRow(4, "in-1"), replyRow(5, "Legacy final reply.")}, nil
			},
		}
		events := make(chan activityEvent, 4)
		e := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "a-1", inputID: "in-1"}}
		done := make(chan replyWaitRun, 1)
		go func() {
			res, out, err := awaitReply(ctx, testutil.NewQuartzMock(t), events, e, working)
			done <- replyWaitRun{res, out, err}
		}()

		// First evaluation confirms inputID in transcript.
		receive(t, ctx, queueReads)
		// Subsequent pending events while working/activeTurn must skip readRowsAfter.
		events <- activityEvent{kind: activityStatusChange, agentID: "a-1"}
		receive(t, ctx, queueReads)
		events <- activityEvent{kind: activityInputChanged, agentID: "a-1"}
		receive(t, ctx, queueReads)

		mu.Lock()
		assert.Equal(t, 1, rowReads, "only the initial delivery confirmation should have read rows")
		activeTurn = false
		mu.Unlock()

		events <- change("a-1", idle)
		got := receive(t, ctx, done)
		require.NoError(t, got.err)
		assert.Equal(t, replyWaitSettled, got.outcome)
		assert.Equal(t, []string{"Legacy final reply."}, got.result.Replies)
		mu.Lock()
		assert.Equal(t, 2, rowReads, "one read to confirm delivery and one final read on settle")
		mu.Unlock()
	})

	t.Run("legacy path fetches partial replies once on timeout", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		newTimer := clock.Trap().AfterFunc(replyWaitTimerTag)
		defer newTimer.Close()
		var mu sync.Mutex
		rowReads := 0
		rows := []*leapmuxv1.AgentChatMessage{userMessageRow(4, "in-1")}
		queueReads := make(chan struct{}, 8)
		reader := replyReader{
			readQueue: func(context.Context, string) (*leapmuxv1.ListAgentInputQueueResponse, error) {
				queueReads <- struct{}{}
				return &leapmuxv1.ListAgentInputQueueResponse{
					Snapshot: &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: true},
				}, nil
			},
			readRowsAfter: func(context.Context, int64, string) ([]*leapmuxv1.AgentChatMessage, error) {
				mu.Lock()
				defer mu.Unlock()
				rowReads++
				return rows, nil
			},
		}
		events := make(chan activityEvent, 4)
		e := &replyEvaluator{reader: reader, spec: replyWaitSpec{agentID: "a-1", inputID: "in-1", timeout: time.Minute}}
		done := make(chan replyWaitRun, 1)
		go func() {
			res, out, err := awaitReply(ctx, clock, events, e, working)
			done <- replyWaitRun{res, out, err}
		}()

		assert.Equal(t, time.Minute, testutil.WaitForTimer(t, ctx, newTimer))
		// First evaluation confirms delivery via readRowsAfter (no reply yet).
		receive(t, ctx, queueReads)
		// Second evaluation skips readRowsAfter (sets skippedReplies=true).
		events <- activityEvent{kind: activityStatusChange, agentID: "a-1"}
		receive(t, ctx, queueReads)

		mu.Lock()
		assert.Equal(t, 1, rowReads, "intermediate pending event must skip readRowsAfter")
		rows = []*leapmuxv1.AgentChatMessage{userMessageRow(4, "in-1"), replyRow(5, "Legacy partial reply.")}
		mu.Unlock()

		clock.Advance(time.Minute).MustWait(ctx)
		got := receive(t, ctx, done)
		require.NoError(t, got.err)
		assert.Equal(t, replyWaitTimedOut, got.outcome)
		assert.Equal(t, messageDelivered, got.result.Message)
		assert.Equal(t, []string{"Legacy partial reply."}, got.result.Replies)
		mu.Lock()
		assert.Equal(t, 2, rowReads, "timeout must fetch legacy partial replies once after skipped pending reads")
		mu.Unlock()
	})
}

func TestAwaitReply_InvokesOnActivityCallback(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	w := &fakeReplyWorker{queue: queuedSnapshot(0)}
	var observed []activityEventKind
	e := &replyEvaluator{
		reader: w.reader(),
		spec: replyWaitSpec{
			agentID: "a-1",
			inputID: "in-1",
			onActivity: func(ev activityEvent) {
				observed = append(observed, ev.kind)
			},
		},
		notice: func(string, ...any) {},
	}
	events := make(chan activityEvent)
	done := make(chan replyWaitRun, 1)
	go func() {
		res, out, err := awaitReply(ctx, testutil.NewQuartzMock(t), events, e, idle)
		done <- replyWaitRun{res, out, err}
	}()

	events <- change("a-1", working)
	w.deliver(&leapmuxv1.AgentInputQueueSnapshot{}, userMessageRow(4, "in-1"), replyRow(5, "Done."))
	events <- change("a-1", idle)

	got := receive(t, ctx, done)
	require.NoError(t, got.err)
	assert.Equal(t, replyWaitSettled, got.outcome)
	assert.Equal(t, []activityEventKind{activityChange, activityChange}, observed)
}

func TestFormatSanitizedReplies(t *testing.T) {
	assert.Equal(t, "", formatSanitizedReplies(nil))
	assert.Equal(t, "Hello", formatSanitizedReplies([]string{"Hello"}))
	assert.Equal(t, "First\n\nSecond", formatSanitizedReplies([]string{"First", "Second"}))
	assert.Equal(t, "clean", formatSanitizedReplies([]string{"\x1b[31mclean\x1b[0m"}))
}

func TestAgentReplyEnded_UsesStatusRequestIDWhenPresent(t *testing.T) {
	var out, errOut bytes.Buffer
	p := &cli.Printer{Out: &out, Err: &errOut, Format: cli.FormatJSON}
	res := agentReplyResult{
		ID:              "a-1",
		InputID:         "retry-input-1",
		RequestID:       "req-1",
		statusRequestID: "req-1:schema-retry:1",
		Message:         messageDelivered,
		State:           "waiting_for_user",
		Replies:         []string{},
	}
	err := agentReplyEnded(p, res, replyWaitWaiting, time.Minute)
	var status *cli.ExitStatus
	require.ErrorAs(t, err, &status)
	assert.Equal(t, cli.ExitWaitEnded, status.Code)
	assert.Contains(t, out.String(), `"request_id":"req-1:schema-retry:1"`)
	assert.Contains(t, out.String(), `leapmux agent request-status a-1 req-1:schema-retry:1`)
	assert.Contains(t, errOut.String(), "disposition: in_progress (request_id: req-1:schema-retry:1)")
	assert.Contains(t, errOut.String(), "check status: leapmux agent request-status a-1 req-1:schema-retry:1")
}

func TestPersistentWorkerChannel_ReusesSingleChannelAndReconnectsOnDrop(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker(queuedInput(inputAlpha, "Run tests", 1))
	harness := startHubChannelHarness(t, &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:      &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: id},
			TileId:   "root-1",
			WorkerId: "worker-A", WorkspaceId: "ws-1",
		},
	}, w)

	client, err := control.NewClient(harness.url)
	require.NoError(t, err)
	ch := newPersistentWorkerChannel(client, "worker-A")
	defer ch.Close()
	reader := channelReplyReader(ch, id)

	ctx := testutil.DeadlineContext(t)
	for range 3 {
		resp, err := reader.readQueue(ctx, "")
		require.NoError(t, err)
		require.Len(t, resp.GetSnapshot().GetItems(), 1)
	}
	assert.Equal(t, 1, harness.channelOpens(), "multiple queue reads must share one E2EE channel")

	// Drop the relay websocket and confirm the next read reconnects once cleanly.
	harness.mu.Lock()
	for _, ws := range harness.relays {
		_ = ws.CloseNow()
	}
	harness.mu.Unlock()
	resp, err := reader.readQueue(ctx, "")
	require.NoError(t, err)
	require.Len(t, resp.GetSnapshot().GetItems(), 1)
	assert.Equal(t, 2, harness.channelOpens(), "dropped channel must trigger a single transparent reconnect")
}

func TestPersistentWorkerChannel_CallOnceDoesNotRetryOnDrop(t *testing.T) {
	id := fullTestID("ag-callonce")
	w := newQueueWorker(queuedInput(inputAlpha, "Run tests", 1))
	harness := startHubChannelHarness(t, &recordingHub{workers: workerRows("worker-A")}, w)

	client, err := control.NewClient(harness.url)
	require.NoError(t, err)
	ch := newPersistentWorkerChannel(client, "worker-A")
	defer ch.Close()

	ctx := testutil.DeadlineContext(t)
	var queueResp leapmuxv1.ListAgentInputQueueResponse
	require.NoError(t, ch.Call(ctx, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
		&leapmuxv1.ListAgentInputQueueRequest{AgentId: id}, &queueResp))
	assert.Equal(t, 1, harness.channelOpens())

	// Drop the relay websocket and invoke a mutating RPC via CallOnce.
	harness.mu.Lock()
	for _, ws := range harness.relays {
		_ = ws.CloseNow()
	}
	harness.mu.Unlock()

	var ack leapmuxv1.EnqueueAgentInputResponse
	err = ch.CallOnce(ctx, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure,
		&leapmuxv1.EnqueueAgentInputRequest{AgentId: id, InputId: "in-once", Text: "mutate"}, &ack)
	require.Error(t, err)
	assert.Equal(t, 1, harness.channelOpens(), "CallOnce must not reopen or retry a mutating RPC on transport failure")

	// The failed channel was cleared, so a later read opens a fresh channel cleanly.
	require.NoError(t, ch.Call(ctx, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure,
		&leapmuxv1.ListAgentInputQueueRequest{AgentId: id}, &queueResp))
	assert.Equal(t, 2, harness.channelOpens())

	// Closing the channel prevents any later Call or CallOnce from opening a new channel.
	ch.Close()
	err = ch.CallOnce(ctx, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure,
		&leapmuxv1.EnqueueAgentInputRequest{AgentId: id, InputId: "in-after-close", Text: "mutate"}, &ack)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 2, harness.channelOpens())
}

func TestAwaitReply_RejectsUnexpectedReplyStep(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	e := &replyEvaluator{
		spec: replyWaitSpec{agentID: "a-1", inputID: "in-1"},
		evalOverride: func(context.Context, leapmuxv1.AgentActivityState) (replyVerdict, error) {
			return replyVerdict{step: replyStep(99), message: messageDelivered}, nil
		},
	}
	_, _, err := awaitReply(ctx, testutil.NewQuartzMock(t), make(chan activityEvent), e, working)
	ce := cli.FromError(err)
	require.NotNil(t, ce)
	assert.Equal(t, cli.CodeInternal, ce.Code)
	assert.Contains(t, ce.Message, "unexpected reply evaluation step 99")
}

func TestAgentReplyResult_QuietLines(t *testing.T) {
	t.Run("default returns input_id for agent send --wait -q", func(t *testing.T) {
		res := agentReplyResult{ID: "ag-1", InputID: "in-1"}
		assert.Equal(t, []string{"in-1"}, res.QuietLines())
	})

	t.Run("falls back to agent id when input_id is empty", func(t *testing.T) {
		res := agentReplyResult{ID: "ag-1"}
		assert.Equal(t, []string{"ag-1"}, res.QuietLines())
	})

	t.Run("quietAgentID returns created agent id even when input_id is set", func(t *testing.T) {
		res := agentReplyResult{ID: "ag-created", InputID: "in-1", quietAgentID: true}
		assert.Equal(t, []string{"ag-created"}, res.QuietLines())
	})

	t.Run("agentReplyEnded preserves quietAgentID in quiet mode", func(t *testing.T) {
		var out, errOut bytes.Buffer
		p := &cli.Printer{Out: &out, Err: &errOut, Format: cli.FormatQuiet}
		res := agentReplyResult{
			ID:           "ag-created",
			InputID:      "in-1",
			RequestID:    "req-1",
			Message:      messageDelivered,
			State:        "waiting_for_user",
			Replies:      []string{},
			quietAgentID: true,
		}
		err := agentReplyEnded(p, res, replyWaitWaiting, time.Minute)
		var status *cli.ExitStatus
		require.ErrorAs(t, err, &status)
		assert.Equal(t, cli.ExitWaitEnded, status.Code)
		assert.Equal(t, "ag-created\n", out.String())
	})
}
