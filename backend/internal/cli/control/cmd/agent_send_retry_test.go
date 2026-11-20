package cmd

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// retryWorker is the activityWorker with two parts of the real Worker that a
// retried `agent send --wait` needs: an enqueue that adds nothing for an
// input id that the queue or the transcript already holds, and transcript
// pages of at most the requested limit, with has_more, in each direction.
type retryWorker struct {
	*activityWorker
	transcriptRequests []*leapmuxv1.ListAgentMessagesRequest
}

func (w *retryWorker) DispatchWith(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
		var in leapmuxv1.EnqueueAgentInputRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		w.mu.Lock()
		var acceptedSeq int64
		if index := slices.IndexFunc(w.rows, func(row *leapmuxv1.AgentChatMessage) bool { return row.GetId() == in.GetInputId() }); index >= 0 {
			acceptedSeq = w.rows[index].GetSeq()
		}
		held := slices.ContainsFunc(w.queue.GetItems(), func(item *leapmuxv1.QueuedAgentInput) bool { return item.GetId() == in.GetInputId() }) ||
			acceptedSeq > 0
		if in.GetRequireExisting() && !held {
			w.mu.Unlock()
			writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{ExistingInputMissing: true})
			return
		}
		if !held {
			w.queue.Items = append(w.queue.Items, &leapmuxv1.QueuedAgentInput{Id: in.GetInputId(), State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED})
		}
		snapshot := proto.CloneOf(w.queue)
		w.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{Snapshot: snapshot, AcceptedSeq: acceptedSeq})
		if !held {
			w.enqueued <- &in
		}
	case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
		var in leapmuxv1.ListAgentMessagesRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.mu.Lock()
		w.transcriptRequests = append(w.transcriptRequests, &in)
		resp := transcriptPage(w.rows, &in)
		w.mu.Unlock()
		writeProtoResponse(rw, resp)
	default:
		w.activityWorker.DispatchWith(ctx, caller, req, rw)
	}
}

// transcriptPage answers ListAgentMessages from rows, which hold ascending
// seqs, as the Worker pages them: at most the limit, in ascending seq, with
// has_more for more rows in the direction of the anchor.
func transcriptPage(rows []*leapmuxv1.AgentChatMessage, in *leapmuxv1.ListAgentMessagesRequest) *leapmuxv1.ListAgentMessagesResponse {
	limit := int(in.GetLimit())
	if limit <= 0 {
		limit = len(rows)
	}
	var page []*leapmuxv1.AgentChatMessage
	hasMore := false
	switch in.GetAnchor() {
	case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE:
		older := slices.DeleteFunc(slices.Clone(rows), func(row *leapmuxv1.AgentChatMessage) bool { return row.GetSeq() >= in.GetCursorSeq() })
		start := max(0, len(older)-limit)
		page, hasMore = older[start:], start > 0
	case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER:
		newer := slices.DeleteFunc(slices.Clone(rows), func(row *leapmuxv1.AgentChatMessage) bool { return row.GetSeq() <= in.GetCursorSeq() })
		end := min(len(newer), limit)
		page, hasMore = newer[:end], end < len(newer)
	case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST:
		end := min(len(rows), limit)
		page, hasMore = rows[:end], end < len(rows)
	default:
		start := max(0, len(rows)-limit)
		page, hasMore = rows[start:], start > 0
	}
	var latest int64
	if n := len(rows); n > 0 {
		latest = rows[n-1].GetSeq()
	}
	resp := &leapmuxv1.ListAgentMessagesResponse{HasMore: hasMore, LatestSeq: &latest}
	for _, row := range page {
		out := proto.CloneOf(row)
		if !in.GetIncludeReplyText() {
			out.ReplyText = nil
		}
		resp.Messages = append(resp.Messages, out)
	}
	return resp
}

// A retry of `agent send --wait --request-id` after the first run's message
// reached the agent queues nothing, and it prints the reply to the message
// of the first run. That message lies more than two pages back, behind the
// replies that followed it.
func TestAgentSendWaitOverIPC_ARetryPrintsTheReplyOfTheFirstRun(t *testing.T) {
	id := fullTestID("ag-2")
	inputID := requestScopedID("tests-1", requestScopeAgentSend, id)
	w := &retryWorker{activityWorker: newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})}
	w.rows = []*leapmuxv1.AgentChatMessage{replyRow(1, "An answer to an earlier message."), userMessageRow(2, inputID)}
	for seq := int64(3); seq <= 122; seq++ {
		w.rows = append(w.rows, replyRow(seq, fmt.Sprintf("Step %d.", seq)))
	}
	last := "All 12 tests pass."
	w.rows[len(w.rows)-1].ReplyText = &last
	startAgentWorker(t, id, w)

	data := runControl(t, "agent", "send", id, "Run the tests", "--wait", "--request-id", "tests-1").Data(t)
	assert.Equal(t, inputID, data["input_id"])
	assert.Equal(t, "delivered", data["message"])
	assert.Equal(t, "idle", data["state"])
	assert.Equal(t, []any{"All 12 tests pass."}, data["replies"])
	assert.Empty(t, w.enqueued, "the retry queued no second message")
	w.mu.Lock()
	defer w.mu.Unlock()
	var firstReplyRead *leapmuxv1.ListAgentMessagesRequest
	for _, request := range w.transcriptRequests {
		assert.NotEqual(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE, request.GetAnchor(),
			"the Worker acknowledges the accepted input by its exact sequence")
		if request.GetIncludeReplyText() && firstReplyRead == nil {
			firstReplyRead = request
		}
	}
	require.NotNil(t, firstReplyRead)
	assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, firstReplyRead.GetAnchor())
	assert.EqualValues(t, 1, firstReplyRead.GetCursorSeq())
}

// A retry while the first run's message still waits in the queue waits for
// that message, as the first run did.
func TestAgentSendWaitOverIPC_ARetryWaitsForTheQueuedMessage(t *testing.T) {
	id := fullTestID("ag-2")
	inputID := requestScopedID("tests-1", requestScopeAgentSend, id)
	w := &retryWorker{activityWorker: newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: working})}
	w.queue = &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: true, Items: []*leapmuxv1.QueuedAgentInput{{Id: inputID, State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}}}
	startAgentWorker(t, id, w)
	ctx := testutil.DeadlineContext(t)
	go func() {
		receive(t, ctx, w.queueRead)
		w.deliver(inputID, false, replyRow(0, "All 12 tests pass."))
		w.setState(id, idle)
	}()

	data := runControl(t, "agent", "send", id, "Run the tests", "--wait", "--request-id", "tests-1").Data(t)
	assert.Equal(t, inputID, data["input_id"])
	assert.Equal(t, []any{"All 12 tests pass."}, data["replies"])
	assert.Empty(t, w.enqueued, "the retry queued no second message")
}

func TestAgentSendWaitOverIPC_ARecognizedRetryReportsDeliveryWhileWaiting(t *testing.T) {
	id := fullTestID("ag-2")
	inputID := requestScopedID("tests-1", requestScopeAgentSend, id)
	w := &retryWorker{activityWorker: newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER})}
	w.queue = &leapmuxv1.AgentInputQueueSnapshot{ActiveTurn: true}
	w.rows = []*leapmuxv1.AgentChatMessage{userMessageRow(1, inputID)}
	startAgentWorker(t, id, w)

	out := runControl(t, "agent", "send", id, "Run the tests", "--wait", "--request-id", "tests-1")
	require.Equal(t, cli.ExitWaitEnded, out.Code, out.Stderr)
	data := envelopeData(t, []byte(out.Stdout))
	assert.Equal(t, inputID, data["input_id"])
	assert.Equal(t, "delivered", data["message"])
	assert.Equal(t, "waiting_for_user", data["state"])
	assert.Empty(t, w.enqueued)
}

func TestAgentSendWaitOverIPC_AFreshKeyDoesNotQueueBehindAPrompt(t *testing.T) {
	id := fullTestID("ag-2")
	w := &retryWorker{activityWorker: newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER})}
	startAgentWorker(t, id, w)

	out := runControl(t, "agent", "send", id, "Run the tests", "--wait", "--request-id", "unused-key")
	out.requireFailure(t, cli.CodeAgentWaiting)
	assert.Empty(t, w.enqueued)
}

// A retry after someone removed the first run's message from the queue queues
// nothing, so no reply can come. The command says so instead of waiting, and
// it ends as the first run ends when the removal happens while it waits: with
// not_found and exit status 6.
func TestAgentSendWaitOverIPC_ARetryOfARemovedMessageFails(t *testing.T) {
	id := fullTestID("ag-2")
	inputID := requestScopedID("tests-1", requestScopeAgentSend, id)
	w := &retryWorker{activityWorker: newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: idle})}
	w.queue = &leapmuxv1.AgentInputQueueSnapshot{}
	w.rows = []*leapmuxv1.AgentChatMessage{replyRow(1, "An answer to an earlier message.")}
	startAgentWorker(t, id, &removedInputWorker{w})

	o := runControl(t, "agent", "send", id, "Run the tests", "--wait", "--request-id", "tests-1")
	f := o.Failure(t)
	assert.Equal(t, cli.CodeNotFound, f.Code)
	assert.Contains(t, f.Message, "someone removed the message "+inputID)
	assert.Contains(t, f.Message, "this retry queued nothing")
	require.NotEmpty(t, f.Hints)
	assert.Contains(t, f.Hints[0], "new --request-id")
	assert.Equal(t, cli.ExitWaitEnded, o.Code, "the wait ended without a reply, so the command exits 6")
}

func TestAgentSendWaitOverIPC_ARemovedRetryReportsRemovalWhileWaiting(t *testing.T) {
	id := fullTestID("ag-2")
	inputID := requestScopedID("tests-1", requestScopeAgentSend, id)
	w := &retryWorker{activityWorker: newActivityWorker(map[string]leapmuxv1.AgentActivityState{id: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER})}
	startAgentWorker(t, id, &removedInputWorker{w})

	out := runControl(t, "agent", "send", id, "Run the tests", "--wait", "--request-id", "tests-1")
	failure := out.Failure(t)
	assert.Equal(t, cli.CodeNotFound, failure.Code)
	assert.Contains(t, failure.Message, "someone removed the message "+inputID)
	assert.Equal(t, cli.ExitWaitEnded, out.Code)
}

// removedInputWorker answers an enqueue as the Worker answers a repeat of an
// input that someone removed from the queue: with the queue, which does not
// hold it. The Worker writes no transcript row of a removed input either.
type removedInputWorker struct {
	*retryWorker
}

func (w *removedInputWorker) DispatchWith(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() == leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure {
		writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{Snapshot: &leapmuxv1.AgentInputQueueSnapshot{}})
		return
	}
	w.retryWorker.DispatchWith(ctx, caller, req, rw)
}
